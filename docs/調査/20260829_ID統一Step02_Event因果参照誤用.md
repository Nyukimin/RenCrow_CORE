# ID統一Step 02: Event因果参照誤用

## Failure

SuperAgentの`TraceEvent.ParentEventID`へEventIDではなくRunIDが保存されていた。

## Problem

`ParentEventID`を機械的に`CausationEventID`へrenameすると、存在しないEvent参照を
Canonical Event Graphへ持ち込むか、存在しなかった過去Eventを捧造することになる。

## Cause

Event、Run、Traceがstringであり、writer validationも非空と一部のRun一致しか検査して
いなかった。owner別Event Storeは因果参照の存在性とDAGを強制していなかった。

## Evidence

2026-08-29 UTCにproduction DBをread-onlyで計測した。

- `ai_workflow_event`: 86 records、`parent_event_id` 0件
- `trace_event`: 478 records、`parent_event_id` 103件
- `trace_event.parent_event_id`が同tableのEventIDで解決した件数: 0件
- 未解決103件は、例えば`run_lead_...`のようにRunIDを保存していた

## Lesson

Field名のrenameで意味は変換できない。migrationは参照先の実在とID typeを確認し、
根拠のない因果関係を作らない。

## Invariant

- Canonical EventのCausationとDependencyは同一Trace内の存在済みEventだけを指す。
- RunID、TaskID、ActionIDをEventID fieldへ保存しない。
- migrationは根拠のない過去Eventを生成しない。

## Enforcement

Canonical ID型、Event Envelope validation、Event Storeのtransactional foreign reference check、
migration manifest、AST identity linterで強制する。

## Tests

Root、Child、Parallel、Join、missing causation/dependency、cross-trace、cycle、
EventID/RunID type mismatch、production snapshot dry-runを固定testとする。
