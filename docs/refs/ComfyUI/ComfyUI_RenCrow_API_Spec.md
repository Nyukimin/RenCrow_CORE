# ComfyUI RenCrow API Specification

Last updated: 2026-06-01 JST

## 1. Overview

RenCrow server operates the local ComfyUI instance over HTTP.

Routing invariant:

```text
Any agent or tool flow that uses ComfyUI must be routed to Wild.
Image analysis, image understanding, image search, image generation, image editing,
ControlNet, detailer, and workflow operations are Wild-owned tasks.
Do not route these tasks to Worker, Coder, or Research.
```

ComfyUI host:

```text
http://100.83.207.6:8188
```

The ComfyUI process is bound to `0.0.0.0:8188`, so it is reachable from the local machine, LAN, and Tailscale network if firewall rules allow the connection.

Startup script:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\run_comfyui_tailscale.bat
```

## 2. Installed Runtime

ComfyUI path:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI
```

Runtime:

```text
Python: 3.10.6
PyTorch: 2.11.0+cu128
GPU: NVIDIA GeForce RTX 4060 Ti 16GB
ComfyUI: 0.22.0
```

Installed custom nodes:

```text
ComfyUI-Manager
comfyui_controlnet_aux
ComfyUI-Impact-Pack
ComfyUI-Impact-Subpack
```

Available core functions:

```text
Z-Image Turbo text-to-image
Z-Image Base text-to-image
Z-Image Turbo ControlNet patch
ADetailer-equivalent face/hand/person detailing via Impact Pack + Ultralytics
ControlNet preprocessors: Canny, Depth, OpenPose/DWPose, etc.
```

## 3. Models

ComfyUI model names to use in workflow JSON:

```text
diffusion_models:
  z_image_turbo_nvfp4.safetensors
  z_image_turbo_bf16.safetensors
  z_image_bf16.safetensors

text_encoders:
  qwen_3_4b_fp8_mixed.safetensors
  qwen_3_4b_fp4_mixed.safetensors
  qwen_3_4b.safetensors

vae:
  ae.safetensors

loras:
  z_image_turbo_distill_patch_lora_bf16.safetensors
  AWPortrait-Z.safetensors
  REALSTAGRAM_ZIMG.safetensors
  ZIT_JPwoman01.safetensors
  consistent_char_min_park_z_image.safetensors
  rencrow_zimage_ai_yoshikawa.safetensors
  rencrow_zimage_eraiza_ikeda.safetensors
  rencrow_zimage_fuka_koshiba.safetensors
  rencrow_zimage_haruka_ayase.safetensors
  rencrow_zimage_haruka_hukuhara.safetensors
  rencrow_zimage_mio_imada.safetensors

model_patches:
  Z-Image-Turbo-Fun-Controlnet-Union.safetensors

ultralytics/bbox:
  face_yolov8n.pt
  face_yolov8s.pt
  hand_yolov8n.pt

ultralytics/segm:
  person_yolov8n-seg.pt
  person_yolov8s-seg.pt
```

Recommended default for RenCrow:

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

## 4. RenCrow Tool Allowlist

RenCrow should expose a small allowlist, not raw ComfyUI model filenames from user input.

### Text-to-image presets

```text
zimage_turbo_portrait:
  workflow: rencrow_zimage_default_lora_api.json
  unet: z_image_turbo_nvfp4.safetensors
  text_encoder: qwen_3_4b_fp8_mixed.safetensors
  vae: ae.safetensors
  steps: 8
  cfg: 1.0
  sampler: res_multistep
  scheduler: simple
  shift: 3.0
  expected speed: about 19 seconds per 768x768 image after warmup on RTX 4060 Ti 16GB

zimage_base_quality:
  unet: z_image_bf16.safetensors
  text_encoder: qwen_3_4b.safetensors
  vae: ae.safetensors
  steps: 25
  cfg/guidance: 4.0
  use case: slower validation or higher quality Base-compatible LoRA checks
  note: not the default RenCrow realtime path
```

### Detail and control tools

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
  status: available, verify per workflow before production

controlnet_depth:
  nodes: DepthAnythingV2Preprocessor + ModelPatchLoader + ZImageFunControlnet
  patch: Z-Image-Turbo-Fun-Controlnet-Union.safetensors
  status: available, verify per workflow before production

controlnet_pose:
  nodes: OpenposePreprocessor or DWPreprocessor + ModelPatchLoader + ZImageFunControlnet
  patch: Z-Image-Turbo-Fun-Controlnet-Union.safetensors
  status: available, verify per workflow before production
