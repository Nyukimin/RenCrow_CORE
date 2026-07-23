#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
checker=${repo_root}/scripts/rencrow-storage-restore-check
backup_runner=${repo_root}/scripts/rencrow-storage-backup
test_root=$(mktemp -d)
trap 'rm -rf -- "${test_root}"' EXIT

bash -n "${checker}"
bash -n "${backup_runner}"

mkdir -p \
  "${test_root}/source/state/sessions" \
  "${test_root}/source/state/memory" \
  "${test_root}/source/state/exports/parquet" \
  "${test_root}/source/external-memory/redis" \
  "${test_root}/source/external-memory/qdrant" \
  "${test_root}/snapshot"

python3 - "${test_root}/source/state/l1.db" "${test_root}/source/state/l2.db" <<'PY'
import sqlite3
import sys

for path in sys.argv[1:]:
    connection = sqlite3.connect(path)
    connection.execute("CREATE TABLE memory (id INTEGER PRIMARY KEY, value TEXT NOT NULL)")
    connection.execute("INSERT INTO memory(value) VALUES ('kept')")
    connection.commit()
    connection.close()
PY

printf 'REDIS0011-test-rdb' > "${test_root}/source/external-memory/redis/dump.rdb"
printf 'qdrant-snapshot-test' > "${test_root}/source/external-memory/qdrant/full.snapshot"
qdrant_sha256=$(sha256sum "${test_root}/source/external-memory/qdrant/full.snapshot" | cut -d' ' -f1)
tar -C "${test_root}/source" -czf "${test_root}/snapshot/rencrow-state.tar.gz" state external-memory
(
  cd "${test_root}/snapshot"
  sha256sum rencrow-state.tar.gz > SHA256SUMS
)
cat > "${test_root}/snapshot/manifest.txt" <<EOF
format_version=3
core_name=state
session_relative=sessions
operation_memory_relative=memory
cold_export_relative=exports/parquet
conversation_l1_relative=l1.db
conversation_archive_relative=l2.db
redis_export=external-memory/redis/dump.rdb
qdrant_export=external-memory/qdrant/full.snapshot
qdrant_sha256=${qdrant_sha256}
EOF

"${checker}" "${test_root}/snapshot"
echo "[OK] storage backup contract test passed"
