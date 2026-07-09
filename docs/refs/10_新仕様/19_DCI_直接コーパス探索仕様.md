# DCI 直接コーパス探索仕様

## 1. 目的

本仕様は、RenCrow において、従来の RAG / VectorDB 検索だけでは取りこぼしやすい証拠を、LLM エージェントが `grep` / `rg` / `find` / `read` / `shell` などを使って、生のコーパスから直接探索する仕組みを定義する。

この方式を RenCrow 内では **DCI（Direct Corpus Interaction）型探索** または **直接コーパス探索** と呼ぶ。

DCI は既存の RAG を置き換えるものではない。

RenCrow における位置づけは以下である。

```text
VectorDB / FTS / BM25 = 思い出すための検索
DCI / grep / raw read = 調べ直すための探索
```

通常会話では既存の記憶想起を優先し、証拠の精密確認、仕様矛盾の調査、過去ログ探索、ファイル横断調査が必要な場合に DCI を起動する。

## 2. 背景

従来の RAG は、文書を embedding 化し、質問に近い上位 k 件を取得して LLM に渡す。

この方式は高速で扱いやすい一方、最初の検索段階で落ちた文書は、後段の LLM が参照できない。

特に以下のようなケースでは、VectorDB 検索だけでは証拠を取りこぼしやすい。

- 質問文と答えを含む文書が意味的に近くない。
- 複数の文書をまたいで手がかりをたどる必要がある。
- 固有名詞、日付、ファイル名、ログ断片、関数名などの字面が重要である。
- 途中で見つけた語句を使って再検索する必要がある。
- 仕様書、ログ、議事録、Markdown 原本の中から正確な記述を探したい。
- RAG の候補文書は出たが、根拠箇所が曖昧である。

DCI は、LLM エージェントが生コーパスに対して直接操作を行うことで、この問題を補う。

## 3. 基本方針

RenCrow では、DCI を以下の方針で導入する。

1. DCI は通常 RAG の代替ではなく、精密探索モードとして実装する。
2. 既存の Recall Orchestrator から必要時に呼び出す。
3. 直接探索の対象は、許可されたローカルコーパスに限定する。
4. 破壊的操作は禁止する。
5. 検索、読取、要約、証拠抽出の各処理には EventId を付与する。
6. 探索結果は必ず Source Registry または Citation Ledger に紐づける。
7. 正式な長期記憶へ昇格する場合は、staging / validator を通す。
8. 失敗時は、検索語、探索範囲、見つからなかった理由を記録する。

## 4. 全体構成

```text
User Message
  ↓
Intent / Domain / Entity Extraction
  ↓
Recall Orchestrator
  ├─ L0 Current Thread
  ├─ L1 Today Memory
  ├─ L2 Monthly Memory
  ├─ L3 Long-term Memory
  ├─ FTS / BM25
  ├─ VectorDB
  └─ DCI Trigger 判定
        ↓
    DCI Explorer
      ├─ Query Planner
      ├─ Grep / ripgrep Search
      ├─ File Reader
      ├─ Context Extractor
      ├─ Evidence Localizer
      └─ Search Trace Logger
        ↓
    Evidence Pack
        ↓
    Persona Prompt Builder
        ↓
    Local LLM / Worker / Coder
        ↓
    Response + Citation + EventId
```

## 5. DCI を起動する条件

DCI は毎ターン起動しない。

### 5.1 ユーザー明示トリガ

ユーザーが以下のように依頼した場合。

```text
探して
grepして
仕様書から確認して
ログを見て
どこに書いてある？
前に話したやつ
矛盾してない？
原文を確認して
ファイルを横断して
```

### 5.2 システム判断トリガ

Recall Orchestrator が以下を検知した場合。

- VectorDB 検索の類似度が低い。
- FTS 検索では候補が多すぎる。
- 複数の候補が矛盾している。
- 固有名詞、関数名、設定名、ファイル名が含まれる。
- ユーザーが正確な根拠を求めている。
- 回答に出典、行番号、該当箇所が必要である。
- 作業対象が仕様書、ログ、コード、設定ファイルである。
- 「記憶」ではなく「記録」を参照すべき内容である。

