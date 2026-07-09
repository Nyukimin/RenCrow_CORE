# RenCrow STT Remote HTTPS 利用仕様

更新日: 2026-05-06

原本: `llm-server/docs/stt_remote_https.md`

この仕様書は、別PCからRenCrow STTを利用するためのHTTP/HTTPS構成を定義する。特に、別PCのブラウザでマイクを使って録音し、WAVをSTTサーバへ送る運用を対象にする。

> RenCrow Chat側メモ: このリポジトリではWhisperKit/Core MLを直接実装しない。Mac上の `rencrow-stt` を外部STT Providerとして扱い、Go側は `external_http` で `/stt/file` / `/stt/chat-input` へ接続する。
> Streamingクライアント仕様は [STT_Streaming_Client仕様.md](./STT_Streaming_Client仕様.md) を正とする。

## 1. 結論

別PCのブラウザでマイクを使う場合、WebページとSTT APIはHTTPSで提供する。

STTサーバ本体は既存実装どおりHTTPでよい。ただし、外部公開面にはHTTPSリバースプロキシを置く。

```text
Other PC Browser
  ↓ HTTPS
https://stt.example.local/stt/file
  ↓ reverse proxy
http://127.0.0.1:8765/stt/file
  ↓
RenCrow STT Server
  ↓
WhisperKit / Core ML
```

理由:

- ブラウザの `getUserMedia()` は原則HTTPSのsecure contextでのみ使用できる
- `localhost` / `127.0.0.1` は例外だが、別PCからMacへアクセスする場合は例外にならない
- HTTPSページからHTTP APIへ送るとmixed contentやCORSで失敗する可能性が高い

## 2. 対象

対象:

- 別PCのブラウザでマイク録音する
- 録音した音声をWAVとしてRenCrow STTへ送る
- RenCrow STTはMac上で動かす
- STT providerは `whisperkit`
- APIは `/health`, `/stt/file`, `/stt/chat-input`

対象外:

- 完全リアルタイムSTT
- WebSocket streaming
- 話者分離
- 複数マイク管理
- 公開インターネット向けの本格認証基盤

## 3. 推奨構成

### 3.1 LAN内で使う場合

```text
Other PC
  Browser / Web UI
    ↓ HTTPS
Mac
  Caddy or nginx
    ↓ HTTP localhost
  RenCrow STT Server :8765
    ↓
  WhisperKit Helper
```

STTサーバはlocalhostに閉じる。

```bash
uv run rencrow-stt --config configs/stt.yaml --host 127.0.0.1
```

HTTPSリバースプロキシだけをLANへ公開する。現行の公開面は `https://192.168.1.31:8443/`。

```text
https://<Macのホスト名>/stt/file
https://<Macのホスト名>/stt/chat-input
https://<Macのホスト名>/health
```

現行確認済み:

```text
STT本体:  http://127.0.0.1:8765
HTTPS画面: https://192.168.1.31:8443
CA証明書: /Users/yukimi/GenerativeAI/llm-server/run/stt_https/rencrow-stt-ca.crt
```

### 3.2 開発時の簡易構成

curlやネイティブアプリから使うだけならHTTPでもよい。

```bash
uv run rencrow-stt --config configs/stt.yaml --host 0.0.0.0
curl -s -X POST http://<MacのIP>:8765/stt/file -F file=@audio.wav
```

ただし、別PCのブラウザでマイクを使うWeb UIには推奨しない。

## 4. 起動仕様

### 4.1 WhisperKit helper build

```bash
cd /Users/yukimi/GenerativeAI/llm-server
cd src/rencrow/stt/providers/whisperkit_provider/whisperkit_helper
swift build -c release
```

### 4.2 STT server

```bash
cd /Users/yukimi/GenerativeAI/llm-server
uv run rencrow-stt --config configs/stt.yaml --host 127.0.0.1
```

内部URL:

```text
http://127.0.0.1:8765
```

設定ファイル:

```text
configs/stt.yaml
```

現在の主要設定:

```yaml
stt:
  provider: whisperkit
  language: ja
  model: large-v3-v20240930_turbo
  timeout_sec: 600

  endpoint:
    host: 127.0.0.1
    port: 8765
```

## 5. HTTPS終端

### 5.1 Caddy例

IPで利用する場合は `192.168.1.31` をMacのHTTPS公開面とする。

```text
192.168.1.31 {
  reverse_proxy 127.0.0.1:8765
}
```

