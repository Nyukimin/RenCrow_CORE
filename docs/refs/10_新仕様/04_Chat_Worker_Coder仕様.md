# Chat / Worker / Coder 仕様

## 目的

RenCrow は Chat / Worker / Coder の三分割を最重要境界として扱う。

この境界は UI 上の呼び名ではなく、実行責務、権限、安全境界、ログ契約を分けるためのシステム境界である。

## 役割

| 役割 | 主な実体 | 責務 | 担当しないこと |
| --- | --- | --- | --- |
| Chat | Mio | ユーザー対話、ルーティング判断、結果返却、全体モニタリング | 破壊的操作、patch 適用、file / shell / git 実行 |
| Worker | Shiro / Worker Core | 実行可否判断、Coder 選定、patch / command 実行、ログ記録、安全境界管理 | ユーザー向け自然対話の最終表現、Coder の提案生成 |
| Coder | AO / Aka / Kin / Gin | plan / patch / proposal 生成、設計説明、複数案提示 | 採用決定、正本ブランチ管理、破壊的操作の直接実行 |

## LLM alias 境界

Shiro は用途によって LLM alias を分ける。

| 用途 | character | LLM alias | max_tokens cap | 備考 |
| --- | --- | --- | ---: | --- |
| 通常 Worker / OPS / Coder 検証 | Shiro | `Worker` | 4096 | Worker Core として長めの検証・説明を許可する。 |
| IdleChat 内の Shiro 発話 | Shiro | `ChatWorker` | 1024 | backing model と prompt は Worker と同じ。雑談用に出力長だけ短くする。 |

`Shiro = Worker` と `Shiro = ChatWorker` は競合ではない。同じ Shiro persona が、通常作業では `Worker`、IdleChat では `ChatWorker` を使うという用途別 alias である。

`ChatWorker` は Worker 実行権限や Coder 選定の別人格ではない。IdleChat の発話生成に限定して使い、patch / shell / git / tool execution の責務は通常の `Worker` 境界に残す。

## 不変ルール

Coder は破壊的操作を直接実行しない。

Coder が返すものは候補である。

- plan
- patch
- proposal
- risk
- cost hint

実行、採用、ログ記録、安全確認は Worker が担当する。

## route chain

代表的な流れ:

```text
adapter input
  -> MessageOrchestrator
  -> Mio route decision
  -> CHAT / PLAN / ANALYZE / OPS / RESEARCH / CODE / CODE1 / CODE2 / CODE3 / CODE4
  -> route-specific handler
  -> Worker / Coder / Chat response
  -> event / report / Viewer / channel response
```

主な route:

| route | 意味 | 主な処理 |
| --- | --- | --- |
| `CHAT` | 通常対話 | Mio が応答する |
| `PLAN` | 計画 | 計画作成を優先する |
| `ANALYZE` | 分析 | 調査・解析を行う |
| `OPS` | 運用 | Worker が安全境界内で実行する |
| `RESEARCH` | 調査 | 外部検索や情報収集を行う |
| `CODE` | Coder 自動選択 | Worker が Coder を選ぶ |
| `CODE1` | AO / DeepSeek | 仕様設計寄り |
| `CODE2` | Aka / OpenAI | 実装寄り |
| `CODE3` | Kin / Claude | 高品質推論・複雑作業 |
| `CODE4` | Gin / Gemini | 補助 Coder 枠 |

明示コマンド、ルール辞書、分類器、安全側 fallback の順で route を決める。

fallback は正常系ではなく、分類できなかった場合の安全側経路である。

route 判定は `routing.Decision.Evidence` に、明示コマンド、ルール辞書、分類器、安全側 fallback の各段階を `DecisionEvidence` として残す。event log の `routing.decision` には evidence summary を含める。これは Worker execution evidence とは別であり、route 判定の根拠だけを扱う。

自然文の実装、修正、更新、テスト追加の依頼は `CODE2` を優先する。Proposal を生成できる Coder であれば、Worker が policy / protected file / workspace を確認して patch 実行へ進む。

`CODE1` から `CODE4` は Shiro 経由の autonomous route として扱う。provider 未接続や distributed 実機接続が不足する場合は、成功扱いにせず案内・ログ・report で区別する。

## capability adaptation

Coder 自動選択は route 判定そのものとは別責務として扱う。

| 領域 | 主担当 | 内容 |
| --- | --- | --- |
| node capability 観測 | `internal/infrastructure/capability`, `cmd/rencrow/runtime_capability.go` | OS / memory / LLM provider availability / ToolRegistry を起動時に観測する |
| capability contract | `internal/domain/capability` | `NodeCapabilities`, `LLMCapability`, `ToolCapability`, profile, coder selection evidence を保持する |
| runtime wiring | `cmd/rencrow/runtime_coders.go`, `cmd/rencrow/runtime_orchestrator.go` | config と観測結果から coder capability を構成し、MessageOrchestrator へ渡す |
| Coder 選択 | `internal/application/orchestrator/code_executor*.go` | route の要求品質、利用可能 coder、縮退先を使って実行先 Coder を選ぶ |

