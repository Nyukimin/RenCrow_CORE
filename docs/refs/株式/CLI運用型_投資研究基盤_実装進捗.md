# CLI運用型 投資研究基盤 実装進捗

## 位置づけ

この文書は `CLI運用型_投資研究基盤_実装仕様書.md` の実装状況を追跡するための進捗メモである。

## 実装済みCLI

| CLI | 状態 | 主な保存先 |
|---|---|---|
| `01_init_db.py` | 実装済み | schema, `strategy_version` |
| `02_fetch_market.py` | 実装済み | `price_raw`, `corporate_action`, `source_fetch_log` |
| `03_fetch_macro.py` | 実装済み | `macro_series`, `economic_calendar`, `source_fetch_log` |
| `04_build_features.py` | 実装済み | `feature_weekly` |
| `05_detect_events.py` | 実装済み | `event_log`, `feature_weekly.event_risk_score` |
| `06_make_snapshot.py` | 実装済み | `snapshot_registry`, snapshot gzip |
| `07_sync_universe.py` | 実装済み | `instruments`, config sync |
| `08_validate_data.py` | 実装済み | `data_quality_check` |
| `09_backtest_weekly_rotation.py` | 実装済み | `backtest_run`, `backtest_metric`, equity/trade CSV |
| `10_risk_check.py` | 実装済み | `risk_check_result` |
| `11_generate_decision.py` | 実装済み | `weekly_signal`, `decision_log`, approval YAML/JSON |
| `12_paper_trade.py` | 実装済み | `paper_trade_log`, `tax_lot_log` |
| `13_llm_report.py` | 実装済み | report Markdown, `llm_audit_log` |
| `14_audit_report.py` | 実装済み | audit Markdown |
| `15_manual_stop.py` | 実装済み | `event_log` |

## 仕様要件との対応

