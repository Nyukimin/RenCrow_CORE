# RenCrowエージェントのハートビート実装仕様

## 所有範囲

COREが認証済みの実行主体、Task、Run、実行状態を管理する。実行主体はMio、Shiro、Kuro、Midori。BOARDが担当先、本文、claim、返信とACKを管理する。作業の実行と障害復旧は各機能の正規入口を使う。

## ハートビートと着手

- `rencrow run`の常駐処理で、10秒ごとに実行状態とタスクを確認する。CLIの判断は決定的に実行し、LLMを呼ばない。実行処理や遅い依存先を待って全エージェントの確認を止めない。
- `heartbeat.agent_scheduler_enabled`で有効化する。Workstream、収集、Backlogなどの既存周期処理にはそれぞれの間隔を使う。
- 実行中かどうかはエージェント単位で判定する。同一エージェントで同時に実行するタスクは原則1件とする。既存の全体、module、routeの実行制限も適用する。
- 空いたエージェントは、BOARD上で自分に割り当てられたタスクを先に選ぶ。担当が0件の場合、他のエージェントの担当件数にかかわらず、自分専用のタスクへ進む。それぞれの候補内で優先度の降順、登録時刻の昇順で選ぶ。同時刻の場合は永続IDで順を確定する。
- BOARDが優先度を提供しない担当タスクは同じ優先度として扱い、BOARDが返す登録時刻を使う。専用キューは既存のRunQueueとTaskの担当、優先度、登録時刻を使う。
- 登録済みの専用Taskは `rencrow tasks enqueue <task_id>` でキューに入れる。起動中のCOREが既存のローカル認証で受け付け、Task IDに対応する初回実行を一度だけ登録する。同じTask IDを再送した場合は既存の受付結果を返す。Runは、ハートビートが着手するときにTaskの所有者が発行する。
- BOARDの読み取りが無応答などで失敗した場合は、BOARDを利用できない状態として記録する。復旧を並行して進めながら、空いたエージェントは自分専用のキューから作業する。BOARD復旧後は次のタスク選択から通常の順序に戻る。
- 両方に仕事がなければ、許可済みの能力でエージェント間の会話、Web閲覧、制作、ゲームなどの活動を選べる。空き時間の活動中もCLIの確認を続け、仕事が来たら活動を終了できる地点で切り替える。定型の活動はCLIで完結し、意味判断や生成が必要な工程でLLMを起動する。

## 割り込みと再開

- 認証済みのユーザーとの会話など、優先度が高いタスクの到着を実行中にも受け付ける。現在のタスクより優先度が高いとき、現在の実行に中断を要求する。
- 中断時は、実行を止められる地点で次の工程と進捗を既存のチェックポイント契約に従って保存する。保存と現在の実行の終了を確認してから、優先タスクを開始する。保存や実行停止を確認できない場合は実行枠を保持する。
- 再開は同じTaskの新しいRunとして行う。先行Runとチェックポイントを照合し、完了済みの外部操作は既存の実行記録を参照する。タスクの優先度、登録時刻、キューと再開位置は再起動後も保持する。

## 障害復旧

| 障害 | 復旧処理 | 完了条件 |
| --- | --- | --- |
| タスクの進行が停止 | 最後に進んだ工程と待機先から原因を特定し、ownerの復旧処理で解消する。 | 復旧確認後、保存済みの地点から処理が進む。 |
| 依存先が無応答 | サービス、接続、処理段階を確認し、無応答の原因を取り除く。 | 正規経路の応答が戻り、対象処理が進む。 |
| COREが異常終了 | COREの外で動くサービス管理機構が検知し、RenCrow全体を依存順に再起動する。COREの起動回復が永続キューと旧Runを照合する。 | 各サービスが稼働し、再開可能なTaskが保存済みの地点から再開する。 |

復旧できなかった実行には、どの段階で止まったかと、確認できた原因を残す。稼働中の旧実行がある場合は、その終了または実行権の失効を確認してから再開する。

サービス管理機構は配備先で承認済みのサービスと依存関係を使う。再起動は依存するサービスから停止し、依存先から起動する。各サービスの正規readinessを確認してから次のサービスへ進む。計画停止は障害からの再起動の対象外とする。

macOSでは、COREと独立したシステムサービスから `rencrow resilience stack-reconcile` を10秒間隔で実行する。設定ファイルは `/Library/Application Support/RenCrow/resilience-stack.v1.json`。`schema` を `rencrow.launchd-stack/v1`、`complete` を `true` とし、実機で確認した常駐サービスを `services` に起動順で列挙する。各要素に `label`、`domain`、`plist_path`、`dependencies`、`readiness` を記録する。COREの死活確認には `127.0.0.1:18790` の `/health/live` を使う。サービスの疎通は `http` または `tcp` でローカルの正規入口を確認する。サービス固有の監視口がない常駐プロセスは、`process` でlaunchdの稼働状態とPIDを照合する。

監視はCOREのPIDを保存し、launchdがCOREを先に自動再起動した場合もPIDの変化を検出する。復旧時は計画のハッシュ、停止と起動の段階、最後に確認したサービスの位置を保存する。停止が途中なら残りの停止を確認してから起動へ進み、起動が途中なら依存先と起動済みのサービスを確認して続ける。COREが稼働中のBOARD単独障害では、専用タスクを続けながらBOARDの復旧を待つ。

Macの監視サービスは [LaunchDaemonの定義](../deploy/macos/com.rencrow.resilience-stack-monitor.plist.template) に従う。管理者がビルド済みの監視用バイナリを `/usr/local/libexec/rencrow-stack-monitor` に、実機で照合した計画を所定の場所にroot所有で設置する。`rencrow resilience stack-check` で構成と現在の状態を確認してから、LaunchDaemonをsystemドメインに登録する。計画停止の前後には、システムサービスの管理者が `rencrow resilience stack-maintenance enter` と `rencrow resilience stack-maintenance leave` を使う。

