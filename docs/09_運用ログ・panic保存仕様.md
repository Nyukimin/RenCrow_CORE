# 運用ログ・panic保存仕様

## 目的

RenCrow_COREを止めたままにせず、異常停止、応答停止、再起動、自己修復を一つの事故ライフサイクルとして扱います。同時に、通常ログと同一事故の反復でディスクを増やし続けないようにします。

責務は次のように分離します。

- systemd: COREプロセス外から異常終了を検出し、必ず再起動する
- journalと日別アーカイブ: 直近7日間の連続した運用ログを保持する
- Go製`rencrow resilience`: 異常証拠の集約、生存監視、修復、再発確認、解決済み証拠のGCを行う
- CORE Repair: 再起動後に事故証拠を読み、実ファイルへの最小修正とtestを行う
- storage backup timer: 6時間ごとにCOREと記憶backendを一つの復旧単位として保存・検査する

COREプロセス自身だけに再起動責務を持たせません。panicやデッドロック後はプロセス内コードを実行できないため、外部supervisorを必須とします。

本書は journal集約、日別アーカイブ、事故台帳、自己修復といった**運用面**を規定します。アプリケーションが**何をどの形式で出力するか**は `10_ログ仕様.md` を正本とします。本書の手順はLinuxのsystemd環境を前提としており、Windows／macOSでの収集手段は異なります。アプリケーション側の実装をsystemd前提にしないでください。

## Ubuntuホストのフリーズ復旧補足

これはRenCrowプロセスの監視とは別の、Ubuntuホスト自身の復旧要件です。CORE、PORTAL、LLM、Toolsのいずれかが停止していても動作し、RenCrowのHTTP endpoint、ユーザーservice、repository内の設定には依存しません。Ubuntuのsystemd system unitとkernel sysctlだけを使います。

### 目的と限界

次の順序を実現します。

```text
kernel soft/hard lockup検知
  -> kernel panic
  -> 設定した待機時間
  -> 自動reboot
  -> 次回bootで前回bootの終了形とkernel証拠を検証
```

これはCPUがlockupを検知できる場合に限って有効です。電源断、ACアダプター／バッテリー瞬断、基板・RAM故障、GPU完全停止、ファームウェア停止ではkernelが処理を続けられず、自動rebootも証拠保存もできないことがあります。データ書き込み中の再起動による破損リスクがあるため、導入前に重要データの検証済みbackupを作成します。

### 構成要件

- `/etc/sysctl.d/99-rencrow-host-recovery.conf` で `kernel.watchdog=1`、`kernel.softlockup_panic=1`、`kernel.hardlockup_panic=1`、`kernel.panic=10` を設定する。
- systemd system service `ubuntu-shutdown-audit.service` をboot後に一度実行し、`journalctl --list-boots` と前回bootのshutdown markerを突合する。
- 検証結果はRenCrowのworkspaceやrepositoryではなく、`/var/lib/ubuntu-shutdown-audit/` に保存する。
- 前回bootが `Reached target System Power Off` または `Reached target System Reboot` と `systemd-shutdown` を持つ場合だけ `normal_shutdown` とする。それ以外は `unexpected_termination` とし、原因を断定しない。
- 前回bootのkernel error、panic、OOM、watchdog、MCE、GPU、storage、thermalの該当行を、次回bootのreportに保存する。秘密値やユーザー会話本文は保存しない。
- `/dev/watchdog` が存在し、機種のhardware watchdogを検証できる場合だけ追加採用する。存在しない機種へ無理にkernel moduleを挿入しない。

### 導入手順

導入はroot権限で行い、変更前の値とunitを退避します。設定反映後に意図的なpanic試験は行わず、次回の自然な事象で検証します。

```bash
UBUNTU_AUDIT_SRC=/path/to/RenCrow_CORE/docs/ubuntu-host-recovery
sudo install -d -m 0750 /var/lib/ubuntu-shutdown-audit/reports
sudo install -d -m 0755 /usr/local/libexec
sudo install -m 0644 "$UBUNTU_AUDIT_SRC/99-rencrow-host-recovery.conf" /etc/sysctl.d/99-rencrow-host-recovery.conf
sudo install -m 0755 "$UBUNTU_AUDIT_SRC/ubuntu-shutdown-audit" /usr/local/libexec/ubuntu-shutdown-audit
sudo install -m 0644 "$UBUNTU_AUDIT_SRC/ubuntu-shutdown-audit.service" /etc/systemd/system/ubuntu-shutdown-audit.service
sudo sysctl --system
sudo systemctl daemon-reload
sudo systemctl enable --now ubuntu-shutdown-audit.service
```

