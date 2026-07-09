#!/usr/bin/env bash
# Migrate the legacy picoclaw_multiLLM runtime home directory (~/.picoclaw) to
# the RenCrow_CORE runtime home directory (~/.rencrow), fixing up the
# `~/.picoclaw/rencrow/memory` -> `~/.rencrow/memory` de-nesting on the way.
#
# Usage:
#   scripts/migrate_picoclaw_home.sh [--dry-run] [--move]
#
#   --dry-run   Print what would happen without changing anything.
#   --move      Move (rename) instead of copy. Default is copy, so the old
#               ~/.picoclaw tree is preserved until you delete it yourself.
#
# The script is idempotent: if ~/.rencrow already exists it aborts with a
# message instead of overwriting anything.
set -euo pipefail

log() {
  printf '[migrate_picoclaw_home] %s\n' "$1"
}

DRY_RUN=0
MOVE=0

for arg in "$@"; do
  case "$arg" in
    --dry-run)
      DRY_RUN=1
      ;;
    --move)
      MOVE=1
      ;;
    -h|--help)
      sed -n '2,16p' "$0"
      exit 0
      ;;
    *)
      echo "[migrate_picoclaw_home] unknown option: $arg" >&2
      exit 2
      ;;
  esac
done

run() {
  if [ "$DRY_RUN" -eq 1 ]; then
    log "DRY-RUN: $*"
  else
    "$@"
  fi
}

HOME_DIR="${HOME:?HOME is not set}"
OLD_HOME="$HOME_DIR/.picoclaw"
NEW_HOME="$HOME_DIR/.rencrow"
NESTED_MEMORY="$NEW_HOME/rencrow/memory"
FLAT_MEMORY="$NEW_HOME/memory"

log "old runtime home: $OLD_HOME"
log "new runtime home: $NEW_HOME"

if [ ! -e "$OLD_HOME" ]; then
  log "no $OLD_HOME found -- nothing to migrate."
  exit 0
fi

if [ -e "$NEW_HOME" ]; then
  log "ABORT: $NEW_HOME already exists. Refusing to overwrite an existing runtime home."
  log "Inspect $NEW_HOME manually, then re-run once it is removed or merged."
  exit 1
fi

# --- Step 1: copy or move ~/.picoclaw -> ~/.rencrow ---------------------
if [ "$MOVE" -eq 1 ]; then
  log "moving $OLD_HOME -> $NEW_HOME"
  run mv "$OLD_HOME" "$NEW_HOME"
else
  log "copying $OLD_HOME -> $NEW_HOME"
  if command -v rsync >/dev/null 2>&1; then
    run rsync -a "$OLD_HOME"/ "$NEW_HOME"/
  else
    run cp -a "$OLD_HOME" "$NEW_HOME"
  fi
fi

if [ "$DRY_RUN" -eq 1 ]; then
  log "DRY-RUN: would de-nest $NESTED_MEMORY -> $FLAT_MEMORY (if present)"
  log "DRY-RUN: would remove leftover empty $NEW_HOME/rencrow/ (if empty)"
else
  # --- Step 2: de-nest ~/.rencrow/rencrow/memory -> ~/.rencrow/memory ---
  if [ -d "$NESTED_MEMORY" ]; then
    if [ -e "$FLAT_MEMORY" ]; then
      log "WARNING: both $NESTED_MEMORY and $FLAT_MEMORY exist. Skipping automatic de-nesting."
      log "WARNING: please merge them manually, e.g.:"
      log "WARNING:   rsync -a \"$NESTED_MEMORY\"/ \"$FLAT_MEMORY\"/ && rm -rf \"$NESTED_MEMORY\""
    else
      log "de-nesting $NESTED_MEMORY -> $FLAT_MEMORY"
      mv "$NESTED_MEMORY" "$FLAT_MEMORY"
    fi
  fi

  # --- Step 3: remove leftover empty ~/.rencrow/rencrow/ ----------------
  NESTED_DIR="$NEW_HOME/rencrow"
  if [ -d "$NESTED_DIR" ]; then
    if [ -z "$(ls -A "$NESTED_DIR" 2>/dev/null)" ]; then
      log "removing empty leftover directory $NESTED_DIR"
      rmdir "$NESTED_DIR"
    else
      log "WARNING: $NESTED_DIR still has content after de-nesting, left in place for manual review:"
      ls -A "$NESTED_DIR" | sed 's/^/[migrate_picoclaw_home]   /'
    fi
  fi
fi

log "migration of runtime home directory complete."
log ""
log "Remaining manual steps:"
log "  1. Stop and disable the legacy systemd --user units, if present:"
log "       systemctl --user disable --now picoclaw-watchdog.timer picoclaw-watchdog.service"
log "  2. Install and enable the new watchdog units:"
log "       make install-watchdog enable-watchdog"
log "  3. If you export PICOCLAW_* environment variables (e.g. in shell rc files"
log "     or systemd unit overrides), rename them to RENCROW_* (e.g. RENCROW_TOOLS_ROOT)."
if [ "$MOVE" -eq 0 ]; then
  log "  4. Once you have verified $NEW_HOME looks correct, you may delete $OLD_HOME manually:"
  log "       rm -rf \"$OLD_HOME\""
fi
