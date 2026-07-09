# README_architecture v2

## この設計書セットの位置づけ

この設計書セットの中心は、Chat / Worker / Coder の役割分担ではない。  
中心にあるのは「記憶」と「記録」である。

Mio の設計では、まず一次情報を失わないための原本保存を持ち、その上で、想起・人格形成・継続性のための記憶層を育てる。  
Chat / Worker / Coder は、その記憶と記録に対して異なる権限と責務を持つ Actor として定義する。

この README は、各仕様書への入口であり、実装順と読順のガイドでもある。  
個々の仕様の本体は各ドキュメントにある。

## 設計の中心命題

Mio には、次の 2 系統を分けて持たせる。

### 記憶システム
思い出すために持つ。  
想起、人格の継続、れんとの関係性、作業の最適化に使う。  
そのため、圧縮、忘却、昇格、再構成を前提とする。

### 記録システム
失わないために持つ。  
会話、報告、作業、イベント、成果物参照を一次情報として残す。  
普段の応答では直接使わず、必要なときだけ掘り返す。

この 2 系統を混ぜないことが、Mio の継続性と可塑性を両立させる前提になる。

## Actor の定義

Actor は人格の有無ではなく、記憶と記録へのアクセス権で定義する。

### Chat
全記憶を統べる中枢。  
本人格を持ち、れんとの関係性を保持する。  
全層を参照し、記憶更新の最終判断を持つ。

### Worker
作業支援 Actor。  
作業記憶とロールプレイ用の人格を持つ。  
調査、比較、要約、定期整備、昇格候補抽出を担う。  
関係性記憶の最終判断は持たない。

### Coder
実装 Actor。  
作業用記憶だけを持つ。  
Hook と Sandbox の境界内で、計画、変更、検証を行う。  
非人格的で、関係性記憶を持たない。

## 記憶アーキテクチャの考え方

Mio の記憶は、完全網羅を目指さない。  
継続運用の中で、人格が少しずつ深まることを目標にする。

そのために、次の流れを持つ。

1. リアルタイムでは、チャットログと作業文脈を整理する  
2. 日次で、会話と作業を Event 単位に要約する  
3. 一定期間で、圧縮・忘却・昇格を行う  
4. 長期では、関係つき知識、関係性断片、作業手順を別々に育てる  
5. 必要なときだけ、今の文脈に合う断片を少数想起する

ここで重要なのは、「全部保存して全部想起する」ではなく、  
「原本は記録に残し、記憶は想起に効く形へ再編集する」ことである。

## 技術方針

技術は段階的に入れる。

### まず使うもの
- DB
- 検索
- EventId
- Hook
- append-only な原本保存

### 次に使うもの
- RAG
- 関係検索
- 記憶スコアリング
- TTL / 昇格 / 圧縮

### 必要になったら使うもの
- GraphRAG
- グラフベースの参照知識
- ファインチューニング

ファインチューニングは出発点ではない。  
まず、保存・整理・想起・昇格の流れを外部システムとして成立させる。

## 文書マップ

### 中心文書
- `memory_architecture.md`  
  記憶システム全体の考え方。人格、関係性、想起、昇格、忘却の中心文書。

- `source_preservation.md`  
  原本保存の考え方。会話、報告、作業を「記録」として残す別系統。

### 保存形式
- `memory_storage_schema.md`  
  記憶側の保存形式。Identity / Relationship / Project / Work Optimization / Reference を定義。

- `record_storage_schema.md`  
  記録側の保存形式。Conversation / Task / Event / Audit / Artifact を定義。

### Actor 仕様
- `chat_spec.md`
- `worker_spec.md`
- `coder_spec.md`

### 共通基盤
- `event_schema.md`
- `hook_policy.md`
- `commands.md`
- `task_payloads.md`
- `integration_contracts.md`

### 運用系
- `runtime_state.md`
- `session_lifecycle.md`
- `failure_recovery.md`
- `maintenance_jobs.md`
- `observability.md`
- `security_boundary.md`
- `storage_layout.md`
- `artifact_policy.md`
- `routing_rules.md`
- `repo_bootstrap.md`

## 推奨読順

1. `memory_architecture.md`
2. `source_preservation.md`
3. `README_architecture_v2.md`
4. `memory_storage_schema.md`
5. `record_storage_schema.md`
6. `storage_layout.md`
7. `chat_spec.md`
8. `worker_spec.md`
9. `coder_spec.md`
10. `event_schema.md`
11. `hook_policy.md`
12. `commands.md`
13. `task_payloads.md`
14. `integration_contracts.md`
15. `runtime_state.md`
16. `session_lifecycle.md`
17. `failure_recovery.md`
18. `maintenance_jobs.md`
19. `observability.md`
20. `security_boundary.md`
21. `artifact_policy.md`
22. `routing_rules.md`
23. `repo_bootstrap.md`

## 最小実装順

最初から賢くしない。  
まず、最小の end-to-end を成立させる。

### Phase 1
- EventId を通す
- 原本保存を append-only で置く
- Chat → Worker / Coder の委譲を通す
- Hook を最小限有効化する

### Phase 2
- 記憶ストレージを分離する
- 日次要約を Worker で回す
- 想起候補を 3〜5 件だけ先読みする

### Phase 3
- 昇格、圧縮、忘却を入れる
- 関係つき Reference Memory を育てる
- 定期整備を安定運用する

### Phase 4
- GraphRAG や高度な関係検索を検討する
- 必要なら軽量ファインチューニングを検討する

## 現時点の設計原則

- 記憶と記録を混ぜない
- 原本は失わない
- 記憶は想起のために再編集する
- Chat が全記憶を統べる
- Worker は作業記憶とロール人格を持つ
- Coder は作業記憶だけを持つ
- 保存より先に想起設計を立てる
- 完全想起ではなく、自然な数件の想起を目指す
- 最終判断は Chat が持つ
