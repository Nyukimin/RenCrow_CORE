# LLM 運用 最新情報

**更新日**: 2026-05-11

この文書は、`docs/LLM運用/` の現行情報への入口である。

## 現行構成

RenCrow_LLM は OpenAI 互換 API として動作する。

現行の公開 model 名は以下の 4 つ。

| Role | Public URL | Backend URL | Public model | 主用途 |
| --- | --- | --- | --- | --- |
| Chat | `http://127.0.0.1:8081` | `http://127.0.0.1:18081` | `Chat` | 会話、音声 UI、軽い応答 |
| Worker | `http://127.0.0.1:8082` | `http://127.0.0.1:18082` | `Worker` | 要約、整理、RAG、実務処理 |
| Heavy | `http://127.0.0.1:8083` | `http://127.0.0.1:18083` | `Heavy` | 深い分析、失敗原因調査、最終レビュー |
| Wild | `http://127.0.0.1:8084` | `http://127.0.0.1:18084` | `Wild` | 創作、画像プロンプト、創作用画像解析 |

`Coder` は現行 proxy ではクライアントへ公開しない。コード修正方針、影響範囲、テスト観点は `Worker` を使う。

## 主要仕様

- LLM role、モデル割り当て、起動方式: [`../LLM/LLM仕様.md`](../LLM/LLM仕様.md)
- RenCrow から呼ぶ OpenAI 互換 API: [`../サーバとクライアント/OpenAI互換API仕様.md`](../サーバとクライアント/OpenAI互換API仕様.md)
- クライアント側の role 切り替え: [`../サーバとクライアント/LLM切り替え仕様.md`](../サーバとクライアント/LLM切り替え仕様.md)
- 管理 API による起動・停止・再起動: [`../サーバとクライアント/管理API_起動再起動仕様.md`](../サーバとクライアント/管理API_起動再起動仕様.md)
- MLX 運用デーモン: [`../サーバとクライアント/MLX運用デーモン仕様.md`](../サーバとクライアント/MLX運用デーモン仕様.md)
- LLM メモリ監視 API: [`../サーバとクライアント/LLMメモリ監視API仕様.md`](../サーバとクライアント/LLMメモリ監視API仕様.md)
- Viewer の LLM Ops status 表示: [`../サーバとクライアント/Viewer_LLM_Ops_Status仕様.md`](../サーバとクライアント/Viewer_LLM_Ops_Status仕様.md)
- Prompt Bundle / KV キャッシュ向け固定 prefix: [`../LLM/PromptBundle仕様.md`](../LLM/PromptBundle仕様.md)

## 運用上の要点

- 通常運用では Chat + Worker を常駐させる。
- Heavy / Wild は必要時に起動する。
- クライアントは用途ごとに base URL と `model` 名を切り替える。
- `GET /v1/models` の `id` をリクエスト `model` に使い、`backend_model` は表示・確認用に使う。
- 管理 API は `http://127.0.0.1:8079` で動作し、`Authorization: Bearer <LLM_OPS_TOKEN>` を必須とする。
- LLM role ごとの固定 prompt は Prompt Bundle として管理し、動的 context は固定 prefix の後ろに置く。

## 旧資料

古い Ollama 前提、旧 Coder3 / Worker 個別仕様、重複した role メモ、実装済みの追加依頼書は削除済みであり、実装判断では参照しない。