`SelectCoderWithEvidence` は、選択結果だけでなく要求品質、選択 Coder、縮退 route、候補ごとの除外理由を `CoderSelectionEvidence` として返す。Application はこの evidence をログへ残す。これは routing evidence とは別であり、Mio が `CODE3` などを選んだ理由ではなく、Worker がどの Coder へ渡したかの能力根拠である。

## plan / patch / execution の境界

```text
Coder
  -> plan / patch / proposal を生成

Worker
  -> proposal を検証
  -> protected file / workspace / policy を確認
  -> file / shell / git / test を実行
  -> report / event / log を残す

Chat
  -> ユーザーへ結果を返す
```

## tool / security 境界

Worker 実行と tool 実行要求は近いが、同じ境界ではない。

| 領域 | 主担当 | 役割 |
| --- | --- | --- |
| WorkerExecutionService | `internal/application/service/worker_execution_*.go` | Coder proposal を受け、patch / shell / git / test の実行単位を管理する |
| ToolRunner | `internal/infrastructure/tools/runner_*.go` | tool 実行要求を shell / file / web search / subagent などの実装へ渡す |
| PolicyEngine / security | `internal/infrastructure/security`, `internal/domain/security` | workspace、protected pattern、許可/拒否/要確認の判断を行う |
| toolloop | `internal/application/toolloop` | tool calling loop を扱う。Chat の最終応答契約と混ぜない |

Coder はこれらを直接実行しない。Coder が返す proposal は Worker と policy 境界を通過してから実行される。

## prompt / persona 境界

prompt 本文は Go source へ埋め込まず、外部ファイルとして扱う。

| 領域 | 主担当 | 役割 |
| --- | --- | --- |
| PromptBundle | `internal/adapter/config/promptbundle`, `internal/adapter/config/prompts.go` | manifest と prompt file を読み込み、runtime へ渡す |
| character prompts | `prompts/characters/*`, `prompts/*.md` | Mio / Shiro / Coder などの system / policy / routing / knowledge |
| skill context loader | `internal/domain/context/skills_loader.go`, `internal/domain/context/builder.go`, `workspace/skills`, `prompts/skills` | SKILL.md の metadata / context summary を読む。workspace が prompts より優先され、同名 skill は先勝ち |
| Persona registry | `internal/infrastructure/persona`, `workspace/` | persona / styleguide の保存と読み込み |
| Mio persona self-edit | `internal/domain/agent/mio_persona.go`, `internal/infrastructure/persona/editor.go`, `cmd/rencrow/runtime_agents.go` | Mio の persona edit intent を検出し、LLM による persona file 更新を行う。prompt bundle や runtime memory とは混同しない |

prompt、persona、runtime memory は近いが、同じ state として扱わない。prompt は初期方針、persona は人格・文体設定、memory は runtime により増える観測データである。

skill context は prompt 補助であり、実行権限ではない。`SkillMetadata.CanExecute` は loader では常に false とし、shell / file / web などの実行は ToolRunner、Worker execution、security policy を必ず通る。

## 実装箇所

| 仕様 | 主担当 |
| --- | --- |
| Mio / Shiro / Coder の domain entity | `internal/domain/agent` |
| route 値 | `internal/domain/routing` |
| route 分類 | `internal/infrastructure/routing` |
| orchestration | `internal/application/orchestrator/message_orchestrator_*.go` |
| Coder proposal / CodeExecutor | `internal/application/orchestrator/code_executor*.go`, `internal/domain/proposal`, `internal/domain/patch` |
| Worker execution | `internal/application/service/worker_execution_*.go` |
| execution persistence | `internal/infrastructure/persistence/execution` |
| ToolRunner / security policy | `internal/infrastructure/tools/runner_*.go`, `internal/infrastructure/security`, `internal/domain/security` |
| toolloop | `internal/application/toolloop` |
| standalone agent process | `cmd/rencrow-agent/*.go` |
| autonomous / subagent execution | `internal/application/autonomous`, `internal/application/subagent`, `cmd/rencrow/autonomous_entry.go` |
| distributed execution / transport | `internal/application/orchestrator/distributed_orchestrator_*.go`, `internal/domain/transport`, `internal/infrastructure/transport` |
| prompt bundle / external prompts | `internal/adapter/config/promptbundle`, `internal/adapter/config/prompts.go`, `prompts/` |
| skill context loader | `internal/domain/context/skills_loader.go`, `internal/domain/context/builder.go`, `workspace/skills`, `prompts/skills` |
| persona / light memory | `internal/infrastructure/persona`, `internal/domain/agent/light_memory.go` |

## 検証

確認対象:

- explicit route command が期待 route に入る。
- Coder route で Coder が直接実行しない。
- WorkerExecutionService が protected file / workspace / policy を守る。
- job_id、session_id、route、status が記録される。
- Viewer / event log に結果が追跡できる。

主なテスト:

```bash
GOCACHE=/tmp/rencrow-gocache go test ./internal/domain/agent ./internal/domain/routing ./internal/application/orchestrator ./internal/application/service
GOCACHE=/tmp/rencrow-gocache go test -count=1 -tags=e2e ./test/e2e
```
