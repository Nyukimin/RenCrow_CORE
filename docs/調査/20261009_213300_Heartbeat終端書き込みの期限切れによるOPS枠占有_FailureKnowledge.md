# Heartbeat終端書き込みの期限切れによるOPS枠占有

記録日: 2026-10-09 (JST)。対象: Heartbeat worker (Gmail intake / X Bookmark / backlog runner / workstream) の
Task終端書き込みと、その再試行 (`internal/application/heartbeat/`)。

## 概要

operations (OPS) 経路の実行枠 (`DestructiveTasks=1`) が空かず、定期tickごとに
`operations task limit reached` が出続け、viewerの`/ops`が `parallel limit exceeded` で失敗した。
枠を握っていたのは完了済みのはずの Heartbeat worker のTaskで、`running` のまま残っていた。

## 発生条件

- Heartbeat workerは約3秒で終わるが、終端書き込み (`Succeed`/`Fail`/`Cancel`) が固定10秒の予算を超えた。
- task storeの1トランザクションが store 全体 (`task_state.jsonl` 約66MB、`task_run.jsonl` 約29MB) を
  毎回読み直し、さらに非公平なグローバルロック (flockの10msポーリング) の待ちが加わる。
- 履歴は日々増える (Memory Promotionが1日約2,800 Taskを作る)。OPS Taskの `created_at -> started_at` の
  日別中央値は 9/14 の1.5秒から 10/5 以降は10秒超へ増え、10秒の予算を追い越した。

## 事実

- 初回の終端書き込みが `context deadline exceeded` で失敗し、滞留台帳へ登録された。
- 再試行 (5分間隔) は8回すべて同じ理由で失敗し、30分で放棄された。Taskは `running` のまま残った。
- 再試行が構造的に不利だった理由:
  - 全entryが共有の10秒contextを使い、
  - entryごとに事前の `Get` (全走査) の後に `Succeed` (全走査) を実行し、
  - 1回の走査が約5秒を超えた時点で、2回分の走査を要する再試行は成立しなくなる。
  初回は1回の走査で足りたため10/5まで持った。
- 再起動時の孤児回収は枠を解放していた (毎回1件を `failed` にした) が、再起動後約30分以内の
  最初のworkerが同じ理由で再びleakした。

## 結論 (Failure Knowledge)

- **Failure**: 完了済みのHeartbeat Taskが終端できず `running` のまま残り、OPS枠を再起動まで占有した。
- **Problem**: 終端書き込みの予算が store の実測遅延を下回り、書き込みが成功しうるのに期限切れで失敗扱いになった。
  再試行も同じ予算の共有・事前読み取り・短い放棄窓のため、遅い store では成立しなかった。
- **Cause**: (1) 予算が固定10秒で、store遅延 (履歴量に比例) と無関係に決まっていた。(2) 再試行が
  entryごと・操作ごとの予算を持たず、`Get` と `Succeed` で走査を2回払った。(3) 30分で放棄し、
  放棄が「枠の恒久占有」という失敗モードそのものになっていた。(4) 「一時障害」を想定した設計を、
  恒常的な遅延に使い続けた。
- **Lesson**: 終端書き込みの予算は観測した store 遅延に余裕を持たせ、再試行は書き込みごとに独立させる。
  放棄は最後の手段で、枠を握り続ける事実を観測可能にする。書き込み前の読み取りは、遅い store では
  再試行を不利にする。
- **Invariant**:
  1. 終端書き込み1回の予算は、観測した store 1トランザクションの遅延の3倍以上である (現在60秒)。
  2. 再試行はentryごと・書き込みごとに独立した予算を持ち、書き込み前にTaskを読まない。
  3. 別経路が先に決着させたTaskは、終端状態からの無効遷移の拒否で判断する。非終端状態からの
     無効遷移は決着済みとみなさない。
  4. 再試行は有界の指数backoff (1分から倍々、上限15分) で行い、窓 (24時間) を超えた場合だけ放棄し、
     ログとイベントで観測可能にする。1回のsweepが tick を塞ぐ時間は上限 (2件・累計90秒) を持つ。
  5. 状態遷移の不変条件 (終端状態からの遷移禁止) は弱めない。別のcancel経路は足さない。
- **Enforcement**:
  - `domaintask.InvalidStatusTransitionError` / `domaintask.AlreadyTerminal` で無効遷移を型付けし
    (エラー文言は不変)、`taskmanager` が返す。
  - Heartbeatの `TaskOwner` 境界から `Get` を外した (型で事前読み取りを禁止)。
  - 予算・backoff・件数上限は `worker_finalization_sweep.go` / `worker_lifecycle.go` の定数で固定。
- **Tests**:
  - `internal/domain/task/status_transition_test.go` (終端/非終端の判定、文言不変)
  - `internal/application/taskmanager/status_transition_error_test.go` (実JSONL store越しの型付きエラー)
  - `internal/application/heartbeat/worker_finalization_sweep_test.go`
    (予算が観測遅延の3倍以上、entryごとの独立予算、事前Getなし、件数上限、停止要求での打ち切り、
    backoffの伸びと上限、旧30分窓で放棄しないこと、保存済み書き込みの冪等な再試行、非終端の無効遷移の保持、
    遅い書き込みの観測ログ、24時間窓での放棄とイベント)

## 手順 (再現と修正)

1. 再現: 本番store相当の履歴量の合成storeで、終端書き込みコストが履歴量に比例することを確認した
   (履歴200件 25ms に対し60,000件 1.4秒、約55倍)。
2. 修正前のテストを先に追加し、旧挙動で期待した理由 (予算10秒、事前Get、30分放棄、件数上限なし、
   停止要求を無視) で失敗することを確認した。
3. 修正後に同じテストが通ることを確認した。

## 残るリスクと未実施 (今回の範囲外)

本修正は緩和策であり、根本原因 (store全体を毎回走査する構造と、非公平なグローバルロック) は残る。
store遅延は履歴量に比例して増え続けるため、次の根治策が別途必要:

- terminalなTaskの保持期限とcold退避 (方針b)
- 1日約2,800件のMemory PromotionをcanonicalなTaskにしている設計の見直し (方針c)
- storeの索引化・末尾読み (方針d、共有契約のため影響が大きい)

予算は固定60秒のため、1トランザクションが約12秒を超えて伸び続ければいずれ再び不足する。
sweepが `worker finalization slow` (予算の半分超) を出すので、その傾向を観測して次の対策の時期を判断する。

## 関連ファイル

- `internal/application/heartbeat/worker_lifecycle.go` (`heartbeatTaskFinalizeTimeout`、`finishWorker`)
- `internal/application/heartbeat/worker_finalization_sweep.go` (backoff、sweep、放棄)
- `internal/domain/task/status_transition.go`
- `internal/application/taskmanager/manager.go` (`updateStatusInTransaction`)
- 仕様: `docs/02_機能仕様.md` (Heartbeatによる定期収集)
