# Prompt Bundle 仕様

この文書は、LLM role ごとの固定プロンプトを安定した prefix として構成し、KV キャッシュを効率的に利用するための Prompt Bundle 仕様を定義する。

## 目的

- Chat / Worker / Heavy / Wild ごとの system prompt と専用ナレッジを role 単位で管理する。
- 固定プロンプトの読み込み順と内容を明示し、KV キャッシュの再利用効率を高める。
- Prompt 追加や削除による意図しない prefix 変更を manifest lint で検出する。
- role 間の責務混在を防ぐ。

## 対象 role

対象 role は以下とする。

- `chat`
- `worker`
- `heavy`
- `wild`

実装上のキャラクター名は以下の role に対応する。

| character | role |
|-----------|------|
| `mio` | Chat |
| `shiro` | Worker |
| `kuro` | Heavy |
| `midori` | Wild |
| `aka` / `ao` / `gin` / `kin` | Coder |

## ディレクトリ構成

各 character のキャラクター別プロンプトは `workspace_dir/prompts/characters/<character>/` を正本にする。
repo 側の `prompts/characters/` は参照しない。

```text
~/.picoclaw/workspace/prompts/characters/mio/
~/.picoclaw/workspace/prompts/characters/shiro/
~/.picoclaw/workspace/prompts/characters/kuro/
~/.picoclaw/workspace/prompts/characters/midori/
~/.picoclaw/workspace/prompts/characters/aka/
~/.picoclaw/workspace/prompts/characters/ao/
~/.picoclaw/workspace/prompts/characters/gin/
~/.picoclaw/workspace/prompts/characters/kin/
```

各 character ディレクトリには `manifest.txt` を置き、読み込む `.md` ファイルと順序を明示する。

例:

```text
~/.picoclaw/workspace/prompts/characters/<character>/
  manifest.txt
  00_system.md
  10_policy.md
  20_routing.md
  30_knowledge.md
```

将来的な role 共通配置として、以下の role ディレクトリも仕様上は扱える。

```text
~/.picoclaw/prompts/llm/chat/
~/.picoclaw/prompts/llm/worker/
~/.picoclaw/prompts/llm/heavy/
~/.picoclaw/prompts/llm/wild/
```

`manifest.txt` の例:

```text
# fixed prompt prefix for chat
00_system.md
10_policy.md
20_routing.md
30_knowledge.md
```

## 読み込み仕様

`~/.picoclaw/prompts/llm/{role}/manifest.txt` を、その role の固定プロンプト定義の正本とする。

manifest に列挙された `.md` ファイルを、記載順に結合する。この結合結果を `role_static_prompt` と呼ぶ。

```text
role_static_prompt =
  00_system.md
  + fixed separator
  + 10_policy.md
  + fixed separator
  + 20_routing.md
  + fixed separator
  + 30_knowledge.md
```

`fixed separator` は実装で固定された区切り文字列とし、実行ごとに変化してはならない。

`role_static_prompt` は KV キャッシュ対象の固定 prefix として扱う。

## 固定 prefix ルール

`role_static_prompt` には、実行ごとに変わる情報を含めてはならない。

含めてはならない情報:

- 現在日時
- `session_id`
- `job_id`
- ユーザー入力
- 会話履歴
- ツール実行結果
- 一時状態
- ランダム値
- 環境依存の動的情報

動的コンテキストは必ず `role_static_prompt` の後ろに追加する。

```text
[固定 prefix]
role_static_prompt

[動的 context]
現在日時
session_id
会話履歴
ユーザー入力
実行状態
```

固定 prefix の途中に動的情報を挿入してはならない。

## Manifest 仕様

`manifest.txt` は以下の形式とする。

- 1 行に 1 ファイル名を書く。
- 空行は無視する。
- `#` で始まる行はコメントとして無視する。
- 指定できるのは role ディレクトリ直下の `.md` ファイルのみとする。
- `../` を含むパスやサブディレクトリ参照は禁止する。
- 読み込み順は manifest の記載順とする。

## Manifest Lint 仕様

各 role ディレクトリに対して manifest lint を行う。

検証項目:

1. `manifest.txt` が存在する。
2. manifest に列挙されたファイルが全て存在する。
3. manifest に列挙されたファイルは `.md` のみである。
4. role ディレクトリ直下の `.md` は全て manifest に列挙されている。
5. manifest に重複行がない。
6. manifest のパスに `../` やサブディレクトリ参照を含めない。
7. 空行と `#` 始まりのコメント行は無視する。
8. 読み込み順は manifest の記載順と一致する。

Lint エラー例:

```text
prompt manifest mismatch: role=chat
missing files:
  - 20_routing.md
unlisted md files:
  - 99_local_note.md
duplicate entries:
  - 10_policy.md
```

## 旧形式からの移行

旧形式:

```text
~/.picoclaw/prompts/llm/chat.system.md
~/.picoclaw/prompts/llm/worker.system.md
~/.picoclaw/prompts/llm/heavy.system.md
~/.picoclaw/prompts/llm/wild.system.md
```

新形式:

```text
~/.picoclaw/prompts/llm/chat/manifest.txt
~/.picoclaw/prompts/llm/chat/00_system.md

~/.picoclaw/prompts/llm/worker/manifest.txt
~/.picoclaw/prompts/llm/worker/00_system.md

~/.picoclaw/prompts/llm/heavy/manifest.txt
~/.picoclaw/prompts/llm/heavy/00_system.md

~/.picoclaw/prompts/llm/wild/manifest.txt
~/.picoclaw/prompts/llm/wild/00_system.md
```

移行期間を設ける場合は、新形式を優先する。

新形式が存在しない場合のみ旧形式を fallback として読む。ただし、最終的には旧形式を deprecated とする。

## 責務分離

各 role の専用知識は、その role ディレクトリ内に閉じ込める。

Chat / Worker / Heavy / Wild の責務をまたぐプロンプト混在は禁止する。

例:

- Worker 専用の実行ポリシーを Chat 側に混ぜない。
- Chat のルーティング判断を Worker 側に混ぜない。
- Heavy の失敗原因分析用ナレッジを通常 Worker の固定 prefix に常時混ぜない。
- Wild の創作用ナレッジを Chat の通常会話 prefix に混ぜない。

固定プロンプトは LLM role の恒久的な振る舞いを定義するためのものであり、セッション状態や実行状態の保存場所として使ってはならない。
