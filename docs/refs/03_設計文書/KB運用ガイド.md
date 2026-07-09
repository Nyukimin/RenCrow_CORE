# Knowledge Base (KB) 運用ガイド

**バージョン:** 1.0
**更新日:** 2026-03-07
**対象:** RenCrow v5.0 Conversation System with KB

---

## 目次

1. [概要](#概要)
2. [アーキテクチャ](#アーキテクチャ)
3. [ドメイン設計](#ドメイン設計)
4. [KB保存（Web検索結果）](#kb保存web検索結果)
5. [KB検索（RAG）](#kb検索rag)
6. [管理・運用](#管理運用)
7. [パフォーマンスチューニング](#パフォーマンスチューニング)
8. [トラブルシューティング](#トラブルシューティング)

---

## 概要

### Knowledge Base (KB) とは

RenCrow の **Knowledge Base (KB)** は、Web検索結果やその他の外部情報を永続化し、
後続の会話で再利用するための仕組みです。

**主な用途:**
- Worker の RESEARCH タスク結果の保存
- Chat の RAG (Retrieval-Augmented Generation) による情報補完
- ドメイン特化知識の蓄積（映画、技術、歴史等）

**技術スタック:**
- **VectorDB:** Qdrant (ベクトル検索エンジン)
- **Embedding:** 768次元ベクトル（Cohere/OpenAI互換）
- **コレクション構造:** ドメイン別 (`kb_{domain}`)

---

## アーキテクチャ

### データフロー

```
1. Web検索実行
   ├─ Google Custom Search API
   └─ 検索結果 (Title, Link, Snippet)
          ↓
2. KB保存
   ├─ Title + Snippet → Embedding生成
   ├─ Document 作成（Content, Source, Meta）
   └─ Qdrant へ保存 (kb_{domain})
          ↓
3. KB検索（RAG）
   ├─ ユーザークエリ → Embedding生成
   ├─ Qdrant ベクトル検索 (Top-K)
   └─ RecallPack の LongFacts に追加
          ↓
4. Chat応答生成
   └─ KB検索結果を文脈として利用
```

### レイヤー構成

| レイヤー | 役割 | 実装 |
|---------|------|------|
| **Domain** | KB Document 定義 | `internal/domain/conversation/document.go` |
| **Application** | - | - |
| **Infrastructure** | VectorDB 操作 | `internal/infrastructure/persistence/conversation/vectordb_store.go` |
| **Adapter** | ConversationManager | `internal/infrastructure/persistence/conversation/real_manager.go` |
| **Engine** | RAG統合 | `internal/infrastructure/persistence/conversation/engine_impl.go` |

---

## ドメイン設計

### ドメインとは

**ドメイン** は KB の論理的な分類単位です。Qdrant では `kb_{domain}` という名前のコレクションとして管理されます。

### 標準ドメイン

| ドメイン | 用途 | 例 |
|---------|------|-----|
| `general` | 汎用・一般知識 | ニュース、日常会話 |
| `programming` | プログラミング技術 | Go言語、アルゴリズム |
| `movie` | 映画情報 | おすすめ映画、レビュー |
| `anime` | アニメ情報 | 作品紹介、キャラクター |
| `tech` | 技術トレンド | AI動向、新技術 |
| `history` | 歴史・文化 | 歴史的事実、文化背景 |

### ドメイン選定ガイドライン

**原則:**
1. **粒度:** 広すぎず狭すぎず（検索精度とメンテナンス性のバランス）
2. **安定性:** 頻繁に変更しない（データ移行コストが高い）
3. **明確性:** 境界が曖昧なドメインは避ける

**良い例:**
- `programming` - 明確な技術分野
- `movie` - 明確なエンタメ分野

**悪い例:**
- `entertainment` - 広すぎる（movie/anime/game を分離すべき）
- `go_concurrency` - 狭すぎる（programming に統合すべき）

### カスタムドメイン追加

新しいドメインを追加する場合：

1. **ドメイン名を決定**（小文字英数字、アンダースコア可）
2. **既存ドメインと重複しないか確認**
3. **初回保存時に自動作成される**（手動作成不要）

```go
// 例: 料理レシピドメインを追加
domain := "recipe"
mgr.SaveWebSearchToKB(ctx, domain, "パスタ レシピ", results)
```

---

## KB保存（Web検索結果）

### API: SaveWebSearchToKB

**シグネチャ:**
```go
func (m *RealConversationManager) SaveWebSearchToKB(
    ctx context.Context,
    domain string,
    query string,
    results []WebSearchResult,
) error
```

**パラメータ:**
- `domain` - 保存先ドメイン（例: "programming"）
- `query` - 検索クエリ（メタ情報として保存）
- `results` - Web検索結果の配列

**WebSearchResult 構造:**
```go
type WebSearchResult struct {
    Title   string `json:"title"`   // ページタイトル
    Link    string `json:"link"`    // URL
    Snippet string `json:"snippet"` // 要約文
}
```

### 使用例

```go
// 1. Web検索を実行（Google Custom Search API等）
results := []WebSearchResult{
    {
        Title:   "Go言語の並行処理入門",
        Link:    "https://example.com/go-concurrency",
        Snippet: "ゴルーチンとチャネルの基本的な使い方を解説します。",
    },
    {
        Title:   "Go Concurrency Patterns",
        Link:    "https://example.com/go-patterns",
        Snippet: "実践的な並行処理パターンを紹介します。",
    },
}

// 2. KB に保存
err := mgr.SaveWebSearchToKB(ctx, "programming", "Go言語 並行処理", results)
if err != nil {
    log.Printf("Failed to save to KB: %v", err)
}
```

### 自動保存（Phase 4.2 実装予定）

**Worker RESEARCH ルート**で自動的にKB保存する仕組み：

```go
// Orchestrator レベルでフック
if route == routing.RouteRESEARCH {
    response, err := mio.Chat(ctx, task)
    if err == nil {
        // Web検索結果を抽出してKB保存
        if webResults := extractWebSearchResults(response); len(webResults) > 0 {
            mgr.SaveWebSearchToKB(ctx, task.Domain, task.Message, webResults)
        }
    }
}
```

**注意:** 現状は手動API呼び出しが必要です。

---

## KB検索（RAG）

### 自動統合

ConversationEngine の `BeginTurn()` で **自動的にKB検索が実行されます**。

**動作フロー:**
1. ユーザーメッセージを受信
2. 現在のドメインを取得（Thread.Domain）
3. ユーザーメッセージの Embedding を生成
4. Qdrant で類似度検索（Top-3）
5. 検索結果を `[KB]` プレフィックス付きで RecallPack.LongFacts に追加

**コード例（engine_impl.go）:**
```go
// Knowledge Base (KB) 検索（RAG統合）
if realMgr, ok := e.manager.(*RealConversationManager); ok {
    domain := "general"
    if thread, err := e.manager.GetActiveThread(ctx, sessionID); err == nil && thread != nil {
        domain = thread.Domain
    }

    kbDocs, err := realMgr.SearchKB(ctx, domain, userMessage, 3)
    if err != nil {
        log.Printf("[ConversationEngine] WARN: SearchKB failed: %v", err)
    } else if len(kbDocs) > 0 {
        for _, doc := range kbDocs {
            fact := "[KB] " + doc.Content
            pack.LongFacts = append(pack.LongFacts, fact)
        }
    }
}
```

### 手動検索（テスト・デバッグ用）

```go
// SearchKB を直接呼び出し
docs, err := mgr.SearchKB(ctx, "programming", "Go言語のエラーハンドリング", 5)
if err != nil {
    log.Fatalf("SearchKB failed: %v", err)
}

for i, doc := range docs {
    fmt.Printf("%d. [Score: %.4f] %s\n", i+1, doc.Score, doc.Source)
    fmt.Printf("   %s\n\n", doc.Content[:200])
}
```

### RecallPack での表示

KB検索結果は `RecallPack.LongFacts` に含まれます：

```
RecallPack {
    ShortContext: [...],  // 短期記憶
    MidSummaries: [...],  // 中期記憶（DuckDB）
    LongFacts: [
        "[KB] # Go言語の並行処理入門\n\nゴルーチンとチャネルの基本...\n\nSource: https://...",
        "[KB] # Go Concurrency Patterns\n\n実践的な並行処理パターン...\n\nSource: https://...",
    ],
}
```

---

## 管理・運用

### kb-admin CLI

KB の管理には `kb-admin` コマンドを使用します。

#### 1. KB検索テスト

```bash
kb-admin search programming "Go言語 並行処理"
```

**出力例:**
```
🔍 Searching KB in domain 'programming' for: Go言語 並行処理

Found 2 documents:

--- Document 1 ---
ID:     550e8400-e29b-41d4-a716-446655440000
Source: https://example.com/go-concurrency
Score:  0.8752
Created: 2026-03-07T10:30:00Z
Content:
# Go言語の並行処理入門

ゴルーチンとチャネルの基本的な使い方を解説します。
goroutine は軽量スレッドで、channel で通信します...
```

#### 2. ドキュメント一覧表示

```bash
kb-admin list programming
```

**出力例:**
```
📚 Domain: programming

Found 5 documents:

--- Document 1 ---
ID:      550e8400-e29b-41d4-a716-446655440000
Source:  https://go.dev/doc/
Created: 2026-03-07T10:30:00Z
Content: # Go Documentation
         The Go programming language is an open source project...

--- Document 2 ---
ID:      6ba7b810-9dad-11d1-80b4-00c04fd430c8
Source:  https://www.rust-lang.org/
Created: 2026-03-06T15:20:00Z
Content: # Rust Programming Language
         Rust is a systems programming language...
```

**オプション:**
- デフォルト: 最大100件表示
- Content は最初の150文字まで表示

#### 3. 統計情報確認

```bash
kb-admin stats
```

**出力例:**
```
📊 Knowledge Base Statistics

Found 3 collection(s):

  ✓ programming
    Documents: 45
    Vector Size: 768

  ✓ movie
    Documents: 23
    Vector Size: 768

  ✓ general
    Documents: 12
    Vector Size: 768
```

**表示内容:**
- 存在するコレクション一覧（空コレクションは非表示）
- 各ドメインのドキュメント数
- ベクトル次元数（Embedder設定確認）

#### 4. 古いドキュメント削除

```bash
# 30日より古いドキュメントを削除
kb-admin cleanup programming 30
```

**出力例:**
```
🗑️  Cleanup Policy
Domain: programming
Delete documents older than: 30 days (before 2026-02-05)

Documents before cleanup: 45
Documents after cleanup:  38
✓ Deleted: 7 documents
```

**注意事項:**
- 削除は **不可逆** です（事前バックアップ推奨）
- `created_at` フィールドで判定
- 削除前後のカウントで実際の削除数を確認

### Qdrant Web UI

Qdrant の Web UI でも直接管理できます：

```bash
# Qdrant起動
docker run -p 6333:6333 -p 6334:6334 qdrant/qdrant

# Web UI アクセス
open http://localhost:6333/dashboard
```

**操作:**
- コレクション一覧表示: Collections → `kb_programming` 等
- ドキュメント検索: Search → Query入力
- 削除: Points → Filter → Delete

---

## パフォーマンスチューニング

### 1. Embedding キャッシュ

**問題:** 同一クエリで毎回 Embedding を生成するのは非効率

**対策:**
```go
type EmbeddingCache struct {
    cache map[string][]float32
    mu    sync.RWMutex
}

func (c *EmbeddingCache) GetOrGenerate(ctx context.Context, text string, embedder EmbeddingProvider) ([]float32, error) {
    c.mu.RLock()
    if cached, ok := c.cache[text]; ok {
        c.mu.RUnlock()
        return cached, nil
    }
    c.mu.RUnlock()

    embedding, err := embedder.Embed(ctx, text)
    if err != nil {
        return nil, err
    }

    c.mu.Lock()
    c.cache[text] = embedding
    c.mu.Unlock()

    return embedding, nil
}
```

### 2. バッチ保存

**問題:** Web検索結果を1件ずつ保存すると遅い

**対策:**
```go
// SaveWebSearchToKB 内部でバッチ Upsert
points := make([]*qdrant.PointStruct, 0, len(results))
for _, result := range results {
    // Point 作成
    points = append(points, point)
}

// 一括 Upsert
v.client.Upsert(ctx, &qdrant.UpsertPoints{
    CollectionName: collectionName,
    Points:         points,
    Wait:           &waitTrue,
})
```

### 3. Top-K 調整

**問題:** Top-K が大きすぎると遅い、小さすぎると情報不足

**推奨値:**
- **Chat RAG:** Top-3 〜 Top-5（文脈補完）
- **テスト検索:** Top-10（精度確認）
- **一覧表示:** Top-100（管理用）

### 4. Qdrant インデックスチューニング

**HNSW パラメータ:**
```yaml
# Qdrant 設定ファイル
collections:
  kb_programming:
    hnsw_config:
      m: 16               # グラフの接続数（デフォルト: 16）
      ef_construct: 100   # 構築時の探索幅（デフォルト: 100）
```

**推奨値:**
- 高速検索優先: `m: 8, ef_construct: 64`
- 精度優先: `m: 32, ef_construct: 200`
- バランス: `m: 16, ef_construct: 100`（デフォルト）

### 5. DuckDB クエリ最適化 ✅ 実装済み

**複合インデックスによる高速化:**

```sql
-- GetSessionHistory 用（session_id + ts_start）
CREATE INDEX idx_session_thread_session_ts 
    ON session_thread(session_id, ts_start DESC);

-- SearchByDomain 用（domain + ts_start）
CREATE INDEX idx_session_thread_domain_ts 
    ON session_thread(domain, ts_start DESC);
```

**効果:**
- WHERE + ORDER BY を単一インデックスでカバー
- インデックススキャン回数: 2回 → 1回（50%削減）
- 履歴件数増加時のスケーラビリティ向上

**適用クエリ:**
- `GetSessionHistory`: セッション履歴取得
- `SearchByDomain`: ドメイン内検索

### 6. Redis キャッシュモニタリング ✅ 実装済み

**キャッシュヒット率の可視化:**

```go
// メトリクス取得
metrics := redisStore.GetMetrics()
fmt.Printf("Session: %d hits, %d misses\n", 
    metrics.SessionHits, metrics.SessionMisses)

// ヒット率計算
sessionRate, threadRate := redisStore.GetCacheHitRate()
fmt.Printf("Hit Rate: Session=%.1f%%, Thread=%.1f%%\n", 
    sessionRate, threadRate)
```

**期待値:**
- **Session ヒット率:** 80-90%（同一ユーザーの連続会話）
- **Thread ヒット率:** 60-70%（短期記憶の再利用）

**アクション基準:**
- ヒット率 <50% → **TTL延長検討**（デフォルト: Session=24h, Thread=1h）
- ヒット率 >95% → **メモリ使用量確認**（過剰キャッシュの可能性）

**設定調整:**

```go
// redis_store.go で TTL 調整
r := &RedisStore{
    client: client,
    ttl:    48 * time.Hour, // Session TTL: 24h → 48h
}

// Thread TTL は SaveThread 内で指定
r.client.Set(ctx, key, data, 2*time.Hour) // 1h → 2h
```

### 7. エラーハンドリング（リトライ機構） ✅ 実装済み

**VectorDB操作の自動リトライ:**

```go
// 設定
DefaultRetryConfig{
    MaxAttempts:  3,              // 最大3回試行
    InitialDelay: 100ms,          // 初回待機100ms
    MaxDelay:     2s,             // 最大待機2秒
    Multiplier:   2.0,            // 指数バックオフ
}
```

**適用操作:**
- `SearchKB`: KB検索
- `SaveWebSearchToKB`: Web検索結果保存
- `Recall`: 長期記憶検索

**動作例:**
```
# 一時的なネットワークエラー
Attempt 1: Failed (network timeout)
Wait: 100ms
Attempt 2: Failed (connection refused)
Wait: 200ms
Attempt 3: Success
```

**エラーログ:**
```
SearchKB: VectorDB search failed after retries (domain=programming, query="Go並行処理", topK=5): connection timeout
SaveWebSearchToKB: Failed to save result 3/5 to VectorDB after retries (title="Example"): network error
```

**グレースフルデグラデーション:**
- Embedder未設定時: エラーではなく空結果を返す
- 部分的な保存失敗: 成功分は保存、全失敗時のみエラー

---

## トラブルシューティング

### Q1. KB検索結果が空になる

**原因:**
1. Embedder が未設定
2. ドメインが存在しない
3. 検索クエリの Embedding が失敗

**確認:**
```bash
# 1. Embedder 設定確認
kb-admin search programming "test"
# → "embedder not configured" エラーが出る場合は Embedder 未設定

# 2. ドメイン存在確認
kb-admin stats
# → 対象ドメインが "empty" の場合はドキュメント未保存

# 3. ログ確認
tail -f ~/.rencrow/logs/rencrow.log | grep "SearchKB"
```

### Q2. KB保存が失敗する

**原因:**
1. Qdrant が起動していない
2. Embedding 生成に失敗
3. コレクション作成エラー

**確認:**
```bash
# 1. Qdrant 起動確認
curl http://localhost:6333/
# → {"title":"qdrant - vector search engine",...} が返る

# 2. Embedding 確認
# ログで "failed to generate embedding" を検索

# 3. コレクション確認
curl http://localhost:6333/collections/kb_programming
```

### Q3. 検索精度が低い

**原因:**
1. Embedding モデルの品質
2. ドキュメント内容が不適切（Snippet が短すぎる等）
3. Top-K が小さすぎる

**対策:**
```go
// 1. Embedding モデルを変更
embedder := cohere.NewCohereProvider(apiKey, "embed-multilingual-v3.0")

// 2. Content を充実させる
doc.Content = fmt.Sprintf("# %s\n\n%s\n\n詳細: %s\n\nSource: %s",
    result.Title, result.Snippet, result.FullText, result.Link)

// 3. Top-K を増やす
docs, _ := mgr.SearchKB(ctx, domain, query, 10)  // 3 → 10
```

### Q4. メモリ使用量が多い

**原因:**
1. Embedding キャッシュが肥大化
2. VectorDB のメモリマップ

**対策:**
```go
// 1. キャッシュサイズ制限
type LRUEmbeddingCache struct {
    maxSize int
    // LRU実装
}

// 2. Qdrant のメモリ設定
# docker-compose.yml
services:
  qdrant:
    environment:
      - QDRANT__STORAGE__ON_DISK_PAYLOAD=true  # Payload をディスクに保存
```

---

## まとめ

### チェックリスト

**KB保存:**
- [ ] Web検索結果を `WebSearchResult` に変換
- [ ] 適切なドメインを選定
- [ ] `SaveWebSearchToKB()` を呼び出し
- [ ] エラーハンドリング（ログ記録）

**KB検索（RAG）:**
- [ ] ConversationEngine が Embedder を持っている
- [ ] Thread.Domain が適切に設定されている
- [ ] RecallPack.LongFacts に `[KB]` プレフィックスが含まれる

**運用:**
- [ ] `kb-admin stats` で定期的に確認
- [ ] Qdrant バックアップ（定期スナップショット）
- [ ] 古いドキュメント削除（Phase 4.2 後）

---

**次のステップ:** [実装仕様_会話LLM_v5.md](../04_実装仕様_機能拡張/実装仕様_会話LLM_v5.md) を参照
