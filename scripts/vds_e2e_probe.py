#!/usr/bin/env python3
"""E2E probe for Viewer voice-direct streaming (/voice-chat WebSocket)."""

from __future__ import annotations

import argparse
import json
import sys
import time
import uuid
import wave
from dataclasses import asdict, dataclass, field
from pathlib import Path

import requests
import websocket


DEFAULT_WAV = Path("tmp/stt_inputs/client_stt_input_20260609_140311.wav")
DEFAULT_WS_URL = "ws://127.0.0.1:18790/voice-chat"
DEFAULT_BASE_URL = "http://127.0.0.1:18790"
DEFAULT_PROMPT = (
    "この音声を聞いて、話している内容を日本語で要約し、最後に数字も書き出してください。"
)

PHASE1_MAX_COMMIT_TO_FIRST_TOKEN_MS = 15_000.0
PHASE1_MAX_COMMIT_TO_FINAL_MS = 25_000.0


@dataclass
class VDSRoundResult:
    i: int
    protocol: str = "vds_session_start_pcm16_commit"
    utterance_id: str = ""
    sample_rate: int = 16000
    channels: int = 1
    chunk_ms: int = 200
    realtime: bool = False
    events: list[str] = field(default_factory=list)
    messages: list[dict] = field(default_factory=list)
    delta_event_count: int = 0
    delta_text: str = ""
    final_text: str = ""
    error_code: str = ""
    ok: bool = False
    err: str = ""
    timings: dict[str, float] = field(default_factory=dict)
    metrics: dict[str, float] = field(default_factory=dict)
    sse_agent_response_ms: float | None = None
    sse_response_preview: str = ""


def load_pcm16_chunks(wav_path: Path, chunk_ms: int, tail_silence_ms: int = 0):
    if chunk_ms <= 0:
        raise ValueError("chunk_ms must be positive")
    if tail_silence_ms < 0:
        raise ValueError("tail_silence_ms must be >= 0")
    with wave.open(str(wav_path), "rb") as wav:
        channels = wav.getnchannels()
        sample_width = wav.getsampwidth()
        sample_rate = wav.getframerate()
        frame_count = wav.getnframes()
        if channels != 1:
            raise ValueError(f"VDS probe requires mono WAV, got channels={channels}")
        if sample_width != 2:
            raise ValueError(f"VDS probe requires PCM16 WAV, got sample_width={sample_width}")
        frames = wav.readframes(frame_count)
    frames_per_chunk = max(1, int(sample_rate * (chunk_ms / 1000.0)))
    bytes_per_frame = channels * sample_width
    chunk_bytes = frames_per_chunk * bytes_per_frame
    chunks = [frames[i : i + chunk_bytes] for i in range(0, len(frames), chunk_bytes)]
    if tail_silence_ms > 0:
        tail_frames = int(sample_rate * (tail_silence_ms / 1000.0))
        tail_bytes = b"\x00" * (tail_frames * bytes_per_frame)
        chunks.extend(tail_bytes[i : i + chunk_bytes] for i in range(0, len(tail_bytes), chunk_bytes))
    return sample_rate, channels, chunks


def new_utterance_id(prefix: str = "vds-probe") -> str:
    return f"{prefix}-{uuid.uuid4().hex[:8]}"


def build_session_start_payload(
    *,
    utterance_id: str,
    sample_rate: int,
    channels: int,
    prompt: str = "",
    voice_input_mode: str = "vds_sub",
    viewer_session_id: str = "vds-probe-session",
    channel: str = "viewer",
    chat_id: str = "viewer-user",
) -> dict:
    return {
        "type": "session.start",
        "utterance_id": utterance_id,
        "sample_rate": sample_rate,
        "channels": channels,
        "format": "pcm16le",
        "model": "Chat",
        "voice_input_mode": voice_input_mode,
        "prompt": prompt,
        "viewer_session_id": viewer_session_id,
        "channel": channel,
        "chat_id": chat_id,
    }


