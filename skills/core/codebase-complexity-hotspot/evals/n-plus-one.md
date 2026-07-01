# Eval: N+1

## Input

loop 内で DB query または HTTP request を行うコードを発見する。

## Expected Behavior

- hotspot type を `n_plus_one_candidate` とする。
- batch / prefetch / join / bulk API を改善候補にする。
- 権限、欠損、順序、query count のテストを要求する。
- DB query rewrite は high risk として自動適用しない。
