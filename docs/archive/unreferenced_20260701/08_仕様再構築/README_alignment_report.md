# README 参照整合チェック

## 結論

README v2 は、今回作成済みの仕様群と整合しています。
実運用では、旧版ではなく `README_architecture_v2.md` を正式版として採用するのが自然です。

## 参照確認

- README 内で参照されている `.md` ファイル数: 23
- 実在確認できた参照ファイル数: 23
- README が参照しているが未作成のファイル数: 0
- 今回の想定仕様群で未作成のファイル数: 0

README からの参照切れはありません。

## README v2 で参照している主な文書

- memory_architecture.md : OK
- source_preservation.md : OK
- memory_storage_schema.md : OK
- record_storage_schema.md : OK
- chat_spec.md : OK
- worker_spec.md : OK
- coder_spec.md : OK
- event_schema.md : OK
- hook_policy.md : OK
- commands.md : OK
- task_payloads.md : OK
- integration_contracts.md : OK
- runtime_state.md : OK
- session_lifecycle.md : OK
- failure_recovery.md : OK
- maintenance_jobs.md : OK
- observability.md : OK
- security_boundary.md : OK
- storage_layout.md : OK
- artifact_policy.md : OK
- routing_rules.md : OK
- repo_bootstrap.md : OK
- README_architecture_v2.md : OK

## 補足

- 旧版 `README_architecture.md` は索引として残っていますが、現在の設計重心とは少しずれます。
- 新しい入口としては `README_architecture_v2.md` を正式採用し、旧版はアーカイブ扱いにするのが自然です。
- 既存リンクの置換先としては、最終的に `README_architecture.md` を新版内容で差し替えるか、あるいは `README_architecture_v2.md` を正式名に昇格させる二択です。

## 推奨アクション

1. `README_architecture_v2.md` を正式版とする
2. 旧 `README_architecture.md` は `README_architecture_legacy.md` 相当の扱いにする
3. 参照先は今後 v2 ベースで更新する
