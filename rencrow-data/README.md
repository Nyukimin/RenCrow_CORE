# RenCrow Data Foundation

This directory contains the repository-local workflow for stock, ETF, and crypto
research data. It is an implementation runbook, not an additional product source of
truth. Runtime database placement follows
[the configuration reference](../docs/05_設定リファレンス.md), and any external
action follows [the automatic execution and safety policy](../docs/07_安全・自動実行・データ方針.md).

## Optional Python operation

This workflow is an optional data/operations capability. It is not part of the
standard Go runtime or standard installer, and CORE can run without Python when
this capability is not selected. A host that enables the scheduled data jobs
must provide the documented Python environment explicitly. Missing Python or a
disabled scheduler remains unavailable and does not trigger automatic runtime
installation. The canonical system-wide classification is
[the standard Go distribution boundary](../docs/04_アーキテクチャ概要.md#標準go配布境界).

MVP commands:

```bash
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/01_init_db.py
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/02_fetch_market.py
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/03_fetch_macro.py
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/04_build_features.py
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/05_detect_events.py
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/06_make_snapshot.py
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/08_validate_data.py --as-of latest
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/09_backtest_weekly_rotation.py --snapshot latest
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/10_risk_check.py --snapshot latest
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/11_generate_decision.py --snapshot latest
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/13_llm_report.py --snapshot latest --decision latest
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/14_audit_report.py --snapshot latest --decision latest
```

The same weekly research flow can be run through Make:

```bash
make rencrow-data-weekly-research
```

The audit report includes a paper-operation gate and a weekly ledger showing
which snapshot, validation, feature, backtest, risk, paper trade, and report logs
are present or missing for each paper decision.
With `--paper-latest`, it audits the latest paper-traded decision for the
snapshot before falling back to the latest decision candidate.

Paper trading uses the generated decision policy file and does not place broker orders:

```bash
make rencrow-data-paper-trade DATA_POLICY_FILE=rencrow-data/policies/latest.yml
```

Live broker orders are disabled in the initial MVP. The schema keeps `order_log`
as a future placeholder, but inserts are blocked until a separate live-trading
specification explicitly replaces that guard.

Generated databases, snapshots, backtest CSVs, policy files, and reports are
runtime artifacts. Keep them out of git unless a specific fixture is being added.

For historical backfill from online providers:

```bash
make rencrow-data-backfill
```

For persistent scheduled runs:

```bash
make install-data-scheduler enable-data-scheduler
make data-scheduler-status
```

The default config points to local fixture files. Real provider adapters should
keep secrets in `.env` or GitHub Actions Secrets, never in config files.
