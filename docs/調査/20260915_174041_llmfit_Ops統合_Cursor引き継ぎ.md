# Cursor引き継ぎ — LLM Ops（llmfit統合）Phase 1

作成: 2026-09-15 17:40 JST  
作成理由: Claude Desktop セッション（`rencrow-cb` / session `eed444f0-...`）が rate limit で中断。作業を Cursor へ移管する。  
フェーズ: **検討・引き継ぎ資料**（本ファイル作成時点では実装・配備を再開していない）

## 1. 一言で言うと

Debug Viewer の Ops に「LLM Ops」タブを追加し、各ノードの llmfit 観測（Hardware Capability / Model Fit）を表示する Phase 1 は **ソース上ほぼ完了・ローカルコミット済み**。  
**本番 CORE（hostname: `fujitsu-ubunts`）へのバイナリ／config 反映は未実施**。画面はまだ旧バイナリのまま。

## 2. 正本・参照

| 種別 | パス |
| --- | --- |
| workspace 共通ルール | `RenCrow_EcoSystem/AGENTS.md` |
| CORE 作業ルール | `RenCrow_CORE/AGENTS.md` |
| 機能仕様（Part A） | `docs/02_機能仕様.md`「LLM Ops / Hardware Capability（llmfit統合）」 |
| 実装仕様（Part B） | `docs/調査/RenCrow_llmfit_Ops統合_実装仕様.md` |
| 設定 | `docs/05_設定リファレンス.md`「LLM Ops / llmfit 設定」 |
| API | `docs/06_Public_API仕様.md`「LLM Ops Viewer API」 |
| Claude 元セッション | `~/.claude/projects/-Users-yukimikawaguchi-Documents-Product-RenCrow/eed444f0-b07e-434b-bd9f-e45dc15fbafc.jsonl` |

注意: `public-docs-guard.yml` が `docs/` 配下のプライベートIP表記を拒否する。本資料も実IPを書かない。接続先は運用者が把握している LAN インベントリ（CORE=`fujitsu-ubunts`、RX6800ホスト、M5 Mac、Windows GPU機）を使うこと。

## 3. 完了していること（証拠つき）

### 3.1 ソース（RenCrow_CORE / `main`）

`origin/main` より **4コミット先行・未 push**（2026-09-15 17:40 時点）:

| Commit | 内容 |
| --- | --- |
| `56501f4d` | LAN IP 直書きを設定＋変数参照へ（先行・本作業と隣接） |
| `08a31a71` | **feat**: LLM Ops Phase 1（domain / infra llmfit / application / viewer / docs） |
| `cd5baae9` | **fix**: GPU 枚数（`gpus[].count`）と空き VRAM（`*float64` / null=不明）表示 |
| `656184df` | **refactor**: 設定キー `llm_ops` → `llm_capability`（同名異概念の解消） |

主要配置:

```text
internal/domain/llmops/
internal/application/llmops/
internal/infrastructure/llmfit/
internal/features/llmops/
internal/adapter/viewer/llmops_handler.go
internal/adapter/viewer/assets/js/tabs/llm-ops.js
internal/adapter/viewer/assets/css/tabs/llm-ops.css
cmd/rencrow/runtime_llmops.go
```

Viewer API（変更なし・route名は維持）:

```text
GET  /viewer/llm-ops/nodes
GET  /viewer/llm-ops/node?node_id=
GET  /viewer/llm-ops/node/models?node_id=
GET  /viewer/llm-ops/model/matrix?model_id=
POST /viewer/llm-ops/refresh
```

設定キー（現行コードの正）:

```yaml
llm_capability:
  llmfit:
    enabled: true
    system_ttl: 5m
    models_ttl: 15m
    health_ttl: 30s
    request_timeout: 5s
    top_limit: 20
    nodes:
      - id: "..."          # [A-Za-z0-9_.-]
        mode: "http"       # http | cli
        endpoint: "http://<host>:8787"  # http のみ
```

重要: 本番 `core.yaml` に残る旧 `llm_ops:` は **RenCrow_LLM 管理デーモン用の退役キー**で、現行 CORE はパースしない。  
LLM Ops（llmfit）用に同じキー名を使ってはならない → そのため `llm_capability` へ改名済み。

