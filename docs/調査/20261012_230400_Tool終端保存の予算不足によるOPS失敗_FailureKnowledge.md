# Tool終端保存の予算不足によるOPS失敗

記録日: 2026-10-12 (JST)。対象: `POST /v1/agent/ops`（legacy OPS入口）が呼ぶ Shiro の Tool ループが、
Tool実行後に Action／Attempt を終端する書き込み（`actionmanager.Manager.CompleteToolAttempt`）。

## 概要

本番へ配備した直後に `rencrowctl ops` 相当の要求が成功しなかった。4回の要求の結果は次のとおり。

| 回 | 結果 | 判定 |
|---|---|---|
| 1・2回目 | HTTP 503 `runtime_unavailable` | 仕様どおりの即時拒否（Heartbeat workerがoperations実行枠を保持中）。不具合ではない |
| 3回目 | HTTP 500 `execution_failed`（`complete tool attempt: context deadline exceeded`） | 本件。終端保存専用の5秒予算が切れた |
| 4回目 | クライアント側の120秒で切断。Taskはcancelledで終端 | 遅さの帰結。要求自体は長引いただけ |

## 発生条件

- 要求のLLMが Tool を呼ぶ（Tool呼出しが無い応答は Action store の Tool 経路を通らず、この失敗に当たらない）。
- Action store（`state/actions` 配下のJSONL）が大きく、他の書き手（Memory Promotion、LLM呼出しの記録）が
  同じstoreを同時に使っている。

## 事実（観測と実測。推定は「推定」と明記）

- 期限の構成（OPS要求1件）:
  - HTTPサーバに `ReadTimeout`／`WriteTimeout` は無い。handlerは `r.Context()` をそのまま使い、期限を足さない
    （クライアント切断だけが取消になる）。
  - admission（`CreateAndStartRun`）、Subagent、toolloop、`provider.Chat` は要求のcontextを引き継ぐ。
    LLM呼出しの上限は provider 側のHTTPクライアントの期限（既定120秒。設定で変わる）。
  - Tool attempt の終端保存だけが、取消を切り離した**固定5秒**のcontext（`CompleteToolAttempt`）だった。
  - Run の終端は handler が要求から切り離した60秒の予算で行う（今回も正しく failed／cancelled に閉じた）。
  - LLM呼出しの Action 終端（`internal/infrastructure/llm/middleware/action.go`）は取消を切り離すだけで期限が無い。
- 3回目の失敗は、Tool 実行そのものではなく、その後の終端保存（5秒）が切れて起きた。Task と Run は
  failed で閉じたが、Action は open、Attempt は running のまま本番の Action store に残った
  （open の Action は全体でこの1件だけ。次回起動時の `recoverActionRunsAfterRestart` が cancelled に畳む）。
- Action store の規模（2026-10-12 時点の本番コピー）: ファイル約72MB（Action状態 約48MB、Attempt 約27MB）、
  Action 55,415件、行数110,829。memory_promotion が約95%を占め、1日あたり約2,500件増える。
- Action store の全 `Transaction`／`ReadTransaction` は、両ファイルを全件読み、全件を `Validate` し、
  `validateSnapshot` を通す（commit は追記のみ）。実測（本番コピーを開発機で、実際の `JSONLStore` と
  `actionmanager` を通して計測）: open+検証 約1.0秒、`CreateAction` 約1.2秒、`ValidateToolAttempt` 約1.0秒、
  `CompleteToolAttempt` 約1.2秒。CPUの大半は `encoding/json` のdecodeだった。
- 本番ホスト（4コアのLinux、負荷あり）では Tool 1回（作成、admission検査、実行、終端）が7〜10秒かかった。
  LLM応答のlogから ToolLoop に戻るまでにも毎回約4秒かかっている（LLM呼出しの Action 終端に当たると推定。直接計測は無い）。
  idle の本番に、読み取りだけの Tool 1回を使う要求を送ると HTTP 200 で66秒、そのうちLLM待ち以外が約27秒だった。
