# RenCrow_CORE Ver0.80 Public Repo 起点化仕様

作成日: 2026-07-01
対象: `picoclaw_multiLLM`
位置づけ: push 済み HEAD から新規 Public repository `RenCrow_CORE` を作るための公開前仕様

## 目的

この文書は、`picoclaw_multiLLM` 現ブランチの push 済み HEAD を `RenCrow_CORE` Ver0.80 の seed / staging source として扱い、新規 Public repository へ投入する前に満たす条件を固定する。

Public repo 起点化は、既存機能を削って軽くする作業ではない。公開不能物だけを除外し、既存機能は次のいずれかとして残す。

- `modules/*`: 公開 contract、DTO、event、純粋 policy、state ownership。
- `internal/features/*`: feature facade、ports、registrar、legacy 実装の束ね。
- `internal/adapter/*`: external adapter / compatibility adapter。
- `internal/domain`、`internal/application`、`internal/infrastructure`: 段階移行中の `legacy-body`。

## 起点

| 項目 | 方針 |
| --- | --- |
| Source repo | `picoclaw_multiLLM` 現ブランチ |
| Target repo | 新規 Public repository `RenCrow_CORE` |
| 起点 | Ver0.80 構成変更、代表テスト、非削除チェック、docs 同期を push した HEAD |
| 作業形態 | PR ではなく新規 repo 初期投入 |
| 既存機能 | 削除しない。未移行は `legacy-body` として catalog に残す |

## 公開範囲

公開するもの:

- Go source、module contracts、feature registrars、adapter、domain、application、infrastructure。
- `docs/02_正本仕様/05`、`06`、`07`、この文書。
- `modules/README.md`、`modules/CURRENT_MAP.md`、`internal/features/README.md`。
- build / test / run の最小手順。
- `LICENSE` と attribution。

公開前に除外または公開可否判断するもの:

- secret / token / API key / password / private key。
- machine-local config、`config.yaml` 実体、`.env` 実体。
- runtime cache、generated artifact、binary、large artifact。
- logs、session dump、private test evidence、local DB。
- private-only docs、private prompt、user-specific dataset。
- `tmp/`、`logs/`、`.cache/`、`.gocache/`、`node_modules/`、`build/`、`dist/`。

## 初期 README 要件

Public repo の root `README.md` は、少なくとも次を含む。

- RenCrow_CORE Ver0.80 の目的。
- module tree。
- Feature Module Catalog の概要。
- build / test / run の最小手順。
- `modules/*` と `internal/features/*` の役割。
- `legacy-body` と未移行領域の扱い。
- secret / local config を repo に入れない方針。
- 代表テストコマンド。
- license / attribution。

旧 `picoclaw_multiLLM`、旧 PR、旧 v3 branch、旧 docs path を前提にした README 文章は、RenCrow_CORE 起点 README では使わない。

## 公開前チェック

