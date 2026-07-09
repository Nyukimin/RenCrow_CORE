# DeepSeek キャッシュ設計 仕様（Coding LLM向け）

この文書は、3レイヤー構成（Chat／Worker／Coder）において、DeepSeekのKVキャッシュ（Prefix cache）を最大限に効かせて **コストを下げつつ安定運用するための仕様** をMarkdownで固定する。

---

## 0. ゴール

- DeepSeek APIの **cache hit** を増やし、入力トークン単価を下げる。
- 「同じリポジトリを反復して改修する」運用で、**毎回の巨大な文脈送信を“固定prefix化”**し、末尾に差分だけを載せる。
- 役割分担（Chat／Worker／Coder）を崩さず、**ルーティングとI/O（JSON/Markdown）を安定**させる。

---

## 1. 前提（DeepSeek KV Cacheの扱い）

- キャッシュは **先頭（prefix）が一致**しているほど効く。
- キャッシュ単位は **64トークン単位**（短い断片は効きにくい）。
- キャッシュは **best-effort**（必ず当たる保証はない）。
- キャッシュ構築・保持は永続ではなく、利用されないと一定時間で失効しうる。

> したがって「キャッシュ設計」とは、**毎回同一の“固定prefix”を、同一の順序と文字列で送る**設計である。

---

## 2. 構成要素（メッセージ設計）

Coding LLM（DeepSeek）へ渡す入力は、必ず次の2層で構成する。

### 2.1 固定prefix（絶対不変）
- `CODING_RULES_v1`：コーディング方針、出力形式、禁止事項、判断ルール
- `REPO_SNAPSHOT_v1`：リポジトリの固定情報（木構造、主要ファイルの要約、共通制約、作業手順の基礎）
- `HOT_FILES_v1`：頻繁に参照するコード断片の抜粋（更新頻度が低いもの）
- `ENV_CONSTRAINTS_v1`：OS、Shell、実行条件（例：PowerShell優先）、テスト/ビルド方法の固定

**禁止：**
- 日時、ランダムID、セッションID、トレースIDなど「毎回変わる文字列」を固定prefixに入れない
- 箇条書き記号、空白、改行、順序を変更しない（同内容でも文字列差分でキャッシュが死ぬ）
- 「今回だけ注意」などの個別事情は固定prefixに混ぜない

### 2.2 可変サフィックス（毎回変わる末尾）
- `TASK`：今回の依頼（要件、完了条件）
- `DIFF_OR_PATCH`：既存差分（あれば）
- `LOGS`：エラー/実行ログ（必要部分のみ）
- `NEW_FILES`：新規ファイル/更新されたファイルの抜粋（必要最小限）
- `QUESTIONS`：不明点（必要なら）

原則：**可変情報は必ず末尾に追記**する。

---

## 3. ルーティング仕様（Chat／Worker／Coder）

### 3.1 Chat（会話・最終整形）
- 愛称（例：Mio）は設定で差し替え可能
- ユーザーとの対話窓口
- Worker/Codingの結果を受け取り、ユーザー向けに整形
- 原則として **コード生成をしない**（やるなら軽微な整形のみ）

### 3.2 Worker（非コーディング実務・JSON専用）
- ログ解析、要件整理、タスク分解、影響範囲推定
- **JSONで出力**し、Mioが読みやすく整形できるようにする
- Coding LLMに渡す **可変サフィックス（TASK等）だけ**を生成する
- 固定prefixの生成・更新は担当しない（後述のスナップショット更新フローに従う）

### 3.3 Coder（DeepSeek）
- 実装・diff作成・テスト手順提示
- 入力は必ず「固定prefix + 可変サフィックス」
- 出力は「git diff + 実行コマンド + 注意点」を優先

---

## 4. 固定prefixの実体（ファイル化）

固定prefixは「毎回同一文字列」を保証するため、**ファイルとして管理**する。

### 4.1 ファイル配置（例）
- `./openclaw/prompts/coding/CODING_RULES_v1.md`
- `./openclaw/prompts/coding/REPO_SNAPSHOT_v1.md`
- `./openclaw/prompts/coding/HOT_FILES_v1.md`
- `./openclaw/prompts/coding/ENV_CONSTRAINTS_v1.md`

