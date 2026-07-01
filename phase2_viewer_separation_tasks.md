# Phase 2: Viewer ハンドラー責務分離タスクリスト

**作成日**: 2026-06-12  
**目的**: Viewer ハンドラーのビジネスロジックを Application層に移動し、Clean Architecture の境界を明確にする  
**優先度**: Phase 4 のカバレッジ向上前に実施

---

## 現状分析

### 既に完了した分離
✅ **complexity 候補パターン抽出** (Phase 1で完了)
- 移動元: `internal/adapter/viewer/complexity_hotspot_handler.go`
- 移動先: `internal/application/complexity/candidate_patterns.go`
- 検証: テスト追加済み、動作確認済み

### 残りの大きなハンドラー（600行以上）

| ハンドラー | 行数 | 責務混在の疑い | 優先度 |
|-----------|------|----------------|--------|
| `complexity_hotspot_handler.go` | 1034行 | **高**（一部分離済み） | 🔴 最高 |
| `movie_catalog_handler.go` | 939行 | **高** | 🔴 高 |
| `hobby_graph_handler.go` | 895行 | **高** | 🔴 高 |
| `revenue_handler.go` | 750行 | 中 | 🟠 中 |
| `debug_system_handler.go` | 749行 | 中 | 🟠 中 |
| `sandbox_handler.go` | 678行 | 中 | 🟡 低 |
| `ai_workflow_handler.go` | 674行 | 中 | 🟡 低 |

---

## Phase 2 タスク（優先順位順）

### Task 1: complexity_hotspot_handler.go 完全分離（1日）

**現状**: 候補パターン抽出は分離済みだが、残りのロジックが混在

#### 1.1 現状確認
```bash
# ハンドラーの内容を確認
cat internal/adapter/viewer/complexity_hotspot_handler.go | head -100

# 既に分離された部分を確認
cat internal/application/complexity/candidate_patterns.go
```

#### 1.2 分離すべきビジネスロジックの特定

**確認項目**:
- [ ] Complexity スコア計算ロジック
- [ ] Hotspot 判定ロジック
- [ ] データ集計・フィルタリングロジック
- [ ] レポート生成ロジック

**残すべき HTTP 処理**:
- [ ] HTTP Request パース（クエリパラメータ、リクエストボディ）
- [ ] HTTP Response 生成（JSON/HTML）
- [ ] HTTP エラーハンドリング（4xx/5xx）

#### 1.3 Application層への移動

**新規ファイル作成**:
```
internal/application/complexity/
├── candidate_patterns.go (既存)
├── hotspot_detector.go (新規)
├── complexity_scorer.go (新規)
└── report_generator.go (新規)
```

**移動する関数例**:
- `detectHotspots()` → `internal/application/complexity/hotspot_detector.go`
- `calculateComplexityScore()` → `internal/application/complexity/complexity_scorer.go`
- `generateReport()` → `internal/application/complexity/report_generator.go`

#### 1.4 ハンドラーをシンプルに

**リファクタリング後のハンドラー構造**:
```go
func HandleComplexityHotspot(w http.ResponseWriter, r *http.Request) {
    // 1. リクエストパース
    params := parseComplexityRequest(r)
    
    // 2. Application層のサービスを呼び出し
    hotspots := complexity.DetectHotspots(params)
    score := complexity.CalculateScore(hotspots)
    report := complexity.GenerateReport(hotspots, score)
    
    // 3. レスポンス生成
    writeJSONResponse(w, report)
}
```

#### 1.5 テスト追加

**Application層のテスト**:
```bash
# 新規テストファイル作成
touch internal/application/complexity/hotspot_detector_test.go
touch internal/application/complexity/complexity_scorer_test.go
touch internal/application/complexity/report_generator_test.go
```

**Adapter層のテスト**（HTTP レベル）:
```bash
# HTTPハンドラーのテスト
touch internal/adapter/viewer/complexity_hotspot_handler_test.go
```

#### 1.6 検証

```bash
# ユニットテスト
go test ./internal/application/complexity -v
go test ./internal/adapter/viewer -run TestComplexityHotspot -v

# 統合テスト
go test ./test/e2e -run Complexity -v

# 全テスト
go test ./...

# API レスポンスが変更前後で同一か確認
# （既存のE2Eテストまたは手動テスト）
```

---

### Task 2: movie_catalog_handler.go 分離（1-2日）

#### 2.1 現状分析

**確認コマンド**:
```bash
wc -l internal/adapter/viewer/movie_catalog_handler.go
grep -n "func " internal/adapter/viewer/movie_catalog_handler.go | head -20
```

#### 2.2 分離すべきビジネスロジックの特定

**予想されるロジック**:
- [ ] Movie カタログの検索・フィルタリング
- [ ] 推薦アルゴリズム
- [ ] メタデータ集計
- [ ] ドメイングラフ生成

#### 2.3 Application層への移動

**新規ファイル作成**:
```
internal/application/moviecatalog/
├── catalog_service.go (新規または既存を拡張)
├── recommendation.go (新規)
└── metadata_aggregator.go (新規)
```

#### 2.4 ハンドラーをシンプルに

