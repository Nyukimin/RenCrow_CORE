# RenCrow_CORE Ver0.80 組み換え実装作業資料

作成日: 2026-07-01
対象: `picoclaw_multiLLM`
位置づけ: `06_RenCrow_CORE_Ver0.80_モジュール化実装仕様.md` に基づき、実装に入る直前に使う作業資料

## 目的

この資料は、`picoclaw_multiLLM` 現ブランチを RenCrow_CORE Ver0.80 の seed / staging source として整えるための実装作業メモである。

ここで扱うのは、次の 3 点である。

- `cmd/picoclaw` registrar 起点追加の作業メモ。
- Viewer Chat contract 固定のテスト設計。
- 既存機能非削除チェック表。

この資料は、既存機能を削らずに module / feature boundary へ寄せるためのものとする。実体移動、route 削除、provider 削除、Viewer tab 削除は、この資料だけを根拠に行わない。

## 1. `cmd/picoclaw` registrar 起点追加メモ

### 目的

`cmd/picoclaw` を process 起動、config 読込、依存注入、feature registrar 呼び出しへ寄せる。

最初の作業単位では、既存 route 登録を壊さず、feature group ごとの登録入口だけを作る。

### 最初に触る候補

| ファイル | 目的 | 注意 |
| --- | --- | --- |
| `cmd/picoclaw/feature_registrars.go` | feature group registrar の入口を追加する | 新規追加。既存 route を削らない。 |
| `cmd/picoclaw/routes.go` | 既存 `register*Routes` の呼び出し単位を整理する | handler 本体や route path を変更しない。 |
| `cmd/picoclaw/module_routes.go` | module endpoints の登録単位を維持する | `/viewer/modules/*` を消さない。 |
| `internal/features/*/registrar.go` | feature registrar の最終受け口 | 最初は空でもよい。実装重複を持たせない。 |

### 追加する入口の標準形

```go
// cmd/picoclaw/feature_registrars.go
func registerFeatureRoutes(
    mux *http.ServeMux,
    cfg *config.Config,
    dependencies *Dependencies,
    sttRuntime sttRuntime,
    voiceChatRuntime voiceChatRuntime,
    debugSystemOpts viewer.DebugSystemOptions,
) {
    // 既存 register*Routes を feature group 順に呼ぶ。
    // 初回は route 移動ではなく、呼び出し順と責務の見える化に留める。
}
```

初回作業では、既存の `registerViewerBaseRoutes`、`registerViewerDynamicRoutes`、`registerSTTAndAudioRoutes`、`registerLLMOpsRoutes`、`registerEntryAndChromeRoutes`、`registerChannelRoutes` を削らない。

### feature group 順

| 順 | Group | 既存入口の候補 | feature root |
| --- | --- | --- | --- |
| 1 | Channels | `registerChannelRoutes` | `internal/features/channels` |
| 2 | Viewer base | `registerViewerBaseRoutes` | `internal/features/viewer` |
| 3 | Viewer dynamic | `registerViewerDynamicRoutes` | `internal/features/viewer` と各 feature |
| 4 | LLM Ops | `registerLLMOpsRoutes` | `internal/features/ops`, `internal/features/llm` |
| 5 | Voice / STT / Audio | `registerSTTAndAudioRoutes` | `internal/features/voice`, `internal/features/stt`, `internal/features/tts` |
| 6 | Module endpoints | `registerModuleRoutes` | `internal/features/core`, `modules/core` |
| 7 | Entry / Chrome | `registerEntryAndChromeRoutes` | `internal/features/ops` または `internal/features/channels` |

### 完了条件

- `cmd/picoclaw/feature_registrars.go` から feature group の登録順が読める。
- 既存 route path は変わっていない。
- `cmd/picoclaw` に新しい巨大 `manager` を作っていない。
- `internal/features/*` に handler 本体や provider 実装を複製していない。
- `go test ./cmd/picoclaw ./internal/features/... ./internal/adapter/viewer ./modules/...` が通る。

## 2. Viewer Chat contract 固定のテスト設計

### 目的

Viewer 通常チャットは `to=mio|shiro|kuro|midori` を送る契約に固定する。

`model_alias`、`route_prefix`、`base_url`、`model` は legacy 互換として隔離し、新しい通常 Chat 経路の primary contract にしない。

### contract 対象

| 対象 | 固定すること |
| --- | --- |
| `modules/chat` | recipient contract、許可値、既定値、invalid recipient の扱い |
| `internal/adapter/viewer/handler_send.go` | legacy alias の扱いを legacy として隔離すること |
| Viewer JS send payload | 通常チャットで `model_alias` / `route_prefix` を primary payload に含めないこと |
| `internal/application/orchestrator` | `to` を Chat 相手として扱い、OPS / Worker 実行 route と混同しないこと |

