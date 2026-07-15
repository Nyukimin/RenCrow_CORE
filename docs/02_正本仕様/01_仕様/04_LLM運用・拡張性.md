# 現行仕様: LLM運用・拡張性

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../01_仕様.md`
- source_spec: `../01_仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: Local LLM運用と拡張境界

## Local LLM 運用（2026-06-29）

* Chat endpoint: `http://127.0.0.1:8081`, model `Chat`
* Worker endpoint: `http://127.0.0.1:8082`, models `Worker` / `ChatWorker` / `Coder1` - `Coder4`
* Heavy endpoint: `http://127.0.0.1:8083`, model `Heavy`
* Wild endpoint: `http://127.0.0.1:8084`, model `Wild`
* Ollama model 物理定義や `num_ctx` は RenCrow_LLM 側で管理する。RenCrow_CORE には重複して持たせない。
* LINE指示ごとのフロー:
  1. LLM へリクエスト（先に投げる、事前ヘルスチェックなし）
  2. ヘルスチェック（成功・失敗に関わらず常時実行）
  3. Ollama が落ちていれば `ollama_restart_command` で再起動
  4. LLM 失敗時のみリトライ（成功時の重複応答を防ぐ）

## 拡張性

* ルーティングカテゴリは追加可能（例：SUMMARIZE、EXPORT、INGEST等）
* 辞書はデータ駆動で追加可能（ログから育てる前提）
* 分類器プロンプトは1枚固定し、カテゴリ追加時にのみ更新する
