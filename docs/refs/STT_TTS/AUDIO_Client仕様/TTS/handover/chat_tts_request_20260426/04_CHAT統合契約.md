# Chat 統合契約（picoclaw_multiLLM 向け）

## 前提

- Chat: [github.com/Nyukimin/picoclaw_multiLLM](https://github.com/Nyukimin/picoclaw_multiLLM)（Go 96.4%）
- 現状: RenCrow_TTS は小さな TTS gateway と provider interface を定義する。実 SBV2 推論は外部 provider の責務
- 目標: Chat の LLM 応答を RenCrow_TTS に流し、音声で返す経路を整備する
- 本書は **Chat 側に何を実装してもらうか** を規定する契約書

## 1. 接続モデル

```
[Chat (picoclaw_multiLLM)]
      |
      | HTTP POST /synthesis（単発） or WS /sessions（ストリーミング）
      v
[RenCrow_TTS :8770 (HTTPS/WSS)]
      |
      v
[TTS provider (SBV2 / VOICEVOX / cloud TTS / etc)]
      |
      v
[生成音声]
      |
      | Chat が path or url を解決してユーザに再生
      v
[Browser / Mic 反対側の Speaker]
```

本 TTS は **HTTP/WS サーバ**、Chat はクライアント。Chat は LLM 応答のテキストを受け取ったら本サービスへ送信する。

## 2. Chat 側に必要な設定

### 2.1 環境変数

| 変数 | 例 | 意味 |
|---|---|---|
| `RENCROW_TTS_URL` | `https://127.0.0.1:8770` | TTS サーバ HTTPS ベース URL |
| `RENCROW_TTS_WS_URL` | `wss://127.0.0.1:8770/sessions` | TTS WSS URL |
| `RENCROW_TTS_DEFAULT_VOICE_ID` | `female_01` | 既定ボイス |
| `RENCROW_TTS_MODE` | `oneshot` / `streaming` | HTTP 単発 or WS ストリーミング |
| `RENCROW_TTS_AUDIO_ROOT` | `./cache` | TTS と共有するキャッシュディレクトリ（同一マシン時） |
| `RENCROW_TTS_DEBUG_REQUEST_ID` | 任意 | Chat 側で request_id を指定したい場合に使う |

### 2.2 起動順序

1. RenCrow_TTS を起動
2. `curl -k https://127.0.0.1:8770/health/ready` で `ready` 確認
3. Chat 起動

## 3. Chat 側の責務

| 責務 | 対応コード位置（picoclaw_multiLLM の想定） |
|---|---|
| HTTP クライアント | 新規 `pkg/voice/rencrow_tts_client.go` |
| WS クライアント（オプション） | 新規 `pkg/voice/rencrow_tts_ws_client.go` |
| LLM 応答テキストの chunking | 既存 Router / Chat エージェント出力を分割 |
| 音声再生経路 | Telegram など各チャネルのボイス送信機能に繋ぐ |

## 4. 推奨利用パターン

### パターン A: 単発（oneshot）- 短応答向け

```
[LLM 応答確定] → [POST /synthesis] → [audio_path] → [file を送る]
```

### パターン B: ストリーミング - 長応答向け

```
[LLM delta] ----\
[LLM delta]     --> [WS text_delta] -> [audio_chunk_ready 逐次] → [逐次再生]
[LLM delta] ----/
```

## 5. エラー処理

| code | 推奨動作 |
|---|---|
| `voice_not_found` / `VOICE_NOT_FOUND` | UX 通知 + default voice で再試行 |
| `engine_unavailable` / `ENGINE_UNAVAILABLE` | 「TTS準備中」を通知しバックオフ再試行 |
| `invalid_request` | 入力分割して再送 |
| `synthesis_failed` / `SYNTHESIS_FAILED` | ログ記録して次発話へ継続 |
| `INVALID_SEQ` | 接続再確立 |

## 6. デバッグ連携（推奨）

- HTTP request に `X-RenCrow-TTS-Request-Id` を付ける
- TTS レスポンス `request_id` を Chat 側ログに残す
- 障害時に `request_id`, `voice_id`, `text length`, `provider_name`, `error.code` を記録する
