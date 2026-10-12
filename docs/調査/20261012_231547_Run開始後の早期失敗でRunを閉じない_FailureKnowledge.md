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
| 閉じている | Heartbeat の `beginWorker`、legacy OPS の `admitLegacyOPS`、DCI identity acceptance の `admitAgentOpsDCIExecution`、Viewer の DCI 検索 `ServeHTTP`、SuperAgent の `startRun` | 各経路が Run か Task を閉じる。ただし Heartbeat は戻った後の6つの呼出し元が `finish` を呼ぶ前提で、呼出し元の全経路は未確認。SuperAgent は lease の取り付け失敗では中断するが、その後の claimed の記録の失敗では lease が切れて Run が再発行されるまで残る (占有は lease で有界)。DCI 検索は閉じるが、書込みの予算が10秒で、Task store の実測 (約11秒) を下回っていた (2026-10-13 に60秒へ直した。下の「共通APIへの統合」) |
| 修正済み (共通API、2026-10-13) | `playback_action_receipt.go` の `Record` (CHAT) | `CreateAction` 失敗で未終端だった |
| 修正済み (共通API、2026-10-13) | `tts_action_bridge.go` の `executionContext` と `StartSession` (CHAT) | `WithIdentity`、`CreateAction`、束縛の失敗で未終端だった |
| 修正済み (共通API、2026-10-13) | `backlog/action_context.go` の `beginAction` (OPERATIONS、単一枠) | `WithIdentity`、`CreateAction`、`WithBoundActionAttempt` の失敗で未終端だった |
| 修正済み (共通API、2026-10-13) | `sandbox/promotion_diff_apply.go` の `executePatchAction` (CODE) | `WithIdentity`、`CreateAction` の失敗で未終端だった |
| 修正済み (共通API、2026-10-13) | `llm/middleware/action.go` の `begin` (GENERAL) | `WithIdentity`、`CreateAction`、束縛の失敗で未終端だった |
| 修正済み (共通API、2026-10-13) | `vision/action_analyzer.go` の `executionContext` と `Analyze` (CHAT) | 同上 |
| 修正済み (共通API、2026-10-13) | `stt/action_provider.go` の `executionContext` と `Transcribe` (CHAT) | 同上 |
| 未修正 (到達しにくい) | IdleChat の `issueIdleChatRun` (既存Taskの経路)、`startGenerationSuccessor` | Task owner が返した Run の検証に失敗した時に、その Run を閉じずに返す。実際の owner は自分の Run を検証済みなので到達しにくい。新規Taskは cancel される。直さない理由は下の「共通APIへの統合」に書いた |

上の「同じ型」だった7経路は、いずれも成功経路では `complete` 相当の関数が Attempt、Action、Run を閉じる。欠けているのは、準備段階の失敗である。
また各 `complete` の取消の分類 (`Canceled` は cancelled、期限切れは failed) が、各ファイルにコピーされており、
`context.WithoutCancel` だけで期限が無い。

## 残るリスクと未実施

- [OK] 上の未修正7経路と DCI 検索の終端書込みの予算 (10秒) は、2026-10-13 に共通の終端API
  (`taskmanager.Manager.CloseStartedRun`) へ統合して直した。経緯と残りは下の「共通APIへの統合」に書く。
  (当初の記述: 範囲が7パッケージにまたがり、経路ごとに失敗の再現試験が要るため、MP の修正では一覧に留めた。
  DCI 検索は MP と同じ RESEARCH 枠を握るので、書込みが期限切れになれば本件と同じ停止になり得た。)
- [WARN] 生存証拠 (lease や heartbeat) による枠の占有判定は未実装。これが無い間、閉じ漏れが1件でもあれば、再起動まで
  その枠の仕事は止まる。
- Action store が毎回全件を読む遅さ (本番で約72MB、1操作 約1秒以上) は直していない。取消が届く窓を広げる要因として残る。
- [WARN] 本変更は配備していない。本番は旧binaryのままで、再起動までは孤児Taskが残る可能性がある。

## 共通APIへの統合 (2026-10-13 追記)

MP の修正で確定した規則を、Task の owner に置いた一つの終端APIにまとめ、同じ型だった7経路と DCI 検索をそれで直した。
MP もそれに移し、MP 専用の定数・番兵・分類 (`memoryPromotionTerminalWriteTimeout`、`errMemoryPromotionRunNotClosed`、
`memoryPromotionTerminalState`) は消した。

### 共通APIの形と置き場所

