# ツール雛形

新しいツールを作るときは、このディレクトリのファイルをコピーして開始する。

## ファイル一覧

| ファイル | 用途 |
|---------|------|
| `SKILL.md.tmpl` | SKILL.md の雛形（frontmatter + 入出力サンプル + 不変条件） |
| `main.go.tmpl` | Go 実装の雛形（バリデーション + dry-run + エラーJSON） |
| `test_sample.json` | テストケースの雛形（正常 + dry-run + エラー） |

## 使い方

1. このディレクトリのファイルをコピー
2. `{{TOOL_ID}}` 等のプレースホルダを置換
3. 実装
4. TOOL_CONTRACT.md の DoD チェックリストを通す
5. Worker に受領依頼

## 参照

- `/TOOL_CONTRACT.md` -- 根本ルール
- `/docs/tooling/SECURITY.md` -- セキュリティ詳細
- `/docs/tooling/EXAMPLES.md` -- 入出力サンプル集
