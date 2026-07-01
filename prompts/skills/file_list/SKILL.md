---
name: file_list
tool_id: file_list
version: "1.0.0"
category: query
dry_run: false
invariants:
  - "path must be non-empty"
  - "path traversal (../) is rejected"
  - "control characters in path are rejected"
  - "default limit: 100, max limit: 1000"
  - "pagination via limit + offset"
  - "timeout: 10 seconds"
---

# file_list

List files and directories in a given path.

## Parameters

| Name   | Type   | Required | Description                       |
|--------|--------|----------|-----------------------------------|
| path   | string | yes      | Directory path to list            |
| limit  | int    | no       | Max entries to return (default 100, max 1000) |
| offset | int    | no       | Number of entries to skip (default 0) |

## Usage

```json
{
  "tool": "file_list",
  "args": {
    "path": "/home/user/projects",
    "limit": 20,
    "offset": 0
  }
}
```

## Response

Returns one entry per line. Directories have a trailing `/`.
Includes pagination info when more entries exist.

```
file1.txt
file2.go
subdir/
--- showing 1-20 of 150 (next offset: 20) ---
```

## Safety

- **Path validation**: Traversal (`../`) and control characters rejected
- **Pagination**: Default limit 100, max 1000
- **Timeout**: 10 seconds
