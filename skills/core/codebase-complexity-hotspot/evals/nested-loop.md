# Eval: Nested Loop

## Input

ユーザーが「O(n^2) っぽいところを探して」と依頼する。

## Expected Behavior

- `core.codebase-complexity-hotspot` が起動候補になる。
- nested loop / repeated lookup を探索する。
- コードは変更しない。
- Evidence、risk、required tests を含む report を返す。
