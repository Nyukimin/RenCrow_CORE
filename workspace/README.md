# workspace/ - RenCrow ランタイムキャラクター設定

RenCrowのキャラクター人格・行動規範・スキル定義を管理するディレクトリです。
RenCrow本体（picoClawフレームワーク由来）のランタイム設定であり、
LLMにコンテキストとして渡されます。

## prompts/ との違い

| ディレクトリ | 役割 | 読み込みタイミング |
|-------------|------|-------------------|
| `prompts/` | LLMシステムプロンプト（技術的な指示） | Go起動時に `LoadPrompts()` で読み込み |
| `workspace/` | キャラクター人格・行動規範（上位設計） | RenCrowフレームワークが自動読み込み |

`prompts/mio.md` は「LLMへの技術的なシステムプロンプト」、
`workspace/CHAT_PERSONA.md` は「Mioというキャラクターの人格定義」です。

## ファイル一覧

### キャラクター定義

| ファイル | 内容 |
|---------|------|
| `SOUL.md` | RenCrowの基本人格（性格・価値観） |
| `IDENTITY.md` | プロジェクト情報（名前・バージョン・目的・能力） |
| `CHAT_PERSONA.md` | **Mioのキャラクター設定**（性格・口調・呼称・甘さの演出ルール） |
| `PrimerMessage.md` | 応答の基本型（要点→手順→確認）、モード切替（秘書/友人）、NG例 |

### 行動規範

| ファイル | 内容 |
|---------|------|
| `AGENT.md` | エージェント行動指針（簡潔さ・確認・ツール使用・学習） |
| `USER.md` | ユーザー情報テンプレート（好み・言語・タイムゾーン） |

### 記憶・スキル

| パス | 内容 |
|-----|------|
| `skills/` | スキル定義（各サブディレクトリに `SKILL.md`） |

運用記憶（OperationMemory）は `workspace/` ではなく、`operation_memory_dir`
（未設定時は `~/.picoclaw/rencrow/memory/`）に保存します。
DB や runtime state と同じ永続領域に置き、別 PC 起動時にも一緒に移せるようにします。

## skills/ 一覧

| スキル | 概要 |
|-------|------|
| `github/` | GitHub操作 |
| `hardware/` | ハードウェア情報参照 |
| `skill-creator/` | 新規スキル作成ガイド |
| `summarize/` | テキスト要約 |
| `tmux/` | tmuxセッション操作 |
| `weather/` | 天気情報取得 |

## 編集時の注意

- `CHAT_PERSONA.md` の変更はMioの性格・口調に直接影響します
- `PrimerMessage.md` は応答品質の根幹なので慎重に編集してください
- `operation_memory_dir/MEMORY.md` はランタイムで自動更新される場合があります
