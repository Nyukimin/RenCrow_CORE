# README_architecture.md

## 目的

この文書は、RenCrow のアーキテクチャ仕様群の入口である。  
Chat / Worker / Coder の役割分担、共通基盤、保存方針、運用方針を一式で参照できるようにする。

この README 自体は仕様の本体ではない。  
各項目の詳細は個別ドキュメントを正とする。

---

## 全体方針

RenCrow の基本方針は以下の3本である。

- Chat は Hermes 型
- Worker / Coder は Claude Code 型
- 共通基盤は EventId と Hook 駆動

この方針により、会話主体・調査主体・実装主体を分離し、責務の混線を防ぐ。

---

## コア構成

### 1. Chat

- 唯一の会話主体
- 記憶参照の中心
- 委譲判断の中心
- 最終応答の生成担当

参照: `chat_spec.md`

### 2. Worker

- 独立コンテキストの調査専用機
- 読む、探す、比べる、要約する、整える
- 定期整備の担当

参照: `worker_spec.md`

### 3. Coder

- Hook と Sandbox の内側で動く実装担当
- 計画、変更、検証の担当
- 危険操作は Hook で制御

参照: `coder_spec.md`

---

## 共通基盤

### Event / Hook

- イベント列で状態遷移を追う
- Hook で実行節目を強制する
- 最終応答だけでなく、途中過程も観測対象にする

参照:

- `event_schema.md`
- `hook_policy.md`
- `observability.md`

### コマンドと境界契約

- Chat から Worker / Coder へは自由文でなく command で委譲する
- request / result / status / 再委譲条件を境界契約として固定する

参照:

- `commands.md`
- `task_payloads.md`
- `integration_contracts.md`
- `routing_rules.md`

### セッション実行と復旧

- 短期状態は runtime state として持つ
- セッション開始から終了までのライフサイクルを固定する
- 失敗時は戻り先を型で固定する

参照:

- `runtime_state.md`
- `session_lifecycle.md`
- `failure_recovery.md`

---

## 保存と配置

### ストレージ配置

- `events/` は状態遷移の記録
- `audit/` は実行痕跡
- `memory/` は保存層
- `artifacts/` は成果物
- `runtime/` は一時状態

参照: `storage_layout.md`

### 記憶方針

- `profile` は人物記憶
- `skills` は再利用手順
- `history` は履歴検索用要約
- `runtime candidate` は保存候補であり記憶本体ではない

参照: `memory_policy.md`

### 成果物方針

- 成果物は Event でも memory でもない
- Deliverable / Draft / Verification / Temp / Derived を分ける
- temp を恒久参照先にしない

参照: `artifact_policy.md`

---

## 運用と保護

### 定期整備

- Worker が起動時・日次・週次・手動の整備を担当する
- 自動統合はしない
- 整備は元データ非破壊を前提とする

参照: `maintenance_jobs.md`

### セキュリティ境界

- 許可は信頼ではなく境界で決める
- Sandbox 不可だからといって自動で緩めない
- MCP は自動全開放しない
- 共有環境は自動変更しない

参照: `security_boundary.md`

---

## 新規リポジトリへの適用

この仕様群を新規リポジトリへ持ち込む際は、最初から全機能を実装しない。  
先に最小 end-to-end を通し、その上で観測、保存、整備を積み上げる。

参照: `repo_bootstrap.md`

---

## 推奨読順

### 最初に読む

1. `README_architecture.md`
2. `chat_spec.md`
3. `worker_spec.md`
4. `coder_spec.md`

### 次に読む

5. `event_schema.md`
6. `hook_policy.md`
7. `commands.md`
8. `routing_rules.md`
9. `task_payloads.md`

### 実装時に読む

10. `storage_layout.md`
11. `runtime_state.md`
12. `session_lifecycle.md`
13. `failure_recovery.md`
14. `integration_contracts.md`

### 運用時に読む

15. `maintenance_jobs.md`
16. `observability.md`
17. `security_boundary.md`
18. `memory_policy.md`
19. `artifact_policy.md`

### リポジトリ立ち上げ時に読む

20. `repo_bootstrap.md`

---

## 最小実装順

最小実装は以下の順で行う。

1. EventId 発行
2. Chat → Worker / Coder の command 委譲
3. PreToolUse / PostToolUse / Stop Hook
4. result 契約の受理判定
5. runtime state
6. history 要約保存
7. skill 候補抽出
8. 定期整備ジョブ

この順を崩すと、先に複雑化して追跡不能になりやすい。

---

## 設計原則の要約

RenCrow の設計原則は以下に要約される。

- Chat は考える
- Worker は読む
- Coder は直す
- Hook は止める
- EventId は追えるようにする
- 記憶は分ける
- 成果物は混ぜない
- 境界は自動で緩めない

以上を、各仕様書で個別に具体化する。