| 要件 | 状態 | 根拠 |
|---|---|---|
| CLIだけで週次研究フローを再現 | 実装済み | `test_weekly_cli_flow.py` |
| 仕様書記載CLI引数とJSON summary互換性 | 実装済み | `test_cli_contract.py` |
| CLI実行共通ログ | 実装済み | `cli_run_log`, `run_id`, `started_at`, `finished_at`, `test_cli_contract.py` |
| CLI実行ログのconfig hash | 実装済み | `config_hash_for_paths`, `cli_run_log.config_hash`, `test_config_hash_changes_when_config_file_changes` |
| CLI実行ログの終了コード保存 | 実装済み | `cli_run_log.exit_code`, `test_cli_contract.py` |
| validation blocker / risk stopのログ記録 | 実装済み | `cli_run_log.status`, `test_quality_validation.py`, `test_risk_check.py` |
| return outlier / adjustment anomalyのblocker検知 | 実装済み | `data_quality_check`, `test_quality_validation.py` |
| volume outlierのblocker検知 | 実装済み | `data_quality_check.volume_outlier`, `test_quality_validation.py` |
| CLI状態エラー時の失敗ログ記録 | 実装済み | `fail_cli_run`, `test_cli_run_log_closes_state_errors_as_failures` |
| `01_init_db.py --reset` の通常運用禁止と失敗ログ | 実装済み | `01_init_db.py`, `cli_run_log.status=fail`, `test_cli_contract.py` |
| `cd rencrow-data`後のデフォルトDB/出力パス | 実装済み | `resolve_repo_relative_path`, `test_default_paths_are_local_after_cd_into_data_dir` |
| 14.2記載のfixture直指定CLIと結合コマンド列 | 実装済み | `02_fetch_market.py --fixture`, `03_fetch_macro.py --fixture`, `test_spec_14_2_full_fixture_integration_flow` |
| fetch系CLIのpartial終了コード | 実装済み | `test_cli_contract.py` |
| データ利用条件のfetchログ保存 | 実装済み | `source_fetch_log.usage_terms`, `test_providers_backfill.py`, `test_pipeline_e2e.py` |
| usage terms未指定fetchの欠落明示 | 実装済み | `usage_terms_missing`, `test_init_db.py` |
| universe preset入力エラー時のfallback禁止 | 実装済み | `07_sync_universe.py`, `test_universe_sync.py` |
| Make経由の日次refreshフロー | 実装済み | `make rencrow-data-daily-refresh`, `test_cli_contract.py` |
| Make経由の週次研究フロー | 実装済み | `make rencrow-data-weekly-research` |
| systemd schedulerからの週次研究フローとpartial通知 | 実装済み | `scripts/rencrow_data_scheduler.sh weekly`, `bash -n scripts/rencrow_data_scheduler.sh` |
| `01` から `08` までのデータ基盤CLI | 実装済み | `test_pipeline_e2e.py`, `test_quality_validation.py` |
| `feature_weekly` の再現性と対象フィルタ | 実装済み | `test_market_features.py` |
| feature config hash保存 | 実装済み | `feature_weekly.feature_config_hash`, `04_build_features.py`, `test_market_features.py`, `test_cli_contract.py` |
| feature生成の利用可能日境界 | 実装済み | `04_build_features.py --week-end`, `test_cli_contract.py` |
| 4週momentum / volatility / drawdown / MA gap / volume change / macro / event flag | 実装済み | `feature_weekly`, `test_market_features.py` |
| 直近1週skipの12週モメンタム | 実装済み | `feature_weekly.ret_12w_skip1`, `test_market_features.py` |
| 26週モメンタム | 実装済み | `feature_weekly.ret_26w`, `test_market_features.py` |
| USD資産のJPY換算とFX release境界 | 実装済み | `close_adj_jpy`, `test_market_features.py` |
| event検知の対象週境界 | 実装済み | `05_detect_events.py --week-end`, `test_cli_contract.py` |
| snapshot hash保存 | 実装済み | `snapshot_registry`, `test_pipeline_e2e.py` |
| snapshot作成CLIのsnapshot_idログ | 実装済み | `06_make_snapshot.py`, `cli_run_log.snapshot_id`, `test_cli_contract.py` |
| feature/eventのsnapshot参照 | 実装済み | `feature_weekly.source_snapshot_id`, `event_log.snapshot_id`, `test_pipeline_e2e.py` |
| snapshot対象feature hash検証 | 実装済み | `stable_feature_hash`, `test_backtest_weekly_rotation.py`, `test_generate_decision.py`, `test_risk_check.py`, `test_paper_trade.py` |
| snapshot source summary詳細監査 | 実装済み | `snapshot_registry.source_summary_json`, `test_pipeline_e2e.py` |
| snapshot metadataの週次境界監査 | 実装済み | `source_summary_json.as_of`, `event_state_json.as_of`, `test_pipeline_e2e.py` |
| snapshot欠損率監査 | 実装済み | `snapshot_registry.missing_rate`, `test_pipeline_e2e.py` |
| snapshot precheckのDB実績source監査 | 実装済み | `precheck_status`, `test_pipeline_e2e.py` |
| 週次ETF回転backtest | 実装済み | `test_backtest_weekly_rotation.py` |
| 戦略実行の売買対象asset type境界と監査 | 実装済み | `tradable_asset_types`, `universe_assets`, `candidate_json.asset_type`, `test_backtest_weekly_rotation.py`, `test_generate_decision.py` |
| 税、手数料、スリッページ、1週ラグ | 実装済み | `backtest.py`, `test_backtest_weekly_rotation.py` |
| backtest cost config | 実装済み | `rencrow-data/config/costs.yml`, `09_backtest_weekly_rotation.py --cost-config`, `test_backtest_weekly_rotation.py` |
| backtest turnover時のcost/tax drag反映 | 実装済み | `cost_drag`, `tax_drag`, `test_backtest_weekly_rotation.py` |
| backtest equity CSVでの1週ラグ検証 | 実装済み | `symbol`, `signal_symbol`, `test_backtest_weekly_rotation.py` |
| backtestのsnapshot日上限と未来feature除外 | 実装済み | `snapshot_registry.snapshot_date`, `test_backtest_weekly_rotation.py` |
| `train`/`test`/`oos_YYYY` split metric | 実装済み | `--walk-forward`, `test_backtest_weekly_rotation.py` |
| Calmar、平均保有期間、worst month、recovery months | 実装済み | `backtest_metric`, `test_backtest_weekly_rotation.py` |
| 同一config hashのbacktest再現性 | 実装済み | `test_backtest_weekly_rotation.py` |
| 2008/2020/2022相当のstress期間split | 実装済み | `stress_2008`, `stress_2020`, `stress_2022`, `test_backtest_weekly_rotation.py` |
| event severity定義とNFP/雇用イベント対応 | 実装済み | `EVENT_SEVERITY_BY_IMPORTANCE`, `DEFAULT_EVENT_SEVERITY_BY_CATEGORY`, `test_market_features.py` |
| event state JSON出力 | 実装済み | `05_detect_events.py`, `event_state_summary`, `test_cli_contract.py` |
| event vetoをbacktestへ反映 | 実装済み | `event_vetoed`, `test_backtest_weekly_rotation.py` |
| event vetoをdecisionへ反映 | 実装済み | `veto_json`, `test_generate_decision.py` |
| risk checkのpass/reduce/stop/kill | 実装済み | `test_risk_check.py` |
| 週次損失・実現volatility閾値のrisk stop | 実装済み | `weekly_loss_check`, `volatility_check`, `test_risk_check.py` |
| event risk閾値によるrisk stop | 実装済み | `event_risk_stop_threshold`, `test_risk_check.py` |
| data quality partialによるrisk stop | 実装済み | `quality_partials`, `test_risk_check.py` |
| data quality warningによるrisk reduce | 実装済み | `quality_warnings`, `test_risk_check.py` |
| decision前のbacktest signal集中判定 | 実装済み | `backtest_run.result_json.latest_signal`, `planned_concentration`, `test_risk_check.py` |
| 単一銘柄集中によるrisk reduce | 実装済み | `single_symbol_concentration_limit`, `test_risk_check.py` |
| asset class集中によるrisk reduce | 実装済み | `asset_class_concentration_limit`, `test_risk_check.py` |
| 手動kill switch記録 | 実装済み | `15_manual_stop.py`, `test_risk_check.py` |
| 手動kill switch解除監査 | 実装済み | `15_manual_stop.py --resolve-event-id`, `event_log.resolved_at`, `test_risk_check.py` |
| 手動kill switch解除入力エラーの終了コード | 実装済み | `15_manual_stop.py`, `cli_run_log.detail_json.exit_code=4`, `test_risk_check.py` |
| データ復旧時のevent自動解除監査 | 実装済み | `event_log.resolution_note`, `test_market_features.py` |
| decision candidate生成 | 実装済み | `test_generate_decision.py` |
| 同一snapshotのdecision再生成と過去snapshot不変性 | 実装済み | `test_generate_decision.py` |
| decision再生成時のrisk checkリンク不変性 | 実装済み | `risk_check_result.decision_id`, `candidate_json.risk_check_id`, `test_generate_decision.py`, `test_audit_report.py` |
| LLM report失敗時のdecision不変性 | 実装済み | `test_generate_decision.py` |
| LLM report成功時のdecision/risk不変性 | 実装済み | `test_llm_report.py` |
| LLM reportの再生成decision risk追跡 | 実装済み | `candidate_json.risk_check_id`, `test_llm_report.py` |
| `--risk-check` 省略時の最新risk check解決 | 実装済み | `test_generate_decision.py`, `test_weekly_cli_flow.py` |
| human approval前提 | 実装済み | approval YAML/JSON, `test_generate_decision.py`, `test_paper_trade.py` |
| YAML承認案と `latest.yml` 生成 | 実装済み | `test_generate_decision.py` |
| YAML承認ファイル読込 | 実装済み | `test_paper_trade.py` |
| 承認者・承認時刻・承認理由の必須化 | 実装済み | `test_paper_trade.py` |
| 承認理由のdecision log保存 | 実装済み | `decision_log.approval_reason`, `test_paper_trade.py` |
| approval fileのdecision/snapshot/strategy/candidate照合 | 実装済み | `run_paper_trade`, `test_paper_trade.py` |
| 紙運用gateの承認証跡必須化 | 実装済み | `paper_gate.missing_approval_evidence_rows`, `test_audit_report.py` |
| 紙運用gateのdecision理由証跡必須化 | 実装済み | `paper_gate.missing_decision_evidence_rows`, `test_audit_report.py` |
| paper tradeのaccount scope境界 | 実装済み | `decision_log.account_scope=paper`, `test_paper_trade.py` |
| paper trade | 実装済み | `paper_trade_log`, `test_paper_trade.py` |
| paper tradeのsnapshot参照 | 実装済み | `paper_trade_log.snapshot_id`, `test_paper_trade.py` |
| paper trade成功時のCLIログ成功件数 | 実装済み | `12_paper_trade.py`, `cli_run_log.success_count`, `test_paper_trade.py` |
| 週次CLI flowからの承認・paper trade・audit | 実装済み | `test_weekly_cli_flow.py` |
| no tradeのpaper記録 | 実装済み | `paper_trade_log.status=vetoed`, `test_paper_trade.py` |
| TCA summaryとfill model保存 | 実装済み | `paper.py`, `test_paper_trade.py` |
| fill model別の約定価格仮定 | 実装済み | `open_next_session`, `vwap_approx`, `test_paper_trade.py` |
| 税ロット近似 | 実装済み | `tax_lot_log`, `test_paper_trade.py` |
| tax lotの課税口座限定 | 実装済み | `enforce_tax_lot_taxable_scope_*`, `test_init_db.py` |
| live order禁止 | 実装済み | live order CLIなし、`order_log` INSERT拒否trigger |
| LLMは説明補助のみ | 実装済み | `13_llm_report.py`, `llm_audit_log` |
| LLMが売買判断を変更しない | 実装済み | report-only CLI design |
| LLM audit必須フィールド保存 | 実装済み | `snapshot_id`, `model`, `prompt_version`, `input_hash`, `output_hash`, `output_path`, `test_llm_report.py` |
| LLM auditのdecision紐づき保存 | 実装済み | `llm_audit_log.decision_id`, `test_llm_report.py` |
| LLM監査のuncertainty flag保存 | 実装済み | `llm_audit_log.uncertainty_flag`, `test_llm_report.py` |
| LLM監査のspec_generation task保存 | 実装済み | `llm_audit_log.task_type`, `test_llm_report.py` |
| LLM reportのsnapshot/decision一致境界 | 実装済み | `13_llm_report.py --snapshot --decision`, `test_llm_report.py` |
| 監査レポートと紙約定一覧 | 実装済み | `14_audit_report.py`, `test_audit_report.py` |
| audit reportでのsnapshot metadata監査 | 実装済み | `source_summary_json`, `event_state_json`, `features_hash_match`, `test_audit_report.py` |
| audit report fetch品質のsnapshot境界 | 実装済み | `source_summary_json.latest_fetches`, `test_audit_report.py` |
| audit reportでのsnapshot fetch失敗/partial一覧 | 実装済み | `fetch_failures_total`, `fetch_partials_total`, `test_audit_report.py` |
| audit reportでの売買対象asset type監査 | 実装済み | decision signal table, paper trade table, `test_audit_report.py` |
| `--paper-latest` 監査対象解決 | 実装済み | paper trade済み最新decisionを優先 |
| 紙運用decision理由の追跡性 | 実装済み | `decision_evidence`, `weekly_signal.reason_json`, `test_audit_report.py` |
| 紙運用gateのdecision別report証跡 | 実装済み | `llm_audit_log.decision_id`, `test_audit_report.py` |
| 紙運用完了ゲート判定 | 実装済み | `paper_gate.status`, `gate_failures`, `test_audit_report.py` |
| 紙運用gateの再生成decision risk追跡 | 実装済み | `candidate_json.risk_check_id`, `_risk_for_decision`, `test_audit_report.py` |
| 紙運用8週/12週の期間幅判定 | 実装済み | `paper_span_days`, `gate_failures`, `test_audit_report.py` |
| 紙運用12週preferred ready判定 | 実装済み | `paper_gate.status=preferred_ready`, `test_audit_report.py` |
| 紙運用ログ欠落チェック | 実装済み | fetch, snapshot, validation, feature, backtest, risk, paper trade, report |
| no trade / event veto証跡のready gate反映 | 実装済み | `missing_no_trade_evidence`, `missing_event_veto_evidence`, `test_audit_report.py` |
| 税・コスト込みbacktest成績のready gate反映 | 実装済み | `performance_failure_rows`, `missing_or_degraded_tax_cost_performance`, `test_audit_report.py` |
| 仮想約定TCA証跡のready gate反映 | 実装済み | `tca_rows`, `missing_tca_evidence_rows`, `test_audit_report.py` |
| 仮想約定TCA明細の永続化 | 実装済み | `paper_trade_log.target_weight/notional/estimated_cost/slippage`, `test_paper_trade.py`, `test_audit_report.py` |
| 紙運用週次台帳 | 実装済み | `paper_gate.weeks`, `test_audit_report.py` |
| calendar event検知の冪等性 | 実装済み | `event_log.context_json`, `test_market_features.py` |
| kill switchと手動停止手順 | 実装済み | `CLI運用型_投資研究基盤_手動停止手順.md` |

## 現在の未完了・運用待ち

| 項目 | 状態 | 理由 |
|---|---|---|
| 8から12週間の紙運用継続 | 未完了 | 実期間の継続運用が必要 |
| 小額実運用仕様 | 未着手 | 紙運用完了後に別仕様化する |
| ライブ発注CLI | 対象外 | 初期MVPでは作らない |
| 証券会社API接続 | 対象外 | 初期MVPでは作らない |

## 現時点の検証コマンド

```bash
uv run --with pytest --with requests python -m pytest rencrow-data/tests
```

直近確認結果:

```text
107 passed in 82.61s (0:01:22)
```

追加確認:

```bash
git diff --check
bash -n scripts/rencrow_data_scheduler.sh
make -n rencrow-data-daily-refresh SNAPSHOT_DATE=today
make -n rencrow-data-weekly-research
make -n rencrow-data-manual-stop
make -n install-data-scheduler
```

直近確認結果:

```text
all passed
```
