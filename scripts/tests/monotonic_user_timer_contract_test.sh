#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

assert_line() {
  local unit="$1"
  local expected="$2"
  sed 's/\r$//' "$unit" | grep -Fqx "$expected" \
    || fail "${unit##*/} is missing '${expected}'"
}

assert_absent_line_prefix() {
  local unit="$1"
  local prefix="$2"
  if sed 's/\r$//' "$unit" | grep -Eq "^${prefix}"; then
    fail "${unit##*/} must not contain a ${prefix} timer directive"
  fi
}

assert_timer_contract() {
  local unit="$1"
  local startup="$2"
  local active="$3"
  local accuracy="$4"
  local service="$5"

  [[ -f "$unit" ]] || fail "timer unit is missing: $unit"
  assert_line "$unit" "$startup"
  assert_line "$unit" "$active"
  assert_line "$unit" "$accuracy"
  assert_line "$unit" "Unit=${service}"
  assert_absent_line_prefix "$unit" "OnBootSec="
  assert_absent_line_prefix "$unit" "Persistent="
}

assert_timer_contract \
  "${ROOT_DIR}/systemd/user/rencrow-resilience.timer" \
  "OnStartupSec=3min" \
  "OnUnitActiveSec=2min" \
  "AccuracySec=5s" \
  "rencrow-resilience.service"

echo "PASS: monotonic user timer contracts"
