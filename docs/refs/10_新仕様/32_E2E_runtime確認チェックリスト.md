# E2E runtime 確認チェックリスト

## 1. 目的

この文書は、`docs/10_新仕様/31_未実装項目実装仕様.md` の Phase 12「E2E / runtime 要確認項目の棚卸し」を実行するためのチェックリストである。

`17_E2E残課題.md` は残課題の台帳であり、本書は実行担当者が実機 / live runtime / browser / 外部チャネルで確認するときの証跡、成功条件、失敗条件を定義する。

以下を原則とする。

- skip は成功扱いにしない。
- fallback は成功扱いにしない。
- health ok だけで user flow 成立扱いにしない。
- handler / stub / unit test だけで実 API E2E 完了扱いにしない。
- repo example config だけで runtime 確認済みにしない。
- 古いログを根拠にしない。

## 2. 参照仕様

- `docs/10_新仕様/10_検証仕様.md`
- `docs/10_新仕様/13_実装項目インベントリ.md`
- `docs/10_新仕様/17_E2E残課題.md`
- `docs/10_新仕様/31_未実装項目実装仕様.md`

## 3. 共通前提

### 3.1 live runtime config

実機確認では、repo 内の example config ではなく live runtime config を確認する。

確認対象:

- `~/.picoclaw/config.yaml`
- `http://127.0.0.1:18790/health`
- `http://127.0.0.1:18790/viewer/runtime-config`
- `/viewer` の実表示
- event log
- Worker / channel / provider の実ログ

### 3.2 証跡として必ず残すもの

各確認では、以下を記録する。

| 証跡 | 内容 |
| --- | --- |
| 実行日時 | いつ確認したか |
| 実行環境 | local / distributed / browser / external API |
| config source | live config の path または runtime-config response |
| 実行コマンド | `go test`, curl, browser操作、外部イベント送信など |
| route / session | route、job_id、session_id、channel id |
| event log | routing、agent response、attachment、warning、error |
| response | ユーザーに返った本文または失敗表示 |
| 判定 | pass / fail / skipped / blocked |
| skip理由 | secret不足、実機不足、device不足など |

## 4. 確認項目

### 4.0 2026-05-18 local verification result

実行済み:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./...
```

判定:

- pass。
- production code の package test は通過。

実行済み:

```bash
node --test \
  internal/adapter/viewer/viewer_memory_panel.test.mjs \
  internal/adapter/viewer/viewer_idle_mode_buttons.test.mjs \
  internal/adapter/viewer/viewer_audio_button.test.mjs \
  internal/adapter/viewer/viewer_stt_https.test.mjs
```

判定:

- pass。
- 2026-05-18 に `viewer_audio_button.test.mjs` の fake DOM に `document.body` / stable `main` / Source Registry refresh stub を補い、`viewer_stt_https.test.mjs` を現行の初期 `home` tab 契約に合わせた。
- audio / STT / memory / idle mode の Node Viewer contract は 34件 pass。

扱い:

- Go test pass は runtime / handler / domain 層の回帰確認として扱う。
- Viewer audio / STT の Node test pass は静的 contract の回帰確認として扱う。
- 実ブラウザ audio / STT live E2E は別確認であり、Node test pass だけで完了扱いにしない。

### 4.0.0 2026-05-19 current worktree regression result

実行済み:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./...
node internal/adapter/viewer/viewer_memory_panel.test.mjs
git diff --check
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run TestAPIErrorIncludesStatus -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run 'TestSuperAgentStatus|TestSuperAgentStatusRejectsDuplicateCurrentView' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run 'TestAIWorkflowStatusAndContextBudget|TestAIWorkflowStatusRejectsMalformedCurrentView' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run 'TestComplexityStatus|TestComplexityStatusRejectsMalformedCurrentView' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run 'TestSandboxStatus|TestSandboxStatusRejectsMalformedCurrentView' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run 'Test(RevenueStatus|SkillGovernanceStatus)' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run 'Test(EvaluateHeavyWorker|HeavyWorkerRuntimeDiagnostics)' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run 'TestWorkstreamStatus' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run 'TestBrowserTraceAPI' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/viewer -run 'TestHandleSuperAgentStatus' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./pkg/rencrowclient -run 'TestAIWorkflowStatus' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/viewer -run 'TestHandleAIWorkflowStatus' -count=1 -v
PICOCLAW_BROWSER_E2E=1 PICOCLAW_LIVE_E2E=1 PICOCLAW_LIVE_BASE_URL=http://127.0.0.1:18790 GOCACHE=/tmp/picoclaw-gocache go test -tags=e2e ./test/e2e -run TestE2E_Phase25BrowserViewerSessionContract -count=1 -v
PICOCLAW_LIVE_E2E=1 PICOCLAW_LIVE_BASE_URL=http://127.0.0.1:18790 GOCACHE=/tmp/picoclaw-gocache go test -tags=e2e ./test/e2e -run TestE2E_SuperAgentRunQueueClientManualLedgerFlow -count=1 -v
PICOCLAW_LIVE_E2E=1 PICOCLAW_LIVE_BASE_URL=http://127.0.0.1:18790 GOCACHE=/tmp/picoclaw-gocache go test -tags=e2e ./test/e2e -run TestE2E_AIWorkflowExternalControlClientRequiresApproval -count=1 -v
```

判定:

