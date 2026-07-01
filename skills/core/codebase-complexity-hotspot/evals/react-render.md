# Eval: React Render

## Input

component render 内で大きな配列の `filter` / `sort` / `map` を実行している。

## Expected Behavior

- hotspot type を `render_hotspot` とする。
- memoization、selector cache、virtualized list を候補にする。
- 表示結果、ソート順、key、再レンダリング、スマホ表示の確認を要求する。
