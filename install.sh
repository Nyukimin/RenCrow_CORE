#!/usr/bin/env bash
set -euo pipefail

RENCROW_HOME="${HOME}/.rencrow"
RENCROW_CONFIG_DIR="${RENCROW_HOME}/config"
RENCROW_BIN="${HOME}/.local/bin"
SYSTEMD_USER_DIR="${HOME}/.config/systemd/user"
RENCROW_SHARE_DIR="${HOME}/.local/share/rencrow"
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"

echo "RenCrow_CORE installer"

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required." >&2
  exit 1
fi

mkdir -p "${RENCROW_HOME}/logs" "${RENCROW_HOME}/data/sessions" "${RENCROW_CONFIG_DIR}"
mkdir -p "${RENCROW_BIN}" "${SYSTEMD_USER_DIR}" "${RENCROW_SHARE_DIR}/scripts"
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

install -m 0644 "systemd/user/rencrow.service" "${SYSTEMD_USER_DIR}/rencrow.service"
install -m 0755 scripts/rencrow_log_rotate.sh "${RENCROW_SHARE_DIR}/scripts/rencrow_log_rotate.sh"
install -m 0644 systemd/user/rencrow-log-rotate.service "${SYSTEMD_USER_DIR}/rencrow-log-rotate.service"
install -m 0644 systemd/user/rencrow-log-rotate.timer "${SYSTEMD_USER_DIR}/rencrow-log-rotate.timer"

mkdir -p "${SYSTEMD_USER_DIR}/rencrow.service.d"
install -m 0644 systemd/user/rencrow.service.d/10-panic-stack.conf \
  "${SYSTEMD_USER_DIR}/rencrow.service.d/10-panic-stack.conf"
install -m 0644 systemd/user/rencrow.service.d/20-resilience.conf \
  "${SYSTEMD_USER_DIR}/rencrow.service.d/20-resilience.conf"
install -m 0644 systemd/user/rencrow.service.d/30-games-observer.conf \
  "${SYSTEMD_USER_DIR}/rencrow.service.d/30-games-observer.conf"

sed "s#@RENCROW_REPO_DIR@#${REPO_DIR}#g" \
  systemd/user/rencrow-resilience.service \
  > "${SYSTEMD_USER_DIR}/rencrow-resilience.service"
install -m 0644 systemd/user/rencrow-resilience.timer \
  "${SYSTEMD_USER_DIR}/rencrow-resilience.timer"

systemctl --user daemon-reload
systemctl --user enable rencrow
systemctl --user enable --now rencrow-log-rotate.timer
systemctl --user enable --now rencrow-resilience.timer

echo "Installed RenCrow_CORE."
echo "Configure llm_gateway in ${RENCROW_CONFIG_DIR}/core.yaml for RenCrow_LLM."
echo "Start: systemctl --user start rencrow"
echo "Logs:  journalctl --user -u rencrow -f"
echo "LINE webhook: https://<current-host>/webhook/line"
