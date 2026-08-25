# CORE Check Plan runner

`rencrow-check-plan-runner` is the CORE owner integration for the cross-module
`rencrow-check-plan` planner. It reads the versioned manifest at
`config/checks/core.json`, invokes the fixed planner executable with an
explicit UTC evaluation time, and executes only included checks from the
allowlist.

The repository manifest is installed at
`~/.local/share/rencrow/checks/core.json`. The runner uses that path by
default; `RENCROW_CORE_CHECK_MANIFEST` may select another explicit owner
manifest for an isolated runtime or test.

Runtime checks use the existing CORE routes:

- `GET /health`
- `GET /health/ready`
- `GET /viewer/memory/layers?include_l2=false&limit=1`

The Conversation L1 snapshot integrity check is declared for the `backup`
phase and is deferred during the manifest's `runtime` phase. It never opens a
live production database. Backup execution, when explicitly requested with a
snapshot directory, delegates only to the fixed read-only
`rencrow-storage-restore-check` owner utility.

Example:

```bash
go run ./cmd/rencrow-check-plan-runner \
  --manifest config/checks/core.json \
  --planner /home/nyukimi/.local/bin/rencrow-check-plan \
  --core-url http://127.0.0.1:18790 \
  --phase runtime
```

The command emits a bounded JSON receipt containing `plan_revision` and
`results`. Exit status is `0` when all included checks pass, `1` when an
included check fails, `2` for CLI/input errors, and `3` for blocked or
fail-closed plan/route validation.