### 5.3 起動しない条件

以下では DCI を起動しない。

- 一般的な雑談。
- 明らかに短期記憶だけで答えられる話。
- 外部最新情報が必要で、ローカルコーパスに存在しない話。
- ユーザーが推測や意見を求めているだけの場合。
- コーパス探索より Web 確認が適切な場合。
- セキュリティ上、対象ディレクトリにアクセスできない場合。

## 6. 対象コーパス

DCI の探索対象は、RenCrow の Source Registry に登録されたローカルコーパスのみとする。

初期対象は以下。

```text
docs/
  仕様書
  設計メモ
  README
  architecture documents

memory/
  daily digest
  monthly digest
  confirmed long-term memory exports

records/
  完全保存ログ
  conversation records
  task records

staging/
  validator 未通過の候補データ

logs/
  event log
  worker log
  tool execution log

source_registry/
  source metadata
  citation ledger

projects/
  RenCrow 関連コード
  config
  prompt
  rules
```

### 6.1 探索対象外

以下は標準では探索対象外。

```text
.env
秘密鍵
API token
認証情報
ブラウザ Cookie
個人情報を含む未分類ファイル
OS 全体
ユーザーのホームディレクトリ全域
node_modules
venv
大型バイナリ
生成物キャッシュ
```

必要な場合は、Source Registry に明示登録してから探索対象にする。

## 7. DCI Explorer の責務

DCI Explorer は、直接コーパス探索を担当する実行コンポーネントである。

### 7.1 主な責務

- ユーザー発話から探索クエリを作る。
- 探索対象ディレクトリを決める。
- grep / rg / find を実行する。
- ヒットしたファイルの周辺文脈を読む。
- 新しい手がかり語を抽出する。
- 必要に応じて再検索する。
- 証拠箇所を絞り込む。
- Evidence Pack を生成する。
- Search Trace を記録する。

### 7.2 やってはいけないこと

- ファイルの削除。
- ファイルの上書き。
- 権限変更。
- 外部送信。
- 自動コミット。
- 正式 DB への直接書き込み。
- ユーザー記憶の直接確定。
- Source Registry への無審査追加。

## 8. ツール制限

DCI Explorer が使用できる標準ツールは以下。

```text
read_file
list_dir
find
grep
rg
sed -n
head
tail
wc
cat
python read-only script
```

### 8.1 shell 使用ルール

shell を使う場合は、以下を守る。

```text
許可:
  rg
  grep
  find
  sed -n
  awk 読取用途のみ
  python 読取・集計用途のみ

禁止:
  rm
  mv
  cp -f
  chmod
  chown
  curl 外部送信
  wget 外部取得
  git commit
  git push
  pip install
  npm install
  PowerShell の破壊的操作
```

shell は原則として read-only で使う。

## 9. 探索フロー

### 9.1 標準フロー

```text
1. ユーザー発話を受け取る
2. 探索意図を判定する
3. 検索語候補を抽出する
4. 探索対象コーパスを決める
5. rg / grep で広めに検索する
6. ヒットファイルを読む
7. 周辺文脈を抽出する
8. 新しい固有名詞・見出し・キー語を拾う
9. 必要なら再検索する
10. 証拠箇所を絞る
11. Evidence Pack を作る
12. 回答生成へ渡す
13. Search Trace を保存する
```

### 9.2 多段探索フロー

多段階の探索では、検索語を固定しない。

```text
初期クエリ
  ↓
ヒット文書
  ↓
見出し・固有名詞・日付・EventId を抽出
  ↓
派生クエリを生成
  ↓
再検索
  ↓
証拠候補を比較
  ↓
最終 Evidence Pack
```

例:

```text
「DCI の話、RenCrow のどこに関係する？」

初期検索:
  DCI
  Direct Corpus Interaction
  grep
  RAG
  VectorDB

派生検索:
  Recall Orchestrator
  Source Registry
  完全保存
  Evidence Pack
  raw corpus
```

## 10. Evidence Pack

DCI Explorer は、探索結果をそのまま LLM に渡さない。

必ず Evidence Pack に整形する。

### 10.1 Evidence Pack 形式

