# デザインMCP導入メモ

作成日: 2026-06-05

## 目的

RenCrow / Viewer UI 改善で、デザイン生成、Figma 連携、実ブラウザ確認を使えるようにする。

## 導入済み MCP

| name | 用途 | 設定 |
| --- | --- | --- |
| `aidesigner` | UI 生成、既存スタックに合わせたデザイン案作成、Tailwind 生成 | `.codex/config.toml`, `.mcp.json` |
| `figma` | Figma デザイン読み取り、書き込み、Figma 公式 Skills による design-to-code | `.codex/config.toml`, `.mcp.json` |
| `playwright` | Viewer / Web UI の実ブラウザ操作、スクリーンショット、E2E 確認 | `.codex/config.toml`, `.mcp.json` |

## 導入コマンド

```bash
npx -y @aidesigner/agent-skills init codex --trust-project
codex mcp add figma --url https://mcp.figma.com/mcp
codex mcp add playwright -- npx @playwright/mcp@latest
```

Figma 公式 Skills は `figma/mcp-server-guide` の `skills/` を `.agents/skills/` に配置している。

## 認証

`aidesigner` は OAuth が必要。

```bash
codex mcp login aidesigner
```

この環境ではブラウザ自動起動に失敗することがある。その場合は表示された URL をブラウザで開いて認証する。

`figma` は Figma 側の OAuth が必要。Codex CLI で認証できない場合は Codex app の Plugins から Figma を入れて認証する。

## 使い分け

- 初期 UI 案を作る: `aidesigner`
- Figma ファイルから実装する、または Figma に書き戻す: `figma` + `figma-*` Skills
- Viewer の表示崩れや操作確認を詰める: `playwright`

## 検証コマンド

```bash
codex mcp list
npx -y @aidesigner/agent-skills doctor codex
jq . .mcp.json
```

`aidesigner doctor codex` は project config / project skill / project trust が `ok: true` なら RenCrow project 用の導入は成功扱い。
