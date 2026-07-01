#!/usr/bin/env python3
"""Run Webwright as an optional external browser-fetch tool.

This wrapper intentionally keeps Webwright outside the RenCrow runtime. It only
constructs a repeatable command and executes `python -m webwright.run.cli` when
Webwright is available in the selected Python environment.
"""

from __future__ import annotations

import argparse
import os
import shlex
import subprocess
import sys
from pathlib import Path


LOCAL_PROFILE_CONFIG = Path(__file__).with_name("config_local_worker.yaml")


def prepare_config_args(args: argparse.Namespace) -> None:
    if args.local_worker_profile or args.local_responses_endpoint:
        args.config.append(str(LOCAL_PROFILE_CONFIG))
    if not args.local_responses_endpoint:
        return
    override_path = args.output_dir / "_webwright_local_responses_override.yaml"
    override_path.write_text(
        "\n".join(
            [
                "model:",
                f"  model_name: {args.local_model}",
                f"  openai_endpoint: {args.local_responses_endpoint}",
                f"  openai_api_key: {args.local_api_key}",
                "",
            ]
        ),
        encoding="utf-8",
    )
    args.config.append(str(override_path))


def build_command(args: argparse.Namespace) -> list[str]:
    if args.uvx_from:
        cmd = [args.uvx_binary, "--from", args.uvx_from, args.python or "python", "-m", "webwright.run.cli"]
    else:
        python = args.python or os.environ.get("WEBWRIGHT_PYTHON") or sys.executable
        cmd = [python, "-m", "webwright.run.cli"]
    for config in args.config:
        cmd.extend(["-c", config])
    cmd.extend(["-t", args.task])
    if args.start_url:
        cmd.extend(["--start-url", args.start_url])
    if args.task_id:
        cmd.extend(["--task-id", args.task_id])
    cmd.extend(["-o", str(args.output_dir)])
    cmd.extend(args.webwright_arg)
    return cmd


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Run Webwright for a browser-backed data fetch task.")
    parser.add_argument("--task", required=True, help="Natural-language Webwright task.")
    parser.add_argument("--start-url", default="", help="Initial URL for Webwright.")
    parser.add_argument("--task-id", default="", help="Stable task id for output folders.")
    parser.add_argument("--output-dir", type=Path, default=Path("tmp/webwright_runs"), help="Webwright output root.")
    parser.add_argument(
        "--python",
        default="",
        help="Python executable that has Webwright installed. Defaults to WEBWRIGHT_PYTHON or current Python.",
    )
    parser.add_argument(
        "--uvx-from",
        default="",
        help="Run through uvx from this package spec, for example git+https://github.com/microsoft/Webwright.git.",
    )
    parser.add_argument("--uvx-binary", default="uvx", help="uvx executable path when --uvx-from is used.")
    parser.add_argument(
        "-c",
        "--config",
        action="append",
        default=None,
        help="Webwright config name. Repeatable. Defaults to base.yaml.",
    )
    parser.add_argument(
        "--local-worker-profile",
        action="store_true",
        help="Append tools/webwright_fetch/config_local_worker.yaml for RenCrow local Responses API.",
    )
    parser.add_argument(
        "--local-responses-endpoint",
        default="",
        help="Append local worker profile and override openai_endpoint, e.g. http://192.168.1.207:8082/v1/responses.",
    )
    parser.add_argument("--local-model", default="Coder1", help="Model used with --local-responses-endpoint.")
    parser.add_argument("--local-api-key", default="dummy", help="API key value used with --local-responses-endpoint.")
    parser.add_argument(
        "--webwright-arg",
        action="append",
        default=[],
        help="Extra argument appended to the Webwright CLI command. Repeat for multiple values.",
    )
    parser.add_argument("--dry-run", action="store_true", help="Print command without executing it.")
    args = parser.parse_args(argv)
    if args.config is None:
        args.config = ["base.yaml"]
    return args


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    args.output_dir.mkdir(parents=True, exist_ok=True)
    prepare_config_args(args)
    cmd = build_command(args)
    print(shlex.join(cmd), flush=True)
    if args.dry_run:
        return 0
    try:
        return subprocess.run(cmd, check=False).returncode
    except FileNotFoundError as exc:
        print(f"failed to launch Webwright command: {exc}", file=sys.stderr)
        return 127


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
