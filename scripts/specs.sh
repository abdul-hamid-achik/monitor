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
#   FAIL <spec> (<status>[: <diagnostic>])
# and exits non-zero if any spec FAILed (SKIPs never fail the run).
#
# glyph's own diagnostics/progress are written to stderr on purpose (its
# configureLogger routes them there; GLYPHRUN_PROGRESS=always makes that
# noisier still) — only its `--format json` stdout is the parseable result.
# stdout and stderr are therefore captured to two separate files below;
# merging them would let a stray progress/log line break the JSON parse and
# turn a real PASS into a false "invalid glyph output" FAIL.
set -u

# LC_ALL=C before the glob below expands, so `specs/*.yml` sorts the same
# way regardless of the shell's locale. Without this, a run under
# en_US.UTF-8 (a typical local shell) and one under C/C.UTF-8 (a typical CI
# image) can list the same spec files in a different order, which breaks
# "local and CI print the same list" for no reason other than locale.
export LC_ALL=C

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

if [ ! -x ./bin/monitor ]; then
  echo "scripts/specs.sh: ./bin/monitor not found or not executable (run 'task build', or 'go build -o bin/monitor ./cmd/monitor', first)" >&2
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
tmp_err="$(mktemp)"
trap 'rm -f "$tmp_out" "$tmp_err"' EXIT

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

  : >"$tmp_out"
  : >"$tmp_err"
  "$GLYPH" run "$spec" --format json >"$tmp_out" 2>"$tmp_err"
  glyph_exit=$?

  status="$(jq -r '.status // empty' <"$tmp_out" 2>/dev/null)"

  if [ "$glyph_exit" -eq 0 ] && [ "$status" = "passed" ]; then
    echo "PASS $spec"
    continue
  fi

  if [ -z "$status" ]; then
    status="glyph exited $glyph_exit with no parseable status"
  fi

  # glyph reports a single top-level `.diagnostic` for a run that never got
  # to evaluate any outcome (status "errored", e.g. a missing precondition).
  # A run that failed an outcome instead carries its detail per-outcome, so
  # fall back to the first non-passed outcome's message — this way FAIL is
  # never printed bare in either case.
  detail="$(jq -r '
      .diagnostic
      // ((.outcomes // []) | map(select(.status != "passed")) | .[0].message)
      // empty
    ' <"$tmp_out" 2>/dev/null)"

  if [ -n "$detail" ]; then
    echo "FAIL $spec ($status: $detail)"
  else
    echo "FAIL $spec ($status)"
  fi

  run_dir="$(jq -r '.runDir // empty' <"$tmp_out" 2>/dev/null)"
  if [ -n "$run_dir" ]; then
    echo "    run dir: $run_dir"
  fi
  if [ -s "$tmp_err" ]; then
    echo "    glyph stderr:"
    sed 's/^/      /' "$tmp_err"
  fi

  fail=1
done

exit "$fail"
