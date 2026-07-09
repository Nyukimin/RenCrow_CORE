# Realtime STT Server 他PC利用仕様

## 概要

このサーバは Windows + NVIDIA GPU 上で動作するリアルタイム音声認識サーバです。

他PCからは以下の2通りで利用できます。

- HTTP API に音声ファイルをPOSTして文字起こしする
- WebSocket に 16kHz PCM 音声チャンクを送ってリアルタイム文字起こしする

ブラウザのマイクを他PCから直接使う場合、原則として HTTPS が必要です。`http://127.0.0.1` はローカルPCではマイク許可されますが、LAN上の別PCから `http://<server-ip>:8766` へアクセスする場合はブラウザ制約でマイクが使えないことがあります。

## サーバ情報

現在の標準起動:

```text
Host: 0.0.0.0
Port: 8766
Protocol: HTTP / WebSocket
Default model: small
Device: cuda
Compute type: float16
Language: ja
```

ローカル確認URL:

```text
http://127.0.0.1:8766/
```

LAN内の他PCからのURL:

```text
http://<server-ip>:8766/
```

例:

```text
http://192.168.1.33:8766/
```

## 起動と停止

サーバPCで実行します。

```powershell
cd C:\Users\nyuki\GenerativeAI\STT\realtime-stt-server
.\start_server.ps1
```

停止:

```powershell
cd C:\Users\nyuki\GenerativeAI\STT\realtime-stt-server
.\stop_server.ps1
```

モデルを変更して起動:

```powershell
cd C:\Users\nyuki\GenerativeAI\STT\realtime-stt-server
$env:STT_MODEL="turbo"
.\start_server.ps1
```

主な環境変数:

```text
STT_MODEL         small / medium / large-v3 / turbo など
STT_DEVICE        cuda
STT_COMPUTE_TYPE  float16
STT_LANGUAGE      ja
```

## ヘルスチェック

```http
GET /health
```

レスポンス例:

```json
{
  "ok": true,
  "model": "small",
  "device": "cuda",
  "compute_type": "float16",
  "language": "ja",
  "uptime_seconds": 139.359
}
```

## 音声ファイルAPI

```http
POST /v1/audio/transcriptions
Content-Type: multipart/form-data
```

フォーム:

```text
file: wav/mp3/m4a などの音声ファイル
```

curl例:

```powershell
curl.exe -X POST http://<server-ip>:8766/v1/audio/transcriptions `
  -F "file=@C:\path\to\audio.wav"
```

レスポンス例:

```json
{
  "text": "これはリアルタイム音声認識のテストです。",
  "segments": [
    {
      "start": 0.0,
      "end": 3.0,
      "text": "これはリアルタイム音声認識のテストです。"
    }
  ],
  "language": "ja",
  "language_probability": 1.0,
  "timings": {
    "total_ms": 324.87,
    "generator_wait_ms": 14.72,
    "iteration_ms": 310.15,
    "encode_ms": 210.98,
    "encode_calls": 1
  }
}
```

`timings.encode_ms` が Whisper encoder にかかった時間です。

## WebSocketリアルタイムAPI

```text
ws://<server-ip>:8766/ws/transcribe
```

クライアントから送る音声形式:

```text
sample rate: 16000 Hz
channels: 1
format: signed 16-bit little-endian PCM
chunk size: 100ms 程度を推奨
```

接続直後のサーバメッセージ:

```json
{
  "type": "ready",
  "sample_rate": 16000,
  "model": "small",
  "language": "ja"
}
```

文字起こし中:

```json
{
  "type": "transcribing"
}
```

確定結果:

```json
{
  "type": "final",
  "text": "これはリアルタイム音声認識のテストです。",
  "segments": [
    {
      "start": 0.0,
      "end": 3.0,
      "text": "これはリアルタイム音声認識のテストです。"
    }
  ],
  "language": "ja",
  "language_probability": 1.0,
  "timings": {
    "total_ms": 324.87,
    "generator_wait_ms": 14.72,
    "iteration_ms": 310.15,
    "encode_ms": 210.98,
    "encode_calls": 1
  }
}
```

無音または認識結果なし:

```json
{
  "type": "empty"
}
```

## ブラウザUI

サーバには簡易ブラウザUIが含まれています。

```text
http://<server-ip>:8766/
```

ただし、他PCのブラウザからマイクを使う場合はHTTPS化してください。

### HTTPSが必要な理由

ブラウザの `getUserMedia()` は安全なコンテキストでのみ許可されます。

安全なコンテキストとして扱われやすいもの:

- `https://...`
- `http://localhost`
- `http://127.0.0.1`

LAN内の他PCからの `http://192.168.x.x:8766` はマイク許可されない可能性があります。

## HTTPS化の推奨構成

本番または別PCブラウザ利用では、Uvicornを直接公開せず、Caddy / nginx / Cloudflare Tunnel などを前段に置いてHTTPS終端してください。

### Caddy例

`Caddyfile` 例:

```text
stt.local {
  reverse_proxy 127.0.0.1:8766
}
```

LANだけで使う場合は、自己署名証明書やローカルCAを各クライアントPCに信頼させる必要があります。

## Windowsファイアウォール

他PCから接続するには、サーバPCで TCP `8766` を許可してください。

PowerShell管理者での例:

```powershell
New-NetFirewallRule `
  -DisplayName "Realtime STT Server 8766" `
  -Direction Inbound `
  -Action Allow `
  -Protocol TCP `
  -LocalPort 8766
```

## E2E実測値

テスト環境:

```text
GPU: NVIDIA GeForce RTX 4060 Ti 16GB
Model: small
Device: cuda
Compute type: float16
Language: ja
```

HTTPファイルAPI:

```text
認識結果: これはリアルタイム音声認識のテストです。 エンド2エンドで確認しています。
クライアント往復: 2514.01 ms
サーバ推論全体: 2331.70 ms
エンコード時間: 1771.64 ms
encode calls: 1
```

WebSocketリアルタイムAPI:

```text
認識結果: これはリアルタイム音声認識のテストです。
クライアントE2E: 10544.40 ms
サーバ推論全体: 324.87 ms
エンコード時間: 210.98 ms
encode calls: 1
```

## 制限事項

- 認証は未実装です。信頼できるLAN内でのみ使ってください。
- HTTPSは未設定です。他PCブラウザのマイク利用には別途HTTPS終端が必要です。
- WebSocketの確定タイミングは簡易VADの無音検出に依存します。
- 同時接続数や長時間連続稼働の負荷試験は未実施です。
- `8765` は既存プロセスが使用中だったため、このサーバは `8766` を使います。

## 推奨クライアント実装方針

音声ファイルを処理するだけなら `/v1/audio/transcriptions` を使ってください。

リアルタイム入力では、クライアント側で以下を満たしてください。

- マイク入力を 16kHz mono PCM16 little-endian に変換する
- 100ms前後のチャンクでWebSocket送信する
- `final` メッセージを受け取ったタイミングで画面や後続処理へ反映する
- ネットワーク切断時はWebSocket再接続する