- pass。
- `go test ./...` は全 package test 通過。
- `viewer_memory_panel.test.mjs` は 14件 pass。
- `git diff --check` は pass。
- `pkg/rencrowclient` は non-2xx response を `APIError` として返し、method / path / status / body を呼び出し側に残せることを確認済み。
- `pkg/rencrowclient.SuperAgentStatus` は status API の current view に同一 `run_id` / `queue_id` の重複や必須 ID 欠落がある場合、E2E 証跡として成功扱いしないことを確認済み。
- `pkg/rencrowclient.SuperAgentStatus` は terminal `agent_run` / terminal `run_queue` に `completed_at` がない場合も、E2E 証跡として成功扱いしないことを確認済み。
- `pkg/rencrowclient.SuperAgentStatus` は scheduler enabled なのに interval / claim limit が 0 以下の場合も、E2E 証跡として成功扱いしないことを確認済み。Viewer Ops は live runtime config の scheduler disabled を `scheduler:disabled` / `blocked: scheduler disabled` として表示する。
- 同 validation 補強後、live service の SuperAgent manual ledger / pause-resume queue reentry E2E は pass。これは current view の証跡読み取り確認であり、scheduler 正常完了 E2E ではない。
- 2026-05-19 19:19 UTC に `pkg/rencrowclient.SuperAgentStatus` は `agent_run.status` を `running` / `paused` / `completed` / `failed` / `cancelled`、`run_queue.status` を `queued` / `claimed` / `completed` / `failed` / `cancelled` に限定する local client test を追加した。live SuperAgent manual ledger / pause-resume queue reentry E2E は再 pass。これは status 文字列だけで scheduler 正常完了や真の長時間再開成功を判断しないための境界確認であり、scheduler 正常完了 E2E ではない。
- 2026-05-19 19:53 UTC に `pkg/rencrowclient.SuperAgentStatus` は `agent_run.status=failed` に `summary`、`run_queue.status=failed` に `reason` を要求する local client test を追加した。live SuperAgent manual ledger / pause-resume queue reentry E2E は再 pass。これは failed terminal を証跡なしで成功扱いしないための境界確認であり、scheduler 正常完了 E2E ではない。
- 2026-05-20 01:31 UTC に `pkg/rencrowclient.SuperAgentStatus` は `subagent_task` / `context_pack` / `message_channel` / `trace_event` の必須 field、重複 ID、`context_pack.token_estimate` 負数を拒否する local client test を追加した。これは current view の証跡読み取り境界確認であり、scheduler 正常完了 E2E ではない。
- 2026-05-20 03:05 UTC に `pkg/rencrowclient.SuperAgentStatus` は `agent_run.started_at`、`subagent_task.created_at`、`context_pack.created_at`、`message_channel.created_at`、`trace_event.created_at`、`run_queue.created_at` 欠落を拒否する local client test を追加した。これは timestamp 欠落の current view を SuperAgent status 証跡として扱わないための境界確認であり、scheduler 正常完了 E2E ではない。
- 2026-05-20 05:40 UTC に `pkg/rencrowclient.ClaimRunQueueItem` / `CompleteRunQueueItem` は claimed item の `created_at` 欠落、terminal complete item の `created_at` / `completed_at` 欠落を拒否する local client test を追加した。これは timestamp 欠落の run queue direct response を SuperAgent run queue 台帳証跡として扱わないための境界確認であり、scheduler 正常完了 E2E ではない。
- 2026-05-20 07:00 UTC に SuperAgent 保存前 domain validation は agent_run の `started_at` 欠落、subagent_task / context_pack / message_channel / trace_event / run_queue の `created_at` 欠落、terminal agent_run / run_queue の `completed_at` 欠落を拒否する test を追加した。timestamp 欠落の ledger item は保存時点で拒否し、後続 current view を malformed にしない。
- `pkg/rencrowclient.AIWorkflowStatus` は status API の current view に workflow event / project memory / worktree / command / context usage の同一 ID 重複や必須 ID 欠落がある場合、E2E 証跡として成功扱いしないことを確認済み。
- `pkg/rencrowclient.AIWorkflowStatus` は context budget policy が enabled なのに warn / stop ratio が不正な場合も、E2E 証跡として成功扱いしないことを確認済み。Viewer Ops は live runtime config の context budget disabled を `context-budget:disabled` / `blocked: context budget disabled` として表示する。
- 2026-05-20 05:45 UTC に `pkg/rencrowclient.RunCommand` / `CheckContextBudget` は command invocation event、returned context usage、context budget warning / stop event の `created_at` 欠落を拒否する local client test を追加した。これは timestamp 欠落の direct response を AI Workflow command / context budget 証跡として扱わないための境界確認であり、scheduler 起点 E2E ではない。
- 2026-05-20 07:10 UTC に AI Workflow 保存前 domain validation は workflow event / worktree / context usage の `created_at` 欠落、project memory / command の `updated_at` 欠落を拒否する test を追加した。timestamp 欠落の ledger item は保存時点で拒否し、後続 current view を malformed にしない。
- 2026-05-20 07:20 UTC に Persona 保存前 domain validation は discomfort / trigger / canonical response / observation / meta profile update / interface session の `created_at` 欠落を拒否する test を追加した。timestamp 欠落の observation ledger item は保存時点で拒否し、後続 current view を malformed にしない。
- 2026-05-20 10:50 UTC に Viewer Ops の Persona Observation fetch failure 境界を補強し、`/viewer/persona-observation` が non-2xx の場合は stale approved Meta 更新候補や空台帳ではなく `persona observation status unavailable: ...` / `blocked: persona meta review state unreadable` / `blocked: long-term personality update state unreadable` を表示する Node contract test を追加した。これは status API failure を Human Review ありの meta 適用や long-term personality update 成功として誤読しないための visible-state 境界確認であり、meta 適用 live E2E の代替ではない。
- 2026-05-20 05:50 UTC に `pkg/rencrowclient.EvaluateHeavyWorker` は Heavy Worker requested event の `created_at` 欠落を拒否する local client test を追加した。これは timestamp 欠落の direct response を Heavy Worker requested 証跡として扱わないための境界確認であり、RouteANALYZE 実運用成功ではない。
- `pkg/rencrowclient.ComplexityStatus` は status API の current view に scan / hotspot / evidence / report の同一 ID 重複や必須 ID 欠落がある場合、E2E 証跡として成功扱いしないことを確認済み。
- `pkg/rencrowclient.ComplexityStatus` は `status=completed` の scan に `completed_at` がない場合も、E2E 証跡として成功扱いしないことを確認済み。
- `pkg/rencrowclient.ComplexityStatus` は proposal / coder diff request / concrete diff proposal report が `pending_review` 以外の場合、また concrete diff proposal content が `Patch applied: false` と Human approval required を示さない場合も、E2E 証跡として成功扱いしないことを確認済み。Viewer Ops は live current view の review-only report を `mode: review-only` / `blocked: no patch applied` として表示する。
- 2026-05-20 01:26 UTC に `pkg/rencrowclient.ComplexityStatus` は scan count 負数、hotspot / evidence line range 不整合、hotspot confidence 範囲外の current view を local client error として拒否する test を追加した。これは malformed numeric evidence を Complexity status 証跡として扱わないための境界確認であり、実 Coder provider 成功、patch 適用、Sandbox apply、外部 PR 作成の代替ではない。
- 2026-05-20 03:55 UTC に `pkg/rencrowclient.ComplexityStatus` は scan / hotspot / evidence / report の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の ledger item は report-only scan、review-only proposal、Coder diff failure audit の証跡として扱わない。
- 2026-05-20 06:45 UTC に Complexity Hotspot の保存前 domain validation は scan / hotspot / evidence / report の `created_at` 欠落と completed scan の `completed_at` 欠落を拒否する test を追加した。timestamp 欠落の ledger item は保存時点で拒否し、後続 current view を malformed にしない。
- 2026-05-20 06:00 UTC に `pkg/rencrowclient.CreateComplexityConcreteDiff` / `CreateComplexityCoderDiff` は response hotspot / concrete diff artifact / Workstream Artifact / Sandbox Promotion / Sandbox Gate Log の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の direct response は review-only concrete diff / coder diff 証跡として扱わない。
- `TestE2E_ComplexityStatusClientCurrentView` は live `/viewer/complexity-hotspots` を `pkg/rencrowclient.ComplexityStatus` 経由で読み、completed `report_only` scan / hotspot / evidence / `pending_review` report artifact の current view を確認する。これは status 証跡読み取り E2E であり、実 Coder provider 成功、patch 適用、Sandbox apply、外部 PR 作成の代替ではない。
- 2026-05-20 10:20 UTC に Viewer Ops は `/viewer/complexity-hotspots` fetch failure 時、stale な review artifact を表示せず `Complexity review artifacts unavailable: ...` / `blocked: patch apply state unreadable` を表示する Node contract test を追加した。これは status API failure を patch 適用証跡として扱わないための local 表示境界であり、実 Coder provider 成功、Sandbox apply、外部 PR 作成の完了条件にはしない。
- `pkg/rencrowclient.SandboxStatus` は status API の current view に sandbox / artifact / promotion / gate log の同一 ID 重複や必須 ID 欠落、decision status 欠落がある場合、E2E 証跡として成功扱いしないことを確認済み。
- `pkg/rencrowclient.SandboxStatus` は `gate_status=promotion_applied` の Gate Log に `post_apply_verification` がない場合も、E2E 証跡として成功扱いしないことを確認済み。
- `pkg/rencrowclient.SandboxStatus` は `decision.status` / `gate_log.gate_status` が `approve` / `reject` / `needs_review` / `needs_more_tests` / `promotion_applied` / `rollback_executed` 以外の場合も、E2E 証跡として成功扱いしないことを確認済み。2026-05-19 20:23 UTC の通常 config live `/viewer/sandbox?limit=1` は 503 `sandbox store unavailable` のため、正式 apply / rollback 成功の確認ではない。
- 2026-05-20 01:48 UTC に `pkg/rencrowclient.SandboxStatus` は `promotion_applied` / `rollback_executed` Gate Log について、Gate Log / Promotion の Human approval、apply diff、post-apply verification path、rollback plan path、completed artifact が揃わない current view を local client error として拒否する test を追加した。これは terminal status 文字列だけを正式 apply / rollback 証跡として扱わないための境界確認であり、通常 config の Sandbox disabled 状態や外部 PR 未作成の残課題は変わらない。
- 2026-05-20 04:05 UTC に `pkg/rencrowclient.SandboxStatus` は sandbox / artifact / promotion / gate log の created_at 欠落を local client error として拒否する test を追加した。timestamp 欠落の status view は Promotion Request / Gate / artifact や正式 apply / rollback 証跡として扱わない。
- 2026-05-20 10:10 UTC に Viewer Ops は `/viewer/sandbox` fetch failure 時、stale な `promotion_applied` Gate Log を表示せず `Sandbox gate logs unavailable: ...` / `blocked: promotion apply state unreadable` を表示する Node contract test を追加した。これは status API failure を正式 apply / rollback 証跡として扱わないための local 表示境界であり、live apply / rollback E2E の完了条件にはしない。
- 2026-05-20 05:20 UTC に `pkg/rencrowclient.CreatePromotionRequest` / `ApplyPromotion` / `RollbackPromotion` は direct response の promotion / gate_log / post-apply artifact / rollback artifact `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の direct response は Promotion Request / apply / rollback 証跡として扱わない。
- 2026-05-20 07:50 UTC に Sandbox 保存前 domain validation は sandbox / artifact / promotion / gate log の `created_at` 欠落を拒否する test を追加した。timestamp 欠落の Promotion Request / Gate / artifact ledger item は保存時点で拒否し、後続 current view を malformed にしない。
- `pkg/rencrowclient.RevenueStatus` は status API の current view に Human Decision / Daily Routine / Channel Draft / External Send Apply の同一 ID 重複や必須 ID 欠落、verified send record なしの `external_actions_applied=true` がある場合、E2E 証跡として成功扱いしないことを確認済み。`external_send_applied=false` の apply record が `post_send_verified=true` / `post_send_evidence` / `apply_status=sent` を持つ場合も、実外部送信成功証跡として受け取らない。external channel readiness の `external_channel_adapter=unconfigured` / `external_channel_adapter_configured=false` / `human_approval_required_for_external_send=true` も current view 証跡として検証する。
- 2026-05-20 01:18 UTC に `pkg/rencrowclient.RevenueStatus` は dashboard summary の pending decision / daily report / channel draft / external send apply count が負数の current view を local client error として拒否する test を追加した。これは malformed summary count を dashboard 証跡として扱わないための境界確認であり、実外部送信 E2E の代替ではない。
- 2026-05-20 02:32 UTC に `internal/domain/revenue.ValidateExternalSendApplyRecord`、`pkg/rencrowclient.ApplyRevenueExternalSend`、`RevenueStatus` は未送信・未適用 record の `send_result=sent` と、external channel adapter required / unconfigured 状態なのに record 側だけ設定済み `channel_adapter` を持つ current view / direct response を local error として拒否する test を追加した。これは blocked audit を実送信成功や adapter 接続済み証跡として扱わないための境界確認であり、実外部送信 E2E の代替ではない。
- 2026-05-20 04:15 UTC に `pkg/rencrowclient.RevenueStatus` は Human Decision / Daily Routine / Channel Draft / External Send Apply の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の current view は dashboard / external send audit 証跡として扱わない。
- 2026-05-20 04:35 UTC に `pkg/rencrowclient.ApplyRevenueExternalSend` は direct response の external send apply record の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の direct audit response は実外部送信 / blocked audit 証跡として扱わない。
- 2026-05-20 04:50 UTC に `pkg/rencrowclient.EvaluateRevenueHumanDecision` / `ReviewRevenueHumanDecision` / `CreateRevenueDailyRoutineReport` / `CreateRevenueChannelDraft` は direct response の record / report / draft の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の direct response は Human Decision Gate / draft-only 証跡として扱わない。
- 2026-05-20 07:30 UTC に Revenue 保存前 domain validation は market research / SNS post metric / product / customer voice / revenue event / daily routine / channel draft / external send apply / Human Decision Gate record の `created_at` 欠落を拒否する test を追加した。timestamp 欠落の revenue ledger item は保存時点で拒否し、後続 dashboard / external send audit current view を malformed にしない。
- 2026-05-20 01:20 UTC に `pkg/rencrowclient.AIWorkflowStatus` は context usage の `estimated_cost` / `kv_cache_estimate` が負数の current view を local client error として拒否する test を追加した。これは malformed numeric estimate を AI Workflow status 証跡として扱わないための境界確認であり、scheduler 起点 E2E や正式 apply E2E の代替ではない。
- 2026-05-20 02:55 UTC に `pkg/rencrowclient.AIWorkflowStatus` は workflow event / worktree / context usage の `created_at`、project memory / command の `updated_at` 欠落を local client error として拒否する test を追加した。これは timestamp 欠落の current view を AI Workflow status 証跡として扱わないための境界確認であり、scheduler 起点 E2E や正式 apply E2E の代替ではない。
- `pkg/rencrowclient.SkillGovernanceStatus` は status API の current view に manifest / trigger / change / contribution / external PR submit / coder transcript の同一 ID 重複や必須 ID 欠落、`pr_url` / post-submit 証跡なしの created PR record がある場合、E2E 証跡として成功扱いしないことを確認済み。`external_pr_created=false` の submit record が `submit_status=created` / `pr_url` / `post_submit_verified=true` / `post_submit_evidence` を持つ場合も、実 PR 作成成功証跡として受け取らない。external PR readiness の `external_pr_adapter=unconfigured` / `external_pr_adapter_configured=false` / `human_approval_required_for_pr=true` も current view 証跡として検証する。
- 2026-05-19 19:16 UTC に `pkg/rencrowclient.RevenueStatus` は `apply_status` を `blocked` / `failed` / `sent` に限定し、`pkg/rencrowclient.SkillGovernanceStatus` は `submit_status` を `blocked` / `failed` / `created` に限定する local client test を追加した。live `TestE2E_RevenueExternalSendClientRequiresApprovalAndAuditsBlockedApply` と `TestE2E_SkillGovernanceExternalPRClientRequiresApprovalAndAuditsBlockedSubmit` は再 pass。これは status 文字列だけで実外部送信 / 実 PR 作成を成功扱いしないための境界確認であり、実外部送信 E2E や実 PR 作成 E2E の代替ではない。
- 2026-05-20 02:32 UTC に `pkg/rencrowclient.SubmitSkillGovernanceExternalPR` / `SkillGovernanceStatus` は external PR adapter required / unconfigured 状態なのに record 側だけ設定済み `pr_adapter` を持つ current view / direct response を local error として拒否する test を追加した。これは blocked audit を GitHub adapter 接続済みや実 PR 作成証跡として扱わないための境界確認であり、実 PR 作成 E2E の代替ではない。
- 2026-05-20 04:15 UTC に `pkg/rencrowclient.SkillGovernanceStatus` は trigger log / change log / contribution / external PR submit / coder transcript の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の current view は external PR audit / Coder evidence 証跡として扱わない。
- 2026-05-20 04:35 UTC に `pkg/rencrowclient.SubmitSkillGovernanceExternalPR` は direct response の external PR submit record の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の direct audit response は実 PR 作成 / blocked audit 証跡として扱わない。
- 2026-05-20 04:50 UTC に `pkg/rencrowclient.EvaluateSkillGovernanceContributionGate` は direct response の gate log の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の direct gate response は Contribution Gate 証跡として扱わない。
- 2026-05-20 07:40 UTC に Skill Governance 保存前 domain validation は manifest の `updated_at` 欠落、trigger / change / contribution / external PR submit / coder transcript の `created_at` 欠落を拒否する test を追加した。timestamp 欠落の external PR audit / Coder evidence ledger item は保存時点で拒否し、後続 current view を malformed にしない。
- `pkg/rencrowclient.EvaluateHeavyWorker` / `HeavyWorkerRuntimeDiagnostics` は Heavy Worker evaluate / runtime diagnostics API が 2xx を返しても request echo、decision status、requested event、parent event、agent、runtime role / route、failure semantics、LLM Ops unavailable error が整合しない場合、E2E 証跡として成功扱いしないことを確認済み。
- 2026-05-19 22:25 UTC に Heavy Worker runtime diagnostics の LLM Ops status probe も read-only timeout を使うようにし、live `/viewer/ai-workflow/heavy-worker/runtime-diagnostics` が約 3 秒で 200 body を返すことを確認した。body は `role=Heavy` / `route=ANALYZE` / configured base/model を含むが、`llm_ops.live_available=false` と `llm-ops GET /v1/status ... context deadline exceeded` を返すため、Heavy 実 provider 成功や LLM Ops 管理 API 到達成功とは扱わない。browser E2E でも Ops `Heavy Runtime` card の `route: ANALYZE` と `llm-ops GET /v1/status` visible state を確認した。
- 2026-05-20 10:55 UTC に Viewer Ops の Heavy Worker runtime diagnostics fetch failure 境界を補強し、`/viewer/ai-workflow/heavy-worker/runtime-diagnostics` が non-2xx の場合は stale Heavy provider / LLM Ops live state ではなく `heavy runtime diagnostics unavailable: ...` / `blocked: RouteANALYZE provider state unreadable` / `blocked: LLM Ops live state unreadable` を表示する Node contract test を追加した。これは runtime diagnostics API failure を Heavy 実 provider成功、RouteANALYZE 実運用成功、LLM Ops 管理 API 到達成功として誤読しないための visible-state 境界確認であり、Heavy 実 provider E2E や scheduler 正常完了 E2E の代替ではない。
- 2026-05-20 12:15 UTC に Viewer Ops の persisted logs fetch failure 境界を補強し、`/viewer/logs?scope=persisted` が non-2xx の場合は stale latest job / Mio last report / last error / operator feed を使わず `ops logs unavailable: ...` / `Ops logs unavailable: ...` を表示する Node contract test を追加した。これは route failure を最新ジョブ、Mio報告、失敗イベント、operator feed 成功として誤読しないための visible-state 境界確認であり、live session E2E や runtime health 成功の代替ではない。
- 2026-05-20 12:25 UTC に Viewer monitor の status fetch failure 境界を補強し、`/viewer/status` が non-2xx の場合は stale agent running / idle / job state を使わず各 agent を `unavailable`、reason を `viewer status unavailable: ...` として表示する Node contract test を追加した。これは route failure を agent 稼働成功、live session 成功、runtime health 成功として誤読しないための visible-state 境界確認であり、live session E2E や runtime health 成功の代替ではない。
- 2026-05-20 13:55 UTC に Viewer Chat の `/viewer/send` failure 境界を補強し、non-2xx の場合は generic `send failed` ではなく body 付き `Viewer send unavailable: HTTP <status>: ...` を通常 chat timeline に表示する Node contract test を追加した。これは send route failure を通常 chat 応答成功や live session 成功として扱わないための local 表示境界であり、live Viewer send E2E の代替ではない。
- 2026-05-20 14:15 UTC に Viewer IdleChat control failure 境界を補強し、start / stop / mode control API が non-2xx の場合は console-only generic `idlechat control failed` ではなく body 付き `IdleChat control unavailable: HTTP <status>: ...` を IdleChat table に表示する Node contract test を追加した。これは control route failure を IdleChat 開始 / 停止 / mode 切替成功や live IdleChat session 成功として扱わないための local 表示境界であり、live IdleChat E2E の代替ではない。
- 2026-05-20 14:25 UTC に Viewer IdleChat status / logs fetch failure 境界を補強し、`/viewer/idlechat/status` と `/viewer/idlechat/logs?limit=20` が non-2xx の場合は stale summary row や空表示を使わず、body 付き `IdleChat status unavailable: HTTP <status>: ...` / `IdleChat logs unavailable: HTTP <status>: ...` を IdleChat table に表示する Node contract test を追加した。これは status / logs route failure を IdleChat 稼働状態、履歴取得、live IdleChat session 成功として扱わないための local 表示境界であり、live IdleChat E2E の代替ではない。
- 2026-05-20 14:35 UTC に Viewer live mode topic fetch failure 境界を補強し、`mode=live` の topic polling で `/viewer/idlechat/status` が non-2xx の場合は topic text を空や stale のままにせず、body 付き `IdleChat status unavailable: HTTP <status>: ...` を `#liveTopicText` に表示する Node contract test を追加した。これは live topic bar の status route failure を live IdleChat topic 更新成功や live session 成功として扱わないための local 表示境界であり、live IdleChat E2E の代替ではない。
- 2026-05-20 10:01 UTC に IdleChat 外部 topic / trend source の HTTP failure 境界を補強し、NHK RSS / Google News RSS の non-2xx response body を error に保持する local test を追加した。Wikipedia / Google Trends / Reddit / Hatena の status error も同じ body 付き整形へ揃えた。これは外部 topic seed / trend fetch failure を status だけで失わないための local 境界確認であり、live IdleChat 会話 E2E や外部 RSS / API 到達成功の代替ではない。
- 2026-05-20 08:20 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer` を Playwright Chromium で開き、route fulfill により `/viewer/idlechat/status` / `/viewer/idlechat/logs?limit=20` の non-2xx を再現した。IdleChat table と live topic bar に body 付き failure が表示されることを確認したが、API failure 再現の表示境界確認であり、live IdleChat 会話 E2E の代替ではない。スクリーンショットは `tmp/viewer-idlechat-failure-boundary.png` と `tmp/viewer-live-topic-failure-boundary.png`。
- 2026-05-20 08:23 UTC に Viewer Ops runtime config / debug system snapshot fetch failure 境界を補強し、`/viewer/runtime-config` と `/viewer/debug/system` が non-2xx の場合は stale local LLM config や単なる disabled / missing 表示にせず、body 付き `runtime config unavailable: HTTP <status>: ...` と `blocked: HTTP <status>: ...` を runtime readiness に表示する Node contract test を追加した。これは runtime dependency route failure を live runtime config 成功、STT / TTS readiness 成功、LLM Ops 到達成功として扱わないための visible-state 境界確認であり、実 browser audio / STT / TTS E2E の代替ではない。
- 2026-05-20 08:26 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=ops` を Playwright Chromium で開き、route fulfill により `/viewer/runtime-config` 503 と `/viewer/debug/system` 502 を再現した。Runtime Config / STT / TTS readiness に body 付き failure が表示されることを確認した。スクリーンショットは `tmp/viewer-runtime-config-failure-boundary.png` と `tmp/viewer-debug-system-failure-boundary.png`。これは API failure 再現の表示境界確認であり、live runtime config 成功や実 browser audio / STT / TTS E2E の代替ではない。
- 2026-05-20 08:29 UTC に Viewer TTS audio unlock / playback failure 境界を補強し、browser autoplay block などで `Audio.play()` が reject した場合は console-only `tts audio ... failed` にせず、`#ttsNowPlaying` に `TTS audio unavailable: <error>` を表示し、audio button の blocked state に error detail を保持する Node contract test を追加した。これは TTS playback / lip sync 成功の代替ではなく、音声失敗を本文表示 fallback や口パク成功と混同しないための visible-state 境界確認である。
- 2026-05-20 08:31 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer` を Playwright Chromium で開き、`HTMLMediaElement.play()` reject を再現した。`#ttsNowPlayingText` に `TTS audio unavailable: NotAllowedError: browser blocked autoplay` が表示され、audio button title に error detail が残ることを確認した。スクリーンショットは `tmp/viewer-tts-audio-failure-boundary.png`。これは browser audio failure の visible-state 境界確認であり、TTS playback / lip sync 成功の代替ではない。
- 2026-05-26 UTC に TTS timeout / drain の local 境界を補強した。IdleChat 発話単位 TTS 待ちは 15 秒、session drain は 15 秒で、発話 timeout は `tts_error_kind=timeout`、drain timeout は `session_audio_timeout` として記録する。timeout 済み TTS public route / pending を閉じて遅延 audio chunk を stale drop し、Viewer pending message fallback も 15 秒の `display_only` / `idle-display-only` に揃えた。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/application/idlechat`、`GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw`、`node internal/adapter/viewer/viewer_audio_button.test.mjs`、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は pass。これは local contract / visible-state 境界確認であり、実 browser audio playback / lip sync 成功の代替ではない。
- 2026-05-26 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer` を Playwright Chromium で開き、IdleChat pending message の display-only fallback と stale audio drop を確認した。browser 上で fallback delay 15000ms、`idle-display-only=true`、`idle-pending-tts=false`、古い session の `tts.audio_chunk` 投入後も `ttsPlayback.queue` が増えないことを確認した。これは実ブラウザ上の display-only / stale audio 境界確認であり、実 TTS provider 音声生成、実 audio playback、lip sync 成功の代替ではない。
- 2026-05-20 08:33 UTC に Viewer STT capture log / session id copy failure 境界を補強し、Clipboard write が reject した場合は toast / console-only にせず、`debugSttSession` と session badge に `STT log copy unavailable: ...` / `STT session copy unavailable: ...` を保持する Node contract test を追加した。これは STT 証跡コピー失敗を保存済み・共有済み証跡や実 mic STT 成功として扱わないための visible-state 境界確認である。
- 2026-05-20 08:36 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=ops` を Playwright Chromium で開き、clipboard write reject を再現した。`debugSttSession` と session badge に `STT log copy unavailable: clipboard denied` が表示されることを確認した。スクリーンショットは `tmp/viewer-stt-copy-failure-boundary.png`。これは STT 証跡コピー失敗の visible-state 境界確認であり、実 browser microphone STT E2E の代替ではない。
- 2026-05-19 20:01 UTC に `pkg/rencrowclient.RuntimeHealth` を追加し、`/health` の 503 down body を client validation 経由で読めるようにした。HTTP status と body status の矛盾、down/degraded check の message 欠落、overall status と checks の矛盾は local client test で拒否する。live `TestE2E_Phase25LiveRuntimeHealth` は `local_llm_chat` / `local_llm_worker` の `connection refused` checks を出して fail のまま維持する。
- 2026-05-19 21:44 UTC に `/health` / `/ready` handler の request timeout を短時間化し、local LLM endpoint 不達時も 5 秒 client timeout ではなく約 1.04 秒で 503 down body を返すことを live service で確認した。body は `local_llm_chat` / `local_llm_worker` の `connection refused` checks を含む。これは blocked 証跡の取得改善であり、`TestE2E_Phase25LiveRuntimeHealth` の成功条件ではない。
- 2026-05-19 20:09 UTC に `pkg/rencrowclient.LLMOpsHealth` / `LLMOpsStatus` を追加し、`/viewer/llm-ops/health` / `status` の 2xx response でも health status、roles / health / halted / pid が不整合なら成功扱いしない local client test を追加した。live `TestE2E_Phase25LiveLLMOpsProxyClientBlockedOrLive` は health/status の 502 `upstream unreachable` を `APIError` body として保持することを確認して pass。これは LLM Ops 管理 API 不達の blocked 証跡であり、Chat / Worker endpoint 復旧ではない。
- 2026-05-19 20:13 UTC に `pkg/rencrowclient.StopLLMOps` / `StartLLMOps` / `RestartLLMOps` を追加し、role / selection validation と 502 body retention を local client test で確認した。live `TestE2E_Phase25LiveLLMOpsProxyClientBlockedOrLive` は stop/start/restart も 502 `upstream unreachable` を blocked 証跡として確認して pass。これは実 model control 成功ではない。
- 2026-05-20 01:55 UTC に `pkg/rencrowclient.LLMOpsStatus` は memory `llm_by_role` に対応する `roles` state がない response、memory role mismatch、negative port / rss_mib / pid を local client error として拒否する test を追加した。これは process/memory 情報だけを LLM Ops 管理 API 到達や model control 成功の証跡として扱わないための境界確認である。
- 2026-05-19 20:18 UTC に L1 store 無効時も `/viewer/memory/layers` を route 未登録 404 にせず 503 `memory layers unavailable` として返す wiring test と live `TestE2E_Phase25LiveMemoryLayersUnavailableWhenL1Disabled` を追加して pass。これは Memory Layers 横断表示の成功ではなく、L1 store 無効の blocked 証跡を route で明示する確認である。
- 2026-05-19 20:45 UTC に `/viewer/runtime-config` の `runtime_readiness` へ Memory Layers / Source Registry の available と status route readiness を分けて追加した。live config は `memory_layers_available=false` / `memory_layers_status_available=true` / `source_registry_available=false` / `source_registry_status_available=true` を返し、`pkg/rencrowclient.RuntimeConfig` は available なのに status route がない response を malformed として拒否する。live `/viewer/memory/layers` と `/viewer/source-registry` は 503 のままで、browser Ops readiness は `memory-layers:missing` / `memory-route:present` / `source:missing` / `source-route:present` を visible state として表示する。これは blocked route の可視化であり、Memory Layers 横断表示や Source Registry staging review / validate / promote 成功ではない。
- 2026-05-19 20:23 UTC に Sandbox disabled 時も `/viewer/sandbox` status route を route 未登録 404 にせず 503 `sandbox store unavailable` として返す live `TestE2E_Phase25LiveSandboxStatusUnavailableWhenSandboxDisabled` を追加して pass。これは Sandbox status の blocked 証跡を route で明示する確認であり、Human approval あり正式 apply / rollback 成功ではない。
- 2026-05-19 20:30 UTC に `/viewer/runtime-config` の `runtime_readiness` へ `sandbox_enabled` / `sandbox_status_available` を追加し、live config では disabled だが status route present として確認できるようにした。`pkg/rencrowclient.RuntimeConfig` は sandbox enabled なのに status route がない response を malformed として拒否する。live `TestE2E_Phase25LiveViewerRuntimeConfigClient` と browser `TestE2E_Phase25BrowserViewerSessionContract` は pass。これは Sandbox disabled / blocked の可視化であり、正式 apply / rollback 成功ではない。
- 2026-05-19 20:38 UTC に `/viewer/runtime-config` の `runtime_readiness` へ Knowledge Memory / Browser Trace API の enabled / status route / fetcher route readiness を追加した。live config は `knowledge_memory_enabled=true` / `knowledge_memory_status_available=true` / `browser_trace_api_enabled=true` / `browser_trace_api_status_available=true` / `browser_trace_api_fetcher_available=true` を返し、`pkg/rencrowclient.RuntimeConfig` は enabled なのに status route がない response や fetcher route だけある response を malformed として拒否する。live `TestE2E_Phase25LiveViewerRuntimeConfigClient` と browser `TestE2E_Phase25BrowserViewerSessionContract` は pass。これは route availability の可視化であり、Knowledge Memory create/review/promote や Browser Trace discover/fetcher proposal/正式 API 採用の成功ではない。
- 2026-05-19 21:20 UTC に Viewer Ops へ `SuperAgent Terminal Audits` を追加し、live `/viewer/superagent?limit=3` の failed LeadAgent run summary と run queue terminal reason を visible state として確認した。browser E2E は `superagent terminal audits:` / `terminal runs` / `failed runs` / `missing evidence:` を確認して pass。これは failed terminal 証跡の可視化であり、scheduler 正常完了 E2E や真の長時間再開 E2E の完了条件にはしない。
- 2026-05-19 21:49 UTC に Viewer Ops へ `SuperAgent Resume Audits` を追加し、live `/viewer/superagent?limit=20` の `resume` queue reason が `without scheduler execution` の手動台帳証跡であることを `manual-ledger` / `blocked: true long-running resume not verified` として browser E2E で visible state 確認した。これは pause/resume queue reentry 台帳を、実行中 goroutine を伴う真の長時間再開 E2E と誤認しないための表示境界である。
- 2026-05-19 22:33 UTC に `SuperAgent Resume Audits` へ trace payload の `runtime_control=` action 表示と `runtime-control applied` 集計を追加した。manual ledger resume は `runtime-control:none` / `0 runtime-control applied` として browser E2E で確認し、pause/resume trace だけを実行中 goroutine 制御済みや真の長時間再開成功として扱わない。
- 2026-05-20 09:50 UTC に Viewer Ops の SuperAgent status fetch failure 境界を追加し、`/viewer/superagent` non-2xx 時は SuperAgent card / Terminal Audits / Resume Audits が `unavailable` と body を visible state として出す Node contract test を追加した。これは status API failure を空 audit や stale terminal / resume row と混同しないための表示境界であり、scheduler 正常完了 E2E や真の長時間再開 E2E の代替ではない。
- 2026-05-19 21:26 UTC に Viewer Ops へ `AI Workflow Run Evidence` を追加し、command / context usage / SuperAgent trace の `run_id` 単位照合結果と `blocked: scheduler normal completion not verified` を visible state として確認した。browser E2E は `ai workflow run evidence:` / `command-context-trace same-run` / blocked 表示を確認して pass。これは AI Workflow status 200 や過去 event を scheduler 起点正常完了と誤認しないための表示境界である。
- 2026-05-20 09:55 UTC に Viewer Ops の AI Workflow status fetch failure 境界を追加し、`/viewer/ai-workflow` non-2xx 時は AI Workflow card / Run Evidence が `unavailable` と body を visible state として出す Node contract test を追加した。これは status API failure を空 evidence や stale same-run row と混同しないための表示境界であり、scheduler 起点 E2E や正式 apply E2E の代替ではない。
- 2026-05-19 21:29 UTC に Skill Governance external PR audit summary へ `not created` 件数と `external PR adapter: unconfigured / configured: no / human approval: required` を追加し、browser E2E で visible state を確認した。これは external PR adapter 未接続の blocked audit を実 PR 作成成功と誤認しないための表示境界である。
- 2026-05-19 22:44 UTC に同 summary へ `blocked: no external PR created` を追加した。これは blocked audit を実 PR 作成成功と誤認しないための表示境界である。
- 2026-05-19 22:06 UTC に Viewer Ops へ `Skill Evidence Audits` を追加し、live `/viewer/skill-governance/recent?limit=10` の trigger / contribution gate / coder transcript 境界を `skill evidence audits:` として browser E2E で visible state 確認した。現 current view は trigger と passed contribution gate を返すが `coder_transcripts=[]` のため、`blocked: coder evidence transcript not observed` を表示する。これは Coder evidence transcript 保存完了や実 PR 作成成功ではない。
- 2026-05-19 22:47 UTC に同 summary へ `blocked: passed contribution gate is not external PR evidence` を追加した。passed contribution gate は Coder evidence transcript 保存完了や実 PR 作成成功の代替ではない。
- 2026-05-20 09:45 UTC に Viewer Ops の Skill Governance status fetch failure 境界を追加し、`/viewer/skill-governance/recent` non-2xx 時は Skill Governance card / external PR audit table / coder evidence audit table が `unavailable` と body を visible state として出す Node contract test を追加した。これは status API failure を空 audit や stale external PR row と混同しないための表示境界であり、実 PR 作成 E2E の代替ではない。
- 2026-05-20 00:38 UTC に `Skill Evidence Audits` の diff+transcript evidence 判定を、`CoderTranscriptEntry` の実 schema (`segment` / `evidence_path`) に合わせた。`patch_evidence` と `transcript_evidence` の両方が同一 job/session で揃った場合だけ evidence complete とし、片側だけの場合は E2E 証跡として受け取らない。`pkg/rencrowclient.SkillGovernanceStatus` も同じ境界を検証する。local test と `make install` / service restart 後の live browser E2E は pass したが、live current view は `coder_transcripts=[]` のため実 Coder Proposal evidence 発生の確認ではない。
- 2026-05-19 21:32 UTC に Revenue external send audit summary へ `not sent` 件数と `external channel adapter: unconfigured / configured: no / human approval: required` を追加し、browser E2E で visible state を確認した。これは external channel adapter 未接続の blocked audit を実外部送信成功と誤認しないための表示境界である。
- 2026-05-19 22:44 UTC に同 summary へ `blocked: no external send applied` を追加した。これは blocked audit を実外部送信成功と誤認しないための表示境界である。
- 2026-05-19 22:37 UTC に Revenue Channel Drafts summary へ `draft-only` 件数と `external send requires human approval: yes` を追加し、browser E2E で visible state を確認した。これは channel draft の存在を外部送信済みや post-send verification 成功として扱わないための表示境界である。
- 2026-05-20 09:40 UTC に Viewer Ops の Revenue status fetch failure 境界を追加し、`/viewer/revenue` non-2xx 時は Revenue card / drilldown / channel draft summary / external send audit table が `unavailable` と body を visible state として出す Node contract test を追加した。これは status API failure を空台帳や stale external send row と混同しないための表示境界であり、実外部送信 E2E の代替ではない。
- 2026-05-19 21:38 UTC に Complexity Review Artifacts 表を追加し、live current view の review-only reports を `pending-review` / `0 patch applied` / `human approval required` / `blocked: no patch applied` として browser E2E で visible state 確認した。live `/viewer/complexity-hotspots?limit=5` は `Patch applied: false` / `Human approval required: true` の concrete diff proposal を返す。これは patch 未適用、Sandbox apply 未実施、外部 PR 未作成の境界表示である。
- 2026-05-20 00:45 UTC に Complexity Coder diff provider failure の audit 境界を追加した。`complexity_coder_diff_failure` は `status=failed`、`Patch applied: false` を必須とし、Viewer Ops の `Complexity Review Artifacts` は failed 件数を表示する。`job_complexity_coder_diff_failure_audit_20260520004900` の live call は `503 complexity coder diff generation timed out` で止まり、current view に failed artifact として残ることを確認した。これは timeout / malformed output を成功扱いせず、かつ current view から消さないための証跡である。
- 2026-05-20 02:14 UTC に `pkg/rencrowclient.ComplexityStatus` の `complexity_coder_diff_failure` current view validation を追加補強し、failure artifact は `status=failed`、`Patch applied: false`、`Failure reason:` を満たさない場合に local client error として拒否する test を追加した。これは failed artifact 文字列だけを Coder provider timeout / unified diff 不備の証跡として受け取らないための境界確認である。
- 2026-05-19 22:10 UTC に Viewer Ops へ `Runtime Blocked Route Audits` を追加し、live `/viewer/source-registry?action=staging&limit=3` と `/viewer/memory/layers` の 503 body を `source registry unavailable` / `memory layers unavailable` として browser E2E で visible state 確認した。これは Source Registry / Memory Layers の route present を機能成功と誤認しないための blocked route 表示境界である。
- 2026-05-19 22:13 UTC に同 audit へ `/viewer/sandbox?limit=1` の `503 sandbox store unavailable` を追加した。browser E2E は `sandbox store unavailable` と runtime store requirement 表示を確認して pass。
- 2026-05-19 22:19 UTC に LLM Ops read-only proxy timeout を短縮し、live `/viewer/llm-ops/status` が upstream 不達時も約 3 秒で `502 upstream unreachable` を返すことを確認した。`Runtime Blocked Route Audits` に LLM Ops status を追加し、browser E2E は `upstream unreachable` と LLM Ops runtime dependency blocked 表示を確認して pass。これは LLM Ops 管理 API 成功や model control 成功ではない。
- `pkg/rencrowclient.WorkstreamStatus` は status API の current view に workstream / goal / artifact / annotation / steering / heartbeat / vault update の同一 ID 重複や必須 ID 欠落がある場合、E2E 証跡として成功扱いしないことを確認済み。
- `pkg/rencrowclient.WorkstreamStatus` は `status=completed` の Goal に `completed_at` がない場合も、E2E 証跡として成功扱いしないことを確認済み。
- `TestE2E_WorkstreamStatusClientCurrentView` は live `/viewer/workstreams` を `pkg/rencrowclient.WorkstreamStatus` 経由で読み、waiting goal と pending_review artifact の current view を確認する。これは Workstream status 証跡読み取り E2E であり、scheduler 起点 E2E、Vault approved review による実ファイル適用、正式 apply E2E の代替ではない。
- 2026-05-19 19:08 UTC に現 binary へ再 install / restart 後、live `/viewer/workstreams?limit=3` が waiting goal と pending_review artifact を返すこと、Viewer Ops card が `waiting goals:` / `pending-review:` / `mode: review-only` / `blocked: no vault apply` を visible state として表示することを `TestE2E_Phase25BrowserViewerSessionContract` で確認した。これは Workstream artifact / goal の存在を Vault apply や正式 apply 成功と誤認しないための表示境界であり、Vault approved review による実ファイル適用、scheduler 起点 E2E、正式 apply E2E の完了条件にはしない。
- 2026-05-19 19:13 UTC に `pkg/rencrowclient.WorkstreamStatus` が Vault update `review_status` の許可値を `pending` / `approved` / `rejected` に限定し、`applied` などの曖昧な status を E2E 証跡として受け取らない local client test を追加した。live `TestE2E_WorkstreamStatusClientCurrentView` は再 pass。これは Vault approved review による実ファイル適用や正式 apply E2E の代替ではない。
- 2026-05-20 01:52 UTC に `pkg/rencrowclient.WorkstreamStatus` は Vault update current view で `applied=true` が `review_status=approved` と `applied_path` を満たさない場合、および `applied=false` が `applied_path` を持つ場合を local client error として拒否する test を追加した。これは applied flag / path だけを Vault apply 成功証跡として扱わないための境界確認である。
- 2026-05-20 03:15 UTC に `pkg/rencrowclient.WorkstreamStatus` は workstream / goal / artifact / annotation / steering / heartbeat / vault update の `created_at` 欠落を local client error として拒否する test を追加した。これは timestamp 欠落の current view を Workstream status 証跡として扱わないための境界確認であり、Vault approved review による実ファイル適用、scheduler 起点 E2E、正式 apply E2E の代替ではない。
- 2026-05-20 05:05 UTC に `pkg/rencrowclient.CreateWorkstreamArtifact` / `CreateWorkstreamVaultUpdate` / `ReviewWorkstreamVaultUpdate` は direct response の artifact / vault_update `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の direct response は Workstream Artifact / Vault Update 証跡として扱わない。
- 2026-05-20 06:50 UTC に Workstream 保存前 domain validation は workstream / goal / artifact / annotation / steering / heartbeat / vault update の `created_at` 欠落と completed goal の `completed_at` 欠落を拒否する test を追加した。timestamp 欠落の ledger item は保存時点で拒否し、後続 current view を malformed にしない。
- 2026-05-19 21:16 UTC に Workstream Vault Review の Viewer 表示へ `applied` / `applied_path` と集計を追加し、live `/viewer/workstreams?limit=3` の `vault_updates=[]` 状態でも `workstream vault review:` / `0 applied` / `blocked: no vault apply` を表示することを browser E2E で確認した。これは pending_review artifact / waiting goal と Vault apply 済みを混同しないための blocked 表示であり、Vault approved review による実ファイル適用の完了条件にはしない。
- 2026-05-20 00:27 UTC に `TestE2E_WorkstreamVaultUpdatePreviewAndApprovedApply` で live `/viewer/workstreams/vault-updates` create、`/preview`、`/review` を実行し、`update_id=vault_apply_20260520002739.458203088` が `applied=true`、`applied_path=/home/nyukimi/picoclaw_multiLLM/vault/workstreams/live-e2e/ws_vault_apply_20260520002739.458203088/STATUS.md` として status current view に残ることを確認した。`TestE2E_Phase25BrowserViewerSessionContract` でも Workstreams card の `vault applied:` と Vault Review の applied count を確認済み。これは vault_root 配下の隔離 live-e2e file apply 成功であり、外部送信、PR 作成、scheduler 正常完了ではない。
- 2026-05-20 10:00 UTC に Viewer Ops は `/viewer/workstreams` fetch failure 時、stale な Vault update row を表示せず `Workstream vault reviews unavailable: ...` / `blocked: vault apply state unreadable` を表示する Node contract test を追加した。これは status API failure を Vault apply 証跡として扱わないための local 表示境界であり、live Vault apply / 正式 apply E2E の完了条件にはしない。
- `pkg/rencrowclient.BrowserTraceAPIStatus` / `DiscoverBrowserTraceAPI` / `ValidateBrowserTraceAPICandidate` / `CreateBrowserTraceAPIFetcherProposal` は Browser Trace API discovery / validation review / fetcher proposal の malformed response を E2E 証跡として成功扱いしないことを確認済み。

