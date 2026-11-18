#!/usr/bin/env python3
"""Hash the TaskCenter live-E2E build and static-check source closure."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
import subprocess
import sys


GO_TARGETS = (
    "./cmd/rencrow",
    "./test/e2e/browser/seed_task_center_e2e",
    "./internal/adapter/viewer",
    "./internal/application/moviecatalog",
    "./internal/application/taskcenter",
    "./internal/application/taskmanager",
    "./internal/application/taskmeeting",
    "./internal/domain/task",
    "./internal/domain/taskexecution",
    "./internal/domain/taskmeeting",
    "./internal/domain/workstream",
    "./internal/infrastructure/persistence/task",
    "./internal/infrastructure/persistence/workstream",
    "./internal/infrastructure/tools",
    "./modules/core",
)

GO_SOURCE_FIELDS = (
    "GoFiles",
    "CgoFiles",
    "CFiles",
    "CXXFiles",
    "MFiles",
    "HFiles",
    "FFiles",
    "SFiles",
    "SwigFiles",
    "SwigCXXFiles",
    "SysoFiles",
    "EmbedFiles",
    "TestGoFiles",
    "XTestGoFiles",
    "TestEmbedFiles",
    "XTestEmbedFiles",
)

GATE_FILES = (
    "go.mod",
    "go.sum",
    "package.json",
    "package-lock.json",
    "test/e2e/browser/task_center_source_manifest.py",
    "test/e2e/browser/run_task_center_e2e.sh",
    "test/e2e/browser/task_center_live_e2e.mjs",
    "internal/adapter/viewer/task_center_browser.test.mjs",
)


def parse_json_stream(payload: str) -> list[dict[str, object]]:
    decoder = json.JSONDecoder()
    records: list[dict[str, object]] = []
    offset = 0
    while offset < len(payload):
        while offset < len(payload) and payload[offset].isspace():
            offset += 1
        if offset == len(payload):
            break
        record, offset = decoder.raw_decode(payload, offset)
        if not isinstance(record, dict):
            raise ValueError("go list returned a non-object package record")
        records.append(record)
    if not records:
        raise ValueError("go list returned no package records")
    return records


def path_within(path: Path, root: Path) -> bool:
    try:
        path.relative_to(root)
        return True
    except ValueError:
        return False


def main() -> int:
    root = Path(__file__).resolve().parents[3]
    go_environment = subprocess.run(
        ["go", "env", "GOMOD", "GOOS", "GOARCH", "CGO_ENABLED", "GOVERSION"],
        cwd=root,
        check=True,
        capture_output=True,
        text=True,
    )
    if go_environment.stderr.strip():
        raise ValueError("go env emitted diagnostics: " + go_environment.stderr.strip())
    go_module = go_environment.stdout.splitlines()
    if len(go_module) != 5 or Path(go_module[0]).resolve() != root / "go.mod":
        raise ValueError("go env did not identify the expected RenCrow_CORE module")

    command = ["go", "list", "-deps", "-test", "-json", *GO_TARGETS]
    listed = subprocess.run(command, cwd=root, check=True, capture_output=True, text=True)
    if listed.stderr.strip():
        raise ValueError("go list emitted diagnostics: " + listed.stderr.strip())
    records = parse_json_stream(listed.stdout)
    files: set[Path] = set()
    package_count = 0
    for record in records:
        if record.get("Error") or record.get("DepsErrors"):
            raise ValueError("go list returned an incomplete package record: " + str(record.get("ImportPath", "unknown")))
        module = record.get("Module")
        if not isinstance(module, dict) or module.get("Main") is not True:
            continue
        module_dir = Path(str(module.get("Dir", ""))).resolve()
        if module_dir != root:
            continue
        directory = Path(str(record.get("Dir", ""))).resolve()
        if not path_within(directory, root):
            continue
        package_count += 1
        for field in GO_SOURCE_FIELDS:
            values = record.get(field, [])
            if not isinstance(values, list):
                continue
            for value in values:
                if not isinstance(value, str) or not value:
                    continue
                candidate = (directory / value).resolve()
                if not path_within(candidate, root):
                    # `-test` can report generated test-main sources from a
                    # build cache. Only repository-owned source is manifested.
                    continue
                if not candidate.is_file():
                    raise FileNotFoundError(f"go list source is missing: {candidate}")
                files.add(candidate)
    if package_count == 0:
        raise ValueError("go list did not report any source packages from the main module")

    for relative in GATE_FILES:
        candidate = (root / relative).resolve()
        if not path_within(candidate, root) or not candidate.is_file():
            raise FileNotFoundError(f"required TaskCenter E2E gate file is missing: {relative}")
        files.add(candidate)

    print("# RenCrow_CORE TaskCenter E2E source manifest v2")
    print("# command: " + " ".join(command))
    print("# context: " + " ".join(go_module[1:]))
    for source in sorted(files, key=lambda item: item.relative_to(root).as_posix()):
        digest = hashlib.sha256(source.read_bytes()).hexdigest()
        print(f"{digest}  {source.relative_to(root).as_posix()}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.CalledProcessError, ValueError, json.JSONDecodeError) as error:
        print(f"task-center source manifest: {error}", file=sys.stderr)
        raise SystemExit(2)
