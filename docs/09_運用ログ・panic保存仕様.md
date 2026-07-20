# 運用ログ・panic保存仕様

## 目的

RenCrow_COREを止めたままにせず、異常停止、応答停止、再起動、自己修復を一つの事故ライフサイクルとして扱います。同時に、通常ログと同一事故の反復でディスクを増やし続けないようにします。

責務は次のように分離します。

- systemd: COREプロセス外から異常終了を検出し、必ず再起動する
- journalと日別アーカイブ: 直近7日間の連続した運用ログを保持する
- Go製`rencrow resilience`: 異常証拠の集約、生存監視、修復、再発確認、解決済み証拠のGCを行う
- CORE Repair: 再起動後に事故証拠を読み、実ファイルへの最小修正とtestを行う

COREプロセス自身だけに再起動責務を持たせません。panicやデッドロック後はプロセス内コードを実行できないため、外部supervisorを必須とします。

## 正式な記録先

Linuxのsystemd常用環境では、`rencrow.service`のstdoutとstderrをsystemd journalへ送ります。

```bash
journalctl --user -u rencrow.service --since "1 hour ago"
journalctl --user -u rencrow.service -f
```

systemdの起動、停止、終了コードと、COREが出力する通常ログ、panic、stackは同じunitの時系列として確認できます。panic時は`GOTRACEBACK=all`を使用し、panicしたgoroutineだけでなく全goroutineのstackをstderrへ出力します。

### Viewer requestの操作元ログ

`POST /viewer/send`の受付、非同期処理開始、完了、errorには、同じ`job_id`と次の操作元fieldを記録します。

```text
operation_source="RenCrow_PORTAL"
viewer_client_id="portal-..."
input_source=stt
user_id="viewer-user"
device_name="Linux x86_64"
source_ip_masked="192.168.1.x"
source_ip_hash=0123456789abcdef
user_agent="Mozilla/5.0 ..."
recipient=mio
```

- `operation_source`はserverが確認したclient種別、`viewer_client_id`はbrowser tab単位の相関です。
- `input_source`は`text`、`stt`、または未指定clientの`unknown`です。
- `user_id`と`device_name`はclient申告の観測値であり、認証・認可には使用しません。現行PORTALの`user_id`は`viewer-user`です。
- browser APIはhostnameを公開しないため、`device_name`にはOS／platform名を記録します。tabの区別には`viewer_client_id`を使用します。
- 接続元IPの生値はjournalへ書かず、IPv4末尾octetまたはIPv6 `/64`をマスクした値と、同一接続元を照合する短いSHA-256相関hashを記録します。
- User-Agentは制御文字を除去し、512文字まで記録します。

## 7日保持

`rencrow-log-rotate.timer`は1時間ごとに起動し、journalをUTC日付単位のgzipへ書き出します。

```text
~/.rencrow/logs/archive/
├── rencrow-2026-07-18.log.gz
├── rencrow-2026-07-17.log.gz
└── ...直近7日分
```

- 当日分は1時間ごとに安全な一時ファイルへ再出力し、完成後に置き換えます。
- 完了済みの過去日は再出力しません。
- 7日を超えた日別アーカイブは自動削除します。
- journalのrate limitはCORE unitだけ無効化し、高頻度ログ中のpanicやerrorを欠落させません。
- アーカイブは`0600`、アーカイブディレクトリは`0700`とします。

## 生存監視と再起動

`rencrow.service`は`Restart=always`で動作し、`StartLimitIntervalSec=0`により連続異常終了時にもsystemdが恒久停止しません。異常終了時の`ExecStopPost`は、終了理由、終了コード、直近journal、panic stackを事故台帳へ保存します。正常な手動停止は事故にしません。

`rencrow-resilience.timer`は30秒ごとに`GET /health/live`を確認します。このendpointはHTTPイベントループ自身だけを確認し、LLM、STT、TTS、DBなどの外部依存を確認しません。systemd上で`active/running`かつ起動後30秒を経過したCOREだけをprobeし、20秒未満に近接した手動確認はfailure回数へ重複計上しません。2回連続で2秒以内に応答しない場合だけハングと判定し、取得可能ならpprof goroutineを保存してからCOREを再起動します。再起動には2分のcooldownを設けます。

依存を含む総合状態は従来どおり`GET /health`、受付可能状態は`GET /ready`を使います。外部LLM停止を理由にCOREを再起動してはいけません。

