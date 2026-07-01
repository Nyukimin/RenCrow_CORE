---
name: subagent
tool_id: subagent
version: "1.0.0"
category: query
dry_run: false
invariants:
  - "agent name must be non-empty"
  - "message must be non-empty"
  - "agent must be registered in config"
  - "timeout: 30 seconds"
---

# subagent

Invoke a registered sub-agent by name.

## Parameters

| Name    | Type   | Required | Description                    |
|---------|--------|----------|--------------------------------|
| agent   | string | yes      | Name of the registered agent   |
| message | string | yes      | Message to send to the agent   |

## Usage

```json
{
  "tool": "subagent",
  "args": {
    "agent": "researcher",
    "message": "Find the latest Go release notes"
  }
}
```

## Response

Returns the sub-agent's response as a string.

## Safety

- **Agent validation**: Only registered agents can be invoked
- **Timeout**: 30 seconds
- **Availability**: Only registered when subagents are configured
