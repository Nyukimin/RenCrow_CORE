# ComfyUI RenCrow API 仕様

作成日: 2026-06-01

## 1. 方針

RenCrow の画像生成は、ローカル ComfyUI を既定 backend とする。

ComfyUI を利用するエージェントは `Wild` 固定とする。画像生成、画像編集、ControlNet、detailer、workflow 操作は Worker / Coder / Research に流さない。

画像検索、画像生成、画像解析、画像分析、画像プロンプト、構図・衣装・質感などの視覚系タスクは `WILD` route で扱う。

## 2. ComfyUI 接続先

```text
http://100.83.207.6:8188
```

ComfyUI は `0.0.0.0:8188` で待ち受ける。RenCrow からは HTTP API で操作し、Windows 側ファイルパスを直接読まない。

## 3. Health

```http
GET /system_stats
```

RenCrow は HTTP 200 を ComfyUI reachable とみなす。

確認すべき主な field:

```text
system.comfyui_version
system.python_version
devices[0].name
devices[0].vram_total
devices[0].vram_free
```

## 4. 生成投入

```http
POST /prompt
Content-Type: application/json
```

Request:

```json
{
  "prompt": {
    "...": {
      "class_type": "...",
      "inputs": {}
    }
  },
  "client_id": "rencrow-server"
}
```

Response:

```json
{
  "prompt_id": "uuid",
  "number": 1,
  "node_errors": {}
}
```

RenCrow は `prompt_id` を永続化またはジョブ状態へ保持する。完了 polling と結果 lookup に必須。

## 5. 完了確認

```http
GET /history/{prompt_id}
```

実行中は `{}` の場合がある。

完了時は `outputs` に画像 metadata が入る。

```json
{
  "prompt_id": {
    "outputs": {
      "10": {
        "images": [
          {
            "filename": "zimage_verification_00001_.png",
            "subfolder": "",
            "type": "output"
          }
        ]
      }
    }
  }
}
```

Polling:

```text
interval: 2-5 秒
timeout: 初回ロード 10 分、warmup 後 2-5 分
```

## 6. 画像取得

```http
GET /view?filename={filename}&subfolder={subfolder}&type={type}
```

`subfolder` が空なら omit する。

RenCrow は生成画像を必ず `/view` 経由で取得する。Tailscale 越しに Windows filesystem path を直接読まない。

## 7. 既定 Text-to-Image Workflow

既定 workflow は Z-Image Turbo + portrait LoRA stack とする。

Model:

```text
UNET: z_image_turbo_nvfp4.safetensors
Text encoder: qwen_3_4b_fp8_mixed.safetensors
VAE: ae.safetensors
LoRA 1: AWPortrait-Z.safetensors, strength_model 0.8, strength_clip 0.8
LoRA 2: REALSTAGRAM_ZIMG.safetensors, strength_model 0.3, strength_clip 0.3
CLIPLoader type: lumina2
Sampling model shift: 3.0
Steps: 8
CFG: 1.0
Sampler: res_multistep
Scheduler: simple
Resolution: 768x768 or 1024x1024
```

ComfyUI 側の保存済み API workflow:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\user\default\workflows\rencrow_zimage_default_lora_api.json
```

RenCrow 側 template で置換する値:

```text
PROMPT_TEXT
SEED
WIDTH
HEIGHT
FILENAME_PREFIX
```

## 8. Prompt 方針

推奨構造:

```text
subject, composition, lighting, camera/lens or illustration style, anatomy quality, detail level
```

Z-Image Turbo の negative prompt は、通常の別 prompt ではなく positive conditioning を `ConditioningZeroOut` して使う。

`CFG` は `1.0` を既定とし、workflow を明示的に変更して再検証するまで変えない。

## 9. 入力画像

Image-to-image、ControlNet、detailer input が必要な場合、RenCrow はまず ComfyUI へ upload する。

```http
POST /upload/image
Content-Type: multipart/form-data
```

Fields:

```text
image: file
type: input
overwrite: true or false
```

返却された filename を `LoadImage` に渡す。

## 10. ControlNet / Detailer

ControlNet と ADetailer 相当処理は、workflow ごとに別途検証してから production 使用する。

利用候補:

```text
Z-Image-Turbo-Fun-Controlnet-Union.safetensors
UltralyticsDetectorProvider
FaceDetailer
BboxDetectorSEGS
SEGSDetailer
CannyEdgePreprocessor
DepthAnythingV2Preprocessor
OpenposePreprocessor
DWPreprocessor
```

## 11. 安全制約

RenCrow は次を守る。

- 任意の user-supplied workflow JSON を ComfyUI に直接渡さない。
- 検証済み workflow template を使う。
- prompt length、width、height、batch size、output count を検証する。
- batch size は既定 1。
- width / height は検証済み上限内に制限する。
- ComfyUI port を public internet に公開しない。trusted Tailscale peer 前提。

## 12. エラー扱い

```text
connection refused:
  ComfyUI が起動していない、または 8188 が遮断されている。

HTTP 400 from /prompt:
  workflow JSON、node、model、node input が不正。

empty /history:
  実行中。timeout まで polling 継続。

no image in outputs:
  SaveImage output なし、または内部失敗。history status と ComfyUI log を確認する。

CUDA out of memory:
  nvfp4 model、fp8 text encoder、小さい resolution、batch_size 1 で再試行する。
```

## 13. 参照元

詳細な workflow JSON と ComfyUI 側 runtime 情報は以下を参照する。

```text
docs/ComfyUI/ComfyUI_RenCrow_API_Spec.md
```

正本仕様は以下を参照する。

```text
docs/01_正本仕様/16_ComfyUI画像生成_正本仕様.md
```
