---
name: shell
tool_id: shell
version: "1.0.0"
category: mutation
dry_run: true
invariants:
  - "command must be non-empty string (max 10000 chars)"
  - "control characters are rejected"
  - "shell metacharacters for chaining are rejected: ; && || | ` $( newline"
  - "allowed_commands config restricts executable commands"
  - "mode=plan returns preview without executing"
  - "timeout: 30 seconds"
---

# shell

Execute a shell command via `sh -c`.

## Parameters

| Name    | Type   | Required | Description                     |
|---------|--------|----------|---------------------------------|
| command | string | yes      | Shell command to run            |
| mode    | string | no       | `"plan"` for dry-run (no exec)  |

## Usage

```json
{
  "tool": "shell",
  "args": {
    "command": "ls -la /tmp"
  }
}
```

### Dry-run
```json
{
  "tool": "shell",
  "args": {
    "command": "rm -rf /tmp/old",
    "mode": "plan"
  }
}
```

## Response

Returns combined stdout+stderr as a string.

## Safety

- **Dry-run**: `mode=plan` shows command without executing
- **Command restriction**: `AllowedShellCommands` config limits executable commands
- **Timeout**: 30 seconds
- **Validation**: Non-empty, max 10000 chars, no control characters
- **Metacharacter ban**: `;` `&&` `||` `|` `` ` `` `$(` and newline are always rejected — use separate `shell` calls instead of chaining
