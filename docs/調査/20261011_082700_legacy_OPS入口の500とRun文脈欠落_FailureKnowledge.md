# legacy OPS入口の500とRun文脈欠落

記録日: 2026-10-11 (JST)。対象: `POST /v1/agent/ops` のnative coding profile無効時(legacy)の分岐
(`cmd/rencrow/runtime_agent_ops_legacy.go`)と、Lead Agent runの台帳記録 (`internal/application/orchestrator/superagent_runtime.go`)。

## 概要

`rencrowctl ops` (`POST /v1/agent/ops`) が、本番設定 (native profile無効、SuperAgent台帳有効) でHTTP 500
`{"error":"execution_failed"}` を返し続け、原因がlogに残らなかった。

## 発生条件

- native coding profileが無効 (本番の既定。`native_harness` 設定なし)。
- SuperAgent台帳が有効で、Subagent Managerにrecorderが設定されている。
- Shiroが `SubagentManager` を持つ (Subagent有効)。Shiroはnative coding / Codex work pathでなければ、毎回
  `RunSync` へ入る。モデルがspawnを選んだ時だけの間欠ではなく、条件を満たせば毎回起きる。

## 事実

- legacy分岐は `NewTaskID()` を採番するだけで、Task ownerにTaskもRunも作らなかった。応答の `task_id` はTask ownerに存在しないIDだった。
- `subagent.WithSuperAgentRuntime` を付けずに `ShiroAgent.Execute` を呼んでいた。Managerはrecorder設定時にruntime contextが無いと
  `superagent runtime context is required when recorder is configured` を返す (この返却はlogを出さない)。
- handlerは `Execute` のerrorを捨てて500 `execution_failed` にしており、logも残さなかった。journalには
  `[Subagent] start agent=shiro instruction_len=N` が1行出るだけで、その後の `error` / `complete` が続かなかった。
- 再現: 実 `ShiroAgent` + 実 `subagent.Manager` (本番と同じ `SetSuperAgentRecorder`) + 実handlerで、recorderありは500、
  recorderなし (対照) は200。recorderの有無が唯一の分岐だった。既存の `TestAgentOps*` はfake executorを使うため再現できなかった。
- native profile有効時は、Task ownerの `AdmitAcceptedOPS` でTask/Runを作り、ShiroはSubagentを通らない
  (`executeNativeCoding`) ため、この500を持たない。

## 結論 (Failure Knowledge)

- **Failure**: legacy OPS入口が常に500 `execution_failed` を返し、原因がlogに残らなかった。
- **Problem**: recorder設定下で、SuperAgent runtime contextを持たずにSubagentを呼んだ。さらに実行主体のTask/Runが
  Task ownerに存在しなかった。
- **Cause**: Shiroを実行する経路ごとにTask/Run作成とruntime context付与が手書きで、legacy分岐だけが欠けた。
  executorのerrorを握りつぶす実装が、欠落を本番ログから辿れなくした。fake executorの試験は、実Shiro + 実Manager +
  recorderの組合せを通らなかった。
- **Lesson**: Subagentを保持するExecutorを呼ぶ入口は、必ずTask ownerのTask/Runを持ち、台帳が有効ならLead Agent runの
  記録とruntime contextを持つ。executorのerrorは握りつぶさず、識別子付きの1行にして残す。
- **Invariant**:
  1. Shiro executorを呼ぶOPS入口の関数は、呼出し前にTask ownerのTask/Run identityをcontextへ束縛する。
  2. 台帳が有効なら、Shiro実行前にLead Agent runを記録し、同じTask/Runのruntime contextを渡す。Subagentの記録と
     Lead Agent runは同じ `task_id` / `run_id` / `trace_id` に相関する。
  3. 受け付けたRunは、成功・失敗・client cancel・panic・台帳書込み失敗のいずれでも終端する (operations実行枠を残さない)。
     終端書込みはrequest contextから切り離した60秒の予算で行う。
  4. 実行枠が空いていないときは、作成したTaskをfailedで閉じ、executorを呼ばずに503 `runtime_unavailable` を返す。
  5. request messageはTask、台帳 (Goalは固定文)、logへ保存しない。
- **Enforcement**:
  - `RecordLeadAgentRunStarted/Finished` を値型 `LeadAgentRunInput` の公開APIにして、入口ごとの手書きと偽の
    `ProcessMessageRequest` の構築を不要にした (orchestrator 2経路と本入口の3 caller)。
  - `TestAgentOpsIngressBindsTaskRunIdentityBeforeCallingExecutor` (AST) が、executorを呼ぶOPS入口関数が
    `WithIdentity` を持つことを強制する。新しい入口の追加・削除では期待集合の更新が必要になる。
  - 本番配線 (`withAgentOpsLeadRunRecorder(deps.superAgentStore)`) の存在を `TestAgentOpsProductionWiringSharesInitializedNativeCodingRuntime` が確認する。
- **Tests**: `cmd/rencrow/runtime_agent_ops_legacy_test.go`
  (実Shiro + 実Manager + recorder + 実Task ownerで200、Task/Run終端、`lead_agent.started` -> `subagent.started` ->
  `subagent.completed` -> `lead_agent.completed` が同じtask/run/traceに相関、recorderなしの対照、実行失敗のlogとTask/Run失敗、
  空出力、cancel、実行枠満杯の503とqueued残りなし、終端書込み失敗、Lead Agent runの開始/終了記録失敗、executorのpanic)、
  `internal/application/orchestrator/superagent_runtime_test.go` (値型の記録内容・Resume checkpoint・不正identity)。

## 手順 (再現と修正)

1. 再現: 実Shiro + 実Manager + recorderで500、recorderなしで200を確認した (原因文言は上記のとおり)。
2. 修正前に新規試験を追加し、期待した理由 (500 `execution_failed`、Taskが作られない、実行枠の拒否が働かない、台帳記録が無い) で失敗することを確認した。
3. Lead Agent run記録の入力を値型へ切り出す `refactor` を先に入れ、orchestratorの既存試験が挙動不変で通ることを確認した。
4. legacy分岐を、Task ownerのTask/Run、Lead Agent run、runtime context、終端の順に置き換えた。

## 運用上の影響

- 同時に実行できるlegacy OPS requestは、operations実行枠 (`DestructiveTasks=1`) の数までになった。以前は上限がなかった。
  枠が空いていないrequestは503 `runtime_unavailable` になる。別障害で枠を占有するTaskがあると、修正後のopsは503になりうる
  (隠れた500より観測しやすい)。
- 応答の `task_id` は、Task ownerに実在するTask IDになった。応答のキーは変わらない。

## 関連ファイル

- `cmd/rencrow/runtime_agent_ops.go` (分岐、handler option)
- `cmd/rencrow/runtime_agent_ops_legacy.go` (legacy分岐)
- `cmd/rencrow/runtime_dependencies.go` (台帳の注入)
- `internal/application/orchestrator/superagent_runtime.go`
- `internal/application/subagent/manager.go` (recorder設定時のruntime context必須の不変条件。変更なし)

## 未確認

- 本番での修正後の実機確認 (配備は別作業)。
- Heartbeat workerの `workerAgent.Execute` が同種の失敗をするか。Taskは作るが `WithSuperAgentRuntime` を付けず、
  Subagentを持つShiroは無条件で `RunSync` に入るため、台帳有効なら同じ失敗をしうる。ただし本番のjournalでSubagentの開始行が
  観測されたのはOPSの1件だけで、顕在化は確認されていない。別作業でRedを取ってから `RecordLeadAgentRunStarted` を使って直す。