扱い:

- 現行 worktree 差分の local regression として扱う。
- skip / fallback / health ok を成功扱いするものではない。
- 実 API E2E、実ブラウザ音声、正式 promotion apply、実外部送信、実 PR 作成の完了扱いにはしない。403 / 503 / 外部 API fail を証跡として保持するための client 境界確認にとどめる。

### 4.0.1 2026-05-19 live dependency verification result

実行済み:

```bash
curl -sS -i http://127.0.0.1:18790/health
curl -fsS http://127.0.0.1:18790/viewer/runtime-config
curl -sS -i http://127.0.0.1:18790/viewer/superagent?limit=1
curl -sS -i http://127.0.0.1:18790/viewer/ai-workflow?limit=1
curl -sS -i http://127.0.0.1:18790/viewer/revenue?limit=1
curl -sS -i http://127.0.0.1:18790/viewer/complexity-hotspots?limit=1
PICOCLAW_LIVE_E2E=1 PICOCLAW_LIVE_BASE_URL=http://127.0.0.1:18790 \
GOCACHE=/tmp/picoclaw-gocache go test -count=1 -tags=e2e ./test/e2e \
  -run 'TestE2E_Phase25Live(RuntimeHealth|ViewerRuntimeConfigClient)' -v
set -a; . "$HOME/.picoclaw/.env"; set +a
PICOCLAW_CONFIG=$HOME/.picoclaw/config.yaml \
GOCACHE=/tmp/picoclaw-gocache go test -count=1 -tags=e2e ./test/e2e \
  -run 'TestE2E_OllamaProvider|TestE2E_MioAgent_Chat_RealOllama|TestE2E_Routing_Chat_NaturalLanguage' -v
```

判定:

- live service は起動中だが、`/health` は 503。local OpenAI-compatible Chat / Worker endpoint が `no route to host`。
- `/viewer/runtime-config` は取得できるが、health fail のため live service health E2E は fail とする。runtime-config は `TestE2E_Phase25LiveViewerRuntimeConfigClient` で health と分離して readiness fields を確認する。
- 2026-05-19 08:26 UTC に live service を現ソースで build した binary へ更新した。更新後、`/viewer/superagent`、`/viewer/ai-workflow`、`/viewer/revenue`、`/viewer/complexity-hotspots` は 200。更新前の installed binary は該当 route を含まず 404 だったため、古い binary の結果は現行実装判定に使わない。
- Ollama は live config の `http://100.83.207.6:11434` で `/api/tags` / generate / MioAgent chat / natural language CHAT routing が pass。
- `PICOCLAW_CONFIG=$HOME/.picoclaw/config.yaml` を使う場合、Ollama と無関係な外部 provider key validation も通る必要がある。最終確認では `~/.picoclaw/.env` を読み込んで validation を通し、実 key 値は記録していない。

扱い:

- live `/health` の 503 は fail として扱い、local OpenAI-compatible endpoint 復旧後に再確認する。
- 新仕様 Viewer API route は pass として扱う。ただし route 200 だけで SuperAgent run queue scheduler、AI Workflow command flow、Revenue 外部送信、Complexity scan の user flow 成立とは扱わない。
- Ollama E2E は pass として扱う。
- repo default config で stale Ollama endpoint を参照して skip した結果は、live runtime の成功を上書きしない。ただし skip は成功扱いにしない。

#### 4.0.1.1 2026-05-19 15:30 UTC runtime dependency readiness recheck

実行済み:

```bash
set -a; [ -f "$HOME/.picoclaw/.env" ] && . "$HOME/.picoclaw/.env"; set +a
for k in SLACK_BOT_TOKEN SLACK_SIGNING_SECRET DISCORD_BOT_TOKEN TELEGRAM_BOT_TOKEN TELEGRAM_WEBHOOK_SECRET \
  GOOGLE_API_KEY_CHAT GOOGLE_SEARCH_ENGINE_ID_CHAT GOOGLE_API_KEY_WORKER GOOGLE_SEARCH_ENGINE_ID_WORKER \
  STT_GATEWAY_URL RENCROW_STT_URL STT_PROVIDER_URL TTS_PROVIDER_URL TTS_PROVIDER IRODORI_BASE_URL SBV2_BASE_URL; do
  if [ -n "${!k:-}" ]; then printf '%s=present\n' "$k"; else printf '%s=missing\n' "$k"; fi
done
curl -sS -m 5 http://127.0.0.1:18790/viewer/runtime-config
curl -sS -m 5 http://127.0.0.1:18790/health
curl -sS -i -m 5 http://192.168.1.33:8766/health || true
curl -sS -i -m 5 http://192.168.1.33:8766/ || true
set -a; [ -f "$HOME/.picoclaw/.env" ] && . "$HOME/.picoclaw/.env"; set +a
PICOCLAW_CONFIG=$HOME/.picoclaw/config.yaml \
GOCACHE=/tmp/picoclaw-gocache go test -tags=e2e ./test/e2e \
  -run 'TestE2E_GoogleSearch_(Chat|Worker)' -count=1 -v
```

判定:

- Google Search Chat / Worker は `.env` 読み込み込みで pass。
- Slack / Discord / Telegram の実 token / webhook secret は missing。file payload 実 API E2E は blocked。
- `STT_GATEWAY_URL` は present だが、runtime-config の `stt_base_url=http://192.168.1.33:8766` は `/health` / `/` が 5 秒 timeout。browser microphone STT は実ブラウザ / 実 mic 未確認のため blocked。
- TTS provider env は missing。TTS playback / lip sync は実ブラウザ audio event 未確認のため blocked。
- `/health` は local Chat / Worker endpoint `http://192.168.1.13:8081` / `:8082` への `no route to host` で down。scheduler 正常完了 E2E の blocked 理由として維持する。
- 2026-05-19 15:39 UTC に `/health`、`/viewer/runtime-config`、`/viewer/superagent?limit=5`、`/viewer/llm-ops/status` を再確認した。`/health` は同 endpoint への `no route to host` で 503、SuperAgent status は 200 だが `runtime_config.run_queue_scheduler_enabled=false`、LLM Ops は `upstream unreachable`。scheduler 正常完了 E2E は引き続き blocked とする。
- 2026-05-19 15:55 UTC に同じ readiness を再確認した。`/health` は同 endpoint への `no route to host` で 503、`/viewer/runtime-config` は同 endpoint と `stt_base_url=http://192.168.1.33:8766` を返し、`/viewer/superagent?limit=5` は 200 だが `runtime_config.run_queue_scheduler_enabled=false`、`/viewer/llm-ops/status` は `upstream unreachable`。Slack / Discord / Telegram の実 token / webhook secret は missing、TTS provider env も missing。STT は `STT_GATEWAY_URL` present だが `http://192.168.1.33:8766/health` が `no route to host`。SuperAgent / AI Workflow scheduler 正常完了、file payload 実 API、browser microphone STT、TTS playback、lip sync は引き続き blocked とする。
- 2026-05-19 に `/viewer/runtime-config` の response へ `tts_base_url` / `tts_health_path` と、secret 値なしの `runtime_readiness` を追加した。`runtime_readiness` は Slack / Discord / Telegram credential pair、STT gateway env、TTS provider env の present / missing を boolean で返す。これは readiness 記録用の補助情報であり、実 API / 実ブラウザ E2E の成功条件にはしない。
- 2026-05-19 に `pkg/rencrowclient.RuntimeConfig` を追加し、runtime-config API の 2xx だけでは readiness view 成立扱いしない client boundary を追加した。LLM Ops enabled / configured / base URL、local LLM provider / Chat / Worker endpoint、STT / TTS URL、TTS health path、`runtime_readiness` fields が壊れた response は local client test で拒否する。Phase25 live E2E は `TestE2E_Phase25LiveRuntimeHealth` と `TestE2E_Phase25LiveViewerRuntimeConfigClient` に分離し、health fail が runtime-config readiness 証跡を隠さないようにした。
- 2026-05-19 19:02 UTC に現 binary へ再 install / restart 後、`/viewer/llm-ops/status` が 502 `upstream unreachable` を返すこと、Viewer Ops readiness が LLM Ops `configured:present` / `proxy:present` / `live:missing` / `blocked: HTTP 502: upstream unreachable` を visible state として表示することを `TestE2E_Phase25BrowserViewerSessionContract` で確認した。これは LLM Ops proxy 設定済みを管理 API 到達成功と誤認しないための表示境界であり、Chat / Worker local LLM endpoint 復旧や scheduler 正常完了 E2E の完了条件にはしない。
- 2026-05-19 20:51 UTC に `/health` の live 503 を Viewer Ops の Runtime Health card へ表示した。local Chat / Worker endpoint は `connection refused`、20:54 UTC の再起動後は 5 秒 timeout のままで、browser E2E は `service:missing` / `chat:missing` / `worker:missing` / `blocked:` を visible state として確認した。health fail を runtime-config / LLM Ops proxy 証跡と混同しない。
- 2026-05-19 16:41 UTC に現 worktree を `make install` して `picoclaw.service` を再起動し、`TestE2E_Phase25LiveViewerRuntimeConfigClient` が live service に対して pass することを確認した。`/viewer/runtime-config` は `tts_base_url=http://192.168.1.13:7870` と `runtime_readiness` を返す。Slack / Discord / Telegram credential は false、STT gateway env は true、TTS provider env は false。`TestE2E_Phase25LiveRuntimeHealth` は `/health status=503, want 200` で fail のままなので、runtime-config readiness pass を health 成功や外部 API / 実ブラウザ音声 E2E 成功扱いにしない。
- 2026-05-19 16:46 UTC に `runtime_readiness` へ `stt_gateway_config_present` / `tts_provider_config_present` を追加した live binary へ更新した。`/viewer/runtime-config` は `stt_gateway_env_present=true` / `stt_gateway_config_present=true` / `tts_provider_env_present=false` / `tts_provider_config_present=true` を返し、`TestE2E_Phase25LiveViewerRuntimeConfigClient` は pass。env presence と effective config presence を分けて記録するが、実 provider health、実 browser playback、lip sync は未確認のまま blocked とする。
- 2026-05-19 に `pkg/rencrowclient.RuntimeConfig` の validation を補強し、STT / TTS endpoint があるのに config-present field が false、または endpoint がないのに config-present field が true の response を E2E 証跡として受け取らない local client test を追加した。補強後も live `TestE2E_Phase25LiveViewerRuntimeConfigClient` は pass。
- 2026-05-19 16:54 UTC に Viewer Ops へ runtime readiness card 表示を追加した。`viewer_memory_panel.test.mjs` は `runtimeReadinessCards` と STT / TTS / channel readiness badge 表示を確認して pass。live `/viewer` と配信中 `ops.js` にも該当 element / renderer が含まれることを curl で確認した。これは runtime readiness の表示確認であり、実ブラウザ操作 / 実 mic / 実 audio playback の成功条件ではない。
- 2026-05-19 に `TestE2E_Phase25BrowserViewerSessionContract` へ Ops `runtimeReadinessCards` の visible state と readiness text 確認を追加し、`PICOCLAW_BROWSER_E2E=1 PICOCLAW_LIVE_BASE_URL=http://127.0.0.1:18790` で pass した。これは Viewer runtime readiness 表示の browser E2E であり、STT mic input、TTS playback、lip sync の audio flow 成功とは別項目として扱う。
- 2026-05-19 17:54 UTC に `/viewer/debug/system` の audio health probe を並列化し、STT / TTS endpoint 不達でも route が長時間固まらず blocked 証跡を返すようにした。live `time curl -sS -m 4 http://127.0.0.1:18790/viewer/debug/system` は約 1.2 秒で返り、`stt_ok=false`、`tts_live_ok=false`、`tts_ready_ok=false`、timeout `last_error` を返した。
- `pkg/rencrowclient.DebugSystemSnapshot` を追加し、debug system response の `updated_at`、audio URL、ok state の整合性を local client test で検証するようにした。`TestE2E_Phase25LiveDebugSystemSnapshotClient` は live service に対して pass。これは STT / TTS blocked 証跡を読むための境界確認であり、実 mic STT、TTS playback、lip sync 成功扱いにはしない。
- `pkg/rencrowclient.DebugSystemSnapshot` は `updated_at` の RFC3339 形式、endpoint 設定済み down 状態の health body または `last_error`、`tts_ready_ok` と `tts_live_ok` の整合性も検証する。空の down 状態や ready-only 状態を STT / TTS readiness 証跡として成功扱いしない。
- 2026-05-20 13:35 UTC に Viewer Debug panel の `/viewer/debug/system` fetch failure 境界を補強し、non-2xx の場合は generic `fetch failed` ではなく body 付き `HTTP <status>: ...` を GPU / Audio summary に表示する Node contract test を追加した。これは debug system route failure を STT / TTS readiness、GPU readiness、実 mic STT、browser audio playback / lip sync 成功として扱わないための local 表示境界であり、live browser audio / STT E2E の代替ではない。
- 2026-05-20 14:05 UTC に Viewer STT artifact persistence の `/viewer/stt/log`、`/viewer/stt/wav`、`/viewer/stt/autotest` failure 境界を補強し、non-2xx の場合は generic `stt ... failed` ではなく body 付き `HTTP <status>: ...` を error として保持する Node contract test を追加した。これは STT log / WAV / autotest 保存失敗を実 mic STT 成功や STT provider readiness 成功として扱わないための local 保存境界であり、live browser audio / STT E2E の代替ではない。
- 2026-05-19 17:00 UTC に現 binary の live service で `TestE2E_AIWorkflowExternalControlClientRequiresApproval`、`TestE2E_RevenueExternalSendClientRequiresApprovalAndAuditsBlockedApply`、`TestE2E_SkillGovernanceExternalPRClientRequiresApprovalAndAuditsBlockedSubmit`、`TestE2E_SuperAgentRunQueueClientManualLedgerFlow`、`TestE2E_SuperAgentPauseResumeAndQueueReentryClientFlow` を再実行し pass。これらは Human approval 境界、blocked audit、手動台帳、pause-resume queue reentry の再確認であり、実外部送信、実 PR 作成、scheduler 正常完了、真の長時間再開の完了条件にはしない。

