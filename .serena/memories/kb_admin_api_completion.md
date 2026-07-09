# kb-admin API 拡充完了

**Status**: ✅ 完了 (2026-03-07)
**Branch**: feature/brush-up
**Commit**: 05cbc55
**Priority**: 🔴 高優先
**Estimated**: 2-3時間 → **Actual**: 1.5時間

## 実装内容

### 1. RealConversationManager に KB管理API追加

**ファイル**: `internal/infrastructure/persistence/conversation/real_manager.go`

追加メソッド (4つ):
```go
// ListKBDocuments はKBコレクション内の全ドキュメントを取得
func (m *RealConversationManager) ListKBDocuments(ctx context.Context, domain string, limit int) ([]*domconv.Document, error)

// GetKBCollections は存在するKBコレクション一覧を取得
func (m *RealConversationManager) GetKBCollections(ctx context.Context) ([]string, error)

// GetKBStats はKBコレクションの統計情報を取得
func (m *RealConversationManager) GetKBStats(ctx context.Context, domain string) (*KBStats, error)

// DeleteOldKBDocuments は指定日時より古いKBドキュメントを削除
func (m *RealConversationManager) DeleteOldKBDocuments(ctx context.Context, domain string, before time.Time) (int, error)
```

### 2. vectordbStoreIface インターフェース拡張

**ファイル**: `internal/infrastructure/persistence/conversation/store_interfaces.go`

- 4つのKB管理メソッドシグネチャを追加
- `time` パッケージ import 追加

### 3. cmd/kb-admin 全コマンド実装

**ファイル**: `cmd/kb-admin/main.go`

#### cmdList (lines 177-206)
- `mgr.ListKBDocuments()` を使用してドキュメント一覧を取得
- 最大100件表示（limit指定可能）
- ID/Source/CreatedAt/Content preview を表示

Before: "Use 'search' command..." (未実装メッセージ)
After: 実際のドキュメントリスト表示

#### cmdStats (lines 245-272)
- `mgr.GetKBCollections()` でコレクション一覧取得
- 各コレクションの `GetKBStats()` で統計取得
- DocumentCount / VectorSize を表示

Before: 既知ドメインを手動チェック（簡易実装）
After: 実際のコレクション統計表示

#### cmdCleanup (lines 275-311)
- 削除前: `GetKBStats()` でドキュメント数確認
- 削除実行: `DeleteOldKBDocuments()`
- 削除後: `GetKBStats()` で結果確認
- 削除数の差分を表示

Before: "This feature will be implemented in Phase 4.2" (未実装)
After: 実際の削除処理実行＋統計表示

### 4. テスト対応

**ファイル**: `internal/infrastructure/persistence/conversation/real_manager_test.go`

mockVectorDBStore に5メソッド追加:
- `SaveKB` / `SearchKB` (既存)
- `ListKBDocuments` (新規)
- `GetKBCollections` (新規)
- `GetKBStats` (新規)
- `DeleteOldKBDocuments` (新規)

## 動作検証

### ビルド
```bash
go build -o kb-admin ./cmd/kb-admin
# → OK (エラーなし)
```

### テスト
```bash
go test ./...
# → 全PASS (conversation パッケージ含む)
```

### 使用例
```bash
# ドキュメント一覧
kb-admin list programming

# 統計情報
kb-admin stats

# 古いドキュメント削除 (30日以上前)
kb-admin cleanup general 30
```

## 効果

### Before (TODOコメント3箇所)
- `list`: 「search コマンドで代用して」メッセージのみ
- `stats`: 既知ドメインを手動チェック（簡易実装）
- `cleanup`: 「Phase 4.2 で実装予定」メッセージのみ

### After (全機能実装完了)
- `list`: 実際のKBドキュメント一覧を表示（Content preview付き）
- `stats`: 実際のコレクション統計（件数/ベクトルサイズ）
- `cleanup`: 削除処理実行＋Before/After統計表示

## 関連ファイル
- `internal/infrastructure/persistence/conversation/vectordb_store.go` (lines 669-875)
  - 既存実装を活用（ListKBDocuments/GetKBCollections/GetKBStats/DeleteOldKBDocuments）
- `docs/KB運用ガイド.md`
  - kb-admin 使用方法が記載されている

## Next Steps
このタスク完了により、技術的負債リストの「kb-admin API拡充」が全て解消された。

残りの技術的負債:
- テストカバレッジ向上 (中優先度)
- エラーハンドリング強化 (中優先度)
- パフォーマンス最適化 (低優先度)
- ドキュメント整備 (低優先度)
