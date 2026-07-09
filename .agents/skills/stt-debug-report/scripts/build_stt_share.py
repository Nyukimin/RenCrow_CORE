#!/usr/bin/env python3
"""
STT デバッグ解析 → tmp/stt_share_for_server.md 生成スクリプト

使い方:
  python3 scripts/build_stt_share.py [--tmp-dir tmp] [--output tmp/stt_share_for_server.md]
"""
import argparse
import hashlib
import json
import re
from datetime import datetime
from pathlib import Path


def sha256(path: Path) -> str:
    h = hashlib.sha256()
    h.update(path.read_bytes())
    return h.hexdigest()


def human_size(path: Path) -> str:
    b = path.stat().st_size
    for unit in ("B", "KB", "MB"):
        if b < 1024:
            return f"{b}{unit}"
        b //= 1024
    return f"{b}GB"


def parse_client_log(path: Path) -> dict:
    """client_stt_log.txt をパースして主要フィールドを返す"""
    text = path.read_text(encoding="utf-8", errors="replace")
    result = {
        "client_url": "",
        "ws_url": "",
        "test_time": "",
        "session_id": "(unknown)",
        "spoken_text": "",
        "events": [],
        "raw": text.strip(),
    }
    for line in text.splitlines():
        for key in ("client_url", "ws_url", "test_time", "session_id", "spoken_text"):
            if line.startswith(f"{key}:"):
                result[key] = line.split(":", 1)[1].strip()

    # イベント行 (HH:MM:SS · type)
    ev_re = re.compile(r"^(\d{2}:\d{2}:\d{2})\s+·\s+(\S+)$")
    lines = text.splitlines()
    for i, line in enumerate(lines):
        m = ev_re.match(line.strip())
        if m:
            payload = ""
            if i + 1 < len(lines):
                nxt = lines[i + 1].strip()
                if not ev_re.match(nxt):
                    payload = nxt
            result["events"].append({"time": m.group(1), "type": m.group(2), "payload": payload})
    return result


def parse_e2e_json(path: Path) -> dict:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except Exception:
        return {}


def parse_compare_report(path: Path) -> str:
    """比較レポートのサマリ行だけ抽出"""
    lines = path.read_text(encoding="utf-8", errors="replace").splitlines()
    # 件数比較テーブルと自動判定メモだけ返す
    capture = False
    out = []
    for line in lines:
        if line.startswith("## 件数比較") or line.startswith("## 自動判定"):
            capture = True
        elif line.startswith("## ") and capture:
            if line.startswith("## サーバー先頭"):
                capture = False
                continue
        if capture:
            out.append(line)
    return "\n".join(out).strip()


def collect_files(tmp_dir: Path):
    """tmp/ 配下の STT 関連ファイルを収集"""
    main_files = []
    for name in [
        "client_stt_log.txt",
        "client_stt_input_latest.wav",
        "stt_e2e_from_mic_latest.json",
        "stt_server_analysis_latest.md",
        "stt_server_analysis_latest.json",
    ]:
        p = tmp_dir / name
        if p.exists():
            main_files.append(p)

    # compare report (最新1件)
    compare_reports = sorted(tmp_dir.glob("stt_compare_report_*.md"), reverse=True)
    if compare_reports:
        main_files.append(compare_reports[0])

    # server log (最新1件)
    server_logs = sorted(tmp_dir.glob("voice_bridge_*.log"), reverse=True)
    if server_logs:
        main_files.append(server_logs[0])

    # アーカイブ WAV
    archive_wavs = sorted((tmp_dir / "stt_inputs").glob("*.wav")) if (tmp_dir / "stt_inputs").exists() else []

    return main_files, archive_wavs, compare_reports, server_logs


def render_event_table(events: list, limit=15) -> str:
    rows = ["| time | event | payload |", "|---|---|---|"]
    for e in events[:limit]:
        p = e["payload"].replace("|", "\\|")
        rows.append(f"| `{e['time']}` | `{e['type']}` | {p} |")
    return "\n".join(rows)


def summarize_events(events: list) -> dict:
    from collections import Counter
    return dict(Counter(e["type"] for e in events))