```json
{
  "event_id": "evt_dci_20260518_000001",
  "query": "DCI RenCrow RAG grep",
  "intent": "仕様書化のための関連箇所探索",
  "corpus_scope": [
    "docs/10_新仕様/",
    "memory/",
    "records/"
  ],
  "evidence": [
    {
      "source_id": "src_spec_memory_001",
      "file_path": "docs/10_新仕様/09_Memory_SourceRegistry仕様.md",
      "heading": "Source Registry",
      "line_start": 120,
      "line_end": 148,
      "snippet": "Source Registry は observed / candidate / validated / promoted の状態遷移を守る...",
      "reason": "DCI探索結果の昇格ルールに関係するため"
    }
  ],
  "derived_terms": [
    "Source Registry",
    "Recall Pack",
    "staging",
    "validator"
  ],
  "confidence": 0.82,
  "limitations": [
    "一部ファイルは未登録のため探索対象外"
  ]
}
```

### 10.2 Evidence Pack に含めるもの

- EventId。
- 検索意図。
- 探索範囲。
- 検索語。
- 派生検索語。
- ファイルパス。
- 見出し。
- 行番号。
- 抜粋。
- 採用理由。
- 信頼度。
- 制限事項。

### 10.3 Evidence Pack に含めないもの

- 大量の原文。
- 秘密情報。
- 未検証の長文要約。
- LLM の推測だけで作った根拠。
- staging 未通過データの確定表現。

## 11. Search Trace

DCI 探索は再現性が重要である。

そのため、すべての探索ステップを Search Trace として保存する。

### 11.1 Search Trace 形式

```json
{
  "event_id": "evt_dci_20260518_000001",
  "started_at": "2026-05-18T12:00:00+09:00",
  "ended_at": "2026-05-18T12:00:07+09:00",
  "actor": "Worker",
  "mode": "dci",
  "steps": [
    {
      "step": 1,
      "tool": "rg",
      "command": "rg \"VectorDB|RAG|Source Registry\" docs/10_新仕様",
      "result_count": 18,
      "status": "ok"
    },
    {
      "step": 2,
      "tool": "read_file",
      "file_path": "docs/10_新仕様/09_Memory_SourceRegistry仕様.md",
      "line_start": 100,
      "line_end": 160,
      "status": "ok"
    }
  ],
  "final_evidence_count": 4,
  "status": "completed"
}
```

### 11.2 保存先

MVP では SQLite に保存する。

```text
SQLite:
  dci_search_trace
  dci_evidence
  dci_query_terms
```

将来は DuckDB + Parquet へ月次アーカイブする。

## 12. DB 設計

### 12.1 dci_search_trace

```sql
CREATE TABLE IF NOT EXISTS dci_search_trace (
  event_id TEXT PRIMARY KEY,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  actor TEXT NOT NULL,
  mode TEXT NOT NULL,
  user_query TEXT,
  corpus_scope TEXT,
  status TEXT NOT NULL,
  final_evidence_count INTEGER DEFAULT 0,
  error_message TEXT
);
```

### 12.2 dci_search_step

```sql
CREATE TABLE IF NOT EXISTS dci_search_step (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
  step_no INTEGER NOT NULL,
  tool TEXT NOT NULL,
  command_text TEXT,
  file_path TEXT,
  result_count INTEGER,
  status TEXT NOT NULL,
  error_message TEXT,
  created_at TEXT NOT NULL
);
```

### 12.3 dci_evidence

```sql
CREATE TABLE IF NOT EXISTS dci_evidence (
  evidence_id TEXT PRIMARY KEY,
  event_id TEXT NOT NULL,
  source_id TEXT,
  file_path TEXT NOT NULL,
  heading TEXT,
  line_start INTEGER,
  line_end INTEGER,
  snippet TEXT NOT NULL,
  reason TEXT,
  confidence REAL DEFAULT 0.0,
  created_at TEXT NOT NULL
);
```

### 12.4 dci_query_terms

```sql
CREATE TABLE IF NOT EXISTS dci_query_terms (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
  term TEXT NOT NULL,
  term_type TEXT,
  parent_term TEXT,
  created_at TEXT NOT NULL
);
```

## 13. 既存 RenCrow モジュールとの関係