def build_session_commit_payload(*, utterance_id: str) -> dict:
    return {"type": "session.commit", "utterance_id": utterance_id}


def summarize_vds_messages(messages: list[dict], *, commit_at: float | None) -> tuple[dict[str, float], dict[str, float], str, str, str]:
    timings: dict[str, float] = {}
    metrics: dict[str, float] = {}
    delta_parts: list[str] = []
    final_text = ""
    error_code = ""
    first_delta_at: float | None = None
    final_at: float | None = None
    ready_at: float | None = None

    for msg in messages:
        ev_type = str(msg.get("type") or "")
        at = msg.get("_at")
        if ev_type == "session.ready" and isinstance(at, (int, float)):
            ready_at = float(at)
        if ev_type == "llm.delta":
            text = str(msg.get("text") or "")
            if text:
                delta_parts.append(text)
            if first_delta_at is None and isinstance(at, (int, float)):
                first_delta_at = float(at)
        if ev_type == "llm.final":
            final_text = str(msg.get("text") or "")
            if isinstance(at, (int, float)):
                final_at = float(at)
            raw_metrics = msg.get("metrics")
            if isinstance(raw_metrics, dict):
                for key in ("commit_to_first_token_ms", "commit_to_final_ms"):
                    value = raw_metrics.get(key)
                    if isinstance(value, (int, float)):
                        metrics[key] = float(value)
        if ev_type == "error":
            error_code = str(msg.get("error_code") or msg.get("message") or "error")

    if commit_at is not None:
        if first_delta_at is not None:
            timings["commit_to_first_delta_ms"] = round((first_delta_at - commit_at) * 1000.0, 1)
        if final_at is not None:
            timings["commit_to_final_ms"] = round((final_at - commit_at) * 1000.0, 1)
        if ready_at is not None:
            timings["commit_to_ready_ms"] = round((ready_at - commit_at) * 1000.0, 1)

    if metrics:
        if "commit_to_first_token_ms" in metrics:
            timings.setdefault("commit_to_first_delta_ms", metrics["commit_to_first_token_ms"])
        if "commit_to_final_ms" in metrics:
            timings.setdefault("commit_to_final_ms", metrics["commit_to_final_ms"])

    return timings, metrics, "".join(delta_parts), final_text, error_code


def meets_phase1_gate(timings: dict[str, float], *, wav_duration_sec: float, warm: bool) -> tuple[bool, list[str]]:
    reasons: list[str] = []
    first_ms = timings.get("commit_to_first_delta_ms")
    final_ms = timings.get("commit_to_final_ms")
    max_first = PHASE1_MAX_COMMIT_TO_FIRST_TOKEN_MS
    max_final = max(PHASE1_MAX_COMMIT_TO_FINAL_MS, wav_duration_sec * 1000.0)
    if warm:
        if first_ms is None:
            reasons.append("missing commit_to_first_delta_ms")
        elif first_ms > max_first:
            reasons.append(f"commit_to_first_delta_ms={first_ms} > {max_first}")
        if final_ms is None:
            reasons.append("missing commit_to_final_ms")
        elif final_ms > max_final:
            reasons.append(f"commit_to_final_ms={final_ms} > {max_final}")
    return not reasons, reasons


