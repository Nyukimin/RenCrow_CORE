---
name: file_write
tool_id: file_write
version: "1.0.0"
category: mutation
dry_run: true
invariants:
  - "path must be non-empty"
  - "path traversal (../) is rejected"
  - "control characters in path are rejected"
  - "content must be provided"
  - "directories are created automatically"
  - "mode=plan returns preview without writing"
  - "timeout: 10 seconds"
---

# file_write

Write content to a file. Supports dry-run mode.

## Parameters

| Name    | Type   | Required | Description                        |
|---------|--------|----------|------------------------------------|
| path    | string | yes      | Absolute file path                 |
| content | string | yes      | Content to write                   |
| mode    | string | no       | `"plan"` for dry-run (no write)    |

## Usage

### Normal write
```json
{
  "tool": "file_write",
  "args": {
    "path": "/home/user/output.txt",
    "content": "Hello, World!"
  }
}
```

### Dry-run (preview only)
```json
{
  "tool": "file_write",
  "args": {
    "path": "/home/user/output.txt",
    "content": "Hello, World!",
    "mode": "plan"
  }
}
```

## Response

- **Normal**: `"Successfully wrote N bytes to /path"`
- **Dry-run**: Preview showing exists/create, content size, first 5 lines

## Safety

- **Dry-run**: `mode=plan` previews without writing
- **Path validation**: Traversal (`../`) and control characters rejected
- **Auto-mkdir**: Parent directories created automatically
- **Timeout**: 10 seconds