def main():
    parser = argparse.ArgumentParser(description="STT デバッグ解析 → stt_share_for_server.md 生成")
    parser.add_argument("--tmp-dir", default="tmp", help="tmp ディレクトリのパス (default: tmp)")
    parser.add_argument("--output", default="tmp/stt_share_for_server.md", help="出力ファイルパス")
    args = parser.parse_args()

    tmp_dir = Path(args.tmp_dir)
    output_path = Path(args.output)
    now = datetime.now().strftime("%Y-%m-%d %H:%M:%S")

    main_files, archive_wavs, compare_reports, server_logs = collect_files(tmp_dir)

    # --- クライアントログ解析 ---
    client_log_path = tmp_dir / "client_stt_log.txt"
    client_info = parse_client_log(client_log_path) if client_log_path.exists() else {}

    # --- E2E JSON 解析 ---
    e2e_path = tmp_dir / "stt_e2e_from_mic_latest.json"
    e2e = parse_e2e_json(e2e_path) if e2e_path.exists() else {}

    # --- 比較レポート解析 ---
    compare_summary = ""
    if compare_reports:
        compare_summary = parse_compare_report(compare_reports[0])

    # --- サーバー解析 MD ---
    server_analysis_md = tmp_dir / "stt_server_analysis_latest.md"
    server_analysis_text = server_analysis_md.read_text(encoding="utf-8").strip() if server_analysis_md.exists() else ""

    # ===================== レポート生成 =====================
    lines = []
    lines.append("# STT 共有データ一覧（サーバー担当向け）")
    lines.append("")
    lines.append(f"作成日時: {now}")
    lines.append("")

    # --- 観測サマリ ---
    lines.append("## 観測サマリ")
    lines.append("")

    # E2E
    inf_ok = e2e.get("inference_success", "?")
    ws_ok = e2e.get("ws_success", "?")
    inf_ms = ""
    if e2e.get("inference"):
        inf_ms = f" ({e2e['inference'][0].get('ms', '?')}ms)"
    lines.append(f"- STT 推論（HTTP直接）: **{inf_ok}**{inf_ms}")
    lines.append(f"- `/stt-ws` WebSocket 経路: **{ws_ok}**")

    # session_id
    sid = client_info.get("session_id", "(unknown)")
    sid_status = "**`(unknown)` のまま**（サーバーから `session_info` 未受信）" if sid == "(unknown)" else f"`{sid}`"
    lines.append(f"- `session_id`: {sid_status}")

    # サーバーイベント数
    server_events_zero = "server_events=0" in (server_analysis_text + compare_summary)
    if server_events_zero:
        lines.append("- サーバーログ突合: **不可**（サーバーログに `[stt]` イベント行なし、`server_events=0`）")
    else:
        lines.append("- サーバーログ突合: 確認中")

    lines.append("")
    lines.append("---")
    lines.append("")

    # --- ファイル一覧 ---
    lines.append("## 共有ファイル一覧")
    lines.append("")
    lines.append("### メインログ")
    lines.append("")
    lines.append("| ファイル | 説明 | サイズ | sha256 |")
    lines.append("|---|---|---|---|")

    desc_map = {
        "client_stt_log.txt": "クライアントSTTログ",
        "client_stt_input_latest.wav": "最新マイク入力WAV（16kHz mono）",
        "stt_e2e_from_mic_latest.json": "E2Eプローブ結果",
        "stt_server_analysis_latest.md": "サーバーログ解析結果（MD）",
        "stt_server_analysis_latest.json": "サーバーログ解析結果（JSON）",
    }

    for f in main_files:
        if f.suffix == ".wav" and f.name == "client_stt_input_latest.wav":
            desc = desc_map.get(f.name, f.name)
        elif f.name.startswith("stt_compare_report_"):
            desc = "client/server 比較レポート"
        elif f.name.startswith("voice_bridge_"):
            desc = "サーバーログ切り出し"
        else:
            desc = desc_map.get(f.name, f.name)
        lines.append(f"| `{f.name}` | {desc} | {human_size(f)} | `{sha256(f)}` |")

    if archive_wavs:
        lines.append("")
        lines.append("### アーカイブ WAV（`tmp/stt_inputs/`）")
        lines.append("")
        lines.append("| ファイル | サイズ | sha256 |")
        lines.append("|---|---|---|")
        for f in archive_wavs:
            lines.append(f"| `{f.name}` | {human_size(f)} | `{sha256(f)}` |")
        # latest と同一ハッシュの確認
        if client_log_path.exists() and (tmp_dir / "client_stt_input_latest.wav").exists():
            latest_hash = sha256(tmp_dir / "client_stt_input_latest.wav")
            for f in archive_wavs:
                if sha256(f) == latest_hash:
                    lines.append("")
                    lines.append(f"※ `client_stt_input_latest.wav` は `{f.name}` と同一ファイル（ハッシュ一致）")
                    break

    lines.append("")
    lines.append("---")
    lines.append("")

    # --- クライアントログ内容 ---
    if client_info.get("raw"):
        lines.append("## 最新クライアントログ内容（`client_stt_log.txt`）")
        lines.append("")
        lines.append("```")
        lines.append(client_info["raw"])
        lines.append("```")
        lines.append("")
        lines.append("---")
        lines.append("")

    # --- E2E 結果 ---
    if e2e:
        lines.append("## E2Eプローブ結果（`stt_e2e_from_mic_latest.json`）")
        lines.append("")
        lines.append("```json")
        summary_e2e = {
            "provider_url": e2e.get("provider_url", ""),
            "ws_url": e2e.get("ws_url", ""),
            "inference_success": e2e.get("inference_success", ""),
            "ws_success": e2e.get("ws_success", ""),
            "timestamp": e2e.get("timestamp", ""),
        }
        lines.append(json.dumps(summary_e2e, ensure_ascii=False, indent=2))
        lines.append("```")
        lines.append("")
        if e2e.get("inference"):
            first = e2e["inference"][0]
            lines.append(f"- 推論レスポンス: `\"{first.get('text', '')}\"` ({first.get('ms', '?')}ms)")
        if e2e.get("ws") and e2e["ws"][0].get("events"):
            evs = " → ".join(e2e["ws"][0]["events"])
            lines.append(f"- WS受信イベント: `{evs}`")
        lines.append("")
        lines.append("---")
        lines.append("")

    # --- 比較結果 ---
    if compare_summary:
        lines.append(f"## 比較結果サマリ（`{compare_reports[0].name}`）")
        lines.append("")
        lines.append(compare_summary)
        lines.append("")
        lines.append("---")
        lines.append("")

    # --- サーバー解析 ---
    if server_analysis_text:
        lines.append("## サーバーログ解析（`stt_server_analysis_latest.md`）")
        lines.append("")
        lines.append(server_analysis_text)
        lines.append("")
        lines.append("---")
        lines.append("")

    # --- 比較コマンド ---
    lines.append("## 比較コマンド（再実行用）")
    lines.append("")
    log_arg = f"tmp/{server_logs[0].name}" if server_logs else "tmp/voice_bridge_YYYYMMDD_HHMMSS_HHMMSS.log"
    lines.append("```bash")
    lines.append("python3 docs/STT_TTS/tools/compare_stt_logs.py \\")
    lines.append('  --client-log "tmp/client_stt_log.txt" \\')
    lines.append(f'  --server-log "{log_arg}" \\')
    lines.append('  --output "tmp/stt_compare_report_latest.md"')
    lines.append("```")
    lines.append("")
    lines.append("---")
    lines.append("")

    # --- 依頼文リンク ---
    lines.append("## 依頼文リンク")
    lines.append("")
    lines.append("- サーバーログ出力依頼（パス非依存版）: `docs/STT_TTS/AUDIO_Client仕様/STT/stt_server_logging_request_path_agnostic_2026-04-13.md`")
    lines.append("- session_id 問題 証拠付き問い合わせ: `docs/STT_TTS/AUDIO_Client仕様/STT/stt_server_inquiry_with_proof_2026-04-13.md`")
    lines.append("")

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text("\n".join(lines), encoding="utf-8")
    print(f"[OK] 生成完了: {output_path}")
    print(f"  main_files: {len(main_files)}")
    print(f"  archive_wavs: {len(archive_wavs)}")
    print(f"  compare_reports: {len(compare_reports)}")
    print(f"  server_logs: {len(server_logs)}")


if __name__ == "__main__":
    main()