`docs/ubuntu-host-recovery/` は再現可能なinstall sourceを置く場所であり、実行時にRenCrowをimport、起動、監視するものではありません。`UBUNTU_AUDIT_SRC` は実際に取得したsource directoryへ置き換えます。

確認条件は次のとおりです。

```bash
sysctl kernel.watchdog kernel.softlockup_panic kernel.hardlockup_panic kernel.panic
systemctl is-enabled ubuntu-shutdown-audit.service
systemctl status ubuntu-shutdown-audit.service --no-pager
sudo find /var/lib/ubuntu-shutdown-audit -maxdepth 2 -type f -ls
```

`kernel.panic=10` はpanic後10秒でrebootする値です。画面にpanicが表示されても、再起動までの10秒間に電源断を行わないでください。通常の手動shutdownを異常扱いしないため、導入直後に一度通常のrebootを行い、reportが `normal_shutdown` になることを確認します。

### 次回異常停止後の検証

```bash
sudo ls -lt /var/lib/ubuntu-shutdown-audit/reports
sudo sed -n '1,220p' /var/lib/ubuntu-shutdown-audit/reports/<latest>.env
journalctl --list-boots --no-pager
journalctl -b -1 -k -p warning..alert --no-pager
```

`unexpected_termination` は「ログが正常終了markerなしに途切れた」という事実を示すだけで、kernel、電源、ハードウェアのどれかを自動確定しません。画面写真、電源状態、SMART、memtestの結果と組み合わせて判定します。

### 無効化とrollback

自動rebootが業務上危険、または誤検知が疑われる場合は、まずpanic設定を戻してからunitを停止します。

```bash
sudo sysctl -w kernel.softlockup_panic=0 kernel.hardlockup_panic=0 kernel.panic=0
sudo systemctl disable --now ubuntu-shutdown-audit.service
```

再導入前に、`/etc/sysctl.d/99-rencrow-host-recovery.conf` と `/etc/systemd/system/ubuntu-shutdown-audit.service` の退避版との差分を確認します。収集済みreportは原因確認が終わるまで削除しません。

## Backupの運用記録

`rencrow-storage-backup.service`のstdout／stderrはjournalへ記録します。各実行は少なくともCORE停止・再開、snapshot検証結果、保存先、Knowledge mirror結果を出力します。

```bash
journalctl --user -u rencrow-storage-backup.service --since "24 hours ago"
systemctl --user list-timers rencrow-storage-backup.timer
```

Redis、Qdrant、mount、圧縮、checksum、復元検証のどれかが失敗した場合、serviceはnon-zeroで終了します。失敗途中のstagingは削除し、取得済みQdrant server snapshotもcleanupし、停止前にCOREがactiveだった場合はCOREを再開します。直前の検証済み世代は削除しません。timerの次回実行を待つだけにせず、原因解消後に`make storage-backup-run-once`を実行し、新しい検証済み世代が作られたことを確認します。

backup windowはrunnerが`RENCROW_BACKUP`をmountして所有し、終了時に元のunmount状態へ戻します。整合snapshot中は
`rencrow.service`をruntime maskし、deploy、手動restart、resilienceを含む別経路からのstartを失敗させます。
成功・失敗・signalの全終了経路でmaskを解除し、開始前にactiveだったCOREだけを再開します。
再開時は設定済みportのloopback `/health/ready`を最大300秒検査し、`ready=true`になるまで
`CORE restarted`と記録しません。長時間Agent RunはSuperAgent owner storeのcheckpointと期限付きleaseを正本とし、
backup停止中に失効したclaimは再起動後のschedulerが同じrun／checkpoint identityで回収します。

## 正式な記録先

Linuxのsystemd常用環境では、`rencrow.service`のstdoutとstderrをsystemd journalへ送ります。

