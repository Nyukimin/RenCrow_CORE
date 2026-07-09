# LightMemory 統合検討メモ

- Date: 2026-07-07
- Scope: `internal/domain/agent.LightMemory` と `internal/domain/conversation.ConversationEngine` の将来統合検討
- Status: 設計メモ。今回は実装しない
- Primary refs:
  - `internal/domain/agent/light_memory.go`
  - `internal/domain/conversation/engine.go`
  - `internal/infrastructure/persistence/conversation/engine_impl.go`
  - `docs/refs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md`
  - `docs/refs/01_正本仕様/06_会話エンジン.md`

## 0. 結論

[OK] LightMemory は、Worker / Coder 系エージェント向けの軽量な L0 Turn / Prompt Context 補助として現状維持する。

[OK] 将来の統合候補として、永続化や RecallPack 全体ではなく「直近会話を prompt messages として取得し、turn 終了後に記録する」最小責務の共通 interface を検討する。

[NG] Worker / Coder に ConversationEngine 側の L1 SQLite、DuckDB、VectorDB、RecallTrace、UserMemory promotion をそのまま持ち込まない。LightMemory の特徴は、外部インフラなし、ゼロ永続化、プロセス再起動でリセットされる低フットプリント性である。

[WARN] 現行コードでは Shiro / Heavy / Wild も ConversationEngine を注入され得るため、LightMemory と ConversationEngine が同時に使われる経路がある。統合する場合は「短期文脈の二重注入」を先に検出・防止する必要がある。

## 1. 用語と L0 整合性

### 1.1 事実

`docs/refs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md` は、L0 を保存媒体ではなく lifecycle 上の位置として定義している。

| Layer | 正本上の意味 | このメモでの扱い |
|---|---|---|
| L0 | Turn / Prompt Context。現在 user message、attachment、system prompt、直近数発話。Retention は turn 単位 | LightMemory の `RecentMessages()` 出力や RecallPack `ShortContext` のうち、prompt に注入される直近会話部分 |
| L1 | Thread / Hot Memory。直近会話、thread state、session state、short summary、search cache | ConversationEngine / L1 SQLite 側の hot memory。Worker / Coder へ無条件導入しない |
| L2 以上 | summary、candidate、confirmed、validated、archive など | 今回の共通 interface 対象外 |

### 1.2 提案

共通 interface の名前に `Memory` を使う場合でも、責務は `ShortTermMemory` または `PromptContextMemory` のように L0/L1 を混同しない名前にする。特に LightMemory は「正式記憶」や「長期記憶」ではなく、prompt context 用の bounded buffer として扱う。

## 2. 現状比較

### 2.1 事実: API

| 観点 | LightMemory | ConversationEngine |
|---|---|---|
| 定義 | `internal/domain/agent/light_memory.go` の concrete struct | `internal/domain/conversation/engine.go` の interface。実装は `RealConversationEngine` |
| turn 開始 | `RecentMessages(sessionID string) []llm.Message` | `BeginTurn(ctx, sessionID, userMessage) (*RecallPack, error)` |
| turn 終了 | `Record(sessionID, userMessage, response)` | `EndTurn(ctx, sessionID, userMessage, response) error`、拡張で `EndTurnAs` |
| reset | `Clear(sessionID)`, `ClearAll()` | `ResetSession(ctx, sessionID)`, `FlushCurrentThread(ctx, sessionID)` |
| 返却形式 | `[]llm.Message`。user / assistant 交互、system なし | `RecallPack`。`FilterForRole()`、`ApplyRecallBudget()`、`ToPromptMessages()`、trace 生成を持つ |
| エラー | 返さない | `BeginTurn` / `EndTurn` は error を返す。ただし実装は多くを warning にして graceful degradation |

### 2.2 事実: ライフサイクル

| 観点 | LightMemory | ConversationEngine |
|---|---|---|
| 生成 | `agent.NewLightMemory(maxTurns)` | `conversationpersistence.NewRealConversationEngine(manager, persona)` |
| 配線 | `cmd/rencrow/runtime_agents.go` で Shiro、`cmd/rencrow/runtime_coders.go` と `cmd/rencrow-agent/*` で Coder に注入 | `cmd/rencrow/runtime_conversation.go` で構築し、Mio / Shiro / Heavy / Wild に注入 |
| セッション単位 | `sessionID` key の map | `sessionID` に紐づく active thread / RecallPack / store |
| 日次・明示 reset | `ClearAll()` はあるが、現状の検索範囲では runtime 配線からの定期呼び出しは見つからない | `/new` などから `ResetSession`、`/compact` などから `FlushCurrentThread` が使われる |
| role | role filter なし。llm.Message の role は user / assistant 固定 | `FilterForRole("chat"|"worker"|"wild"|"heavy"|...)` あり |

### 2.3 事実: 永続性