初回設置では、管理者が次の例の3ファイルを照合してから設置する。初回の `stack-reconcile` が `baseline established` と現在のCORE PIDを記録したことを確認する。

```sh
sudo install -d -o root -g wheel -m 0755 /usr/local/libexec '/Library/Application Support/RenCrow'
sudo install -o root -g wheel -m 0755 "$MONITOR_BINARY" /usr/local/libexec/rencrow-stack-monitor
sudo install -o root -g wheel -m 0644 "$VERIFIED_PLAN" '/Library/Application Support/RenCrow/resilience-stack.v1.json'
sudo /usr/local/libexec/rencrow-stack-monitor resilience stack-check
sudo install -o root -g wheel -m 0644 "$MONITOR_PLIST" /Library/LaunchDaemons/com.rencrow.resilience-stack-monitor.plist
sudo plutil -lint /Library/LaunchDaemons/com.rencrow.resilience-stack-monitor.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/com.rencrow.resilience-stack-monitor.plist
```

## TDDと受入

| ID | 検証すること |
| --- | --- |
| HB-01 | 実行中や依存先の無応答時も10秒周期の確認を継続する。確認のみの周期はLLM呼出数が0。 |
| HB-02 | 同一エージェントの会話、BOARD、専用タスクが同時に到着しても実行は1件。別エージェントは許容される範囲で並行する。 |
| HB-03 | 自分宛てのBOARD、専用キュー、空き時間の順。各キュー内で優先度と古さを守る。他の担当のみのBOARDやBOARD復旧中でも専用タスクが進む。 |
| HB-04 | 高優先の到着で保存と停止完了のあとに優先タスクを開始する。中断Taskは同じTask IDの後続Runで保存地点から進む。 |
| HB-05 | タスクの固着と依存先の無応答を検出し、原因解消後に進行を確認する。BOARD復旧後はBOARD優先に戻る。 |
| HB-06 | CORE停止を外部監視が検知し、RenCrowシステムを依存順に再起動する。旧実行と完了済み操作を照合し、再開可能なタスクだけが続きから進む。 |

実装前に対応する失敗テストを固定する。開発時は選択、排他、永続化、配線の回帰テストで結果を確認し、失敗時は該当する実装単位の配備を保留する。配備後はインストール済みCORE、認証済み会話、実BOARDの担当・claim・返信・ACK、保存されたTask／Runを照合する。障害注入と自動再起動の受入には、停止の原因、復旧後の応答、旧Runと後続Runの状態を記録する。

## 現行実装の境界

- `tasks enqueue` は、Mio、Shiro、Midoriが担当する登録済み・未実行のローカル所有 `CHAT` Taskを受け付ける。保存済みのTaskタイトルを入力に、各エージェントがテキスト作業を行う。入力と結果はTaskの所有者が管理し、共有キューの表示には共通ラベルを使う。`tasks create` と `tasks queue` はTaskの登録と状態更新を行う。
- この登録経路の実行権限は、Taskにひも付くテキスト作業に限る。Kuroの現行実行先はフルアクセスの明示許可を前提とする。
- 専用テキストTaskは、Runを発行するときに入力のハッシュと実行の所有情報を保存する。結果が返れば、Taskを完了する前に結果を保存する。割り込みでは現在のRunを待機として閉じ、同じTaskを再開するキューを作る。結果が保存済みなら後続Runは生成を省いて完了する。生成中に止まった場合は後続Runで生成するため、停止時点によっては推論先で計算が重複し得る。
- 起動時は専用テキストTaskの実行記録を先に照合する。前のCOREが残したRunはチェックポイントから再開し、Taskが完了済みの場合はキューの記録を修復する。BOARDは保存済みの受付や応答を既存の配送経路で再利用する。外部変更を行う作業は、それぞれの実行経路で再開位置と副作用を確認する。
- 既存の収集やWorkstream処理でShiroを使う場合も、実行枠を確保できる周期に着手する。すでに開始した外部操作は、処理とTaskの記録が終わるまで実行枠を保持する。
- 空き時間の活動には、設定で有効にした既存のGames自発プレイを接続する。ゲームの判断と開始は既存の実行経路、各ターンの判断は参加エージェントの実行枠を使う。雑談、Web閲覧、制作の自動着手は未接続。
- macOSの外部復旧コマンドは `rencrow resilience stack-reconcile`。実機での運用には、全サービスと依存順を記述したroot所有の `/Library/Application Support/RenCrow/resilience-stack.v1.json` と、COREの外でコマンドを定期実行する権限付きサービスが必要。個々のTaskや依存先の停止原因を自動修復する処理は、ownerごとの実装と受入が必要。

## BOARD配送の回帰防止

安全エラー応答は、応答イベントに実行主体を設定せずに永続化される。公開処理が通常応答と同じ位置の主体を照合すると、保存済みの安全エラーがlease取得前に拒否され、配送が残り続ける。安全エラーの公開時は、保存済みの検証済みBOARD入力に結び付いたresponderを照合する。通常応答の主体照合は既存の応答イベントに従う。テストは正しいresponderでのclaim・公開完了と、別responderでの拒否を確認する。

返信は、COREが保存した元の送信とスレッド・宛先を照合できる場合に着手候補にする。元の送信記録がない返信は次周期で照合をやり直す。候補の判定ではclaimやACKを行わない。先に届いた未相関返信が、担当タスクや専用キューの着手を繰り返し占有しないことをテストで確認する。
