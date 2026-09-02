# CORE owner verifier

`rencrow-core-verify` is the read-only RenCrow_CORE executor for the v3 owner
manifest. It accepts one declared `check_id` and emits one
`rencrow.check-receipt.v1` JSON receipt:

```text
rencrow-core-verify run \
  --manifest config/checks/core.json \
  --check-id core_health \
  --observed-at 2026-08-27T00:00:00Z \
  --evidence-dir /path/to/a/bounded-evidence-dir
```

The executable has a fixed command allowlist. Health, readiness, and the
lightweight L1 query use CORE's loopback Public API. Backup integrity requires
an explicitly supplied snapshot and delegates only to
`rencrow-storage-restore-check`; it never opens a live database. Deployment,
systemd lifecycle, startup trace, and authenticated Agent checks require their
explicit owner inputs and return `blocked` when those inputs or the canonical
route are unavailable. No fallback route, test double, restart, build, deploy,
or external mutation is performed.

Step 03 DCI acceptance uses three fixed checks. The pre check records a fresh
authenticated Shiro route result, the post check binds the same request after a
canonical service restart, and `core-dci-identity-final` strictly joins those
owner-only evidence files with the service-cutover, cutover, and deploy
receipts. The final check re-observes the canonical service, listener, and
readiness and requires a clean warning/error journal since the post evidence.
It emits only bounded hashes, canonical chain IDs/counts, and booleans; it does
not expose input paths, process IDs, queries, credentials, or raw logs.

Exit status is `0` for `passed`/`not_applicable`, `10` for `failed`, `20` for
`blocked`, `30` for `unverified`, and `2` for a CLI, manifest, or schema error.
