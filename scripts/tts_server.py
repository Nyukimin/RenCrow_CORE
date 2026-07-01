from __future__ import annotations

import asyncio
import re
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from fastapi import FastAPI, HTTPException, WebSocket, WebSocketDisconnect
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, Field
from scipy.io import wavfile

from style_bert_vits2.constants import BASE_DIR, Languages
from style_bert_vits2.nlp import bert_models
from style_bert_vits2.tts_model import TTSModelHolder


HOST = "0.0.0.0"
PORT = 8765

CACHE_DIR = Path(__file__).resolve().parent / "cache"
CACHE_DIR.mkdir(parents=True, exist_ok=True)

VOICE_REGISTRY: dict[str, dict[str, Any]] = {
    "female_01": {
        "model_name": "amitaro",
        "speaker_id": 0,
        "default_style": "Neutral",
        "style_weight": 2.0,
        "language": "JP",
        "voice_profile": "lumina_female",
    },
    # Temporary alias until a dedicated male model is registered.
    "male_01": {
        "model_name": "amitaro",
        "speaker_id": 0,
        "default_style": "Neutral",
        "style_weight": 2.0,
        "language": "JP",
        "voice_profile": "lumina_male",
    },
}

CONNECTIVE_HINTS = (
    "ですが",
    "ただし",
    "なので",
    "つまり",
    "まず",
    "次に",
    "なお",
    "それと",
    "ここで",
)

LANGUAGE_MAP = {
    "JP": Languages.JP,
    "EN": Languages.EN,
    "ZH": Languages.ZH,
}


@dataclass
class LoadedVoice:
    voice_id: str
    model_name: str
    speaker_id: int
    default_style: str
    style_weight: float
    language: Languages
    voice_profile: str
    model: Any


@dataclass
class SessionState:
    session_id: str
    response_id: str | None
    voice_id: str
    speech_mode: str
    event: str
    urgency: str
    conversation_mode: str
    user_attention_required: bool
    text_buffer: str = ""
    next_seq: int = 1
    next_chunk_index: int = 0
    created_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))
    last_token_at: datetime = field(default_factory=lambda: datetime.now(timezone.utc))


class EmotionVectorModel(BaseModel):
    warmth: float = 0.5
    cheerfulness: float = 0.2
    seriousness: float = 0.4
    alertness: float = 0.2
    calmness: float = 0.5
    expressiveness: float = 0.2


class ProsodyModel(BaseModel):
    speed: float = 0.5
    pitch: float = 0.5
    pause: float = 0.5
    expressiveness: float = 0.2


class ReasonTraceModel(BaseModel):
    event: str = "system_notification"
    applied_context_rules: list[str] = Field(default_factory=list)
    applied_text_features: list[str] = Field(default_factory=list)
    voice_profile: str = ""


class EmotionStateModel(BaseModel):
    primary_emotion: str = "calm"
    emotion_vector: EmotionVectorModel = Field(default_factory=EmotionVectorModel)
    prosody: ProsodyModel = Field(default_factory=ProsodyModel)
    reason_trace: ReasonTraceModel = Field(default_factory=ReasonTraceModel)


class SessionStartMessage(BaseModel):
    type: str = "session_start"
    session_id: str
    response_id: str | None = None
    voice_id: str = "female_01"
    speech_mode: str = "conversational"
    context: dict[str, Any] = Field(default_factory=dict)


class TextDeltaMessage(BaseModel):
    type: str = "text_delta"
    session_id: str
    seq: int
    text: str
    emitted_at: str | None = None
    emotion_state: EmotionStateModel | None = None


class SessionEndMessage(BaseModel):
    type: str = "session_end"
    session_id: str
    is_final: bool = True


class SynthesizeRequest(BaseModel):
    text: str
    voice_id: str = "female_01"
    event: str = "conversation"
    speech_mode: str = "conversational"
    urgency: str = "normal"
    session_id: str | None = None
    emotion_state: EmotionStateModel | None = None


class RuntimeState:
    def __init__(self) -> None:
        self.ready: bool = False
        self.holder: TTSModelHolder | None = None
        self.voices: dict[str, LoadedVoice] = {}
        self.sessions: dict[str, SessionState] = {}
        self.synth_lock = asyncio.Lock()


