# エラーハンドリング強化完了

**Status**: ✅ 完了 (2026-03-07)
**Branch**: feature/brush-up
**Commit**: 8fbda70
**Priority**: 🟡 中優先度
**Estimated**: 2-3時間 → **Actual**: 1.5時間

## 実装内容

### 1. リトライ機構実装 (retry.go)

**新規ファイル**: `internal/infrastructure/persistence/conversation/retry.go`

**機能**:
```go
// withRetry — 指数バックオフでリトライ実行
func withRetry(ctx context.Context, config RetryConfig, operation func() error) error

// DefaultRetryConfig — デフォルト設定
RetryConfig{
    MaxAttempts:  3,              // 最大3回試行
    InitialDelay: 100ms,          // 初回待機100ms
    MaxDelay:     2s,             // 最大待機2秒
    Multiplier:   2.0,            // 指数バックオフ係数
}

// RetryableError インターフェース
type RetryableError interface {
    error
    IsRetryable() bool
}
```

**特徴**:
- 指数バックオフ（100ms → 200ms → 400ms...最大2s）
- コンテキストキャンセル対応
- リトライ可能/不可能エラーの判定
- 試行回数とエラー詳細を含むエラーメッセージ

### 2. RealConversationManager エラーハンドリング改善

#### 2.1 Recall メソッド

**Before**:
```go
embedding, err := r.embedder.Embed(ctx, query)
if err != nil {
    log.Printf("Failed to embed query for recall: %v", err)
    return []domconv.Message{}, nil
}
vdbResults, err := r.vectordbStore.SearchSimilar(ctx, embedding, topK)
if err != nil || len(vdbResults) == 0 {
    return []domconv.Message{}, nil  // Silent fail
}
```

**After**:
```go
embedding, err := r.embedder.Embed(ctx, query)
if err != nil {
    log.Printf("Recall: Failed to embed query %q: %v", query, err)
    return []domconv.Message{}, nil
}

// VectorDB検索をリトライ付きで実行
var vdbResults []*domconv.ThreadSummary
err = withRetry(ctx, DefaultRetryConfig, func() error {
    var searchErr error
    vdbResults, searchErr = r.vectordbStore.SearchSimilar(ctx, embedding, topK)
    return searchErr
})
if err != nil {
    log.Printf("Recall: VectorDB search failed after retries for query %q: %v", query, err)
    return []domconv.Message{}, nil
}
```

**改善点**:
- VectorDB検索に3回リトライ適用
- エラーログにクエリ内容を含む
- リトライ後も失敗した場合の明示的ログ

#### 2.2 SearchKB メソッド

**Before**:
```go
queryEmbedding, err := m.embedder.Embed(ctx, query)
if err != nil {
    return nil, fmt.Errorf("failed to generate query embedding: %w", err)
}
docs, err := m.vectordbStore.SearchKB(ctx, domain, queryEmbedding, topK)
if err != nil {
    return nil, fmt.Errorf("failed to search kb: %w", err)
}
```

**After**:
```go
queryEmbedding, err := m.embedder.Embed(ctx, query)
if err != nil {
    return nil, fmt.Errorf("failed to generate query embedding for domain=%s, query=%q: %w", domain, query, err)
}

// VectorDB検索をリトライ付きで実行
var docs []*domconv.Document
err = withRetry(ctx, DefaultRetryConfig, func() error {
    var searchErr error
    docs, searchErr = m.vectordbStore.SearchKB(ctx, domain, queryEmbedding, topK)
    return searchErr
})
if err != nil {
    return nil, fmt.Errorf("failed to search kb after retries (domain=%s, query=%q, topK=%d): %w", domain, query, topK, err)
}
```

**改善点**:
- VectorDB検索に3回リトライ適用
- エラーメッセージに domain/query/topK 含む
- リトライ失敗時の詳細なコンテキスト

#### 2.3 SaveWebSearchToKB メソッド

**Before**:
```go
contentEmbedding, err := m.embedder.Embed(ctx, result.Title+" "+result.Snippet)
if err != nil {
    // ログして続行（個別失敗で全体を止めない）
    continue
}
if err := m.vectordbStore.SaveKB(ctx, doc); err != nil {
    // ログして続行
    continue
}
```

