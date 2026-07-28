#!/usr/bin/env bash
set -euo pipefail

RENCROW_HOME="${HOME}/.rencrow"
RENCROW_BIN="${HOME}/.local/bin"
SYSTEMD_USER_DIR="${HOME}/.config/systemd/user"
REPO_DIR="$(cd "$(dirname "$0")" && pwd)"

if [ "$#" -ne 1 ]; then
  echo "Usage: $0 <worker|coder1|coder2|coder3|coder4>" >&2
  exit 1
fi

AGENT_TYPE="$1"
case "${AGENT_TYPE}" in
  worker|coder1|coder2|coder3|coder4) ;;
  *)
    echo "Unsupported agent type: ${AGENT_TYPE}" >&2
    exit 1
    ;;
esac

if ! command -v go >/dev/null 2>&1; then
  echo "Go is required." >&2
  exit 1
fi

mkdir -p "${RENCROW_HOME}/logs" "${RENCROW_HOME}/workspace"
mkdir -p "${RENCROW_BIN}" "${SYSTEMD_USER_DIR}"

cd "${REPO_DIR}"
go build -o rencrow-agent ./cmd/rencrow-agent
install -m 0755 rencrow-agent "${RENCROW_BIN}/rencrow-agent"

if [ ! -f "${RENCROW_HOME}/config.yaml" ]; then
  cp config/config.yaml.example "${RENCROW_HOME}/config.yaml"
  sed -i "s|./workspace|${RENCROW_HOME}/workspace|g" "${RENCROW_HOME}/config.yaml"
fi

if [ ! -f "${RENCROW_HOME}/.env" ]; then
  cat > "${RENCROW_HOME}/.env" <<'EOF'
# Optional RenCrow_LLM Gateway credential.
RENCROW_LLM_API_KEY=
EOF
  chmod 600 "${RENCROW_HOME}/.env"
fi

cat > "${SYSTEMD_USER_DIR}/rencrow-agent-${AGENT_TYPE}.service" <<EOF
[Unit]
Description=RenCrow Agent (${AGENT_TYPE})
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=${RENCROW_HOME}
ExecStart=${RENCROW_BIN}/rencrow-agent -standalone -agent ${AGENT_TYPE} -config ${RENCROW_HOME}/config.yaml
EnvironmentFile=${RENCROW_HOME}/.env
Restart=always
RestartSec=5
StandardInput=null
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable "rencrow-agent-${AGENT_TYPE}"

echo "Installed rencrow-agent ${AGENT_TYPE}."
echo "Configure llm_gateway in ${RENCROW_HOME}/config.yaml for RenCrow_LLM."
echo "Start: systemctl --user start rencrow-agent-${AGENT_TYPE}"
