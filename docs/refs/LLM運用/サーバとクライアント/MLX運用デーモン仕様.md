# MLX 運用デーモン（Chat / Worker 常駐）

**用途**: RenCrow で **Chat（会話）** と **Worker（構造化 JSON 作業）** を MLX 上の OpenAI 互換 API に載せるときの、**LLM サーバ側**の運用仕様（管理デーモン + 推論ポート）。現行の role / model / port は [`../LLM/LLM仕様.md`](../LLM/LLM仕様.md) を参照。

## RenCrow Chat / Worker とポート・モデル名

| 役割 | 推論 API（OpenAI 互換） | TCP ポート | `local_llm` で渡す model 名の例 |
|------|-------------------------|------------|-----------------------------------|
| **Chat** | `http://<HOST>:8081/v1/...` | **8081** | `Chat` |
| **Worker** | `http://<HOST>:8082/v1/...` | **8082** | `Worker` |
| **Wild**（現運用） | `http://<HOST>:8081/v1/...` | **8081** | `Chat` |
| **Wild専用**（将来任意） | `http://<HOST>:8083/v1/...` | **8083** | `Wild` |
| **管理**（本デーモン） | `http://<HOST>:8079/...` | **8079** | （推論には使わない） |

- 推論のエンドポイントは OpenAI 互換の **`/v1/chat/completions`** を前提にする。
- 正しい推論リクエストは **Chat = `http://<HOST>:8081/v1/chat/completions` model `Chat`**、**Worker = `http://<HOST>:8082/v1/chat/completions` model `Worker`**。
- Chat / Worker 推論サーバは **`/ready` を実装しない**。`http://<HOST>:8081/ready` や `http://<HOST>:8082/ready` を readiness 判定に使うのは仕様違反。
- 推論サーバの生存確認は `GET /health`、または `max_tokens: 1` の軽量な `POST /v1/chat/completions` を使う。
- 現運用では Chat と Worker の2プロセス構成。Wildは専用プロセスを使わず、Chatの `8081` / model `Chat` へ集約する。
- `8083` / model `Wild` は、Wild専用プロセスを後から起動した場合だけ使う予約構成である。
- RenCrow 設定例（要点）: `local_llm.enabled: true` とし、role ごとの `base_url` と `model` を [`../LLM/LLM仕様.md`](../LLM/LLM仕様.md) に合わせる。初回ロードを考え **`timeout_sec` は 120 秒以上**、`global_concurrency` / `model_concurrency` は MLX 負荷に合わせて抑える。
- **Viewer（Ops タブ）**: RenCrow 側で `llm_ops.enabled: true` かつ環境変数 **`LLM_OPS_TOKEN`** を設定すると、ブラウザから **状態取得 / Chat+Worker 停止 / 全ロール再起動** が可能（RenCrow が管理 API をサーバ側プロキシし、トークンはブラウザに出さない）。

## 何ができるか

- **起動直後**：管理 API を先に待受し、`Chat` と `Worker` が死んでいればバックグラウンドで復旧（重いモデルでも操作 API は先に応答）。
- **定期ヘルス**：既定 300 秒ごとに各ロールの `http://127.0.0.1:<port>/health` を確認し、異常時は自動で `mlx-restart` 相当。
- **`/v1/control/stop`**：プロセス停止。**停止後は自動復旧しない**（`halted` 扱い。外部から明示 `restart` まで止めたまま）。
- **`/v1/control/restart`**：**再起動**。`halted` を解除し、ポート上のサーバを立て直してヘルス待ちまで行う。
- **`/health`**（管理ポート）：デーモン生存確認（認証なし）。
- **`/v1/status`**：**Bearer 必須**。各ロールのヘルスと `halted` フラグを JSON で返す。

## 環境変数

| 変数 | 既定 | 説明 |
|------|------|------|
| `LLM_OPS_TOKEN` | （必須） | `Authorization: Bearer ...` と完全一致させる。**外部公開時は強い乱数**。 |
| `LLM_OPS_HOST` | `0.0.0.0` | 管理 HTTP の bind アドレス。 |
| `LLM_OPS_PORT` | `8079` | 管理 HTTP のポート。ファイアウォール開放対象に含める。 |
| `LLM_OPS_HEALTH_INTERVAL` | `300` | ウォッチドッグの周期（秒）。CLI `--interval` で上書き可。 |

## 手動起動

リポジトリルートで:

```bash
export LLM_OPS_TOKEN='強いシークレット'
uv run mlx-mgmt Chat Worker
```

または:

```bash
chmod +x scripts/start_mlx_mgmt_daemon.sh
# .env.mlx-mgmt に LLM_OPS_TOKEN=...
./scripts/start_mlx_mgmt_daemon.sh
```

## 外部 PC からの curl 例

`<HOST>` を Mac の LAN IP に置換。ポート **8079（管理）**・**8081（Chat）**・**8082（Worker）** を許可すること。

デーモン生存:

```bash
curl -s http://<HOST>:8079/health
```

状態（認証あり）:

```bash
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" http://<HOST>:8079/v1/status
```

両方強制停止:

```bash
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" -H 'Content-Type: application/json' \
  -d '{"roles":["Chat","Worker"]}' -X POST http://<HOST>:8079/v1/control/stop
```

または全部（管理対象のロールのみ）:

```bash
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" -H 'Content-Type: application/json' \
  -d '{"roles":"all"}' -X POST http://<HOST>:8079/v1/control/stop
```

強制再起動:

```bash
curl -s -H "Authorization: Bearer ${LLM_OPS_TOKEN}" -H 'Content-Type: application/json' \
  -d '{"roles":"all"}' -X POST http://<HOST>:8079/v1/control/restart
```

## launchd でログイン後常駐

1. `uv` のフルパスを `which uv` で確認し、`scripts/launchd/com.llm-server.mlx-mgmt.plist.example` をコピーしてパス類を編集する。
2. `LLM_OPS_TOKEN` を plist の `EnvironmentVariables` に設定するか、起動時に環境ファイルを読み込むようにラップする。
3. `mkdir -p run` が済んでいること。
4. `launchctl load ~/Library/LaunchAgents/com.llm-server.mlx-mgmt.plist` （配置場所に合わせる）。

※ `Chat` と `Worker` の OpenAI API はそれぞれ **8081 / 8082**。管理 API **8079** は強いトークンを前提にし、`stop` が任意停止できるため公開範囲は VPN のみなどにすることを推奨する。
