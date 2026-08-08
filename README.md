# RenCrow_CORE

RenCrow_CORE は、人格を持つ会話、複数エージェントへのルーティング、記憶・Recall、作業実行、同期Policy Decision、継続作業、Debug Viewer による観測を一つの runtime にまとめる RenCrow システムの中核です。

CORE は外部モジュールの実装本体を抱え込まず、契約、ルーティング、状態、同期policy、監査、UI projection を所有します。LLM、STT、TTS、Vision、画像生成、ゲーム世界、横断ツールは、それぞれ独立した RenCrow モジュールが担当します。

RenCrow_Workspaceは外部runtime moduleではなく、実行時の正本
`~/.rencrow/workspace`をbackup／復旧するためのportableな非secret snapshotです。

## 主な機能

- Mio、Shiro、Kuro、Midori を使い分ける会話とルーティング
- Worker、Coder、Advisor、Tool の責務分離
- 会話履歴、RecallPack、Knowledge Relation、provenance
- Go HTTP/XMLによるWeb検索・ニュース検索、14既定sourceの日次ニュースSQLite
- Policy Decision、安全gate、Workstream、Scheduler、Heartbeat
- Opportunity、EconomicTask、RevenueEvent、Reflection の安全な管理
- Viewer REST/SSE、状態表示、ログ、ジョブ・エージェント観測
- LLM、STT、TTS、Browser、外部 runtime との接続契約
- GPU空き状態を確認して実行するTTS発音日次チェック

## クイックスタート

必要条件は Go 1.25 以降です。外部 LLM などを利用する場合は、その runtime も別途用意してください。

```bash
cp config/config.yaml.example config.yaml
# config.yaml の endpoint、model、保存先を環境に合わせて編集
make build
./build/rencrow
```

既定の設定ファイルは作業ディレクトリの `./config.yaml` です。別の場所を使う場合は `RENCROW_CONFIG` を指定します。

```bash
RENCROW_CONFIG=/path/to/config.yaml ./build/rencrow
curl http://127.0.0.1:18790/health
```

API key や token はリポジトリへ保存せず、`${ENV_VAR}` 形式で環境変数から展開してください。

## Go配布境界

標準配布はCOREのGo binaryと設定を基本とし、Python／Node.jsを必須runtimeにしません。
Browser Actor、Webwright、`rencrow-data`はoptional sidecar／運用機能です。PORTAL／GAMESの
ブラウザJavaScriptはブラウザで実行します。

COREと各moduleのnative Go process、公開contract、Config、health、errorの意味はUbuntu、
Windows、macOSで共通にします。外部systemは所有moduleの境界外へ隔離し、未配置時は対象機能だけを
`disabled`または`unavailable`にします。WindowsのCUDA用WSLはGPU外部computeだけの特例であり、
CORE、database、news収集、一般sidecarをWSLへ移しません。

標準検索はcredential不要の`bing_rss`、速報検索は`bing_news_rss`をGo binary内で扱います。
検索結果の上位URLは同じ`web_gather.search_and_fetch`契約で本文取得します。日次ニュースは
既定14 sourceを既存設定を上書きせずL1 source registryへ登録し、検証済み項目を
`l1_memory.db`へ永続化します。SearXNG／YaCyは明示設定時だけ使う代替providerです。

映画カタログのdomain、DB、検索、評価、Public API、importはCOREが所有します。外部サイトの
巡回CrawlerはRenCrow_Toolsのoptional Go sidecarへ分離し、COREからPythonを直接起動しません。
Vision／Imageを含む標準配布対象と外部computeの正本は
[標準Go配布境界](docs/04_アーキテクチャ概要.md#標準go配布境界)を参照してください。

## 永続データ配置

Ubuntuの基準配置では、稼働データを`/srv/rencrow/db`、別媒体のbackupを
`/srv/rencrow/backup`へ分離します。COREは`/srv/rencrow/db/core`、TRADE、Image、GAMES、
Toolsはそれぞれ同名のmodule subtreeだけを所有します。映画・趣味catalog、会話Memory、
COREが採用した生成物のdomain metadataはCOREの所有範囲ですが、外部取得artifact、画像object、
Replayなどは生成したmoduleが所有します。

`/srv/rencrow/db`が利用できないproduction起動でrepository内、`~/.rencrow`、一時directoryへ
暗黙fallbackしてはいけません。Windows／macOSでは同じ論理構造を設定済み絶対pathへ配置します。
媒体format、mount、module別subtree、backup整合性の正本は
[設定リファレンスの「DB物理配置とbackup」](docs/05_設定リファレンス.md#db物理配置とbackup)です。
RenCrow_WorkspaceのGit snapshotはこのlive dataやbackupの代替ではありません。

外部利用者向けの`Chat`／`IdleChat`画面は独立した`RenCrow_PORTAL`が所有します。COREの`/viewer`はデバッグ・運用確認専用です。

## 運用ログ

Linuxの常用環境では、COREのstdout/stderrをsystemd journalへ記録し、panic時は`GOTRACEBACK=all`で全goroutineのstackを残します。journalは1時間ごとに日別gzipへ書き出し、直近7日分を`~/.rencrow/logs/archive/`へ保持します。

CORE自身のpanic・異常終了・ハングは`rencrow-resilience.timer`が監視します。systemdがプロセスを再起動し、Go製のresilience処理が事故証拠の集約、`doctor`、Repairジョブ、修正後のtest/build/atomic install、24時間の再発監視を行います。未解決事故は削除せず、同じ事故を署名でまとめて容量を制限します。

```bash
make install-log-retention enable-log-retention
make install-resilience enable-resilience
systemctl --user restart rencrow.service

journalctl --user -u rencrow.service -f
make log-retention-status
make log-retention-run-once
ls -lh ~/.rencrow/logs/archive/
```

panic調査では、journalと日別アーカイブの両方を確認します。詳細は[運用ログ・panic保存仕様](docs/09_運用ログ・panic保存仕様.md)を参照してください。

## ドキュメント

公開仕様は [docs/README.md](docs/README.md) から読めます。実装状況は [docs/08_実装状況・ロードマップ.md](docs/08_実装状況・ロードマップ.md) に、公開 API の安定性区分は [docs/06_Public_API仕様.md](docs/06_Public_API仕様.md) に記載しています。

IrodoriTTSの発音日次チェックは[feature README](internal/features/pronunciationcheck/README.md)を参照してください。実行時刻とGPU admissionはCOREが所有し、RenCrow_TTSはチェックTool APIだけを提供します。

## 開発と検証

```bash
go test ./modules/...
go test ./cmd/rencrow ./internal/features/... ./internal/adapter/viewer ./modules/...
go test ./...
go vet ./...
```

ローカルWindowsでは、test生成物をrepo内の`Tmp/test-runtime/`へ限定します。

```powershell
.\scripts\test-local.ps1
.\scripts\test-local.ps1 -Step go
.\scripts\test-rencrow-system.ps1
```

- `modules/*`: 外部利用可能な契約と純粋 policy
- `internal/features/*`: feature 単位の route・依存境界
- `internal/domain/*`: domain type と validation
- `internal/application/*`: use case と orchestration
- `internal/adapter/*`: Viewer、channel、provider adapter
- `internal/infrastructure/*`: persistence と技術実装
- `cmd/rencrow`: process composition root

貢献方法は [CONTRIBUTING.md](CONTRIBUTING.md)、脆弱性報告は [SECURITY.md](SECURITY.md) を参照してください。

## License

MIT License。詳細と attribution は [LICENSE](LICENSE) を参照してください。