- 3回目の失敗した Tool の開始と同じ秒に Memory Promotion の実行が始まり、その後約11秒間隔で連続した
  （13:31〜13:37に27件、1回につき Action 2操作）。`Manager.mu` と JSONL の OSロックは共有なので、OPS の終端保存が
  その後ろで待たされたと推定される（ロック待ちの直接計測は無い）。
- 03:15Z に成功した確認は Tool 呼出しの無い「OK」応答で、Action store の Tool 経路を通っていない。
  配備した版と前の版の差分に ops、toolloop、actionmanager、Action store、LLM middleware は含まれない
  （TaskStore の回転などのみ）。版差ではなく、要求が Tool 経路を通ったことと、並行負荷の差。
- 503 は `CreateAndStartRun` が原子的に拒否した結果で、Task は1件も永続されない（storeで確認）。

## 結論 (Failure Knowledge)

- **Failure**: 完了した Tool 呼出しの終端保存が期限切れになり、OPS 要求が HTTP 500 で失敗し、Attempt が
  running のまま残った。
- **Problem**: 終端保存の予算（5秒）が Action store の実測遅延を下回っていた。保存が成功しうるのに
  期限切れで失敗扱いになり、ToolLoop は仕様どおり後続modelを呼ばずに停止した。
- **Cause**: (1) Action store が全トランザクションで全履歴を読み検証するため、遅延が履歴量に比例する
  （Memory Promotion が履歴の大半を作る）。(2) 終端保存の予算が store の遅延と無関係な固定5秒だった。
  (3) 同じ store を Memory Promotion などが同時に使い、ロックの待ちが加わった。
  (4) 推定: 同じ store の遅さが、別経路（Memory Promotion の Run 終端漏れ。下記）でも別の症状を出している。
- **Lesson**: Heartbeat の Task 終端書き込み（`20261009_213300_...FailureKnowledge.md`）と同型。取消から切り離した
  終端保存の予算は、観測した遅延の3倍以上にする。予算の延長は緩和であり、遅延の根（全履歴読み）は残る。
- **Invariant**:
  1. Tool attempt の終端保存1回の予算は、観測した Tool 1回の Action store 処理時間（10秒）の3倍以上である
     （現在60秒）。かつ有限である。
  2. 終端保存は呼出元の取消・期限切れから独立し、元のcontext valuesを保持する。
  3. 終端保存に失敗した場合は成功扱いにせず、元の実行エラーと保存エラーを保持して停止し、後続modelを呼ばない
     （この挙動は変更しない）。
- **Enforcement**: `internal/application/actionmanager/manager.go` の名前付き定数 `AttemptTerminalWriteTimeout`（60秒。当初は
  `toolAttemptTerminalWriteTimeout`。2026-10-13 に、Attempt の終端を閉じる共通の `CloseAttempt` と共有するため改名した）。値の根拠は定数のコメントとこの記録に残す。
- **Tests**: `internal/application/actionmanager/tool_attempt_budget_test.go`
  - `TestCompleteToolAttemptTerminalWriteBudgetCoversObservedStoreLatency`: 呼出元が期限切れでも、storeへ届く
    contextの残り時間が観測遅延の3倍（30秒）以上である。
  - `TestCompleteToolAttemptTerminalWriteBudgetIsBounded`: 取消済みの呼出元でも期限が付いており、上限が有限（5分以内）である。
  - 既存の `TestCompleteToolAttemptCancellationPreservesContextValues` などが、取消の切り離しとcontext valuesの保持を保証する。

## 手順 (再現と修正)

1. 再現: 本番コピーの Action store に対し、実際の `JSONLStore` と `Manager` で作成・検査・終端の各操作に
   1秒前後かかることを確認した。本番では同じ操作が数秒かかり、並行書き込みが加わると5秒を超える。
2. 修正前に新規試験を追加し、期待した理由（storeへ届く予算が5秒で、30秒に足りない）で失敗することを確認した。
3. 定数を60秒にして同じ試験が通ること、関連パッケージの試験が通ることを確認した。

## 仕様との差（正本の更新）