LAN内だけで自己署名またはinternal CAを使う場合:

```text
192.168.1.31 {
  tls internal
  reverse_proxy 127.0.0.1:8765
}
```

注意:

- `tls internal` の証明書は、利用する別PC側で信頼させる必要がある
- 証明書を信頼していないHTTPSページでは、ブラウザのマイク権限やAPI呼び出しが安定しない

### 5.2 nginx例

```nginx
server {
    listen 443 ssl;
    server_name 192.168.1.31;

    ssl_certificate     /path/to/fullchain.pem;
    ssl_certificate_key /path/to/privkey.pem;

    client_max_body_size 50m;

    location / {
        proxy_pass http://127.0.0.1:8765;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

## 6. API仕様

外部公開URLはHTTPSを使う。

```text
BASE_URL=https://192.168.1.31:8443
```

### 6.1 Health

```text
GET /health
```

Response:

```json
{
  "status": "ok",
  "provider": "whisperkit",
  "model": "large-v3-v20240930_turbo",
  "device": "apple_silicon",
  "ready": true
}
```

### 6.2 WAV文字起こし

```text
POST /stt/file
Content-Type: multipart/form-data
```

Form:

```text
file: audio.wav
```

Response:

```json
{
  "text": "ルミナ、今日の予定を確認して。",
  "language": "ja",
  "duration": 2.42,
  "segments": [
    {
      "start": 0.0,
      "end": 2.42,
      "text": "ルミナ、今日の予定を確認して。"
    }
  ]
}
```

### 6.3 Chat投入用JSON

```text
POST /stt/chat-input
Content-Type: multipart/form-data
```

Form:

```text
file: audio.wav
```

Response:

```json
{
  "type": "user_input",
  "source": "local_stt",
  "provider": "whisperkit",
  "text": "ルミナ、今日の予定を確認して。",
  "confidence_note": null,
  "event_id": "evt_stt_20260506_153739_3747833e"
}
```

## 7. curl例

```bash
BASE_URL="https://192.168.1.31:8443"

curl -s "$BASE_URL/health"
```

```bash
curl -s -X POST "$BASE_URL/stt/file" \
  -F file=@audio.wav
```

```bash
curl -s -X POST "$BASE_URL/stt/chat-input" \
  -F file=@audio.wav
```

証明書検証を一時的に無視する開発用確認:

```bash
curl -k -s -X POST "https://192.168.1.31:8443/stt/file" \
  -F file=@audio.wav
```

ブラウザ運用では `-k` 相当は使えないため、証明書を正しく信頼させる。

## 8. ブラウザ録音クライアント仕様

### 8.1 Secure Context

Web UIはHTTPSで配信する。

```text
https://192.168.1.31:8443/
```

HTTPページでは、別PCのブラウザでマイクが使えない可能性が高い。

### 8.2 録音からSTT送信まで

```text
navigator.mediaDevices.getUserMedia({ audio: true })
  ↓
MediaRecorder
  ↓
Blob
  ↓
WAVまたはブラウザ生成可能な音声形式
  ↓
multipart/form-data
  ↓