### 13.1 Recall Orchestrator

Recall Orchestrator は、DCI を呼び出すかどうかを判断する。

```text
通常:
  L0 → L1 → L2 → L3 → FTS → VectorDB

精密調査が必要:
  L0 → L1 → L2 → L3 → FTS → VectorDB → DCI
```

DCI の結果は Recall Pack に統合する。

### 13.2 Source Registry

DCI の探索対象は Source Registry で管理する。

Source Registry の状態は以下を使う。

```text
observed
candidate
validated
promoted
archived
```

DCI で発見された新しいファイルや記述は、直接 promoted にしない。

必ず candidate または validated として登録し、validator を通す。

### 13.3 Memory Candidate

DCI で発見されたユーザー記憶候補は、直接 `user:<uid>` に入れない。

```text
DCI evidence
  ↓
memory_candidate
  ↓
validator
  ↓
confirmed memory
```

### 13.4 Worker / Coder

DCI の実行主体は原則 Worker とする。

Coder は以下の場合のみ DCI を使える。

- コードベース調査。
- 仕様書との整合確認。
- 変更影響範囲の確認。
- テスト失敗ログの原因調査。

Coder は DCI 結果をもとに patch proposal を作ることはできるが、破壊的変更を直接実行してはいけない。

## 14. 日本語コーパス対応

日本語では単純 grep だけでは表記ゆれを拾いにくい。

そのため、Query Planner は以下の展開を行う。

### 14.1 表記ゆれ展開

```text
ベクトル検索
vector search
VectorDB
ベクターDB
意味検索
埋め込み検索
embedding
```

### 14.2 全角半角

```text
ＡＩ / AI
ＲＡＧ / RAG
ＬＬＭ / LLM
２０２６ / 2026
```

### 14.3 同義語・関連語

```text
仕様書
設計書
ドキュメント
README
設計メモ

記憶
メモリ
memory
長期記憶
ユーザー記憶

探索
検索
grep
rg
直接探索
```

### 14.4 カタカナ・英語

```text
ソースレジストリ
Source Registry

リコールパック
Recall Pack

ステージング
staging
```

## 15. ランキング方針

DCI のヒット結果は、単純な一致数ではなく、以下で再ランクする。

```text
score =
  0.30 * exact_match
+ 0.20 * heading_match
+ 0.20 * proximity_to_keywords
+ 0.15 * source_priority
+ 0.10 * recency
+ 0.05 * user_project_relevance
```

### 15.1 優先する証拠

- 正本仕様書。
- validated / promoted source。
- 見出し直下の記述。
- EventId つきログ。
- 明示的に決定事項と書かれた文書。
- 最新仕様フォルダ内の記述。

### 15.2 優先度を下げる証拠

- 古い議事録。
- staging 未検証データ。
- 断片ログ。
- LLM の仮要約。
- 出典不明メモ。
- deprecated フォルダ。

## 16. 失敗時の挙動

DCI 探索で十分な証拠が見つからない場合、推測で埋めない。

以下を返す。

```text
- 探索した範囲
- 試した検索語
- 見つからなかったもの
- 見つかったが不十分だったもの
- 次に必要な情報
```

例:

```text
docs/10_新仕様/ と records/ は探索しましたが、
「DCI」という語は見つかりませんでした。

ただし「Source Registry」「Recall Pack」「raw log」という関連記述は見つかりました。
そのため、本仕様は既存仕様への追加仕様として扱うのが妥当です。
```

## 17. セキュリティ

DCI はローカルファイルを直接読むため、強い制限が必要である。

### 17.1 基本ルール

- allowlist 方式で探索対象を限定する。
- denylist で秘密情報を除外する。
- shell は read-only に限定する。
- 実行コマンドは Search Trace に保存する。
- 外部送信は禁止する。
- ユーザー記憶候補は即確定しない。
- 生ログから個人情報を抽出した場合は sensitivity を付与する。

### 17.2 denylist 例

```text
.env
*.pem
*.key
*.p12
id_rsa
credentials.json
token.json
cookies.sqlite
secrets/
private/
```

### 17.3 shell command gate

実行前にコマンドを分類する。