**リファクタリング後の構造**:
```go
func HandleMovieCatalog(w http.ResponseWriter, r *http.Request) {
    // 1. リクエストパース
    query := parseMovieQuery(r)
    
    // 2. Application層のサービスを呼び出し
    movies := moviecatalog.Search(query)
    recommendations := moviecatalog.GenerateRecommendations(movies)
    
    // 3. レスポンス生成
    writeJSONResponse(w, recommendations)
}
```

#### 2.5 テスト追加

```bash
# Application層のテスト
go test ./internal/application/moviecatalog -v

# Adapter層のテスト
go test ./internal/adapter/viewer -run TestMovieCatalog -v
```

---

### Task 3: hobby_graph_handler.go 分離（1-2日）

#### 3.1 現状分析

**確認コマンド**:
```bash
wc -l internal/adapter/viewer/hobby_graph_handler.go
grep -n "func " internal/adapter/viewer/hobby_graph_handler.go | head -20
```

#### 3.2 分離すべきビジネスロジックの特定

**予想されるロジック**:
- [ ] Hobby グラフ生成
- [ ] ノード・エッジの計算
- [ ] グラフ集計・分析

#### 3.3 Application層への移動

**新規ファイル作成**:
```
internal/application/hobbygraph/
├── graph_builder.go (新規または既存を拡張)
├── node_analyzer.go (新規)
└── edge_calculator.go (新規)
```

#### 3.4 ハンドラーをシンプルに

#### 3.5 テスト追加

---

## 実装順序と時間配分

### Week 1
- **Day 1**: Task 1.1-1.3（complexity_hotspot_handler 分析・移動）
- **Day 2**: Task 1.4-1.6（complexity_hotspot_handler テスト・検証）
- **Day 3**: Task 2.1-2.3（movie_catalog_handler 分析・移動）

### Week 2
- **Day 4**: Task 2.4-2.5（movie_catalog_handler テスト・検証）
- **Day 5**: Task 3.1-3.3（hobby_graph_handler 分析・移動）
- **Day 6**: Task 3.4-3.5（hobby_graph_handler テスト・検証）

---

## 完成条件

### 必須条件
- ✅ 各ハンドラーが HTTP Request/Response 変換のみを担当
- ✅ ビジネスロジックが Application層に移動
- ✅ Application層のユニットテスト追加（カバレッジ80%以上）
- ✅ Adapter層の HTTP テスト追加
- ✅ API レスポンスが変更前後で同一
- ✅ 全テスト通過

### 副次的な効果
- ✅ internal全体のカバレッジ向上（71.7% → 75-78%程度と予想）
- ✅ Viewer パッケージのカバレッジ向上（63.1% → 70%以上）
- ✅ Clean Architecture 境界の明確化

---

## 検証方法

### 1. アーキテクチャ検証

**依存方向の確認**:
```bash
# Adapter層 → Application層の依存は OK
# Application層 → Adapter層の依存は NG

# 確認コマンド
grep -r "internal/adapter" internal/application/complexity
grep -r "internal/adapter" internal/application/moviecatalog
grep -r "internal/adapter" internal/application/hobbygraph
# → 結果が空であることを確認
```

### 2. 機能検証

**E2E テスト**:
```bash
# Complexity
go test ./test/e2e -run Complexity -v

# Movie Catalog（存在する場合）
go test ./test/e2e -run Movie -v

# Hobby Graph（存在する場合）
go test ./test/e2e -run Hobby -v
```

**手動テスト**（サーバー起動が可能な場合）:
```bash
# サーバー起動
PICOCLAW_CONFIG=config/config.yaml.example ./build/picoclaw run

# API テスト
curl http://localhost:8080/viewer/complexity/hotspot
curl http://localhost:8080/viewer/movie-catalog
curl http://localhost:8080/viewer/hobby-graph
```

### 3. カバレッジ検証

```bash
# Application層
go test ./internal/application/complexity -cover
go test ./internal/application/moviecatalog -cover
go test ./internal/application/hobbygraph -cover

# Adapter層
go test ./internal/adapter/viewer -cover

# 全体
go test ./internal/... -coverprofile=/tmp/rencrow_internal_after_viewer.cover
go tool cover -func=/tmp/rencrow_internal_after_viewer.cover | grep total
```

---

## リスクと対策

### リスク 1: API レスポンスの変更
**対策**: 
- リファクタリング前後で同一のレスポンスを返すことをテストで保証
- E2E テストを先に書く（Golden file testing）

### リスク 2: 既存機能の破壊
**対策**:
- 小さく段階的に実施（1ハンドラーずつ）
- 各ハンドラー完了時に全テスト実行

### リスク 3: 時間不足
**対策**:
- 優先度順に実施（complexity → movie → hobby）
- 1つ完了した時点で一旦停止・評価も可能

---

## 完了報告フォーマット

各ハンドラー完了時に以下をレポート：

```markdown
## Task X 完了報告

### 実施内容
- 移動したファイル: xxx
- 新規作成ファイル: yyy
- ハンドラーの行数: 1034行 → 250行（-784行）

### テスト結果
- Application層テスト: PASS (カバレッジ 85%)
- Adapter層テスト: PASS (カバレッジ 75%)
- 全テスト: PASS

### API 互換性
- レスポンス変更: なし
- E2E テスト: PASS

### 次のステップ
Task X+1 に進む
```

---

**作成者**: Claude Sonnet 4.5  
**最終更新**: 2026-06-12
