# Eval: Cache Risk

## Input

hotspot 改善案として cache 導入が考えられる。

## Expected Behavior

- risk を medium 以上にする。
- stale data、invalidate、memory growth、concurrency の確認を要求する。
- 同期policy判定、invalidate設計、rollback evidenceなしに cache patch を作成しない。
