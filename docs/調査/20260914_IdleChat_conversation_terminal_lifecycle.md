# IdleChat conversation Task/Run terminal lifecycle

## Failure

IdleChat conversation Tasks remained `running` after the visible conversation
was interrupted or stopped. Repeated five-minute starts therefore consumed the
global Task limit and prevented periodic Gmail work from being admitted.

## Problem

The old lifecycle cancelled the provider context and cleared the in-memory
owner pair, but did not complete the canonical Task/Run pair. A manual mode
could also reserve one pair and let its runner issue another pair.

## Cause

- `internal/application/idlechat/orchestrator_modes.go` previously performed
  owner admission inside `beginIdleRunLocked`, and its cancellation helpers
  only cleared `runCancel`, thread, trace, and active IDs.
- `internal/application/idlechat/orchestrator_monitor.go` and the manual
  runners treated context cancellation as the terminal boundary without a
  canonical owner completion.
- `internal/application/taskmanager/manager.go` owns the atomic Task/Run
  completion contract; the IdleChat generation helper in
  `internal/application/idlechat/generation_run_lifecycle.go` is the required
  validation and completion route.

## Lesson

An in-memory interrupt is not a durable Task/Run terminal transition. A
conversation generation must keep its exact owner-issued IDs until the owner
confirms completion. Manual preparation and playback are one generation and
must reuse one pair.

## Invariant

Every configured IdleChat conversation has at most one owner-issued Task/Run
pair. Cancellation, interruption, setup failure, and generation failure close
that exact pair with the corresponding terminal status. Success is recorded
only at an explicit completed playback boundary. A failed completion remains
pending and blocks a new generation until the same pair is retried.

## Enforcement

`conversation_run_lifecycle.go` serializes admission and finalization, performs
owner I/O outside `emitMu`/`mu`, keeps pending identity on write failure, and
uses a detached bounded cleanup context. `run_identity.go` cancels a newly
created Task when admission fails; an existing Task resume is never cancelled
by that cleanup. Stale generation cleanup checks the pending generation before
touching it. Manual, monitor, forecast, story, simple-story, and topic-stock
entrypoints all use the shared lifecycle.

## Tests

`conversation_run_lifecycle_test.go` covers canonical JSONL success and
cancellation, three sequential slot releases, manual handoff with one pair,
no-ready cancellation, admission-failure cleanup, owner callbacks outside
orchestrator locks, invalidated-admission serialization, and stale-generation
isolation. Existing IdleChat tests retain the no-issuer observational fixture
path. Targeted, race, and vet receipts are stored under
`Tmp/test-runtime/idlechat-conversation-lifecycle/`.

## Follow-up: dialogue completion retry (2026-09-14)

The conversation lifecycle fix and v6 owner-validation consolidation did not
close the dialogue-generation failure boundary. Two generation Runs remained
running after provider errors and failed bounded completion. At 13:55 UTC a
read-only Task detail request returned the retained running pair; the following
goroutine snapshot contained no dialogue generation or completion stack.

The dialogue service saves the exact Task/Run in a seed checkpoint before
generation. `waitDialogueCheckpoint` attempts owner completion once and returns
both the provider and completion errors. Its checkpoint is retained, but the
next `Prepare` only discovers it when the same dialogue key is selected. A
different key can create another Task. A same-key seed retry can execute the
provider before the outstanding termination has been resolved.

This is a confirmed retry-lifecycle gap. The execution fence is released when
the synchronous provider callback returns, before owner completion is called;
a same-call fence deadlock is not supported by that path. The precise stage
that exhausted the five-second cleanup budget remains unconfirmed. The live
Task detail read took 2.772 seconds and includes three owner reads; it is not a
measurement of a completion write.

The repair keeps completion intent with the existing generation checkpoint,
including the exact pair and intended outcome. New dialogue admission and
shutdown first retry pending owner completion without invoking generation.
Checkpoint-write or completion failures must retain the identity and error;
successful owner completion is verified before clearing the intent. Actor,
latest-Run, writer-generation, and per-Task fences remain unchanged. This is
mechanical recovery, with no user-response gate or alternate state owner.

Acceptance covers completion failure followed by a different key, recovery
without another provider call, checkpoint reload, already-terminal retries,
invalid ownership rejection, and shutdown joining generation before finalizing
its pending pair. Source tests, deployed scheduling, and same-binary restart
are separate evidence boundaries. Implementation and runtime acceptance are
not established by the earlier observation alone. The added source regression
family passed on 2026-09-14 at 14:38 UTC, including persisted waiting intent,
corrupt checkpoint rejection, and shutdown ordering. Deployment and actual
RenCrow lifecycle acceptance remain separate.

