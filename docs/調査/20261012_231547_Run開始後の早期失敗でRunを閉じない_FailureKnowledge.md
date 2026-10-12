# Run開始後の早期失敗でRunを閉じず、RESEARCH実行枠を占有し続けた

記録日: 2026-10-12 (JST)。対象: Memory Promotion (MP) の1回分を実行する `actionMemoryPromotionRunner.RunOne`
(`cmd/rencrow/memory_promotion_action_runner.go`)。時刻は特記なければUTC。

## 概要

本番で MP が 13:37Z から全停止した。Taskを開始してRunを発行した後、inner の実行に入る前に失敗する経路が
Runを閉じずに return しており、Action記録の無い RESEARCH の `running` Task が1件残った。RESEARCH枠は
`LongResearchTasks=1` なので、以後の MP は `ErrParallelLimit` で延期され続けた。同じ型は 08:44Z にも1件あり、
次の再起動まで数時間詰まった (前の調査の記録)。07:35Z ごろの停止も同じ型の可能性があるが未確認。

## 発生条件

- `RunOne` が `CreateAndStartRun` で Task (RESEARCH, running) と Run を作った後、`WithIdentity` か `CreateAction` が
  失敗する。修正前はどちらも `return err` だけで、Run も Task も閉じなかった。
- 呼び出し元のcontextが終わっていると `CreateAction` は取消・期限で失敗する。Action store の1操作は本番で7〜11秒
  かかるため、`CreateAndStartRun` の後に取消が届く窓が広い。

## 事実 (観測)

- コード (修正前): `RunOne` は3か所 (`WithIdentity`、`CreateAction`、`WithChildBoundActionAttempt`) で
  `return RunResult{}, err` していた。Run を閉じる `complete` は inner を実行した後でしか呼ばれない。
- 本番のjournal (識別子は転記しない): 13:32:25Z に MP が成功。13:37:26Z に最初の
  `Memory ProfilePromotion deferred: execution capacity unavailable` が出て、再試行間隔が 10s、20s、40s、1m20s、
  2m40s、5m、5m と伸びた (13:48Zまで確認)。13:32:25Z から 13:37:26Z の間に MP 由来のERRORは無い。
- 本番storeのコピー (14:31Z、識別子と題名は転記しない) の集計: `running` の Task は RESEARCH・midori の1件だけで、
  Memory Promotion の題名、`started_at` は 13:37:04Z、Run は1つで `running`、以後更新が無い。13:37:26Z の最初の延期は、
  この Task が枠を握った約22秒後にあたる。
- プロセス内では回収されない: `recoverOrphanTaskRunsAfterRestart` は再起動時に、Action を持たない running の Run
  を失敗で終端する。現在のプロセスが書いた Run は writer generation が現行のため対象外。再起動が唯一の回収手段だった。
- 修正前に対して追加した試験は、Task と Run が `running` のまま残ること、閉じる書込みの失敗が返らないことで失敗した。

## 事実と仮説の区別

- 事実: 上のコード経路が Run を開かせたままにする。その結果、枠が埋まる。
- 仮説 (未確認): 実際に失敗させた原因は、idle lease の取消である。`llmBusyTracker.BeginExternal` は、利用者向けの
  LLM呼出しが始まると MP が持つ idle lease の context を取り消す。MP の worker は `context.Canceled` を失敗として
  報告しない (`runtime_memory_promotion.go`) ので、ログに何も残らない。孤児Taskが作られた 13:37:04Z の直前
  (13:37:03Z) に、OPS 要求の ToolLoop が2回目のLLM呼出しを始めている (Toolの実行中は利用者向けのLLM呼出しが途切れ、MP が
  idle lease を得られた可能性がある。MP が lease を持っていたこと自体は、孤児Taskが作られたことから言える)。時刻は合うが、孤児Taskを作った RunOne のエラー文は
  どこにも残っていないので、確定ではない。45秒の tick 期限でも同じ経路になる。

## 結論 (Failure Knowledge)