```text
read_only: 許可
unknown: 要確認または拒否
write: 拒否
network: 拒否
destructive: 拒否
```

## 18. パフォーマンス方針

DCI は強力だが、巨大コーパスでは遅くなる。

### 18.1 MVP 制限

```text
最大探索時間: 10秒
最大検索ステップ: 8
最大読取ファイル数: 10
最大 Evidence 数: 6
最大抜粋長: 1件あたり800文字
最大総 Evidence Pack: 3000 tokens 以内
```

### 18.2 大規模コーパス対策

200K 文書級の大規模コーパスでは、DCI を単独で走らせない。

以下の順で範囲を絞る。

```text
1. Source Registry metadata
2. FTS / BM25
3. VectorDB
4. directory scope
5. DCI
```

つまり、巨大コーパスでは DCI を「全探索」ではなく「絞り込まれた範囲への精密探索」として使う。

## 19. UI 表示

DCI 実行時、Viewer には以下を表示できるようにする。

```text
ステータス:
  調べ直しています
  原文を確認しています
  証拠を絞り込んでいます

表示項目:
  探索対象
  検索語
  見つかったファイル数
  採用した根拠
  confidence
  EventId
```

ユーザー向けには、低レベルな grep ログは通常表示しない。

ただし、詳細表示を開いた場合は Search Trace を確認できる。

## 20. 返答フォーマット

DCI 結果を使った回答では、以下の構成を推奨する。

```text
結論:
  何が分かったか

根拠:
  どのファイルのどの箇所にあるか

補足:
  見つからなかった点、未確認点

次の扱い:
  仕様に反映する / staging に置く / validator に回す
```

### 20.1 例

```text
結論として、DCI は RenCrow では通常 RAG の代替ではなく、
仕様書・ログ・記録を精密に調べ直す探索モードとして入れるのが妥当です。

根拠は、既存仕様で Source Registry と staging / validator を分けており、
外部取得や記憶候補を無審査で正式 DB に入れない方針があるためです。

そのため、DCI で見つけた証拠も直接 memory に昇格せず、
Evidence Pack として保持し、必要に応じて validator へ渡します。
```

## 21. 実装ファイル案

現行 RenCrow は Go repository であるため、実装候補は以下を基本にする。

```text
internal/application/dci/
  explorer.go
  query_planner.go
  corpus_scope.go
  command_gate.go
  grep_runner.go
  file_reader.go
  evidence_builder.go
  trace_logger.go
  ranking.go

internal/domain/dci/
  evidence.go
  trace.go
  policy.go

internal/infrastructure/persistence/conversation/
  l1_sqlite_dci.go

cmd/picoclaw/
  runtime_dci.go

configs or config schema:
  dci
  corpus_allowlist
  corpus_denylist
```

Python 実装案が必要な場合は別仕様に分離する。現行本線では Go 実装を優先する。

## 22. 設定ファイル案

### 22.1 dci

```yaml
dci:
  enabled: true
  storage: "sqlite" # "jsonl" も選択可能。未指定時は現行互換で jsonl。
  trace_path: "./workspace/logs/dci_search_trace.jsonl"
  sqlite_path: "./workspace/dci.db"
  mode: "read_only"

  limits:
    max_seconds: 10
    max_steps: 8
    max_files_read: 10
    max_evidence: 6
    max_snippet_chars: 800
    max_pack_tokens: 3000

  tools:
    allowed:
      - rg
      - grep
      - find
      - sed
      - head
      - tail
      - wc
      - read_file
    denied:
      - rm
      - mv
      - chmod
      - chown
      - curl
      - wget
      - git push
      - npm install
      - pip install

  trigger:
    explicit_keywords:
      - "探して"
      - "grep"
      - "仕様書"
      - "ログ"
      - "原文"
      - "どこに書いてある"
      - "矛盾"
      - "前に話した"

    intent_types:
      - "evidence_lookup"
      - "spec_verification"
      - "log_investigation"
      - "memory_record_lookup"
      - "source_confirmation"

  ranking:
    exact_match: 0.30
    heading_match: 0.20
    proximity_to_keywords: 0.20
    source_priority: 0.15
    recency: 0.10
    user_project_relevance: 0.05
```

