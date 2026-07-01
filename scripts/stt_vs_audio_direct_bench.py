#!/usr/bin/env python3
"""Compare end-to-end latency: STT->text->Chat vs audio->Chat (input_audio)."""

from __future__ import annotations

import argparse
import base64
import json
import sys
import threading
import time
import urllib.request
from dataclasses import asdict, dataclass, field
from datetime import datetime, timezone
from pathlib import Path

import requests


DEFAULT_WAV = Path("tmp/stt_inputs/client_stt_input_20260609_140311.wav")
DEFAULT_BASE = "http://127.0.0.1:18790"
DEFAULT_LLM_URL = "http://192.168.1.207:8081/v1/chat/completions"
DEFAULT_PROMPT = (
    "この音声を聞いて、話している内容を日本語で要約し、最後に数字も書き出してください。"
)


@dataclass
class PathResult:
    path: str
    ok: bool = False
    error: str = ""
    bench_id: str = ""
    stt_ms: float | None = None
    send_ms: float | None = None
    total_ms: float | None = None
    llm_first_token_ms: float | None = None
    llm_response_complete_ms: float | None = None
    agent_response_ms: float | None = None
    stt_text_preview: str = ""
    response_preview: str = ""


class SSECollector:
    def __init__(self, base_url: str, timeout: float):
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self._stop = threading.Event()
        self._ready = threading.Event()
        self._ready_error: Exception | None = None
        self.events: list[dict] = []
        self._lock = threading.Lock()
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        self._thread = threading.Thread(target=self._run, daemon=True)
        self._thread.start()
        if not self._ready.wait(timeout=15):
            raise TimeoutError("SSE /viewer/events did not become ready")
        if self._ready_error:
            raise self._ready_error

    def stop(self) -> None:
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=3)

    def snapshot_len(self) -> int:
        with self._lock:
            return len(self.events)

    def _append(self, event: dict) -> None:
        with self._lock:
            self.events.append(event)

    def _run(self) -> None:
        url = f"{self.base_url}/viewer/events"
        headers = {"Last-Event-ID": "9223372036854775807"}
        try:
            with requests.get(url, headers=headers, stream=True, timeout=(6, self.timeout)) as resp:
                if resp.status_code != 200:
                    self._ready_error = RuntimeError(f"SSE HTTP {resp.status_code}")
                    self._ready.set()
                    return
                self._ready.set()
                data_lines: list[str] = []
                for raw in resp.iter_lines(decode_unicode=True):
                    if self._stop.is_set():
                        break
                    line = raw or ""
                    if line == "":
                        payload = "\n".join(data_lines).strip()
                        data_lines = []
                        if payload:
                            try:
                                self._append(json.loads(payload))
                            except json.JSONDecodeError:
                                pass
                        continue
                    if line.startswith("data:"):
                        data_lines.append(line[5:].strip())
        except Exception as exc:  # noqa: BLE001
            if not self._ready.is_set():
                self._ready_error = exc
                self._ready.set()


