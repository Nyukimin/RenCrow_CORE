# Viewer To-Be Ops Browser E2E

To-Be Ops を、現在のソースからビルドしたRenCrowサーバー、実ストア、実HTTP API、
Playwright管理ブラウザで検証する。正常系のAPI route interceptionやレスポンスmockは
使用しない。

## セットアップと実行

```bash
npm ci
npx playwright install firefox chromium
npm run test:e2e:viewer:to-be
```

標準ではFirefoxとChromiumを実行する。短いFirefox限定確認も用意する。

```bash
npm run test:e2e:viewer:to-be:firefox
```

通常は `127.0.0.1:18791` から空きポートを探索する。先頭ポートが競合する場合は変更できる。

```bash
RENCROW_E2E_PORT=28791 npm run test:e2e:viewer:to-be
```

## テスト行列

| 層 | ケース | 実行範囲 |
| --- | --- | --- |
| 実バックエンド | unavailable / empty | Advisor空、Knowledge/Recall未設定、Revenue実API |
| 実バックエンド | populated / boundary | Advisor JSONL、Knowledge/Recall L1 SQLite、Revenue 7件と長いID |
| 表示 | responsive | `1440x900`、`390x844` |
| 表示 | interaction | 5ブロック、各5指標、全5details、refresh、reload |
| 状態 | normalized status | `ok`、`warning`、`blocked`、`unavailable` |
| 障害契約 | fault injection | HTTP 500、不正JSON、blocked response、5秒timeout |
| ブラウザ | cross-browser | Firefox、Chromium |

障害契約だけはPlaywright routeで対象APIへ限定注入する。これは実バックエンドE2Eと
レポート上で分離し、正常系が実APIであることを置き換えない。

## 隔離境界

- `127.0.0.1` の独立ポートだけで起動し、競合ポートは使用しない。
- 既存の `rencrow.service` と `:18790` は停止・再起動しない。
- HOME、workspace、session、Viewer log、Advisor、Revenue、L1 SQLiteは実行ごとの領域に置く。
- LLM、外部送信、Heartbeat、browser sidecarは無効にする。
- populated構成のCodex profileは表示契約のため登録するが、commandは `false` で実行しない。
- テスト終了時は、成功・失敗にかかわらずテストサーバーを停止する。
- 成果物は `output/playwright/to-be-ops-live-e2e/<run-id>/` に保存する。

## 合格条件と証跡

- 7本のTo-Be APIが実HTTP GETで200になる。
- Advisor、Knowledge Relation、Economic、Approval、Recall Traceの実fixtureが表示される。
- `ok / warning / blocked / unavailable` が期待するカードへ伝播する。
- 全detailsが操作でき、長いIDでもdesktop/mobileに横あふれがない。
- refreshとreload後も実ストアデータが維持される。
- To-Be由来のconsole error、page error、対象APIの予期しないrequest failureがない。
- `tracker.json` に単一の正準QA trackerを生成し、各browser reportと画像を紐づける。

隔離構成で無効にした既存optional panelのconsole errorは各reportへ記録するが、
To-Be E2Eの合否には混ぜない。全Viewer console errorゼロは別の全画面QA対象である。
