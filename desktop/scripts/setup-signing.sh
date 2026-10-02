#!/bin/sh
# One-time signing + notarization setup for Monitor Desktop.
#
#   scripts/setup-signing.sh                  local: store notarization credentials
#                                             in the login keychain (notarytool
#                                             profile "monitor-desktop"), so
#                                             `bun run dist:notarized` signs AND
#                                             notarizes on this Mac.
#   scripts/setup-signing.sh --github CERT.p12
#                                             CI: set the GitHub Actions secrets
#                                             desktop.yml reads for desktop-v* tags.
#
# Secrets are read with echo off and handed to notarytool / `gh secret set`
# on stdin — never placed in argv, env files or the shell history.
#
# Before running it, create an app-specific password for your Apple ID at
# https://account.apple.com (Sign-In and Security → App-Specific Passwords).
set -eu

PROFILE="monitor-desktop"
REPO="abdul-hamid-achik/monitor"

identity=$(security find-identity -v -p codesigning | sed -n 's/.*"\(Developer ID Application: .*\)"/\1/p' | head -1)
if [ -z "$identity" ]; then
  echo "No 'Developer ID Application' certificate in your keychain." >&2
  echo "Create one at https://developer.apple.com/account/resources/certificates and install it." >&2
  exit 1
fi
team_id=$(printf '%s' "$identity" | sed -n 's/.*(\([A-Z0-9]\{10\}\))$/\1/p')
echo "Signing identity: $identity"
echo "Team ID:          $team_id"

read_secret() {
  # $1 = prompt; prints the value on stdout.
  printf '%s' "$1" >&2
  stty -echo
  IFS= read -r value
  stty echo
  printf '\n' >&2
  printf '%s' "$value"
}

printf 'Apple ID (email): ' >&2
IFS= read -r apple_id

if [ "${1:-}" != "--github" ]; then
  echo "Storing notarization credentials as keychain profile '$PROFILE'."
  echo "notarytool will ask for the app-specific password:"
  xcrun notarytool store-credentials "$PROFILE" --apple-id "$apple_id" --team-id "$team_id"
  echo
  echo "Done. Build, sign and notarize with:"
  echo "  cd desktop && bun run dist:notarized"
  exit 0
fi

cert="${2:-}"
if [ -z "$cert" ] || [ ! -f "$cert" ]; then
  echo "usage: $0 --github path/to/DeveloperID.p12" >&2
  echo "Export it from Keychain Access: right-click '$identity' → Export… → .p12" >&2
  exit 2
fi
command -v gh >/dev/null || { echo "gh (GitHub CLI) is required" >&2; exit 1; }

p12_password=$(read_secret "Password you gave the .p12 export: ")
app_password=$(read_secret "App-specific password for $apple_id: ")

set_secret() {
  printf '%s' "$2" | gh secret set "$1" --repo "$REPO" >/dev/null
  echo "  set $1"
}
echo "Setting GitHub Actions secrets on $REPO:"
set_secret MACOS_CERTIFICATE_P12_BASE64 "$(base64 < "$cert" | tr -d '\n')"
set_secret MACOS_CERTIFICATE_PASSWORD "$p12_password"
set_secret APPLE_ID "$apple_id"
set_secret APPLE_APP_SPECIFIC_PASSWORD "$app_password"
set_secret APPLE_TEAM_ID "$team_id"
echo "Done. Delete the exported .p12 now that GitHub has it: rm '$cert'"
echo "A tag like desktop-v0.1.0 then builds, signs, notarizes and drafts a release."
