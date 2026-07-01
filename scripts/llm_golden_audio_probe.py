#!/usr/bin/env python3
"""Probe RenCrow_LLM with golden WAV via OpenAI-compatible input_audio."""

from __future__ import annotations

import argparse
import base64
import json
import sys
import urllib.error
import urllib.request
from pathlib import Path


DEFAULT_WAV = Path("tmp/stt_inputs/client_stt_input_20260609_140311.wav")
DEFAULT_URL = "http://192.168.1.207:8081/v1/chat/completions"
DEFAULT_PROMPT = (
    "この音声を聞いて、話している内容を日本語で要約し、最後に数字も書き出してください。"
)


def load_manifest(path: Path) -> dict:
    return json.loads(path.read_text(encoding="utf-8"))


def resolve_wav(args: argparse.Namespace) -> Path:
    if args.wav:
        return Path(args.wav)
    if args.manifest:
        manifest = load_manifest(Path(args.manifest))
        trimmed = manifest.get("trimmed_wav") or manifest.get("wav")
        if not trimmed:
            raise SystemExit(f"manifest missing trimmed_wav: {args.manifest}")
        return Path(trimmed)
    return DEFAULT_WAV


def post_chat(url: str, model: str, prompt: str, wav_path: Path, timeout: float, max_tokens: int) -> dict:
    data = base64.b64encode(wav_path.read_bytes()).decode("ascii")
    payload = {
        "model": model,
        "think": False,
        "max_tokens": max_tokens,
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
        url,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.loads(resp.read().decode("utf-8"))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url", default=DEFAULT_URL)
    parser.add_argument("--model", default="Chat")
    parser.add_argument("--wav", help="trimmed golden wav path")
    parser.add_argument("--manifest", help="dataset manifest json (uses trimmed_wav)")
    parser.add_argument("--prompt", default=DEFAULT_PROMPT)
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--max-tokens", type=int, default=400)
    parser.add_argument("--out", help="write full JSON response")
    args = parser.parse_args()

    wav_path = resolve_wav(args)
    if not wav_path.is_file():
        raise SystemExit(f"wav not found: {wav_path}")

    try:
        body = post_chat(args.url, args.model, args.prompt, wav_path, args.timeout, args.max_tokens)
    except urllib.error.HTTPError as exc:
        err = exc.read().decode("utf-8", errors="replace")
        print(err[:4000], file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001
        print(f"request failed: {exc}", file=sys.stderr)
        return 1

    content = (
        body.get("choices", [{}])[0]
        .get("message", {})
        .get("content", "")
    )
    print(f"wav: {wav_path}")
    print(f"model: {args.model}")
    print("--- response ---")
    print(content)
    if args.out:
        Path(args.out).write_text(json.dumps(body, ensure_ascii=False, indent=2), encoding="utf-8")
    return 0 if content.strip() else 1


if __name__ == "__main__":
    raise SystemExit(main())
