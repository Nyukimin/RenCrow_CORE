#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 /path/to/config.yaml" >&2
  exit 2
fi

config_path="$1"
if [ ! -f "$config_path" ]; then
  echo "config file not found: $config_path" >&2
  exit 1
fi

backup_path="${config_path}.bak"
if [ -e "$backup_path" ]; then
  echo "backup already exists: $backup_path" >&2
  exit 1
fi

cp "$config_path" "$backup_path"

python3 - "$config_path" <<'PY'
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
lines = path.read_text(encoding="utf-8").splitlines(keepends=True)
out = []
i = 0

while i < len(lines):
    line = lines[i]
    out.append(line)
    if re.match(r"^ollama:\s*(?:#.*)?$", line):
        block = []
        i += 1
        while i < len(lines):
            current = lines[i]
            if current.strip() and not current.startswith((" ", "\t", "#")):
                break
            block.append(current)
            i += 1

        has_model = any(re.match(r"^\s+model\s*:", item) for item in block)
        chat_line = next((item for item in block if re.match(r"^\s+chat_model\s*:", item)), None)
        if not has_model and chat_line is not None:
            value = chat_line.split(":", 1)[1]
            indent = re.match(r"^(\s*)", chat_line).group(1)
            out.append(f"{indent}model:{value}")

        for item in block:
            if re.match(r"^\s+(chat_model|worker_model)\s*:", item):
                continue
            out.append(item)
        continue
    i += 1

path.write_text("".join(out), encoding="utf-8")
PY

echo "migrated: $config_path"
echo "backup: $backup_path"