| 観点 | LightMemory | ConversationEngine |
|---|---|---|
| 保存媒体 | process 内 map + slice | RealConversationManager、L1 SQLite、DuckDB、VectorDB、optional Redis など |
| 再起動 | リセットされる。コメント上も意図的 | store 設定次第で thread、summary、search cache、trace、knowledge 等が残る |
| trace | なし | `RecordRecallTrace`、L1 SQLite trace store 連携あり |
| profile / user memory | なし | profile extractor、UserMemory prompt、candidate capture など Mio 向け機能と連動 |

### 2.4 事実: メモリフットプリント

| 観点 | LightMemory | ConversationEngine |
|---|---|---|
| 保持上限 | `maxTurns`。config validation は 1〜20。default は Worker / Coder とも 3 | RecallPack は token budget あり。store 側には thread、summary、cache、trace など複数の保持領域がある |
| 1 turn の内容 | userMessage、response、timestamp | Message、Thread、Summary、KB/SearchCache snippet、TraceItem、Persona、UserProfile など |
| 外部依存 | なし | SQLite / DuckDB / VectorDB / embedding / summary provider などが構成により関与 |
| <10MB 制約 | 適合しやすい。turn 数と文字列長だけが主な増加要因 | そのまま Worker / Coder に導入すると、プロセス内メモリだけでなくストア接続・cache・trace の運用コストが増える |

## 3. 現状の利用箇所

### 3.1 事実: LightMemory 呼び出し

- `ShiroAgent.Execute`
  - `ConversationEngine` があれば `BeginTurn` → `FilterForRole("worker")` → `ToPromptMessages()` を先に追加する。
  - その後 `LightMemory.RecentMessages(t.ChatID())` を追加する。
  - LLM 応答後に `LightMemory.Record(...)` し、さらに `ConversationEngine.EndTurnAs(..., SpeakerShiro)` を呼ぶ。
- `CoderAgent.GenerateProposal`
  - system prompt の後、現在 user message の前に `LightMemory.RecentMessages(t.ChatID())` を追加する。
  - Proposal 抽出と self-check 成功後に `LightMemory.Record(...)` する。
- `CoderAgent.GenerateWithPrompt`
  - `GenerateProposal` と同様に直近履歴を追加し、応答後に `Record` する。
- `cmd/rencrow/runtime_coders.go`
  - Coder1〜4 で共有する `globalLightMemory` を使う。
  - 最初の LightMemory 有効 Coder で初期化し、以後の Coder に同じ instance を渡す。
- `cmd/rencrow-agent/handler_coder.go`
  - SSH / remote Coder 経路では handler の `globalMemory` を再利用する。
- `cmd/rencrow-agent/runtime_init.go`
  - standalone agent 初期化時は agent ごとに `NewLightMemory` する。

### 3.2 事実: ConversationEngine 呼び出し

- `cmd/rencrow/runtime_conversation.go`
  - `cfg.Conversation.Enabled` のとき `RealConversationManager` と `RealConversationEngine` を構築する。
  - L1 SQLite path があれば `L1SQLiteStore` を接続し、engine に recall trace store として渡す。
  - embedder、summarizer、thread boundary detector、profile extractor を条件付きで接続する。
- `cmd/rencrow/runtime_agents.go`
  - Mio には `NewMioAgent(..., convEngine)` で注入する。
  - convEngine が nil でなければ Shiro / Heavy / Wild にも注入する。
- `MioAgent.Chat`
  - `BeginTurn` → `FilterForRole("chat")` → `RecordRecallTrace` → `ToPromptMessages()` の順で prompt を組み立てる。
  - UserMemory prompt、Web検索 context、発言帰属ガード、recent glossary context、現在 user message を追加する。
  - 応答後に `EndTurn` と `captureUserMemoryCandidate` を呼ぶ。
- `HeavyAgent.Generate` / `WildAgent.Generate`
  - `BeginTurn` → role filter → trace → prompt messages → 応答後 `EndTurnAs`。
- `ShiroAgent.Execute`
  - ConversationEngine と LightMemory の両方が有効なら、両方の短期文脈が prompt に入る。

## 4. 共通インターフェース案

### 4.1 提案: 最小 interface

```go
type ShortTermMemory interface {
    RecallPromptMessages(ctx context.Context, sessionID string, userMessage string, role string) ([]llm.Message, error)
    RecordTurn(ctx context.Context, sessionID string, userMessage string, response string, speaker string) error
    ResetSession(ctx context.Context, sessionID string) error
}
```

意図:

- LLM 呼び出し側が欲しい最小成果物は `[]llm.Message` である。
- `role` は ConversationEngine の role filter に合わせる。ただし LightMemory は無視してよい。
- `speaker` は Shiro / Heavy / Wild / Mio などの発話帰属を失わないための将来拡張点にする。LightMemory は現状 assistant として扱ってよい。
- `ResetSession` は LightMemory の `Clear`、ConversationEngine の `ResetSession` に対応させる。