### `modules/chat` に置くテスト候補

| Test | 期待 |
| --- | --- |
| `TestNormalizeViewerRecipientAllowsPublicChatTargets` | `mio`, `shiro`, `kuro`, `midori` を許可する |
| `TestNormalizeViewerRecipientDefaultsToMio` | 空文字は `mio` になる |
| `TestNormalizeViewerRecipientRejectsUnknownTarget` | `worker`, `coder`, `ops`, `heavy`, `wild`, 任意文字列を通常 Chat recipient として成功扱いしない |
| `TestViewerRecipientDoesNotImplyExecutionRoute` | `to=shiro` は Worker / OPS 実行 route ではない |

### Viewer adapter に置くテスト候補

| Test | 期待 |
| --- | --- |
| `TestViewerSendPayloadUsesRecipientContract` | 新経路では `to` を Chat recipient として処理する |
| `TestViewerSendDoesNotIncludeLegacyAliasInNewPath` | 通常送信 payload から `model_alias` / `route_prefix` に依存しない |
| `TestLegacyViewerAliasIsExplicitlyLegacy` | 旧 alias を残す場合、関数名・テスト名・コメントに legacy 境界が見える |

### 非互換にしない条件

- 既存 Viewer の旧 alias 利用を即時削除しない。
- legacy alias は互換経路として残し、新経路とはテストで分ける。
- `to=shiro` を `/ops`、`CODE`、Worker execution として扱わない。
- 対象 runtime 不可時に Mio へ黙って fallback しない。

### 検証コマンド

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/chat ./internal/adapter/viewer ./internal/application/orchestrator ./cmd/picoclaw
```

Viewer JS を触る場合は、最低 1 セッションで `/viewer/send`、Timeline event、error 表示を確認する。

## 3. 既存機能非削除チェック表

### 方針

構成変更で確認する対象は、route、CLI、Viewer tab、background job、module endpoint である。

削除ではなく、次のいずれかとして記録する。

- `保持`: 既存経路のまま残す。
- `adapter経由`: feature registrar / adapter / facade 経由へ寄せる。
- `stub`: feature root だけ先に置き、実装本体は既存位置に残す。
- `未移行`: legacy-body として残す。

### HTTP route チェック

| Area | 最低限保持する経路 |
| --- | --- |
| Channel | `/webhook`, `/webhook/telegram`, `/webhook/discord`, `/webhook/slack` |
| Viewer base | `/viewer`, `/viewer/assets/`, `/viewer/runtime-config`, `/viewer/events`, `/viewer/send` |
| Viewer character | `/viewer/character/state`, `/viewer/character/manifest`, `/viewer/live2d/character`, `/viewer/live2d/chat`, `/viewer/live2d/emotion` |
| TTS / audio | `/viewer/tts/audio`, `/viewer/tts/playback-ack`, `/viewer/active-control`, `/audio-router/events` |
| STT | `/viewer/stt/log`, `/viewer/stt/wav`, `/viewer/stt/wav/raw`, `/viewer/stt/autotest`, `/viewer/stt/admin/restart`, `/stt/chat-input` |
| VoiceChat | `/voice-chat`, `/voice-chat-ws` |
| Module endpoints | `/viewer/modules/manifest`, `/viewer/modules/health`, `/viewer/modules/chat/route`, `/viewer/modules/llm/diagnostics`, `/viewer/modules/worker/diagnostics`, `/viewer/modules/tts/diagnostics`, `/viewer/modules/tts/playback-state`, `/viewer/modules/stt/diagnostics`, `/viewer/modules/stt/viewer-input` |
| Ops / jobs | `/viewer/status`, `/viewer/jobs`, `/viewer/job/detail`, `/viewer/logs`, `/viewer/repair/run`, `/viewer/backlog`, `/viewer/scheduler` |
| Source / knowledge / memory | `/viewer/source-registry`, `/viewer/knowledge-memory`, `/viewer/memory/snapshot`, `/viewer/memory/layers`, `/viewer/memory/recall-pack` |
| Governance / security / reports | `/viewer/verification/recent`, `/viewer/sandbox`, `/viewer/skill-governance/recent`, `/viewer/tool-harness/recent`, `/viewer/dci/recent` |
| Workstream / revenue | `/viewer/workstreams`, `/viewer/workstreams/goals`, `/viewer/revenue`, `/viewer/revenue/daily-routine` |
| Web / browser | `/viewer/browser-trace-api`, `/viewer/browser-trace-api/discover`, `/viewer/complexity-hotspots` |
| SuperAgent / AI workflow | `/viewer/superagent`, `/viewer/superagent/runs`, `/viewer/ai-workflow`, `/viewer/ai-workflow/events` |

未設定の optional channel、disabled verification、disabled sandbox などは 503 `unavailable` を返してよい。これは route が存在し、現在状態として利用不可を明示している状態であり、404 route 欠落とは区別する。

### Ver0.80 registrar 実装反映

2026-07-01 時点の現ブランチでは、HTTP route 登録の handoff は次の状態である。

| Feature group | 状態 | 備考 |
| --- | --- | --- |
| Viewer base | `adapter経由` | `internal/features/viewer` が base/static route 登録を所有する。 |
| IdleChat | `adapter経由` | `internal/features/idlechat` が Viewer route 登録と background start handoff を所有する。 |
| Ops / jobs / workstream / revenue | `adapter経由` | `internal/features/ops` が route 登録を所有する。 |
| Voice / STT / TTS | `adapter経由` | `internal/features/voice` が composite registrar、`stt` と `tts` が各 route 群を所有する。 |
| Web / browser | `adapter経由` | `internal/features/web` が BrowserTrace / Complexity route 登録を所有する。 |
| Source / Knowledge / Memory | `adapter経由` | `internal/features/source`、`knowledge`、`memory` が route 登録を所有する。 |
| Reports / Governance / Sandbox / SuperAgent / AIWorkflow | `adapter経由` | 各 feature registrar が route 登録を所有する。`security` は直接 route なし。 |
| Channels / Entry / Chrome bridge | `adapter経由` | `internal/features/channels` が inbound route と entry / chrome bridge route を所有する。 |
| Distributed | `保持` | 直接 HTTP route はなく、transport / remote-agent wiring は legacy-body に保持する。 |

この表は「既存機能を削った」ことを意味しない。handler 本体、provider 実装、CLI、background job、module endpoint は削除せず、必要なものは legacy-body として保持する。

確認コマンド:

```bash
rg -n 'mux\\.Handle(Func)?\\(' cmd/picoclaw internal/adapter/viewer
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw ./internal/adapter/viewer
```

### CLI チェック

| CLI | 保持すること |
| --- | --- |
| `run` | HTTP server 起動 |
| `version` / `-v` / `--version` | version 表示 |
| `health` | health check |
| `status` | status 表示 |
| `doctor` | doctor 実行 |
| `channels` | channel adapter 操作 |
| `gateway` | gateway 操作 |
| `ollama` | Ollama 操作 |
| `logs` | logs 操作 |
| `chat` | chat CLI |
| `evidence` | evidence CLI |
| `jobs` | jobs CLI |
| `source-registry` | Source Registry 操作 |
| `web-gather` | WebGather 操作 |
| `browser-actor` | BrowserActor 操作 |
| `knowledge` | Knowledge import 操作 |
| `help` / `-h` / `--help` | help 表示 |

確認コマンド:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw
```

