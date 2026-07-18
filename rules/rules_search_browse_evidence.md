# Search / Browse Evidence Rule

## Purpose

外部情報調査では、検索とブラウジングを分離する。

調査結果を製品仕様へ採用する場合は、根拠を確認したうえで `docs/README.md` から該当する現行正本を更新する。調査メモを別の正本にしない。

## Core Rule

```text
Search = discovery only
Browse / Fetch = evidence
Browser = rendered / interactive verification
```

## Required

- 既知 URL がある場合、検索しない。
- 検索は URL 候補を見つける目的に限定する。
- 評価、要約、比較、仕様化、採用判断に使う URL は必ず開いて読む。
- 根拠にするのは `source_read` または `browser_evidence` のみとする。
- UI、JS rendering、login profile、screenshot、network trace が必要な場合は real browser / headless browser で確認する。
- 外部情報を使った最終報告では、`searched`、`read`、`browser verified`、`not verified` を必要に応じて区別する。

## Forbidden

- 検索結果 snippet を根拠にする。
- 検索結果一覧を読んだだけで source を読んだ扱いにする。
- ranking や title だけで採用 / 不採用判断をする。
- search result を Source Registry / Memory / Knowledge の promoted evidence にする。
- 開けなかった URL を根拠化する。

## Source Registry Boundary

```text
search_result:
  discovery cache only

source_read:
  staging candidate

browser_evidence:
  staging candidate / rendered evidence
```

Source Registry staging、Memory、Knowledge、Domain Graph promotion では、`source_read` または `browser_evidence` への参照を必須にする。
