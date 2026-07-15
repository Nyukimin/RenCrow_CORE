# Memory Lifecycle・Recall Context 実装契約

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../02_実装仕様.md`
- source_spec: `../../refs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md`、`../../refs/10_新仕様/09_Memory_SourceRegistry仕様.md`
- last_reviewed: 2026-07-15
- scope: COREが所有するMemory lifecycle、RecallPack、prompt injection、promotion、traceの現行実装契約

## 1. 責務境界

Memoryは保存媒体ではなく、「何を保存し、何を正式記憶として扱い、何をLLMへ注入したか」を追跡する境界である。

次を混同しない。

| 領域 | 役割 |
| --- | --- |
| Conversation | 現在thread、rolling summary、過去thread summary |
| UserMemory | ユーザーのprofile、preference、project、constraint等 |
| Character / Persona | character固有の設定、観測、正本persona |
| Knowledge | 検証済み外部知識、Wiki、Relation |
| OperationMemory | runtimeで参照する運用ノート |
| Runtime logs | 状態観測・診断。原則として正式記憶ではない |

## 2. RecallPack

`internal/domain/conversation.RecallPack`をLLM promptへ渡す選別済みcontextの正本型とする。DB全体やraw observationを直接promptへ注入しない。

RecallPackは、rolling summary、short context、mid summaries、long facts、KB / Wiki / search cache / relation snippets、persona、user profileを保持する。呼び出し側は次を行う。

1. conversation engineからRecallPackを取得する。
2. `FilterForRole`でroleごとの許可範囲へ絞る。
3. token budgetを適用する。
4. 採用項目と不採用理由をRecall Traceへ記録する。
5. 選別済みprompt messageだけをLLM providerへ渡す。

## 3. UserMemory lifecycle

| state | 意味 | prompt注入 |
| --- | --- | --- |
| `observed` | 観測直後。未評価 | 不可 |
| `candidate` | 保存候補。未確定 | 不可 |
| `confirmed` | evidenceを伴い確定 | 条件を満たす場合のみ可 |
| `pinned` | 明示理由を伴う優先記憶 | 条件を満たす場合のみ可 |

注入可能条件はすべて満たす必要がある。

- `active=true`
- stateが`confirmed`または`pinned`
- `superseded_by`が空
- lifecycleが`decayed`ではない
- sensitivityが空または`normal`
- scopeが対象personaを許可する

`confirmed`と`pinned`にはevidenceが必要である。`pinned`には明示理由も必要とし、sensitive memoryを自動昇格しない。

## 4. 外部入力とSource Registry

外部情報は次の順で処理する。

```text
fetch / import
  -> staging(pending)
  -> validation(validated / rejected)
  -> human or policy gate
  -> Knowledge / Memoryへのpromotion
```

取得成功だけで正式Knowledgeにしない。source、raw hash、validation、promotion先を追跡できない項目はprompt injection対象にしない。

## 5. Recall Trace

Recall Traceは最低限次を追跡する。

- session / response / role
- 候補のkind、source、score
- 採用・role filter除外・budget除外
- prompt section
- redaction後の安全な表示情報

trace失敗で会話を停止しない場合でも、失敗をlogへ残す。traceはprompt内容そのものや機密値を無制限に保存しない。

## 6. 実装owner

| 責務 | 主な場所 |
| --- | --- |
| RecallPack / role filter / budget | `internal/domain/conversation/` |
| UserMemory state / injection guard | `internal/domain/memory/` |
| conversation engine / L1 / trace store | `internal/infrastructure/persistence/conversation/` |
| Mio / Shiro等への注入 | `internal/domain/agent/` |
| Source Registry / promotion | `internal/application/sourcefetcher/`、Viewer source handlers |
| Viewer inspection | `internal/adapter/viewer/memory*_handler.go`、`recall_trace_handler.go` |
| lifecycle job | `cmd/rencrow/runtime_background_jobs.go` |

## 7. 検証

- domain: memory state、promotion、sensitivity、scope、role filter、budget
- persistence: lifecycle、staging、validation、promotion、recall trace
- integration: conversation engineからagent promptまで
- E2E: L0〜L3 RecallPack、Source Registry staging→validate→promote→memory layers

将来phaseや未実装案は`docs/refs/`に残し、code・testのない契約を本書へ追加しない。