### Viewer tab チェック

| Tab asset | 保持する feature |
| --- | --- |
| `home.js` | Viewer home |
| `timeline.js` | Timeline / event |
| `roles.js` | Agent / role |
| `sessions.js` | session |
| `jobs.js` | job |
| `idlechat.js` | IdleChat |
| `backlog.js` | Backlog |
| `memory.js` | Memory |
| `ops.js` | Ops / runtime / source / knowledge / governance |
| `system.js` | System / diagnostics |
| `reports.js` | Reports |
| `develop.js` | Develop / workflow |
| `overview.js` | Overview |
| `progress.js` | Progress |
| `instructions.js` | Instructions |
| `movie-db.js` | Movie catalog |
| `investment.js` | Investment |
| `news-pack.js` | News pack |

確認コマンド:

```bash
find internal/adapter/viewer/assets/js/tabs -maxdepth 1 -type f -printf '%f\n' | sort
GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/viewer
```

### Background job チェック

| Job | 保持すること |
| --- | --- |
| Source Registry sweeper | `startSourceRegistrySweeper` |
| Memory lifecycle | `startMemoryLifecycleJob` |
| Daily intake sweeper | `startDailyIntakeSweeper` |
| Parquet export | `startParquetExportJob` |
| Movie catalog backfill | `startMovieCatalogBackfillJob` |
| SuperAgent run queue scheduler | `startSuperAgentRunQueueScheduler` |
| Heartbeat service | `buildHeartbeatService` / `heartbeatSvc.Start()` |
| IdleChat orchestrator | `idleChatOrch.Start()` |

確認コマンド:

```bash
rg -n 'start(SourceRegistry|MemoryLifecycle|DailyIntake|Parquet|MovieCatalog|SuperAgent)|heartbeatSvc\\.Start|idleChatOrch\\.Start' cmd/picoclaw
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw
```

### Module endpoint チェック