- **Failure**: MP が 13:37Z から全停止し、再起動まで再開しなかった。
- **Problem**: Run を開始した関数が、開始後の早期失敗で Run を閉じずに return した。Run は実行枠を保持する資源なので、
  閉じない経路は枠の恒久占有になる。
- **Cause**: (1) `CreateAndStartRun` が Task と Run を原子的に作るため、その直後から枠を握る。後続の準備 (識別子の束縛、
  Action作成) が失敗する経路を、閉じる処理の対象にしていなかった。(2) 閉じる処理 `complete` は inner 実行後の1経路にしか
  無く、早期失敗の経路は設計の外だった。(3) 取消は失敗として報告されないので、枠の占有に気付く手掛かりがログに無かった。
  (4) 同じ型のコードが各所にコピーされており、1か所の欠陥が他にも複製されている (下の表)。
- **Lesson**: 資源を取得したら、取得の直後から、すべての失敗経路に解放を結び付ける。取得と解放を別々の関数に置く
  構造 (`CreateAndStartRun` の呼出し元が `complete` を別に持つ) では、準備段階の失敗が解放の対象から漏れる。
  終端の書込みは、呼出し元の取消から切り離し、有限の期限を付ける。
- **Invariant**: Run を開始した関数は、開始後のすべての失敗経路で、その Run (と Task) を終端する。終端の書込みは呼出し元の
  取消から切り離し、有限の期限 (60秒、書込みごとに独立) を持つ。終端の書込みが失敗したら、元の原因と一緒にエラーとして返し、
  握りつぶさない。取消に重なって失敗した場合も、worker は失敗として報告する (取消だけなら報告しない)。
- **Enforcement**:
  - 行動試験: MP の早期失敗の各経路で Run と Task が終端し、枠が解放される。
  - architecture test: Run を開始する関数は台帳に登録済みでなければならない。未登録の新規経路と古くなった登録は失敗する。
    台帳は「閉じている」「呼出し元が閉じる」「既知の未修正」を区別し、未修正を可視に保つ。台帳が保証するのは「新しい開始経路が
    レビューされること」までで、各経路が実際に閉じることは、その経路の行動試験が保証する。
  - 再起動時の補償 (`recoverOrphanTaskRunsAfterRestart`) は残る。これは予防ではなく回収である。
- **Tests**:
  - `cmd/rencrow`: `TestActionMemoryPromotionRunnerClosesRunWhenIdentityBindingFails`、
    `...ClosesRunWhenActionCreationFails`、`...ClosesRunWhenCallerContextEndsDuringActionCreation` (期限と取消の2通り。
    呼出し元のcontextが終わっていても閉じることと、書込みの期限を確認)、`...ReturnsCloseFailureAfterIdentityBindingFails`、
    `...ReturnsBothCausesWhenActionCreationAndCloseFail`、`...MarksRunNotClosedWhenFinalWriteFailsAfterCancellation`。
    `TestMemoryPromotionWorkerReportsCancellationOnlyWhenTheRunWasNotClosed` (worker): 取消だけなら報告せず、
    閉じる書込みの失敗を含む取消は報告する。
  - `internal/application/taskmanager`: `TestRunStartingFunctionsAreReviewedForClosure`。

## 同じ型の洗い出し

`CreateAndStartRun`、`StartRunWithReason`、`StartRunFromCheckpoint` を呼ぶ非試験の関数15個を調べた。
関数名で台帳に載せている。分類はコードを読んで付けたもので、行動試験があるのは MP だけである。

