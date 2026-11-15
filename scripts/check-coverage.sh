#!/usr/bin/env bash
# Checks every sensitive package in coverage-floor.txt against its floor.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
floors="$root/coverage-floor.txt"

if [ ! -f "$floors" ]; then
  echo "no coverage floor at $floors"
  exit 1
fi

packages=()
while read -r pkg floor; do
  case "$pkg" in ''|'#'*) continue ;; esac
  packages+=("./$pkg")
done < "$floors"

if [ "${#packages[@]}" -eq 0 ]; then
  echo "coverage-floor.txt names no package"
  exit 0
fi

# The numbers come from the same run that would fail on a broken test, so a
# package cannot report a coverage its tests did not produce.
if ! report="$(cd "$root" && go test -cover "${packages[@]}" 2>&1)"; then
  printf '%s\n' "$report"
  echo "go test -cover failed"
  exit 1
fi

status=0
while read -r pkg floor; do
  case "$pkg" in ''|'#'*) continue ;; esac

  line="$(printf '%s\n' "$report" | grep -E "^ok[[:space:]]+github\.com/f1shsmell/flotilla/$pkg[[:space:]]" || true)"
  got="$(printf '%s' "$line" | sed -n 's/.*coverage: \([0-9.]*\)%.*/\1/p')"

  if [ -z "$got" ]; then
    echo "FAIL  $pkg  no coverage reported"
    status=1
    continue
  fi

  if awk "BEGIN { exit !($got < $floor) }"; then
    echo "FAIL  $pkg  $got% < floor $floor%"
    status=1
  else
    echo "ok    $pkg  $got% >= floor $floor%"
  fi
done < "$floors"

exit "$status"
