# Understand PicoClaw Graph

## Purpose

`picoclaw_multiLLM` に Understand Anything を適用し、`.understand-anything/knowledge-graph.json` と dashboard を更新・確認する。

## When to Use

- ユーザーが「understand を picoclaw_multiLLM に適用」「knowledge graph を更新」「architecture graph を見たい」と依頼した。
- 大規模調査の前に codebase graph を更新する。

## Required Context

- project root: `/home/nyukimi/RenCrow/picoclaw_multiLLM`
- graph symlink: `.understand-anything -> docs/understand-anything`
- output: `.understand-anything/knowledge-graph.json`
- config: `.understand-anything/config.json`

## Procedure

1. root `/home/nyukimi/RenCrow` ではなく project root へ移動する。
2. `.understand-anything/.understandignore` を確認する。
3. `@understand-anything/core` を build する。
4. `scan-project.mjs` で scan を更新する。
5. `extract-import-map.mjs` で import map を更新する。
6. `extract-structure.mjs` で structure を更新する。
7. `.understand-anything/build-deterministic-graph.mjs` で graph / meta を保存する。
8. node validation で dangling edge、layer / tour missing refs を確認する。
9. dashboard が必要なら token 付き URL を取得する。

## Verification

- `meta.json` の gitCommitHash が current HEAD。
- validation issue count が 0。
- analyzedFiles、nodes、edges、layers、tourSteps を報告する。
- dashboard URL は `?token=` 付きで共有する。

## Safety

- `.understand-anything` 生成物を commit 対象に含めるかは明示確認する。
- unrelated dirty files を混ぜない。
- graph が current HEAD と一致しない状態で完了扱いしない。

