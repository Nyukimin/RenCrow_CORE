# /review-architecture

## Purpose
変更案が現行仕様、責務境界、既存アーキテクチャと矛盾しないか確認する。

## Agent
Coder

## Required Skill
core.architecture-review

## Required Context
- `AGENTS.md`
- `docs/README.md`
- 対象領域に対応する `docs/01_システム概要.md` から
  `docs/09_運用ログ・panic保存仕様.md` の現行正本
- 対象仕様
- 関連 production code
- 必要なら `git diff`

## Steps
1. 変更案の目的を 1 行で要約する。
2. Chat / Worker / Coder / Adapter / Application / Domain / Infrastructure の責務境界と照合する。
3. 現行仕様と矛盾する点を列挙する。
4. 破壊的影響、runtime wiring、config、Viewer/API、E2E 確認の不足を確認する。
5. 採用可否、修正必須点、保留点を分けて返す。

## Output
```text
結論:
一致点:
懸念:
修正必須:
保留:
確認コマンド:
```