runtime = RuntimeState()
app = FastAPI(title="PicoClaw TTS Server", version="0.2.0")
app.mount("/cache", StaticFiles(directory=str(CACHE_DIR)), name="cache")


def utcnow() -> datetime:
    return datetime.now(timezone.utc)


def clamp01(value: float) -> float:
    if value < 0.0:
        return 0.0
    if value > 1.0:
        return 1.0
    return value


def find_model_file_from_info(info: Any) -> str:
    for file_name in info.files:
        if file_name.endswith(".safetensors") and not file_name.startswith("."):
            return file_name
    raise RuntimeError(f"safetensors file not found for model '{info.name}'")


def normalize_text_for_speech(text: str) -> str:
    normalized = text
    replacements = {
        "PicoClaw": "ピコクロウ",
        "AI": "エーアイ",
        "LLM": "エル・エル・エム",
        "GPU": "ジーピーユー",
        "CPU": "シーピーユー",
        "HTTP": "エイチティーティーピー",
        "SSH": "エスエスエイチ",
        "WebSocket": "ウェブソケット",
    }
    for src, dst in replacements.items():
        normalized = normalized.replace(src, dst)

    normalized = re.sub(r"https?://\S+", "URL省略", normalized)
    normalized = re.sub(r"\s+", " ", normalized).strip()
    normalized = re.sub(r"[!！]{2,}", "！", normalized)
    normalized = re.sub(r"[?？]{2,}", "？", normalized)
    normalized = re.sub(r"[。]{2,}", "。", normalized)
    return normalized


def first_sentence_end_index(text: str) -> int | None:
    match = re.search(r"[。！？]", text)
    if not match:
        return None
    return match.end()


def find_split_index(text: str) -> int | None:
    sentence_end = first_sentence_end_index(text)
    if sentence_end is not None:
        return sentence_end
    if len(text) >= 30:
        comma_pos = text.rfind("、", 0, 30)
        if comma_pos != -1:
            return comma_pos + 1
    if len(text) >= 45:
        candidates: list[int] = []
        comma_pos = text.rfind("、", 0, 45)
        if comma_pos != -1:
            candidates.append(comma_pos + 1)
        for hint in CONNECTIVE_HINTS:
            pos = text.rfind(hint, 0, 45)
            if pos != -1:
                candidates.append(pos)
        if candidates:
            return max(candidates)
        return 45
    return None


def flush_chunks_from_buffer(text_buffer: str, force: bool = False) -> tuple[list[str], str]:
    chunks: list[str] = []
    working = text_buffer
    while True:
        split_idx = find_split_index(working)
        if split_idx is None:
            if force and working.strip():
                chunks.append(working.strip())
                working = ""
            break
        chunk = working[:split_idx].strip()
        if chunk:
            chunks.append(chunk)
        working = working[split_idx:].lstrip()
    return chunks, working


def derive_emotion(event: str, speech_mode: str, voice_profile: str) -> dict[str, Any]:
    if event in ("warning", "error"):
        primary = "alert"
        vector = {
            "warmth": 0.20,
            "cheerfulness": 0.10,
            "seriousness": 0.80,
            "alertness": 0.85,
            "calmness": 0.20,
            "expressiveness": 0.35,
        }
    elif speech_mode == "report":
        primary = "serious"
        vector = {
            "warmth": 0.35,
            "cheerfulness": 0.15,
            "seriousness": 0.75,
            "alertness": 0.35,
            "calmness": 0.55,
            "expressiveness": 0.20,
        }
    else:
        primary = "warm"
        vector = {
            "warmth": 0.72,
            "cheerfulness": 0.40,
            "seriousness": 0.32,
            "alertness": 0.18,
            "calmness": 0.68,
            "expressiveness": 0.36,
        }

    prosody = {
        "speed": 0.48 if primary != "alert" else 0.60,
        "pitch": 0.53,
        "pause": 0.55 if primary != "alert" else 0.30,
        "expressiveness": vector["expressiveness"],
    }

    return {
        "primary_emotion": primary,
        "emotion_vector": vector,
        "prosody": prosody,
        "reason_trace": {
            "event": event,
            "applied_context_rules": [],
            "applied_text_features": [],
            "voice_profile": voice_profile,
        },
    }


