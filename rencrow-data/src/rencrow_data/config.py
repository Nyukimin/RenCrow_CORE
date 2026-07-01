from __future__ import annotations

import json
from pathlib import Path
from typing import Any

from .hashing import sha256_bytes


def load_config(path: str | Path, default: Any = None) -> Any:
    p = Path(path)
    if not p.exists():
        if default is not None:
            return default
        raise FileNotFoundError(p)
    text = p.read_text(encoding="utf-8").strip()
    if not text:
        return default if default is not None else {}
    return json.loads(text)


def config_path(config_root: str | Path, name: str) -> Path:
    root = resolve_repo_relative_path(config_root)
    return root / name


def resolve_repo_relative_path(value: str | Path) -> Path:
    path = Path(value)
    if path.parts[:1] == ("rencrow-data",) and Path.cwd().name == "rencrow-data":
        return Path(*path.parts[1:]) if len(path.parts) > 1 else Path(".")
    return path


def config_hash_for_paths(paths: list[str | Path]) -> str:
    chunks: list[bytes] = []
    for value in sorted((Path(path) for path in paths), key=lambda p: str(p)):
        chunks.append(str(value).encode("utf-8"))
        chunks.append(b"\0")
        if value.exists():
            chunks.append(value.read_bytes())
        chunks.append(b"\0")
    return sha256_bytes(b"".join(chunks))
