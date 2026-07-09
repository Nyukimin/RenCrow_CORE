# Irodori-TTS HTTP API Specification

更新日: 2026-05-06

この仕様書は、Irodori-TTS の `gradio_app.py` を他PCからHTTP経由で利用するためのものです。現行実装は独自REST APIではなく、Gradioが公開するHTTP APIを利用します。

## 1. サービス概要

| 項目 | 内容 |
|---|---|
| サービス名 | Irodori-TTS HTTP Service |
| 実体 | `gradio_app.py` |
| 用途 | 日本語テキストからWAV音声を生成する |
| プロトコル | HTTP |
| 既定ポート | 7870 |
| API Prefix | `/gradio_api` |
| 同時実行 | `default_concurrency_limit=1` のキュー実行 |
| 出力形式 | WAVファイル |
| 生成音声サンプルレート | 48 kHz |
| 最大候補数 | 32 |
| 固定生成長 | 30秒相当を生成し、末尾無音をトリム |
| Viewer標準ステップ数 | `num_steps=16` |

---

## 2. 起動仕様

他PCから利用する場合は、必ず `--server-name 0.0.0.0` で起動します。`127.0.0.1` で起動すると同一PCからしか接続できません。

```bash
cd /Users/yukimi/Documents/Codex/2026-05-06/irodoritts-mac/Irodori-TTS
.venv/bin/python -u gradio_app.py --server-name 0.0.0.0 --server-port 7870 --debug
```

現在のPCのLAN IP例:

```text
192.168.1.31
```

他PCからのベースURL例:

```text
http://192.168.1.31:7870
```

ヘルスチェック:

```bash
curl -i http://192.168.1.31:7870/
curl -s http://192.168.1.31:7870/gradio_api/info
```

---

## 3. 主要エンドポイント

| Method | Path | 用途 |
|---|---|---|
| GET | `/` | Gradio UI |
| GET | `/gradio_api/info` | API定義の取得 |
| GET | `/config` | Gradioアプリ設定の取得 |
| POST | `/gradio_api/run/_load_model` | モデルを事前ロード |
| POST | `/gradio_api/run/_run_generation` | 音声生成 |
| POST | `/gradio_api/run/_clear_runtime_cache` | モデルキャッシュ解放 |

---

## 3.1 現行Gradio入力数の注意

2026-05-06時点のMac側 `gradio_app.py` では、`_load_model` と `_run_generation` の両方に非表示stateの `enable_watermark` が含まれます。

これを省略すると、Gradio側で入力不足になりHTTP 500になります。

```text
_load_model didn't receive enough input values: needed 6, got 5
_run_generation didn't receive enough input values: needed 24, got 23
```

RenCrow Viewer側は `codec_precision` の直後に `enable_watermark=false` を送ります。

---

## 3.2 低遅延TTS方針

Viewer標準設定では生成速度を優先し、`num_steps` は16に固定します。

```text
data[8] = 16
```

Mac側実測では、モデルロード済み状態の `num_steps=16` で約3秒前後です。

---

## 4. 音声生成 API

### 4.1 Endpoint

```http
POST /gradio_api/run/_run_generation
Content-Type: application/json
```

### 4.2 Request Body

GradioのHTTP APIは、入力値を `data` 配列で渡します。順序は固定です。
現行のMac側実装では、5番目の `codec_precision` の直後に非表示stateの `enable_watermark` が必要です。通常は `false` を送ります。

```json
{
  "data": [
    "Aratako/Irodori-TTS-500M-v2",
    "mps",
    "fp32",
    "mps",
    "fp32",
    false,
    "今日はいい天気ですね。",
    null,
    16,
    1,
    "",
    "independent",
    3.0,
    5.0,
    "",
    0.5,
    1.0,
    true,
    "",
    "",
    "",
    "",
    "0.9",
    ""
  ]
}
```

### 4.3 Request Parameters