def resolve_emotion_state(
    explicit: EmotionStateModel | None,
    *,
    event: str,
    speech_mode: str,
    voice_profile: str,
) -> dict[str, Any]:
    if explicit is None:
        return derive_emotion(event, speech_mode, voice_profile)

    payload = explicit.model_dump()
    payload.setdefault("primary_emotion", "calm")
    payload.setdefault("emotion_vector", {})
    payload.setdefault("prosody", {})
    payload.setdefault("reason_trace", {})

    vector = payload["emotion_vector"]
    for key in ("warmth", "cheerfulness", "seriousness", "alertness", "calmness", "expressiveness"):
        vector[key] = clamp01(float(vector.get(key, 0.5)))

    prosody = payload["prosody"]
    for key in ("speed", "pitch", "pause", "expressiveness"):
        prosody[key] = clamp01(float(prosody.get(key, 0.5)))

    reason = payload["reason_trace"]
    reason.setdefault("event", event)
    reason.setdefault("applied_context_rules", [])
    reason.setdefault("applied_text_features", [])
    reason.setdefault("voice_profile", voice_profile)
    return payload


def plan_speech(chunk_text: str, speech_mode: str, prosody: dict[str, float]) -> dict[str, Any]:
    normalized = normalize_text_for_speech(chunk_text)
    if normalized.endswith("、"):
        pause_after = "short"
    elif normalized.endswith(("。", "！", "？")):
        pause_after = "medium"
    else:
        pause_after = "short"

    if speech_mode == "warning":
        pause_after = "short"

    return {
        "normalized_text": normalized,
        "speech_mode": speech_mode,
        "pause_after": pause_after,
        "delivery_trace": {
            "normalized": True,
            "prosody": prosody,
        },
    }


def map_to_stylebert_params(voice: LoadedVoice, emotion_state: dict[str, Any], speech_plan: dict[str, Any]) -> dict[str, Any]:
    primary = emotion_state.get("primary_emotion", "calm")
    vector = emotion_state.get("emotion_vector", {})
    prosody = emotion_state.get("prosody", {})

    sdp_ratio = 0.4
    style_weight = voice.style_weight

    if primary == "alert":
        style_weight = max(1.2, voice.style_weight - 0.4)
        sdp_ratio = 0.2
    elif primary == "serious":
        style_weight = max(1.4, voice.style_weight - 0.2)
        sdp_ratio = 0.3
    elif primary == "cheerful":
        style_weight = voice.style_weight + 0.1
        sdp_ratio = 0.45

    expressiveness = clamp01(float(vector.get("expressiveness", prosody.get("expressiveness", 0.3))))
    speed = clamp01(float(prosody.get("speed", 0.5)))

    return {
        "style": voice.default_style,
        "style_weight": style_weight + (expressiveness * 0.2),
        "sdp_ratio": max(0.1, min(0.6, sdp_ratio + ((speed - 0.5) * 0.3))),
    }


def synthesize_to_file(
    voice: LoadedVoice,
    session_id: str,
    chunk_index: int,
    text: str,
    params: dict[str, Any],
) -> dict[str, Any]:
    wav_path = CACHE_DIR / f"{session_id}_{chunk_index:03d}.wav"
    sample_rate, audio = voice.model.infer(
        text,
        language=voice.language,
        speaker_id=voice.speaker_id,
        sdp_ratio=params["sdp_ratio"],
        style=params["style"],
        style_weight=params["style_weight"],
    )
    wavfile.write(wav_path, sample_rate, audio)
    file_name = wav_path.name
    return {
        "audio_path": f"cache/{file_name}",
        "audio_url": f"/cache/{file_name}",
        "sample_rate": sample_rate,
    }


def get_voice_or_raise(voice_id: str) -> LoadedVoice:
    voice = runtime.voices.get(voice_id)
    if voice is None:
        raise HTTPException(status_code=404, detail=f"voice_id '{voice_id}' is not registered")
    return voice


