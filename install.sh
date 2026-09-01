#!/usr/bin/env bash
set -euo pipefail

RENCROW_HOME="${HOME}/.rencrow"
RENCROW_CONFIG_DIR="${RENCROW_HOME}/config"
RENCROW_BIN="${HOME}/.local/bin"
SYSTEMD_USER_DIR="${XDG_CONFIG_HOME:-${HOME}/.config}/systemd/user"
SYSTEMD_USER_DATA_DIR="${XDG_DATA_HOME:-${HOME}/.local/share}/systemd/user"
RENCROW_SHARE_DIR="${HOME}/.local/share/rencrow"
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "RenCrow_CORE installer"

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required." >&2
  exit 1
fi

legacy_core_unit="${SYSTEMD_USER_DIR}/rencrow.service"
legacy_core_unit_present=false
if [[ -e "${legacy_core_unit}" || -L "${legacy_core_unit}" ]]; then
  if [[ -L "${legacy_core_unit}" || ! -f "${legacy_core_unit}" ]] ||
    ! cmp -s "${REPO_DIR}/systemd/user/rencrow.service" "${legacy_core_unit}"; then
    echo "Refusing to replace operator-owned CORE unit: ${legacy_core_unit}" >&2
    exit 1
  fi
  legacy_core_unit_present=true
fi

mkdir -p "${RENCROW_HOME}/logs" "${RENCROW_HOME}/data/sessions" "${RENCROW_CONFIG_DIR}"
mkdir -p "${RENCROW_BIN}" "${SYSTEMD_USER_DIR}" "${SYSTEMD_USER_DATA_DIR}" "${RENCROW_SHARE_DIR}/scripts"
mkdir -p "${RENCROW_SHARE_DIR}/prompts"

cd "${REPO_DIR}"
go build -o rencrow ./cmd/rencrow
install -m 0755 rencrow "${RENCROW_BIN}/rencrow"
cp -R prompts/. "${RENCROW_SHARE_DIR}/prompts/"

if [ ! -f "${RENCROW_CONFIG_DIR}/core.yaml" ]; then
  if [ -f "${RENCROW_HOME}/config.yaml" ]; then
    install -m 0600 "${RENCROW_HOME}/config.yaml" "${RENCROW_CONFIG_DIR}/core.yaml"
    echo "Migrated existing CORE config to ${RENCROW_CONFIG_DIR}/core.yaml (legacy source retained)."
  else
    install -m 0600 config/config.yaml.example "${RENCROW_CONFIG_DIR}/core.yaml"
    sed -i "s|./data/sessions|${RENCROW_HOME}/data/sessions|g" "${RENCROW_CONFIG_DIR}/core.yaml"
    sed -i "s|./workspace|${RENCROW_HOME}/workspace|g" "${RENCROW_CONFIG_DIR}/core.yaml"
  fi
fi

if [ ! -f "${RENCROW_HOME}/.env" ]; then
  cat > "${RENCROW_HOME}/.env" <<'EOF'
# Optional RenCrow_LLM Gateway credential.
RENCROW_LLM_API_KEY=
EOF
  chmod 600 "${RENCROW_HOME}/.env"
fi

install -m 0644 "systemd/user/rencrow.service" "${SYSTEMD_USER_DATA_DIR}/rencrow.service"
install -m 0755 scripts/rencrow_log_rotate.sh "${RENCROW_SHARE_DIR}/scripts/rencrow_log_rotate.sh"
install -m 0644 systemd/user/rencrow-log-rotate.service "${SYSTEMD_USER_DIR}/rencrow-log-rotate.service"
install -m 0644 systemd/user/rencrow-log-rotate.timer "${SYSTEMD_USER_DIR}/rencrow-log-rotate.timer"

mkdir -p "${SYSTEMD_USER_DIR}/rencrow.service.d"
install -m 0644 systemd/user/rencrow.service.d/10-panic-stack.conf \
  "${SYSTEMD_USER_DIR}/rencrow.service.d/10-panic-stack.conf"
install -m 0644 systemd/user/rencrow.service.d/20-resilience.conf \
  "${SYSTEMD_USER_DIR}/rencrow.service.d/20-resilience.conf"

# These five names were previously host-only projections. Remove only the
# known legacy files after live core.yaml becomes authoritative; preserve any
# other operator-owned drop-ins.
for legacy_dropin in \
  30-games-observer.conf \
  40-codex-path.conf \
  50-movie-catalog.conf \
  60-person-related-catalog.conf \
  70-trade.conf; do
  rm -f -- "${SYSTEMD_USER_DIR}/rencrow.service.d/${legacy_dropin}"
done

# A base unit in XDG_CONFIG_HOME outranks systemctl's runtime mask in
# XDG_RUNTIME_DIR. Remove only the exact legacy CORE-owned copy after the
# canonical unit is installed in XDG_DATA_HOME; preserve all config drop-ins.
if [[ -f "${SYSTEMD_USER_DIR}/rencrow.service" && ! -L "${SYSTEMD_USER_DIR}/rencrow.service" ]] &&
  cmp -s "systemd/user/rencrow.service" "${SYSTEMD_USER_DIR}/rencrow.service"; then
  rm -f -- "${SYSTEMD_USER_DIR}/rencrow.service"
fi

sed "s#@RENCROW_REPO_DIR@#${REPO_DIR}#g" \
  systemd/user/rencrow-resilience.service \
  > "${SYSTEMD_USER_DIR}/rencrow-resilience.service"
install -m 0644 systemd/user/rencrow-resilience.timer \
  "${SYSTEMD_USER_DIR}/rencrow-resilience.timer"

systemctl --user daemon-reload
if ! systemctl --user reenable rencrow.service; then
  if [[ ${legacy_core_unit_present} == true ]]; then
    install -m 0644 "systemd/user/rencrow.service" "${legacy_core_unit}"
  fi
  systemctl --user daemon-reload
  if [[ ${legacy_core_unit_present} == true ]]; then
    systemctl --user reenable rencrow.service || true
  fi
  echo "Failed to enable CORE from ${SYSTEMD_USER_DATA_DIR}; restored the legacy unit." >&2
  exit 1
fi
systemctl --user enable --now rencrow-log-rotate.timer
systemctl --user enable --now rencrow-resilience.timer

echo "Installed RenCrow_CORE."
echo "Configure llm_gateway in ${RENCROW_CONFIG_DIR}/core.yaml for RenCrow_LLM."
echo "Start: systemctl --user start rencrow"
echo "Logs:  journalctl --user -u rencrow -f"
echo "LINE webhook: https://<current-host>/webhook/line"