| Index | Name | Type | Required | Default | Description |
|---:|---|---|---|---|---|
| 0 | checkpoint | string | yes | `Aratako/Irodori-TTS-500M-v2` | Hugging Face repo id、またはサーバ上の `.pt` / `.safetensors` パス |
| 1 | model_device | string | yes | `auto` | `mps`, `cpu`, CUDA環境では `cuda` |
| 2 | model_precision | string | yes | `fp32` | 現在のMac環境では `fp32` |
| 3 | codec_device | string | yes | `auto` | `mps`, `cpu`, CUDA環境では `cuda` |
| 4 | codec_precision | string | yes | `fp32` | 現在のMac環境では `fp32` |
| 5 | enable_watermark | boolean | yes | false | 非表示state。通常は `false` |
| 6 | text | string | yes | none | 読み上げる本文。空文字はエラー |
| 7 | uploaded_audio | object or null | no | null | 参照音声。null の場合はno-reference mode |
| 8 | num_steps | number | yes | 16 | Viewer標準は低遅延優先で16に固定 |
| 9 | num_candidates | number | no | 1 | 候補数。範囲 1..32 |
| 10 | seed_raw | string | no | `""` | 空文字でランダム。固定する場合は整数文字列 |
| 11 | cfg_guidance_mode | string | no | `independent` | `independent`, `joint`, `alternating` |
| 12 | cfg_scale_text | number | no | 3.0 | テキスト条件のCFG scale |
| 13 | cfg_scale_speaker | number | no | 5.0 | 話者条件のCFG scale |
| 14 | cfg_scale_raw | string | no | `""` | 全条件のCFG override。通常は空 |
| 15 | cfg_min_t | number | no | 0.5 | CFGを適用する最小t |
| 16 | cfg_max_t | number | no | 1.0 | CFGを適用する最大t |
| 17 | context_kv_cache | boolean | no | true | コンテキストK/Vキャッシュ |
| 18 | truncation_factor_raw | string | no | `""` | 初期ノイズのスケール。通常は空 |
| 19 | rescale_k_raw | string | no | `""` | temporal score rescale k。通常は空 |
| 20 | rescale_sigma_raw | string | no | `""` | temporal score rescale sigma。通常は空 |
| 21 | speaker_kv_scale_raw | string | no | `""` | 話者K/V強調。通常は空 |
| 22 | speaker_kv_min_t_raw | string | no | `"0.9"` | 話者K/V強調を適用するtしきい値 |
| 23 | speaker_kv_max_layers_raw | string | no | `""` | 話者K/V強調を適用する最大層数 |

### 4.4 Response

正常時は以下の形で返ります。

```json
{
  "data": [
    {
      "visible": true,
      "value": {
        "path": "/absolute/path/to/gradio_outputs/sample_..._001.wav",
        "url": "http://192.168.1.31:7870/gradio_api/file=...",
        "orig_name": "sample_..._001.wav",
        "mime_type": null
      },
      "__type__": "update"
    },
    null,
    "... Generated Audio 2-32 ...",
    "runtime: reused\nseed_used: ...\ncandidates: 1\nsaved[1]: ...",
    "[timing] ---- request ----\n..."
  ],
  "is_generating": false,
  "duration": 12.345,
  "average_duration": 12.345
}
```

`data[0]` から `data[31]` が生成音声候補です。`num_candidates=1` の場合、`data[0]` のみが有効で、残りは `null` または非表示更新値になります。

`data[32]` は実行ログ、`data[33]` はタイミング情報です。

音声ファイルは、返却された `data[0].value.url` をHTTP GETするか、サーバ上の `data[0].value.path` から取得します。他PCから取得する場合は `url` を使用してください。旧形式で `data[0].url` が直接返る場合もあります。

Gradioが `http://127.0.0.1:7870/gradio_api/file=...` を返す場合があります。別PCまたはRenCrowサーバから取得する場合は、ホストを `192.168.1.31` に置換します。

---

## 5. モデル事前ロード API

初回生成はモデルロードで時間がかかるため、サービス起動後に事前ロードを推奨します。

```http
POST /gradio_api/run/_load_model
Content-Type: application/json
```

Request:

```json
{
  "data": [
    "Aratako/Irodori-TTS-500M-v2",
    "mps",
    "fp32",
    "mps",
    "fp32",
    false
  ]
}
```

Response:

```json
{
  "data": [
    "loaded model into memory\ncheckpoint: ...\nmodel_device: mps\n..."
  ],
  "is_generating": false
}
```

---

## 6. モデル解放 API

```http
POST /gradio_api/run/_clear_runtime_cache
Content-Type: application/json
```

Request:

```json
{
  "data": []
}
```

---

## 7. curl Examples

### 7.1 no-reference mode

