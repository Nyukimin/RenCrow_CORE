#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SERVICE_NAME="${SERVICE_NAME:-picoclaw-funnel.service}"
VERIFY_USER="${SUDO_USER:-${USER}}"

log() {
  printf '[tailscale-viewer-root] %s\n' "$*"
}

if [[ "${EUID}" -ne 0 ]]; then
  log "this script must be run with sudo/root:"
  log "sudo $0"
  exit 2
fi

log "disabling ${SERVICE_NAME}"
systemctl disable --now "${SERVICE_NAME}" || true
systemctl reset-failed "${SERVICE_NAME}" || true

if systemctl is-active --quiet "${SERVICE_NAME}"; then
  log "blocked: ${SERVICE_NAME} is still active"
  systemctl status "${SERVICE_NAME}" --no-pager || true
  exit 2
fi

if systemctl is-enabled --quiet "${SERVICE_NAME}" 2>/dev/null; then
  log "blocked: ${SERVICE_NAME} is still enabled"
  systemctl status "${SERVICE_NAME}" --no-pager || true
  exit 2
fi

log "running Serve verification as ${VERIFY_USER}"
if command -v runuser >/dev/null 2>&1 && id "${VERIFY_USER}" >/dev/null 2>&1; then
  exec runuser -u "${VERIFY_USER}" -- "${ROOT_DIR}/scripts/tailscale_viewer_serve_verify.sh"
fi

exec "${ROOT_DIR}/scripts/tailscale_viewer_serve_verify.sh"