### 4.2 提案: LightMemory adapter

```go
type LightMemoryAdapter struct {
    memory *agent.LightMemory
}

func (a LightMemoryAdapter) RecallPromptMessages(ctx context.Context, sessionID, userMessage, role string) ([]llm.Message, error) {
    return a.memory.RecentMessages(sessionID), nil
}

func (a LightMemoryAdapter) RecordTurn(ctx context.Context, sessionID, userMessage, response, speaker string) error {
    a.memory.Record(sessionID, userMessage, response)
    return nil
}

func (a LightMemoryAdapter) ResetSession(ctx context.Context, sessionID string) error {
    a.memory.Clear(sessionID)
    return nil
}
```

適合性:

- [OK] 現行のゼロ永続化、goroutine-safe、FIFO、低フットプリントを維持できる。
- [WARN] speaker metadata は保存できない。必要になった時点で LightMemory の turn 型拡張を検討する。
- [WARN] `ClearAll()` 相当は interface に含めない案。日次カットオーバーが必要なら別 interface に分ける。

### 4.3 提案: ConversationEngine adapter

```go
type ConversationShortTermAdapter struct {
    engine conversation.ConversationEngine
}

func (a ConversationShortTermAdapter) RecallPromptMessages(ctx context.Context, sessionID, userMessage, role string) ([]llm.Message, error) {
    pack, err := a.engine.BeginTurn(ctx, sessionID, userMessage)
    if err != nil || pack == nil {
        return nil, err
    }
    filtered := pack.FilterForRole(role)
    return filtered.ToPromptMessages(), nil
}

func (a ConversationShortTermAdapter) RecordTurn(ctx context.Context, sessionID, userMessage, response, speaker string) error {
    if aware, ok := a.engine.(interface {
        EndTurnAs(context.Context, string, string, string, conversation.Speaker) error
    }); ok {
        return aware.EndTurnAs(ctx, sessionID, userMessage, response, conversation.Speaker(speaker))
    }
    return a.engine.EndTurn(ctx, sessionID, userMessage, response)
}

func (a ConversationShortTermAdapter) ResetSession(ctx context.Context, sessionID string) error {
    return a.engine.ResetSession(ctx, sessionID)
}
```

適合性:

- [OK] ConversationEngine 側の BeginTurn / EndTurn pattern に自然に乗る。
- [OK] role filter と RecallPack 予算制御は既存実装を使える。
- [WARN] `ToPromptMessages()` は Persona / UserProfile / L2 / L3 / KB / SearchCache も含み得るため、「短期記憶だけ」の adapter ではない。Worker / Coder 用には RecallPack の ShortContext だけを取り出す別 adapter が必要になる可能性がある。

### 4.4 提案: より厳密な L0-only interface

Worker / Coder の <10MB 制約を優先するなら、共通化対象をさらに絞る。

```go
type TurnContextMemory interface {
    RecentTurnMessages(ctx context.Context, sessionID string, maxTurns int) ([]llm.Message, error)
    RecordTurn(ctx context.Context, sessionID string, userMessage string, response string) error
}
```

この案では ConversationEngine adapter は RecallPack 全体ではなく `ShortContext` のみを `llm.Message` に変換する。L1 SQLite / DuckDB / VectorDB 由来の候補は含めないため、LightMemory の思想に近い。

## 5. 統合した場合の利点・コスト・リスク

### 5.1 提案: 利点

- Agent 実装から `LightMemory` と `ConversationEngine` の分岐を減らせる。
- Coder / Worker / Chat の prompt 組み立て順をテストしやすくなる。
- 短期文脈の二重注入を interface 層で防止できる。
- 将来 Worker / Coder にも role-specific RecallPack を使う場合、移行入口を揃えられる。

### 5.2 提案: コスト

- 既存の `LightMemory.RecentMessages` / `Record` 直接呼び出しを adapter 経由へ置き換える必要がある。
- `ConversationEngine.ToPromptMessages()` は短期文脈以外も含むため、共通 interface の責務定義を誤ると Worker / Coder に過剰 context が入る。
- `speaker`、role、trace、budget、reset のどこまでを interface に含めるかで抽象が膨らむ。
- 現行テストは LightMemory と ConversationEngine で分かれているため、adapter の contract test が必要になる。

### 5.3 提案: リスク