class SSECollector:
    def __init__(self, base_url: str, timeout: float):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self._stop = False
        self.events: list[dict] = []

    def start(self) -> None:
        import threading

        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        time.sleep(0.2)

    def stop(self) -> None:
        self._stop = True
        if getattr(self, "_thread", None):
            self._thread.join(timeout=3)

    def snapshot_len(self) -> int:
        return len(self.events)

    def _run(self) -> None:
        url = f"{self.base_url}/viewer/events"
        headers = {"Last-Event-ID": "9223372036854775807"}
        try:
            with requests.get(url, headers=headers, stream=True, timeout=(6, self.timeout)) as resp:
                if resp.status_code != 200:
                    return
                data_lines: list[str] = []
                for raw in resp.iter_lines(decode_unicode=True):
                    if self._stop:
                        break
                    line = raw or ""
                    if line == "":
                        payload = "\n".join(data_lines).strip()
                        data_lines = []
                        if payload:
                            try:
                                self.events.append(json.loads(payload))
                            except json.JSONDecodeError:
                                pass
                        continue
                    if line.startswith("data:"):
                        data_lines.append(line[5:].strip())
        except Exception:
            return


def wait_for_agent_response(collector: SSECollector, *, sent_at: float, cursor: int, timeout: float) -> tuple[float | None, str]:
    deadline = time.time() + timeout
    idx = cursor
    while time.time() < deadline:
        pending = collector.events[idx:]
        idx = len(collector.events)
        for ev in pending:
            if ev.get("type") != "agent.response":
                continue
            content = str(ev.get("content") or "")
            if content.strip():
                return round((time.time() - sent_at) * 1000.0, 1), content[:400]
        time.sleep(0.05)
    return None, ""


def run_vds_ws_bench(
    ws_url: str,
    wav_path: Path,
    *,
    rounds: int,
    wait_s: float,
    chunk_ms: int,
    realtime: bool,
    tail_silence_ms: int,
    prompt: str,
    sse_collector: SSECollector | None = None,
) -> list[VDSRoundResult]:
    sample_rate, channels, chunks = load_pcm16_chunks(wav_path, chunk_ms, tail_silence_ms)
    out: list[VDSRoundResult] = []
    for i in range(rounds):
        rec = VDSRoundResult(
            i=i + 1,
            sample_rate=sample_rate,
            channels=channels,
            chunk_ms=chunk_ms,
            realtime=realtime,
            utterance_id=new_utterance_id(),
        )
        sse_cursor = sse_collector.snapshot_len() if sse_collector else 0
        try:
            ws = websocket.create_connection(ws_url, timeout=6)
            ws.settimeout(max(1.0, wait_s))
            start_payload = build_session_start_payload(
                utterance_id=rec.utterance_id,
                sample_rate=sample_rate,
                channels=channels,
                prompt=prompt,
            )
            ws.send(json.dumps(start_payload, ensure_ascii=False))
            for chunk in chunks:
                ws.send_binary(chunk)
                if realtime:
                    time.sleep(chunk_ms / 1000.0)
            commit_at = time.time()
            ws.send(json.dumps(build_session_commit_payload(utterance_id=rec.utterance_id), ensure_ascii=False))
            end = time.time() + wait_s
            while time.time() < end:
                msg = ws.recv()
                now = time.time()
                if isinstance(msg, bytes):
                    rec.messages.append({"type": "binary", "bytes": len(msg), "_at": now})
                    continue
                obj = json.loads(msg)
                ev_type = str(obj.get("type") or "")
                if ev_type:
                    rec.events.append(ev_type)
                compact = {
                    "type": ev_type,
                    "text": str(obj.get("text") or "")[:400],
                    "message": str(obj.get("message") or "")[:180],
                    "error_code": str(obj.get("error_code") or ""),
                    "metrics": obj.get("metrics") if isinstance(obj.get("metrics"), dict) else None,
                    "_at": now,
                }
                rec.messages.append({k: v for k, v in compact.items() if v not in ("", None)})
                if ev_type == "llm.delta" and obj.get("text"):
                    rec.delta_text += str(obj["text"])
                if ev_type == "llm.final" and obj.get("text"):
                    rec.final_text = str(obj["text"])[:400]
                    rec.ok = bool(rec.final_text.strip())
                    break
                if ev_type == "error":
                    rec.error_code = compact.get("error_code") or compact.get("message") or "error"
                    rec.err = rec.error_code
                    break
            ws.close()
            timings, metrics, delta_text, final_text, error_code = summarize_vds_messages(
                rec.messages,
                commit_at=commit_at,
            )
            rec.timings = timings
            rec.metrics = metrics
            if delta_text:
                rec.delta_text = delta_text[:400]
            if final_text:
                rec.final_text = final_text[:400]
                rec.ok = bool(final_text.strip())
            if error_code and not rec.err:
                rec.err = error_code
            if sse_collector is not None and rec.ok:
                agent_ms, preview = wait_for_agent_response(
                    sse_collector,
                    sent_at=commit_at,
                    cursor=sse_cursor,
                    timeout=min(wait_s, 30.0),
                )
                rec.sse_agent_response_ms = agent_ms
                rec.sse_response_preview = preview
            if not rec.ok and not rec.err:
                rec.err = "timed out waiting for llm.final"
        except Exception as exc:  # noqa: BLE001
            rec.err = str(exc)
        out.append(rec)
    return out


