# LLM Ops `/v1/status` クライアント仕様（RenCrow Viewer向け）

## 接続先

- `GET http://192.168.1.31:8079/v1/status`
- Header: `Authorization: Bearer ${LLM_OPS_TOKEN}`

失敗時:

- `401`: トークン不正/未設定
- `503` または timeout: 管理デーモン未起動 or 到達不可

## 既存項目（継続）

- `memory.system.total_bytes` / `total_gib`
- `memory.system.free_bytes` / `free_gib`
- `memory.system.used_bytes` / `used_gib`
- `memory.llm_by_role.Chat.pid` / `rss_bytes` / `rss_mib`
- `memory.llm_by_role.Worker.pid` / `rss_bytes` / `rss_mib`

ロール停止時は `pid/rss_* = null`。

## 追加項目（新規）

`memory.system` 配下に以下を追加済み。

- `available_for_llm_bytes`
- `available_for_llm_gib`
- `used_for_llm_bytes`
- `used_for_llm_gib`
- `llm_safety_margin_bytes`（固定 8 GiB）
- `llm_safety_margin_gib`（固定 8.0）
- `safe_available_for_llm_bytes`
- `safe_available_for_llm_gib`

## 計算定義

- `available_for_llm = (Pages free + Pages speculative) * page_size`
- `used_for_llm = total - available_for_llm`
- `safe_available_for_llm = max(0, available_for_llm - 8GiB)`

## クライアント判定ルール（推奨）

モデル切替可否は `safe_available_for_llm_bytes` を使う。

- 入力: `required_bytes`（候補モデルの必要メモリ見積）
- 判定:
  - `safe_available_for_llm_bytes > required_bytes` -> 切替可
  - それ以外 -> 切替不可

## 取得例

```bash
curl -sS \
  -H "Authorization: Bearer ${LLM_OPS_TOKEN}" \
  http://192.168.1.31:8079/v1/status
```

## Viewer表示例

- System:
  - `total_gib`, `free_gib`, `used_gib`
  - `available_for_llm_gib`, `safe_available_for_llm_gib`
- Role:
  - `Chat.rss_mib`
  - `Worker.rss_mib`