### 22.2 corpus_allowlist

```yaml
allowlist:
  - path: "docs/"
    type: "spec"
    priority: 1.0

  - path: "memory/"
    type: "memory_export"
    priority: 0.8

  - path: "records/"
    type: "record"
    priority: 0.9

  - path: "logs/"
    type: "log"
    priority: 0.6

  - path: "staging/"
    type: "staging"
    priority: 0.5
```

### 22.3 corpus_denylist

```yaml
denylist:
  patterns:
    - ".env"
    - "*.pem"
    - "*.key"
    - "id_rsa"
    - "credentials.json"
    - "token.json"
    - "cookies.sqlite"

  directories:
    - "node_modules/"
    - ".venv/"
    - "venv/"
    - ".git/"
    - "secrets/"
    - "private/"
```

## 23. MVP 実装順

### Phase 1: 読取専用 grep 探索

- corpus allowlist / denylist 作成。
- rg 実行ラッパー。
- read_file ラッパー。
- Search Trace 保存。
- Evidence Pack 生成。

### Phase 2: Recall Orchestrator 統合

- DCI trigger 判定。
- 既存 Recall Trace への Evidence 統合。
- 回答フォーマット適用。
- Viewer への簡易表示。

2026-05-18 時点の実装状況:

- MessageOrchestrator に明示 DCI trigger runtime wiring を追加済み。
- DistributedOrchestrator にも明示 DCI trigger runtime wiring を追加済み。
- `dci.explicit_keywords` に一致した場合、通常の routing / LLM / distributed execution 経路へ流さず `RouteRESEARCH` として DCI Evidence Pack の要約を返す。
- DCI search 失敗時は Chat への fallback 成功扱いにせず、失敗として返す。
- Conversation L1 が有効な場合、DCI evidence / limitation を既存 `RecallTrace` に `Layer=DCI` として保存する。
- RecallTrace 保存に失敗した場合は trace 欠落を成功扱いにせず、失敗として返す。
- Conversation L1 が有効な場合、DCI evidence を `L1StagingKindSearchResult` の `pending` candidate として保存し、validator / promote 前提の `review_required=true` metadata を付与する。
- DCI evidence に file path がある場合は、対応するローカル corpus source を `search_fallback` の disabled Source Registry candidate として自動登録する。Source Registry candidate は `https://local.rencrow.invalid/dci/...` の synthetic URL を使い、`auto_fetch=false` / `review_required=true` metadata を持つ。これは自動 fetch 対象ではなく、staging review / validator の入口として扱う。
- Source Registry Viewer API は staging candidate の一覧、validator 実行、validated staging の news / knowledge / memory promote を扱う。Viewer Memory panel では staging review table から warning 件数と審査状態を確認できる。
- `internal/infrastructure/persistence/dci` に SQLite store を追加済み。`dci_search_trace`, `dci_search_step`, `dci_evidence`, `dci_query_terms` を初期化し、`dci.storage: sqlite` の場合に runtime で使用できる。
- 未指定時は現行互換として JSONL store を使う。
- `dci.max_seconds` と `dci.max_steps` を追加し、探索が巨大 corpus で無制限に進まないようにした。`max_steps` 到達時は Search Trace に `limit` step を残し、Evidence Pack の `limitations` に `max search steps reached` を記録する。
- `dci.max_candidate_files` を追加し、allowlist から読み取り候補を先に収集した上で、query term が path / filename に含まれるファイルを優先して読む最小 ranking を追加した。これにより `max_files_read` が小さい場合でも、walk 順の先頭ファイルだけに偏りにくくする。
- `SourceMetadataRanker` を追加し、Conversation L1 Source Registry の enabled entry に `local_path` / `file_path` / synthetic DCI URL がある場合は、Source Registry metadata を candidate file ranking と Evidence `source_id` に反映できるようにした。ranker が失敗した場合は DCI 自体を Chat fallback 成功扱いにせず、Evidence Pack の `limitations` に `source registry metadata ranking unavailable: ...` を残して通常の allowlist 探索を継続する。
- runtime では Conversation L1 が有効な場合、DCI Explorer に `L1SourceMetadataRanker` を注入する。
- DCI Explorer は候補収集後、読み取り上限で切り捨てる前に候補本文を軽く採点し、query term の本文一致数と複数語一致を使って BM25 相当の content ranking を行う。Tool Harness が設定されている場合、この ranking 用の読み取りも `file_read` tool 経由で行う。
- Conversation L1 が有効な場合、DCI Explorer に `L1KnowledgeFTSCandidateProvider` を注入し、L1 Knowledge FTS から `local_path` / `file_path` / `path` / synthetic DCI URL を持つ knowledge item を candidate file として先に取り込めるようにした。
- `dci.knowledge_fts_domains` で、DCI の local path candidate 注入に使う L1 Knowledge domain を設定できる。未指定時は `general` / `creative` / `news` を使う。
- `VectorKBCandidateProvider` を追加し、Conversation VectorDB KB の semantic search 結果から `local_path` / `file_path` / `path` metadata、または DCI synthetic URL を持つ Document を candidate file として取り込めるようにした。
- DCI Explorer は FTS と VectorDB semantic の複数 candidate provider を併用でき、provider 由来の seed rank を file ranking と Evidence `source_id` に反映する。
- runtime では Conversation Manager が有効な場合、DCI Explorer に `VectorKBCandidateProvider` を注入する。
- MVPとしての DCI 直接コーパス探索は実装済み。今後は実機 VectorDB / Qdrant E2E、ranking weight 調整、大規模 corpus での provider tuning を拡張候補として扱う。