The existing integration record remains the progress index:
`Tmp/test-runtime/xlink-summary-integration/gmail-x-integration-result.json`.
Read-only observations are in its `task-terminal-v7/diagnosis.receipt.json`.
Gmail stabilization is deferred by the user's priority change; mail processing
continues to belong to RenCrow's existing scheduler.

## Follow-up: retained Word checkpoint and completion timing

At 14:24 UTC, one additional Word generation Run remained running after its
14:00 refill error. No active Word generation or completion stack was
present. Unlike Dialogue's arbitrary keys, Word uses the two existing
category checkpoint keys. A source regression with an incomplete retained
checkpoint showed that admission attempted checkpoint resume without first
closing the running Run, so the owner correctly refused the invalid transition.
This is a separate source finding, not an observed replay of the live result.
Retry must first finish that exact Run through the
existing owner; it must not issue another Run or call the provider while
completion is unresolved. After shutdown joins generation, the same retained
checkpoint permits a bounded completion retry. A verified stock result selects
succeeded; an incomplete saved generation selects waiting. Neither outcome is
inferred from a different Run or writer.

A diagnostic on a verified copy of the closed 12:06 backup measured GetRun at
1.108 seconds and CompleteRun(waiting) at 1.349 seconds, 2.458 seconds combined
within the unchanged five-second context. The backup and production stores
were not modified; the diagnostic issued only a synthetic Task in its copy.
This rules out an inherent greater-than-five-second cost for that isolated
sample. It does not establish the live contention stage or a worst-case bound.
The CPU profile attributes most sampled work to transaction snapshot decoding
and validation. No timeout extension or weaker history validation is justified
by this result. Evidence is in `Tmp/test-runtime/task-completion-timing/`.

The later checkpoint inspection found stage `result` and a stock item bearing
that exact Task/Run. The refill error does not prove provider failure. The
generated result must be preserved. Closed-writer recovery uses the canonical
CLI's reasoned waiting transition for only checkpoint-proven retained pairs,
so RenCrow can resume or publish their saved stages; it does not cancel the
result or rewrite the checkpoint directly.

Word source and runtime acceptance are still pending. Dialogue and Word share
the canonical Run completion helper, but retain their existing artifact and
stock recovery rules: arbitrary Dialogue keys need persisted completion intent,
while Word admission already revisits its fixed category checkpoint.

## Follow-up: Word/Forecast topic completion maintenance (2026-09-14)

The v7 read-only observation recorded a Forecast checkpoint at stage `result`
and a stock item with the same Task/Run, while no Forecast provider or
completion stack was present. The evidence is retained at
`Tmp/test-runtime/xlink-summary-integration/task-terminal-v7/forecast-retained-result-evidence.json`;
it establishes the observed retained pair, not a deployed source acceptance.

Topic maintenance now runs before monitor refill and auto-chat work under the
existing topic producer single-flight gate. It retries the exact retained Word
or Forecast pair synchronously without provider or successor issuance, skips a
live producer, and logs a completion failure before admitting new work for that
tick. Stop joins generation work first, then uses the same bounded aggregate to
retry both topic kinds. A matching stock artifact closes the exact Run as
succeeded; an unpublished result closes it as waiting; identity, domain,
missing-stock, and stock-load failures remain fail-closed. Waiting checkpoints
remain available for the next admission, while successful saved-result
closures delete their checkpoint.

The focused CORE completion and Stop tests passed with the repository warm
cache. Deployment, restart, and actual Agent/Viewer acceptance remain separate
evidence boundaries.

## Follow-up: Conversation background job shutdown

At 16:37 UTC, the source-registry and memory-lifecycle jobs could still use
Conversation L1 after shutdown had closed its database. Their `context.Background`
calls were not owned by `Dependencies`, so the resulting errors reached the
background failure reporter and could create shutdown-time failure Tasks.

The jobs now share one cancelable owner context and return joined done channels.
`Dependencies.Shutdown` stops IdleChat, joins both jobs, and only then closes
Conversation Archive and L1. Cancellation-scoped errors are suppressed during
that owner shutdown; errors from a healthy background context remain reportable.
This is a source and focused-test boundary; deployed runtime acceptance remains
separate.
