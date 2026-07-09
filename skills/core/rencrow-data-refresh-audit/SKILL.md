# RenCrow Data Refresh Audit

## Purpose

`rencrow-data` の market data refresh、learning base、paper trade audit、Viewer investment status を、snapshot traceability と approval metadata を保って確認する。

## When to Use

- `docs/株式`、`rencrow-data`、投資研究基盤、market universe、paper trade、approval audit に関する作業。
- daily / weekly refresh、source expansion、active source filtering、Viewer investment status を確認する。

## Required Context

- repo root: `/home/nyukimi/RenCrow/RenCrow_CORE`
- data workflow: `rencrow-data/`
- key docs: `docs/株式/`

## Procedure

1. 対象仕様を `docs/株式/` と `rencrow-data/README.md` から確認する。
2. `make -n` で daily / weekly / manual-stop target の形を確認する。
3. universe 変更では config だけでなく fetch / feature logic の両方を確認する。
4. snapshot_id、approval_reason、paper_trade_log、cli_run_log、exit_code が保持されるか確認する。
5. Viewer status は active sources と audit history を分けて扱う。
6. retired source failure を live operational noise として表示しない。

## Verification

- refresh command の dry-run または実行結果が記録されている。
- snapshot_id から data、feature、paper trade、approval が追跡できる。
- CLI audit に exit_code が残る。
- Viewer status が live state と historical audit を混同しない。

## Safety

- 実投資判断として扱わない。
- paper operation の 8-12 週相当の経過要件を短縮完了扱いしない。
- approval metadata なしに paper trade を進めない。

