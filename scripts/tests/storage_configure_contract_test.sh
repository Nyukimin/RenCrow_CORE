#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
configure=${repo_root}/scripts/rencrow-storage-configure
test_tmp_root=${repo_root}/Tmp/test-runtime
mkdir -p "${test_tmp_root}"
test_root=$(mktemp -d "${test_tmp_root}/storage-configure-contract.XXXXXX")
trap 'rm -rf -- "${test_root}"' EXIT

fake_bin=${test_root}/bin
mkdir -p "${fake_bin}"

cat > "${fake_bin}/lsblk" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
device=${!#}
case "${device}" in
  /dev/fake-db) printf '%s %s\n' "${FAKE_DB_FSTYPE:-ext4}" "${FAKE_DB_SIZE:-2000398934016}" ;;
  /dev/fake-backup) printf '%s %s\n' "${FAKE_BACKUP_FSTYPE:-ext4}" "${FAKE_BACKUP_SIZE:-2000398934016}" ;;
  *)
    while IFS= read -r path; do [[ -z ${path} ]] || printf '%s RENCROW_DB\n' "${path}"; done <<<"${FAKE_DB_DEVICES:-/dev/fake-db}"
    while IFS= read -r path; do [[ -z ${path} ]] || printf '%s RENCROW_BACKUP\n' "${path}"; done <<<"${FAKE_BACKUP_DEVICES:-/dev/fake-backup}"
    ;;
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
chmod +x "${fake_bin}"/*

if [[ ! -x ${configure} ]]; then
  echo "[RED] missing storage configure owner CLI: ${configure}" >&2
  exit 1
fi
bash -n "${configure}"

common_env=(
  PATH="${fake_bin}:${PATH}"
  RENCROW_STORAGE_TEST_ROOT="${test_root}/root"
  RENCROW_STORAGE_FSTAB="${test_root}/fstab"
  RENCROW_STORAGE_RECEIPT_DIR="${test_root}/receipts"
)

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

cat > "${test_root}/fstab" <<'EOF'
# unrelated entries must survive
UUID=root / ext4 defaults 0 1
UUID=old-db /srv/rencrow/db ext4 defaults 0 2
UUID=old-backup /srv/rencrow/backup ext4 defaults 0 2
EOF

apply_json=${test_root}/apply.json
env "${common_env[@]}" "${configure}" apply --json > "${apply_json}"
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