@app.on_event("startup")
async def startup_event() -> None:
    runtime.ready = False

    holder = TTSModelHolder(
        BASE_DIR / "model_assets",
        "cpu",
        [("CPUExecutionProvider", {"arena_extend_strategy": "kSameAsRequested"})],
    )
    runtime.holder = holder

    bert_models.load_tokenizer(Languages.JP)
    bert_models.load_model(Languages.JP, device_map="cpu")

    for voice_id, cfg in VOICE_REGISTRY.items():
        model_name = cfg["model_name"]
        info = next((x for x in holder.models_info if x.name == model_name), None)
        if info is None:
            raise RuntimeError(f"model '{model_name}' for voice_id '{voice_id}' not found")

        model_file = find_model_file_from_info(info)
        model = holder.get_model(model_name, model_file)
        model.load()

        runtime.voices[voice_id] = LoadedVoice(
            voice_id=voice_id,
            model_name=model_name,
            speaker_id=int(cfg["speaker_id"]),
            default_style=str(cfg["default_style"]),
            style_weight=float(cfg["style_weight"]),
            language=LANGUAGE_MAP[str(cfg["language"])],
            voice_profile=str(cfg["voice_profile"]),
            model=model,
        )

    runtime.ready = True


@app.get("/health/live")
async def health_live() -> dict[str, str]:
    return {"status": "live"}


@app.get("/health/ready")
async def health_ready() -> dict[str, Any]:
    return {
        "status": "ready" if runtime.ready else "starting",
        "voices": list(runtime.voices.keys()),
    }


@app.get("/health/models")
async def health_models() -> dict[str, Any]:
    return {
        "voices": {
            voice_id: {
                "model_name": voice.model_name,
                "speaker_id": voice.speaker_id,
                "style": voice.default_style,
            }
            for voice_id, voice in runtime.voices.items()
        }
    }


@app.post("/synthesize")
async def synthesize(req: SynthesizeRequest) -> dict[str, Any]:
    if not runtime.ready:
        raise HTTPException(status_code=503, detail="TTS server is not ready")

    voice = get_voice_or_raise(req.voice_id)
    session_id = req.session_id or f"oneshot-{uuid.uuid4().hex[:8]}"
    emotion_state = resolve_emotion_state(
        req.emotion_state,
        event=req.event,
        speech_mode=req.speech_mode,
        voice_profile=voice.voice_profile,
    )
    speech_plan = plan_speech(
        chunk_text=req.text,
        speech_mode=req.speech_mode,
        prosody=emotion_state["prosody"],
    )
    params = map_to_stylebert_params(voice, emotion_state, speech_plan)

    async with runtime.synth_lock:
        result = await asyncio.to_thread(
            synthesize_to_file,
            voice,
            session_id,
            0,
            speech_plan["normalized_text"],
            params,
        )

    return {
        "session_id": session_id,
        "chunk_index": 0,
        "text": speech_plan["normalized_text"],
        "audio_path": result["audio_path"],
        "audio_url": result["audio_url"],
        "sample_rate": result["sample_rate"],
        "emotion_state": emotion_state,
        "speech_plan": speech_plan,
    }


