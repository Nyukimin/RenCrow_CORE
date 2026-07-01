---
name: web_search
tool_id: web_search
version: "1.0.0"
category: query
dry_run: false
invariants:
  - "query must be non-empty string (max 500 chars)"
  - "control characters are rejected"
  - "requires GOOGLE_API_KEY and GOOGLE_SEARCH_ENGINE_ID"
  - "returns max 5 results"
  - "timeout: 15 seconds"
---

# web_search

Search the web using Google Custom Search JSON API.

## Parameters

| Name  | Type   | Required | Description     |
|-------|--------|----------|-----------------|
| query | string | yes      | Search query    |

## Usage

```json
{
  "tool": "web_search",
  "args": {
    "query": "Go programming language"
  }
}
```

## Response

Formatted search results (max 5):
```
1. Title
   Snippet text
   https://example.com

2. Title
   Snippet text
   https://example.com
```

## Configuration

Requires environment variables:
- `GOOGLE_API_KEY_CHAT` / `GOOGLE_API_KEY_WORKER`
- `GOOGLE_SEARCH_ENGINE_ID_CHAT` / `GOOGLE_SEARCH_ENGINE_ID_WORKER`

## Safety

- **Validation**: Non-empty, max 500 chars, no control characters
- **Timeout**: 15 seconds
