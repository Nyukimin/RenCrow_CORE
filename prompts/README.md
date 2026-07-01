# prompts/ - LLMシステムプロンプト

RenCrowの各LLMコンポーネントが使用するシステムプロンプトを管理するディレクトリです。
ファイルを編集するだけでプロンプトを変更でき、リビルド不要です。

## 設定

`config.yaml` で読み込みパスを指定:

```yaml
prompts_dir: "./prompts"
```

未設定またはファイルが存在しない場合、ビルトインのフォールバック値が使われます。

## ファイル一覧

| ファイル | 用途 | 使用コンポーネント |
|---------|------|-------------------|
| `mio.md` | Mioの会話ペルソナ（性格・口調・特徴） | ConversationEngine → Mio Chat |
| `coder.md` | Coder proposal生成の出力フォーマット指示 | CoderAgent (Coder1/2/3共通) |
| `classifier.md` | タスク分類器のカテゴリ定義と応答指示 | LLMClassifier (ルーティング) |
| `worker.md` | Shiro Workerの基本指示 | ShiroAgent |
| `idle_chat/mio.md` | IdleChat用 Mioの性格 | IdleChatOrchestrator |
| `idle_chat/shiro.md` | IdleChat用 Shiroの性格 | IdleChatOrchestrator |
| `idle_chat/aka.md` | IdleChat用 Akaの性格 | IdleChatOrchestrator |
| `idle_chat/ao.md` | IdleChat用 Aoの性格 | IdleChatOrchestrator |
| `idle_chat/gin.md` | IdleChat用 Ginの性格 | IdleChatOrchestrator |

## キャラクタープロンプト

Mio のキャラクタープロンプト、固定ナレッジは workspace 側にまとめます。
`mio/` ディレクトリは `manifest.txt` を持ち、読み込む `.md` ファイルの順序を明示します。

```text
~/.picoclaw/workspace/prompts/characters/
└── mio/     # Chat
```

標準構成:

```text
manifest.txt
00_system.md
10_policy.md
20_routing.md
30_knowledge.md
```

repo 側の `prompts/characters` は参照しません。
Mio の運用正本は `workspace_dir/prompts/characters/mio` に置きます。

## 注意事項

- ファイル形式: UTF-8テキスト（拡張子は `.md` だが中身はプレーンテキスト）
- 前後の空白行は自動トリムされます
- 空ファイルはフォールバック値として扱われます（無視されます）
- `mio.md`（会話用）と `idle_chat/mio.md`（雑談用）は別のプロンプトです
