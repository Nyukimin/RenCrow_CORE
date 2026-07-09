# Complex Story Mode - Archived

**アーカイブ日**: 2026-03-26
**理由**: デッドコード削減・保守性向上

---

## アーカイブされたファイル

| ファイル | 行数 | 説明 |
|---------|------|------|
| `story_mode.go` | 1,668 | 8ステップパイプラインによる物語生成（メイン実装） |
| `story_data.go` | 147 | StorySource, StoryEntry 等のデータ構造 |
| `story_specs.go` | 16 | ストーリー仕様定義 |
| `story_dump.go` | 477 | デバッグ用ダンプ機能 |
| `orchestrator_story_test.go` | 1,587 | Complex Story Mode のテスト（92テスト） |
| `cmd_test-story/main.go` | 473 | テスト・デバッグ用CLIツール（dump-plan, preview-run, scan等） |
| **合計** | **4,368行** | |

---

## アーカイブ理由

### 1. デッドコード
- `RunStorySession()` への参照がゼロ（定義のみ、呼び出しなし）
- `orchestrator.go` では `story_mode_simple.go` の `RunSimpleStorySession()` のみ使用（line 494）

### 2. 複雑性
- 8ステップのパイプライン（Source選択 → 分析 → Plan → Adaptation → Beat → Draft → Revision → 配信）
- 品質課題（draft失敗率高、開幕問題43%）- メモリ記録より

### 3. 保守性
- 1,668行の単一ファイル（God Object化）
- テストカバレッジ不足（`story_mode_test.go` 不在）

### 4. 代替実装
- **story_mode_simple.go** (170行) が稼働中
- ワンショット生成（1 LLM呼び出し）でシンプル・高速
- 10種類の昔話 × 15種類の主人公改変 = 150パターン

---

## 機能比較

| 項目 | Complex Story Mode | Simple Story Mode |
|------|-------------------|-------------------|
| 実装 | 8ステップパイプライン | ワンショット生成 |
| コード行数 | 2,308行 | 170行 |
| LLM呼び出し | 最大8回 | 1回 |
| 生成時間 | 長い（数分） | 短い（数十秒） |
| 品質安定性 | 不安定（draft失敗率高） | 安定 |
| 保守性 | 低い | 高い |
| テスト | なし | なし（要追加） |

---

## 復元方法

必要になった場合の復元手順:

```bash
# 1. ファイルを元の場所に戻す
git mv internal/application/idlechat/archive/complex_story_mode/story_mode.go \
       internal/application/idlechat/
git mv internal/application/idlechat/archive/complex_story_mode/story_data.go \
       internal/application/idlechat/
git mv internal/application/idlechat/archive/complex_story_mode/story_specs.go \
       internal/application/idlechat/
git mv internal/application/idlechat/archive/complex_story_mode/story_dump.go \
       internal/application/idlechat/

# 2. orchestrator.go で RunStorySession() を呼び出すコード追加
# 例: checkAndStartChat() の switch 文に case "story" を追加

# 3. テスト復元
# orchestrator_test.go に StorySource 関連のテストを復元
# （削除されたテストは git history から取得可能）

# 4. ビルド確認
go build ./cmd/rencrow
go test ./internal/application/idlechat/
```

---

## 技術的背景

### Complex Story Mode の設計思想

**8ステップパイプライン**:
1. **Source選択**: アクティブストーリーからランダム選択
2. **Source分析**: `analyzeStorySource()` で骨格・タブー・余韻を抽出
3. **Rewrite Plan**: スタイル・ジャンル・視点・トーン・結末を決定
4. **Adaptation Plan**: 世界設定の改変方針
5. **Beat Plan**: 起承転結のビート構造（3-5ビート）
6. **Draft生成**: ビート単位でLLM呼び出し、段階的生成
7. **Revision**: 全体の整合性チェック・推敲
8. **Viewer配信**: 段落単位でストリーミング

**品質課題**（メモリ `story_mode_pipeline_status.md` より）:
- Q1（開幕）: 43% 不合格 - 唐突な開幕、主人公名不明
- Q3（意外性）: 一部不足 - 予定調和な展開
- Q6（余韻）: 一部不足 - 駆け足な結末

### Simple Story Mode の設計思想

**ワンショット生成**:
- 昔話の「骨格」（事件 → 解決 → オチ）は保持
- 主人公改変によって世界設定・常識・反応が連鎖的に変化
- 大げさ・面白さ優先（笑えるくらいでOK）
- 2000文字前後、テンポ重視

**利点**:
- シンプルな実装（170行）
- 高速生成（1回のLLM呼び出し）
- 保守しやすい
- 品質が安定（複雑なパイプラインの失敗リスクなし）

---

## 参考資料

### 削除されたテスト一覧

`orchestrator_test.go` から削除されたテスト（git history から復元可能）:
- `TestAnalyzeStorySource_MomotaroExtractsTabooAndAftertaste`
- `TestAnalyzeStorySource_IssunExtractsTabooAndAftertaste`
- `TestAnalyzeStorySource_SnowwhiteKeepsTabooAndAftertaste`
- `TestAnalyzeStorySource_AladdinKeepsSpecificStructure`
- `TestStorySkeleton_RedridingHasSpecificBeatCount`
- `TestStorySkeleton_KasajizoHasMoralStructure`
- `TestBuildStoryRewritePlan_*` (複数)

### 関連ドキュメント

- ストーリーモード仕様: `docs/06_実装ガイド進行管理/20260321_ストーリーモード_仕様と実装状況.md`
- メモリ記録: `.claude/projects/.../memory/story_mode_pipeline_status.md`
- アクティブストーリーデータ: `data/story/*.json` (17作品)
- アーカイブストーリーデータ: `data/story/archive/*.json` (10作品)

---

**このアーカイブは将来の参考・復元用に保持されています。**
**現在は story_mode_simple.go を使用してください。**
