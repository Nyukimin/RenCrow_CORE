# /tool-harness-check

## Purpose
tool call が Tool Harness、Command Gate、Sandbox Promotion Gate の契約を満たすか確認する。

## Agent
Worker

## Required Skill
core.tool-harness-check

## Required Context
- 対象 tool call
- 対象 tool schema
- 実行予定の環境
- Human approval の有無

## Steps
1. validate-then-repair の対象か確認する。
2. 修復が issue path に限定されているか確認する。
3. Command Gate / Safety Gate の risk class を判定する。
4. Sandbox root 外 write、外部送信、破壊的操作がないか確認する。
5. 実行してよい / retry message を返す / block する、を明確に分ける。

## Output
```text
判定:
理由:
修復可否:
Safety Gate:
Human approval:
次アクション:
```