### 3.2 ノード側 llmfit

| 役割 | ホスト名の目安 | SSH ユーザー | llmfit | 常駐 |
| --- | --- | --- | --- | --- |
| CORE 本番 | `fujitsu-ubunts` | `nyukimi` | v1.1.15 / `~/.local/bin/llmfit` | systemd user `llmfit.service` **active**（port 8787） |
| RX6800 Ubuntu | `nyukimi-RX6800` | `nyukimi` | serve 稼働を確認 | systemd user `llmfit` **active** |
| M5 Mac | `yukiminoMacBook-Pro.local` | `yukimi` | launchd 系で serve 確認 | `dev.llmfit.serve` |
| Windows GPU | （運用インベントリの Win 機） | `nyuki` | セッション上「導入完了」報告あり | **本資料作成時の再確認は不十分 → 未確認扱い** |

`fujitsu-ubunts` の user unit 概要:

- `~/.config/systemd/user/llmfit.service`
- `ExecStart=%h/.local/bin/llmfit serve --host 0.0.0.0 --port 8787`
- `Restart=always`

ローカル health（CORE 上ループバック）は成功済み: `GET http://127.0.0.1:8787/health` → `status: ok`。

### 3.3 開発中に一度通った受け入れ

- Mac 上での backend / frontend 実装とテスト
- CORE ホスト上の実 llmfit からの E2E（当時は Nodes 表示まで到達）
- GPU 表示バグ修正後、RX6800×4 と unified memory の描画確認（セッション報告）

## 4. 未完了・次にやること（優先順）

### P0 — ドキュメント追随（小さい・安全）

- [ ] `docs/06_Public_API仕様.md` の残存表記を修正  
  - 現状（HEAD）: `llm_ops.llmfit.top_limit` / `llm_ops.llmfit.enabled: false`  
  - 正: `llm_capability.llmfit.*`  
  - Claude 側エージェント「docs/06追随とコミット修正」は rate limit で **failed**

### P0 — 本番配備（CORE = `fujitsu-ubunts`）※未実施

現状証拠（2026-09-15 17:42 JST 頃の読み取り）:

- `~/.local/bin/rencrow` 更新日時 **2026-09-14**、version 表示は `3022ba2b-worktree-gmail-xlink`（LLM Ops コミットより古い）
- `rencrow.bak.*` は見当たらない → **本日の差し替えは未実施**
- `core.yaml` に `llm_ops:`（退役キー）は存在するが、`llm_capability:` は **無い**
- Linux クロスビルド成果物は手元に残っていない想定（再ビルドが必要）

推奨手順（実装再開時・ユーザー確認後）:

1. 作業ツリーの **他作業差分を巻き込まない**（後述「危険地帯」）
2. `GOOS=linux GOARCH=amd64` で `656184df`（またはそれ以降の LLM Ops のみ）をビルド
3. 現行バイナリを `~/.local/bin/rencrow.bak.<日時>` へ退避し sha256 記録
4. `~/.rencrow/config/core.yaml` を同様にバックアップ
5. 新バイナリを配置
6. `llm_capability.llmfit.nodes` を追記（CORE 自身は `mode: cli` または `http://127.0.0.1:8787`、他ノードは `mode: http` + endpoint）
7. CORE 再起動（既存の rencrow サービス手順に従う。勝手に unit 名を変えない）
8. Viewer → Ops → LLM Ops で Node Overview / Model Fit を目視
9. 必要なら push（ユーザー明示指示があるまで push しない）

### P1 — ノード運用の仕上げ

- [ ] Windows ノードの llmfit 常駐・疎通を再確認
- [ ] 各ノードの `llmfit serve` 再起動耐性（systemd / launchd / Task Scheduler）
- [ ] CORE→各ノード 8787 到達性（同一ホスト内ループバックは不要。リモートだけ）
- [ ] ufw 等の FW は「Internet 非公開・trusted mesh のみ」方針を維持

### P2 — Phase 2（仕様上の次）

`docs/08` ロードマップ: 実測統合（bench、Estimate vs Actual、履歴）。  
Phase 1 の「表示・比較のみ／自動ルーティングなし」を壊さないこと。