### 4.0.2 2026-05-19 external API / Google Search verification result

実行済み:

```bash
set -a; . "$HOME/.picoclaw/.env"; set +a
PICOCLAW_CONFIG=$HOME/.picoclaw/config.yaml \
GOCACHE=/tmp/picoclaw-gocache go test -count=1 -tags=e2e ./test/e2e \
  -run 'TestE2E_GoogleSearch|TestE2E_APIProvider_Generate' -v
```

判定:

- Claude provider generate: pass。
- DeepSeek provider generate: pass。
- OpenAI provider generate: fail。API response は `429 insufficient_quota`。
- Google Search Chat: pass。
- Google Search Worker: pass。

扱い:

- Claude / DeepSeek / Google Search Chat / Google Search Worker は pass として扱う。
- OpenAI は quota / billing 側の外部要因で fail として扱い、成功扱いしない。
- API key 値は証跡に残さない。

### 4.0.3 2026-05-19 AI Workflow live command / policy verification result

実行済み:

```bash
curl -sS -i http://127.0.0.1:18790/viewer/ai-workflow/commands/run \
  -H 'Content-Type: application/json' \
  -d '{"command_name":"/tool-harness-check","text":"live E2E: verify registered command invocation is persisted without external apply","agent":"Worker","workstream_id":"ws_live_20260519083353"}'
curl -sS -i http://127.0.0.1:18790/viewer/ai-workflow/context-budget/check \
  -H 'Content-Type: application/json' \
  -d '{"event_id":"ctx_live_20260519083353","agent":"Worker","input_tokens":128,"output_tokens":64,"context_tokens":192,"created_at":"2026-05-19T08:34:05Z"}'
curl -sS -i http://127.0.0.1:18790/viewer/ai-workflow/external-control/check \
  -H 'Content-Type: application/json' \
  -d '{"actor":"Worker","channel_id":"viewer","action":"promotion_apply","human_approved":false}'
curl -sS http://127.0.0.1:18790/viewer/ai-workflow?limit=20 | \
  rg 'external_control_policy_checked|needs_approval|promotion_apply|command_invoked:tool-harness-check:20260519083402|ctx_live_20260519083353'
curl -sS http://127.0.0.1:18790/viewer/ai-workflow?limit=1
curl -sS -i http://127.0.0.1:18790/viewer/ai-workflow/context-budget/check \
  -H 'Content-Type: application/json' \
  -d '{"event_id":"ctx_budget_warn_live_20260519093600","agent":"Worker","input_tokens":600,"context_tokens":600,"created_at":"2026-05-19T09:36:00Z"}'
curl -sS -i http://127.0.0.1:18790/viewer/ai-workflow/context-budget/check \
  -H 'Content-Type: application/json' \
  -d '{"event_id":"ctx_budget_stop_live_20260519093600","agent":"Worker","input_tokens":950,"context_tokens":950,"created_at":"2026-05-19T09:36:01Z"}'
curl -sS http://127.0.0.1:18790/viewer/ai-workflow?limit=20 | \
  rg 'ctx_budget_warn_live_20260519093600|ctx_budget_stop_live_20260519093600|context_budget_warning|context_budget_exceeded'
GOCACHE=/tmp/picoclaw-gocache PICOCLAW_LIVE_E2E=1 go test -tags=e2e ./test/e2e \
  -run 'TestE2E_AIWorkflow(CommandContextAndSuperAgentTraceSameRun|ExternalControlClientRequiresApproval)' -count=1 -v
GOCACHE=/tmp/picoclaw-gocache PICOCLAW_LIVE_E2E=1 PICOCLAW_LIVE_SANDBOX_E2E=1 go test -tags=e2e ./test/e2e \
  -run TestE2E_AIWorkflowPromotionWorkflowRequiresHumanApprovalBeforeApply -count=1 -v
```

判定:

- `/viewer/ai-workflow/commands/run` は 201。registered command `/tool-harness-check` の invocation event が保存された。
- `/viewer/ai-workflow/context-budget/check` は 201。context usage は保存されたが、live config の `context_budget_tokens` が未設定のため decision は `context budget disabled`。
- `/viewer/ai-workflow/external-control/check` は 200。`promotion_apply` は Human approval なしでは `needs_approval`。
- `/viewer/ai-workflow?limit=20` で command event、context usage、external control policy event を確認した。
- `TestE2E_AIWorkflowCommandContextAndSuperAgentTraceSameRun` は `pkg/rencrowclient.AIWorkflowStatus` / `RunCommand` / `CheckContextBudget` / `SuperAgentStatus` を使い、command event、context usage、SuperAgent run / trace が同じ `run_id` と `workstream_id` で live status view から追えることを確認した。
- `pkg/rencrowclient.AIWorkflowStatus` は status API の current view に workflow event / project memory / worktree / command / context usage の同一 ID 重複、必須 field 欠落、context usage の負 token / count がある場合、E2E 証跡として成功扱いしないことを確認済み。JSONL AI Workflow registry list は command / project memory / worktree を最新 state per key の current view として返す。
- `pkg/rencrowclient.RunCommand` は commands API が 2xx を返しても、command registry、`command_invoked` event、requested status、run_id / workstream_id が一致しない malformed response を direct client success として返さない。
- `pkg/rencrowclient.CheckContextBudget` は context-budget API が 2xx を返しても、context usage、decision status、warning / stop event linkage が一致しない malformed response を direct client success として返さない。
- `pkg/rencrowclient.CheckExternalControl` は external-control API が 2xx を返しても、request echo、`allowed` / `needs_approval` / `blocked` status、approval-required 整合性、blocked reason が一致しない malformed response を direct client success として返さない。
- `pkg/rencrowclient.EvaluateHeavyWorker` / `HeavyWorkerRuntimeDiagnostics` は Heavy Worker evaluate / runtime diagnostics API が 2xx を返しても、request echo、decision status、requested event、parent event、agent、runtime role / route、failure semantics、LLM Ops unavailable error が一致しない malformed response を direct client success として返さない。
- 2026-05-19 22:25 UTC に `LLMOpsIdleChatGate` の status probe timeout を read-only route と同じ短時間設定へ揃えた。`TestLLMOpsIdleChatGate_StatusTimesOutQuickly` と `TestHandleAIWorkflowHeavyWorkerRuntimeDiagnostics` は pass し、live `/viewer/ai-workflow/heavy-worker/runtime-diagnostics` も約 3 秒で LLM Ops 不達の blocked 証跡を返す。これは Heavy Worker 実 provider E2E や scheduler 正常完了 E2E の代替ではない。
- `pkg/rencrowclient.RunCommand` / `CheckContextBudget` / `CheckExternalControl` / `CreateWorkstreamArtifact` / `ReviewWorkstreamVaultUpdate` / `PauseRun` / `ResumeRun` は、必須 ID / command / agent / action / artifact metadata 欠落 request を送信前に拒否する。これは live scheduler E2E や正式 apply E2E の代替ではない。
- `pkg/rencrowclient.CreateWorkstreamArtifact` は artifacts API が 2xx を返しても、artifact_id / workstream_id / artifact_type / status が一致しない malformed response を direct client success として返さない。
- `pkg/rencrowclient.CreateWorkstreamVaultUpdate` / `PreviewWorkstreamVaultUpdate` / `ReviewWorkstreamVaultUpdate` は vault create / preview / review API が 2xx を返しても、update_id / workstream_id / file_path / review_status / preview diff / applied flag と applied_path が一致しない malformed response を direct client success として返さない。
- `pkg/rencrowclient.CreateWorkstreamArtifact` / `CreateWorkstreamVaultUpdate` / `ReviewWorkstreamVaultUpdate` は direct response の artifact / vault_update に `created_at` がない場合も direct client success として返さない。
- `pkg/rencrowclient.WorkstreamStatus` は `/viewer/workstreams` が 2xx を返しても、workstream / goal / artifact / annotation / steering / heartbeat / vault update の同一 ID 重複や必須 ID 欠落を current view として成功扱いしない。
- `pkg/rencrowclient` の `SubmitPromotionWorkflow` は apply intent で `ExternalControl` を必須とし、未指定の場合は promotion request 作成前に停止することを test で確認した。
- `TestE2E_AIWorkflowPromotionWorkflowRequiresHumanApprovalBeforeApply` は、一時 sandbox config 有効化中に `pkg/rencrowclient.SubmitPromotionWorkflow` で external control policy allow、Promotion Gate approve、explicit apply intent、post-apply verification path が揃っていても、最終 apply `HumanApproved=false` では apply へ進まず、promotion request / approve gate log / rollback artifact / post-apply verification artifact だけが保存されることを確認した。
- `pkg/rencrowclient.CreatePromotionRequest` は Promotion Request API 2xx だけでは成功扱いせず、promotion_id / sandbox_id / target_path / diff_path、gate event_id、decision status と gate status、review 時の missing requirements、artifact type / path を検証する。
- `pkg/rencrowclient.CreatePromotionRequest` は `promotion_id` / `sandbox_id` / `target_path` のない request を送信前に拒否する。diff / test / rollback / human approval の不足は Promotion Gate 側で `needs_review` / `needs_more_tests` として扱う。
- `SubmitPromotionWorkflow` は apply API が 2xx を返しても、`decision.status` / `gate_log.gate_status` が `promotion_applied`、Gate Log の `promotion_id` / `post_apply_verification`、completed `post_apply_verification_artifact` の `sandbox_id` / 証跡 path、`diff_apply_result.status=applied` と `applied_files` が request と一致しない場合は `Applied=true` にしないことを local client test で確認した。
- `pkg/rencrowclient.ApplyPromotion` も同じ apply response 検証を行い、malformed response を direct apply success として返さない。workflow 経由では同 response を `SkippedReason` として保持し、`Applied=true` にはしない。
- `pkg/rencrowclient.RollbackPromotion` も rollback response 検証を行い、`rollback_executed` / `rolled_back` / completed `rollback_execution` artifact / rollback plan path 一致がない malformed response を direct rollback success として返さない。
- 低レベル `pkg/rencrowclient.ApplyPromotion` / `RollbackPromotion` は Human approval 未承認、apply diff / post-apply verification path 不足、rollback plan 不足の request を送信前に拒否する。これは正式 apply / rollback E2E の代替ではない。
- `pkg/rencrowclient.CreatePromotionRequest` / `ApplyPromotion` / `RollbackPromotion` は direct response の timestamp 欠落も direct client success として返さない。
- local handler test で `post_apply_verification_command` 指定時に verifier / ToolRunner が未接続なら 503 で停止し、`promotion_applied` log / completed artifact を保存しないことを確認した。
- 証跡取得後、一時 sandbox config は復元し、live service 再起動後に `/viewer/sandbox` が 404、`/viewer/ai-workflow` が 200 に戻ることを確認した。
- 2026-05-19 09:05 UTC に `/viewer/ai-workflow` status API の `context_budget_policy` で、live service の実効値が `max_context_tokens=0`、`warn_at_ratio=0.8`、`stop_at_ratio=0.95` であることを確認した。
- 2026-05-19 09:35 UTC に一時 config で `context_budget_tokens=1000`、`context_budget_warn_ratio=0.5`、`context_budget_stop_ratio=0.9` を有効化し、status API の `context_budget_policy` で確認した。
- `ctx_budget_warn_live_20260519093600` は `status=warn`、`event_type=context_budget_warning` として保存された。
- `ctx_budget_stop_live_20260519093600` は `status=stop`、`event_type=context_budget_exceeded` として保存された。
- 証跡取得後、一時 config は削除し、live service 再起動後の `context_budget_policy` は `max_context_tokens=0`、`warn_at_ratio=0.8`、`stop_at_ratio=0.95` に戻した。

扱い:

- live command / context / policy event 保存は部分 pass とする。
- context budget warning / stop は handler live flow として部分 pass。一時 config 有効中に primary LLM provider middleware の runtime warning / stop event 保存も部分 pass。2026-05-19 09:49 UTC に `buildToolRuntime` の実 ToolRunner wrapper が JSONL AI Workflow store へ warning / stop usage と event を保存し、stop 時に tool result を offload することを runtime integration test で確認した。09:52 UTC には live service の DCI search route から Worker ToolRunner `file_read` を通し、`ctx_tool_*` / `evt_tool_context_budget_*` warning / stop が `/viewer/ai-workflow` に保存されることを確認した。証跡取得後、一時 config は削除し、live service 再起動後の policy は disabled に戻した。
- 2026-05-19 15:14 UTC に `/viewer/ai-workflow?limit=5` が 200 で command registry / workflow event / context usage / `context_budget_policy` を返すことを再確認した。ただし `/health` は local Chat / Worker endpoint `no route to host`、`/viewer/superagent?limit=5` は `runtime_config.run_queue_scheduler_enabled=false` のため、scheduler 起点で SuperAgent run queue trace と同一 run として追う E2E は `blocked` として維持する。command / context status 200 や過去 event の存在は scheduler 起点 E2E 成功扱いにしない。
- 2026-05-19 16:25 UTC に `/health` が 503、local Chat / Worker endpoint `http://192.168.1.13:8081` / `:8082` が `no route to host`、`/viewer/superagent?limit=3` が `runtime_config.run_queue_scheduler_enabled=false`、`/viewer/ai-workflow?limit=3` が 200、当時の `/viewer/sandbox?limit=1` が通常 config 復元後の 404 であることを再確認した。2026-05-19 20:23 UTC の runtime 更新後は Sandbox disabled 時も `/viewer/sandbox?limit=1` が 503 `sandbox store unavailable` を返す。scheduler 正常完了 E2E / AI Native scheduler 起点同一 run E2E は blocked 継続とする。
- 2026-05-19 18:43 UTC に現 binary へ `make install` / service restart 後、`/viewer/ai-workflow?limit=1` は `context_budget_policy.max_context_tokens=0` を返した。`TestE2E_Phase25BrowserViewerSessionContract` で AI Workflow Ops card の `context-budget:disabled` / `blocked: context budget disabled` を確認し、`TestE2E_AIWorkflowExternalControlClientRequiresApproval` も再 pass。`/health` は 503 のままであり、scheduler 起点 E2E / warning-stop 成功 / 正式 apply E2E の代替ではない。
- 2026-05-19 に `pkg/rencrowclient.ToolHarnessStatus` を追加し、`/viewer/tool-harness/recent` の 2xx だけでは mediation event view を成功扱いせず、event_id 重複、必須 field 欠落、未知の `validation_status`、repair / relation default の識別子欠落を local client test で拒否するようにした。これは Tool Harness event を E2E 証跡として読むための境界確認であり、provider 固有 tool protocol recovery や実運用 E2E の代替ではない。
- 2026-05-20 01:32 UTC に `pkg/rencrowclient.ToolHarnessStatus` の status / evidence 整合性を補強し、`valid` event に repair evidence がある場合と、`repaired` event に repair / relation default evidence がない場合を malformed current view として拒否する local client test を追加した。
- 2026-05-20 02:40 UTC に `pkg/rencrowclient.ToolHarnessStatus` は mediation event の `created_at` 欠落を local client error として拒否する test を追加した。これは timestamp のない Tool Harness event を実行証跡として扱わないための境界確認であり、provider 固有 tool protocol recovery の代替ではない。
- 2026-05-20 08:10 UTC に `internal/domain/toolharness` と JSONL recorder の保存前 validation は mediation event の `event_id` / `tool_name` / `raw_input_hash` / `validation_status` / `created_at` 欠落、未知 status、`valid` と repair evidence の矛盾、`repaired` の evidence 欠落を保存前に拒否する test を追加した。malformed Tool Harness event を保存時点で拒否する local domain 境界確認であり、provider 固有 tool protocol recovery の代替ではない。
- 2026-05-19 に `pkg/rencrowclient.DCIRecent` / `DCISearch` を追加し、`/viewer/dci/recent` / `/viewer/dci/search` の 2xx だけでは DCI trace / evidence 成立扱いせず、trace 重複、必須 field 欠落、step 重複、request query / Evidence Pack / Trace event_id / evidence count / evidence path-line-snippet の不整合を local client test で拒否するようにした。terminal trace (`completed` / `failed`) が `ended_at` を持たない場合も E2E 証跡として成功扱いしない。これは DCI evidence / trace を E2E 証跡として読むための境界確認であり、実機 VectorDB / Qdrant E2E や大規模 corpus tuning の代替ではない。
- `pkg/rencrowclient.DCIRecent` / `DCISearch` は trace status を `completed` / `failed`、step status を `ok` / `error` / `stopped` / `completed` に限定し、`failed` trace と `error` step に `error_message` がない場合も E2E 証跡として成功扱いしないことを確認済み。
- 2026-05-20 02:47 UTC に `pkg/rencrowclient.DCIRecent` / `DCISearch` は trace `started_at` 欠落を local client error として拒否する test を追加した。これは開始時刻のない DCI trace を read-only search / evidence 実行証跡として扱わないための境界確認であり、実機 VectorDB / Qdrant E2E や ranking tuning の代替ではない。
- 2026-05-20 04:25 UTC に `pkg/rencrowclient.DCIRecent` / `DCISearch` は trace `actor` / `mode` 欠落を local client error として拒否する test を追加した。実行主体と探索 mode が不明な DCI trace は read-only search / evidence 実行証跡として扱わない。
- 2026-05-20 05:55 UTC に `pkg/rencrowclient.DCIRecent` / `DCISearch` は search step の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の step は read-only search / evidence 実行証跡として扱わない。
- 2026-05-20 08:00 UTC に `internal/domain/dci` と JSONL / SQLite store の保存前 validation は search trace の `started_at` / terminal `ended_at` 欠落、step の `created_at` 欠落、未知 status、証跡なし failed / error、重複 step を保存前に拒否する test を追加した。malformed DCI trace を保存時点で拒否する local domain 境界確認であり、実機 VectorDB / Qdrant E2E や ranking tuning の代替ではない。
- 2026-05-20 01:25 UTC に `pkg/rencrowclient.DCIRecent` / `DCISearch` の数値証跡境界を補強し、negative `result_count`、Evidence Pack `confidence` 範囲外、Evidence `confidence` 範囲外を malformed current view として拒否する local client test を追加した。
- 2026-05-19 17:12 UTC に live service で `TestE2E_ToolHarnessAndDCIStatusClientCurrentView` を実行し、`ToolHarnessStatus` が valid mediation event current view を読めること、`DCISearch` が completed trace と Evidence Pack を返し、`DCIRecent` が同じ trace を current view として返すことを確認した。これは read-only DCI search と証跡読み取り E2E であり、provider 固有 tool protocol recovery、実機 VectorDB / Qdrant、大規模 corpus tuning の完了条件にはしない。
- 2026-05-19 22:54 UTC に Viewer Ops の Tool Harness / DCI Trace card へ `provider protocol recovery not verified` / `VectorDB/Qdrant E2E not verified` を追加した。valid mediation event や read-only DCI evidence は provider 固有 tool protocol recovery、実機 VectorDB / Qdrant、大規模 corpus tuning 成功の代替ではない。
- 2026-05-20 11:05 UTC に Viewer Ops の Tool Harness / DCI fetch failure 境界を補強し、`/viewer/tool-harness/recent` または `/viewer/dci/recent` が non-2xx の場合は stale mediation event / DCI trace ではなく `tool harness status unavailable: ...` / `dci trace status unavailable: ...` と blocked 理由を表示する Node contract test を追加した。これは status API failure を provider 固有 tool protocol recovery、read-only DCI evidence 成功、実機 VectorDB / Qdrant E2E 成功として誤読しないための visible-state 境界確認であり、provider recovery や実機 VectorDB / Qdrant E2E の代替ではない。
- 2026-05-20 12:45 UTC に Viewer Ops の DCI manual search fetch failure 境界を補強し、`/viewer/dci/search` が non-2xx の場合は generic `dci search failed` ではなく body 付き `HTTP <status>: ...` を DCI manual search result の `error:` 行へ表示し、stale completed search result を残さない Node contract test を追加した。これは read-only DCI search route failure を evidence pack 成功や実機 VectorDB / Qdrant E2E 成功として扱わないための local 表示境界であり、実機 VectorDB / Qdrant E2E の代替ではない。
- 2026-05-20 11:15 UTC に Viewer Evidence / Verification fetch failure 境界を補強し、`/viewer/evidence/recent`、`/viewer/evidence/summary`、`/viewer/verification/recent`、`/viewer/verification/summary` が non-2xx の場合は stale execution report / verification report / summary count ではなく `Evidence / verification unavailable: ...` / `evidence summary unavailable: ...` / `blocked: execution evidence state unreadable` を表示する Node contract test を追加した。これは status API failure を Worker 実行証跡、verification 成功、post-apply verification 成功として誤読しないための visible-state 境界確認であり、live Worker 実行 E2E や post-apply verification E2E の代替ではない。
- 2026-05-20 13:45 UTC に Viewer Evidence detail fetch failure 境界を補強し、`/viewer/evidence/detail?job_id=...` が non-2xx の場合は generic `evidence detail fetch failed` ではなく body 付き `HTTP <status>: ...` を Selected Evidence Detail に表示する Node contract test を追加した。これは detail route failure を Worker 実行証跡詳細取得成功や post-run verification 成功として扱わないための local 表示境界であり、live Worker 実行 E2E や post-apply verification E2E の代替ではない。
- 2026-05-19 に `pkg/rencrowclient.KnowledgeMemoryStatus` / `ReviewKnowledgeMemory` を追加し、`/viewer/knowledge-memory` / `/viewer/knowledge-memory/review` の 2xx だけでは Knowledge Memory 台帳 / review 成立扱いせず、Personal Archive 原本の unprotected、同一 ID 重複、必須 field 欠落、Dream Run の auto-approved、review request と response の `detail_type` / `id` / `review_status` / promote intent / target status / formal target 不整合を local client test で拒否するようにした。これは Knowledge Memory 台帳と review/promote comparison を E2E 証跡として読むための境界確認であり、正式 memory / Source Registry への自動 promote 成功の代替ではない。
- 2026-05-20 02:08 UTC に `pkg/rencrowclient.SourceRegistryStaging` の current view validation を補強し、terminal `validation_status` (`validated` / `rejected`) は `meta.validated_at` を必須、`rejected` は `meta.validation_issues` も必須にした。これは Source Registry staging の status 文字列だけを review / validate 成立証跡として受け取らないための local client 境界確認である。
- 2026-05-20 02:11 UTC に `pkg/rencrowclient.SourceRegistryStatus` の current view validation を補強し、`last_status` がある source entry は `last_fetched_at` を必須にし、`last_error` 単独も拒否する local client test を追加した。これは Source Registry fetch status 文字列だけを外部 fetch 成功 / 失敗証跡として受け取らないための境界確認である。
- 2026-05-20 03:35 UTC に `pkg/rencrowclient.SourceRegistryStaging` の current view validation を補強し、staging item の `created_at` / `updated_at` 欠落または RFC3339 不正を local client error として拒否する test を追加した。これは timestamp 欠落 / 不正の staging item を Source Registry staging review / validate / promote 証跡として受け取らないための境界確認である。
- 2026-05-20 08:20 UTC に `/viewer/source-registry` の source entry response へ `created_at` / `updated_at` を追加し、`pkg/rencrowclient.SourceRegistryStatus` が欠落または RFC3339 不正を local client error として拒否する test を追加した。timestamp 欠落 / 不正の source entry は Source Registry 台帳、外部 fetch、staging review の証跡として扱わない。
- 2026-05-20 08:30 UTC に `pkg/rencrowclient.MemoryLayers` を追加し、`/viewer/memory/layers` の L0 / L1 / L3 memory event、L2 summary、L3 Qdrant document の必須 field と timestamp を local client validation で確認するようにした。`TestE2E_SourceRegistryStagingValidatePromoteAndMemoryLayers` は Memory Layers を typed client 経由で読み、raw JSON map の存在だけを promoted memory 表示証跡にしない。
- 2026-05-20 08:40 UTC に Conversation L1 memory event の保存前 validation を補強し、`SaveMessage` の空 `session_id` / `thread_id<=0` / 空 speaker / 空 message と、保存・promote 直前の L1 memory event 必須 field / timestamp 欠落を拒否する local persistence test を追加した。Memory Layers client validation だけに頼らず、元の L1 台帳が malformed memory event を作らないことを確認する境界である。
- 2026-05-20 08:50 UTC に Conversation L1 memory event の読み出し validation を補強し、既存 DB row が malformed な場合も `RecentByNamespace` / `RecentByState` / `RecentBySession` scan 時に拒否する local persistence test を追加した。Memory Layers handler が壊れた既存 row を横断表示 current view として返さないための境界である。
- 2026-05-20 09:00 UTC に DuckDB L2 ThreadSummary の保存・読み出し validation を補強し、`SaveThreadSummary` と `GetSessionHistory` / `SearchByDomain` scan 時に `thread_id<=0` と空 `summary` を拒否する local persistence test を追加した。Memory Layers の L2 summary が malformed row を current view として返さないための境界である。
- 2026-05-20 09:10 UTC に VectorDB KB Document の保存・変換 validation を補強し、`SaveKB` は `id` / `domain` / `content` / `created_at` / `updated_at` / `embedding` 欠落を拒否し、Qdrant point 変換は `id` / `domain` / `content` / `created_at` / `updated_at` 欠落を拒否する local test を追加した。Memory Layers の L3 Qdrant document が malformed payload を current view として返さないための境界である。
- 2026-05-20 09:20 UTC に `/viewer/memory/layers` handler の snapshot validation を追加し、hot / cold store が malformed L0 / L1 / L3 memory、L2 summary、L3 Qdrant document を返した場合は 500 `invalid memory layers snapshot` で停止する handler test を追加した。Viewer / browser 直アクセスでも malformed Memory Layers current view を成功扱いしないための境界である。
- 2026-05-20 09:30 UTC に Viewer Memory tab の `refreshMemoryLayers` を補強し、`/viewer/memory/layers` の non-2xx body を table 上に `Memory Layers unavailable: ...` として表示する Node contract test を追加した。handler failure を stale row や空表示として成功扱いしないための Viewer visible-state 境界である。
- 2026-05-20 11:25 UTC に News Pack tab の `refreshNewsPack` を補強し、`/viewer/memory/snapshot` が non-2xx の場合は stale news / digest / recall usage を使わず `News Pack unavailable: ...` / `News digests unavailable: ...` / `News recall usage unavailable: ...` を表示する Node contract test を追加した。これは Memory snapshot API failure を News Pack / Daily Digest / Recall usage 成功として誤読しないための visible-state 境界確認であり、live Source Registry / News ingestion E2E の代替ではない。
- 2026-05-20 11:45 UTC に Memory tab の `refreshMemoryEvents` / `refreshRecallTraces` を補強し、`/viewer/memory/events` または `/viewer/recall/traces` が non-2xx の場合は stale L1 event / search cache / recall trace を使わず `Memory events unavailable: ...` / `Search cache unavailable: ...` / `Recall traces unavailable: ...` を表示する Node contract test を追加した。これは memory event / Recall Trace route failure を L1 event log、search cache、recall usage 成功として誤読しないための visible-state 境界確認であり、live conversation memory / recall E2E の代替ではない。
- 2026-05-20 11:55 UTC に Memory tab の `refreshMemorySnapshot` を補強し、`/viewer/memory/snapshot` が non-2xx の場合は stale memory / news / digest / knowledge count を使わず `Memory snapshot unavailable: ...` / `Memory snapshot news unavailable: ...` / `Memory snapshot digests unavailable: ...` を表示する Node contract test を追加した。これは Memory snapshot route failure を L1 memory、News Pack、Daily Digest、Knowledge count 成功として誤読しないための visible-state 境界確認であり、live conversation memory / News ingestion E2E の代替ではない。
- 2026-05-20 06:10 UTC に `pkg/rencrowclient.PromoteSourceRegistryStaging` の direct response validation を補強し、news / knowledge / memory promotion item の `created_at` 欠落を local client error として拒否する test を追加した。これは timestamp 欠落の promotion response を Source Registry promote 証跡として受け取らないための境界確認である。
- 2026-05-20 06:15 UTC に `pkg/rencrowclient.PromoteSourceRegistryStaging` の direct response validation を追加補強し、news / knowledge / memory promotion item の `ID` 欠落を local client error として拒否する test を追加した。これは ID 欠落の promotion response を Source Registry promote 成功証跡として受け取らないための境界確認である。
- 2026-05-20 11:35 UTC に Memory tab の Source Registry fetch failure 境界を補強し、`/viewer/source-registry` または `action=staging` が non-2xx の場合は stale source / staging row を使わず `Source Registry unavailable: ...` / `Source Registry staging unavailable: ...` / `staging unavailable: ...` を表示する Node contract test を追加した。これは Source Registry route failure を source 台帳、staging review、validate / promote 成功として誤読しないための visible-state 境界確認であり、live Source Registry E2E の代替ではない。
- `pkg/rencrowclient.KnowledgeMemoryStatus` は Creative / News status を `candidate` / `reviewed` / `promoted` / `rejected`、Daily Intake status を `candidate` / `reviewed` / `enabled` / `active` / `rejected`、Dream status を `draft` / `proposal` / `reviewed` / `promoted` / `rejected`、Dream review status を `pending` / `approved` / `rejected`、Temporal Marker layer を `thread` / `today` / `3days` / `week` / `month` / `year` / `long_term` に限定し、未知値を E2E 証跡として成功扱いしないことを確認済み。
- 2026-05-20 02:01 UTC に Dream Consolidation Run の status / review_status 整合性を保存前 validation と client current view validation で補強し、`pending` + `promoted`、`approved` + `rejected`、`rejected` + `reviewed` のような矛盾状態を Knowledge Memory 台帳証跡として受け取らない local test を追加した。
- 2026-05-20 01:22 UTC に `pkg/rencrowclient.KnowledgeMemoryStatus` は Temporal Marker の `access_count` が負数の current view を local client error として拒否する test を追加した。これは malformed temporal marker count を Knowledge Memory 台帳証跡として扱わないための境界確認であり、正式 memory / Source Registry 自動同期の代替ではない。
- 2026-05-20 03:25 UTC に `pkg/rencrowclient.KnowledgeMemoryStatus` は Personal Archive / Creative / News / Daily Intake / Temporal Marker / Dream Run の `created_at` 欠落を local client error として拒否する test を追加した。これは timestamp 欠落の current view を Knowledge Memory 台帳証跡として扱わないための境界確認であり、正式 memory / Source Registry 自動同期の代替ではない。
- 2026-05-20 06:20 UTC に Knowledge Memory create handler は Personal Archive / Creative / News / Daily Intake / Temporal Marker / Dream Run 作成 payload が `created_at` を省略した場合も保存前に server timestamp を付与する test を追加した。これは create API が timestamp 欠落の台帳 item を生成しないための local handler 境界確認である。
- 2026-05-20 06:25 UTC に Knowledge Memory の保存前 domain validation は Creative / News / Daily Intake / Dream の未知 status / review_status を拒否する test を追加した。これは unknown status の台帳 item を保存時点で拒否し、後続 current view を malformed にしないための local domain 境界確認である。
- 2026-05-20 06:30 UTC に Knowledge Memory の保存前 domain validation は Temporal Marker の `access_count` が負数の item を拒否する test を追加した。これは negative count の台帳 item を保存時点で拒否し、後続 current view を malformed にしないための local domain 境界確認である。
- 2026-05-20 06:35 UTC に Knowledge Memory の保存前 domain validation は Personal Archive / Creative / News / Daily Intake / Temporal Marker / Dream Run の `created_at` 欠落を拒否する test を追加した。これは timestamp 欠落の台帳 item を保存時点で拒否し、後続 current view を malformed にしないための local domain 境界確認である。
- 2026-05-20 10:40 UTC に Viewer Ops の Knowledge Memory fetch failure 境界を補強し、`/viewer/knowledge-memory` が non-2xx の場合は stale promoted/enabled 台帳や空台帳ではなく `knowledge memory status unavailable: ...` / `blocked: memory promote state unreadable` / `blocked: source registry sync state unreadable` を表示する Node contract test を追加した。これは status API failure を正式 memory promote / Source Registry 自動同期成功として誤読しないための visible-state 境界確認であり、正式 memory / Source Registry 自動同期 E2E の代替ではない。
- 2026-05-20 12:05 UTC に Memory tab の Knowledge Memory ledger fetch failure 境界を補強し、`/viewer/knowledge-memory` が non-2xx の場合は stale Personal Archive / Creative Knowledge / News Knowledge / Daily Intake / Temporal Marker / Dream Run row を使わず `Knowledge memory ledger unavailable: ...` を表示する Node contract test を追加した。これは route failure を正式 memory promote / Source Registry 自動同期 / Dream consolidation 成功として誤読しないための visible-state 境界確認であり、正式 memory / Source Registry 自動同期 E2E の代替ではない。
- 2026-05-20 12:35 UTC に Viewer Ops の Knowledge Memory detail fetch failure 境界を補強し、`/viewer/knowledge-memory?detail_type=...&id=...` が non-2xx の場合は generic error ではなく body 付き `HTTP <status>: ...` を Knowledge Detail Result に表示する Node contract test を追加した。これは detail route failure を promoted/enabled item の詳細取得成功や正式 memory / Source Registry 自動同期成功として扱わないための local 表示境界であり、正式 memory / Source Registry 自動同期 E2E の代替ではない。
- 2026-05-20 12:55 UTC に Memory tab の Knowledge Memory detail fetch failure 境界を補強し、`fetchMemoryKnowledgeDetail` が `/viewer/knowledge-memory?detail_type=...&id=...` の non-2xx body を `HTTP <status>: ...` として Knowledge Memory Detail に表示し、stale detail を残さない Node contract test を追加した。これは Memory tab の detail route failure を台帳詳細取得成功、review/promote 成功、正式 memory / Source Registry 自動同期成功として扱わないための local 表示境界であり、正式 memory / Source Registry 自動同期 E2E の代替ではない。
- 2026-05-20 09:56 UTC に Source Registry sweeper の HTTP fetch failure 境界を補強し、`sweepHTTPSource` が non-2xx response body を error に保持する local test を追加した。これは外部 source fetch failure を status だけで失わず `MarkSourceRegistryFetched` の error 証跡へ理由を残すための境界確認であり、live Source Registry fetch / staging / promote E2E の代替ではない。
- 2026-05-19 に `pkg/rencrowclient.SourceRegistryStatus` / `SourceRegistryStaging` / `ValidateSourceRegistryStaging` / `PromoteSourceRegistryStaging` を追加し、`/viewer/source-registry` の 2xx だけでは Source Registry 台帳 / staging validate / promote 成立扱いせず、source entry / staging item の同一 ID 重複、必須 field 欠落、trust score 範囲外、validate response の request id / passed-status-issues / auto-promote intent 不整合、promote response の target / staging_id / category / domain / namespace 不整合を local client test で拒否するようにした。これは Source Registry staging review / validate / promote API を E2E 証跡として読むための境界確認であり、外部 fetch 成功、正式 memory / news / knowledge 自動確定、または外部情報の正当性確認の代替ではない。
- 2026-05-20 13:05 UTC に Memory tab の Source Registry action failure 境界を補強し、validate / promote / run 操作が non-2xx の場合は generic error ではなく body 付き `HTTP <status>: ...` を staging status line または run status に表示する Node contract test を追加した。これは validate / promote / run route failure を Source Registry staging review、promote、外部 fetch/run 成功として扱わないための local 表示境界であり、live Source Registry E2E の代替ではない。
- 2026-05-20 13:15 UTC に Memory tab の Source Registry save / export / import action failure 境界を補強し、source save、YAML export、YAML import が non-2xx の場合は console-only generic error ではなく body 付き `HTTP <status>: ...` を Source Registry action status に表示する Node contract test を追加した。これは source 登録、YAML export/import、Source Registry 設定反映成功として扱わないための local 表示境界であり、live Source Registry E2E の代替ではない。
- 2026-05-20 13:25 UTC に Memory tab の memory state / promote action failure 境界を補強し、`/viewer/memory/state` と `/viewer/memory/promote` が non-2xx の場合は generic error ではなく body 付き `HTTP <status>: ...` を Memory table に表示する Node contract test を追加した。これは memory state 更新や memory promote route failure を Memory Layers 反映、Source Registry 自動同期、正式 memory promote 成功として扱わないための local 表示境界であり、live memory promote E2E の代替ではない。
- 2026-05-20 01:18 UTC に Source Registry validate response の terminal status 境界を追加し、`pending` response や `validated` / `rejected` と `passed` / issues が矛盾する response を local client test で拒否するようにした。`validated` は `passed=true` かつ issues 空、`rejected` は `passed=false` かつ issues ありでなければ E2E 証跡として扱わない。
- 2026-05-19 23:22 UTC に一時 Conversation L1 config と隔離 SQLite `/tmp/picoclaw-source-registry-live-e2e/l1.sqlite` で `TestE2E_SourceRegistryStagingValidatePromoteAndMemoryLayers` を実行して pass。local RSS fixture の live source 登録、fetch/run、validated staging、明示 validate、memory promote、Memory Layers の promoted memory 表示まで確認した。通常 config は復元済みで、通常 live readiness は `conversation_enabled=false` / `l1_sqlite_config_present=false` / `source_registry_available=false` / `memory_layers_available=false` を正とする。
- `pkg/rencrowclient.SourceRegistryStatus` / `SourceRegistryStaging` は source kind、fetch `last_status`、staging / validation status を許可値に限定し、`last_status=error` の source entry に `last_error` がない場合も E2E 証跡として成功扱いしない。保存層の `MarkSourceRegistryFetched` も unknown fetch status と error 証跡なしを拒否することを確認済み。
- 2026-05-19 に `pkg/rencrowclient.BrowserTraceAPIStatus` / `DiscoverBrowserTraceAPI` / `CreateBrowserTraceAPIFetcherProposal` を追加し、`/viewer/browser-trace-api` / `discover` / `fetcher-proposals` の 2xx だけでは Browser Trace API discovery / fetcher proposal 成立扱いせず、trace / candidate / schema / validation / coverage / artifact の同一 ID 重複や必須 field 欠落、write method、validation status / issues、discover response の request trace 整合、fetcher proposal の `official_promotion=false` / `implementation_apply=false` / `pending_review` artifact / validated candidate / Workstream artifact 整合不備を local client test で拒否するようにした。これは Browser Trace API discovery と fetcher proposal を E2E 証跡として読むための境界確認であり、実 Fetcher 実装、正式 API 採用、promoted DB write、外部 API 利用規約確認の代替ではない。
- 2026-05-20 02:05 UTC に Browser Trace API schema の `schema_json` を保存前 validation と client current view validation で JSON として検証し、壊れた schema 文字列を Browser Trace API discovery / fetcher proposal の E2E 証跡として受け取らない local test を追加した。
- 2026-05-20 00:01 UTC に `pkg/rencrowclient.ValidateBrowserTraceAPICandidate` と `/viewer/browser-trace-api/validations` を追加し、Human review evidence なしでは `needs_review`、review evidence が揃った場合だけ `validated` として保存する境界を追加した。live `TestE2E_BrowserTraceAPIDiscoverValidateAndFetcherProposal` で local trace fixture の discover、initial `needs_review`、validation review、review-only fetcher proposal、current view を確認済み。証跡は `trace_run_id=trace_e2e_20260520000547.552107722`、`candidate_id=api_cand_d7c3f3f50a6a`、`validation_id=api_val_review_api_cand_d7c3f3f50a6a_1779235267925517562`、`proposal_artifact_id=art_fetcher_proposal_api_cand_d7c3f3f50a6a`。正式 API 採用、promoted DB write、実 Fetcher 実装、外部 API 利用規約の自動確認ではない。
- `pkg/rencrowclient.BrowserTraceAPIStatus` と `internal/domain/browsertrace` は APICandidate status を `candidate`、APIValidation status を `validated` / `needs_review`、APIArtifact status を `generated` / `draft` / `pending_review` に限定し、未知 status を E2E 証跡や保存対象として成功扱いしないことを確認済み。
- 2026-05-20 01:01 UTC に APIValidation の status / passed / issues 整合性も固定した。`validated` は `passed=true` かつ issues 空、`needs_review` は `passed=false` かつ issues ありでなければ、保存前 validation と `pkg/rencrowclient.BrowserTraceAPIStatus` の current view validation が拒否する。これは validation status 文字列だけを正式 API 採用や Fetcher 実装の証跡として扱わないための境界である。
- 2026-05-20 01:36 UTC に `pkg/rencrowclient.BrowserTraceAPIStatus` の数値証跡境界を補強し、candidate confidence 範囲外、schema `sample_count < 0`、schema confidence 範囲外を malformed current view として拒否する local client test を追加した。
- 2026-05-20 03:45 UTC に `pkg/rencrowclient.BrowserTraceAPIStatus` の current view validation を補強し、trace run / API candidate / schema / validation / coverage / artifact の `created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の ledger item は Browser Trace discovery / validation review / fetcher proposal 証跡として扱わない。
- 2026-05-20 06:40 UTC に Browser Trace API の保存前 domain validation は trace run / API candidate / schema / validation / coverage / artifact の `created_at` 欠落を拒否する test を追加した。timestamp 欠落の ledger item は保存時点で拒否し、後続 current view を malformed にしない。
- 2026-05-20 05:35 UTC に `pkg/rencrowclient.CreateBrowserTraceAPIFetcherProposal` の direct response validation を補強し、Workstream Artifact を返す fetcher proposal response は `workstream_artifact.created_at` 欠落を local client error として拒否する test を追加した。timestamp 欠落の Workstream Artifact は Browser Trace fetcher proposal 証跡として扱わない。
- 2026-05-19 17:21 UTC に live service で `TestE2E_KnowledgeMemoryAndBrowserTraceStatusClientCurrentView` を実行し、`KnowledgeMemoryStatus` と `BrowserTraceAPIStatus` が空台帳の current view を読めることを確認した。これは status route 到達確認であり、Knowledge Memory の create / review / promote、Browser Trace の実 trace discover / fetcher proposal / 正式 API 採用の完了条件にはしない。
- 2026-05-20 10:30 UTC に Viewer Ops は `/viewer/browser-trace-api` fetch failure 時、stale trace candidate / fetcher proposal 件数を表示せず `browser trace api status unavailable: ...` / `blocked: official API adoption state unreadable` / `blocked: fetcher implementation state unreadable` を表示する Node contract test を追加した。これは status API failure を正式 API 採用や実 Fetcher 実装証跡として扱わないための local 表示境界である。
- 2026-05-19 18:57 UTC に live service を現 binary へ再 install / restart し、`/viewer/knowledge-memory?limit=1` と `/viewer/browser-trace-api?limit=1` が空台帳を返すこと、Viewer Ops card が `blocked: empty ledger` / `blocked: no trace candidates` を visible state として表示することを `TestE2E_Phase25BrowserViewerSessionContract` で確認した。これは空台帳 route を成功扱いしないための表示境界であり、Knowledge Memory の create / review / promote、Browser Trace の実 trace discover / fetcher proposal / 正式 API 採用の完了条件にはしない。
- 2026-05-19 22:50 UTC に Viewer Ops の Knowledge Memory / Browser Trace API card へ `blocked: no memory promote verified` / `blocked: no official API adoption` を追加した。route present や空台帳は正式 memory promote や公式 API 採用成功の代替ではない。
- 2026-05-19 23:43 UTC に `pkg/rencrowclient.CreateKnowledgeNewsItem` / `CreateKnowledgeDailyIntakeRule` と live `TestE2E_KnowledgeMemoryCreateReviewPromoteCurrentView` を追加し、通常 live config で Knowledge Memory の News 作成 -> approved promote -> `promoted` current view、Daily Intake Rule 作成 -> approved promote -> `enabled` current view を確認した。証跡は `news_id=news_e2e_20260519234334.871771383`、`rule_id=rule_e2e_20260519234334.871771383`。JSONL store は同一 ID 追記履歴を最新 state per ID に集約する。通常 config は Conversation L1 disabled なので、正式 memory / Source Registry 自動同期や `/viewer/memory/layers` 表示の成功とは分けて扱う。
- 2026-05-19 23:48 UTC に live create/review/promote 後の Viewer Ops card 契約を `blocked: empty ledger` から `daily intake:` / `news:` / `review-only: promote not verified` へ更新した。これは Knowledge Memory 台帳の current view が空でなくなったためである。
- 2026-05-20 10:40 UTC に Knowledge Memory status fetch failure 時は stale promoted news / enabled daily intake rule を使わず `knowledge memory status unavailable: ...` を visible state として表示する Node contract test を追加した。これは status API failure の見せ方であり、正式 memory promote / Source Registry 自動同期成功ではない。
- 2026-05-20 12:05 UTC に Memory tab の Knowledge Memory ledger fetch failure 時は stale Personal Archive / Creative / News / Daily Intake / Temporal Marker / Dream Run row を使わず `Knowledge memory ledger unavailable: ...` を visible state として表示する Node contract test を追加した。これは ledger route failure の見せ方であり、正式 memory promote / Source Registry 自動同期 / Dream consolidation 成功ではない。
- 2026-05-20 00:02 UTC に live Browser Trace discover/validation/proposal 後の Viewer Ops card 契約を `blocked: no trace candidates` から `fetcher proposals:` / `review-only: no official API adoption` へ更新した。live browser `TestE2E_Phase25BrowserViewerSessionContract` は pass。これは Browser Trace の current view が空でなくなったためであり、正式 API 採用や実 Fetcher 実装の成功扱いにはしない。
- 2026-05-20 10:30 UTC に Browser Trace API status fetch failure 時は stale candidate / fetcher proposal を使わず `browser trace api status unavailable: ...` を visible state として表示する。これは status API failure の見せ方であり、正式 API 採用、promoted DB write、実 Fetcher 実装の成功ではない。
- 2026-05-19 17:26 UTC に runtime を更新し、L1 store 無効時の live `/viewer/source-registry` は 404 ではなく 503 `source registry unavailable` を返すことを確認した。Source Registry staging review / validate / promote の live E2E は blocked として維持する。503 は依存不足の明示であり、route 成功扱いにしない。
- 2026-05-19 17:31 UTC に `/viewer/runtime-config` の `runtime_readiness` へ Source Registry / Conversation L1 readiness を追加した。live config では `conversation_enabled=false`、`l1_sqlite_config_present=false`、`source_registry_available=false` であり、`TestE2E_Phase25LiveViewerRuntimeConfigClient` と `PICOCLAW_BROWSER_E2E=1` の `TestE2E_Phase25BrowserViewerSessionContract` で API / Ops visible state を確認した。readiness false は blocked 理由であり、Source Registry staging review / validate / promote 成功扱いにはしない。
- 2026-05-19 20:45 UTC に Memory Layers / Source Registry route readiness を追加した。live `/viewer/runtime-config` は L1 disabled だが status route present として `memory_layers_status_available=true` / `source_registry_status_available=true` を返し、Ops readiness では `memory-route:present` / `source-route:present` と `blocked: conversation L1 disabled` が見える。503 route present は成功ではなく、blocked 証跡として扱う。
- 2026-05-19 20:51 UTC に Runtime Health card を追加し、live `/health` 503 または timeout を Viewer Ops で `service:missing` / `chat:missing` / `worker:missing` として確認した。これは local LLM endpoint 復旧ではなく、画面上の失敗可視化である。
- 2026-05-19 20:58 UTC に Revenue external send apply audit を Viewer Ops の専用表として表示するようにした。live current view は `external_channel_adapter=unconfigured`、apply audit は `blocked` / `not_sent` / `post_send_verified=false` / `external_send_applied=false` で、browser E2E は audit 表の visible state を確認した。これは外部送信 apply audit の可視化であり、実送信成功や post-send verification 成功ではない。
- 2026-05-19 21:02 UTC に Skill Governance external PR submit audit を Viewer Ops の専用表として表示するようにした。live current view は `external_pr_adapter=unconfigured`、submit audit は `blocked` / `external_pr_created=false` / `post_submit_verified=false` で、browser E2E は audit 表の visible state を確認した。これは外部 PR submit audit の可視化であり、実 PR 作成や post-submit verification 成功ではない。
- 2026-05-19 21:08 UTC に Sandbox Promotion Gate Log を Viewer Ops の専用表として表示するようにした。通常 config の live `/viewer/sandbox?limit=1` は 503 `sandbox store unavailable` のままだが、browser E2E は Gate Log summary の visible state を確認した。これは post-apply evidence の表示境界であり、正式 apply / rollback 成功ではない。
- 2026-05-19 22:41 UTC に Gate Log summary へ `formal apply requires human approval` / `blocked: no promotion applied` と rollback 件数を追加した。Gate Log 表示や post-apply evidence 件数は正式 apply / rollback 成功の代替ではない。
- 2026-05-20 10:10 UTC に Sandbox status fetch failure 時は stale Gate Log を使わず `sandbox status unavailable: ...` / `Sandbox gate logs unavailable: ...` を visible state として表示する。これは通常 config disabled や status API failure の見せ方であり、正式 apply / rollback 成功ではない。
- 2026-05-19 20:30 UTC に Sandbox readiness を追加した。live `/viewer/runtime-config` は `sandbox_enabled=false`、`sandbox_status_available=true` を返し、Ops readiness では `Sandbox` / `enabled:missing` / `status:present` / `blocked: sandbox disabled` が visible state として確認できる。これは Sandbox disabled の blocked 理由であり、正式 apply / rollback 成功扱いにはしない。
- 2026-05-19 20:38 UTC に Knowledge Memory / Browser Trace API readiness を追加した。live `/viewer/runtime-config` は Knowledge Memory と Browser Trace API の status route present を返し、Ops readiness では `Knowledge Memory` / `/viewer/knowledge-memory`、`Browser Trace API` / `fetcher:present` / `review-only: discover and fetcher proposal require evidence` が visible state として確認できる。これは空台帳 / review-only route の可視化であり、実 memory promote や fetcher proposal 成功扱いにはしない。
- 2026-05-19 22:50 UTC に Ops card 側でも `blocked: no memory promote verified` / `blocked: no official API adoption` を表示し、readiness present と正式 promote / 正式 API 採用を分離した。23:48 UTC 時点では Knowledge Memory 台帳が非空になったため、Ops card は `review-only: promote not verified` を表示する。
- 2026-05-19 09:43 UTC に `pkg/rencrowclient.CheckExternalControl` から live service の external control policy check を呼び、Human approval なしの `promotion_apply` が `needs_approval` で止まり、`workflow_events` に保存されることを `go test -tags=e2e ./test/e2e -run TestE2E_AIWorkflowExternalControlClientRequiresApproval` で確認した。
- 正式環境変更、外部送信、PR 作成は実行していない。
- verifier / ToolRunner 未接続時の `post_apply_verification_command` 拒否は local handler 境界の pass とする。Human approval ありの正式 promotion apply E2E は未実施のまま残す。

### 4.0.4 2026-05-19 Complexity Hotspot report-only / review-only live verification result

実行済み:

```bash
curl -sS -i http://127.0.0.1:18790/viewer/complexity-hotspots/scan \
  -H 'Content-Type: application/json' \
  -d '{"scan_id":"scan_live_20260519083500","workstream_id":"ws_live_20260519083500","repo":"picoclaw_multiLLM","root_path":".","scan_scope":["internal/application/complexity"],"max_hotspots":3,"exclude_dirs":[".git","build","node_modules","vendor"]}'
