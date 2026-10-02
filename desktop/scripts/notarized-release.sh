#!/bin/sh
# Universal release build, signed with the keychain's Developer ID and
# notarized with whatever credentials tvault injected (run it as
# `tvault run -p monitor-desktop -- sh scripts/notarized-release.sh`, which is
# what `bun run dist:notarized:tvault` does).
#
# Prefers an App Store Connect API key (APPLE_API_KEY_P8 + APPLE_API_KEY_ID +
# APPLE_API_ISSUER: scoped to App Store Connect, the option Apple and
# electron-builder recommend) over an Apple ID app-specific password. The .p8
# is written to a private temp directory for the build only and removed on
# exit; nothing is echoed.
set -eu
cd "$(dirname "$0")/.."

if [ -n "${APPLE_API_KEY_P8:-}" ] && [ -n "${APPLE_API_KEY_ID:-}" ] && [ -n "${APPLE_API_ISSUER:-}" ]; then
  umask 077
  keydir=$(mktemp -d)
  trap 'rm -rf "$keydir"' EXIT INT TERM
  printf '%s\n' "$APPLE_API_KEY_P8" > "$keydir/AuthKey_${APPLE_API_KEY_ID}.p8"
  export APPLE_API_KEY="$keydir/AuthKey_${APPLE_API_KEY_ID}.p8"
  # One method at a time: electron-builder picks the first complete set.
  unset APPLE_ID APPLE_APP_SPECIFIC_PASSWORD APPLE_API_KEY_P8
  echo "notarizing with the App Store Connect API key ${APPLE_API_KEY_ID}"
elif [ -n "${APPLE_ID:-}" ] && [ -n "${APPLE_APP_SPECIFIC_PASSWORD:-}" ] && [ -n "${APPLE_TEAM_ID:-}" ]; then
  echo "notarizing with the Apple ID app-specific password"
else
  echo "no notarization credentials in the environment: run through tvault (see desktop/README.md)" >&2
  exit 1
fi

bun run dist:release
