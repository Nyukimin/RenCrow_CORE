# Claude Code 指示配置ガバナンス仕様

## 1. 目的

本仕様は、RenCrow における AI エージェント向け指示を、`AGENTS.md`、rules、skills、subagents、hooks / permissions、output style、system prompt 追記へ適切に分配するための基準を定義する。

目的は、すべての指示を常時読み込ませるのではなく、必要な指示を必要なタイミングで読み込ませ、絶対に守るべき制約は機械的に強制することである。

本仕様は、Anthropic の "Steering Claude Code: CLAUDE.md files, skills, hooks, rules, subagents and more" の考え方を RenCrow の Worker / Coder / Chat / Tool Harness 構成へ再設計して取り込む。

## 2. 背景

RenCrow は複数 module を束ねる workspace であり、`/home/nyukimi/RenCrow` は単一 repo ではない。

```text
RenCrow workspace root
  picoclaw_multiLLM  Core / Chat / Viewer / runtime
  RenCrow_CMD        CLI / command client
  RenCrow_STT        speech-to-text
  RenCrow_TTS        text-to-speech
  RenCrow_LLM        local LLM runtime
  RenCrow_Vision     vision
  RenCrow_Tools      shared tools / browser sidecar / verification CLI
```

この構造では、すべての作業規約を root `AGENTS.md` や module `AGENTS.md` へ追加し続けると、以下の問題が起きる。

```text
- 常時 context が重くなる
- 対象外 module の指示が混入する
- 長い手順が毎回読み込まれる
- 禁止事項が文章ルール止まりになり、実行時にすり抜ける
- Viewer / STT / rencrow-data など専門領域の検証手順が散逸する
- compaction 後に必要な手順や制約が失われる
```

したがって、RenCrow では「どこに指示を書くか」を設計対象として扱う。

## 3. 位置づけ

本仕様は以下に接続する。

```text
21_AI_Native_Engineering_Workflow仕様
  AI が働く開発環境と context / tool / authority / verification / safety boundary を定義する。

24_Agent_Skill_Governance仕様
  Skill / Command / Plugin / Agent Rule の管理と評価を定義する。

20_Tool_Harness_Contract_Mediation仕様
  tool call の検証、修復、安全実行を定義する。

AGENTS.md
  module root と常時必要な最小ルールを定義する。
```

本仕様は、既存仕様を置き換えない。既存仕様に散在する AI 指示を、どの媒体へ配置すべきかを決める上位の配置規約である。

## 4. 基本原則

RenCrow では、AI エージェント向け指示を以下の原則で配置する。

```text
1. 常時必要な事実だけを AGENTS.md に置く。
2. path や file pattern に依存する制約は path-scoped rules に置く。
3. 手順、runbook、チェックリストは skills に置く。
4. 大量調査、ログ解析、依存関係調査は subagents に隔離する。
5. 絶対に守る禁止事項は hooks / permissions で機械的に止める。
6. 出力口調や作業モードの恒久変更は output style として慎重に扱う。
7. 一時的な追加指示は system prompt 追記または依頼本文に閉じる。
```

AI への文章指示は、遵守率を上げることはできるが、完全な安全柵ではない。

削除、破壊的 git 操作、official DB 直接 write、外部送信、公開、課金、PR 作成など、人間の承認や監査が必要な操作は、文章ではなく Tool Harness、hooks、permissions、Promotion Gate、Human approval で止める。

## 5. 指示媒体の責務分担

| 媒体 | RenCrow での用途 | 置くべきもの | 置かないもの |
| --- | --- | --- | --- |
| root `AGENTS.md` | workspace 全体の索引 | module root 一覧、repo 境界、横断ツール正本、最重要禁止事項 | Viewer 詳細手順、STT 測定手順、長い runbook |
| module `AGENTS.md` | module の常時ルール | module 目的、責務境界、一次参照、実装前ルール | 特定 directory だけの細則、検証ログ |
| path-scoped rules | file / directory 固有制約 | Viewer UI、STT、rencrow-data、systemd、DB migration の制約 | 汎用作業手順 |
| skills | 再利用可能な手順 | rebuild / restart、Viewer E2E、STT latency probe、paper trade refresh、spec writing | 常時知っているべき module 一覧 |
| subagents | 隔離された調査 | 大量ログ解析、広範囲仕様探索、依存関係棚卸し、deep search | main thread で逐次 steering したい作業 |
| hooks / permissions | 機械的強制 | 危険 command block、format / test 自動化、PreCompact 保存、外部送信 block | 判断が必要な設計相談 |
| output style | 会話スタイル変更 | 教育モード、説明重視モードなど明示的な作業モード | coding assistant 標準挙動を壊す指示 |
| system prompt 追記 | その起動だけの追加条件 | 一時的な文体、出力形式、特定基準 | 永続的な project ルール |

