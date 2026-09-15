# RenCrow_CORE rules

本書はRenCrow_COREだけの入口。[統一ルール](../AGENTS.md)を継承し、本文・モデル役割・共通検査規定を複製しない。親がcatalogではない配置では、global設定が参照するEcoSystem正本（manifestの隣のAGENTS.md）を確認する。既読なら読み直さない。

## 所有範囲

共有製品契約、実Agent、会話、Worker／Coder、Public API、Debug Viewer、routing・policy・状態の正本を所有する。製品のCoderはplan／patchを生成し、適用・実行は許可されたWorkerが担当する。認証・policy・実Actorの境界を維持する。

## 必要なときに読む

対象の`README.md`／`docs/README.md`から現行仕様を選ぶ。下表の該当節だけを作業前に読み、対象外の節や他moduleの詳細をまとめて読まない。製品契約の不足は実装・test・production wiringと照合してowner正本へ反映する。

| 作業 | 必須の参照 |
|---|---|
| 製品契約・routing・他moduleとの境界 | [CORE契約](rules/task-rules.md#contract) |
| コード・設定・挙動の変更 | [実装と検証](rules/task-rules.md#implementation) |
| Tool／Skill／MCPの追加・変更 | [Capability登録](rules/task-rules.md#capability) |
| LLM連携・response処理 | [LLM境界](rules/task-rules.md#llm) |
| Viewer・表示・音声同期 | [表示検証](rules/task-rules.md#viewer) |
| ID・cache・queue・永続状態 | [状態管理](rules/task-rules.md#state) |
| Worker実行・process・再起動 | [運用手順](rules/task-rules.md#runtime) |
| 回帰・観測・外部調査 | [証拠](rules/task-rules.md#evidence) |
| 仕様・文書・コメントだけの変更 | [文書の検証](rules/task-rules.md#documentation) |
| 指示の配置変更 | [指示配置](rules/task-rules.md#placement) |