curl -sS http://127.0.0.1:18790/viewer/complexity-hotspots?limit=20 | \
  rg 'scan_live_20260519083500|art_complexity_scan_live_20260519083500|report_only'
curl -sS -i http://127.0.0.1:18790/viewer/complexity-hotspots/proposals \
  -H 'Content-Type: application/json' \
  -d '{"hotspot_id":"scan_live_20260519083500_nested_loop_1","workstream_id":"ws_live_20260519083500","goal_id":"goal_complexity_live_20260519085000","artifact_id":"art_complexity_live_20260519085000"}'
curl -sS -i http://127.0.0.1:18790/viewer/complexity-hotspots/concrete-diffs \
  -H 'Content-Type: application/json' \
  -d '{"hotspot_id":"scan_live_20260519083500_nested_loop_1","workstream_id":"ws_live_20260519083500","artifact_id":"art_complexity_concrete_live_20260519085000","concrete_diff":"diff --git a/internal/application/complexity/analyzer.go b/internal/application/complexity/analyzer.go\n--- a/internal/application/complexity/analyzer.go\n+++ b/internal/application/complexity/analyzer.go\n@@ -108,1 +108,1 @@\n-old\n+new","test_result_path":"sandbox/ws_live_20260519083500/reports/test.txt","rollback_plan_path":"sandbox/ws_live_20260519083500/reports/rollback.md"}'
```

判定:

- `/viewer/complexity-hotspots/scan` は 201。
- `scan_live_20260519083500` は `mode=report_only`、`status=completed`、`files_scanned=8`、`hotspots_found=3`。
- `art_complexity_scan_live_20260519083500` の report artifact、hotspot、evidence が status API から確認できた。
- `/viewer/complexity-hotspots/proposals` は 201。proposal / coder diff request / Workstream review artifact が保存された。
- `/viewer/complexity-hotspots/concrete-diffs` は 201。`art_complexity_concrete_live_20260519085000` が `complexity_concrete_diff_proposal` / `pending_review` として保存され、`patch_applied=false` / `human_approval_required=true` を返した。
- 対象外ファイルの concrete diff は 400 で拒否された。
- 2026-05-19 09:56 UTC に一時 sandbox config で `/viewer/sandbox` route を有効化し、`sandbox_id` 付き proposal `promo_complexity_sandbox_live_20260519095600` が Promotion Request / Gate Log を作り、`diff_path` / `test_result_path` / `rollback_plan_path` / `human_approval` 不足で `needs_more_tests` になることを確認した。
- 同じ一時 config で `sandbox_id` 付き concrete diff `promo_complexity_sandbox_concrete_live_20260519095630` が Promotion Request / Gate Log を作り、diff / test / rollback はあるが Human approval 未承認のため `needs_review` で止まることを確認した。
- `/viewer/sandbox?limit=10` から両 promotion と `evt_complexity_promotion_gate_1779184573811590017` / `evt_complexity_concrete_diff_gate_1779184588820294958` を確認した。
- 証跡取得後、一時 sandbox config は削除し、当時の live service 再起動後に `/viewer/sandbox` が 404 へ戻ることを確認した。2026-05-19 20:23 UTC の runtime 更新後は Sandbox disabled 時も status route が 503 `sandbox store unavailable` を返す。
- 2026-05-19 09:02 UTC に sandbox store 未接続の proposal / concrete diff を再確認し、503 時に新規 `goal_id` / `artifact_id` / `promotion_id` が status API へ部分保存されないことを確認した。
- 2026-05-19 に `coder-diffs` の未接続 generator / generator error / unified diff 不備 / 対象 hotspot file 外 diff が review artifact / Sandbox Promotion Request 保存前に止まることを test で確認した。runtime wiring でも Coder provider 未接続時は `/viewer/complexity-hotspots/coder-diffs` を 404 にせず 503 `complexity coder diff mode unavailable` で止める local test を追加した。実 Coder provider live E2E は未確認のまま残す。
- 2026-05-19 15:26 UTC に現行 binary へ更新後、live `/viewer/complexity-hotspots/coder-diffs` を呼び、Coder provider 応答に unified diff がない場合は `400 coder output did not contain unified diff` で停止することを確認した。status API に該当 job / artifact / error は残らなかった。当時の `/viewer/sandbox?limit=5` は 404。2026-05-19 20:23 UTC の runtime 更新後は Sandbox disabled 時も status route が 503 `sandbox store unavailable` を返す。
- `coder-diffs` generation timeout を local handler test で確認済み。timeout 時は `503 complexity coder diff generation timed out` で停止し、review artifact / Workstream artifact / Sandbox Promotion Request を部分保存しない。
- 2026-05-20 00:10 UTC に Coder diff prompt へ same-hotspot evidence snippets と diff-only system prompt を追加後、現 binary へ `make install` / service restart し、live `/viewer/complexity-hotspots/coder-diffs` を `job_complexity_coder_diff_evidence_20260520001000` で再実行した。結果は `503 complexity coder diff generation timed out` で、status current view に該当 job / artifact / timeout error は残らなかった。これは実 Coder provider 成功ではなく、入力証跡改善後も timeout blocked が残る証跡である。
- 2026-05-19 に `pkg/rencrowclient.ComplexityStatus` / `CreateComplexityConcreteDiff` / `CreateComplexityCoderDiff` を追加した。concrete diff / coder diff API が 2xx を返しても、`patch_applied=false`、Human approval required、pending review artifact、sandbox gate、coder result の整合性が取れない response は direct client success としない。これは実 Coder provider live E2E の代替ではない。
- 2026-05-19 17:08 UTC に live service で `TestE2E_ComplexityStatusClientCurrentView` を実行した。初回は JSONL store の append 履歴により同一 report `artifact_id` が複数返り、client validation が fail。JSONL store の Complexity List API を最新 state per ID の current view へ修正し、`make install` / service restart 後に同 E2E は pass。completed report-only scan、hotspot、evidence、pending_review report artifact を確認したが、実 Coder provider 成功、patch 適用、Sandbox apply、外部 PR 作成は未確認のまま残す。
- 2026-05-19 18:49 UTC に現 binary へ `make install` / service restart 後、`TestE2E_ComplexityStatusClientCurrentView` と `TestE2E_Phase25BrowserViewerSessionContract` を再実行し pass。Viewer Ops card が `reports` / `pending-review` と `mode: review-only` / `blocked: no patch applied` を表示することを確認した。これは patch 未適用を見える化するもので、実 Coder provider 成功、patch 適用、Sandbox apply、外部 PR 作成の代替ではない。
- 2026-05-20 10:20 UTC に Complexity status fetch failure 時は stale report を使わず `complexity hotspot status unavailable: ...` / `Complexity review artifacts unavailable: ...` を visible state として表示する。これは status API failure の見せ方であり、patch 適用や外部 PR 作成の成功ではない。
- 2026-05-19 に Sandbox Promotion apply handler は `post_apply_verification_command` 指定時に verifier / ToolRunner が未接続なら 503 で停止し、completed artifact / `promotion_applied` log を保存しないことを test で確認した。
- patch 適用、Sandbox Promotion apply、外部 PR 作成は実行していない。

扱い:

- report-only live scan、proposal review artifact、concrete diff review artifact、対象外 diff rejection は部分 pass とする。
- Sandbox Gate live は一時 sandbox config で部分確認済み。Sandbox Promotion apply の verifier 未接続拒否は local handler test 済みだが、Human approval あり正式 apply、patch 適用、外部 PR 作成は実行していない。
- Coder diff provider の live 呼び出しは malformed output または timeout で blocked。in-scope unified diff を返す provider 成功 E2E は未確認として残す。
- patch 適用や外部 PR 作成はこの skill の通常完了条件に含めない。

### 4.0.4.1 2026-05-19 Skill Governance external PR submit audit verification result

実行済み:

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/domain/skillgovernance ./internal/infrastructure/persistence/skillgovernance ./internal/adapter/viewer ./pkg/rencrowclient ./cmd/picoclaw
node internal/adapter/viewer/viewer_memory_panel.test.mjs
GOCACHE=/tmp/picoclaw-gocache PICOCLAW_LIVE_E2E=1 go test -tags=e2e ./test/e2e \
  -run TestE2E_SkillGovernanceExternalPRClientRequiresApprovalAndAuditsBlockedSubmit -count=1 -v
curl -sS -i http://127.0.0.1:18790/viewer/skill-governance/contribution-gate \
  -H 'Content-Type: application/json' \
  -d '{"event_id":"evt_skill_pr_gate_live_20260519103000","repo":"example/repo","target_branch":"main","problem_statement":"live audit boundary check for external PR submit","existing_prs_checked":true,"real_problem_verified":true,"core_change_verified":true,"diff_human_approved":true,"test_result":"GOCACHE=/tmp/picoclaw-gocache go test ./internal/domain/skillgovernance ./internal/infrastructure/persistence/skillgovernance ./internal/adapter/viewer ./pkg/rencrowclient ./cmd/picoclaw"}'
curl -sS -i http://127.0.0.1:18790/viewer/skill-governance/external-pr-submit \
  -H 'Content-Type: application/json' \
  -d '{"submit_id":"submit_skill_pr_live_noapproval_20260519103000","contribution_event_id":"evt_skill_pr_gate_live_20260519103000","repo":"example/repo","title":"Live audit boundary check","human_approved":false}'
curl -sS -i http://127.0.0.1:18790/viewer/skill-governance/external-pr-submit \
  -H 'Content-Type: application/json' \
  -d '{"submit_id":"submit_skill_pr_live_20260519103000","contribution_event_id":"evt_skill_pr_gate_live_20260519103000","repo":"example/repo","title":"Live audit boundary check","diff_path":"workspace/logs/skill_governance/coder_evidence/job-live/skill_diff.md","test_result":"GOCACHE=/tmp/picoclaw-gocache go test ./internal/domain/skillgovernance ./internal/infrastructure/persistence/skillgovernance ./internal/adapter/viewer ./pkg/rencrowclient ./cmd/picoclaw","human_approved":true}'
curl -sS 'http://127.0.0.1:18790/viewer/skill-governance/recent?limit=5' | \
  rg 'submit_skill_pr_live|external_pr_submit_records|external PR adapter'
```

判定:

- Contribution Gate は `evt_skill_pr_gate_live_20260519103000` を `gate_status=passed` として保存した。
- Human approval なしの external PR submit request は 403 で停止し、audit record は保存されなかった。
- Human approval ありの request は 202。実 PR は作らず、`submit_skill_pr_live_20260519103000` が `submit_status=blocked`、`failure_reason="external PR adapter is not configured"`、`external_pr_created=false`、`post_submit_verified=false` として保存された。
- created 成功 audit は `pr_url` と `post_submit_evidence` がない場合に domain validation で拒否される。blocked audit を実 PR 作成済み / post-submit verified 扱いにしない。
- blocked audit は request 側の成功値を採用しない。local handler test で `submit_status=created` / `pr_url` / `external_pr_created=true` / `post_submit_verified=true` / `post_submit_evidence` / `pr_adapter=github` が混入しても、保存時は `blocked`、PR URL なし、検証証跡なし、`pr_adapter=unconfigured` になることを確認した。
- Contribution Gate client は malformed success response を成功扱いしない。event_id / repo / gate_status / decision status / can_contribute / stop_reasons が一致しない場合は error になる。
- external PR client は malformed success response を成功扱いしない。response record の `submit_id` / `contribution_event_id` / `repo` が request と一致しない場合、top-level / record の作成・検証状態が矛盾する場合、または `pr_url` / `post_submit_evidence` がない created success は error になる。
- `pkg/rencrowclient.SubmitSkillGovernanceExternalPR` / `EvaluateSkillGovernanceContributionGate` は `submit_id` / `contribution_event_id` / `repo` / `title` などが欠けた request を送信前に拒否する。2026-05-20 00:54 UTC 時点では `SubmitSkillGovernanceExternalPR` の `human_approved=false` も client 側で拒否する。
- 2026-05-20 01:37 UTC に `pkg/rencrowclient.SubmitSkillGovernanceExternalPR` / `SkillGovernanceStatus` は external PR submit response / current view の `title`、`human_approved=true`、`approval_status=approved`、top-level `human_approval_required_for_pr=true` を検証する local client test を追加した。これは Human approval と approved audit が揃わない external PR record を実 PR 作成証跡として扱わないための境界確認であり、実 PR 作成 E2E の代替ではない。
- 2026-05-20 02:32 UTC に `pkg/rencrowclient.SubmitSkillGovernanceExternalPR` / `SkillGovernanceStatus` は external PR adapter required / unconfigured 状態で record 側だけ `pr_adapter=github` などを返す malformed response / current view を拒否する local client test を追加した。これは blocked audit を adapter 接続済みや実 PR 作成証跡として扱わないための境界確認であり、実 PR 作成 E2E の代替ではない。
- `/viewer/skill-governance/recent` は `external_pr_submit_records` を返し、Ops Viewer summary は external PR audit 件数を表示する。
- `TestE2E_SkillGovernanceExternalPRClientRequiresApprovalAndAuditsBlockedSubmit` は `pkg/rencrowclient` 経由で同 flow を live service に対して実行し、pass した。
- 2026-05-19 17:41 UTC に `/viewer/skill-governance/recent?limit=3` が `external_pr_adapter=unconfigured`、`external_pr_adapter_configured=false`、`human_approval_required_for_pr=true` を返すことを確認した。
- 初回 live status client 再実行では、JSONL store が同一 `skill_id` manifest の append 履歴を複数返し、current view validation が fail した。JSONL `ListSkillManifests` を最新 manifest per `skill_id` に修正し、`make install` / service restart 後に同 live E2E は pass した。