| 置き場所 | API | 役割 |
|---|---|---|
| `internal/application/taskmanager/started_run_closure.go` | `Manager.CloseStartedRun`、`Manager.AbortStartedRun`、`RunOutcomeOf`、`RunOutcome`、`ErrStartedRunNotClosed`、`StartedRunTerminalWriteTimeout` | 開始した Run と Task を閉じる。取消から切り離し、書込みごとに60秒、取消は cancelled・期限切れとその他は failed、閉じる書込みの失敗は原因と結合し番兵を付けて返す。`AbortStartedRun` は Action を作る前の準備の失敗用 |
| `internal/application/actionmanager/attempt_closure.go` | `Manager.CloseAttempt`、`AttemptOutcomeOf`、`AttemptOutcome`、`ErrAttemptNotClosed`、`AttemptTerminalWriteTimeout` | Attempt と Action を閉じる同じ規則。期限切れだけは Attempt が timed_out (Action は failed) |

根拠は `40-modularization` の根拠1 (今日 9 箇所 (MP、同型の7経路、DCI検索) から使われる)。taskmanager と actionmanager は互いに import しない兄弟なので、
Attempt を閉じてから Run を閉じる合成は呼び出し側に置いた。分類 (Task 側と Action 側) は各 owner が持ち、二つが食い違わないことは
`taskmanagertest` の `TestRunAndAttemptOutcomesAgreeForEveryKindOfEnd` が保つ。利用側は全て `*taskmanager.Manager` を直接保持して
いるため、port は足していない。

`CloseAttempt` は `(closed bool, err error)` を返す。合成する呼び出し側は「Attempt を閉じられたか」で Run を閉じるかを決めるが、
結合した error を `errors.Is` で判定すると、原因が別の呼出し由来の番兵を抱えている場合に誤る。

### 直した経路と、直していない経路

| 経路 | 状態 | 行動試験 |
|---|---|---|
| MP の `RunOne` | 共通APIへ移した (挙動は不変) | `TestActionMemoryPromotionRunner*` |
| `playback_action_receipt.go` の `Record` | 直した | `TestPlaybackActionRecorderClosesTheRunItStarted` |
| `tts_action_bridge.go` の `executionContext` / `StartSession` / `complete` | 直した | `TestActionTTSBridgeClosesTheRunItStarted` |
| `backlog/action_context.go` の `beginAction` | 直した | `TestBeginActionClosesTheRunItStarted` |
| `sandbox/promotion_diff_apply.go` の `executePatchAction` | 直した | `TestPromotionDiffApplierClosesTheRunItStarted` |
| `llm/middleware/action.go` の `begin` と終端 | 直した。終端に60秒の期限を付けた (以前は切り離しだけで期限なし) | `TestActionProviderClosesTheRunItStarted` |
| `vision/action_analyzer.go` | 直した | `TestActionAnalyzerClosesTheRunItStarted` |
| `stt/action_provider.go` | 直した | `TestActionProviderClosesTheRunItStarted` (stt) |
| `viewer/dci_handler.go` の DCI 検索 | 終端書込みの予算を10秒から60秒へ。検索失敗時の終端の戻り値を捨てていたのをログへ出す | `TestDCISearchHandler*` |
| IdleChat の `issueIdleChatRun` (既存Task)、`startGenerationSuccessor` | **直していない** | 無し |

直していない2経路の理由: owner が自分で検証した Run を返す限り到達しない。到達するのは owner の答えが自己矛盾した時
(TaskID/RunID が不正、または別 Task の Run) だけで、その Run を閉じに行くと、ID を信用できない Run (別 Task の Run かもしれない)
へ書くことになり、枠が詰まるより悪い。加えて、この2関数が使う port (`idlechatRunIssuer`) には Run を閉じる操作が無い。
owner の契約が矛盾した Run を返せない形 (型や検証) になった時、または port に終端が加わった時に再評価する。台帳の理由欄に同じ内容を残した。

行動試験は `taskmanagertest.VerifyStartedRunClosure` の共通シナリオ (成功、作業の失敗、Action 作成の失敗、呼出し元の期限切れ・取消の最中の
Action 作成の失敗、Action 作成後の束縛の失敗、Run の終端書込みの拒否、Attempt の終端書込みの拒否) を、経路ごとに実行する。
各シナリオは失敗を一つずつ注入し、Task と Run が終端になること、実行枠が空くこと、終端書込みが有限で観測遅延の3倍 (30秒) 以上の予算を持つことを確かめる。
先に試験だけを書き、修正前の7経路で、Run が running のまま・Attempt が open のまま・終端書込みに期限が無い、という期待した理由で失敗することを確認してから直した。

