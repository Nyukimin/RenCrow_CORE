#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/../.." && pwd)"
INSTALLER="${REPO_DIR}/install.sh"
DROPIN_DIR="${REPO_DIR}/systemd/user/rencrow.service.d"
DOC="${REPO_DIR}/docs/09_運用ログ・panic保存仕様.md"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

contains() {
  local needle="$1"
  local file="$2"
  grep -Fq -- "$needle" "$file" || fail "${file} does not contain: ${needle}"
}

not_contains() {
  local needle="$1"
  local file="$2"
  if grep -Fq -- "$needle" "$file"; then
    fail "${file} unexpectedly contains: ${needle}"
  fi
}

[[ -f "${INSTALLER}" ]] || fail "installer is missing"
[[ -f "${DOC}" ]] || fail "operations document is missing"

# The production installer is intentionally not executed. Keep its repo source
# allowlist explicit and limited to the two portable CORE-owned drop-ins.
dropin_sources="$(grep -Eo 'systemd/user/rencrow\.service\.d/[A-Za-z0-9.-]+\.conf' "${INSTALLER}" | sort -u)"
expected_sources=$'systemd/user/rencrow.service.d/10-panic-stack.conf\nsystemd/user/rencrow.service.d/20-resilience.conf'
[[ "${dropin_sources}" == "${expected_sources}" ]] || fail "unexpected repo drop-in sources: ${dropin_sources}"

for legacy in \
  30-games-observer.conf \
  40-codex-path.conf \
  50-movie-catalog.conf \
  60-person-related-catalog.conf \
  70-trade.conf; do
  contains "${legacy}" "${INSTALLER}"
done
contains 'for legacy_dropin in' "${INSTALLER}"
contains 'rm -f -- "${SYSTEMD_USER_DIR}/rencrow.service.d/${legacy_dropin}"' "${INSTALLER}"

# Removal must be an exact-name allowlist. Wildcards, recursive deletes, and
# find-based cleanup would erase host-owned unknown drop-ins.
not_contains 'rm -rf' "${INSTALLER}"
not_contains 'rencrow.service.d/*' "${INSTALLER}"
not_contains 'find "${SYSTEMD_USER_DIR}/rencrow.service.d"' "${INSTALLER}"

daemon_reload_line="$(grep -nF 'systemctl --user daemon-reload' "${INSTALLER}" | cut -d: -f1 | head -n1)"
removal_line="$(grep -nF 'rm -f -- "${SYSTEMD_USER_DIR}/rencrow.service.d/${legacy_dropin}"' "${INSTALLER}" | cut -d: -f1 | head -n1)"
[[ -n "${daemon_reload_line}" && -n "${removal_line}" && ${removal_line} -lt ${daemon_reload_line} ]] || fail "legacy cleanup must precede daemon-reload"
not_contains 'systemctl --user restart rencrow' "${INSTALLER}"

repo_dropins="$(find "${DROPIN_DIR}" -maxdepth 1 -type f -exec basename {} \; | sort)"
[[ "${repo_dropins}" == $'10-panic-stack.conf\n20-resilience.conf' ]] || fail "repo drop-in set is not portable allowlist: ${repo_dropins}"

# CORE owns these values in live core.yaml; service drop-ins must not project
# backend endpoints or host-specific catalog URLs.
if grep -R -E 'Environment=RENCROW_(GAMES_OBSERVER_URL|MOVIE_CATALOG_CRAWLER_URL|PERSON_RELATED_CATALOG_PROVIDER_URL)' \
  "${REPO_DIR}/systemd/user/rencrow.service" "${DROPIN_DIR}" >/dev/null; then
  fail "backend endpoint Environment remains in CORE unit/drop-ins"
fi

contains 'live `core.yaml` owns `games.observer_url`, `movie_catalog.crawler_url`, and `person_related_catalog.provider_url`' "${DOC}"
contains '`codex.command` owns the executable absolute path' "${DOC}"
contains 'trade section owns its API endpoint; the operator manages the optional service lifecycle independently' "${DOC}"

echo "PASS: CORE systemd drop-in contract"