```

## 5. LoRA Allowlist

All LoRA files below are installed in:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\models\loras
```

### Default quality LoRAs

```text
AWPortrait-Z.safetensors
  base: Z-Image Turbo
  trigger: none
  recommended strength_model/clip: 0.6-0.9
  RenCrow use: default portrait cleanup and softer skin/light

REALSTAGRAM_ZIMG.safetensors
  base: Z-Image Turbo / De-Turbo
  trigger: none
  recommended strength_model/clip: 0.2-0.4
  RenCrow use: secondary realism; stack after portrait LoRA

ZIT_JPwoman01.safetensors
  base: Z-Image Turbo
  trigger: check bundled README before exposing
  recommended strength_model/clip: 0.6-0.9 initial test range
  RenCrow use: Japanese woman style candidate, not default until separately verified

consistent_char_min_park_z_image.safetensors
  base: Z-Image
  trigger: check bundled README before exposing
  recommended strength_model/clip: 0.5-0.8 initial test range
  RenCrow use: character consistency candidate, not default until separately verified
```

### RenCrow identity LoRAs

These six LoRAs were trained locally from `data_lora_normalized` and converted to ComfyUI-compatible Diffusers format. They are usable in the Turbo workflow, but because they were trained against `z_image_bf16.safetensors`, keep strength low for fast Turbo generation.

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

Recommended prompt shape for identity LoRAs:

```text
{trigger}, cute Japanese woman, natural smile, realistic portrait photo, soft window light, clear eyes, natural black hair, casual modern outfit, high quality
```

Recommended LoRA stack for fast RenCrow identity generation:

```text
LoRA 1: AWPortrait-Z.safetensors, strength_model 0.8, strength_clip 0.8
LoRA 2: REALSTAGRAM_ZIMG.safetensors, strength_model 0.3, strength_clip 0.3
LoRA 3: selected rencrow_zimage_*.safetensors, strength_model 0.35, strength_clip 0.35
```

## 6. Health Check

Request:

```http
GET /system_stats
```

Example:

```text
http://100.83.207.6:8188/system_stats
```

RenCrow should treat HTTP 200 as ComfyUI reachable.

Useful fields:

```text
system.comfyui_version
system.python_version
devices[0].name
devices[0].vram_total
devices[0].vram_free
```

## 7. Queue Generation

Endpoint:

```http
POST /prompt
Content-Type: application/json
```

Request body:

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

RenCrow must persist `prompt_id`. It is required for polling and result lookup.

## 8. Poll Result

Endpoint:

```http
GET /history/{prompt_id}
```

Example:

```text
http://100.83.207.6:8188/history/f257710a-ffc5-49c6-862c-2b7fcea7db5d
```

While the job is running, the response may be `{}`.

When complete, output image metadata appears under `outputs`:

```json
{
  "f257710a-ffc5-49c6-862c-2b7fcea7db5d": {
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

Polling recommendation:

```text
Interval: 2-5 seconds
Timeout: 10 minutes for first Z-Image load, 2-5 minutes after warmup
```

## 9. Fetch Image

Endpoint:

```http
GET /view?filename={filename}&subfolder={subfolder}&type={type}
```

Example:

```text
http://100.83.207.6:8188/view?filename=zimage_verification_00001_.png&type=output
```

If `subfolder` is empty, omit it.

RenCrow should not directly read Windows paths over Tailscale. Always fetch generated images through `/view`.

Local filesystem equivalent:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\output\zimage_verification_00001_.png
```

## 10. Text-to-Image Workflow

This is the default Z-Image Turbo workflow verified on 2026-06-01. It includes the default portrait LoRA stack:

```text
AWPortrait-Z.safetensors
REALSTAGRAM_ZIMG.safetensors
```

