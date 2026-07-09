# RenCrow向け LLMメモリ監視API仕様

このドキュメントは、RenCrowクライアントからMac上のLLMサーバ使用メモリを取得するための仕様です。

## 1. エンドポイント

- `GET /v1/status`
- `GET /mgmt/v1/status`（同内容）

管理デーモンのデフォルトポートは `8079` です。

例:

```bash
curl -s \
  -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  http://127.0.0.1:8079/v1/status
```

## 2. 認証

- `Authorization: Bearer <LLM_OPS_TOKEN>` が必須
- トークン不一致または未指定時は `401`

## 3. 取得できるメモリ項目

`memory` 配下に以下を返します。

- `memory.system.total_bytes`: MacのRAM総量（bytes）
- `memory.system.total_gib`: MacのRAM総量（GiB）
- `memory.system.free_bytes`: 空き容量（bytes）
- `memory.system.free_gib`: 空き容量（GiB）
- `memory.system.used_bytes`: 使用中容量（bytes）
- `memory.system.used_gib`: 使用中容量（GiB）
- `memory.system.available_bytes`: OS全体の利用可能RAM（bytes）
- `memory.system.available_gib`: OS全体の利用可能RAM（GiB）
- `memory.system.available_for_llm_bytes`: LLM切替判定用の利用可能RAM（bytes）
- `memory.system.available_for_llm_gib`: LLM切替判定用の利用可能RAM（GiB）
- `memory.system.used_for_llm_bytes`: LLM切替判定用の使用中RAM（bytes）
- `memory.system.used_for_llm_gib`: LLM切替判定用の使用中RAM（GiB）
- `memory.system.llm_safety_margin_bytes`: LLM向け安全マージン（bytes）
- `memory.system.llm_safety_margin_gib`: LLM向け安全マージン（GiB）
- `memory.system.safe_available_for_llm_bytes`: 安全マージン差し引き後の利用可能RAM（bytes）
- `memory.system.safe_available_for_llm_gib`: 安全マージン差し引き後の利用可能RAM（GiB）
- `memory.system.swap_used_bytes`: swap使用量（bytes）
- `memory.system.swap_used_gib`: swap使用量（GiB）
- `memory.system.memory_pressure`: メモリ圧迫状態（例: `normal`, `warning`, `critical`）
- `memory.system.compressed_bytes`: compressed memory（bytes）
- `memory.system.compressed_gib`: compressed memory（GiB）
- `memory.system.file_cache_bytes`: file cache（bytes）
- `memory.system.file_cache_gib`: file cache（GiB）
- `memory.system.wired_bytes`: wired memory（bytes）
- `memory.system.wired_gib`: wired memory（GiB）
- `memory.llm_by_role.Chat`: Chat LLMプロセス情報
- `memory.llm_by_role.Worker`: Worker LLMプロセス情報
  - `pid`: 対象プロセスPID
  - `rss_bytes`: プロセスRSS（bytes）
  - `rss_mib`: プロセスRSS（MiB）
- `memory.top_memory_processes`: RSS順の上位プロセス一覧
- `memory.model_processes`: Chat / Worker などモデルプロセス一覧

`top_memory_processes` / `model_processes` の各要素は以下を推奨します。

- `pid`
- `name`
- `command`
- `rss_bytes`
- `rss_mib`
- `role`（モデルプロセスの場合）
- `model`（モデルプロセスの場合）
- `port`（モデルプロセスの場合）

## 4. レスポンス例

```json
{
  "roles": {
    "Chat": {
      "health_ok": true,
      "detail": "{\"status\":\"ok\"}",
      "halted": false
    },
    "Worker": {
      "health_ok": true,
      "detail": "{\"status\":\"ok\"}",
      "halted": false
    }
  },
  "halted": [],
  "memory": {
    "system": {
      "total_bytes": 137438953472,
      "total_gib": 128.0,
      "free_bytes": 24823832576,
      "free_gib": 23.12,
      "used_bytes": 112615120896,
      "used_gib": 104.88
    },
    "llm_by_role": {
      "Chat": {
        "pid": 12345,
        "rss_bytes": 21474836480,
        "rss_mib": 20480.0
      },
      "Worker": {
        "pid": 12346,
        "rss_bytes": 32212254720,
        "rss_mib": 30720.0
      }
    }
  }
}
```

## 5. クライアント実装時の注意

- `rss_*` はプロセス単位の常駐メモリです。モデル本体に加え、ランタイムやキャッシュも含みます。
- 該当ロール停止中は `pid`, `rss_bytes`, `rss_mib` が `null` になります。
- `free_*` は `vm_stat` ベースのため瞬間的に増減します。UI表示は移動平均やしきい値判定を推奨します。
- 監視ポーリングは `2〜10秒` 程度を推奨します（過度な短周期は不要）。

## 6. RenCrow側の利用イメージ

1. 起動時に `GET /v1/status` を取得  
2. `memory.system.free_gib` が閾値未満なら重いタスクを抑制  
3. `memory.llm_by_role.Chat/Worker.rss_mib` を表示・記録  
4. 必要に応じて既存の管理API（`/v1/control/stop`, `/v1/control/restart`）と組み合わせる
