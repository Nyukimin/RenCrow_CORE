# RenCrow Knowledge Architecture 設計判断

起点: 2026-08-21 Qwen知識劣化の議論

## 基本原則
RenCrowはLLM交換可能を前提とする。
Knowledge belongs to RenCrow.
Reasoning belongs to the LLM.

LLMのparametric knowledgeは補助であり、RenCrowの正規Knowledgeの正本にしない。

## 分離
- Evidence: 原資料・観測・source
- Knowledge: 検証・正規化された共有知識
- Memory: ユーザー/Agentが経験した履歴
- Belief: Agentが現時点で持つ仮説・評価

## 流れ
Evidence
→ Canonical Knowledge
→ Indexes
→ Task-specific Recall
→ Agent-specific View
→ LLM reasoning

Knowledge Broker / Recall Orchestrator相当の境界で、taskごとに必要な量と形式へ再構成する。

## Knowledge growth
ユーザーとの会話・指示からだけ知識を増やすのでは不足する。
Knowledge gapやinterest domainを検出し、Source Registry→Search/Feed/API→Staging→Validator→Promotionの自律Research loopを将来持つ。

## DB vs LoRA / Fine-tuning
最新事実、ユーザー情報、仕様、KnowledgeをDB/Memoryの正本として保持する。
LoRA/FTは、安定した技能、推論傾向、行動特性など派生的に学習する限定用途。
Knowledgeを自動即時LoRA化しない。