Saved API workflow:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\user\default\workflows\rencrow_zimage_default_lora_api.json
```

Replace:

```text
PROMPT_TEXT
SEED
WIDTH
HEIGHT
FILENAME_PREFIX
```

Request body for `POST /prompt`:

```json
{
  "client_id": "rencrow-server",
  "prompt": {
    "1": {
      "class_type": "UNETLoader",
      "inputs": {
        "unet_name": "z_image_turbo_nvfp4.safetensors",
        "weight_dtype": "default"
      }
    },
    "2": {
      "class_type": "CLIPLoader",
      "inputs": {
        "clip_name": "qwen_3_4b_fp8_mixed.safetensors",
        "type": "lumina2",
        "device": "default"
      }
    },
    "3": {
      "class_type": "VAELoader",
      "inputs": {
        "vae_name": "ae.safetensors"
      }
    },
    "11": {
      "class_type": "LoraLoader",
      "inputs": {
        "model": ["1", 0],
        "clip": ["2", 0],
        "lora_name": "AWPortrait-Z.safetensors",
        "strength_model": 0.8,
        "strength_clip": 0.8
      }
    },
    "12": {
      "class_type": "LoraLoader",
      "inputs": {
        "model": ["11", 0],
        "clip": ["11", 1],
        "lora_name": "REALSTAGRAM_ZIMG.safetensors",
        "strength_model": 0.3,
        "strength_clip": 0.3
      }
    },
    "4": {
      "class_type": "CLIPTextEncode",
      "inputs": {
        "clip": ["12", 1],
        "text": "PROMPT_TEXT"
      }
    },
    "5": {
      "class_type": "ConditioningZeroOut",
      "inputs": {
        "conditioning": ["4", 0]
      }
    },
    "6": {
      "class_type": "ModelSamplingAuraFlow",
      "inputs": {
        "model": ["12", 0],
        "shift": 3.0
      }
    },
    "7": {
      "class_type": "EmptySD3LatentImage",
      "inputs": {
        "width": 768,
        "height": 768,
        "batch_size": 1
      }
    },
    "8": {
      "class_type": "KSampler",
      "inputs": {
        "model": ["6", 0],
        "positive": ["4", 0],
        "negative": ["5", 0],
        "latent_image": ["7", 0],
        "seed": 2606010949,
        "steps": 8,
        "cfg": 1.0,
        "sampler_name": "res_multistep",
        "scheduler": "simple",
        "denoise": 1.0
      }
    },
    "9": {
      "class_type": "VAEDecode",
      "inputs": {
        "samples": ["8", 0],
        "vae": ["3", 0]
      }
    },
    "10": {
      "class_type": "SaveImage",
      "inputs": {
        "images": ["9", 0],
        "filename_prefix": "FILENAME_PREFIX"
      }
    }
  }
}
```

To add one RenCrow identity LoRA, insert a third `LoraLoader` after node `12` and rewire `CLIPTextEncode` and `ModelSamplingAuraFlow` to node `13`:

```json
"13": {
  "class_type": "LoraLoader",
  "inputs": {
    "model": ["12", 0],
    "clip": ["12", 1],
    "lora_name": "rencrow_zimage_ai_yoshikawa.safetensors",
    "strength_model": 0.35,
    "strength_clip": 0.35
  }
},
"4": {
  "class_type": "CLIPTextEncode",
  "inputs": {
    "clip": ["13", 1],
    "text": "rc_ai_yoshikawa, cute Japanese woman, natural smile, realistic portrait photo, soft window light, clear eyes, natural black hair, casual modern outfit, high quality"
  }
},
"6": {
  "class_type": "ModelSamplingAuraFlow",
  "inputs": {
    "model": ["13", 0],
    "shift": 3.0
  }
}
```

Do not set RenCrow identity LoRAs near `1.0` in the Turbo workflow. Verified fast path uses `0.35`; higher values can distort faces because these identity LoRAs were trained against the Z-Image Base checkpoint.

## 11. Prompt Guidance

Recommended positive prompt structure:

```text
subject, composition, lighting, camera/lens or illustration style, anatomy quality, detail level
```

Example:

```text
A sharp, natural portrait photo of a Japanese woman in her late twenties, standing in soft window light, realistic skin texture, symmetrical eyes, normal hands, detailed hair, 50mm lens, high quality, coherent anatomy
```

For Z-Image Turbo, negative prompting is implemented above as zeroed conditioning from the positive conditioning. Keep CFG at `1.0` unless a workflow is explicitly changed and re-tested.

## 12. ADetailer Equivalent

Use Impact Pack nodes for detail correction.

Main nodes:

```text
UltralyticsDetectorProvider
FaceDetailer
BboxDetectorSEGS
SEGSDetailer
```

Detector model names:

```text
bbox/face_yolov8n.pt
bbox/face_yolov8s.pt
bbox/hand_yolov8n.pt
segm/person_yolov8n-seg.pt
segm/person_yolov8s-seg.pt
```

Recommended first pass:

```text
face detector: bbox/face_yolov8n.pt
bbox_threshold: 0.35-0.50
bbox_dilation: 10
bbox_crop_factor: 3.0
detailer steps: 8
detailer cfg: 1.0
detailer denoise: 0.25-0.45
```

The detector was verified by generating a face crop preview:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\output\adetailer_detector_check_00001_.png
```

