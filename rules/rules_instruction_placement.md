# rules_instruction_placement.md - AI 指示配置ルール

## 目的

RenCrow の AI エージェント向け指示を、常時読み込み、path 固有制約、再利用手順、隔離調査、機械的安全柵へ分離する。

正本仕様は `docs/10_新仕様/82_Claude_Code指示配置ガバナンス仕様.md` とする。

## 配置基準

| 指示の種類 | 配置先 |
| --- | --- |
| module root、責務境界、常時必要な禁止事項 | `AGENTS.md` |
| 特定 directory / file pattern だけの制約 | `rules/rules_path_scoped_constraints.md` または個別 rule |
| runbook、検証手順、繰り返す作業 | `skills/core/*/SKILL.md` |
| 大量ログ解析、広範囲仕様探索、依存棚卸し | subagent |
| 必ず止める危険操作 | hooks / permissions / Tool Harness |
| 一時的な出力形式 | 依頼本文または一時 system prompt |

## 判断ルール

- `AGENTS.md` に 1 回の作業でしか使わない手順を追加しない。
- `AGENTS.md` に長いコマンド列を追加しない。skill へ移す。
- path 固有制約を全作業の常時 context に入れない。
- 危険操作の禁止を文章だけで完結させない。
- Skill 変更は `24_Agent_Skill_Governance仕様` に従い、評価と証跡を残す。

## hooks / permissions 候補

以下は文章ルールではなく、可能な限り機械的に止める。

- `git reset --hard`
- 未確認の `git checkout --`
- workspace root `/home/nyukimi/RenCrow` での誤った build / test / git 操作
- official DB / confirmed memory / Source Registry への未承認 direct write
- broad delete
- external PR 作成、外部投稿、公開、課金、送信
- secret / token / key の表示、保存、commit

