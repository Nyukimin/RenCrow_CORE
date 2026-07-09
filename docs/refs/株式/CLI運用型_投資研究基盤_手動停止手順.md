# CLI運用型 投資研究基盤 手動停止手順

## 位置づけ

この手順は `CLI運用型_投資研究基盤_実装仕様書.md` の kill switch / 手動停止条件を運用するための手順である。

手動停止は売買判断ではなく、紙運用または将来の小額実運用へ進む前に、RenCrow側の週次decisionを停止状態へ倒すための監査イベントである。

## 手動停止の記録

```bash
cd picoclaw_multiLLM
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/15_manual_stop.py \
  --db rencrow-data/data/rencrow.db \
  --operator "operator-name" \
  --reason "manual stop reason" \
  --note "optional note"
```

Make経由:

```bash
make rencrow-data-manual-stop \
  DATA_STOP_OPERATOR="operator-name" \
  DATA_STOP_REASON="manual stop reason" \
  DATA_STOP_NOTE="optional note"
```

記録先:

- `event_log.level`: `kill`
- `event_log.reason`: `manual_kill_switch`
- `event_log.event_risk_score`: `1.0`
- `event_log.context_json`: operator、reason、note、recorded_at、recovery_rule

## 停止後の確認

```bash
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/10_risk_check.py \
  --db rencrow-data/data/rencrow.db \
  --snapshot latest \
  --strategy weekly_etf_rotation_v1 \
  --risk-config rencrow-data/config/risk_limits.yml \
  --json
```

期待結果:

- `status` は `kill_switch`
- `event_check` は `fail`
- 終了コードは `3`

この状態で `11_generate_decision.py` を実行しても、decision candidateは `vetoed=true` かつ `target_weight=0.0` になり、人間承認前で停止する。

## 解除ルール

- LLMは kill switch を解除できない。
- 手動停止イベントをDBから削除しない。
- 解除する場合は、別途人間レビュー後に `event_log.resolved_at` と `resolution_note` を残す。
- 解除後も、`10_risk_check.py` が `pass` になるまでは紙運用・小額実運用へ進まない。
- NISA資産の売買判断やrisk budgetには接続しない。

解除コマンド:

```bash
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/15_manual_stop.py \
  --db rencrow-data/data/rencrow.db \
  --operator "operator-name" \
  --reason "manual review cleared reason" \
  --resolve-event-id 123 \
  --note "optional note" \
  --json
```

解除時の記録:

- `event_log.resolved_at`: 解除時刻
- `event_log.resolution_note`: operator、reason、note、resolved_at、manual_resolution、recovery_rule
- `cli_run_log`: `15_manual_stop.py` の解除実行ログ

## 監査

週次監査:

```bash
PYTHONPATH=rencrow-data/src python3 rencrow-data/src/14_audit_report.py \
  --db rencrow-data/data/rencrow.db \
  --snapshot latest \
  --paper-latest
```

確認点:

- `Risk` セクションに `status: kill_switch` が残る。
- `Decision` セクションで `vetoed: True` が確認できる。
- `Paper Trades` セクションに実約定ではなく `vetoed` または `no_candidate` が残る。
