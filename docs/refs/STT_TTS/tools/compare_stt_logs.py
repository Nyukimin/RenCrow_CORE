#!/usr/bin/env python3
import argparse
import datetime as dt
import json
import re
from collections import Counter
from pathlib import Path


CLIENT_HEADER_RE = re.compile(r"^\s*(\d{1,2}:\d{2}:\d{2})\s*·\s*([a-z_]+)\s*$")
STT_JSON_RE = re.compile(r"\[stt\]\s*(\{.*\})")
TRANSCRIBED_RE = re.compile(r'transcribed:\s*"(.*)"')


def _read_lines(path: Path):
    return path.read_text(encoding="utf-8", errors="replace").splitlines()


def parse_client_log(path: Path):
    lines = _read_lines(path)
    events = []
    i = 0
    while i < len(lines):
        line = lines[i].strip()
        m = CLIENT_HEADER_RE.match(line)
        if not m:
            i += 1
            continue
        hhmmss, event_type = m.group(1), m.group(2)
        payload = ""
        if i + 1 < len(lines):
            nxt = lines[i + 1].rstrip()
            if not CLIENT_HEADER_RE.match(nxt.strip()):
                payload = nxt.strip()
                i += 1
        events.append(
            {
                "source": "client",
                "raw_time": hhmmss,
                "event": event_type,
                "payload": payload,
            }
        )
        i += 1
    return events


def _event_from_stt_json(obj):
    event = str(obj.get("event", "")).strip()
    ts = str(obj.get("ts", "")).strip()
    payload_bits = []
    for key in ("phase", "result", "error_code", "message", "session_id", "request_id"):
        if key in obj and str(obj[key]).strip():
            payload_bits.append(f"{key}={obj[key]}")
    payload = " ".join(payload_bits)
    return event, ts, payload


def parse_server_log(path: Path):
    lines = _read_lines(path)
    events = []

    for line in lines:
        line_stripped = line.strip()
        if not line_stripped:
            continue

        m = STT_JSON_RE.search(line_stripped)
        if m:
            try:
                obj = json.loads(m.group(1))
            except json.JSONDecodeError:
                obj = {}
            event, ts, payload = _event_from_stt_json(obj)
            if event:
                events.append(
                    {
                        "source": "server",
                        "raw_time": ts,
                        "event": event,
                        "payload": payload,
                    }
                )
            continue

        if "speech start detected" in line_stripped:
            events.append(
                {
                    "source": "server",
                    "raw_time": "",
                    "event": "speech_start",
                    "payload": "",
                }
            )
            continue

        if "speech end (" in line_stripped:
            events.append(
                {
                    "source": "server",
                    "raw_time": "",
                    "event": "speech_end",
                    "payload": "",
                }
            )
            continue

        if "transcribed:" in line_stripped:
            mm = TRANSCRIBED_RE.search(line_stripped)
            payload = mm.group(1) if mm else ""
            events.append(
                {
                    "source": "server",
                    "raw_time": "",
                    "event": "final",
                    "payload": payload,
                }
            )

    return events


def _safe_iso_to_hms(s):
    try:
        return dt.datetime.fromisoformat(s.replace("Z", "+00:00")).strftime("%H:%M:%S")
    except Exception:
        return s


def summarize_counts(events):
    return Counter(e["event"] for e in events)


def to_rows_for_preview(events, limit=30):
    rows = []
    for e in events[:limit]:
        t = e["raw_time"]
        if e["source"] == "server" and "T" in t:
            t = _safe_iso_to_hms(t)
        rows.append((t, e["event"], e["payload"]))
    return rows


def render_markdown(client_events, server_events, client_path, server_path):
    c_count = summarize_counts(client_events)
    s_count = summarize_counts(server_events)
    all_keys = sorted(set(c_count.keys()) | set(s_count.keys()))

    lines = []
    lines.append("# STT ログ比較レポート")
    lines.append("")
    lines.append("## 入力ファイル")
    lines.append(f"- client: `{client_path}`")
    lines.append(f"- server: `{server_path}`")
    lines.append("")
    lines.append("## 件数比較（event別）")
    lines.append("| event | client | server |")
    lines.append("|---|---:|---:|")
    for k in all_keys:
        lines.append(f"| `{k}` | {c_count.get(k, 0)} | {s_count.get(k, 0)} |")
    lines.append("")

    lines.append("## クライアント先頭イベント")
    lines.append("| time | event | payload |")
    lines.append("|---|---|---|")
    for t, ev, payload in to_rows_for_preview(client_events):
        payload_escaped = payload.replace("|", "\\|")
        lines.append(f"| `{t}` | `{ev}` | {payload_escaped} |")
    lines.append("")

    lines.append("## サーバー先頭イベント")
    lines.append("| time | event | payload |")
    lines.append("|---|---|---|")
    for t, ev, payload in to_rows_for_preview(server_events):
        payload_escaped = payload.replace("|", "\\|")
        lines.append(f"| `{t}` | `{ev}` | {payload_escaped} |")
    lines.append("")

    lines.append("## 自動判定メモ")
    checks = []
    checks.append(
        ("final返却", c_count.get("final", 0) > 0 and (s_count.get("provider_success", 0) > 0 or s_count.get("final", 0) > 0))
    )
    checks.append(("speech_start発生", c_count.get("speech_start", 0) > 0 or s_count.get("speech_start", 0) > 0))
    checks.append(("draft発生", c_count.get("draft", 0) > 0 or s_count.get("draft", 0) > 0))
    for name, ok in checks:
        lines.append(f"- {name}: {'OK' if ok else 'NG'}")
    lines.append("")
    lines.append("## 補足")
    lines.append("- サーバー側で `draft` が0でも、クライアント表示ロジックで draft が見える場合があります。")
    lines.append("- `startup_config` の `stt_draft_enabled` と接続先WS URLを必ず併記して比較してください。")
    lines.append("")
    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(description="Compare client STT log and server STT log.")
    parser.add_argument("--client-log", required=True, help="Client log text path")
    parser.add_argument("--server-log", required=True, help="Server log text path")
    parser.add_argument(
        "--output",
        required=True,
        help="Output markdown report path",
    )
    args = parser.parse_args()

    client_path = Path(args.client_log)
    server_path = Path(args.server_log)
    output_path = Path(args.output)
    output_path.parent.mkdir(parents=True, exist_ok=True)

    client_events = parse_client_log(client_path)
    server_events = parse_server_log(server_path)
    report = render_markdown(client_events, server_events, client_path, server_path)
    output_path.write_text(report, encoding="utf-8")

    print(f"client_events={len(client_events)}")
    print(f"server_events={len(server_events)}")
    print(f"wrote={output_path}")


if __name__ == "__main__":
    main()