`docs/architecture/identity/IDENTITY_CANONICAL.md` の「Tool outcome terminal recording」は、終端保存を
「取消だけを切り離した5秒上限のcontext」と定めていた。これを60秒上限へ改め、根拠（Action storeの観測遅延の3倍）を
添えた。同文書の IdleChat の5秒cleanup は別経路で、別の失敗域であり、この変更の対象外にした。

## 残るリスクと未実施（今回の範囲外）

本修正は緩和策で、根本原因（Action store が全履歴を毎回読む構造）は残る。

1. **Action store の全件読み**: 履歴は日々増える。根治の選択肢は、(a) ファイル署名（サイズ・更新時刻・inode）で
   無効化する読み取りのsnapshotと追記の反映（形式も移行も変えず、別プロセスの書き込みは署名の不一致で
   安全側に落ちる）、(b) 終端済みActionの日次回転とcold退避（TaskStore の方向と揃える）、
   (c) 件数の大半を占める memory_promotion の Action 化の見直し。仕様案 `20261010_205550_TaskStore_ID索引形式_仕様案.md`
   は Action store の肥大を範囲外としており、別の仕様案が要る。
2. **ロック保持の延長**: 予算が60秒になったため、`Manager.mu` を待つ操作（`ValidateAttemptLineage` などの
   読み取りを含む）が最大60秒待たされうる。許容するが、観測可能にしておく。
3. **予算の直列消費**: handler の60秒の終端予算と Tool の60秒は直列に消費されうる。最悪の要求時間は
   クライアントの待機を超える。
4. **LLM呼出しの Action 終端に期限が無い**: `internal/infrastructure/llm/middleware/action.go` は取消だけを切り離す。
   store が詰まると呼出元が無期限に待つ。上の Invariant 1 と同じ扱い（有限で、観測遅延の3倍以上）にそろえる検討が要る。
5. **終端保存に失敗した場合の記録の欠落**: Tool の外部効果は完了済みなのに Attempt は running のまま残り、
   次回起動時に cancelled へ畳まれる（成功した効果が cancelled と記録される）。破壊的 Tool では監査上の穴になる。
   予算の延長で頻度は下がるが、構造は残る。
6. **Memory Promotion の Run 終端漏れ**（別の欠陥。今回は直していない）:
   `cmd/rencrow/memory_promotion_action_runner.go` の `RunOne` は、`CreateAndStartRun` の成功後に
   `WithIdentity`／`CreateAction`／`WithChildBoundActionAttempt` が失敗すると、Run を閉じずに返す。
   本番では Action 記録の無い RESEARCH の running Task が残り、`LongResearchTasks=1` の枠を握り続けて、
   以降の Memory Promotion が `ErrParallelLimit` で延期され続けた（08:44 にも同型が1件あり、再起動の回収まで数時間詰まった）。
   同じ store の遅さが根にあると推測される（確認は未了）。
7. **時間切れ専用のstatusが無い**: Shiro実行の失敗は一律 HTTP 500 `execution_failed` で、`rencrowctl ops`（CMD）は
   HTTP statusだけを表示する。503 の理由（実行枠満杯）と 500 の理由（実行失敗）は利用者から見分けにくい。
   専用statusやRetry-Afterの追加は公開API契約（`docs/06_Public_API仕様.md`）とCMDの変更を伴うため、別途判断する。
8. **API仕様と実装の差**: `docs/06_Public_API仕様.md` は枠満杯の503を「作成したTaskをfailedに閉じ」と書くが、
   実装と試験（`TestAgentOpsLegacyOPSParallelLimitReturns503AndLeavesNoQueuedTask`）はTaskを1件も永続しない。

## 追記 2026-10-13: 全件読みの根治（Action storeのsnapshot）

「残るリスク」の1（Action store の全件読み）に、`docs/調査/20261013_115300_ActionStore全件読み_設計案.md` のIU-3・IU-4で対処した。
予算の延長（上の Invariant 1）は緩和のまま残し、遅延の根を別の不変条件で押さえる。実測と判断の詳細は設計案の7章。

- **Failure**: Action store の全 `Transaction`／`ReadTransaction`（本番規模で約2〜5秒）と、起動時の回復3件（全Actionを列挙して Run と突き合わせる）が、
  履歴の量に比例して遅くなり、Tool 1回の終端保存が予算を超えて OPS 要求が HTTP 500 になった。