### 4.2 連結順（固定）
Coding LLMへ渡す際の結合順は固定する。

1. `CODING_RULES_v1.md`
2. `ENV_CONSTRAINTS_v1.md`
3. `REPO_SNAPSHOT_v1.md`
4. `HOT_FILES_v1.md`
5. （ここから末尾）`TASK / DIFF / LOGS ...`

> この順序は**絶対に変えない**。

---

## 5. スナップショット更新フロー（v1→v2）

リポジトリの構造が変わる、主要ファイルが大きく変わる等で固定prefixを更新したい場合は、**差し替えではなくバージョンを上げる**。

### 5.1 変更条件（例）
- tree構造が大きく変わった
- HOT_FILESの対象が変わった
- テスト/ビルド方法が変わった
- 出力形式の規約を変えたい

### 5.2 手順
1. `*_v1.md` をコピーして `*_v2.md` を作る
2. Coding LLM入力で参照するファイル名を `v2` に切替
3. 以後、`v2` を固定prefixとして運用（`v1` は残す）

> v1運用中に内容を直接書き換えると、キャッシュ効率が落ちるだけでなく、再現性も崩れる。

---

## 6. WorkerのJSONスキーマ（推奨）

WorkerはCoding LLMに渡す末尾サフィックスを、次のJSONで返す。

```json
{
  "route": "coding",
  "task": {
    "goal": "何を直すか",
    "acceptance": ["完了条件1", "完了条件2"],
    "constraints": ["今回固有の制約"]
  },
  "context": {
    "files": [
      {"path": "src/foo.ts", "excerpt": "必要最小限の抜粋"}
    ],
    "logs": "必要最小限のログ",
    "diff": "既存差分があれば"
  },
  "questions": ["不明点があれば"]
}
```

MioはこのJSONを、Coding LLM向けの `TASK/LOGS/DIFF` ブロックへ整形して末尾に追加する。

---

## 7. API呼び出し仕様（例）

### 7.1 Chat Completions（deepseek-chat）
固定prefix（ファイル内容）＋末尾（TASK）を `messages` に流し込む。

```powershell
curl https://api.deepseek.com/chat/completions `
  -H "Content-Type: application/json" `
  -H "Authorization: Bearer $env:DEEPSEEK_API_KEY" `
  -d '{
    "model": "deepseek-chat",
    "temperature": 0,
    "messages": [
      { "role": "system", "content": "<CODING_RULES_v1>\n<ENV_CONSTRAINTS_v1>" },
      { "role": "user",   "content": "<REPO_SNAPSHOT_v1>\n<HOT_FILES_v1>" },
      { "role": "user",   "content": "<TASK>\n<LOGS>\n<DIFF>" }
    ]
  }'
```

注意：
- `CODING_RULES_v1` などのブロックは **毎回同一文字列**にする
- 末尾の `TASK/LOGS/DIFF` のみを更新する

---

## 8. 運用ガードレール（事故防止）

- Coding LLMの権限は最小（例：リポジトリ内読み書き＋テスト実行まで）
- 秘密情報（APIキー等）を入力に含めない
- ログは必要箇所だけ（長すぎるログはWorkerが要約して末尾に入れる）
- 「推測で断定しない」：不明なファイルや関数は明示的に質問へ回す

---

## 9. 成功判定（チェック項目）

- [ ] 固定prefixファイルがv1として凍結され、運用で変更されていない
- [ ] 依頼ごとの差分は末尾ブロックだけで表現されている
- [ ] Workerは固定prefixを生成しない（末尾だけ生成）
- [ ] 大きな変更はv2へ切替で吸収している
- [ ] 「同一repo反復」のとき、入力コストが体感で下がっている（キャッシュ命中が増えている）

---

## 付録：固定prefixのテンプレ（最小）

### CODING_RULES_v1（例）
- 出力順：1) git diff 2) 実行コマンド 3) 注意点
- 推測でファイルを捏造しない
- 変更範囲を最小化
- 追加/変更したファイルはdiffに必ず含める
- テスト手順を必ず書く（可能なら実行結果も）

### ENV_CONSTRAINTS_v1（例）
- 端末：Windows PowerShell優先
- リポジトリ操作はgit
- 実行環境：Node.js / Python など（実際の値に合わせる）