repository内の `systemd/user/rencrow.service` をproduction unitの正本とします。`install.sh` はこのunitを `~/.config/systemd/user/rencrow.service` へコピーし、inline生成しません。CORE同梱promptはinstall時に`%h/.local/share/rencrow/prompts`へcopyし、正本unitはportable install pathとして`WorkingDirectory=%h/.local/share/rencrow`、`ExecStart=%h/.local/bin/rencrow run`、`EnvironmentFile=%h/.rencrow/.env`、optionalな`EnvironmentFile=-%h/.rencrow/llm_ops.env`、`RENCROW_CONFIG=%h/.rencrow/config/core.yaml`を使います。再起動契約は`Restart=always`、`RestartSec=5`、`StartLimitIntervalSec=0`です。journal契約は`StandardOutput=journal`、`StandardError=journal`、`LogRateLimitIntervalSec=0`、`LogRateLimitBurst=0`です。

### Configとdrop-inの所有境界

- live `core.yaml` owns `games.observer_url`, `movie_catalog.crawler_url`, and `person_related_catalog.provider_url`。これらのbackend endpointをsystemd drop-inへ複製しません。
- `codex.command` owns the executable absolute path。PATHを補うhost固有drop-inは配布しません。
- trade section owns its API endpoint; the operator manages the optional service lifecycle independently。CORE unitはtradeのhost固有URLやservice lifecycleを所有しません。

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

`rencrow-resilience.timer`はuser manager起動3分後から2分ごとに`GET /health/live`を確認します。このendpointはHTTPイベントループ自身だけを確認し、LLM、STT、TTS、DBなどの外部依存を確認しません。systemd上で`active/running`かつ起動後180秒を経過したCOREだけをprobeし、20秒未満に近接した手動確認はfailure回数へ重複計上しません。production起動時の初期化が完了する前に再起動しないため、timerの初回遅延とprocess側graceは実測約145秒を上回る値とします。2回連続で2秒以内に応答しない場合だけハングと判定し、取得可能ならpprof goroutineを保存してからCOREを再起動します。再起動には2分のcooldownを設けます。

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

## LINE通知deploymentとrollback

LINE通知のproduction反映では、実際のunit、WorkingDirectory、ExecStart、EnvironmentFilesを`systemctl --user show`で確認します。binaryを置き換える前に現在のExecStart先をtimestamp付きで退避し、repositoryのtestとbuildを通してからatomicなinstallを行います。

```bash
systemctl --user show rencrow.service \
  --property=Environment --property=EnvironmentFiles \
  --property=ExecStart --property=WorkingDirectory
cp "$HOME/.local/bin/rencrow" \
  "$HOME/.local/bin/rencrow.before-line-notify-$(date -u +%Y%m%d_%H%M%SZ)"
make install
systemctl --user daemon-reload
systemctl --user restart rencrow.service
curl -fsS http://127.0.0.1:18790/health/live
```

外部Webhook URLは`tailscale serve status`と`tailscale funnel status`で現在のhostと転送先を確認してから、LINE Developersへ`https://<current-host>/webhook/line`を登録します。LINEから到達させる443番はFunnelを使います。同じportのFunnelはpath単位ではなくport全体をpublic扱いにするため、CORE側guardはtailscaledの`Tailscale-Funnel-Request`があるtrafficを`POST /webhook/line`だけに制限します。tailnet内のServe trafficは従来のViewer系allowlistを維持します。更新前のURLも記録し、rollback時に戻せるようにします。秘密値と完全なLINE IDはshell history、journal、調査文書へ出しません。

rollbackは新binaryを停止して退避binaryへ戻し、serviceを起動します。通知先fallbackを今回の登録前へ戻す必要がある場合だけ、CORE停止中に`workspace_dir/state/line_notification_target`を別名へ退避します。削除はしません。

```bash
systemctl --user stop rencrow.service
cp "$HOME/.local/bin/rencrow.before-line-notify-<UTC timestamp>" \
  "$HOME/.local/bin/.rencrow.rollback"
chmod +x "$HOME/.local/bin/.rencrow.rollback"
mv "$HOME/.local/bin/.rencrow.rollback" "$HOME/.local/bin/rencrow"
systemctl --user start rencrow.service
```

Funnelだけをrollbackする場合は、LINE DevelopersのWebhook URLを直前の値へ戻してから次を実行します。

```bash
tailscale funnel --https=443 off
tailscale serve --https=443 --bg --yes http://127.0.0.1:18790
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