## 事故台帳

事故台帳は`~/.rencrow/resilience/incidents/<signature>/`に置きます。

```text
incident.json
first.log.gz
latest.log.gz
first-goroutines.txt
latest-goroutines.txt
doctor-latest.json
```

panic stackなどから揮発値を除いて署名を作ります。同じ署名が反復した場合は、事故数と最終発生時刻を更新し、詳細証拠は初回と最新だけを残します。この集約により、未解決事故を削除せず容量を有界にします。

状態は次の順で遷移します。

```text
unresolved
  -> restart_recovered
  -> repair_requested
  -> repair_completed_pending_verification
  -> resolved
```

修復失敗時は`repair_failed`とし、同じ署名に対する自動修復は最大2回です。回数上限後は証拠を保持したまま人の確認を待ち、無限修復ループを作りません。

## 再起動後の自己修復

再起動後に`/health/live`が回復すると、resilience処理は次を実行します。

1. 設定されたCoder backendの`GET /v1/models`が成功することを確認する
2. `rencrow doctor --json`を実行し、事故ディレクトリへ保存する
3. panic、fatal、hang、abnormal exitだけをCOREの`POST /viewer/repair/run`へ渡す
4. Repairは実在するリポジトリファイルだけを対象に原因を特定し、最小変更を適用してtestする
5. Repair完了後、外部のresilience processが`go test ./...`と`go build`を再実行する
6. 合格したcandidate binaryだけを現在のbinaryへatomic renameし、systemdでCOREを再起動する
7. 24時間、同じ事故署名が再発しなければ`resolved`にする

Repair自身にはcommit、push、systemctl、再起動を行わせません。修正提案・適用と、検証・binary配備・再起動の権限を分離します。外部依存障害、設定不足、OOMなど、コード修復で扱うべきでない事故は自動修復対象にしません。Coder backendが停止中の場合は修復回数を消費せず、事故を`restart_recovered`のまま保持して5分後の再確認を待ちます。

既定の修復経路は`CODE2`です。`RENCROW_RESILIENCE_REPAIR_ROUTE=CODE1|CODE2|CODE3|CODE4`で明示変更できます。Repairは指定されたCoder slotを使い、別slotへ黙ってfallbackしません。

緊急時に自動修復だけを止め、liveness監視と再起動を続ける場合は、`rencrow-resilience.service`の`RENCROW_RESILIENCE_AUTO_REPAIR=false`を指定します。

## 解決済み証拠の削除

削除条件は期間だけで決めません。事故状態が`resolved`であることを必須とします。

- 未解決、修復中、修復失敗: 詳細証拠も台帳も自動削除しない
- 解決後7日: `incident.json`以外の大きな詳細証拠を削除する
- 解決後30日: compactな`incident.json`を含む事故ディレクトリを削除する
- 同じ署名が再発: 即座に未解決へ戻し、削除時計を取り消す

したがって「古いから消す」のではなく、「修復済みかつ再発確認済みだから段階的に消す」が正本ルールです。

## 導入と確認

```bash
make test-log-retention
make install-log-retention enable-log-retention
make install-resilience enable-resilience
systemctl --user restart rencrow.service

systemctl --user status rencrow.service --no-pager
systemctl --user status rencrow-log-rotate.timer --no-pager
systemctl --user status rencrow-resilience.timer --no-pager
make log-retention-run-once
rencrow resilience status
ls -lh ~/.rencrow/logs/archive/
```

`10-panic-stack.conf`は`rencrow.service`のdrop-inとして導入されます。drop-in反映にはCORE再起動が必要です。

## 障害調査の最小手順

```bash
# systemdの終了・再起動履歴
journalctl --user -u rencrow.service --since "7 days ago" \
  | grep -E "Main process exited|Scheduled restart|Started RenCrow|Stopping RenCrow"

# panicとstackの起点
journalctl --user -u rencrow.service --since "7 days ago" \
  | grep -n -E "panic:|fatal error:|SIGSEGV|goroutine [0-9]+"

# 保存済み日別ログ
gzip -cd ~/.rencrow/logs/archive/rencrow-YYYY-MM-DD.log.gz | less
```

プロセス再起動とHTTPの一時的な応答停止は別の事象です。終了コード、PID、`/health`、panic stack、外部依存のhealthを分けて判定します。
