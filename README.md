# RenCrow_CORE

RenCrow_CORE は、人格を持つ会話、複数エージェントへのルーティング、記憶・Recall、作業実行、承認、継続作業、Debug Viewer による観測を一つの runtime にまとめる RenCrow システムの中核です。

CORE は外部モジュールの実装本体を抱え込まず、契約、ルーティング、状態、承認、監査、UI projection を所有します。LLM、STT、TTS、Vision、ゲーム世界、横断ツールは、それぞれ独立した RenCrow モジュールが担当します。

## 主な機能

- Mio、Shiro、Kuro、Midori を使い分ける会話とルーティング
- Worker、Coder、Advisor、Tool の責務分離
- 会話履歴、RecallPack、Knowledge Relation、provenance
- 承認ゲート、Workstream、Scheduler、Heartbeat
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

外部利用者向けの`view`／`live`／`lab`画面は独立した`RenCrow_PORTAL`が所有します。`live`は配信用、`lab`は操作可能なAI VTuber部屋として分離し、COREの通常Viewerはデバッグ・運用確認用として残します。

```bash
RENCROW_PORTAL_URL=http://127.0.0.1:18791 ./build/rencrow
```

この設定時、従来の`/viewer?mode=view|live|lab`はPORTALへリダイレクトされます。PORTALが未導入の環境では、変数を設定しなければ従来画面を互換経路として利用できます。

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

整理前の資料、旧仕様、解析データは保存用ブランチ [`archive/docs-classified-20260715`](https://github.com/Nyukimin/RenCrow_CORE/tree/archive/docs-classified-20260715) に残し、LLM 向けに人手で選別した Knowledge は [`knowledge/rencrow-core`](https://github.com/Nyukimin/RenCrow_CORE/tree/knowledge/rencrow-core) で管理します。これらは現在の公開仕様の正本ではありません。

## 開発と検証

```bash
go test ./modules/...
go test ./cmd/rencrow ./internal/features/... ./internal/adapter/viewer ./modules/...
go test ./...
go vet ./...
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
