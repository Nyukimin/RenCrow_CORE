# Phase 4.2: Worker RESEARCH自動保存 実装進捗

**開始日:** 2026-03-07
**ステータス:** 🔄 実装中（ビルドエラー修正待ち）

## 目的

Worker が RESEARCH ルートで Web検索を実行した際、検索結果を自動的に KB (Knowledge Base) に保存する機能を実装。

## 完了項目 ✅

### 1. ToolResponse 拡張
**ファイル:** `internal/domain/tool/response.go`
- `Metadata map[string]any` フィールド追加
- 構造化データ（KB保存用）を格納可能に

### 2. web_search V2実装
**ファイル:** `internal/infrastructure/tools/runner.go`
- `executeWebSearchV2()` メソッド追加
- Google Search API レスポンスを解析
- 表示用文字列 + 構造化データ（Metadata）を返却
- Metadata に以下を格納:
  - `query`: 検索クエリ
  - `search_items`: GoogleSearchItem配列
  - `total_count`: 結果件数

### 3. Mio Agent 拡張
**ファイル:** `internal/domain/agent/mio.go`

**追加インターフェース:**
```go
type ConversationManager interface {
    SearchKB(ctx, domain, query, topK) ([]*Document, error)
    SaveWebSearchToKB(ctx, domain, query, results) error
}

type WebSearchResult struct {
    Title, Link, Snippet string
}
```

**構造体変更:**
- `conversationMgr ConversationManager` フィールド追加
- `WithConversationManager(mgr)` メソッド追加

**executeWebSearch 改修:**
- `ExecuteV2` 使用で構造化データ取得
- Metadata から search_items 抽出
- `SaveWebSearchToKB()` 呼び出し（エラーはログ警告のみ）
- `getStringField()` ヘルパー追加

### 4. ToolRunner インターフェース拡張
**ファイル:** `internal/domain/agent/interfaces.go`
- `ExecuteV2(ctx, toolName, args) (*tool.ToolResponse, error)` 追加
- `tool` パッケージ import 追加

### 5. Mock更新
**ファイル:** 
- `test/integration/flow_test.go`
- `test/integration/ops_react_loop_test.go`
- `internal/domain/agent/shiro_test.go`

**追加実装:**
```go
func (m *mockToolRunner) ExecuteV2(ctx, toolName, args) (*tool.ToolResponse, error) {
    result, err := m.Execute(ctx, toolName, args)
    if err != nil {
        return tool.NewError(tool.ErrInternalError, err.Error(), nil), nil
    }
    return tool.NewSuccess(result), nil
}
```

## 残作業 ⏸️

### 1. LegacyRunner 対応【最優先】
**問題:** `tool.LegacyRunner` が `ExecuteV2` 未実装

**エラー箇所:** `cmd/picoclaw/main.go`
```
cannot use chatToolRunner (type *tool.LegacyRunner) as agent.ToolRunner:
  missing method ExecuteV2
```

**対応:**
- `internal/infrastructure/tools/legacy_runner.go` に ExecuteV2 実装
- または LegacyRunner を ToolRunner に置き換え

### 2. main.go DI統合
**ファイル:** `cmd/picoclaw/main.go`

**必要な変更:**
```go
// RealConversationManager を Mio に注入
mioAgent := agent.NewMioAgent(...)
    .WithConversationManager(realMgr)
```

**注意:** 現在は `convEngine` のみ渡している

### 3. ドメイン取得改善
**問題:** `executeWebSearch` で domain = "general" 固定

**改善策:**
- Thread.Domain を取得する仕組み追加
- または ConversationEngine から現在のドメイン取得

### 4. 統合テスト追加
**新規ファイル:** `test/integration/kb_autosave_test.go`

**テストケース:**
- RESEARCH ルート → web_search 実行 → KB保存確認
- Metadata の構造化データ検証
- ConversationManager=nil 時のグレースフル動作

## ビルドエラー詳細

```bash
# cmd/picoclaw/main.go
Line 323: cannot use chatToolRunner (*tool.LegacyRunner) as agent.ToolRunner
           missing method ExecuteV2
Line 407: cannot use chatToolRunner as agent.ToolRunner in NewMioAgent
Line 408: cannot use workerToolRunner as agent.ToolRunner in NewShiroAgent
```

**原因:** LegacyRunner が古いインターフェース（Execute のみ）

## 次のアクション

### 即座に対応（再開時）
1. `tool.LegacyRunner` 確認 - どこで定義されているか
2. `ExecuteV2` 実装追加
3. ビルド確認
4. main.go で Mio に ConversationManager 注入
5. 全テスト実行

### Phase 4.2 完了条件
- [ ] ビルド成功
- [ ] 既存テスト全PASS
- [ ] KB自動保存の動作確認（手動 or E2E）
- [ ] ドキュメント更新（実装仕様 Phase 4.2 完了マーク）

## 設計メモ

### アーキテクチャ判断
- **Orchestrator フック方式を不採用** - Mio が内部で完結する方がクリーン
- **ConversationManager を Mio に注入** - Engine とは別に Manager も持つ
- **Metadata 方式採用** - ToolResponse に構造化データ格納（拡張性高い）

### トレードオフ
- **ドメイン固定:** 現状 "general" 固定だが、将来的に Thread.Domain 取得で改善可能
- **エラーハンドリング:** KB保存失敗でも検索結果は返す（ユーザー体験優先）
- **型変換:** interface{} → map → struct の多段変換（Go の制約）

## 関連ファイル一覧

### 変更済み
- `internal/domain/tool/response.go`
- `internal/infrastructure/tools/runner.go`
- `internal/domain/agent/mio.go`
- `internal/domain/agent/interfaces.go`
- `test/integration/flow_test.go`
- `test/integration/ops_react_loop_test.go`
- `internal/domain/agent/shiro_test.go`

### 未変更（要対応）
- `internal/infrastructure/tools/legacy_runner.go` ※要確認
- `cmd/picoclaw/main.go`

### 関連ドキュメント
- `docs/KB運用ガイド.md` - Phase 4.1 で作成済み
- `docs/実装仕様_会話LLM_v5.md` - Phase 4.2 タスクリスト記載

## Git コミット未実施

**変更は未コミット** - proposal/clean-architecture ブランチで作業中
