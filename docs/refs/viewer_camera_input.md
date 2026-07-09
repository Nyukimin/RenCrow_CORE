# Viewer Camera Input

## Current Contract

Viewer camera input is browser-side still-frame capture. The same capture path
also supports browser display/tab capture for external media sources.

Supported source types:

- Device camera/microphone.
- Browser screen/window/tab capture, including shared tab audio when the
  browser and selected source provide an audio track.
- Local image/video/audio files through the normal attachment control.

The Viewer displays the selected source as a live MediaStream preview. The
preview should not be frame-rate capped by the still-frame pipeline.

For LLM input, the Viewer converts the selected video preview to JPEG still
frames in the browser. The current continuous capture mode is 1 FPS and stores
the frames as normal Viewer image attachments.

When display/tab capture includes audio, STT can use a cloned copy of that
audio track instead of opening a new microphone stream. The audio still flows
through the existing Viewer STT path and must stay behind the RenCrow module
boundary.

Runtime routing must remain:

```text
rencrow -> RenCrow_LLM -> LLM backend
rencrow -> RenCrow_STT -> STT backend
```

The Viewer must not call Gemma4, MLX-VLM, Ollama, STT providers, or other
model/tool backends directly.

## Reason

Current MLX-VLM 0.6.2 chat completions accepts image inputs, but the tested
`video_url` / `input_video` content parts are rejected by schema validation.
For Gemma4 video understanding, the practical input shape is therefore a frame
sequence.

## Future Option

The transport may be changed later to one-second video chunks:

```text
camera/display stream -> 1s video chunk -> rencrow -> RenCrow_LLM -> frame extraction or input_video
```

That change should stay behind the same RenCrow routing boundary. The upper
Viewer camera UI should not need to call model backends directly.
