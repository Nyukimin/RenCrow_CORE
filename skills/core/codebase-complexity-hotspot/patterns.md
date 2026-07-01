# Detection Patterns

## Nested Loop

- `for` の中の `for`
- `map` の中の `filter`
- `filter` の中の `find`
- `reduce` の中の lookup

## Repeated Lookup

- 同じ配列に対する `find` / `filter` / `some` の繰り返し
- ID lookup を毎回線形探索している処理

## Repeated Scan

- `filter().map()`
- `filter().reduce()`
- `map().filter().map()`

## N+1 Candidate

- loop 内の DB query
- loop 内の HTTP request
- async map で item ごとに fetch

## Render Hotspot

- component render 内の重い filter / sort / map
- render ごとの regex / date / large calculation
- 大きな list の全件描画

## Serialization / Regex

- loop 内の `JSON.parse` / `JSON.stringify`
- loop 内 regex compile
- 大きな文字列への複数 scan
