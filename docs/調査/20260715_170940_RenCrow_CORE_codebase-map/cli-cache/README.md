---
generated_at: "2026-07-15T17:09:40+09:00"
run_id: run_20260715_170940
phase: 0
step: "0d"
profile: RenCrow_CORE_20260715_refined
artifact: cli_cache_status
---

# CLI事前解析キャッシュ

## 概要

cloc、ctags、cscope は環境に存在しなかったためStep 0dをskippedとした。後続は Go toolchain、rg、find、wcへフォールバックした。

## 代替データ

- go env: go1.26.2
- go list ./...: 212 packages
- Go files: 1,490（testを含む）
- refined profile対象production source: 972 files
- Go関数・methodのrg概算: 9,981（testを含む）

## 関連ドキュメント

- ../全体概要_draft.md
- ../profile_refined.yaml