扱い:

- PR gate 後の Human approval 必須 audit 境界は部分 pass。
- blocked audit は実 PR 作成成功ではない。GitHub / 外部 repo への実 PR 作成、PR URL、post-submit verification は未確認として残す。

### 4.0.4.2 2026-05-19 15:19 UTC Skill Governance external PR adapter blocked recheck

実行済み:

```bash
command -v gh
gh auth status
git remote -v
curl -sS -m 5 'http://127.0.0.1:18790/viewer/skill-governance/recent?limit=5'
sed -n '243,292p' internal/adapter/viewer/skill_governance_handler.go
rg -n 'NewBlockedExternalPRSubmitRecord|external PR adapter is not configured|SubmitSkillGovernanceExternalPR|ValidateExternalPRSubmitRecord' \
  internal/domain/skillgovernance internal/adapter/viewer pkg/rencrowclient
```

判定:

- `gh` は `/usr/bin/gh` に存在し、`github.com` へ `Nyukimin` として認証済み。`origin` は `https://github.com/Nyukimin/picoclaw_multiLLM`。
- ただし live `/viewer/skill-governance/recent?limit=5` の `external_pr_submit_records` は `submit_status=blocked`、`external_pr_created=false`、`post_submit_verified=false`、`pr_adapter=unconfigured` の audit record のみ。
- handler は passed Contribution Gate と `human_approved=true` 後も `domainskill.NewBlockedExternalPRSubmitRecord` を保存し、response で `external_pr_created=false` / `external_pr_adapter_configuration=required` を返す。
- GitHub CLI 認証は実 PR 作成 E2E の成功条件ではない。RenCrow 側 adapter、Human approval、対象 repo / branch、diff、rollback、test result、PR URL 保存、post-submit verification が揃うまで実 PR 作成は blocked とする。

### 4.0.5 2026-05-19 SuperAgent run queue manual ledger / scheduler terminal verification result

実行済み:

```bash
curl -sS -i http://127.0.0.1:18790/viewer/superagent/run-queue \
  -H 'Content-Type: application/json' \
  -d '{"queue_id":"rq_live_20260519083900","run_id":"run_live_20260519083900","workstream_id":"ws_live_20260519083900","goal":"live E2E: verify run queue ledger reaches terminal state without scheduler execution","action":"resume","status":"queued","priority":5,"not_before":"2026-05-19T08:39:00Z","created_at":"2026-05-19T08:39:00Z"}'
curl -sS -i http://127.0.0.1:18790/viewer/superagent/run-queue/claim -X POST
curl -sS -i http://127.0.0.1:18790/viewer/superagent/run-queue/complete \
  -H 'Content-Type: application/json' \
  -d '{"queue_id":"rq_live_20260519083900","status":"completed","reason":"manual ledger E2E completed; scheduler remains disabled in live config"}'
curl -sS http://127.0.0.1:18790/viewer/superagent?limit=20 | \
  rg 'rq_live_20260519083900|run_live_20260519083900|completed'
curl -sS http://127.0.0.1:18790/viewer/superagent?limit=1
curl -sS -i http://127.0.0.1:18790/viewer/superagent/run-queue \
  -H 'Content-Type: application/json' \
  -d '{"queue_id":"rq_scheduler_live_20260519092900","run_id":"run_scheduler_live_20260519092900","workstream_id":"ws_scheduler_live_20260519092900","goal":"SuperAgent scheduler E2E ping. Do not send externally. Return a short diagnostic response.","action":"chat","status":"queued","priority":10,"created_at":"2026-05-19T09:29:00Z"}'
sleep 5
curl -sS http://127.0.0.1:18790/viewer/superagent?limit=100
GOCACHE=/tmp/picoclaw-gocache PICOCLAW_LIVE_E2E=1 go test -tags=e2e ./test/e2e \
  -run TestE2E_SuperAgentRunQueueClientManualLedgerFlow -count=1 -v
GOCACHE=/tmp/picoclaw-gocache PICOCLAW_LIVE_E2E=1 go test -tags=e2e ./test/e2e \
  -run 'TestE2E_SuperAgent(PauseResumeAndQueueReentryClientFlow|RunQueueClientManualLedgerFlow)' -count=1 -v
```

判定:

- create は 201、claim は 200 `claimed=true`、complete は 200 `status=completed`。
- `TestE2E_SuperAgentRunQueueClientManualLedgerFlow` は `pkg/rencrowclient.CreateRunQueueItem` / `ClaimRunQueueItem` / `CompleteRunQueueItem` を使い、live service の `/viewer/superagent` current view が対象 queue を `completed` 1件だけ返すことを確認した。
- `TestE2E_SuperAgentPauseResumeAndQueueReentryClientFlow` は `pkg/rencrowclient.PauseRun` / `ResumeRun` で pause -> resume marker clear、同一 `run_id` / `workstream_id` の queue 再投入、`lead_agent_paused` / `lead_agent_resumed` trace、completed queue current view を live service で確認した。
- JSONL store の status view は修正前に同一 `queue_id` の履歴を複数返していたため、SQLite store と同じ current view になるよう `ListRunQueueItems` を最新 state per queue に集約した。
- 修正後の live service では `/viewer/superagent?limit=20` が `rq_live_20260519083900` を `completed` 1件だけ返す。
- 2026-05-19 09:10 UTC に `/viewer/superagent` status API の `runtime_config` で、live service の実効値が `run_queue_scheduler_enabled=false`、`run_queue_scheduler_interval_sec=60`、`run_queue_scheduler_claim_limit=1` であることを確認した。
- 2026-05-19 09:28 UTC に一時 config で `run_queue_scheduler_enabled=true`、`run_queue_scheduler_interval_sec=1`、`run_queue_scheduler_claim_limit=1` を有効化し、status API の `runtime_config` で確認した。
- `rq_scheduler_live_20260519092900` は scheduler により claimed され、local orchestrator へ `channel=superagent` / `chat_id=rq_scheduler_live_20260519092900` として渡った。`context_pack` には `route=CHAT channel=superagent` が残った。
- local LLM `http://192.168.1.13:8081` が `no route to host` のため、queue は `failed`、LeadAgent run は `failed`、trace は `run_queue_claimed` と `run_queue_failed` に収束した。これは user flow 正常完了ではなく、明示 failure として扱う。
- scheduler processor は `resume` / `process_message` action の `CHAT` route や route / `job_id` 欠落を success summary にせず error にする local test 済み。明示 `chat` action の `CHAT` route は `job_id` 付きなら許可する。CHAT fallback は queue `completed` に進めない。
- `pkg/rencrowclient.ClaimRunQueueItem` は claim API が 2xx を返しても、`claimed=true` 時に `queue_id`、`status=claimed`、`created_at` が揃わない malformed response を direct client success として返さない。`claimed=false` なのに claimed item state を含む response も error とする。
- `pkg/rencrowclient.CompleteRunQueueItem` は `queue_id` 欠落や `completed` / `failed` / `cancelled` 以外の terminal status を送信前に拒否する。空 status は `completed` に正規化する。complete API が 2xx を返しても、`completed=true`、対象 `queue_id`、要求 terminal status、`created_at`、terminal `completed_at` が response item と一致しない malformed response を direct client success として返さない。
- `pkg/rencrowclient.CreateAgentRun` / `CreateTraceEvent` / `CreateRunQueueItem` は、必須 ID / type / status / goal / action が欠けた request を送信前に拒否する。run queue create は空 status を `queued` に正規化し、`queued` 以外の create status は送らない。
- `pkg/rencrowclient.PauseRun` / `ResumeRun` は pause / resume API が 2xx を返しても、run_id、期待 status、event_id、runtime_control_action が一致しない malformed response を direct client success として返さない。
- JSONL store の `agent_runs` も SQLite と同じ current view になるよう、最新 state per `run_id` へ集約した。
- 証跡取得後、一時 config は削除し、live service 再起動後の `runtime_config` は `run_queue_scheduler_enabled=false`、`run_queue_scheduler_interval_sec=60`、`run_queue_scheduler_claim_limit=1` に戻した。

扱い:

- run queue manual ledger flow は curl と `pkg/rencrowclient` live E2E の両方で部分 pass。pause / resume marker と queue 再投入台帳も client E2E で部分 pass。scheduler claim / terminal failure flow も部分 pass。
- LLM 到達可能状態での正常完了、fallback でない user flow 成立、実行中 goroutine を伴う真の長時間再開 E2E は残課題として維持する。
- local contract としては、RunController の pause request で実行中 context が cancel された場合に LeadAgent run が `paused` terminal / `lead_agent_paused` trace で記録される test を追加済み。live 長時間再開 E2E の代替成功にはしない。

#### 4.0.5.1 2026-05-19 15:11 UTC SuperAgent scheduler 正常完了 E2E 再確認

実行コマンド:

```bash
curl -sS -m 5 http://127.0.0.1:18790/health
curl -sS -m 5 http://127.0.0.1:18790/viewer/runtime-config
curl -sS -m 5 'http://127.0.0.1:18790/viewer/superagent?limit=5'
curl -sS -m 5 'http://127.0.0.1:18790/viewer/llm-ops/status'
```

判定:

- `blocked`。
- `/health` は `local_llm_chat` / `local_llm_worker` が `http://192.168.1.13:8081` / `:8082` へ `no route to host` のため `status=down`。
- `/viewer/superagent?limit=5` は 200 だが、`runtime_config.run_queue_scheduler_enabled=false`。
- `/viewer/llm-ops/status` は `upstream unreachable`。
- scheduler 正常完了 E2E は、LLM endpoint 復旧と scheduler enabled の一時確認環境が揃うまで未実施として維持する。

### 4.0.6 2026-05-19 Revenue draft-only / Human Decision Gate / apply audit live verification result

実行済み:

```bash
curl -sS -i http://127.0.0.1:18790/viewer/revenue/channel-drafts \
  -H 'Content-Type: application/json' \
  -d '{"draft_id":"rev_draft_live_20260519084700","workstream_id":"ws_live_20260519084700","channel":"manual_review_only","subject":"live draft-only verification","body":"This is a draft-only revenue message for live E2E verification. Do not send externally.","approval_status":"pending","external_send_applied":true,"created_at":"2026-05-19T08:47:00Z"}'
curl -sS -i http://127.0.0.1:18790/viewer/revenue/human-decision-gate \
  -H 'Content-Type: application/json' \
  -d '{"decision_id":"rev_decision_live_20260519084700","decision_type":"closed_channel_send","subject_id":"rev_draft_live_20260519084700","description":"Review whether the draft may be sent later. This call must not send anything externally.","created_at":"2026-05-19T08:47:05Z"}'
curl -sS -i http://127.0.0.1:18790/viewer/revenue/human-decision-gate/review \
  -H 'Content-Type: application/json' \
  -d '{"decision_id":"rev_decision_live_20260519084700","approval_status":"approved"}'
curl -sS http://127.0.0.1:18790/viewer/revenue?limit=20 | \
  rg 'rev_draft_live_20260519084700|rev_decision_live_20260519084700|pending_decision_count|external_actions_applied|external_send_applied'
curl -sS -i http://127.0.0.1:18790/viewer/revenue/channel-drafts/external-send-apply \
  -H 'Content-Type: application/json' \
  -d '{"apply_id":"rev_apply_live_20260519092100","draft_id":"rev_draft_live_20260519084700","decision_id":"rev_decision_live_20260519084700","destination":"manual-review-only@example.invalid","human_approved":true}'
curl -sS -i http://127.0.0.1:18790/viewer/revenue/channel-drafts/external-send-apply \
  -H 'Content-Type: application/json' \
  -d '{"apply_id":"rev_apply_live_no_approval_20260519092100","draft_id":"rev_draft_live_20260519084700","decision_id":"rev_decision_live_20260519084700"}'
curl -sS http://127.0.0.1:18790/viewer/revenue?limit=50 | \
  rg 'rev_apply_live_20260519092100|rev_apply_live_no_approval_20260519092100|external_send_apply_count|external_actions_applied'
GOCACHE=/tmp/picoclaw-gocache PICOCLAW_LIVE_E2E=1 go test -tags=e2e ./test/e2e \
  -run TestE2E_RevenueExternalSendClientRequiresApprovalAndAuditsBlockedApply -count=1 -v
```

判定:

- channel draft create は 201。入力で `external_send_applied=true` を渡しても、保存結果は `external_send_applied=false`。
- channel draft client は malformed draft-only response を成功扱いしない。draft_id / channel、`external_actions_applied=false`、`draft.external_send_applied=false`、Human approval requirement、`approval_status=pending` が一致しない場合は error になる。
- daily routine client は malformed draft report response を成功扱いしない。report_id / workstream_id / date、`status=draft_report`、`external_actions_applied=false`、`report.external_send_applied=false`、Human approval requirement が一致しない場合は error になる。
- Human Decision Gate は `closed_channel_send` を `needs_review` / `pending` として保存し、review API で `approved` に更新した。
- JSONL store の status view は修正前に same `decision_id` の pending / approved を両方返していたため、SQLite store と同じ current view になるよう `ListHumanDecisionGateRecords` を最新 state per decision に集約した。
- 修正後の live service では `/viewer/revenue?limit=20` が `rev_decision_live_20260519084700` を approved 1件だけ返し、`pending_decision_count=0`、`external_actions_applied=false`。
- `/viewer/revenue/channel-drafts/external-send-apply` は、承認済み `closed_channel_send` decision と `human_approved=true` を確認した上で audit record を保存する。live service では外部 channel adapter 未接続のため `apply_status=blocked`、`send_result=not_sent`、`external_send_applied=false`、`post_send_verified=false` として 202 を返した。
- sent 成功 audit は `post_send_evidence` がない場合に domain validation で拒否される。blocked audit を送信済み / post-send verified 扱いにしない。
- 外部 channel adapter 未接続時の blocked audit は、request に `channel_adapter` が指定されても `channel_adapter=unconfigured` として保存することを local handler test で確認した。
- external send client は malformed success response を成功扱いしない。response record の `apply_id` / `draft_id` / `decision_id` が request と一致しない場合、top-level / record の送信・検証状態が矛盾する場合、または `post_send_evidence` がない sent success は error になる。
- Human Decision Gate client は API が 2xx を返しても、decision_id / decision_type / approval_status / gate_status / requires_approval が一致しない malformed response を direct client success として返さない。
- `pkg/rencrowclient.EvaluateRevenueHumanDecision` / `ReviewRevenueHumanDecision` / `CreateRevenueDailyRoutineReport` / `CreateRevenueChannelDraft` / `ApplyRevenueExternalSend` は必須 decision / draft / apply / channel / body などが欠けた request を送信前に拒否する。2026-05-20 00:54 UTC 時点では `ApplyRevenueExternalSend` の `human_approved=false` も client 側で拒否する。
- 2026-05-20 01:43 UTC に `pkg/rencrowclient.ApplyRevenueExternalSend` / `RevenueStatus` は external send apply response / current view の `human_approved=true`、`approval_status=approved`、未送信時の top-level `human_approval_required_for_retry=true` を検証する local client test を追加した。これは Human approval と approved audit が揃わない external send record を実外部送信証跡として扱わないための境界確認であり、実外部送信 E2E の代替ではない。
- `human_approved` なしの同 endpoint は 403 を返し、`rev_apply_live_no_approval_20260519092100` は status API に保存されないことを確認した。
- `TestE2E_RevenueExternalSendClientRequiresApprovalAndAuditsBlockedApply` は `pkg/rencrowclient` 経由で同 flow を live service に対して実行し、pass した。

扱い:

- draft-only、Human Decision Gate review current view、承認後 apply audit / blocked result 記録は部分 pass。
- Viewer Ops は Channel Drafts summary に `draft-only` 件数と `external send requires human approval: yes` を表示し、draft-only 台帳を送信済み成功として扱わない visible state を確認済み。
- 実外部チャネル adapter による送信、送信成功結果、post-send verification は未実装 / 未実施として残す。

#### 4.0.6.1 2026-05-19 15:20 UTC Revenue external send adapter blocked recheck

実行済み:

```bash
curl -sS -m 5 'http://127.0.0.1:18790/viewer/revenue?limit=10'
sed -n '641,731p' internal/adapter/viewer/revenue_handler.go
sed -n '154,205p' internal/domain/revenue/validation.go
rg -n 'external-send|ExternalSend|external_send|channel adapter|channel_adapter|not_sent|post_send|ApplyRevenueExternalSend' \
  internal/domain/revenue internal/adapter/viewer pkg/rencrowclient
```

判定:

- live `/viewer/revenue?limit=10` の channel drafts は `external_send_applied=false`、summary は `external_actions_applied=false`。
- live external send apply records は `apply_status=blocked`、`send_result=not_sent`、`failure_reason="external channel adapter is not configured"`、`channel_adapter=unconfigured`、`post_send_verified=false`、`external_send_applied=false`。
- 2026-05-19 17:47 UTC に `/viewer/revenue?limit=3` が top-level readiness として `external_channel_adapter=unconfigured`、`external_channel_adapter_configured=false`、`human_approval_required_for_external_send=true` を返すことを確認した。`TestE2E_RevenueExternalSendClientRequiresApprovalAndAuditsBlockedApply` は raw HTTP decode ではなく `pkg/rencrowclient.RevenueStatus` 経由で同 current view を検証して pass。
- handler は Human approval と approved `closed_channel_send` decision を確認しても、外部送信 adapter を呼ばず blocked audit を保存する。
- domain validation は sent / applied / post-send verified / evidence の整合性を要求する。blocked audit は送信成功ではない。
- 実外部送信 E2E は、送信先 adapter、Human approval、送信結果、post-send evidence、失敗時 retry / rollback 方針が揃うまで blocked とする。

### 4.1 live service / Viewer 基本

目的:

- live service が起動し、Viewer と runtime config が現行 binary / live config を反映していることを確認する。

確認コマンド:

```bash
curl -fsS http://127.0.0.1:18790/health
curl -fsS http://127.0.0.1:18790/viewer/runtime-config
PICOCLAW_LIVE_E2E=1 \
GOCACHE=/tmp/picoclaw-gocache \
go test -count=1 -tags=e2e ./test/e2e \
  -run 'TestE2E_Phase25Live(RuntimeHealth|ViewerRuntimeConfigClient)' -v
```

成功条件:

- `/health` が ok を返す。
- `/viewer/runtime-config` が live runtime の endpoint / feature 状態を返し、`runtime_readiness` fields を `pkg/rencrowclient.RuntimeConfig` で検証できる。
- repo example と live runtime config の差が確認できる場合、live runtime を優先して記録している。

失敗扱い:

- `/health` だけで Viewer / runtime config を確認済みにする。
- 古い binary / 古い service 状態のログを根拠にする。

### 4.2 Viewer browser session

目的:

- Viewer の実ブラウザ操作で、入力、送信、表示、event log、history が 1 セッションとして成立することを確認する。

確認コマンド:

```bash
PICOCLAW_BROWSER_E2E=1 \
PICOCLAW_LIVE_BASE_URL=http://127.0.0.1:18790 \
GOCACHE=/tmp/picoclaw-gocache \
go test -count=1 -tags=e2e ./test/e2e \
  -run TestE2E_Phase25BrowserViewerSessionContract -v
```

成功条件:

- Chat タブで `#micBtn` が visible。
- IdleChat タブへ切り替えると `#idleStart` が visible。
- `/viewer/send` の response が Viewer 表示と一致する。
- event log / history / session state が同一 session として追える。

失敗扱い:

- DOM に要素が存在するだけで visible / clickable を確認しない。
- 現 UI がタブ式であることを無視し、別タブの要素を初期表示で要求する。
- 送信 response と表示本文がずれている。

### 4.3 Source Registry warning 表示

目的:

- Source Registry 由来テキストの prompt injection warning が metadata と Viewer/API 表示に残り、prompt / memory 本文と混ざらないことを確認する。

確認対象:

- `/viewer/source-registry` 系 API
- Viewer Memory / Source Registry UI
- `security_warnings` metadata
- staging / fetch result の warning 件数

成功条件:

- warning metadata が run API response に含まれる。
- Viewer で warning 件数または badge が確認できる。
- warning は reject とは別に扱われる。
- warning 付き item が無審査で promoted されない。

失敗扱い:

- warning を本文に混ぜる。
- warning 付き source を通常 memory として無審査注入する。
- warning を fallback 表示で隠す。

### 4.4 Slack / Discord / Telegram file payload 実 API