def wait_for_bench_result(
    collector: SSECollector,
    sent_at_ms: int,
    cursor: int,
    timeout: float,
) -> tuple[float | None, float | None, float | None, str]:
    deadline = time.time() + timeout
    llm_first_token_ms: float | None = None
    llm_response_complete_ms: float | None = None
    agent_response_ms: float | None = None
    response_preview = ""
    idx = cursor

    while time.time() < deadline:
        with collector._lock:
            pending = collector.events[idx:]
            idx = len(collector.events)
        for ev in pending:
            at_ms = _event_at_ms(ev)
            if at_ms and at_ms + 200 < sent_at_ms:
                continue

            ev_type = ev.get("type", "")
            if ev_type == "metrics.latency":
                try:
                    payload = json.loads(ev.get("content") or "{}")
                except json.JSONDecodeError:
                    continue
                point = payload.get("point")
                elapsed = payload.get("elapsed_ms")
                if payload.get("kind") == "llm" and point == "first_token" and isinstance(elapsed, (int, float)):
                    if llm_first_token_ms is None:
                        llm_first_token_ms = float(elapsed)
                if payload.get("kind") == "llm" and point == "response_complete" and isinstance(elapsed, (int, float)):
                    llm_response_complete_ms = float(elapsed)
                    response_preview = (payload.get("detail") or "")[:400]
                    return llm_first_token_ms, llm_response_complete_ms, agent_response_ms, response_preview

            if ev_type == "agent.response":
                content = ev.get("content") or ""
                if at_ms and at_ms >= sent_at_ms:
                    agent_response_ms = max(0.0, float(at_ms - sent_at_ms))
                    response_preview = content[:400]
                    return llm_first_token_ms, llm_response_complete_ms, agent_response_ms, response_preview

            if ev_type in {"agent.error", "mailbox.error", "worker.classified_failure"}:
                if at_ms and at_ms >= sent_at_ms:
                    raise RuntimeError((ev.get("content") or "")[:300])

        time.sleep(0.05)

    raise TimeoutError("timeout waiting for llm response_complete / agent.response")


def _event_at_ms(ev: dict) -> int | None:
    ts = (ev.get("timestamp") or "").strip()
    if not ts:
        return None
    try:
        dt = datetime.fromisoformat(ts.replace("Z", "+00:00"))
        return int(dt.timestamp() * 1000)
    except ValueError:
        return None


def transcribe_stt(base_url: str, wav_path: Path, timeout: float) -> tuple[float, str]:
    t0 = time.time()
    with wav_path.open("rb") as fh:
        resp = requests.post(
            f"{base_url.rstrip('/')}/stt/chat-input",
            files={"file": (wav_path.name, fh, "audio/wav")},
            timeout=timeout,
        )
    stt_ms = (time.time() - t0) * 1000.0
    resp.raise_for_status()
    text = (resp.json().get("text") or "").strip()
    if not text:
        raise RuntimeError("STT returned empty text")
    return stt_ms, text


def send_text_message(base_url: str, message: str, timeout: float) -> float:
    t0 = time.time()
    resp = requests.post(
        f"{base_url.rstrip('/')}/viewer/send",
        json={"message": message},
        timeout=timeout,
    )
    send_ms = (time.time() - t0) * 1000.0
    resp.raise_for_status()
    return send_ms


def send_audio_direct(base_url: str, message: str, wav_path: Path, timeout: float) -> float:
    t0 = time.time()
    with wav_path.open("rb") as fh:
        resp = requests.post(
            f"{base_url.rstrip('/')}/viewer/send",
            data={"message": message},
            files={"attachments": (wav_path.name, fh, "audio/wav")},
            timeout=timeout,
        )
    send_ms = (time.time() - t0) * 1000.0
    resp.raise_for_status()
    return send_ms


def run_stt_text_llm(
    base_url: str,
    wav_path: Path,
    collector: SSECollector,
    timeout: float,
    bench_id: str,
) -> PathResult:
    result = PathResult(path="stt_text_llm", bench_id=bench_id)
    cursor = collector.snapshot_len()
    t0 = time.time()
    try:
        stt_ms, text = transcribe_stt(base_url, wav_path, timeout)
        result.stt_ms = round(stt_ms, 1)
        result.stt_text_preview = text[:200]
        message = f"[{bench_id}] {text}"
        send_ms = send_text_message(base_url, message, timeout)
        result.send_ms = round(send_ms, 1)
        sent_at_ms = int(time.time() * 1000)
        first, complete, agent, preview = wait_for_bench_result(
            collector, sent_at_ms, cursor, timeout
        )
        result.llm_first_token_ms = round(first, 1) if first is not None else None
        result.llm_response_complete_ms = round(complete, 1) if complete is not None else None
        result.agent_response_ms = round(agent, 1) if agent is not None else None
        result.response_preview = preview
        result.total_ms = round((time.time() - t0) * 1000.0, 1)
        result.ok = True
    except Exception as exc:  # noqa: BLE001
        result.error = str(exc)
        result.total_ms = round((time.time() - t0) * 1000.0, 1)
    return result


