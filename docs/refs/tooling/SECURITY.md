# ツールセキュリティ仕様

**作成日**: 2026-03-05
**上位文書**: `/TOOL_CONTRACT.md` セクション2（安全レール）

---

## 1. 入力バリデーション

全ツールで以下のバリデーションを**必須**とする。

### 1.1 パストラバーサル

```
拒否パターン: ../ , ..\
検出方法: filepath.Clean() 後に元パスと比較
```

```go
func validatePath(path string) error {
    cleaned := filepath.Clean(path)
    if strings.Contains(path, "..") {
        return fmt.Errorf("path traversal detected: %s", path)
    }
    return nil
}
```

### 1.2 制御文字

```
拒否: \x00-\x08, \x0b-\x0c, \x0e-\x1f
許容: \x09 (TAB), \x0a (LF), \x0d (CR)
```

```go
func validateNoControlChars(s string) error {
    for i, r := range s {
        if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
            return fmt.Errorf("control character at position %d: 0x%02x", i, r)
        }
    }
    return nil
}
```

### 1.3 ID 汚染

```
拒否: ?, #, /, \, %25 (ダブルエンコード)
許容: 英数字, -, _, .
```

```go
func validateID(id string) error {
    for _, c := range id {
        if c == '?' || c == '#' || c == '/' || c == '\\' {
            return fmt.Errorf("invalid character in ID: %c", c)
        }
    }
    if strings.Contains(id, "%25") {
        return fmt.Errorf("double encoding detected in ID: %s", id)
    }
    return nil
}
```

### 1.4 長大入力

| フィールド種別 | 最大長 |
|--------------|--------|
| ID | 256 |
| タイトル / 名前 | 500 |
| 説明文 | 5,000 |
| 本文 / コンテンツ | 50,000 |
| URL | 2,048 |
| 配列要素数 | 1,000 |

---

## 2. 破壊的操作の安全装置

### 2.1 dry-run の実装要件

書き込み系ツールは `mode=plan` で以下を返す:

```json
{
  "mode": "plan",
  "actions": [
    {
      "type": "create|update|delete",
      "target": "entity:<category>:<id>",
      "field": "<field_name>",
      "from": "<old_value>",
      "to": "<new_value>",
      "reason": "<why>"
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

- `mode=plan` では副作用（DB書き込み、ファイル変更、API呼び出し）を**一切**実行しない
- 対象件数が 0 の場合も正常応答（空の actions 配列）を返す

## 3. 取得系の制約

### 3.1 フィールド制限

```json
{
  "fields": ["id", "title", "genres"]
}
```

- `fields` が指定されない場合は全フィールドを返す
- 存在しないフィールド名はエラーではなく無視する

### 3.2 ページング

```json
{
  "limit": 100,
  "offset": 0
}
```

または cursor ベース:

```json
{
  "limit": 100,
  "cursor": "abc123"
}
```

レスポンスに含めるメタ:

```json
{
  "result": [...],
  "pagination": {
    "total": 1500,
    "limit": 100,
    "offset": 0,
    "has_next": true,
    "next_cursor": "def456"
  }
}
```

- デフォルト limit: 100
- 最大 limit: 1000
- limit を超える値はエラーではなく 1000 に切り詰める

---

## 4. 外部 API 呼び出し

### 4.1 タイムアウト

| 対象 | デフォルト | 最大 |
|------|----------|------|
| HTTP リクエスト | 30秒 | 120秒 |
| LLM 呼び出し | 60秒 | 300秒 |
| ファイル操作 | 10秒 | 30秒 |

### 4.2 リトライ

| 設定 | デフォルト |
|------|----------|
| 最大回数 | 3 |
| 方式 | 指数バックオフ (1s, 2s, 4s) |
| リトライ対象 | 5xx, タイムアウト, 接続エラー |
| リトライ除外 | 4xx（バリデーションエラー） |

### 4.3 レート制限

- レート制限レスポンス（429）を受けた場合は `Retry-After` ヘッダに従う
- ヘッダがない場合は 60秒待機
- レート制限の発生はログに記録する

---

**最終更新**: 2026-03-05