def count_ok(records: list[VDSRoundResult]) -> int:
    return sum(1 for item in records if item.ok)


def delta_event_gate(records: list[VDSRoundResult], max_delta_events: int) -> list[dict]:
    if max_delta_events < 0:
        return []
    gates: list[dict] = []
    for rec in records:
        count = rec.events.count("llm.delta")
        rec.delta_event_count = count
        reasons = []
        if count > max_delta_events:
            reasons.append(f"delta_event_count={count} > {max_delta_events}")
        gates.append({"round": rec.i, "passed": not reasons, "reasons": reasons})
    return gates


def build_result(args, wav_path: Path, rounds: list[VDSRoundResult]) -> dict:
    wav_duration_sec = wav_duration(wav_path)
    gates: list[dict] = []
    for idx, rec in enumerate(rounds):
        passed, reasons = meets_phase1_gate(rec.timings, wav_duration_sec=wav_duration_sec, warm=idx > 0 or args.warm_gate_first)
        gates.append({"round": rec.i, "passed": passed, "reasons": reasons})
    delta_gates = delta_event_gate(rounds, getattr(args, "max_delta_events", -1))
    return {
        "ws_url": args.ws_url,
        "base_url": args.base_url,
        "wav": str(wav_path),
        "wav_duration_sec": round(wav_duration_sec, 3),
        "rounds": len(rounds),
        "success": f"{count_ok(rounds)}/{len(rounds)}",
        "phase1_gate": gates,
        "delta_event_gate": delta_gates,
        "timestamp": time.strftime("%Y-%m-%d %H:%M:%S"),
        "results": [asdict(rec) for rec in rounds],
    }


def result_exit_code(args, result: dict) -> int:
    rounds = result.get("results") or []
    if args.require_llm_final:
        ok_count = sum(1 for item in rounds if item.get("ok"))
        if ok_count != len(rounds):
            return 2
    if args.require_phase1_gate:
        for gate in result.get("phase1_gate") or []:
            if not gate.get("passed"):
                return 3
    if getattr(args, "max_delta_events", -1) >= 0:
        for gate in result.get("delta_event_gate") or []:
            if not gate.get("passed"):
                return 4
    return 0


def wav_duration(wav_path: Path) -> float:
    with wave.open(str(wav_path), "rb") as wav:
        return wav.getnframes() / float(wav.getframerate())