def run_audio_direct_llm(
    base_url: str,
    wav_path: Path,
    prompt: str,
    collector: SSECollector,
    timeout: float,
    bench_id: str,
) -> PathResult:
    result = PathResult(path="audio_direct_llm", bench_id=bench_id)
    cursor = collector.snapshot_len()
    t0 = time.time()
    try:
        message = f"[{bench_id}] {prompt}"
        send_ms = send_audio_direct(base_url, message, wav_path, timeout)
        result.send_ms = round(send_ms, 1)
        sent_at_ms = int(time.time() * 1000)
        first, complete, agent, preview = wait_for_bench_result(
            collector, sent_at_ms, cursor, timeout
        )
        result.llm_first_token_ms = round(first, 1) if first is not None else None
        result.llm_response_complete_ms = round(complete, 1) if complete is not None else None
        result.agent_response_ms = round(agent, 1) if agent is not None else None
        result.response_preview = preview
        result.total_ms = round((time.time() - t0) * 1000.0, 1)
        result.ok = True
    except Exception as exc:  # noqa: BLE001
        result.error = str(exc)
        result.total_ms = round((time.time() - t0) * 1000.0, 1)
    return result


def run_direct_llm_api(
    llm_url: str,
    wav_path: Path,
    prompt: str,
    timeout: float,
) -> PathResult:
    result = PathResult(path="direct_llm_api_input_audio")
    t0 = time.time()
    try:
        data = base64.b64encode(wav_path.read_bytes()).decode("ascii")
        payload = {
            "model": "Chat",
            "think": False,
            "max_tokens": 400,
            "messages": [
                {
                    "role": "user",
                    "content": [
                        {"type": "text", "text": prompt},
                        {"type": "input_audio", "input_audio": {"data": data, "format": "wav"}},
                    ],
                }
            ],
        }
        req = urllib.request.Request(
            llm_url,
            data=json.dumps(payload).encode("utf-8"),
            headers={"Content-Type": "application/json"},
            method="POST",
        )
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = json.loads(resp.read().decode("utf-8"))
        total_ms = (time.time() - t0) * 1000.0
        content = body.get("choices", [{}])[0].get("message", {}).get("content", "")
        result.total_ms = round(total_ms, 1)
        result.llm_response_complete_ms = round(total_ms, 1)
        result.response_preview = (content or "")[:400]
        result.ok = bool((content or "").strip())
        if not result.ok:
            result.error = "empty LLM response"
    except Exception as exc:  # noqa: BLE001
        result.error = str(exc)
        result.total_ms = round((time.time() - t0) * 1000.0, 1)
    return result


def wav_duration_sec(wav_path: Path) -> float:
    import wave

    with wave.open(str(wav_path), "rb") as wav:
        return wav.getnframes() / wav.getframerate()


