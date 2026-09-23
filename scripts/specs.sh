#!/usr/bin/env bash
# scripts/specs.sh - the single glyphrun spec runner for local dev, CI and
# release. `task specs`, .github/workflows/ci.yml and
# .github/workflows/release.yml all call this script instead of keeping
# their own copy of the loop, so the skip-list and the glyph invocation stay
# in exactly one place.
#
# This script builds nothing itself: it expects ./bin/monitor to already
# exist (see `task build`).
#
# Usage:
#   scripts/specs.sh                # run every specs/*.yml
#   scripts/specs.sh specs/foo.yml specs/bar.yml
#                                    # run only the named specs (skip-list
#                                    # rules below still apply)
#
# Env:
#   GLYPH               - the glyph command to invoke (default: glyph)
#   MONITOR_SPECS_SKIP  - extra spec basenames to skip, space-separated,
#                         e.g. MONITOR_SPECS_SKIP="a.yml b.yml"
#
# For each spec this prints one of:
#   PASS <spec>
#   SKIP <spec> (<reason>)
#   FAIL <spec> (<status>)
# and exits non-zero if any spec FAILed (SKIPs never fail the run).
set -u

GLYPH="${GLYPH:-glyph}"

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root" || exit 1

if ! command -v "$GLYPH" >/dev/null 2>&1; then
  echo "scripts/specs.sh: '$GLYPH' not found on PATH (set GLYPH=/path/to/glyph)" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "scripts/specs.sh: 'jq' not found on PATH" >&2
  exit 1
fi

if [ "$#" -gt 0 ]; then
  specs=("$@")
else
  specs=(specs/*.yml)
fi

# Space-padded so a substring match below can't false-positive on a prefix,
# e.g. MONITOR_SPECS_SKIP="run.yml" must not also match "run_extra.yml".
extra_skip=" ${MONITOR_SPECS_SKIP:-} "

is_extra_skip() {
  case "$extra_skip" in
  *" $1 "*) return 0 ;;
  *) return 1 ;;
  esac
}

fail=0
tmp_out="$(mktemp)"
trap 'rm -f "$tmp_out"' EXIT

for spec in "${specs[@]}"; do
  base="$(basename "$spec")"
  reason=""

  # Static skip-list: a spec needs a tool or platform this run doesn't have.
  # Kept here (not duplicated in ci.yml/release.yml) so local, CI and release
  # always agree on what's skipped and why.
  case "$base" in
  stash.yml)
    if ! command -v fcheap >/dev/null 2>&1; then
      reason="needs fcheap"
    fi
    ;;
  profile_sample.yml)
    if [ "$(uname -s)" != "Darwin" ]; then
      reason="needs macOS sample (darwin-only)"
    fi
    ;;
  esac

  if [ -z "$reason" ] && is_extra_skip "$base"; then
    reason="skipped via MONITOR_SPECS_SKIP"
  fi

  if [ -n "$reason" ]; then
    echo "SKIP $spec ($reason)"
    continue
  fi

  if ! "$GLYPH" run "$spec" --format json >"$tmp_out" 2>&1; then
    echo "FAIL $spec (glyph exited non-zero)"
    sed 's/^/    /' "$tmp_out"
    fail=1
    continue
  fi

  status="$(jq -r '.status // empty' <"$tmp_out" 2>/dev/null)"
  if [ "$status" = "passed" ]; then
    echo "PASS $spec"
  else
    echo "FAIL $spec (${status:-invalid glyph output})"
    fail=1
  fi
done

exit "$fail"
