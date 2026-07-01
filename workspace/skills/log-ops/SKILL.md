---
name: log-ops
description: Inspect RenCrow persisted JSON operation logs, follow Chat/Worker/Coder execution, and diagnose log retention or GC failures without re-deriving the workflow.
---

# Log Ops

Use this skill when tracing what RenCrow actually did, especially across restarts or after live Viewer history has been lost.

## Canonical Files

- `workspace/orchestrator_event_log.jsonl`
- `workspace/orchestrator_event_gc.jsonl`
- `workspace/execution_report.jsonl`

## Canonical APIs

- `GET /viewer/logs?scope=persisted&limit=...`
- `GET /viewer/agent/detail?id=mio`
- `GET /viewer/job/detail?job_id=...`
- `GET /viewer/audit/summary`

## Investigation Order

1. Find the `job_id`.
Start from `/viewer/logs?scope=persisted&limit=100` or grep the JSONL file.

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
Check whether `viewer_log.enabled` is on and whether append to `workspace/orchestrator_event_log.jsonl` is succeeding.

- agent shows `offline` after restart
This is expected for in-memory status. Use persisted logs for history, not current liveness.

- coder jobs stop at `mailbox.waiting`
Check whether a matching `mailbox.received` exists. If not, the remote/local agent likely never returned.

- GC removed too much
Inspect `workspace/orchestrator_event_gc.jsonl` for `retention_days`, `before_count`, `deleted_count`, and `status`.

- `partial_error` in GC
This usually means malformed JSON lines or broken `timestamp` values were encountered during compaction.

## Rules

- Treat `workspace/orchestrator_event_log.jsonl` as the persisted source of truth for operation history.
- Treat `workspace/execution_report.jsonl` as execution evidence, not the full operation log.
- Do not infer completion from `agent.note`; confirm with `agent.response`.
- When reporting to a user, summarize through Mio-facing outcome first, then add the deeper agent chain only if needed.
