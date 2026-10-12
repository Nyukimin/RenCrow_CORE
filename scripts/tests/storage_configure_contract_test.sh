#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
configure=${repo_root}/scripts/rencrow-storage-configure
test_tmp_root=${repo_root}/Tmp/test-runtime
mkdir -p "${test_tmp_root}"
test_root=$(mktemp -d "${test_tmp_root}/storage-configure-contract.XXXXXX")
trap 'rm -rf -- "${test_root}"' EXIT

cat > "${test_root}/bash3-startup.sh" <<'EOF'
enable -n mapfile 2>/dev/null || true
EOF

fake_bin=${test_root}/bin
mkdir -p "${fake_bin}"

cat > "${fake_bin}/lsblk" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ ${1:-} == -rpn ]]; then
  while IFS= read -r path; do [[ -z ${path} ]] || printf '%s RENCROW_DB\n' "${path}"; done <<<"${FAKE_DB_DEVICES-/dev/fake-db}"
  while IFS= read -r path; do [[ -z ${path} ]] || printf '%s RENCROW_BACKUP\n' "${path}"; done <<<"${FAKE_BACKUP_DEVICES-/dev/fake-backup}"
  if [[ ${FAKE_LABEL_QUERY_STATUS:-0} != 0 ]]; then
    exit "${FAKE_LABEL_QUERY_STATUS}"
  fi
  exit 0
fi
device=${!#}
case "${device}" in
  /dev/fake-db) printf '%s %s\n' "${FAKE_DB_FSTYPE:-ext4}" "${FAKE_DB_SIZE:-2000398934016}" ;;
  /dev/fake-backup) printf '%s %s\n' "${FAKE_BACKUP_FSTYPE:-ext4}" "${FAKE_BACKUP_SIZE:-2000398934016}" ;;
  *) exit 2 ;;
esac
EOF

cat > "${fake_bin}/findmnt" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
target=${!#}
case "${target}" in
  */srv/rencrow/db) printf '%s %s %s\n' "${target}" /dev/fake-db ext4 ;;
  */srv/rencrow/backup) exit 1 ;;
  *) exit 1 ;;
esac
EOF

for command in mount umount systemctl; do
  cat > "${fake_bin}/${command}" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
done
case "${OSTYPE:-}" in
  msys*|mingw*|cygwin*) ;;
  *)
    cat > "${fake_bin}/stat" <<'EOF'
#!/usr/bin/env bash
echo "stat: GNU -c mode query is unavailable in this fixture" >&2
exit 2
EOF
    ;;
esac
chmod +x "${fake_bin}"/*

if [[ ! -x ${configure} ]]; then
  echo "[RED] missing storage configure owner CLI: ${configure}" >&2
  exit 1
fi
bash -n "${configure}"

common_env=(
  PATH="${fake_bin}:${PATH}"
  BASH_ENV="${test_root}/bash3-startup.sh"
  RENCROW_STORAGE_TEST_ROOT="${test_root}/root"
  RENCROW_STORAGE_FSTAB="${test_root}/fstab"
  RENCROW_STORAGE_RECEIPT_DIR="${test_root}/receipts"
)

file_mode() {
  case "${OSTYPE:-}" in
    msys*|mingw*|cygwin*) stat -c '%a' "$1" ;;
    *)
      python3 - "$1" <<'PY'
import os, stat, sys
print(format(stat.S_IMODE(os.stat(sys.argv[1]).st_mode), "o"))
PY
      ;;
  esac
}

inspect_json=${test_root}/inspect.json
env "${common_env[@]}" "${configure}" inspect --json > "${inspect_json}"
python3 - "${inspect_json}" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["status"] == "ok"
assert value["operation"] == "inspect"
assert value["devices"]["db"]["label"] == "RENCROW_DB"
assert value["devices"]["backup"]["label"] == "RENCROW_BACKUP"
assert value["devices"]["db"]["device"] != value["devices"]["backup"]["device"]
PY

if env "${common_env[@]}" FAKE_DB_DEVICES= \
  "${configure}" inspect --json >"${test_root}/missing-label.json" 2>&1; then
  echo "[NG] missing RENCROW_DB label must be rejected" >&2
  exit 1
fi
python3 - "${test_root}/missing-label.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["status"] == "error"
assert "exactly one filesystem (found 0)" in value["error"]
PY

if env "${common_env[@]}" FAKE_DB_DEVICES=$'/dev/fake-db\n/dev/duplicate' \
  "${configure}" inspect --json >"${test_root}/duplicate.json" 2>&1; then
  echo "[NG] duplicate RENCROW_DB labels must be rejected" >&2
  exit 1
fi
python3 - "${test_root}/duplicate.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["status"] == "error"
assert "exactly one" in value["error"]
PY

if env "${common_env[@]}" FAKE_LABEL_QUERY_STATUS=17 \
  "${configure}" inspect --json >"${test_root}/label-query-error.json" 2>&1; then
  echo "[NG] a failed label query with partial output must be rejected" >&2
  exit 1
fi
python3 - "${test_root}/label-query-error.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["status"] == "error"
assert "filesystem labels" in value["error"]
PY

cat > "${test_root}/fstab" <<'EOF'
# unrelated entries must survive
UUID=root / ext4 defaults 0 1
UUID=old-db /srv/rencrow/db ext4 defaults 0 2
UUID=old-backup /srv/rencrow/backup ext4 defaults 0 2
EOF
chmod 0640 "${test_root}/fstab"
fstab_mode_before=$(file_mode "${test_root}/fstab")

apply_json=${test_root}/apply.json
env "${common_env[@]}" "${configure}" apply --json > "${apply_json}"
fstab_mode_after=$(file_mode "${test_root}/fstab")
[[ ${fstab_mode_after} == "${fstab_mode_before}" ]]
first_hash=$(sha256sum "${test_root}/fstab" | cut -d' ' -f1)
env "${common_env[@]}" "${configure}" apply --json >/dev/null
second_hash=$(sha256sum "${test_root}/fstab" | cut -d' ' -f1)
[[ ${first_hash} == "${second_hash}" ]]
grep -Fqx 'UUID=root / ext4 defaults 0 1' "${test_root}/fstab"
[[ $(grep -Fc 'LABEL=RENCROW_DB /srv/rencrow/db ext4 defaults,nofail,x-systemd.device-timeout=30s 0 2' "${test_root}/fstab") == 1 ]]
[[ $(grep -Fc 'LABEL=RENCROW_BACKUP /srv/rencrow/backup ext4 noauto,nofail,user 0 2' "${test_root}/fstab") == 1 ]]
[[ -f ${test_root}/fstab.rencrow-storage.bak ]]
python3 - "${apply_json}" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["status"] == "ok"
assert value["operation"] == "apply"
assert value["changed"] is True
assert value["receipt"]
PY

env "${common_env[@]}" "${configure}" verify --json > "${test_root}/verify.json"
python3 - "${test_root}/verify.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding="utf-8"))
assert value["status"] == "ok"
assert value["operation"] == "verify"
assert value["mounts"]["db"]["mounted"] is True
assert value["mounts"]["backup"]["mounted"] is False
PY

echo "[OK] storage configure contract test passed"
