# TOOL_CONTRACT.md -- RenCrow ツール契約

**作成日**: 2026-03-05
**バージョン**: 1.0
**ステータス**: 正本（ツール設計・受領判断の一次参照）
**配置**: リポジトリルート直下（唯一の正）

---

## 目次

- [0. この文書の位置づけ](#0-この文書の位置づけ)
- [1. 入出力の統一](#1-入出力の統一)
- [2. 安全レール](#2-安全レール)
- [3. 予測可能性](#3-予測可能性)
- [4. 増殖に耐える運用](#4-増殖に耐える運用)
- [5. Definition of Done（完成条件）](#5-definition-of-done完成条件)
- [6. ツール受領フロー](#6-ツール受領フロー)
- [7. ファイル配置規約](#7-ファイル配置規約)
- [付録A: エラー JSON スキーマ](#付録a-エラー-json-スキーマ)
- [付録B: ツール雛形](#付録b-ツール雛形)

---

## 0. この文書の位置づけ

### 0.1 なぜ先にルールを置くのか

Coder が大量にツールを作り始めると、何も決めていない状態では以下が起こる:

- 同じことをするツールが増える
- 引数や出力形式がバラバラになる
- 安全確認の作法が崩れる
- どれが最新版か分からない

結果、RenCrow 全体の予測可能性が落ちて、メンテも難しくなる。

### 0.2 設計思想

**「重いガバナンス」ではなく、最低限の"ツール契約"を先に決める。」**

- ルールと実装を同じ変更単位（PR / コミット）で動かす
- Coder にルールを「読ませる」より「踏ませる」（雛形に契約を埋め込む）
- Worker がツール契約のゲートキーパーになる（新ツール受領 → チェック → 登録）

### 0.3 RenCrow における責務

| 役割 | ツール契約に対する責務 |
|------|---------------------|
| **Worker** | 契約を握り、新ツール受領時にチェックリストを通す。登録のゲートキーパー |
| **Coder** | 契約に従ってツールを作る。雛形から複製して開始する |
| **Chat** | ツールの存在を知り、ユーザーに説明できる。実装詳細には踏み込まない |

---

## 1. 入出力の統一（最優先）

### 1.1 入力

| ルール | 詳細 |
|--------|------|
| **JSON が一次経路** | 入力は原則 JSON 1本（`--json` or stdin） |
| 人間向けフラグは補助 | 便利フラグはあってもいいが、JSON 経路を必ず一次経路にする |
| 非対話 | プロンプトで質問して止まらない。必要な情報は必ず入力に含める |

### 1.2 出力

| ルール | 詳細 |
|--------|------|
| **JSON がデフォルト** | 出力は原則 JSON（`--output json` がデフォルトでもよい） |
| stdout = 結果 | 標準出力は結果のみ |
| stderr = ログ | 標準エラーはログのみ。**混ぜない** |

### 1.3 エラー

エラーも JSON で返す。形式は固定:

```json
{
  "error": {
    "code": "VALIDATION_FAILED",
    "message": "entity_id is required",
    "details": {
      "field": "entity_id",
      "constraint": "required"
    }
  }
}
```

エラーコードの命名規約:

| コード | 意味 |
|--------|------|
| `VALIDATION_FAILED` | 入力バリデーション失敗 |
| `NOT_FOUND` | 対象リソースが存在しない |
| `PERMISSION_DENIED` | 権限不足 |
| `CONFLICT` | 競合（既存リソースとの衝突） |
| `TIMEOUT` | タイムアウト |
| `INTERNAL_ERROR` | 内部エラー（予期しない障害） |
| `RATE_LIMITED` | レート制限超過 |
| `DRY_RUN_ONLY` | dry-run モードのため実行せず |

---

## 2. 安全レール

**前提: 「エージェントは自信満々に間違える」**

### 2.1 破壊的操作

| ルール | 詳細 |
|--------|------|
| **dry-run 必須** | 書き込み系は必ず `--dry-run`（または `mode=plan`）を実装する |
| 差分表示 | 実行前に差分や対象件数を返す |

dry-run の出力例:

```json
{
  "mode": "plan",
  "actions": [
    {"type": "update", "target": "entity:movie:123", "field": "title", "from": "旧タイトル", "to": "新タイトル"},
    {"type": "delete", "target": "entity:movie:456", "reason": "duplicate"}
  ],
  "summary": {
    "updates": 1,
    "deletes": 1,
    "total_affected": 2
  }
}
```

### 2.2 入力バリデーション

**必須**。以下は無条件で弾く:

| 脅威 | 対策 |
|------|------|
| パストラバーサル | `../` を含むパスは拒否 |
| 制御文字 | `\x00`-`\x1f`（改行・タブ以外）を拒否 |
| 二重エンコード | `%25` 等のダブルエンコードを検出して拒否 |
| ID 汚染 | ID に `?`, `#`, `/`, `\` が混入していたら拒否 |
| 長大入力 | フィールドごとに最大長を設定 |

### 2.3 取得系の制約

| ルール | 詳細 |
|--------|------|
| **フィールド制限** | `fields` パラメータで返却フィールドを制限できる設計にする |
| **ページング** | `limit` + `offset` or `cursor` を必ず実装する |
| **デフォルト上限** | 巨大レスポンスをデフォルトで返さない（デフォルト limit: 100） |

#### 2.3.1 `fields` パラメータの適用除外

返却データが**固定スキーマかつ小規模**なツールは `fields` パラメータを省略できる。

適用除外の条件（すべて満たすこと）:
1. 返却フィールドが固定（動的に増えない）
2. 1レスポンスあたりのデータ量が十分小さい（目安: 10KB 以下）
3. フィールド単位で取捨選択する実用上の需要がない

**適用除外の例**: `file_read`（テキスト1本）、`file_list`（名前一覧）、`shell`（stdout）、`web_search`（title/link/snippet 固定3項目）

**`fields` が必要になる場合**: 構造化エンティティを返すツール（DB 検索、API フェッチャー等）で、呼び出し側が不要なフィールドを除外したいとき。該当ツール追加時に実装する。

---

## 3. 予測可能性

**「同じ入力 → 同じ出力」に寄せる。**

### 3.1 非対話

- プロンプトで質問して止まらない
- 必要な情報は必ず入力に含める
- 不足があればエラー JSON を返す（待たない）

### 3.2 時間・乱数の扱い

| ルール | 詳細 |
|--------|------|
| `generated_at` | 時間や乱数に依存する出力には必ず `generated_at` を含める |
| 並び順固定 | デフォルトの並び順を固定する（差分が出ないように） |
| シード指定 | 乱数を使う場合は `seed` パラメータを受け付ける（再現可能性） |

### 3.3 タイムアウトとリトライ

| ルール | 詳細 |
|--------|------|
| タイムアウト | 外から設定できるようにする（デフォルト: 30秒） |
| リトライ | 方針を固定する（デフォルト: 3回、指数バックオフ） |
| **無限待ち禁止** | タイムアウトなしの呼び出しは許可しない |

---

## 4. 増殖に耐える運用

### 4.1 ID とバージョン

| ルール | 詳細 |
|--------|------|
| **tool_id 必須** | 全ツールに一意の ID を付ける（例: `tmdb_fetcher`, `entity_merger`） |
| **version 必須** | セマンティックバージョニング（例: `1.0.0`） |
| 廃止宣言 | `deprecated: true` + `replaced_by: <tool_id>` で置き換え先を明示 |

### 4.2 単一責務

- **やること**を1つに絞る
- 巨大万能ツールを作らない
- 2つのことをするツールは2つに分ける

### 4.3 SKILL 同梱

各ツールに短い SKILL.md を同梱する:

```yaml
---
tool_id: tmdb_fetcher
version: 1.0.0
category: etl
dry_run: true
invariants:
  - "dry-run 必須（書き込み系）"
  - "fields パラメータ必須（取得系）"
  - "JSON 入出力"
---

# tmdb_fetcher

TMDb API から映画・ドラマ情報を取得し、Core entities にマッピングする。
```

### 4.4 廃止と置き換え

増えるのは前提なので、**減らし方も先に決める**:

```json
{
  "tool_id": "old_fetcher",
  "version": "1.2.0",
  "deprecated": true,
  "deprecated_at": "2026-04-01",
  "replaced_by": "tmdb_fetcher",
  "removal_date": "2026-06-01"
}
```

---

## 5. Definition of Done（完成条件）

Coder が新ツールを出すたびに、以下のチェックリストを通す。

### 5.1 必須（全ツール共通）

- [ ] **JSON 入力** -- JSON 経路が一次経路として実装されている
- [ ] **JSON 出力** -- 結果が JSON で返る
- [ ] **JSON エラー** -- エラーが `error.code` / `error.message` / `error.details` で返る
- [ ] **入力バリデーション** -- パストラバーサル・制御文字・ID汚染を弾く
- [ ] **SKILL.md** -- 不変条件を記載した SKILL が同梱されている
- [ ] **テスト** -- サンプル入出力の最低 1 ケースがある

### 5.2 書き込み系のみ

- [ ] **dry-run (plan)** -- `mode=plan` で差分・対象件数を返す

### 5.3 取得系のみ

- [ ] **フィールド制限** -- `fields` パラメータで返却フィールドを制限できる（§2.3.1 の適用除外条件を満たす場合は省略可）
- [ ] **ページング** -- `limit` + `offset` or `cursor` を実装している
- [ ] **デフォルト上限** -- デフォルト limit が設定されている（最大でも 100）

---

## 6. ツール受領フロー

Worker がゲートキーパーとして機能する。

```
Coder: ツール作成
  ├── 雛形（templates/tool/）から複製
  ├── 実装
  └── SKILL.md 作成

Worker: 受領チェック
  ├── Definition of Done チェックリスト照合
  ├── 既存ツールとの重複確認
  ├── tool_id / version の一意性確認
  └── サンプルテスト実行

Worker: 登録
  ├── ToolRegistry に登録
  ├── Git commit（ツール + SKILL + テスト）
  └── ログ記録（event_id 付き）
```

**拒否の場合**: 不足項目を明示して Coder に差し戻す。

---

## 7. ファイル配置規約

```
picoclaw_multiLLM/
├── TOOL_CONTRACT.md              # <-- この文書（根本ルール。唯一の正）
├── templates/
│   └── tool/                     # ツール雛形（契約埋め込み済み）
│       ├── main.go.tmpl
│       ├── SKILL.md.tmpl
│       └── test_sample.json
├── internal/
│   └── infrastructure/
│       └── tools/                # ツール実装
│           ├── runner.go         # ToolRunner（既存）
│           ├── runner_test.go
│           └── ...
├── workspace/
│   └── skills/                   # SKILL.md（各ツールの運用スキル）
│       ├── web_search.md
│       └── ...
└── docs/
    └── tooling/                  # 詳細ドキュメント（補助）
        ├── SECURITY.md           # セキュリティ詳細
        └── EXAMPLES.md           # 入出力サンプル集
```

### 7.1 正本と参照の役割分担

| 場所 | 役割 | 内容 |
|------|------|------|
| `TOOL_CONTRACT.md`（リポジトリルート） | **正本** | 根本ルール。入出力・安全・DoD |
| `workspace/skills/` | 運用スキル | 各ツールの SKILL.md |
| `templates/tool/` | 雛形 | 契約を埋め込んだテンプレート |
| `docs/tooling/` | 補助 | セキュリティ詳細、サンプル集 |
| Obsidian（外部） | 参照コピー | 閲覧・ナレッジ検索用（正本は Git） |

---

## 付録A: エラー JSON スキーマ

```json
{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["error"],
  "properties": {
    "error": {
      "type": "object",
      "required": ["code", "message"],
      "properties": {
        "code": {
          "type": "string",
          "enum": [
            "VALIDATION_FAILED",
            "NOT_FOUND",
            "PERMISSION_DENIED",
            "CONFLICT",
            "TIMEOUT",
            "INTERNAL_ERROR",
            "RATE_LIMITED",
            "DRY_RUN_ONLY"
          ]
        },
        "message": {
          "type": "string"
        },
        "details": {
          "type": "object",
          "additionalProperties": true
        }
      }
    }
  }
}
```

---

## 付録B: ツール雛形

### B.1 SKILL.md テンプレート

```yaml
---
tool_id: <tool_id>
version: 1.0.0
category: <etl|query|mutation|admin>
dry_run: <true|false>
invariants:
  - "JSON 入出力"
  - "<追加の不変条件>"
deprecated: false
replaced_by: null
---

# <tool_id>

<ツールの1行説明>

## 入力

<JSON サンプル>

## 出力

<JSON サンプル>

## 不変条件

- <守るべきルール>
```

### B.2 テストサンプル JSON

```json
{
  "test_name": "basic_usage",
  "input": {
    "...": "..."
  },
  "expected_output": {
    "...": "..."
  },
  "expected_error": null
}
```

---

**最終更新**: 2026-03-05
**バージョン**: 1.0
**メンテナンス**: ツール種別の追加・ルール変更時は必ずこの文書を更新すること
