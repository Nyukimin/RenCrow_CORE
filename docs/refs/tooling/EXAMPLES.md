# ツール入出力サンプル集

**作成日**: 2026-03-05
**上位文書**: `/TOOL_CONTRACT.md`

---

## 1. 標準パターン

### 1.1 取得系（正常）

**入力**:
```json
{
  "mode": "execute",
  "fields": ["id", "title", "genres"],
  "limit": 10,
  "offset": 0
}
```

**出力** (stdout):
```json
{
  "result": [
    {"id": "movie:123", "title": "Spirited Away", "genres": ["animation", "fantasy"]},
    {"id": "movie:456", "title": "Your Name", "genres": ["animation", "romance"]}
  ],
  "pagination": {
    "total": 150,
    "limit": 10,
    "offset": 0,
    "has_next": true
  },
  "generated_at": "2026-03-05T14:30:00Z"
}
```

### 1.2 書き込み系（dry-run）

**入力**:
```json
{
  "mode": "plan",
  "entity_id": "movie:123",
  "updates": {
    "title": "Spirited Away (2001)"
  }
}
```

**出力** (stdout):
```json
{
  "mode": "plan",
  "actions": [
    {
      "type": "update",
      "target": "entity:movie:123",
      "field": "title",
      "from": "Spirited Away",
      "to": "Spirited Away (2001)",
      "reason": "user requested title update"
    }
  ],
  "summary": {
    "creates": 0,
    "updates": 1,
    "deletes": 0,
    "total_affected": 1
  }
}
```

### 1.3 書き込み系（本実行）

**入力**:
```json
{
  "mode": "execute",
  "entity_id": "movie:123",
  "updates": {
    "title": "Spirited Away (2001)"
  }
}
```

**出力** (stdout):
```json
{
  "result": {
    "entity_id": "movie:123",
    "updated_fields": ["title"],
    "previous_values": {
      "title": "Spirited Away"
    }
  },
  "generated_at": "2026-03-05T14:31:00Z"
}
```

---

## 2. エラーパターン

### 2.1 バリデーションエラー

**入力**:
```json
{
  "entity_id": "../../../etc/passwd"
}
```

**出力** (stdout):
```json
{
  "error": {
    "code": "VALIDATION_FAILED",
    "message": "path traversal detected in entity_id",
    "details": {
      "field": "entity_id",
      "value": "../../../etc/passwd",
      "constraint": "no_path_traversal"
    }
  }
}
```

### 2.2 リソース不存在

**入力**:
```json
{
  "entity_id": "movie:999999"
}
```

**出力** (stdout):
```json
{
  "error": {
    "code": "NOT_FOUND",
    "message": "entity movie:999999 does not exist",
    "details": {
      "entity_id": "movie:999999"
    }
  }
}
```

### 2.3 タイムアウト

**出力** (stdout):
```json
{
  "error": {
    "code": "TIMEOUT",
    "message": "request timed out after 30000ms",
    "details": {
      "timeout_ms": 30000,
      "target": "https://api.example.com/v1/movies"
    }
  }
}
```

### 2.4 レート制限

**出力** (stdout):
```json
{
  "error": {
    "code": "RATE_LIMITED",
    "message": "rate limit exceeded, retry after 60s",
    "details": {
      "retry_after_seconds": 60,
      "source": "tmdb_api"
    }
  }
}
```

---

## 3. ログ出力パターン（stderr）

ツールのログは常に stderr に出力する。stdout には混ぜない。

```
2026-03-05T14:30:00Z [INFO] tool=tmdb_fetcher action=fetch entity_count=10
2026-03-05T14:30:01Z [WARN] tool=tmdb_fetcher action=fetch rate_limit_remaining=5
2026-03-05T14:30:02Z [ERROR] tool=tmdb_fetcher action=fetch error="connection refused"
```

---

**最終更新**: 2026-03-05
