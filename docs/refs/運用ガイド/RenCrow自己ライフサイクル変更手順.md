# RenCrow 自己ライフサイクル変更手順

## 目的

Repair / Coder Proposal / Worker 自動実行が、RenCrow 本体を自己再起動または自己 install しないようにする。

`picoclaw.service` の停止、再起動、`~/.local/bin/picoclaw` の上書きは manual approval が必要な運用作業として扱う。

## 自動実行禁止

Worker は以下を含む Proposal を自動実行しない。

- `picoclaw.service` の `start` / `stop` / `restart` / `reload` / `enable` / `disable`
- `picoclaw` プロセスへの `pkill` / `killall`
- `make install`
- `~/.local/bin/picoclaw` へのコピー、上書き、削除

## Coder / Repair の出力

Coder は上記が必要な場合、コマンドを実行対象に含めず、plan または risk に次を明記する。

- なぜ再起動または install が必要か
- どの service / binary が対象か
- 実行前に必要な確認
- 実行後に必要な health check

Worker は該当コマンドを検出した場合、`approval_required` として停止する。

## 承認後の手順

外側の operator が以下を順番に実行する。

```bash
systemctl --user stop picoclaw.service
```

port が閉じていることを確認する。

```bash
curl -fsS --max-time 2 http://127.0.0.1:18790/health
```

この確認では connection refused または timeout を期待する。

必要な build / install を実行する。

```bash
make install
```

service を起動する。

```bash
systemctl --user start picoclaw.service
```

health check を通す。

```bash
curl -fsS http://127.0.0.1:18790/health
```

IdleChat を止めた状態で維持したい場合は、起動後に明示停止する。

```bash
curl -fsS -X POST http://127.0.0.1:18790/viewer/idlechat/stop
```

## 禁止理由

自動修復ジョブが自分自身を再起動すると、実行中の HTTP connection、Repair 状態、IdleChat 停止状態、監査ログの連続性を失う可能性がある。

そのため、自己ライフサイクル変更は修復ジョブの内部処理ではなく、承認済みの外部運用として実行する。