POST https://192.168.1.31:8443/stt/file
```

初期STTサーバはファイル入力を受ける。ブラウザで `webm` が生成される場合は、クライアント側またはサーバ手前でWAVへ変換する。確実に運用するなら、Web UI側でWAVエンコードして送る。

### 8.3 JavaScript例

```js
async function sendWavToSTT(wavBlob) {
  const form = new FormData();
  form.append("file", wavBlob, "recording.wav");

  const res = await fetch("https://192.168.1.31:8443/stt/file", {
    method: "POST",
    body: form
  });

  if (!res.ok) {
    throw new Error(`STT request failed: ${res.status}`);
  }

  return await res.json();
}
```

Chat投入用JSONを直接使う場合:

```js
async function sendWavToChatInput(wavBlob) {
  const form = new FormData();
  form.append("file", wavBlob, "recording.wav");

  const res = await fetch("https://192.168.1.31:8443/stt/chat-input", {
    method: "POST",
    body: form
  });

  return await res.json();
}
```

## 9. CORS

現在のSTTサーバは以下を返す。

```text
Access-Control-Allow-Origin: *
Access-Control-Allow-Methods: *
Access-Control-Allow-Headers: *
```

Web UIとAPIを同じoriginで配信する場合、CORS問題は起きにくい。

```text
https://192.168.1.31:8443/index.html
https://192.168.1.31:8443/stt/file
```

Web UIとAPIを別originにする場合:

```text
https://app.local
https://192.168.1.31:8443
```

この場合も現状は許可されるが、運用時は許可originを限定することを推奨する。

## 10. セキュリティ方針

LAN内だけで利用する場合でも、HTTPS公開面には最低限の保護を置く。

推奨:

- STTサーバ本体は `127.0.0.1:8765` に閉じる
- HTTPSリバースプロキシだけをLANへ公開する
- ファイアウォールで公開範囲をLANに限定する
- 必要に応じてBasic認証、mTLS、VPN、Tailscale等を使う

避ける:

- 認証なしでインターネット公開
- HTTPSページからHTTP APIを直接呼ぶ
- `--host 0.0.0.0` のSTTサーバをWANへ直接公開

## 11. エラー仕様

音声が検出されない場合:

```json
{
  "status": "error",
  "provider": "whisperkit",
  "error_code": "NO_SPEECH_DETECTED",
  "message": "音声が検出されませんでした。",
  "text": ""
}
```

helper未ビルド:

```json
{
  "status": "error",
  "provider": "whisperkit",
  "error_code": "WHISPERKIT_HELPER_NOT_BUILT",
  "message": "WhisperKit helper is not built: ...",
  "text": ""
}
```

タイムアウト:

```json
{
  "status": "error",
  "provider": "whisperkit",
  "error_code": "STT_TIMEOUT",
  "message": "STT processing timed out.",
  "text": ""
}
```

Chat側は `text` が空の場合、LLMへ送らない。

## 12. 動作確認手順

### 12.1 STT server確認

```bash
cd /Users/yukimi/GenerativeAI/llm-server
uv run rencrow-stt --config configs/stt.yaml --host 127.0.0.1
```

別ターミナル:

```bash
curl -s http://127.0.0.1:8765/health
```

### 12.2 HTTPS proxy確認

```bash
curl -s https://192.168.1.31:8443/health
```

### 12.3 WAV送信確認

```bash
curl -s -X POST https://192.168.1.31:8443/stt/file \
  -F file=@audio.wav
```

期待:

- HTTP status `200`
- `text` に日本語文字列が入る
- `segments` が返る

### 12.4 ブラウザマイク確認

別PCのブラウザで以下を確認する。

1. `https://192.168.1.31:8443` を開ける
2. 証明書警告が出ない
3. マイク権限ダイアログが出る
4. 録音できる
5. `POST /stt/file` が成功する
6. 日本語テキストが返る

## 13. 受け入れ条件

1. Mac上で `rencrow-stt` が `127.0.0.1:8765` で起動する
2. HTTPSリバースプロキシ経由で `GET /health` が成功する
3. 別PCから `https://.../stt/file` にWAVを送れる
4. 別PCから `https://.../stt/chat-input` にWAVを送れる
5. ブラウザ上のWeb UIでマイク権限が取得できる
6. HTTPSページからHTTPS APIへ送信できる
7. STT結果に `text`, `language`, `segments` が含まれる
8. Chat投入用JSONに `event_id`, `source=local_stt`, `provider=whisperkit` が含まれる
9. STTエラー時もJSONで返り、Chat本体を落とさない

## 14. 運用メモ

WhisperKit初回実行はモデル取得とCore ML準備で長くなる。現在の `timeout_sec` は600秒にしている。

一度モデル準備が終わった後の短い日本語wavでは、ローカル確認で数秒程度の処理時間だった。

低遅延化をさらに進める場合は、次フェーズでSwift helperを毎回起動する方式から常駐helper方式へ変更する。

## 15. RenCrow Chat側設定

RenCrow Chat側では、Mac上のSTTサーバを外部HTTP Providerとして参照する。

```yaml
stt:
  enabled: true
  provider: external_http
  language: ja
  model: remote-stt
  timeout_ms: 600000
  endpoint_path: /stt
  stream_url: "wss://192.168.1.31:8443/stt/stream"
  external_http:
    url: "https://192.168.1.31:8443/stt/file"
    stream_url: "wss://192.168.1.31:8443/stt/stream"
```

ViewerをRenCrow Chat側から配信する場合は、Chatサーバ自身もHTTPSで配信し、ViewerのWebSocketは同一Originの `wss://<chat-host>/stt` を使う。Chat側 `/stt` は必要に応じて外部STT ProviderへWAVを転送する。
