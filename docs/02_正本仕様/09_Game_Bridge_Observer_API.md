# Game Bridge Observer API 仕様 v0.1

## 1. 目的

この仕様は、RenCrow_GAMES の browser Observer UI と RenCrow Viewer / Ops
を接続する read-only 観察 API を定義する。

正本の game observer 仕様は
`/home/nyukimi/RenCrow/RenCrow_GAMES/docs/08_Observer_UI仕様.md` に置く。
この文書は、`RenCrow_CORE` 側が持つ範囲だけを定義する。

## 2. 責務境界

RenCrow が持つもの:

- game bridge status
- recent game session summary
- candidate event log の read-only 表示
- Viewer / Ops の game bridge status card
- replay turn と candidate event id の相関補助

RenCrow が持たないもの:

- title world state の正本
- title-specific board rendering
- action physics
- replay file の正本
- browser からの game rule 編集
- confirmed memory promotion

## 3. Data Source

Primary source:

```text
<workspace_dir>/logs/game_bridge_events.jsonl
```

この JSONL は candidate/event log であり、confirmed memory ではない。
Viewer は candidate を confirmed と表示してはいけない。

Optional runtime source:

- process-local active session registry
- current bridge mode
- `/viewer/games/status`

process restart 後は active registry が消えてよい。
candidate log から recent session summary を復元できればよい。

## 4. API Surface

Existing:

```text
GET /viewer/games/status
POST /viewer/games/decision
POST /viewer/games/result
```

New read-only observer API:

```text
GET /viewer/games/sessions?limit=20
GET /viewer/games/events?game_id=<id>&session_id=<id>&limit=50
```

Live RenCrow_GAMES observer passthrough:

```text
GET /viewer/games/observer
GET /viewer/games/observer-api/games/status
GET /viewer/games/observer-api/games/sessions
GET /viewer/games/observer-api/games/sessions/{session_id}/frames
GET /viewer/games/observer-api/games/events
```

`/viewer/games/observer` は browser Observer UI を RenCrow Viewer と同じ
origin から配信するための薄い proxy page である。title world state の正本、
描画ロジック、replay 仕様は RenCrow_GAMES 側に残す。
`/viewer/games/observer-api/*` は local RenCrow_GAMES observer
server への read-only proxy であり、ブラウザが `18790` 以外の port を直接
開けなくても live observer を読めるようにする。

v0.1 では write API を追加しない。

## 5. Session Summary Response

`GET /viewer/games/sessions`

```json
{
  "ok": true,
  "source": "candidate_log",
  "sessions": [
    {
      "game_id": "survival_garden",
      "session_id": "sg_20260702_001",
      "persona": "mio",
      "status": "recent",
      "latest_turn": 12,
      "latest_event_id": "game:survival_garden:sg_20260702_001:turn_12",
      "candidate_count": 13,
      "updated_at": "2026-07-02T00:03:00Z",
      "decision_mode": "llm",
      "result_mode": "persisted_candidate",
      "memory_mode": "candidate_only"
    }
  ]
}
```

Rules:

- `limit` default is 20, max is 100.
- sessions are ordered by latest event time descending.
- `status=recent` means reconstructed from candidate log.
- `status=running` may be used only if a runtime active registry exists.
- missing candidate log returns explicit unavailable status, not fabricated data.

## 6. Event List Response

`GET /viewer/games/events`

```json
{
  "ok": true,
  "source": "candidate_log",
  "events": [
    {
      "event_id": "game:survival_garden:sg_20260702_001:turn_12",
      "candidate_memory_id": "game:survival_garden:sg_20260702_001:turn_12:candidate",
      "game_id": "survival_garden",
      "session_id": "sg_20260702_001",
      "turn": 12,
      "persona": "mio",
      "decision_intent": "return_to_camp",
      "memory_refs": [
        "game:survival_garden:sg_20260702_001:turn_11:candidate"
      ],
      "executed_actions": ["return_to_camp"],
      "result_events": ["returned_to_camp"],
      "memory_state": "candidate",
      "promoted": false,
      "created_at": "2026-07-02T00:03:00Z"
    }
  ]
}
```

Rules:

- `game_id` and `session_id` are optional filters.
- `limit` default is 50, max is 500.
- response should avoid returning full large `result` payload by default.
- a future `include_raw=true` may expose raw event JSON for debug only.

## 7. Viewer / Ops Card

RenCrow Viewer may add a Game Bridge card.

Required display:

- bridge ok / unavailable
- decision mode
- result mode
- memory mode
- recent sessions count
- latest game id / session id / turn
- latest event id
- candidate-only warning

The card must not render title boards.
Title boards belong to RenCrow_GAMES Observer UI.

## 8. Failure Behavior

| Failure | API behavior | Viewer behavior |
| --- | --- | --- |
| candidate log missing | `503` or `ok=false` with `source_unavailable` | show unavailable, no stale success |
| malformed log line | skip line and report `skipped_count` | show warning count |
| no events | `ok=true`, empty list | show empty state |
| unsupported method | `405` | no retry loop |
| invalid limit | `400` | show request error |

## 9. Tests

Required:

- session summary from temp candidate log
- event list filter by `game_id + session_id`
- malformed lines are skipped and counted
- missing log is explicit unavailable
- Viewer card does not mark candidate memory as confirmed

Live smoke:

```bash
curl -s http://127.0.0.1:18790/viewer/games/status
curl -s 'http://127.0.0.1:18790/viewer/games/sessions?limit=5'
curl -s 'http://127.0.0.1:18790/viewer/games/events?limit=5'
```

## 10. Non-goals

v0.1 does not implement:

- confirmed memory promotion
- game world reconstruction in RenCrow
- title-specific board rendering
- game control from RenCrow Viewer
- direct LLM calls from browser UI
