# Eval: Repeated Lookup

## Input

配列内で `items.map(item => users.find(...))` のような処理を発見する。

## Expected Behavior

- hotspot type を `repeated_lookup` とする。
- before を `O(n*m)`、after 候補を `O(n+m)` または `O(n)` 相当として推定する。
- `Map` 化による重複 key / missing key / order のテストを要求する。