## 6. AGENTS.md の設計基準

### 6.1 root AGENTS.md

root `AGENTS.md` は、RenCrow workspace に入った AI が最初に誤らないための地図とする。

必須内容:

```text
- `/home/nyukimi/RenCrow` は管理 root であり git root ではないこと
- module root 一覧
- 横断的な新規 tool は RenCrow_Tools を正本にすること
- 作業前に対象 module を決め、module 側 AGENTS.md を読むこと
- 危険操作は rules / hooks / permissions に従うこと
```

禁止内容:

```text
- module 固有の詳細実装手順
- Viewer / STT / rencrow-data などの長い検証 checklist
- 特定作業のコマンド羅列
- 一時的な個人 preference
```

### 6.2 module AGENTS.md

module `AGENTS.md` は、その module で常時必要な判断基準だけを置く。

`picoclaw_multiLLM/AGENTS.md` に残すべき内容:

```text
- Core / Chat / Viewer / runtime の責務
- Chat / Worker / Coder の責務分離
- 一次参照 docs
- 実装開始前の許可、TDD、検証原則
- Viewer / UI では実ブラウザ確認が必要であること
```

別媒体へ移すべき内容:

```text
- Viewer の具体的 Playwright 手順 -> skill
- STT latency probe 手順 -> skill
- rencrow-data refresh 手順 -> skill
- directory 横断の安全制約 -> rules
- 危険 command の禁止 -> hooks / permissions
```

## 7. Path-scoped rules の設計基準

path-scoped rules は、特定 path にだけ適用される制約を置く。

RenCrow では以下を優先して分離する。

| 対象 | rule の例 |
| --- | --- |
| `RenCrow_STT/**` | STT ownership、同期送受信測定、audio fixture、secure context |
| `RenCrow_TTS/**` | voice asset、engine boundary、latency measurement |
| `RenCrow_LLM/**` | local LLM provider、model context、OpenAI-compatible endpoint |
| `RenCrow_CMD/**` | CLI contract、server endpoint compatibility、audio-file input |
| `picoclaw_multiLLM/modules/stt/**` | gateway integration、viewer chat input boundary |
| `picoclaw_multiLLM/viewer/**` | desktop / mobile browser verification、pointer-events、z-index |
| `picoclaw_multiLLM/rencrow-data/**` | snapshot_id、paper trade audit、approval metadata |
| `picoclaw_multiLLM/systemd/**` | user service install、restart、WorkingDirectory |
| `picoclaw_multiLLM/internal/infrastructure/persistence/**` | DB / JSONL migration and audit boundary |

rule は短く保ち、実行手順を含めない。実行手順は skill に置く。

## 8. Skills の設計基準

skills は、再利用される手順、判断基準、成果物形式を持つ作業に使う。

RenCrow で優先して整備する skill:

| Skill | 目的 |
| --- | --- |
| `picoclaw-service-rebuild-restart` | `make install`、`picoclaw.service` restart、health / viewer verification |
| `viewer-live-verification` | Playwright / screenshot / DOM / mobile width verification |
| `stt-latency-debug` | STT module 境界確認、同時送受信 timing probe、live service 確認 |
| `rencrow-data-refresh-audit` | market data refresh、paper trade audit、snapshot traceability |
| `idlechat-stop-verify` | IdleChat stop command / endpoint / persisted state verification |
| `repair-plane-operation` | self-repair button / command / endpoint / audit result verification |
| `understand-picoclaw-graph` | Understand Anything graph generation / dashboard launch |
| `spec-writing-rencrow` | 仕様作成時の保存先、関連仕様検索、実装条件、完了条件 |

Skill は以下を含む。

```text
- trigger 条件
- 対象 module
- 事前確認
- 実行手順
- 失敗時の切り分け
- 検証条件
- 最終報告に含める証跡
```

Skill は便利メモではなく、エージェント行動を変える実行規約として扱う。変更時は `24_Agent_Skill_Governance仕様` に従い、評価、差分、Human approval を残す。

## 9. Subagents の設計基準

subagents は、main thread へ中間結果を大量に持ち込むと作業品質が下がる調査に使う。

RenCrow で subagent に向く作業:

```text
- docs/ 配下の広範囲仕様探索
- live service log の大量解析
- dependency / import graph の棚卸し
- Viewer UI regression の複数 viewport evidence 収集
- rencrow-data の source universe / DB schema 調査
- understand-anything graph からの architecture query
```

subagent の返却は、原則として以下に限定する。

