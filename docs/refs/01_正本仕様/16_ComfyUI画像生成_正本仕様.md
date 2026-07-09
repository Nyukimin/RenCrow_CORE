# ComfyUI 画像生成 正本仕様

作成日: 2026-06-01
ステータス: 正本仕様
参照元:
- `docs/ComfyUI/ComfyUI_RenCrow_API_Spec.md`
- `docs/10_新仕様/48_ComfyUI_RenCrow_API仕様.md`

## 1. 位置付け

RenCrow の画像生成、画像編集、画像解析、画像検索、画像プロンプト、構図・衣装・質感などの視覚系タスクは `WILD` route で扱う。

ComfyUI を利用するエージェントは `Wild` 固定とする。ComfyUI workflow 操作、ControlNet、detailer、image-to-image、画像生成、画像編集、画像解析を Worker / Coder / Research / Analyze に流してはいけない。

RenCrow は ComfyUI を HTTP API で操作する。Windows 側 filesystem path を Tailscale 越しに直接読まず、生成画像は必ず ComfyUI の `/view` 経由で取得する。

## 2. 接続先と起動

ComfyUI host:

```text
http://100.83.207.6:8188
```

ComfyUI は `0.0.0.0:8188` で待ち受ける。公開インターネットへ直接公開せず、trusted Tailscale peer 前提で扱う。

ComfyUI path:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI
```

起動 script:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\run_comfyui_tailscale.bat
```

確認済み runtime:

```text
Python: 3.10.6
PyTorch: 2.11.0+cu128
GPU: NVIDIA GeForce RTX 4060 Ti 16GB
ComfyUI: 0.22.0
```

## 3. Health

RenCrow は次を health check として使う。

```http
GET /system_stats
```

HTTP 200 を ComfyUI reachable とみなす。主に確認する field は次。

```text
system.comfyui_version
system.python_version
devices[0].name
devices[0].vram_total
devices[0].vram_free
```

## 4. API 契約

### 4.1 生成投入

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

RenCrow は `prompt_id` をジョブ状態へ保持する。polling と結果 lookup に必須である。

### 4.2 完了確認

```http
GET /history/{prompt_id}
```

実行中は `{}` の場合がある。完了時は `outputs` に画像 metadata が入る。

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

### 4.3 画像取得

```http
GET /view?filename={filename}&subfolder={subfolder}&type={type}
```

`subfolder` が空なら omit する。

例:

```text
http://100.83.207.6:8188/view?filename=zimage_verification_00001_.png&type=output
```

RenCrow は Windows path を直接読まない。ComfyUI output の local filesystem equivalent は参照情報であり、取得には使わない。

### 4.4 入力画像 upload

Image-to-image、ControlNet、detailer input が必要な場合、RenCrow は先に ComfyUI へ upload する。

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

返却された filename を `LoadImage` node に渡す。

## 5. Tool allowlist

RenCrow は user input から任意の ComfyUI workflow JSON や model filename を直接受け取ってはいけない。検証済み allowlist と workflow template だけを使う。

### 5.1 Text-to-image preset

既定 preset:

```text
zimage_turbo_portrait
workflow: rencrow_zimage_default_lora_api.json
unet: z_image_turbo_nvfp4.safetensors
text_encoder: qwen_3_4b_fp8_mixed.safetensors
vae: ae.safetensors
steps: 8
cfg: 1.0
sampler: res_multistep
scheduler: simple
shift: 3.0
expected speed: warmup 後 768x768 で約 19 秒
```

品質検証用 preset:

```text
zimage_base_quality
unet: z_image_bf16.safetensors
text_encoder: qwen_3_4b.safetensors
vae: ae.safetensors
steps: 25
cfg/guidance: 4.0
use case: slower validation or Base-compatible LoRA checks
note: RenCrow realtime path の既定ではない
```

### 5.2 Detail / Control tools

以下は候補 tool として扱う。production 使用前に workflow ごとの検証を必須とする。

```text
face_detail:
  nodes: UltralyticsDetectorProvider + FaceDetailer
  detector: bbox/face_yolov8n.pt
  denoise: 0.25-0.45

hand_or_person_detail:
  nodes: UltralyticsDetectorProvider + SEGSDetailer
  detectors: bbox/hand_yolov8n.pt, segm/person_yolov8n-seg.pt

controlnet_canny:
  nodes: CannyEdgePreprocessor + ModelPatchLoader + ZImageFunControlnet
  patch: Z-Image-Turbo-Fun-Controlnet-Union.safetensors

controlnet_depth:
  nodes: DepthAnythingV2Preprocessor + ModelPatchLoader + ZImageFunControlnet
  patch: Z-Image-Turbo-Fun-Controlnet-Union.safetensors

controlnet_pose:
  nodes: OpenposePreprocessor or DWPreprocessor + ModelPatchLoader + ZImageFunControlnet
  patch: Z-Image-Turbo-Fun-Controlnet-Union.safetensors
```

## 6. 既定 Text-to-Image workflow

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
Batch size: 1
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

`SaveImage` output から返る `filename` / `subfolder` / `type` を使って `/view` から画像を取得する。

## 7. LoRA allowlist

### 7.1 Default quality LoRA

```text
AWPortrait-Z.safetensors
  base: Z-Image Turbo
  trigger: none
  recommended strength_model/clip: 0.6-0.9
  RenCrow default: strength_model 0.8, strength_clip 0.8

REALSTAGRAM_ZIMG.safetensors
  base: Z-Image Turbo / De-Turbo
  trigger: none
  recommended strength_model/clip: 0.2-0.4
  RenCrow default: strength_model 0.3, strength_clip 0.3

ZIT_JPwoman01.safetensors
  base: Z-Image Turbo
  trigger: bundled README 確認前は一般公開しない
  recommended strength_model/clip: 0.6-0.9 initial test range

consistent_char_min_park_z_image.safetensors
  base: Z-Image
  trigger: bundled README 確認前は一般公開しない
  recommended strength_model/clip: 0.5-0.8 initial test range
```