目的:

- LINE 以外の外部チャネル file payload が、実 API event から共通 attachment pipeline へ流れることを確認する。

確認対象:

| チャネル | payload | 成功条件 |
| --- | --- | --- |
| Slack | `files[]`, `url_private_download` | download した file が `IncomingFile` / `Attachment` contract へ正規化される |
| Discord | `attachments` | attachment URL / filename / MIME / size が共通 pipeline へ渡る |
| Telegram | `document`, `photo`, `file_id -> getFile -> download` | Telegram file が共通 pipeline へ渡る |

共通確認:

- session_id が channel event と attachment event で追える。
- download 失敗が通常 chat 成功として隠れない。
- MIME 不許可が rejection として返る。
- size 超過が rejection として返る。
- prompt injection warning が metadata として残る。

失敗扱い:

- stub / unit test だけで実 API 済みにする。
- download 失敗を「添付なし通常メッセージ」として処理する。
- MIME 不許可や size 超過を fallback 応答で隠す。

2026-05-19 15:45 UTC に adapter-level failure boundary として、Slack / Discord / Telegram の attachment download failure が 502 で止まり、orchestrator に進まないことを local test で確認した。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/channels/slack ./internal/adapter/channels/discord ./internal/adapter/channels/telegram -count=1 -v` は pass。これは実 API event、token、webhook、channel 環境を使う E2E の完了扱いにはしない。

2026-05-20 09:35 UTC に adapter-level failure boundary を追加補強し、Slack / Discord / Telegram の attachment download failure が webhook 502 response に upstream body を保持することを local focused test で確認した。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/channels/slack ./internal/adapter/channels/discord ./internal/adapter/channels/telegram -run 'TestAdapter_ServeHTTP_(File|Attachment|Document)DownloadFailureDoesNotFallbackToChat' -count=1 -v` は pass。これは download failure の証跡を status だけで失わないための境界確認であり、実 API event、token、webhook、channel 環境を使う E2E の完了扱いにはしない。

2026-05-20 09:40 UTC に adapter-level failure boundary を追加補強し、Slack / Discord / Telegram の Send / Probe failure が non-2xx response body を error に保持することを local focused test で確認した。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/channels/slack ./internal/adapter/channels/discord ./internal/adapter/channels/telegram -run TestAdapter_SendAndProbeFailuresIncludeResponseBody -count=1 -v` は pass。これは外部 API 送信 / probe failure の証跡を status だけで失わないための境界確認であり、実 API event、token、webhook、channel 環境を使う E2E の完了扱いにはしない。

2026-05-20 09:45 UTC に LINE media download failure boundary を追加補強し、`MediaDownloader.DownloadContent` が LINE content API の non-2xx response body を error に保持することを local focused test で確認した。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/line -run TestMediaDownloader_DownloadContent_APIErrorIncludesResponseBody -count=1 -v` は pass。これは LINE media download failure の証跡を status だけで失わないための境界確認であり、実 LINE media event や外部チャネル file payload E2E の完了扱いにはしない。

2026-05-20 09:50 UTC に Telegram `getFile` failure boundary を追加補強し、file download 前段の `getFile` non-2xx response body を webhook 502 response に保持することを local focused test で確認した。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/channels/telegram -run TestAdapter_ServeHTTP_GetFileFailureDoesNotFallbackToChat -count=1 -v` は pass。これは Telegram file metadata failure を status だけで失わず、添付なし通常 chat success に fallback しないための境界確認であり、実 Telegram API event E2E の完了扱いにはしない。

2026-05-19 15:47 UTC に common attachment store の unsupported MIME / size rejection と、Slack / Discord / Telegram adapter が `AttachmentSaver.SaveAll` rejection を 502 で停止して orchestrator へ進めないことを local test で確認した。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/application/attachment ./internal/adapter/channels/slack ./internal/adapter/channels/discord ./internal/adapter/channels/telegram -count=1 -v` は pass。これは MIME 不許可 / size 超過を fallback success にしない境界確認であり、実 API event E2E の完了扱いにはしない。

2026-05-19 15:49 UTC に Slack / Discord / Telegram adapter の successful attachment path を real `internal/application/attachment.Store` 経由に寄せ、prompt injection warning が `security_warnings` metadata として orchestrator request の `Attachments` に残ることを local test で確認した。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/adapter/channels/slack ./internal/adapter/channels/discord ./internal/adapter/channels/telegram -count=1 -v` は pass。これは warning metadata を fallback 表示で隠さない境界確認であり、実 API event E2E の完了扱いにはしない。

2026-05-19 18:03 UTC に `/viewer/runtime-config` の `runtime_readiness` へ Slack / Discord / Telegram の `*_webhook_registered` と `*_file_payload_pipeline` を追加した。live service を `make install` / restart 後に確認し、Slack / Discord / Telegram は credentials / webhook / file pipeline がすべて false。`TestE2E_Phase25LiveViewerRuntimeConfigClient` は pass するが、これは外部チャネル file payload 実 API E2E の success ではなく、実 token / webhook / event 未準備による `blocked` 証跡である。`pkg/rencrowclient.RuntimeConfig` は file pipeline が true なのに webhook route が false の response を malformed として拒否する。

2026-05-19 18:10 UTC に `TestE2E_Phase25BrowserViewerSessionContract` へ Slack / Discord / Telegram の visible readiness text を追加し、live Viewer に対して pass。Ops タブ上で `credentials:missing` / `webhook:missing` / `file:missing` が見えることを確認した。これは DOM attached だけでなく visible state の確認であるが、実 API event / token / webhook を使う file payload E2E の完了扱いにはしない。

2026-05-19 23:08 UTC に Viewer Ops の Slack / Discord / Telegram readiness card へ `blocked: real external API file event E2E not verified` を明示した。Node Viewer contract と live browser E2E はこの文言を確認するが、実 token / webhook / 外部 API event / file payload E2E は未達のまま扱う。

2026-05-19 18:10 UTC に Viewer Ops の runtime readiness card が `/viewer/debug/system` の audio snapshot も表示するようにした。live `/viewer/debug/system` は `stt_ok=false` / `tts_live_ok=false` / `tts_ready_ok=false` と timeout `last_error` を返し、Ops タブでは STT `health:missing`、TTS `live:missing` / `ready:missing`、`blocked:` detail が visible state として確認できる。`PICOCLAW_BROWSER_E2E=1 PICOCLAW_LIVE_E2E=1 ... TestE2E_Phase25BrowserViewerSessionContract` は pass。これは blocked 状態の表示確認であり、browser microphone STT、TTS playback、lip sync の成功扱いにはしない。

2026-05-19 23:02 UTC に Viewer Ops の STT / TTS readiness card へ `blocked: real microphone STT E2E not verified` と `blocked: browser audio playback/lip sync E2E not verified` を明示した。Node Viewer contract と live browser E2E はこの文言を確認するが、実 mic / 実 audio device / lip sync の成功条件は未達のまま扱う。

2026-05-20 08:41 UTC に Viewer STT microphone start / artifact persistence failure 境界を補強した。`getUserMedia()` reject と STT log/WAV/autotest 保存 reject を toast / console-only にせず、`debugSttSession` と session badge に `STT microphone start unavailable: ...` / `STT artifact persistence unavailable: ...` を保持する Node contract test を追加し、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は 67件 pass。これは mic 権限拒否や保存失敗を実 mic STT 成功、保存済み証跡、STT provider readiness 成功として扱わないための local 表示境界であり、実 browser microphone STT E2E の代替ではない。

2026-05-20 08:45 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=timeline` を Playwright Chromium で開き、`getUserMedia()` reject を再現した。session badge と `debugSttSession` に `STT microphone start unavailable: permission denied by test` が表示されることを確認した。スクリーンショットは `tmp/viewer-stt-microphone-start-failure-boundary.png`。これは browser microphone start failure の visible-state 境界確認であり、実 browser microphone STT E2E の代替ではない。

2026-05-20 08:48 UTC に Viewer STT WebSocket message / transport failure 境界を補強した。STT server message が `type=error`、JSON parse failure、WebSocket `onerror` の場合は toast / console-only にせず、`debugSttSession` と session badge に `STT recognition unavailable: ...` / `STT message parse unavailable: ...` / `STT websocket unavailable: ...` を保持する Node contract test を追加し、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は 68件 pass。これは STT server / stream failure を実 mic STT 成功や provider readiness 成功として扱わないための local 表示境界であり、実 browser microphone STT E2E の代替ではない。

2026-05-20 08:50 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=timeline` を Playwright Chromium で開き、fake WebSocket から `type=error` を流した。session badge と `debugSttSession` に `STT recognition unavailable: provider timeout live` が表示されることを確認した。スクリーンショットは `tmp/viewer-stt-websocket-message-failure-boundary.png`。これは STT websocket server error の visible-state 境界確認であり、実 browser microphone STT E2E の代替ではない。

2026-05-20 09:50 UTC に audio router HTTP/SSE failure 境界を補強した。`HTTPDownloader.Download` と `SSEClient` は audio download / event stream 接続の non-2xx response body を error に保持する。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/infrastructure/audiorouter -run 'Test(HTTPDownloaderDownload_Non2xxIncludesResponseBody|SSEClientRun_NonOKIncludesResponseBody)' -count=1 -v` は pass。これは audio object expiration / stream unavailable の証跡を status だけで失わないための local 境界確認であり、実 browser audio playback / lip sync E2E の完了扱いにはしない。

2026-05-20 08:54 UTC に Viewer Home send failure 境界を補強した。Home / Daily Desk の送信で `sendViewerMessage()` が reject した場合は toast / console-only にせず、`homeStatusCard` に `Home send unavailable: ...` を保持する Node contract test を追加し、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は 69件 pass。これは Home 入口の送信失敗を通常 chat 成功や fallback 成功として扱わないための local 表示境界であり、live Viewer send E2E の代替ではない。

2026-05-20 08:56 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=home` を Playwright Chromium で開き、route fulfill により `/viewer/send` 502 を再現した。Home の `今日の状態` card に `Home send unavailable: HTTP 502: viewer send route unavailable live` が表示されることを確認した。スクリーンショットは `tmp/viewer-home-send-failure-boundary.png`。これは Home send route failure の visible-state 境界確認であり、live Viewer send 成功 E2E の代替ではない。

2026-05-20 09:01 UTC に Viewer evidence detail の JSON / summary copy failure 境界を補強した。clipboard write が reject した場合は `console.error` のみで終わらせず、copy button に `Evidence JSON copy unavailable: ...` / `Evidence summary copy unavailable: ...` を保持する Node contract test を追加し、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は 70件 pass。これは証跡コピー失敗を共有済み証跡や実行証跡取得成功として扱わないための local 表示境界であり、live evidence copy E2E の代替ではない。

2026-05-20 09:05 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=jobs` を Playwright Chromium で開き、fake evidence detail と clipboard write reject を再現した。Copy JSON / Copy Summary button に `Evidence JSON copy unavailable: clipboard denied live` / `Evidence summary copy unavailable: clipboard denied live` が表示されることを確認した。スクリーンショットは `tmp/viewer-evidence-copy-failure-boundary.png`。これは evidence copy failure の visible-state 境界確認であり、実行証跡取得成功や共有成功の代替ではない。

2026-05-20 09:07 UTC に Viewer generic copy payload failure 境界を補強した。Reports / IdleChat / System などが共有する `copyTextPayload()` で clipboard write が reject した場合は toast / console-only にせず、押下した copy button に `Copy unavailable: ...` を保持する Node contract test を追加し、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は 71件 pass。これはコピー失敗を共有済み証跡や transcript 取得成功として扱わないための local 表示境界であり、各画面の live copy E2E の代替ではない。

2026-05-20 09:09 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=reports` を Playwright Chromium で開き、clipboard write reject を再現した。Report Copy Summary button に `Copy unavailable: clipboard denied live` が表示され、button title にも同じ失敗理由が残ることを確認した。スクリーンショットは `tmp/viewer-generic-copy-failure-boundary.png`。これは generic copy failure の visible-state 境界確認であり、各画面の transcript / report 共有成功の代替ではない。

2026-05-20 09:12 UTC に Viewer unsupported attachment failure 境界を補強した。Viewer 添付で不許可 MIME / 拡張子の file を選んだ場合は toast-only にせず、attachment tray に `Attachment unavailable: unsupported file type: ...` を保持する Node contract test を追加し、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は 72件 pass。これは不許可添付を添付成功や通常送信成功として扱わないための local 表示境界であり、live attachment upload E2E の代替ではない。

2026-05-20 09:14 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=timeline` を Playwright Chromium で開き、不許可添付 `payload.exe` を input に設定した。attachment tray に `Attachment unavailable: unsupported file type: payload.exe` が表示されることを確認した。スクリーンショットは `tmp/viewer-unsupported-attachment-failure-boundary.png`。これは unsupported attachment failure の visible-state 境界確認であり、live attachment upload 成功や通常送信成功の代替ではない。

2026-05-20 09:16 UTC に Viewer message copy failure 境界を補強した。通常 timeline / IdleChat message の `copyMsg()` で clipboard write が reject した場合は unhandled rejection や console-only にせず、押下した Copy button に `Copy unavailable: ...` を保持する Node contract test を追加し、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は 73件 pass。これは message copy 失敗を transcript / message 共有成功として扱わないための local 表示境界であり、live message copy E2E の代替ではない。

2026-05-20 09:18 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=timeline` を Playwright Chromium で開き、message Copy button の clipboard write reject を再現した。Copy button に `Copy unavailable: clipboard denied live` が表示され、button title にも同じ失敗理由が残ることを確認した。スクリーンショットは `tmp/viewer-message-copy-failure-boundary.png`。これは message copy failure の visible-state 境界確認であり、timeline / IdleChat message 共有成功の代替ではない。

2026-05-20 09:21 UTC に Viewer LLM Ops readiness failure 境界を補強した。alias route 送信前の `/viewer/llm-ops/health` / `status` / `stop` / `start` が non-2xx の場合、response body を `llm ops ... failed: HTTP <status>: ...` として保持する Node contract test を追加し、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は 74件 pass。これは LLM Ops readiness route failure を alias 送信成功や fallback 成功として扱わないための local 表示境界であり、live LLM start / stop 成功 E2E の代替ではない。

2026-05-20 09:28 UTC に Viewer Timeline の local send failure 表示境界を補強した。`Viewer send unavailable: ...` の synthetic `agent.response` は TTS 同期話者 `mio` 由来でも Timeline に表示し、通常の TTS 同期応答は従来どおり本文表示側に重複させない Node contract test を追加し、`node internal/adapter/viewer/viewer_memory_panel.test.mjs` は 75件 pass。これは LLM Ops readiness failure や send route failure を silent drop / fallback 成功として扱わないための local 表示境界であり、live LLM start / stop 成功 E2E の代替ではない。

2026-05-20 09:31 UTC に現ソースを `make install` して `picoclaw.service` を再起動後、配信中 `/viewer?tab=timeline` を Playwright Chromium で開き、route fulfill により `/viewer/llm-ops/health` 503 を再現した。Timeline に `Viewer send unavailable: llm ops health failed: HTTP 503: llm ops health unavailable live` が表示されることを確認した。スクリーンショットは `tmp/viewer-llm-ops-readiness-failure-boundary.png`。これは LLM Ops readiness failure の visible-state 境界確認であり、live LLM start / stop 成功 E2E の代替ではない。

### 4.0.3 2026-05-19 Sandbox promotion apply / rollback result

2026-05-19 23:09 UTC に一時 sandbox config と隔離 apply root `/tmp/picoclaw-sandbox-promotion-apply-root` を使い、`PICOCLAW_LIVE_E2E=1 PICOCLAW_LIVE_SANDBOX_E2E=1 ... TestE2E_SandboxPromotionApplyAndRollbackWithHumanApproval` を live service に対して実行して pass。`promo_sandbox_apply_rollback_20260519230949.441009237` は Human approval granted、ExternalControl allow、unified diff apply、post-apply verification command、`promotion_applied` gate log、rollback API、`rollback_executed` gate log、completed `post_apply_verification` / `rollback_execution` artifact まで確認した。証跡取得後は `~/.picoclaw/config.yaml` を復元し、通常 config の `/viewer/runtime-config` は `sandbox_enabled=false`、`/viewer/sandbox?limit=1` は 503 `sandbox store unavailable` に戻した。

### 4.5 Wild / distributed runtime

目的:

- `RouteWILD` が実機分散接続で fallback なしに Wild agent 経由で応答することを確認する。

確認対象:

- `cmd/picoclaw/runtime_distributed_mode.go`
- `internal/application/orchestrator/distributed_orchestrator_routes.go`
- `internal/infrastructure/transport`
- live / temporary distributed config

確認手順:

1. live service とは別に、一時 distributed config を用意する。
2. local transport または SSH transport で distributed mode を起動する。
3. `/wild` または Wild 判定 message を送信する。
4. route evidence を確認する。
5. `job_id` / `session_id` / response / event log を照合する。

成功条件:

- `routing.decision route=WILD` が記録される。
- `agent.response wild -> mio` 相当の evidence がある。
- `job_id` と `session_id` が同一フローとして追える。
- local / CHAT fallback に落ちていない。

失敗扱い:

- local fallback で応答したものを Wild 成功にする。
- `RouteWILD` の code path 存在だけで完了扱いにする。
- route evidence なしで response だけを見る。

2026-05-19 15:43 UTC に local distributed の `RouteWILD` test を強化し、`agent.response wild -> mio` event の `job_id` / `session_id` が `ProcessMessageResponse` と一致することを確認した。`GOCACHE=/tmp/picoclaw-gocache go test ./internal/application/orchestrator -run TestDistributedOrchestrator_ProcessMessage_WildRoute -count=1 -v` は pass。これは local transport / test runtime の境界確認であり、SSH transport / 複数マシン E2E の完了扱いにはしない。

2026-05-19 18:14 UTC に `/viewer/runtime-config` の `runtime_readiness` へ distributed transport readiness を追加した。live では `distributed_enabled=false`、`distributed_transports_present=true`、`distributed_ssh_configured=true`、`distributed_ssh_connected=false`、`distributed_local_transport=false`。`pkg/rencrowclient.RuntimeConfig` は connected SSH transport と distributed enabled / ssh configured の矛盾を malformed response として拒否する。`TestE2E_Phase25LiveViewerRuntimeConfigClient` と `TestE2E_Phase25BrowserViewerSessionContract` は pass し、Ops タブで `Distributed` / `enabled:missing` / `ssh-connected:missing` / `blocked: distributed disabled` が見えることを確認した。これは blocked 理由の可視化であり、Wild SSH / 複数マシン transport E2E の完了扱いにはしない。

### 4.6 分散全経路

目的:

- Wild 以外の Worker / Coder / Heavy などの分散経路が、transport / job / response / event log まで追えることを確認する。

成功条件:

- 対象 route ごとに dispatch evidence が残る。
- transport 接続、job id、worker response、Chat 返却が一致する。
- fallback route に落ちた場合は fail として記録する。

失敗扱い:

- local 実行に落ちたのに distributed 成功扱いにする。
- fallback を degraded success として扱う。

### 4.7 STT browser live

目的:

- 実ブラウザ mic 入力が通常 chat にだけ送信され、IdleChat へ流れないことを確認する。

確認対象:

- HTTPS / browser permission
- mic capture
- trailing silence
- STT provider log
- final text
- `/viewer/send` または通常 chat input

成功条件:

- mic input から final text が得られる。
- final text が通常 chat に送信される。
- IdleChat へ STT input が流れない。
- busy policy が `queue_latest` または `reject` として記録される。

失敗扱い:

- browser permission なしの mock だけで完了扱いにする。
- transcribing のまま終了しない。
- IdleChat に音声入力が流れる。

### 4.8 TTS / audio / lipsync browser live

目的:

- TTS playback と lipsync trigger が、表示本文ではなく audio chunk / audio event を契機に動くことを確認する。

成功条件:

- TTS provider response と audio event が一致する。
- browser playback が実行される。
- lipsync は audio chunk を契機に動く。
- 表示本文の更新だけで lipsync 成功扱いにしない。

失敗扱い:

- DOM 存在だけで playback 成功扱いにする。
- 表示本文を lipsync の唯一根拠にする。
- audio provider 失敗を無音 fallback で成功扱いにする。

## 5. 結果記録テンプレート

```markdown
## E2E確認結果: {項目名}

- 日時:
- 実行者:
- 環境:
- config:
- コマンド / 操作:
- route / session / job:
- 証跡:
- 結果: pass / fail / skipped / blocked
- skip / fail 理由:
- 更新した docs:
- 次アクション:
```

## 6. docs 反映ルール

確認結果に応じて以下を更新する。

| 確認結果 | 更新先 |
| --- | --- |
| E2E が通った | `13_実装項目インベントリ.md`, `17_E2E残課題.md`, 必要なら `31_未実装項目実装仕様.md` |
| skip / blocked | `17_E2E残課題.md` に環境未準備理由を残す |
| fail | `17_E2E残課題.md` に失敗証跡と次アクションを残す |
| 実装不足が判明 | `31_未実装項目実装仕様.md` の該当 Phase に戻す |

## 7. 完了条件

このチェックリスト自体の完了条件は、全項目が `pass` になることではない。

完了条件は以下である。

- E2E / runtime 要確認項目ごとに、確認済み / skip / blocked / fail が分類されている。
- skip と fail が成功扱いされていない。
- fallback が成功扱いされていない。
- 証跡、config、route、session、log が残っている。
- `13_実装項目インベントリ.md` と `17_E2E残課題.md` の状態が実態と一致している。
