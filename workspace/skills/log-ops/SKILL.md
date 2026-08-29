---
name: log-ops
description: Inspect RenCrow canonical operation events, follow Chat/Worker/Coder execution, and diagnose Event Store failures without re-deriving the workflow.
---

# Log Ops

Use this skill when tracing what RenCrow actually did, especially across restarts or after live Viewer history has been lost.

## Canonical Stores

- `storage.databases.event_store` (`component_id=orchestrator`)
- `workspace/execution_report.jsonl`

## Canonical APIs

- `GET /viewer/logs?scope=persisted&limit=...`
- `GET /viewer/agent/detail?id=mio`
- `GET /viewer/job/detail?job_id=...`
- `GET /viewer/audit/summary`

## Investigation Order

1. Find the `job_id`.
Start from `/viewer/logs?scope=persisted&limit=100`.

2. Confirm the route and handoff chain.
Look for:
- `message.received`
- `routing.decision`
- `agent.dispatch`
- `mailbox.sent`
- `mailbox.waiting`
- `mailbox.received`
- `agent.response`

3. Determine where the job stalled.
- stopped before `agent.dispatch`: routing/chat side
- stopped at `mailbox.waiting`: worker/coder side likely blocked
- `mailbox.error` or `agent.error`: failure location is explicit

4. Confirm user-facing completion.
The final user-facing truth is `agent.response` from `mio` to `user`.

## Common Failure Patterns

- `live` has events but `persisted` does not
Check the configured `storage.databases.event_store`, the Canonical Event Store append error, and the `orchestrator` component projection.

- agent shows `offline` after restart
This is expected for in-memory status. Use persisted logs for history, not current liveness.

- coder jobs stop at `mailbox.waiting`
Check whether a matching `mailbox.received` exists. If not, the remote/local agent likely never returned.

## Rules

- Treat the append-only Canonical Event Store as the persisted source of truth for operation history.
- Treat `workspace/execution_report.jsonl` as execution evidence, not the full operation log.
- Do not infer completion from `agent.note`; confirm with `agent.response`.
- When reporting to a user, summarize through Mio-facing outcome first, then add the deeper agent chain only if needed.
