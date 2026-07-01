#!/usr/bin/env bash
set -euo pipefail

cat >&2 <<'EOF'
ops_watchdog_kick.sh is obsolete.

The current watchdog manages only:
- local RenCrow /health
- Tailscale Serve for the Viewer

Run this instead:
  make watchdog-run-once

Funnel, LINE webhook, LLM/Ollama checks, and gateway restart kicks are no longer
part of this watchdog.
EOF
exit 1
