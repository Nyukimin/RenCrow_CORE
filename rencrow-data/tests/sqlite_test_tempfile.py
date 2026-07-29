from __future__ import annotations

import sqlite3
import tempfile
from pathlib import Path
from typing import Any


_original_connect = sqlite3.connect
_tracked_connections: list[sqlite3.Connection] = []


def _tracked_connect(*args: Any, **kwargs: Any) -> sqlite3.Connection:
    connection = _original_connect(*args, **kwargs)
    _tracked_connections.append(connection)
    return connection


if sqlite3.connect is not _tracked_connect:
    sqlite3.connect = _tracked_connect


class TemporaryDirectory(tempfile.TemporaryDirectory):
    """Close test-owned SQLite handles before Windows removes the temp tree."""

    def cleanup(self) -> None:
        root = Path(self.name).resolve()
        remaining: list[sqlite3.Connection] = []
        for connection in _tracked_connections:
            try:
                database_paths = [
                    Path(row[2]).resolve()
                    for row in connection.execute("PRAGMA database_list")
                    if row[2]
                ]
            except sqlite3.ProgrammingError:
                continue
            if any(path == root or root in path.parents for path in database_paths):
                connection.close()
            else:
                remaining.append(connection)
        _tracked_connections[:] = remaining
        super().cleanup()