@app.websocket("/sessions")
async def websocket_sessions(ws: WebSocket) -> None:
    print("WS connect")
    await ws.accept()
    print("WS accepted")

    try:
        while True:
            message = await ws.receive_json()
            message_type = message.get("type")
            print("WS recv", message_type)

            if message_type == "session_start":
                msg = SessionStartMessage.model_validate(message)
                if msg.voice_id not in runtime.voices:
                    await ws.send_json({
                        "type": "error",
                        "session_id": msg.session_id,
                        "code": "VOICE_NOT_FOUND",
                        "message": f"voice_id '{msg.voice_id}' is not registered",
                    })
                    continue

                context = msg.context or {}
                runtime.sessions[msg.session_id] = SessionState(
                    session_id=msg.session_id,
                    response_id=msg.response_id,
                    voice_id=msg.voice_id,
                    speech_mode=msg.speech_mode,
                    event=context.get("event", "conversation"),
                    urgency=context.get("urgency", "normal"),
                    conversation_mode=context.get("conversation_mode", "chat"),
                    user_attention_required=bool(context.get("user_attention_required", False)),
                )
                await ws.send_json({
                    "type": "session_started",
                    "session_id": msg.session_id,
                    "voice_id": msg.voice_id,
                })
                continue

            if message_type == "text_delta":
                msg = TextDeltaMessage.model_validate(message)
                session = runtime.sessions.get(msg.session_id)
                if session is None:
                    await ws.send_json({
                        "type": "error",
                        "session_id": msg.session_id,
                        "code": "SESSION_NOT_FOUND",
                        "message": "session is not active",
                    })
                    continue
                if msg.seq != session.next_seq:
                    await ws.send_json({
                        "type": "error",
                        "session_id": msg.session_id,
                        "code": "INVALID_SEQ",
                        "message": f"expected seq={session.next_seq}, got seq={msg.seq}",
                    })
                    continue

                session.next_seq += 1
                session.last_token_at = utcnow()
                session.text_buffer += msg.text

                flushed_chunks, remaining = flush_chunks_from_buffer(session.text_buffer, force=False)
                session.text_buffer = remaining
                voice = runtime.voices[session.voice_id]

                for chunk_text in flushed_chunks:
                    chunk_index = session.next_chunk_index
                    session.next_chunk_index += 1

                    emotion_state = resolve_emotion_state(
                        msg.emotion_state,
                        event=session.event,
                        speech_mode=session.speech_mode,
                        voice_profile=voice.voice_profile,
                    )
                    speech_plan = plan_speech(
                        chunk_text=chunk_text,
                        speech_mode=session.speech_mode,
                        prosody=emotion_state["prosody"],
                    )
                    params = map_to_stylebert_params(voice, emotion_state, speech_plan)

                    async with runtime.synth_lock:
                        result = await asyncio.to_thread(
                            synthesize_to_file,
                            voice,
                            session.session_id,
                            chunk_index,
                            speech_plan["normalized_text"],
                            params,
                        )

                    await ws.send_json({
                        "type": "audio_chunk_ready",
                        "session_id": session.session_id,
                        "chunk_index": chunk_index,
                        "text": speech_plan["normalized_text"],
                        "audio_path": result["audio_path"],
                        "audio_url": result["audio_url"],
                        "sample_rate": result["sample_rate"],
                        "pause_after": speech_plan["pause_after"],
                    })
                continue

            if message_type == "session_end":
                msg = SessionEndMessage.model_validate(message)
                session = runtime.sessions.get(msg.session_id)
                if session is None:
                    await ws.send_json({
                        "type": "error",
                        "session_id": msg.session_id,
                        "code": "SESSION_NOT_FOUND",
                        "message": "session is not active",
                    })
                    continue

                voice = runtime.voices[session.voice_id]
                flushed_chunks, remaining = flush_chunks_from_buffer(session.text_buffer, force=True)
                session.text_buffer = remaining

                for chunk_text in flushed_chunks:
                    chunk_index = session.next_chunk_index
                    session.next_chunk_index += 1
                    emotion_state = derive_emotion(session.event, session.speech_mode, voice.voice_profile)
                    speech_plan = plan_speech(
                        chunk_text=chunk_text,
                        speech_mode=session.speech_mode,
                        prosody=emotion_state["prosody"],
                    )
                    params = map_to_stylebert_params(voice, emotion_state, speech_plan)

                    async with runtime.synth_lock:
                        result = await asyncio.to_thread(
                            synthesize_to_file,
                            voice,
                            session.session_id,
                            chunk_index,
                            speech_plan["normalized_text"],
                            params,
                        )

                    await ws.send_json({
                        "type": "audio_chunk_ready",
                        "session_id": session.session_id,
                        "chunk_index": chunk_index,
                        "text": speech_plan["normalized_text"],
                        "audio_path": result["audio_path"],
                        "audio_url": result["audio_url"],
                        "sample_rate": result["sample_rate"],
                        "pause_after": speech_plan["pause_after"],
                    })

                runtime.sessions.pop(session.session_id, None)
                await ws.send_json({
                    "type": "session_completed",
                    "session_id": msg.session_id,
                })
                continue

            await ws.send_json({
                "type": "error",
                "session_id": message.get("session_id", ""),
                "code": "UNKNOWN_MESSAGE_TYPE",
                "message": f"unsupported type: {message_type}",
            })

    except WebSocketDisconnect:
        return


if __name__ == "__main__":
    import uvicorn

    uvicorn.run("tts_server:app", host=HOST, port=PORT, reload=False)
