# RenCrow ATLAS Backfill v1 復旧メモ

このパッケージは、前回生成した `RenCrow_ATLAS_Backfill_v1.zip` から、`RenCrow/tmp/atlas/` へ配置しやすい形へ復元したものです。

## 正本とfixture

- `atlas_backfill_v1.json`: canonical Backfill Dataset
- `atlas_backfill_v1_test_fixture.json`: test fixture。canonical Datasetとbyte-for-byte同一。production import sourceにしない。

## local Specification Artifact

`specifications/` 配下に8本あります。`specification_artifacts.json` の `content_path` / `content_sha256` と全件一致を確認済みです。

## external Specification

以下3件はこのpackageへ本文を複製しません。EcoSystem側の既存正本を参照します。

- `spec_l0v2_external` -> `docs/Backlog/ContextShadowRecall.md`
- `spec_memory_verification_external` -> `docs/Backlog/MemoryCheck.md`
- `spec_agent_subagent_external` -> `docs/Backlog/HarnesAgentSubagent.md`

## 配置

このdirectoryの中身を `RenCrow/tmp/atlas/` へコピーしてください。`specifications/` directory構造を保持してください。

既存の `atlas_backfill_v1.json` などがある場合は、上書き前にSHA-256を比較してください。