| 区分 | 関数 (経路) | 内容 |
|---|---|---|
| 修正済み | `cmd/rencrow` の MP の `RunOne` (RESEARCH) | 本件 |
| 閉じている | Heartbeat の `beginWorker`、legacy OPS の `admitLegacyOPS`、DCI identity acceptance の `admitAgentOpsDCIExecution`、Viewer の DCI 検索 `ServeHTTP`、SuperAgent の `startRun` | 各経路が Run か Task を閉じる。ただし Heartbeat は戻った後の6つの呼出し元が `finish` を呼ぶ前提で、呼出し元の全経路は未確認。SuperAgent は lease の取り付け失敗では中断するが、その後の claimed の記録の失敗では lease が切れて Run が再発行されるまで残る (占有は lease で有界)。DCI 検索は閉じるが、書込みの予算が10秒で、Task store の実測 (約11秒) を下回る (別論点) |
| 未修正 (同じ型) | `playback_action_receipt.go` の `Record` (CHAT) | `CreateAction` 失敗で未終端 |
| 未修正 (同じ型) | `tts_action_bridge.go` の `executionContext` と `StartSession` (CHAT) | `WithIdentity`、`CreateAction`、束縛の失敗で未終端 |
| 未修正 (同じ型) | `backlog/action_context.go` の `beginAction` (OPERATIONS、単一枠) | `WithIdentity`、`CreateAction`、`WithBoundActionAttempt` の失敗で未終端 |
| 未修正 (同じ型) | `sandbox/promotion_diff_apply.go` の `executePatchAction` (CODE) | `WithIdentity`、`CreateAction` の失敗で未終端 |
| 未修正 (同じ型) | `llm/middleware/action.go` の `begin` (GENERAL) | `WithIdentity`、`CreateAction`、束縛の失敗で未終端 |
| 未修正 (同じ型) | `vision/action_analyzer.go` の `executionContext` と `Analyze` (CHAT) | 同上 |
| 未修正 (同じ型) | `stt/action_provider.go` の `executionContext` と `Transcribe` (CHAT) | 同上 |
| 未修正 (到達しにくい) | IdleChat の `issueIdleChatRun` (既存Taskの経路)、`startGenerationSuccessor` | Task owner が返した Run の検証に失敗した時に、その Run を閉じずに返す。実際の owner は自分の Run を検証済みなので到達しにくい。新規Taskは cancel される |

上の「同じ型」の7経路は、いずれも成功経路では `complete` 相当の関数が Attempt、Action、Run を閉じる。欠けているのは、準備段階の失敗である。
また各 `complete` の取消の分類 (`Canceled` は cancelled、期限切れは failed) が、各ファイルにコピーされており、
`context.WithoutCancel` だけで期限が無い。

## 残るリスクと未実施

- [WARN] 上の未修正7経路は直していない。範囲が7パッケージにまたがり、経路ごとに失敗の再現試験が要るため、本変更では一覧に
  留めた。2経路以上に適用する時点で、Task owner に共通の終端API (切り離し、有限の期限、取消の分類、原因との結合) を置くのが
  自然である。MP の試験 5本が、各経路の再現試験の雛形になる。
- [WARN] DCI 検索の終端書込みの予算 (10秒) は Task store の実測を下回る。DCI 検索は MP と同じ RESEARCH 枠を握るので、
  書込みが期限切れになれば本件と同じ停止になり得る。search 失敗時は終端書込みの戻り値も捨てている。
- [WARN] 生存証拠 (lease や heartbeat) による枠の占有判定は未実装。これが無い間、閉じ漏れが1件でもあれば、再起動まで
  その枠の仕事は止まる。
- Action store が毎回全件を読む遅さ (本番で約72MB、1操作 約1秒以上) は直していない。取消が届く窓を広げる要因として残る。
- [WARN] 本変更は配備していない。本番は旧binaryのままで、再起動までは孤児Taskが残る可能性がある。

## 関連ファイル

- `cmd/rencrow/memory_promotion_action_runner.go`、`memory_promotion_action_runner_test.go`
- `cmd/rencrow/runtime_memory_promotion.go`、`cmd/rencrow/llm_busy_tracker.go`、`cmd/rencrow/runtime_run_recovery.go`
- `internal/application/taskmanager/started_run_closure_architecture_test.go`
- `docs/調査/20261012_230400_Tool終端保存の予算不足によるOPS失敗_FailureKnowledge.md` (終端書込みの予算の規則)
- `docs/調査/20261011_090057_Run無しqueued_Taskの滞留_FailureKnowledge.md` (実行枠が満杯になる原因の別の側面)
