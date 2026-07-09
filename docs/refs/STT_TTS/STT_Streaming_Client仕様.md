# RenCrow STT Streaming Client 仕様

更新日: 2026-05-06

## 概要

Mac上のRenCrow STTへ、別PCブラウザからHTTPS/WSSで音声を送信する。

現在のMac側エンドポイント:

```text
HTTPS UI:  https://192.168.1.31:8443/
Health:    https://192.168.1.31:8443/health
Streaming: wss://192.168.1.31:8443/stt/stream
```

STT provider:

```text
WhisperKit + Core ML
model: large-v3-v20240930_turbo
language: ja
```

## 重要

ブラウザでマイクを使うため、ページはHTTPSで開く必要がある。

```text
https://192.168.1.31:8443/
```

初回はCA証明書を別PC側で信頼する必要がある。

Mac側CA証明書:

```text
/Users/yukimi/GenerativeAI/llm-server/run/stt_https/rencrow-stt-ca.crt
```

## WebSocket Endpoint

```text
wss://192.168.1.31:8443/stt/stream
```

## Client To Server

### 1. start

WebSocket接続後、最初に送る。

```json
{
  "type": "start",
  "language": "ja",
  "sample_rate": 48000
}
```

### 2. audio

録音中、PCM chunkをbase64で逐次送る。

```json
{
  "type": "audio",
  "format": "pcm_s16le",
  "data": "<base64 encoded PCM16LE mono>"
}
```

音声形式:

```text
format: pcm_s16le
channels: 1
sample_rate: startで指定
byte order: little endian
```

### 3. stop

録音停止・確定文字起こしを要求する。

```json
{
  "type": "stop"
}
```

## Server To Client

### ready

```json
{
  "type": "ready",
  "sample_rate": 48000
}
```

### progress

録音chunkを受信中。現時点では認識途中テキストではなく、受信音声量の進捗。

```json
{
  "type": "progress",
  "duration": 3.0,
  "bytes": 288000
}
```

### final

`stop` 後に返る確定STT結果。

```json
{
  "type": "final",
  "text": "ルミナ、今日の予定を確認して。",
  "language": "ja",
  "duration": 2.42,
  "segments": [
    {
      "start": 0.0,
      "end": 2.42,
      "text": "ルミナ、今日の予定を確認して。"
    }
  ],
  "http_status": 200
}
```

### error

```json
{
  "type": "error",
  "error_code": "STREAM_ERROR",
  "message": "..."
}
```

## 現在の制限

現在のMac側実装は「ストリーミング転送対応」。

```text
ブラウザ → WSS → PCM chunk逐次送信
```

ただし、WhisperKitの認識処理はまだ完全リアルタイムpartialではない。

```text
stop後にWAV化
  ↓
既存 /stt/file へ投入
  ↓
final textを返す
```

現時点:

- progress: あり
- partial text: なし
- final text: あり

次フェーズで追加予定:

- partial text
- VADによる自動final
- WhisperKit helper常駐化
- TTS再生中の割り込み制御

## JavaScript例

RenCrow Viewerは `/viewer/runtime-config` から `stt_stream_url` を取得し、設定されている場合は `wss://192.168.1.31:8443/stt/stream` へ直接接続する。

```js
let audioContext;
let source;
let processor;
let stream;
let socket;

function floatToBase64Pcm16(samples) {
  const buffer = new ArrayBuffer(samples.length * 2);
  const view = new DataView(buffer);

  for (let i = 0; i < samples.length; i++) {
    const sample = Math.max(-1, Math.min(1, samples[i]));
    view.setInt16(
      i * 2,
      sample < 0 ? sample * 0x8000 : sample * 0x7fff,
      true
    );
  }

  let binary = "";
  const bytes = new Uint8Array(buffer);
  for (let i = 0; i < bytes.length; i++) {
    binary += String.fromCharCode(bytes[i]);
  }
  return btoa(binary);
}

async function startSTT() {
  stream = await navigator.mediaDevices.getUserMedia({ audio: true });
  audioContext = new AudioContext();

  socket = new WebSocket("wss://192.168.1.31:8443/stt/stream");

  socket.onopen = () => {
    socket.send(JSON.stringify({
      type: "start",
      language: "ja",
      sample_rate: audioContext.sampleRate
    }));
  };

  socket.onmessage = (event) => {
    const data = JSON.parse(event.data);

    if (data.type === "progress") {
      console.log("recording seconds:", data.duration);
    }

    if (data.type === "final") {
      console.log("STT final:", data.text);
    }

    if (data.type === "error") {
      console.error(data.error_code, data.message);
    }
  };

  source = audioContext.createMediaStreamSource(stream);
  processor = audioContext.createScriptProcessor(4096, 1, 1);

  processor.onaudioprocess = (event) => {
    if (socket.readyState !== WebSocket.OPEN) return;

    const samples = new Float32Array(
      event.inputBuffer.getChannelData(0)
    );

    socket.send(JSON.stringify({
      type: "audio",
      format: "pcm_s16le",
      data: floatToBase64Pcm16(samples)
    }));
  };

  source.connect(processor);
  processor.connect(audioContext.destination);
}

async function stopSTT() {
  processor.disconnect();
  source.disconnect();
  stream.getTracks().forEach(track => track.stop());
  await audioContext.close();

  socket.send(JSON.stringify({ type: "stop" }));
}
```

## Health Check

証明書未設定のcurl確認:

```bash
curl -k https://192.168.1.31:8443/health
```

期待レスポンス:

```json
{
  "status": "ok",
  "provider": "whisperkit",
  "model": "large-v3-v20240930_turbo",
  "device": "apple_silicon",
  "ready": true
}
```

## 受け入れ条件

1. 別PCブラウザで `https://192.168.1.31:8443/` を開ける
2. ブラウザでマイク権限を取得できる
3. `wss://192.168.1.31:8443/stt/stream` に接続できる
4. `start` を送ると `ready` が返る
5. `audio` chunkを送ると `progress` が返る
6. `stop` を送ると `final.text` が返る
7. `final.text` をRenCrow Chatの通常ユーザー入力として扱える
