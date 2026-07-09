# LLM設定ファイルの場所（集約）

このファイルは、LLM関連設定の所在を一か所で確認するための索引です。

## 1) 実行時設定（最優先）

- `config/config.yaml`
  - メインの設定ファイル。
  - `providers`, `conversation`, `agent`, `workers` などの実行時設定を保持。
- `config.yaml`（リポジトリ直下）
  - `config/config.yaml` へのシンボリックリンク。
  - 既存コマンド互換のために残している入口。

## 2) 初期セットアップ用テンプレート

- `config/config.yaml.example`
  - 新規環境でコピーして使うテンプレート本体。
- `config.yaml.example`（リポジトリ直下）
  - `config/config.yaml.example` へのシンボリックリンク（互換入口）。

## 3) Ollamaモデル定義

- `config/Modelfile.chat`
  - `Chat` 側のモデルビルド定義（Ollama `Modelfile`）の本体。
- `Modelfile.chat`（リポジトリ直下）
  - `config/Modelfile.chat` へのシンボリックリンク（互換入口）。

## 4) 会話プロンプト・人格（LLM挙動に影響）

- `config/persona/PrimerMessage.md`
  - 返信スタイル・出力制約の本体。
- `workspace/PrimerMessage.md`（互換リンク）
  - `config/persona/PrimerMessage.md` へのシンボリックリンク。
- `config/prompts/idle_chat/mio.md`
  - idle-chat向けのMioプロンプト本体。
- `config/prompts/idle_chat/shiro.md`
  - idle-chat向けのShiroプロンプト本体。
- `prompts/idle_chat/mio.md` / `prompts/idle_chat/shiro.md`（互換リンク）
  - `config/prompts/idle_chat/` へのシンボリックリンク。

## 5) 読み込みコード（参照先）

- `cmd/rencrow/main.go`
  - 設定ファイルの既定パス解決。
- `internal/adapter/config/config.go`
  - `config.yaml` のスキーマ定義と読み込み処理。

---

## 運用メモ

- まず `config/config.yaml` を編集し、必要に応じて `config/Modelfile.chat` を更新する。
- プロンプト調整は `config/persona/` と `config/prompts/` を編集する。