## 13. ControlNet

Available Z-Image ControlNet patch:

```text
Z-Image-Turbo-Fun-Controlnet-Union.safetensors
```

Relevant nodes:

```text
ModelPatchLoader
ZImageFunControlnet
CannyEdgePreprocessor
DepthAnythingV2Preprocessor
OpenposePreprocessor
DWPreprocessor
ControlNetApply
ControlNetApplyAdvanced
SetUnionControlNetType
```

Recommended flow:

```text
Load input image
Run preprocessor such as CannyEdgePreprocessor / DepthAnythingV2Preprocessor / OpenposePreprocessor
Load model patch with ModelPatchLoader
Apply patch with ZImageFunControlnet
Run KSampler
Decode and SaveImage
```

ControlNet workflows should be separately verified per preprocessor before production use, because each input type needs different strength and preprocessor settings.

## 14. Upload Input Image

If RenCrow needs image-to-image, ControlNet, or detailer input, upload images to ComfyUI first.

Endpoint:

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

The returned filename can be used by `LoadImage`:

```json
{
  "class_type": "LoadImage",
  "inputs": {
    "image": "uploaded_file.png"
  }
}
```

## 15. Error Handling

RenCrow should handle these cases:

```text
Connection refused:
  ComfyUI is not running or port 8188 is blocked.

HTTP 400 from /prompt:
  Invalid workflow JSON, missing node, missing model, or invalid node input.

Empty /history response:
  Job still running. Continue polling until timeout.

No image in outputs:
  Workflow completed without SaveImage output or failed internally. Check history status and ComfyUI logs.

CUDA out of memory:
  Retry with z_image_turbo_nvfp4.safetensors, qwen_3_4b_fp8_mixed.safetensors, smaller resolution, batch_size 1.
```

ComfyUI logs:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\user\comfyui.log
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\comfyui_0_0_0_0_stdout.log
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\comfyui_0_0_0_0_stderr.log
```

## 16. Security Notes

Current configuration listens on all network interfaces:

```text
0.0.0.0:8188
```

This is intended for Tailscale operation. Do not expose this port directly to the public internet.

Recommended RenCrow-side assumptions:

```text
Only trusted Tailscale peers call ComfyUI.
RenCrow validates prompt length, image dimensions, batch size, and output count before calling /prompt.
Use batch_size 1 by default.
Cap width/height to tested limits unless explicitly allowed.
Do not pass arbitrary user-supplied workflow JSON directly to ComfyUI.
```

## 17. Verified Output

Verification image:

```text
http://100.83.207.6:8188/view?filename=zimage_verification_00001_.png&type=output
```

Filesystem path:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\ComfyUI\output\zimage_verification_00001_.png
```

Verification status:

```text
Z-Image Turbo text-to-image: passed
Default LoRA stack AWPortrait-Z + REALSTAGRAM_ZIMG: passed
RenCrow identity LoRA fast Turbo stack at strength 0.35: passed
ADetailer detector recognition: passed
ComfyUI Tailscale access: passed
```

RenCrow identity LoRA success images generated via ComfyUI:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\zimage_lora_success_images_comfy\Ai_Yoshikawa.png
E:\GenerativeAI\Graphics\RenCrow_Image\zimage_lora_success_images_comfy\Eraiza_Ikeda.png
E:\GenerativeAI\Graphics\RenCrow_Image\zimage_lora_success_images_comfy\Fuka_koshiba.png
E:\GenerativeAI\Graphics\RenCrow_Image\zimage_lora_success_images_comfy\Haruka_Ayase.png
E:\GenerativeAI\Graphics\RenCrow_Image\zimage_lora_success_images_comfy\Haruka_Hukuhara.png
E:\GenerativeAI\Graphics\RenCrow_Image\zimage_lora_success_images_comfy\Mio_Imada.png
E:\GenerativeAI\Graphics\RenCrow_Image\zimage_lora_success_images_comfy\contact_sheet.png
```

Generation script used for the ComfyUI verification run:

```text
E:\GenerativeAI\Graphics\RenCrow_Image\scripts\generate_zimage_lora_comfy_fast.py
```

