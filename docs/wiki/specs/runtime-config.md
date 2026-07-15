---
page_id: spec:runtime-config
type: spec
status: active
owner: core
canonical_source: docs/05_設定リファレンス.md
source:
  - docs/05_設定リファレンス.md
  - config/config.yaml.example
related:
  - docs/wiki/concepts/runtime-state.md
summary: 設定は RENCROW_CONFIG または作業ディレクトリの config.yaml から読み込み、secret は環境変数で展開する
updated: 2026-07-15
---

# Runtime config

`rencrow` は `RENCROW_CONFIG` の path を優先し、未指定なら作業ディレクトリの `./config.yaml` を読む。公開 template は `config/config.yaml.example` である。

YAML 内の `${NAME}` は環境変数で展開する。API key、token、private key を repository や YAML に保存しない。外部 endpoint、model、storage path は deployment ごとに設定し、test や example の値を production の正本にしない。

任意 feature は有効化したときだけ固有 endpoint/path が必須になる。外部 bind には TLS、認証、network allowlist を別途用意する。