def render_markdown(report: dict) -> str:
    lines = [
        "# STT→text→LLM vs audio→LLM 実測比較",
        "",
        f"- 実行時刻: `{report['timestamp']}`",
        f"- WAV: `{report['wav']}`",
        f"- 音声長: `{report['wav_duration_sec']:.2f}s`",
        f"- picoclaw base: `{report['base_url']}`",
        f"- LLM API: `{report['llm_url']}`",
        "",
        "## 結果サマリ",
        "",
        "| 経路 | 成功 | STT(ms) | 送信(ms) | LLM初token(ms) | LLM完了(ms) | agent.response(ms) | 合計(ms) |",
        "|---|---:|---:|---:|---:|---:|---:|---:|",
    ]
    for key in ("stt_text_llm", "audio_direct_llm", "direct_llm_api_input_audio"):
        r = report["results"][key]
        lines.append(
            "| {path} | {ok} | {stt} | {send} | {first} | {complete} | {agent} | {total} |".format(
                path=key,
                ok="OK" if r["ok"] else "NG",
                stt=_fmt(r.get("stt_ms")),
                send=_fmt(r.get("send_ms")),
                first=_fmt(r.get("llm_first_token_ms")),
                complete=_fmt(r.get("llm_response_complete_ms")),
                agent=_fmt(r.get("agent_response_ms")),
                total=_fmt(r.get("total_ms")),
            )
        )

    a = report["results"]["stt_text_llm"]
    b = report["results"]["audio_direct_llm"]
    lines.extend(["", "## 判定", ""])
    if a.get("ok") and b.get("ok"):
        a_total = a.get("total_ms") or 0
        b_total = b.get("total_ms") or 0
        if a_total < b_total:
            lines.append(f"- picoclaw 経路で早い方: **stt_text_llm**（差 `{round(b_total - a_total, 1)}ms`）")
        elif b_total < a_total:
            lines.append(f"- picoclaw 経路で早い方: **audio_direct_llm**（差 `{round(a_total - b_total, 1)}ms`）")
        else:
            lines.append("- picoclaw 経路: **同程度**")
    else:
        lines.append("- picoclaw 経路の比較: いずれかが失敗")

    lines.extend(["", "## 詳細", ""])
    for key in ("stt_text_llm", "audio_direct_llm", "direct_llm_api_input_audio"):
        r = report["results"][key]
        lines.append(f"### {key}")
        lines.append("")
        if r.get("error"):
            lines.append(f"- error: `{r['error']}`")
        if r.get("stt_text_preview"):
            lines.append(f"- STT text: `{r['stt_text_preview']}`")
        if r.get("response_preview"):
            lines.append(f"- response: `{r['response_preview']}`")
        lines.append("")
    return "\n".join(lines)


def _fmt(value) -> str:
    if value is None:
        return "—"
    return str(value)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--wav", default=str(DEFAULT_WAV))
    parser.add_argument("--base-url", default=DEFAULT_BASE)
    parser.add_argument("--llm-url", default=DEFAULT_LLM_URL)
    parser.add_argument("--prompt", default=DEFAULT_PROMPT)
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--out-json", default="tmp/stt_vs_audio_direct_bench_latest.json")
    parser.add_argument("--out-md", default="tmp/stt_vs_audio_direct_bench_latest.md")
    args = parser.parse_args()

    wav_path = Path(args.wav)
    if not wav_path.is_file():
        raise SystemExit(f"wav not found: {wav_path}")

    collector = SSECollector(args.base_url, args.timeout * 3)
    collector.start()
    time.sleep(5)  # drain SSE history replay before measuring

    results: dict[str, PathResult] = {}
    try:
        results["stt_text_llm"] = run_stt_text_llm(
            args.base_url,
            wav_path,
            collector,
            args.timeout,
            bench_id=f"bench-stt-{int(time.time())}",
        )
        time.sleep(3)
        results["audio_direct_llm"] = run_audio_direct_llm(
            args.base_url,
            wav_path,
            args.prompt,
            collector,
            args.timeout,
            bench_id=f"bench-audio-{int(time.time())}",
        )
    finally:
        collector.stop()

    results["direct_llm_api_input_audio"] = run_direct_llm_api(
        args.llm_url, wav_path, args.prompt, args.timeout
    )

    report = {
        "timestamp": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC"),
        "wav": str(wav_path),
        "wav_duration_sec": wav_duration_sec(wav_path),
        "base_url": args.base_url,
        "llm_url": args.llm_url,
        "prompt": args.prompt,
        "results": {k: asdict(v) for k, v in results.items()},
    }

    out_json = Path(args.out_json)
    out_md = Path(args.out_md)
    out_json.parent.mkdir(parents=True, exist_ok=True)
    out_json.write_text(json.dumps(report, ensure_ascii=False, indent=2), encoding="utf-8")
    out_md.write_text(render_markdown(report), encoding="utf-8")

    print(json.dumps(report, ensure_ascii=False, indent=2))
    print(f"\nWrote {out_json} and {out_md}")
    return 0 if results["stt_text_llm"].ok and results["audio_direct_llm"].ok else 1


if __name__ == "__main__":
    raise SystemExit(main())