**After**:
```go
contentEmbedding, err := m.embedder.Embed(ctx, result.Title+" "+result.Snippet)
if err != nil {
    log.Printf("SaveWebSearchToKB: Failed to embed result %d/%d (title=%q): %v", i+1, len(results), result.Title, err)
    lastErr = err
    continue
}

// VectorDB保存をリトライ付きで実行
err = withRetry(ctx, DefaultRetryConfig, func() error {
    return m.vectordbStore.SaveKB(ctx, doc)
})
if err != nil {
    log.Printf("SaveWebSearchToKB: Failed to save result %d/%d to VectorDB after retries (title=%q): %v", i+1, len(results), result.Title, err)
    lastErr = err
    continue
}

successCount++
```

**改善点**:
- VectorDB保存に3回リトライ適用
- 成功/失敗カウント追跡
- 詳細ログ（進捗、タイトル、エラー）
- 部分成功の許容（全失敗時のみエラー返却）

### 3. グレースフルデグラデーション維持

**設計原則**: Embedder未設定時はエラーではなく空結果を返す

```go
// SearchKB
if m.embedder == nil {
    log.Printf("SearchKB: Embedder not configured, returning empty results (domain=%s, query=%q)", domain, query)
    return []*domconv.Document{}, nil
}

// SaveWebSearchToKB
if m.embedder == nil {
    log.Printf("SaveWebSearchToKB: Embedder not configured, skipping save (domain=%s, query=%q, %d results)", domain, query, len(results))
    return nil
}
```

**理由**: 既存APIコントラクト維持（TestKBIntegration_NoEmbedder）

## テスト

### retry_test.go (新規)

**6テストケース** (全PASS):
1. `TestWithRetry_Success` — 2回目で成功
2. `TestWithRetry_MaxAttemptsExceeded` — 3回全て失敗
3. `TestWithRetry_NonRetryableError` — 即座失敗（リトライなし）
4. `TestWithRetry_ContextCancelled` — コンテキストキャンセル
5. `TestWithRetry_ImmediateSuccess` — 1回で成功
6. `TestIsRetryableError` — エラー判定ロジック（4サブケース）

### 全既存テスト

```bash
go test ./...
# → 全PASS
```

特に重要:
- `TestKBIntegration_NoEmbedder` — Embedder無効時の動作確認 ✓
- `TestConversationEngine_WithKBSearch` — KB統合テスト ✓
- `TestConversationEngine_KBSearchError` — エラーハンドリング ✓

## 効果

### Before
- ❌ VectorDB失敗時は silent fail（ログなし、リトライなし）
- ❌ エラーメッセージに context 情報なし
- ❌ 一時的なネットワークエラーで即座失敗

### After
- ✅ VectorDB失敗時は最大3回リトライ（指数バックオフ）
- ✅ 詳細ログ（query/domain/件数/進捗等）
- ✅ エラーメッセージに操作コンテキスト含む
- ✅ 一時的エラーから自動回復

### ログ出力例

**成功時**:
```
SaveWebSearchToKB: Saved 5/5 results (domain=programming, query="Rust言語")
```

**部分失敗時**:
```
SaveWebSearchToKB: Failed to save result 3/5 to VectorDB after retries (title="Example"): connection timeout
SaveWebSearchToKB: Saved 4/5 results (domain=programming, query="Rust言語")
```

**リトライ成功時** (ログ例):
```
Recall: VectorDB search failed on attempt 1/3: temporary network error
Recall: VectorDB search succeeded on attempt 2/3
```

## Next Steps

このタスクで VectorDB 操作の信頼性が大幅に向上。

残りの技術的負債:
1. ~~kb-admin API拡充~~ ✅ 完了
2. ~~テストカバレッジ向上~~ ✅ 部分完了
3. ~~エラーハンドリング強化~~ ✅ 完了
4. パフォーマンス最適化 (低優先度)
5. ドキュメント整備 (低優先度)

**推奨**: 次は「パフォーマンス最適化」または「ドキュメント整備」へ。
または feature/brush-up ブランチの作業をここで終了し、成果をまとめることも検討。