### 7.2 RenCrow identity LoRA

Identity LoRA は Turbo workflow で利用可能。ただし Z-Image Base checkpoint で学習されたため、fast Turbo generation では低 strength を維持する。

```text
rencrow_zimage_ai_yoshikawa.safetensors
  trigger: rc_ai_yoshikawa
  fast Turbo strength_model/clip: 0.30-0.40
  verified seed: 2606015101

rencrow_zimage_eraiza_ikeda.safetensors
  trigger: rc_eraiza_ikeda
  fast Turbo strength_model/clip: 0.30-0.40
  verified seed: 2606015102

rencrow_zimage_fuka_koshiba.safetensors
  trigger: rc_fuka_koshiba
  fast Turbo strength_model/clip: 0.30-0.40
  verified seed: 2606015103

rencrow_zimage_haruka_ayase.safetensors
  trigger: rc_haruka_ayase
  fast Turbo strength_model/clip: 0.30-0.40
  verified seed: 2606015104

rencrow_zimage_haruka_hukuhara.safetensors
  trigger: rc_haruka_hukuhara
  fast Turbo strength_model/clip: 0.30-0.40
  verified seed: 2606015105

rencrow_zimage_mio_imada.safetensors
  trigger: rc_mio_imada
  fast Turbo strength_model/clip: 0.30-0.40
  verified seed: 2606015106
```

推奨 prompt shape:

```text
{trigger}, cute Japanese woman, natural smile, realistic portrait photo, soft window light, clear eyes, natural black hair, casual modern outfit, high quality
```

推奨 fast stack:

```text
LoRA 1: AWPortrait-Z.safetensors, strength_model 0.8, strength_clip 0.8
LoRA 2: REALSTAGRAM_ZIMG.safetensors, strength_model 0.3, strength_clip 0.3
LoRA 3: selected rencrow_zimage_*.safetensors, strength_model 0.35, strength_clip 0.35
```

Identity LoRA を Turbo workflow で `1.0` 付近にしない。検証済み fast path は `0.35` である。

## 8. Prompt / Negative prompt

推奨 positive prompt structure:

```text
subject, composition, lighting, camera/lens or illustration style, anatomy quality, detail level
```

例:

```text
A sharp, natural portrait photo of a Japanese woman in her late twenties, standing in soft window light, realistic skin texture, symmetrical eyes, normal hands, detailed hair, 50mm lens, high quality, coherent anatomy
```

Z-Image Turbo の negative prompt は通常の別 prompt ではなく、positive conditioning を `ConditioningZeroOut` して negative conditioning として使う。`CFG` は `1.0` を既定とし、workflow を明示的に変更して再検証するまで変えない。

## 9. ControlNet / Detailer

ControlNet と ADetailer 相当処理は、workflow ごとに別途検証してから production 使用する。

ADetailer 相当の主な node:

```text
UltralyticsDetectorProvider
FaceDetailer
BboxDetectorSEGS
SEGSDetailer
```

Detector model:

```text
bbox/face_yolov8n.pt
bbox/face_yolov8s.pt
bbox/hand_yolov8n.pt
segm/person_yolov8n-seg.pt
segm/person_yolov8s-seg.pt
```

ADetailer first pass:

```text
face detector: bbox/face_yolov8n.pt
bbox_threshold: 0.35-0.50
bbox_dilation: 10
bbox_crop_factor: 3.0
detailer steps: 8
detailer cfg: 1.0
detailer denoise: 0.25-0.45
```

ControlNet patch:

```text
Z-Image-Turbo-Fun-Controlnet-Union.safetensors
```

ControlNet flow:

```text
Load input image
Run preprocessor such as CannyEdgePreprocessor / DepthAnythingV2Preprocessor / OpenposePreprocessor
Load model patch with ModelPatchLoader
Apply patch with ZImageFunControlnet
Run KSampler
Decode and SaveImage
```

## 10. 安全制約

RenCrow は次を守る。

- 任意の user-supplied workflow JSON を ComfyUI に直接渡さない。
- 検証済み workflow template を使う。
- user input から raw model filename / node graph を直接選ばせない。
- prompt length、width、height、batch size、output count を検証する。
- batch size は既定 1。
- width / height は検証済み上限内に制限する。
- ComfyUI port を public internet に公開しない。trusted Tailscale peer 前提。
- 画像取得は `/view` 経由に限定し、Windows path を直接読まない。

## 11. エラー処理

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
  z_image_turbo_nvfp4.safetensors、qwen_3_4b_fp8_mixed.safetensors、小さい resolution、batch_size 1 で再試行する。
```

ComfyUI logs:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\user\comfyui.log
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\comfyui_0_0_0_0_stdout.log
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\comfyui_0_0_0_0_stderr.log
```

## 12. 検証済み状態

2026-06-01 時点で次を passed とする。

```text
Z-Image Turbo text-to-image
Default LoRA stack AWPortrait-Z + REALSTAGRAM_ZIMG
RenCrow identity LoRA fast Turbo stack at strength 0.35
ADetailer detector recognition
ComfyUI Tailscale access
```

Verification image:

```text
http://100.83.207.6:8188/view?filename=zimage_verification_00001_.png&type=output
```

参照用生成 script:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\scripts\generate_zimage_lora_comfy_fast.py
```