```bash
BASE_URL="http://192.168.1.31:7870"

curl -s -X POST "$BASE_URL/gradio_api/run/_run_generation" \
  -H "Content-Type: application/json" \
  -d '{
    "data": [
      "Aratako/Irodori-TTS-500M-v2",
      "mps",
      "fp32",
      "mps",
      "fp32",
      false,
      "今日はいい天気ですね。",
      null,
      16,
      1,
      "",
      "independent",
      3.0,
      5.0,
      "",
      0.5,
      1.0,
      true,
      "",
      "",
      "",
      "",
      "0.9",
      ""
    ]
  }'
```

返却JSONの `data[0].value.url` を取得してダウンロードします。旧形式では `data[0].url` の場合があります。

```bash
curl -L "http://192.168.1.31:7870/gradio_api/file=..." -o output.wav
```

---

## 8. Python Client Example

他PCからは `gradio_client` の利用を推奨します。ファイルアップロードや返却ファイルの扱いが簡単になります。

```bash
pip install gradio_client
```

```python
from gradio_client import Client

client = Client("http://192.168.1.31:7870")

result = client.predict(
    checkpoint="Aratako/Irodori-TTS-500M-v2",
    model_device="mps",
    model_precision="fp32",
    codec_device="mps",
    codec_precision="fp32",
    enable_watermark=False,
    text="今日はいい天気ですね。",
    uploaded_audio=None,
    num_steps=16,
    num_candidates=1,
    seed_raw="",
    cfg_guidance_mode="independent",
    cfg_scale_text=3.0,
    cfg_scale_speaker=5.0,
    cfg_scale_raw="",
    cfg_min_t=0.5,
    cfg_max_t=1.0,
    context_kv_cache=True,
    truncation_factor_raw="",
    rescale_k_raw="",
    rescale_sigma_raw="",
    speaker_kv_scale_raw="",
    speaker_kv_min_t_raw="0.9",
    speaker_kv_max_layers_raw="",
    api_name="/_run_generation",
)

audio_1 = result[0]
# 現行Gradio HTTP APIでは audio_1["value"]["url"] にWAV URLが入る。
# 旧形式では audio_1["url"] の場合がある。
run_log = result[32]
timing = result[33]

print(audio_1)
print(run_log)
print(timing)
```

参照音声を使う場合:

```python
from gradio_client import Client, handle_file

client = Client("http://192.168.1.31:7870")

result = client.predict(
    checkpoint="Aratako/Irodori-TTS-500M-v2",
    model_device="mps",
    model_precision="fp32",
    codec_device="mps",
    codec_precision="fp32",
    enable_watermark=False,
    text="こんにちは。これは参照音声を使ったテストです。",
    uploaded_audio=handle_file("reference.wav"),
    num_steps=16,
    num_candidates=1,
    seed_raw="",
    cfg_guidance_mode="independent",
    cfg_scale_text=3.0,
    cfg_scale_speaker=5.0,
    cfg_scale_raw="",
    cfg_min_t=0.5,
    cfg_max_t=1.0,
    context_kv_cache=True,
    truncation_factor_raw="",
    rescale_k_raw="",
    rescale_sigma_raw="",
    speaker_kv_scale_raw="",
    speaker_kv_min_t_raw="0.9",
    speaker_kv_max_layers_raw="",
    api_name="/_run_generation",
)
```

---

## 9. 運用上の注意

- `127.0.0.1` 起動では他PCから接続できません。LAN公開時は `0.0.0.0` で起動してください。
- macOSのファイアウォールでPythonまたはポート7870がブロックされている場合、他PCから接続できません。
- LAN内公開のため認証はありません。信頼できるネットワーク内だけで利用してください。
- キュー同時実行は1です。複数PCから同時に呼ぶと順番待ちになります。
- 初回呼び出しはモデルロードとHugging Faceからの取得により時間がかかります。運用開始時に `_load_model` を呼ぶことを推奨します。
- `checkpoint` にローカルファイルパスを指定する場合、そのパスはTTSサーバ側のファイルシステム上のパスです。クライアントPC上のパスではありません。
- Gradioの返却 `path` はサーバ内パスです。他PCから取得する場合は `url` を使ってください。

---

## 10. 現在確認した稼働状態

2026-05-06時点で、以下のプロセスが起動していることを確認済みです。

```bash
gradio_app.py --server-name 127.0.0.1 --server-port 7870 --debug
```

この状態ではローカルPCから `http://127.0.0.1:7870` にアクセスできます。他PCから使う場合は、サービスを停止して `--server-name 0.0.0.0` で再起動してください。