### 規則の追加と、台帳の強化

- **Invariant (追加)**: Run を開始した関数は、開始後のすべての失敗経路で `CloseStartedRun` (準備の失敗なら `AbortStartedRun`) で終端する。
  Action を作る経路は、先に `CloseAttempt` で Attempt と Action を閉じる。
- **Enforcement (追加)**: 台帳の区分に「共通APIで閉じている」を足した。この区分の関数が属するファイルは `CloseStartedRun` を呼んでいなければならず、
  共通の規則を名乗りながら別実装を持つことはできない。新しい開始経路が台帳に無ければ落ちる性質は保つ。
- 取消の分類が変わった経路: patch apply と DCI 検索は、以前は取消も failed にしていた。共通の規則に合わせ、取消は cancelled になる。

### 残るリスク

- [WARN] 構造上、`CreateAndStartRun` の直後の `WithIdentity` の失敗は、実 owner では起こらない (owner が返す ID は常に妥当) ので、その経路の
  行動試験は無い。コードは閉じる側へ直した。
- [WARN] Attempt を閉じられなかった時は Run も閉じない (MP の既存の挙動を全経路で維持した)。この間 Run は実行枠を握る。
  再起動時の回復は、開いた Action を cancelled にしてから Run を閉じるので、Run を先に閉じても整合は取れる。
  Attempt の終端書込みが `ErrAttemptConflict` (既に閉じている) で失敗した場合は、Run が再起動まで残る。Run を閉じる方針に改めるかは未決。
- [WARN] `CompleteRun` が「既に終端」を理由に失敗した場合も、`ErrStartedRunNotClosed` を付けて返す (他の主体が先に閉じた場合の誤報)。
- [WARN] 他の終端書込みは共通APIに移していない。Heartbeat、legacy OPS、DCI identity、native OPS は独自の60秒で、規則と値が一致している。
  IdleChat の Run の終端 (`generation_run_checkpoint.go` の後継 Run の終端と `generation_run_recovery.go` の終端は5秒、会話の cleanup は30秒) は、
  Task store の実測 (約11秒) を下回る、または3倍に足りない。DCI 検索や MP のように実行枠を握り続ける型の失敗が、ここにも起こり得る。
  IdleChat の Run は別の owner の経路で、本変更の範囲外にした。
- [WARN] 台帳が見るのは `CreateAndStartRun`、`StartRunWithReason`、`StartRunFromCheckpoint` を呼ぶ関数だけである。Task 単位の `Start` で Run を
  開始する経路 (orchestrator の `task_lifecycle.go` と `message_orchestrator.go`。`defer` の `finish` が Task と Run を終端する設計) は、
  別の系統で台帳の対象外にある。2026-10-12 22:54Z に本番で、利用者の chat 要求が `conversation turn conflict` を含む失敗になった。
  本番 store のコピー (2026-10-12 23:19Z) では、その Task と Run は failed で終端していて、running の CHAT は無く、実行枠は握っていない。
  /viewer/status の task_activities (プロセス内の MonitorStore。永続しない) に chatting / running のまま残るのは、この失敗経路が
  終端を示す表示イベント (`entry.stage` の終端、利用者宛の最終 `agent.response`) が出ないためと推定される (コードの読みによる。当時のイベントログは未確認)。
  Run の終端漏れではない。MonitorStore はプロセス内のみなので再起動で消える。
- 本変更は配備していない。本番は旧binaryのままで、再起動までは同型の孤児 Task が残る可能性がある。

## 関連ファイル

- `cmd/rencrow/memory_promotion_action_runner.go`、`memory_promotion_action_runner_test.go`
- `cmd/rencrow/runtime_memory_promotion.go`、`cmd/rencrow/llm_busy_tracker.go`、`cmd/rencrow/runtime_run_recovery.go`
- `internal/application/taskmanager/started_run_closure_architecture_test.go`
- `docs/調査/20261012_230400_Tool終端保存の予算不足によるOPS失敗_FailureKnowledge.md` (終端書込みの予算の規則)
- `docs/調査/20261011_090057_Run無しqueued_Taskの滞留_FailureKnowledge.md` (実行枠が満杯になる原因の別の側面)
- 共通の終端API: `internal/application/taskmanager/started_run_closure.go`、`internal/application/actionmanager/attempt_closure.go`
- 共通の行動試験: `internal/application/taskmanager/taskmanagertest/closure.go`、`closure_suite.go`
- 台帳: `internal/application/taskmanager/started_run_closure_architecture_test.go`