```text
- 結論
- 根拠ファイル / line / command
- 未確認事項
- main thread が次に実行すべき具体 action
```

subagent に作業を投げる場合でも、正式な file edit、service restart、push、外部送信は main thread の Worker / Tool Harness / Human approval 経由で扱う。

## 10. Hooks / permissions の設計基準

hooks / permissions は、文章ルールでは不十分な禁止や自動処理に使う。

RenCrow で機械的に止めるべき操作:

```text
- `git reset --hard`
- `git checkout --` による未確認 revert
- workspace root での誤った `git` / build / test 実行
- official DB / confirmed memory / Source Registry への未承認 direct write
- `rm -rf` や broad delete
- external PR 作成、外部投稿、公開、課金、送信
- secret / token / key の表示、保存、commit
```

RenCrow で自動化候補となる hook:

```text
- PreToolUse: 危険 command block、repo root mismatch block
- PostToolUse: file edit 後の formatter / focused test suggestion
- PreCompact: session summary、未完了TODO、重要証跡 path の保存
- Stop: worktree status、未push / 未検証 warning
```

Hook の出力を main context に戻す場合は、短い blocking reason と next action に限定する。

## 11. Output style / system prompt 追記

output style は強い指示媒体であり、RenCrow では慎重に扱う。

使用してよい例:

```text
- 学習目的で説明を厚くする一時的な coding style
- review-only mode
- terse operations mode
```

避けるべき例:

```text
- Claude Code / Codex の標準 coding assistant 挙動を消す
- verification habit を弱める
- safety boundary を文体指示で上書きする
- project 固有 rule を output style に埋め込む
```

system prompt 追記は、その invocation だけに必要な追加条件へ限定する。永続化が必要なものは AGENTS.md、rules、skills、hooks のいずれかへ移す。

## 12. 移行方針

### 12.1 棚卸し

既存の以下を棚卸しする。

```text
- root AGENTS.md
- module AGENTS.md
- CLAUDE.md
- rules/
- docs/10_新仕様/
- skills/
- commands/
- service runbook
```

各指示を以下に分類する。

```text
always_on_fact
path_scoped_constraint
procedure
isolated_research
deterministic_guardrail
temporary_preference
obsolete_or_duplicate
```

### 12.2 移動基準

| 分類 | 移動先 |
| --- | --- |
| `always_on_fact` | root / module `AGENTS.md` |
| `path_scoped_constraint` | path-scoped rules |
| `procedure` | skills |
| `isolated_research` | subagents |
| `deterministic_guardrail` | hooks / permissions / Tool Harness |
| `temporary_preference` | user-level setting or invocation prompt |
| `obsolete_or_duplicate` | 削除候補。削除前に参照元を確認する |

### 12.3 段階移行

```text
Phase 1: AGENTS.md を索引化し、module boundary と常時ルールだけに絞る。
Phase 2: Viewer / STT / rencrow-data / service operation を skills へ分離する。
Phase 3: path-scoped rules を module / directory ごとに追加する。
Phase 4: 危険 command と外部作用を hooks / permissions で block する。
Phase 5: 大量調査用 subagents を定義する。
Phase 6: Skill / rule / hook の評価ログを Skill Governance に接続する。
```

## 13. 完了条件

本仕様の初期適用は、以下を満たしたとき完了とする。

```text
- root AGENTS.md が 200 lines 程度を目安に索引化されている
- module AGENTS.md が常時必要な module 固有 rule に絞られている
- Viewer / STT / rencrow-data / service restart の手順が skill 化されている
- module / path 固有制約が path-scoped rules に分離されている
- 危険 command / external side effect が hook or permission で止まる
- PreCompact 相当の作業記録保存が定義されている
- Skill 変更時の評価と Human approval が `24_Agent_Skill_Governance仕様` に接続されている
```

## 14. 非目標

本仕様では以下を行わない。

```text
- Claude Code の実装をそのまま RenCrow に移植する
- 既存 AGENTS.md を即時削除する
- すべての docs を rules / skills へ自動移行する
- Human approval を省略する
- hooks / permissions 未整備のまま危険操作を許可する
- output style で安全規約を上書きする
```

## 15. 運用上の注意

RenCrow では、指示が長くなるほど安全になるとはみなさない。

安全性と作業品質は、以下の組み合わせで確保する。

```text
短い常時指示
対象 path だけに効く rules
必要時だけ読む skills
隔離された subagents
機械的に止める hooks / permissions
証跡を残す Workstream / Skill Governance
```

常時 context に入れる指示は、AI がどの作業でも必ず知っている必要があるものだけに絞る。

手順は skill へ、安全柵は hook へ、調査は subagent へ、制約は rules へ分離する。

