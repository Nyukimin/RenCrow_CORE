# RenCrow_STT 接続実装のお願い（Chat本体向け）

Chat 開発チーム各位

RenCrow_STT の仕様を確定したため、Chat 側の接続実装をお願いします。  
本依頼は「音声入力 -> STT final 受信 -> 既存 LLM/TTS パイプライン接続」を対象とします。

## 実装依頼内容

1. `RENCROW_STT_URL`（例: `ws://127.0.0.1:8090/stt`）へ WS 接続
2. 音声バイナリ（WAV PCM16 16kHz mono）を STT へ送信
3. `final` イベント受信時に既存 Router へ user 発話として投入
4. `error` イベントの code に応じて再接続/通知を実施

## 契約上の注意

- STT は `final` までを責務とし、LLM 応答生成は Chat 側責務です。
- `reply_reset` / `reply_delta` は RenCrow_STT から送出されません。
- 主経路は `/stt`、既存互換で `/ws` と `/stt-ws` も受理します。

## 参照ファイル

- `02_STT_API.md`
- `04_CHAT統合契約.md`
- `05_起動手順_README.md`

以上、よろしくお願いします。