### Phase 3: Source Registry 統合

- DCI で見つけた source の登録。
- candidate / validated / promoted 状態管理。
- Citation Ledger 連携。

### Phase 4: 日本語クエリ展開

- 表記ゆれ辞書。
- 同義語辞書。
- 全角半角正規化。
- 固有名詞抽出。

### Phase 5: 精密調査モード

- 多段検索。
- 派生語検索。
- Evidence 再ランキング。
- 仕様矛盾検出。
- ログ原因調査。

## 24. 成功指標

DCI 導入後は以下を計測する。

```text
dci_trigger_count
dci_success_rate
average_search_steps
average_latency
evidence_precision
user_acceptance_rate
false_positive_rate
no_evidence_rate
vector_search_fallback_rate
```

特に重要な指標:

```text
証拠発見率
根拠箇所の正確性
ユーザーが「それ」と認めた率
RAG単独で見つからなかった証拠を見つけた率
```

## 25. 設計上の結論

DCI は RenCrow の中心機能ではなく、記憶 OS における「原文確認能力」である。

RenCrow において、VectorDB は思い出すための層であり、DCI は調べ直すための層である。

したがって、最終構成は以下とする。

```text
通常想起:
  L0 / L1 / L2 / L3 / VectorDB / FTS

精密調査:
  DCI / grep / raw read / Evidence Pack

正式記憶化:
  staging / validator / Source Registry / promoted memory
```

この構成により、RenCrow は単に記憶を検索するだけでなく、必要なときに原文へ戻って確認できる。

これは「記憶」と「記録」を分ける RenCrow の方針とも一致する。

## 26. 既存 RAG との使い分け

| 用途 | 優先方式 |
| --- | --- |
| 雑談中の軽い想起 | VectorDB |
| 過去の関連話題 | VectorDB + L1/L2 |
| 作品名・用語検索 | FTS / BM25 |
| 仕様書の正確な箇所確認 | DCI |
| ログ原因調査 | DCI |
| 多段階の証拠探索 | DCI |
| 巨大静的コーパスの一次候補抽出 | VectorDB / FTS |
| 候補文書内の精密確認 | DCI |
| 最新 Web 情報 | Web / Source Registry |
| 正式記憶化 | validator 経由 |

## 27. 最小実装の判断

最初から高度な agent 探索にしない。

MVP では以下で十分である。

```text
1. 明示トリガで DCI 起動
2. allowlist 内を rg 検索
3. 上位ファイルを read
4. 周辺文脈を Evidence Pack 化
5. Search Trace 保存
6. 回答に根拠として反映
```

この段階で効果が出てから、多段探索、派生語生成、矛盾検出を追加する。