Public repo 投入前に、現ブランチで次を実行する。

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/...
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw ./internal/features/... ./internal/adapter/viewer ./modules/...
GOCACHE=/tmp/picoclaw-gocache go test ./...
GOCACHE=/tmp/picoclaw-gocache go vet ./...
git diff --check
git status --short --branch
git log -1 --oneline
```

公開不能物の候補確認:

```bash
git ls-files | rg '(^|/)(\.env|config\.yaml|.*\.pem|.*\.key|logs/|tmp/|cache|artifact|\.db$|\.sqlite$)'
rg -n '(api[_-]?key|secret|token|password|sk-[A-Za-z0-9]|BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY)' --glob '!vendor/**' --glob '!node_modules/**' --glob '!logs/**' --glob '!tmp/**'
```

巨大 artifact 候補確認:

```bash
git ls-files -z | xargs -0 du -h | sort -h | tail -50
```

## 非削除チェック

Public repo 起点化前に、次の既存入口が落ちていないことを確認する。

| 種別 | 最低確認 |
| --- | --- |
| HTTP route | `docs/02_正本仕様/07_RenCrow_CORE_Ver0.80_組み換え実装作業資料.md` の HTTP route チェック |
| CLI | `run`、`health`、`status`、`doctor`、`channels`、`logs`、`chat`、`evidence`、`jobs`、`source-registry`、`web-gather`、`browser-actor`、`knowledge`、`help` |
| Viewer tab | `internal/adapter/viewer/assets/js/tabs` の主要 tab asset |
| Background job | Source Registry、Memory lifecycle、Daily intake、Parquet、Movie catalog、SuperAgent、Heartbeat、IdleChat |
| Module endpoint | `modules/core.RegisteredModuleEndpointPaths()` の endpoint |

## 初期投入手順

1. `picoclaw_multiLLM` 現ブランチで Ver0.80 の構成変更を完了する。
2. 代表テストと必要な live 確認を実行する。
3. `05`、`06`、`07`、この文書、root `README.md`、`modules/README.md`、`modules/CURRENT_MAP.md`、`internal/features/README.md` を同期する。
4. 公開不能物、巨大 artifact、local config、secret 候補を洗い出す。
5. 日本語 commit message で commit / push する。
6. push 済み HEAD を `RenCrow_CORE` Ver0.80 seed として export する。
7. 新規 Public repository `RenCrow_CORE` に投入する。
8. clean clone で `go test ./modules/...` と代表 build / run 手順を再現する。

## 完了条件

- push 済み HEAD が `RenCrow_CORE` Ver0.80 の起点として説明できる。
- root `README.md` が RenCrow_CORE Ver0.80 の初期 README として読める。
- `LICENSE` と attribution が残っている。
- Feature Module Catalog にある既存機能が削除されていない。
- `modules/*` と `internal/features/*` の一覧が current worktree と一致する。
- 公開不能物が Public repo に入らない方針と確認コマンドが明文化されている。
- clean clone 後に module contract test と代表テストを再現できる。

## Export 除外 manifest

Public repo 作成時は、repo root の `.rencrow-core-exportignore` を export 除外 manifest として扱う。

このファイルは削除指示ではない。`picoclaw_multiLLM` staging repo では既存 fixture、runtime 設定、検証 artifact、local tool binary が残っていてもよいが、新規 Public repository `RenCrow_CORE` へ投入する snapshot からは除外する。

`config.yaml`、`kb-admin`、`go1.25.0.linux-amd64.tar.gz`、`tmp/`、generated understand-anything artifact、`baseline_*.patch`、`samba_*.conf`、`samba_protocol_patch.py`、`test_patch.json` は、Public seed では除外対象である。

`docs/archive/`、`docs/refs/`、`docs/調査/`、`docs/05_運用/`、旧 staging 正本である `docs/02_正本仕様/01_仕様.md`、`02_実装仕様.md`、`03_Runtime_Config.md` は、private / reference / investigation / machine-local operational history を含みうるため Public seed から除外する。Public seed に残す正本は、`05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md`、`06_RenCrow_CORE_Ver0.80_モジュール化実装仕様.md`、`07_RenCrow_CORE_Ver0.80_組み換え実装作業資料.md`、`08_RenCrow_CORE_Ver0.80_Public_Repo起点化仕様.md` を中心とする。

`.agents/`、`.aidesigner/`、`.claude/`、`.codex/`、`.cursor/`、`.serena/`、`.mcp.json` は local agent / IDE / MCP / memory metadata であり、Public seed には含めない。`.github/workflows/` は staging repo 側の CI / scheduled job 定義であり、Ver0.80 Public seed の初期投入対象から外す。Public repo の CI は、公開範囲が確定した後に別 commit で明示的に追加する。

Ver0.80 seed では Go module path は `github.com/Nyukimin/picoclaw_multiLLM` のまま保持する。Public repository 名を `RenCrow_CORE` に変えることと、Go module path を変更することは別作業である。module path rename は全 import path と downstream 利用者に影響するため、Ver0.80 初期投入では行わない。

## Public repo 公開後の次フェーズ判断

Public repo `RenCrow_CORE` の初期投入後は、次を Ver0.80 maintenance として扱う。

- CI は Public repo 側に別 commit で追加する。初期 gate は clean clone で実証済みの `go test ./modules/...` と `go test ./cmd/picoclaw ./internal/features/... ./internal/adapter/viewer ./modules/...` に限定する。
- Go module path rename は Ver0.80 では実施しない。理由は import path、downstream 利用者、staging repo との同期に影響するためである。rename を行う場合は Ver0.81 以降の互換 migration として、全 import path、module docs、release note、consumer migration guide を同時に更新する。
- handler body migration は小さい feature group から進める。Ver0.80 maintenance の最初の対象は LLM Ops route ownership handoff とし、handler body は Viewer adapter 側 legacy-body に残す。
- local path 一般化は secret 除外とは別作業である。RenCrow_Tools 参照は `RENCROW_TOOLS_ROOT` を優先し、未設定時は `$HOME/RenCrow/RenCrow_Tools` へ解決する。machine-local `config.yaml` 実体は staging repo で残っていても Public export から除外する。
- tag / release note は Public repo の状態を基準に作成する。tag は `v0.80`、release note は Public seed の scope、代表テスト、既知の未移行 legacy-body、Go module path 維持方針、CI gate を含める。
