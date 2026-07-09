# Webwright Fetch

Webwright Fetch は、ブラウザ操作が必要なデータ取得を RenCrow 本体から切り離して実行し、取得結果を L1 staging JSONL へ変換するための補助ツールである。

## 位置づけ

- RenCrow 本体 runtime には依存させない。
- Go module / service / Source Registry sweeper へ Webwright を直接組み込まない。
- Webwright の実行成果を `external_fetch` の pending staging 候補として出力する。
- validator / promote は既存の Source Registry / L1 staging 側で行う。

## 使う場面

- API / RSS では取れない動的 Web 画面。
- 検索、フィルタ、ページ遷移、ログイン後画面など、ブラウザ操作が必要な取得。
- 取得手順、スクリーンショット、ログを証跡として残したい取得。

API / RSS で取れるものは、既存の Source Registry fetcher を優先する。

## 実行

Webwright が別途インストール済みの場合:

```bash
python tools/webwright_fetch/run_webwright_fetch.py \
  --task "Find recent public AI policy updates and summarize sources" \
  --start-url "https://www.google.com/search?q=AI+policy+news" \
  --task-id ai_policy \
  --output-dir tmp/webwright_runs
```

Webwright が現在の Python にない場合は、Webwright 入りの Python または `uvx` を指定する:

```bash
python tools/webwright_fetch/run_webwright_fetch.py \
  --task "Find recent public AI policy updates and summarize sources" \
  --start-url "https://www.google.com/search?q=AI+policy+news" \
  --task-id ai_policy \
  --output-dir tmp/webwright_runs \
  --python /path/to/webwright-venv/bin/python

python tools/webwright_fetch/run_webwright_fetch.py \
  --task "Find recent public AI policy updates and summarize sources" \
  --start-url "https://www.google.com/search?q=AI+policy+news" \
  --task-id ai_policy \
  --output-dir tmp/webwright_runs \
  --uvx-from "git+https://github.com/microsoft/Webwright.git"
```

`uvx` は外部 package 取得を伴うため、RenCrow config の `webwright_fetch.uvx_from` は既定では空のままにする。`uvx` を使う場合だけ明示設定する。

RenCrow のローカル Responses API を使う場合:

```bash
python tools/webwright_fetch/run_webwright_fetch.py \
  --task "Open https://example.com and write a concise final report with the page title and one sentence summary." \
  --start-url "https://example.com" \
  --task-id local_worker_example \
  --output-dir tmp/webwright_runs \
  --uvx-from "git+https://github.com/microsoft/Webwright.git" \
  --local-responses-endpoint "http://127.0.0.1:8082/v1/responses" \
  --local-model Coder1
```

LAN クライアントから使う場合は `127.0.0.1` を Mac の IP に置き換える:

```bash
python tools/webwright_fetch/run_webwright_fetch.py \
  --task "Open https://example.com and write a concise final report with the page title and one sentence summary." \
  --start-url "https://example.com" \
  --task-id local_worker_example \
  --output-dir tmp/webwright_runs \
  --uvx-from "git+https://github.com/microsoft/Webwright.git" \
  --local-responses-endpoint "http://192.168.1.207:8082/v1/responses" \
  --local-model Coder1
```

この profile は `tools/webwright_fetch/config_local_worker.yaml` を使い、`playwright codegen` / headed browser / fenced JSON を禁止し、headless Playwright script の生成に寄せる。

Webwright の `report.json` を L1 staging JSONL へ変換:

```bash
rencrow web-gather webwright-fetch \
  --task "Collect the public article title, summary, and key facts" \
  --start-url "https://example.com/article" \
  --task-id ai_policy \
  --dry-run

python tools/webwright_fetch/webwright_to_staging.py \
  --input tmp/webwright_runs/default/ai_policy/report.json \
  --output tmp/webwright_staging/ai_policy.jsonl \
  --namespace kb:news \
  --source-id webwright:ai_policy \
  --source-url "https://www.google.com/search?q=AI+policy+news"
```

出力 JSONL は `conversation.L1StagingItem` の Go JSON field 名に合わせる。

変換済み JSONL を RenCrow L1 staging へ取り込む:

```bash
rencrow web-gather import-webwright-jsonl tmp/webwright_staging/ai_policy.jsonl --json
```

取り込み時も `webwright_fetch` 由来 metadata、`pending`、`auto_promote=false`、credential-like text の拒否を確認する。

`rencrow web-gather webwright-fetch` は実行前に `webwright_fetch.responses_endpoint` の TCP 到達性を確認する。local Worker Responses API が起動していない場合は Webwright を起動せず、preflight error として終了する。

実行前診断:

```bash
rencrow web-gather doctor --json
```

`doctor` は L1 staging store、SearXNG 設定、Webwright runner、Python、`uvx_from`、Responses endpoint 到達性を確認する。Webwright が disabled の場合は skipped として扱う。

## 出力ポリシー

- `Kind`: `external_fetch`
- `Namespace`: 既定 `kb:webwright`
- `ValidationStatus`: `pending`
- `RawText`: Webwright の report / result / sections を平文化した本文
- `SummaryDraft`: report の title / summary / result から短い要約案を作る
- `Meta`: `webwright=true`, `tool=webwright_fetch`, `input_path`, `task_id` などの証跡情報

## 禁止

- 取得結果を validated / promoted として直接出力しない。
- API key、cookie、Authorization header、個人情報を raw_text に保存しない。
- robots / terms / rate limit が不明なサイトで自動大量取得しない。
- RenCrow 本体起動時に Webwright を必須 dependency にしない。
