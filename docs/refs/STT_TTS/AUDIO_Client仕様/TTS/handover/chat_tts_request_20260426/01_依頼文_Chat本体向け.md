# RenCrow_TTS 連携実装のお願い（Chat本体）

`picoclaw_multiLLM` 側で、`RenCrow_TTS_02` を使った音声返答連携の実装をお願いしたいです。  
今回の接続先は **HTTPS / WSS 前提** です。

## 目的

- LLM応答を `RenCrow_TTS_02` に渡して音声化する
- 単発（HTTP）と将来拡張（WSストリーミング）の両方に対応可能な構成にする

## 接続先（現行）

- HTTP Base URL: `https://127.0.0.1:8770`
- WS URL: `wss://127.0.0.1:8770/sessions`
- Ready確認: `GET /health/ready`

## 参照資料（同梱）

1. `02_TTS_API.md`（API契約）
2. `03_TTS仕様.md`（運用要件）
3. `04_CHAT統合契約.md`（Chat側実装責務）
4. `05_起動手順_README.md`（起動・疎通確認）

## 実装してほしい最小範囲

1. `RENCROW_TTS_URL` / `RENCROW_TTS_WS_URL` の環境変数対応
2. 単発合成（`POST /synthesis`）クライアント実装
3. `audio_path` または `audio_url` の取得と音声送出
4. エラーコード（`voice_not_found`, `engine_unavailable`, `invalid_request`, `synthesis_failed`）のハンドリング
5. 可能であれば `X-RenCrow-TTS-Request-Id` を付与して追跡可能化

## 受け入れ基準

- `GET /health/ready` が `ready` のとき、Chatから単発TTSが成功する
- 失敗時にエラー種別ごとのログが残る
- 既定ボイス（`female_01`）で音声返信できる
- 環境変数だけで接続先を切り替えられる

## 補足

- サーバ側は TLS 対応済み（証明書配置済み環境）
- 当面は単発（oneshot）優先で問題ありません