## 5. 危険地帯（絶対に混ぜない）

`RenCrow_CORE` 作業ツリーには **LLM Ops と無関係な大量の未コミット差分**がある（例: TaskID / job_id 移行、orchestrator、agent 多数）。

制約:

- LLM Ops の追加コミットは **当該ファイルだけ**をステージする
- `docs/02` `04` `06` 等に他作業の未コミット差分が載っている場合は **ハンク単位**で切り分ける（Claude は `08a31a71` 作成時に実施済み）
- `data/orchard.sqlite3` など生成物をコミットしない
- 新ブランチ作成禁止（CORE `AGENTS.md` / EcoSystem 共通）
- push・本番再起動・sudo はユーザー指示または明確な運用手順がある場合のみ

## 6. 設計上の不変条件（崩さない）

1. llmfit は **観測ソース**であり、正本 DB・Scheduler・自動ルーティングにしない（Phase 1）
2. 未導入・停止時は CORE 本体を止めず、LLM Ops のみ `OFFLINE` / `STALE` へ縮退
3. 設定キーは `llm_capability`。旧 `llm_ops` は別概念（退役）
4. Viewer route・機能名「LLM Ops」・パッケージ `llmops` は維持してよい
5. public docs にプライベートIP・マシンローカル絶対パスを書かない
6. 標準 Go 配布境界: llmfit は外部 sensor。Python/Node/Docker を標準起動条件に足さない

## 7. 検証チェックリスト（再開時）

ローカル（コード）:

- [ ] `go test` / 既存 plan に沿った llmops・llmfit・viewer 関連テスト
- [ ] `viewer_llm_ops.test.mjs`（GPU count / null VRAM / refresh）

本番（`fujitsu-ubunts`）:

- [ ] `systemctl --user is-active llmfit` → active
- [ ] `curl -s http://127.0.0.1:8787/health` → ok
- [ ] 配備後 `rencrow version` が期待 commit を示す
- [ ] `core.yaml` に `llm_capability.llmfit.nodes` が存在し、旧意味の `llm_ops` を流用していない
- [ ] Viewer LLM Ops: ノード数・ONLINE、RX6800 が複数枚として見える、空き VRAM 不明が「0」に化けない

## 8. 中断時の未完エージェント（Claude）

| 時刻帯 (JST) | 内容 | 結果 |
| --- | --- | --- |
| 17:19 | config キー改名 | completed → commit `656184df` |
| 17:19 | docs/06 追随とコミット修正 | **failed**（session limit） |
| 17:19 | Cursor向け引き継ぎ資料 | **failed**（session limit）→ 本ファイルで代替 |

## 9. 推奨する再開プロンプト（Cursor向け）

```text
RenCrow_CORE の LLM Ops（llmfit）作業を引き継ぐ。
正本: docs/調査/20260915_174041_llmfit_Ops統合_Cursor引き継ぎ.md
まず P0 の docs/06 の llm_capability 追随だけ行い、結果を報告。
本番配備は手順提示までとし、実行は確認後。
他作業の未コミット差分（TaskID等）には触れない。
```

## 10. 進捗（本資料作成時点）

| 項目 | 評価 |
| --- | --- |
| Phase 1 実装・ローカルコミット | 一部達成（約 90%）— push / 本番未反映 |
| ノード llmfit 導入 | 一部達成（約 80%）— Win 再確認不足 |
| 本番 CORE 配備 | 未達成（0%） |
| docs/06 キー名追随 | 未達成（0%） |
| 本引き継ぎ資料 | 達成（100%） |

---

## 経緯（要約）

```text
目的: llmfit Ops統合作業を Cursor が再開できる状態にする
制約: EcoSystem/CORE AGENTS、public-docs-guard、他差分非混在、実装再開は確認後
仮説: ソースは完了近く、ブロッカーは docs/06 追随と本番バイナリ+config 未配備
検証: git log/status、fujitsu-ubunts の llmfit/rencrow/core.yaml、他ノード ssh を確認
判断: 引き継ぎ資料を docs/調査 に作成。実装・配備はまだ行わない
次の一手: ユーザー確認後に P0（docs/06）→ 本番配備手順の実行
```