| Endpoint | Source |
| --- | --- |
| `/viewer/modules/manifest` | `modules/core.ModuleManifestEndpoint` |
| `/viewer/modules/health` | `modules/core.ModuleHealthEndpoint` |
| `/viewer/modules/llm/diagnostics` | `modules/core.ModuleLLMDiagnosticsEndpoint` |
| `/viewer/modules/chat/route` | `modules/core.ModuleChatRouteEndpoint` |
| `/viewer/modules/worker/diagnostics` | `modules/core.ModuleWorkerDiagnosticsEndpoint` |
| `/viewer/modules/tts/diagnostics` | `modules/core.ModuleTTSDiagnosticsEndpoint` |
| `/viewer/modules/tts/playback-state` | `modules/core.ModuleTTSPlaybackStateEndpoint` |
| `/viewer/modules/stt/diagnostics` | `modules/core.ModuleSTTDiagnosticsEndpoint` |
| `/viewer/modules/stt/viewer-input` | `modules/core.ModuleSTTViewerInputEndpoint` |

確認コマンド:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./modules/core ./cmd/picoclaw
```

## 実装前チェックリスト

| 項目 | OK 条件 |
| --- | --- |
| 正本仕様 | `05` と `06` が RenCrow_CORE Public repo 起点化を明記している |
| feature scaffold | `internal/features/*/README.md`, `ports.go`, `registrar.go` がある |
| module catalog | `modules/README.md`, `modules/core/manifest.go`, `modules/dependency_rules_test.go` が現存 module と一致している |
| route 非削除 | route チェック表の最低限保持経路が消えていない |
| CLI 非削除 | CLI チェック表の command が消えていない |
| Viewer tab 非削除 | tab asset が消えていない |
| background job 非削除 | background job の起動関数が消えていない |
| module endpoint 非削除 | `/viewer/modules/*` endpoint が消えていない |
| テスト | 代表テストコマンドが通る |
| push | 日本語 commit message で push し、remote HEAD を RenCrow_CORE Ver0.80 起点として説明できる |

## RenCrow_CORE Public repo 起点化準備チェック

このチェックは、新規 Public repository `RenCrow_CORE` を作成する前の準備であり、この資料だけで公開 repo への投入を実行しない。

### 起点

- `picoclaw_multiLLM` 現ブランチの push 済み HEAD を Ver0.80 seed / staging source とする。
- registrar handoff、代表テスト、非削除チェック、docs 同期が push 済みであることを前提にする。
- PR 作成ではなく、新規 Public repo 初期投入の source snapshot として扱う。

### 除外候補

Public repo へ投入する前に、次を除外または公開可否確認する。

- secret / token / API key / private key
- local config / machine-local path / user-local setting
- runtime cache / generated artifact / binary / large file
- logs / session dump / test evidence artifact
- private-only docs / private prompt / private dataset
- `.env`、`.pem`、`.key`、`config.yaml` 実体、`logs/`、`tmp/`、runtime DB

### 事前確認コマンド

```bash
git status --short --branch
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw ./internal/features/... ./internal/adapter/viewer ./modules/...
git diff --check
git ls-files | rg '(^|/)(\\.env|config\\.yaml|.*\\.pem|.*\\.key|logs/|tmp/|cache|artifact|\\.db$)'
rg -n '(api[_-]?key|secret|token|password|sk-[A-Za-z0-9]|BEGIN (RSA |OPENSSH |EC )?PRIVATE KEY)' --glob '!vendor/**' --glob '!node_modules/**' --glob '!logs/**' --glob '!tmp/**'
```

### 現ブランチで確認済みの公開前レビュー候補

2026-07-01 の tracked file 名ベース確認では、次が RenCrow_CORE Public repo 投入前のレビュー候補として出ている。これは削除指示ではなく、公開範囲制御または公開可否判断の対象である。

- `config.yaml`
- `.env.example`
- `config.yaml.example`
- `config/config.yaml.example`
- `tmp/stt_inputs/*`
- `tmp/stt_test_*`
- `tmp/viewer_test_recording_script*.md`
- `docs/archive/unreferenced_20260701/STT_TTS/tmp/*`
- `docs/refs/STT_TTS/tmp/*`
- `artifact` / `cache` を名前に含む source or docs

Public repo 投入前に、上記が sample / fixture / docs として公開可能か、または export から除外すべきかを決める。

### 起点化完了条件

- remote HEAD が Ver0.80 seed として説明できる。
- 既存機能非削除チェック表にある route / CLI / Viewer tab / background job / module endpoint が落ちていない。
- `modules/README.md`、`modules/CURRENT_MAP.md`、`internal/features/README.md` が HEAD の状態と矛盾しない。
- Public repo 投入時に除外するものが、削除ではなく公開範囲の制御として説明できる。