def render_markdown(report: dict) -> str:
    lines = [
        "# VDS /voice-chat E2E probe",
        "",
        f"- 実行時刻: `{report['timestamp']}`",
        f"- WAV: `{report['wav']}`",
        f"- 音声長: `{report['wav_duration_sec']:.2f}s`",
        f"- WS: `{report['ws_url']}`",
        f"- 成功: `{report['success']}`",
        "",
        "## ラウンド",
        "",
        "| round | ok | delta events | commit→delta(ms) | commit→final(ms) | SSE agent.response(ms) | final preview | error |",
        "|---:|---:|---:|---:|---:|---:|---|---|",
    ]
    for item in report["results"]:
        timings = item.get("timings") or {}
        lines.append(
            "| {i} | {ok} | {delta_events} | {first} | {final} | {sse} | {preview} | {err} |".format(
                i=item.get("i"),
                ok="yes" if item.get("ok") else "no",
                delta_events=item.get("delta_event_count", ""),
                first=timings.get("commit_to_first_delta_ms", ""),
                final=timings.get("commit_to_final_ms", ""),
                sse=item.get("sse_agent_response_ms", ""),
                preview=str(item.get("final_text") or "")[:80].replace("|", "/"),
                err=str(item.get("err") or item.get("error_code") or "")[:80].replace("|", "/"),
            )
        )
    lines.extend(["", "## Phase 1 gate", ""])
    for gate in report.get("phase1_gate") or []:
        status = "PASS" if gate.get("passed") else "FAIL"
        reasons = "; ".join(gate.get("reasons") or [])
        lines.append(f"- round {gate.get('round')}: **{status}** {reasons}")
    if report.get("delta_event_gate"):
        lines.extend(["", "## Delta event gate", ""])
        for gate in report.get("delta_event_gate") or []:
            status = "PASS" if gate.get("passed") else "FAIL"
            reasons = "; ".join(gate.get("reasons") or [])
            lines.append(f"- round {gate.get('round')}: **{status}** {reasons}")
    return "\n".join(lines) + "\n"


def main() -> None:
    parser = argparse.ArgumentParser(description="VDS E2E probe for /voice-chat WebSocket")
    parser.add_argument("--wav", default=str(DEFAULT_WAV), help="Path to mono PCM16 WAV")
    parser.add_argument("--ws-url", default=DEFAULT_WS_URL)
    parser.add_argument("--base-url", default=DEFAULT_BASE_URL, help="rencrow HTTP base for optional SSE")
    parser.add_argument("--prompt", default=DEFAULT_PROMPT)
    parser.add_argument("--rounds", type=int, default=2)
    parser.add_argument("--wait", type=float, default=120.0)
    parser.add_argument("--chunk-ms", type=int, default=200)
    parser.add_argument("--realtime", action="store_true")
    parser.add_argument("--tail-silence-ms", type=int, default=0)
    parser.add_argument("--with-sse", action="store_true", help="Also wait for SSE agent.response")
    parser.add_argument("--require-llm-final", action="store_true")
    parser.add_argument("--require-phase1-gate", action="store_true")
    parser.add_argument("--max-delta-events", type=int, default=-1, help="Fail when a round receives more llm.delta events")
    parser.add_argument("--warm-gate-first", action="store_true", help="Apply phase1 gate to round 1 as well")
    parser.add_argument("--write-md", default="", help="Optional markdown summary path")
    args = parser.parse_args()

    wav_path = Path(args.wav)
    if not wav_path.exists():
        raise SystemExit(f"wav not found: {wav_path}")

    collector = None
    if args.with_sse:
        collector = SSECollector(args.base_url, timeout=max(args.wait + 10, 30))
        collector.start()

    try:
        rounds = run_vds_ws_bench(
            args.ws_url,
            wav_path,
            rounds=max(1, args.rounds),
            wait_s=args.wait,
            chunk_ms=args.chunk_ms,
            realtime=args.realtime,
            tail_silence_ms=args.tail_silence_ms,
            prompt=args.prompt,
            sse_collector=collector,
        )
    finally:
        if collector is not None:
            collector.stop()

    report = build_result(args, wav_path, rounds)
    print(json.dumps(report, ensure_ascii=False, indent=2))
    if args.write_md:
        Path(args.write_md).write_text(render_markdown(report), encoding="utf-8")
    sys.exit(result_exit_code(args, report))


if __name__ == "__main__":
    main()
