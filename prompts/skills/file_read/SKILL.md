---
name: file_read
tool_id: file_read
version: "1.0.0"
category: query
dry_run: false
invariants:
  - "path must be non-empty"
  - "path traversal (../) is rejected"
  - "control characters in path are rejected"
  - "line limit via limit + offset parameters"
  - "timeout: 10 seconds"
---

# file_read

Read the contents of a file.

## Parameters

| Name   | Type   | Required | Description                          |
|--------|--------|----------|--------------------------------------|
| path   | string | yes      | Absolute file path                   |
| limit  | int    | no       | Max lines to return (default: all)   |
| offset | int    | no       | Lines to skip (default: 0)           |

## Usage

### Read entire file
```json
{
  "tool": "file_read",
  "args": {
    "path": "/home/user/document.txt"
  }
}
```

### Read specific line range
```json
{
  "tool": "file_read",
  "args": {
    "path": "/home/user/large.log",
    "limit": 50,
    "offset": 100
  }
}
```

## Response

Returns file contents as a string.
When using limit/offset, includes pagination info.

## Safety

- **Path validation**: Traversal (`../`) and control characters rejected
- **Line limit**: Optional limit + offset for large files
- **Timeout**: 10 seconds
