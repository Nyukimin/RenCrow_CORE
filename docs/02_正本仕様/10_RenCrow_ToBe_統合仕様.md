# RenCrow To-Be 統合仕様

- status: canonical
- lifecycle: canonical index
- owner: RenCrow_CORE
- canonical_path: `docs/02_正本仕様/10_RenCrow_ToBe_統合仕様.md`
- parent_spec: `01_仕様.md`、`05_RenCrow_CORE_Ver0.80_モジュール構成仕様.md`
- source_spec: 同pathの2026-07-15分割前正本と採用済みrefs
- implementation_spec: `11_RenCrow_ToBe_統合実装仕様.md`
- promoted_at: 2026-07-14
- last_reviewed: 2026-07-15
- scope: RenCrowの目標状態と領域別正本の入口

## 1. この文書の役割

本書はRenCrowが到達すべき全体像の入口である。目標状態を責務・実装・検証境界ごとの子文書へ分割する。

To-Beの記載だけで実装済みとは判断しない。現在状態は`12_CORE_機能台帳.md`、実装順と受入は`11_RenCrow_ToBe_統合実装仕様.md`を確認する。

## 2. 領域別正本

| 章 | 役割 | 主な内容 |
| --- | --- | --- |
| [01_全体像・CORE責務.md](10_RenCrow_ToBe_統合仕様/01_全体像・CORE責務.md) | umbrella | 前提、全体像、CORE責務、機能台帳 |
| [02_Agent_Profile・Autonomy.md](10_RenCrow_ToBe_統合仕様/02_Agent_Profile・Autonomy.md) | Agent状態 | Profile、Capability、Autonomy、Utility |
| [03_Advisor_Layer.md](10_RenCrow_ToBe_統合仕様/03_Advisor_Layer.md) | 外部助言 | Advisor request / result、approval、score |
| [04_Knowledge_Relation・Recall.md](10_RenCrow_ToBe_統合仕様/04_Knowledge_Relation・Recall.md) | 知識横断 | Relation、Recall、evidence、promotion |
| [05_Economic_Objective・Revenue.md](10_RenCrow_ToBe_統合仕様/05_Economic_Objective・Revenue.md) | 経済目標 | Objective、Workstream、Revenue loop、人間承認 |
| [06_GPT_OSS・Runtime_Flow.md](10_RenCrow_ToBe_統合仕様/06_GPT_OSS・Runtime_Flow.md) | runtime配置 | GPT-OSS-120B、runtime flow、関連実装文書 |

## 3. 派生実装資料

- Advisor / AgentProfile: `../04_構築指標/03_Advisor_AgentProfile接続実装仕様.md`
- Knowledge Relation: `../04_構築指標/04_KnowledgeRelation接続実装仕様.md`
- Economic Objective: `../04_構築指標/05_EconomicObjective接続実装仕様.md`
- To-Be Ops: `../04_構築指標/06_ToBe_Ops表示実装仕様.md`

派生資料は本仕様の意味を黙って変更しない。実装状態が変わったら`12_CORE_機能台帳.md`も同時に更新する。