| リスク | 内容 | 回避方針 |
|---|---|---|
| メモリ <10MB 制約違反 | Worker / Coder に ConversationEngine の store、trace、KB/SearchCache context を持ち込むと軽量性を失う | Worker / Coder 既定は LightMemoryAdapter。ConversationEngine adapter は明示 opt-in |
| 二重注入 | Shiro は現状 convEngine と LightMemory の両方を使い得る | interface 導入時は「1 agent につき prompt memory source は 1 つ」を受け入れ基準にする |
| 用語混同 | LightMemory を L1/L2 記憶と誤解する | docs と型名で L0 / prompt context の責務に限定する |
| 追跡性低下 | LightMemory には trace がない | trace は ConversationEngine 専用機能として残し、LightMemory に必須化しない |
| 永続化の副作用 | Coder の plan / patch 生成内容が永続 store に残る | 永続化が必要になるまで Worker / Coder はゼロ永続化を維持する |

## 6. 段階的移行案

### Phase 0: 設計メモのみ

実施内容:

- 本メモを追加する。
- コード変更はしない。

受け入れ基準:

- [OK] 事実と提案が分離されている。
- [OK] L0 定義が `18_Memory_Lifecycle_Recall_Context.md` と矛盾しない。
- [OK] Worker / Coder に L1 SQLite 依存を持ち込まない方針が明記されている。

### Phase 1: Contract test だけを先に設計

実施内容:

- `ShortTermMemory` または `TurnContextMemory` の contract をテストとして定義する。
- 実装差し替えは行わず、現行 LightMemory の振る舞いを基準にする。

受け入れ基準:

- [OK] maxTurns FIFO、session 分離、system prompt 非混入がテストで表現されている。
- [OK] nil / empty session の扱いが明文化されている。
- [OK] ConversationEngine の L2/L3/KB が Worker / Coder の L0-only contract に混入しないことが検証観点に入っている。

### Phase 2: LightMemory adapter 導入

実施内容:

- LightMemory の concrete API は残したまま adapter を追加する。
- Coder から adapter 経由に切り替える候補を作る。

受け入れ基準:

- [OK] Coder の prompt message 順が変更前後で一致する。
- [OK] LightMemory のゼロ永続化、外部依存なし、maxTurns 1〜20 validation が維持される。
- [OK] 追加メモリ使用量は adapter 分だけで、会話内容保持量は増えない。

### Phase 3: Agent ごとの memory source を単一化

実施内容:

- Shiro のように ConversationEngine と LightMemory が同時に入り得る経路を整理する。
- config 上で `light_memory` と `conversation short context` の優先順位を明確にする。

受け入れ基準:

- [OK] 1 回の LLM 呼び出しで同じ直近会話が二重注入されない。
- [OK] role filter が必要な経路では ConversationEngine adapter、軽量経路では LightMemory adapter が使われる。
- [OK] config の既定値は既存挙動と互換である。

### Phase 4: ConversationEngine adapter の限定導入

実施内容:

- Worker / Coder で永続記憶が必要になった場合だけ、ConversationEngine adapter を opt-in で導入する。
- L0-only adapter と RecallPack adapter を分ける。

受け入れ基準:

- [OK] Worker / Coder の既定構成では SQLite / DuckDB / VectorDB 接続が増えない。
- [OK] opt-in 時も RecallPack role filter、token budget、trace が確認できる。
- [OK] plan / patch など Coder 出力の保存範囲が仕様化され、機密・破壊的操作情報を不用意に永続化しない。

## 7. 今回は実装しない判断

### 7.1 判断

[OK] 今回は共通 interface の検討メモ作成までとし、コード変更は行わない。

理由:

- LightMemory は既に Worker / Coder の低フットプリント要件に合っている。
- ConversationEngine は Mio の多層記憶、RecallPack、trace、UserMemory、Knowledge と結びついており、短期 FIFO の単純置換対象ではない。
- 現状の統合は、利点よりも二重注入、永続化範囲拡大、メモリ / インフラ依存増加のリスクが大きい。

### 7.2 着手トリガー条件

次のいずれかが発生したら、Phase 1 から着手する。

- Worker / Coder にも永続記憶または role-specific RecallPack が必要になった。
- Shiro / Coder の短期文脈注入で二重注入や一貫性不具合が観測された。
- Coder1〜4 共有 LightMemory の session 分離、speaker 帰属、または maxTurns 制御では要件を満たせなくなった。
- prompt context の token budget を Chat / Worker / Coder 横断で統一的に制御する必要が出た。
- LightMemory の内容を Viewer / trace で観測する必要が出た。ただし、この場合も永続化ではなく bounded observation から検討する。

## 8. 次に設計するなら確認すること

- `ShortTermMemory` と `TurnContextMemory` のどちらを正本 interface 名にするか。
- Worker / Coder で必要なのは L0-only か、RecallPack role filter つき context か。
- Shiro で ConversationEngine と LightMemory が同時有効な場合の優先順位。
- Coder の plan / patch を短期記憶に残すことの安全性と retention。
- LightMemory の session 単位が `ChatID()` だけで十分か。Coder slot、agent name、transport を key に含める必要があるか。