- **Problem**: 毎回の全件decode・全件検証は、触るrecordが1件でも履歴全体を読む。起動時の回復は、1件の「存在しないRun参照」が
  起動の失敗になる経路でもあった。
- **Cause**: store が「ID で引く」「Run で引く」「open のものを引く」という問合せの形を持たず、呼出し元（回復の3箇所）が全件を列挙して
  自分で結合していた。履歴の大半（約95%）は空振りの Memory Promotion が作っていた（IU-1で止めた）。
- **Lesson**: 履歴が増え続ける store に対する全件の列挙は、既定にせず、理由のある例外にする。派生物（メモリ上の snapshot）は
  ログの先頭からの decode 結果であり続けるように、ログに追記された bytes だけで変える。増分検証は、旧来の全件検証との等価性を
  状態単位（任意の状態で受理・拒否が一致）とtx単位（同じ列で受理・拒否・全問合せ・ログのbytesが一致）の両方で示す。
- **Invariant**:
  1. 本番コードは Action を無制限に列挙しない。許可された場所（開いている Action だけ、全件確認のopt-in、Manager の転送、
     test 支援、interface の宣言）以外で `ListActions` が Run・Task・Limit・Kind+Name のいずれも持たない filter で呼ばれない。
  2. snapshot は常に「ログの先頭から `applied` byte までを、全件読みと同じ fold で decode した結果」と全 field で等しい。
     各 tx・各読取りの前に2つのログの長さと `applied` を照合し、不一致・commit の成否不明・適用の未完（panic を含む）では、
     次の操作の前にログ全体を読み直す。ログが規則違反なら操作は失敗し続ける（fail closed）。
  3. tx は、触った Action とその Attempt の群、および Task Run ごとの native 委譲の一意性だけを検査する。
     この検査は、全件の規則 `validateSnapshot` と任意の状態で受理・拒否が一致する。
  4. 再起動時の回復は、まだ open の Action、native 委譲、running な Run の Action にだけ作用し、その Run を解決する。
     範囲外の全 Action の Run 参照の確認は、`action_store.verify_run_references_at_start` と `rencrow actions verify --check-runs` に置く。
- **Enforcement**:
  - `internal/application/actionmanager/unbounded_list_architecture_test.go`（無制限 `ListActions` の禁止と許可リスト。staleな許可も失敗にする）、
    `internal/application/taskmanager/unbounded_list_architecture_test.go`（回復が列挙する Run は running のみ）。
  - `internal/infrastructure/persistence/action/`: `fold.go`（旧loaderとsnapshotが共有する畳み込み規則）、`group_validate.go`
    （tx が使う規則。`validateSnapshot` が旧経路のoracle）、`snapshot_sync.go`（長さの照合・再構築・hook）。
  - 設定 `action_store.index.enabled`（既定 false）。false で旧経路。
- **Tests**:
  - `group_validate_test.go`: 任意の状態4万件で、全件の規則とgroupの規則の受理・拒否が一致する。
  - `snapshot_oracle_test.go`: 12 seed × 80 tx のランダム列（不正を含む）を旧経路とsnapshotに流し、受理・拒否、全問合せの全 field、
    ログのbytesが一致し、再構築が0回である。意図的な破壊（対象の漏れ、native一意の省略、規則の一部省略）で失敗することを確認した。
  - `snapshot_fault_test.go`: 子プロセスの強制終了（callback、hook 開始、適用の途中、適用後）と、WAL の形（prepare のみ、追記のみ、commit 済み、
    commit の欠け）の後に開き直して、欠落が0で snapshot = 全件読み。
  - `snapshot_sync_test.go`: 別handleの書込み、ログ外の追記、短くなったログ、`ErrCommitUncertain` の各形、hook の panic。
  - `snapshot_concurrency_test.go`（`-race`）、`verify_test.go`、`cmd/rencrow/actions_restart_scope_test.go`（回復3箇所の置換前後の結果が旗の有無で同じ）。
