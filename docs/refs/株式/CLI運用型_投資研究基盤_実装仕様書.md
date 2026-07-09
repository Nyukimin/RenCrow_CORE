# CLI運用型 投資研究基盤 実装仕様書

## 0. 位置づけ

この仕様書は、`CLI運用型_投資研究基盤_仕様化プロンプト.md` に基づき、RenCrow / RenCrow_CORE の株式・ETF・暗号資産データ基盤を、CLI主体の投資研究・紙運用基盤として実装するための仕様である。

優先する source of truth は次の順序とする。

1. `docs/株式/株式_アルゴリズム評価.md`
2. `docs/株式/学習基盤_実装仕様書.md`
3. `docs/株式/株式_学習基盤.md`
4. `docs/株式/01_クオンツ戦略アーキテクト.md` から `15_規制コンプライアンス枠組み.md` までの関連文書
5. `rencrow-data/` 配下の現在の実装、DBスキーマ、CLI、テスト

本仕様は「100万円の余剰資金をできるだけ早く200万円へ近づけたい」という目的を扱う。ただし、仕様上は最短倍増だけを直接最大化しない。目的関数は、最大ドローダウン、税、手数料、スプレッド、スリッページ、イベントリスクを反映した税後複利成長とする。

## 1. 目的と非目的

### 1.1 目的

- CLIだけで、データ取得、品質検証、特徴量生成、スナップショット作成、バックテスト、リスク判定、紙運用、週次レポート生成を再現できるようにする。
- 個別株、ETF、指数、FX、金利、マクロ、暗号資産、商品ETFを幅広く取得し、学習、比較、レジーム判定、特徴量生成に利用する。
- 初期の実運用候補は、高流動性ETFを中心にする。
- NISAの長期非課税コア資産と、RenCrowの課税口座向け戦術オーバーレイを完全に分離する。
- その週に見えていたデータだけで判断できるよう、snapshot hash、取得ログ、feature版、decision logを保存する。
- LLMは説明、要約、異常整理、イベント解釈、仕様書生成、週次レポート補助に限定し、売買判断の直接主体にしない。
- ライブ売買前に、データ基盤、バックテスト、リスク管理、イベントveto、紙運用の順で完成させる。

### 1.2 非目的

- 初期MVPでライブ発注APIを実装しない。
- LLMに直接売買判断や発注判断を委任しない。
- NISA口座の銘柄入替や売買判断をRenCrowの対象にしない。
- マーケットメイキング、高頻度売買、板データ前提戦略、空売り必須の統計的アービトラージを初期実運用に含めない。
- in-sample成績だけで戦略採用しない。
- 取得失敗、partial、stale、欠損、補正異常をsuccessに丸めない。
- 100万円を200万円へ近づけるために、ドローダウン制限、集中制限、イベント停止条件を外さない。

## 2. 運用前提

### 2.1 口座と資金

- 対象資金は余剰資金100万円を前提にする。
- NISAは長期投信の非課税コア資産として独立管理する。
- RenCrowは課税口座の戦術オーバーレイとして扱う。
- 目標は100万円を200万円へ近づけることだが、税後では課税分を考慮する。簡易前提では、100万円の利益に約20.315%課税されるため、税後200万円にするには税前で約225万円程度が必要になる。
- 実資金投入は、紙運用の完了条件を満たした後、人間承認を必須にする。

### 2.2 対象資産

| 区分 | 用途 | 初期運用での扱い |
|---|---|---|
| ETF | 実運用候補、バックテスト、特徴量 | 初期の主対象 |
| STOCK | 学習、比較、特徴量、相場温度 | 初期実運用では原則対象外 |
| INDEX | レジーム判定、ベンチマーク | 売買対象外 |
| FX | 円換算、為替レジーム | 売買対象外 |
| RATE | 金利レジーム、債券ETF評価 | 売買対象外 |
| MACRO | イベント、景気、インフレ判定 | 売買対象外 |
| CRYPTO | 学習、リスク比較、レジーム補助 | 初期実運用では対象外 |
| CASH_PROXY | 退避先、veto時の待機資産 | 実運用候補 |

### 2.3 初期の運用ユニバース

初期ライブ候補は、流動性、データ品質、税務・運用の単純さを優先し、ETF中心にする。

最小ベース戦略の候補ユニバース:

- `SPY`: 米国株式
- `IEF`: 米国中期債
- `TLT`: 米国長期債
- `GLD`: 金
- `SHY`: cash-like / 短期債退避

日本円口座での実運用に移す場合は、同等の国内ETF、為替影響、売買単位、スプレッド、税、取引手数料を別途評価する。

### 2.4 実行環境

- 正本DBは `RenCrow_CORE/rencrow-data/data/rencrow.db` のSQLiteとする。
- CLIは `RenCrow_CORE/rencrow-data/src/` 配下から実行する。
- 設定は `RenCrow_CORE/rencrow-data/config/` 配下のYAMLを正本にする。
- DB、snapshot、logs、raw cacheはgit管理しない。
- バックアップはsnapshot単位で保存し、同一週の判断を再現できる状態を維持する。

## 3. CLI一覧

### 3.1 既存CLI

| CLI | 役割 | 状態 |
|---|---|---|
| `01_init_db.py` | SQLiteスキーマ作成、初期ユニバース投入 | 実装済み |
| `02_fetch_market.py` | 市場価格、出来高、配当、分割の取得 | 実装済み |
| `03_fetch_macro.py` | マクロ、イベント、金利等の取得 | 実装済み |
| `04_build_features.py` | 週次特徴量生成 | 実装済み |
| `05_detect_events.py` | イベント検知、event_log生成 | 実装済み |
| `06_make_snapshot.py` | snapshot生成、hash登録 | 実装済み |
| `07_sync_universe.py` | 銘柄ユニバース拡張、同期、任意fetch/features実行 | 実装済み |

### 3.2 追加CLI

| CLI | 役割 | MVP優先度 |
|---|---|---|
| `08_validate_data.py` | 欠損、stale、異常リターン、取得失敗集計 | 高 |
| `09_backtest_weekly_rotation.py` | 週次ETF回転戦略のバックテスト | 高 |
| `10_risk_check.py` | DD、損失、集中、ボラ、イベント時サイズ判定 | 高 |
| `11_generate_decision.py` | 週次decision candidate生成 | 高 |
| `12_paper_trade.py` | 紙運用の仮想約定、TCA、税ロット近似 | 中 |
| `13_llm_report.py` | LLMによる週次説明、異常要約、イベント要約 | 中 |
| `14_audit_report.py` | snapshot、fetch、decision、paper trade監査レポート | 中 |

### 3.3 標準実行フロー

日次:

```bash
cd RenCrow_CORE/rencrow-data
python src/02_fetch_market.py --incremental
python src/03_fetch_macro.py --incremental
python src/08_validate_data.py --as-of today
```

週次:

```bash
cd RenCrow_CORE/rencrow-data
python src/07_sync_universe.py --preset cascade --loop
python src/04_build_features.py --week-end latest
python src/05_detect_events.py --week-end latest
python src/06_make_snapshot.py --week-end latest
python src/09_backtest_weekly_rotation.py --snapshot latest
python src/10_risk_check.py --snapshot latest --strategy weekly_etf_rotation_v1
python src/11_generate_decision.py --snapshot latest --strategy weekly_etf_rotation_v1
python src/13_llm_report.py --snapshot latest --decision latest
```

紙運用:

```bash
cd RenCrow_CORE/rencrow-data
python src/12_paper_trade.py --decision latest --approval-file approvals/latest.yml
python src/14_audit_report.py --snapshot latest --paper-latest
```

## 4. データフロー

### 4.1 レイヤ

```text
source -> raw -> clean/validated -> adjusted -> feature_weekly -> snapshot -> backtest/risk -> decision -> paper_trade -> report
```

| レイヤ | 内容 | 保存先 |
|---|---|---|
| source | yfinance、FRED、BOJ、e-Stat、イベントカレンダー等 | `source_fetch_log` |
| raw | 取得値そのもの、調整前価格、出来高、配当、分割 | `price_raw`, `macro_series`, `economic_calendar` |
| validated | 欠損、stale、異常値、partial判定 | `data_quality_check` |
| adjusted | 分割、配当、為替換算を考慮した比較用系列 | 再生成可能。必要に応じ派生テーブル |
| feature | 週次リターン、モメンタム、ボラ、DD、MA乖離 | `feature_weekly` |
| event | FOMC、CPI、BOJ、NFP、急変、取得異常 | `event_log` |
| snapshot | その週に見えていたデータ一式 | `snapshot_registry`, `data/snapshots/` |
| backtest | 税前、税後、コスト後の戦略評価 | `backtest_run`, `backtest_metric` |
| risk | 売買候補に対する制約判定 | `risk_check_result` |
| decision | 人間承認前の候補 | `decision_log` |
| paper | 仮想約定、保有、TCA、税ロット近似 | `paper_trade_log`, `tax_lot_log` |
| report | LLM補助レポート、監査レポート | `llm_audit_log`, ファイル出力 |

### 4.2 再現性ルール

- feature、event、decision、paper tradeは必ず `snapshot_id` を参照する。
- 週次判断は `week_end` 時点で取得済みのデータだけを使う。
- 当日終値確定前の価格を週次判断に使わない。
- 取得失敗、partial、staleがある場合は、該当銘柄を候補から除外するか、全体をno tradeに倒す。
- snapshot作成後にrawデータが増えても、過去snapshotの判断を上書きしない。

## 5. DBスキーマ追加・変更案

### 5.1 既存テーブルの扱い

既存実装のテーブルは維持する。

- `instruments`
- `source_fetch_log`
- `price_raw`
- `corporate_action`
- `macro_series`
- `economic_calendar`
- `etf_holding_snapshot`
- `feature_weekly`
- `event_log`
- `snapshot_registry`
- `decision_log`
- `paper_trade_log`
- `order_log`
- `tax_lot_log`

既存テーブルの破壊的変更は避ける。追加カラムが必要な場合はmigrationで対応する。

### 5.2 `data_quality_check`

データ品質検証結果を保存する。

| カラム | 型 | 内容 |
|---|---|---|
| check_id | INTEGER PK | 検査ID |
| run_id | TEXT | CLI実行ID |
| instrument_id | INTEGER | 対象銘柄 |
| check_date | TEXT | 検査基準日 |
| check_type | TEXT | `missing`, `stale`, `return_outlier`, `volume_outlier`, `adjustment_anomaly` |
| severity | TEXT | `info`, `warning`, `blocker` |
| status | TEXT | `pass`, `fail`, `partial` |
| metric_value | REAL | 欠損率、異常値など |
| detail_json | TEXT | 詳細 |
| created_at | TEXT | 作成時刻 |

### 5.3 `strategy_version`

戦略定義を固定する。

| カラム | 型 | 内容 |
|---|---|---|
| strategy_id | TEXT PK | `weekly_etf_rotation_v1` |
| strategy_name | TEXT | 表示名 |
| version | TEXT | バージョン |
| config_hash | TEXT | 戦略設定hash |
| config_json | TEXT | 戦略設定 |
| active | INTEGER | 有効フラグ |
| created_at | TEXT | 作成時刻 |

### 5.4 `backtest_run`

バックテスト実行単位を保存する。

| カラム | 型 | 内容 |
|---|---|---|
| backtest_id | TEXT PK | 実行ID |
| strategy_id | TEXT | 戦略ID |
| snapshot_id | TEXT | 使用snapshot |
| start_date | TEXT | 開始日 |
| end_date | TEXT | 終了日 |
| mode | TEXT | `insample`, `oos`, `walk_forward` |
| cost_bps | REAL | 売買コスト |
| slippage_bps | REAL | スリッページ |
| tax_mode | TEXT | `none`, `approx_jp_taxable` |
| status | TEXT | `success`, `partial`, `fail` |
| result_json | TEXT | 集計結果 |
| created_at | TEXT | 作成時刻 |

### 5.5 `backtest_metric`

バックテスト指標を正規化して保存する。

| カラム | 型 | 内容 |
|---|---|---|
| metric_id | INTEGER PK | 指標ID |
| backtest_id | TEXT | 実行ID |
| split_name | TEXT | `full`, `train`, `test`, `oos_YYYY` |
| metric_name | TEXT | `cagr`, `sharpe`, `sortino`, `max_dd`, `turnover`, `tax_drag` |
| metric_value | REAL | 値 |
| created_at | TEXT | 作成時刻 |

### 5.6 `weekly_signal`

週次戦略の候補シグナルを保存する。

| カラム | 型 | 内容 |
|---|---|---|
| signal_id | TEXT PK | シグナルID |
| snapshot_id | TEXT | 使用snapshot |
| strategy_id | TEXT | 戦略ID |
| week_end | TEXT | 週末日 |
| instrument_id | INTEGER | 候補銘柄 |
| rank | INTEGER | 順位 |
| target_weight | REAL | リスク判定前の目標比率 |
| raw_score | REAL | 生スコア |
| adjusted_score | REAL | ボラ・イベント調整後スコア |
| vetoed | INTEGER | veto有無 |
| reason_json | TEXT | 理由 |
| created_at | TEXT | 作成時刻 |

### 5.7 `risk_check_result`

売買候補のリスク判定を保存する。

| カラム | 型 | 内容 |
|---|---|---|
| risk_check_id | TEXT PK | 判定ID |
| snapshot_id | TEXT | 使用snapshot |
| strategy_id | TEXT | 戦略ID |
| decision_id | TEXT | 対応decision |
| status | TEXT | `pass`, `reduce`, `stop`, `kill_switch` |
| max_dd_check | TEXT | `pass/fail` |
| weekly_loss_check | TEXT | `pass/fail` |
| concentration_check | TEXT | `pass/fail` |
| volatility_check | TEXT | `pass/fail` |
| event_check | TEXT | `pass/fail` |
| detail_json | TEXT | 詳細 |
| created_at | TEXT | 作成時刻 |

### 5.8 `llm_audit_log`

LLM利用の監査ログを保存する。

| カラム | 型 | 内容 |
|---|---|---|
| llm_log_id | TEXT PK | ログID |
| snapshot_id | TEXT | 参照snapshot |
| task_type | TEXT | `weekly_report`, `anomaly_summary`, `event_summary`, `spec_generation` |
| model | TEXT | モデル名 |
| prompt_version | TEXT | プロンプト版 |
| input_hash | TEXT | 入力hash |
| output_hash | TEXT | 出力hash |
| output_path | TEXT | レポート保存先 |
| uncertainty_flag | INTEGER | 不確実性フラグ |
| created_at | TEXT | 作成時刻 |

## 6. 各CLIの入出力、終了コード、ログ仕様

### 6.1 共通ルール

全CLIは次を満たす。

- `--db-path` を受け取れる。
- `--as-of` または `--week-end` を受け取れる。
- 実行開始、終了、対象件数、成功件数、失敗件数、partial件数を標準出力とログに出す。
- JSON出力が必要な場合は `--json` を持つ。
- 失敗をsuccessに丸めない。
- 既存データを上書きする場合は、上書きではなくupsertまたは新しいrun_idで記録する。

標準終了コード:

| code | 意味 |
|---|---|
| 0 | 成功 |
| 1 | 致命的失敗 |
| 2 | partialまたはstale。研究は可能だが売買候補は停止 |
| 3 | validation blocker。no trade |
| 4 | 入力、設定、引数エラー |

ログ共通項目:

- `run_id`
- `cli_name`
- `started_at`
- `finished_at`
- `status`
- `target_count`
- `success_count`
- `partial_count`
- `fail_count`
- `db_path`
- `snapshot_id`
- `config_hash`

### 6.2 `01_init_db.py`

入力:

- `--db-path`
- `--config-dir`
- `--reset` は開発時のみ。通常運用では禁止。

出力:

- 作成テーブル一覧
- 初期投入銘柄数
- schema version

終了コード:

- 0: 作成または既存DB確認成功
- 4: config不備
- 1: DB作成失敗

### 6.3 `02_fetch_market.py`

入力:

- `--symbols`
- `--asset-types`
- `--start`
- `--end`
- `--incremental`
- `--provider`
- `--db-path`

出力:

- `source_fetch_log`
- `price_raw`
- `corporate_action`
- fetch summary JSON

停止条件:

- provider障害が全体に波及した場合はcode 1
- 一部銘柄のみ失敗の場合はcode 2
- 売買候補銘柄にstaleがある場合はcode 2

### 6.4 `03_fetch_macro.py`

入力:

- `--series`
- `--start`
- `--end`
- `--incremental`
- `--provider`
- `--db-path`

出力:

- `macro_series`
- `economic_calendar`
- `source_fetch_log`

注意:

- マクロ系列は改定があり得る。将来はvintageを保存する。
- イベント予定は、攻めの材料ではなくveto用に扱う。

### 6.5 `04_build_features.py`

入力:

- `--week-end`
- `--symbols`
- `--asset-types`
- `--db-path`

出力:

- `feature_weekly`
- feature summary JSON

生成特徴量:

- 週次リターン
- 4週、12週、26週モメンタム
- 直近1週skipの12週モメンタム
- realized volatility
- drawdown
- moving average gap
- volume change
- USD建て資産の円換算特徴
- マクロ特徴
- イベントフラグ

### 6.6 `05_detect_events.py`

入力:

- `--week-end`
- `--lookback-days`
- `--lookahead-days`
- `--db-path`

出力:

- `event_log`
- event state JSON

イベント種別:

- `scheduled_macro`
- `central_bank`
- `inflation`
- `employment`
- `market_stress`
- `data_quality`
- `provider_failure`

### 6.7 `06_make_snapshot.py`

入力:

- `--week-end`
- `--db-path`
- `--snapshot-dir`

出力:

- `data/snapshots/snapshot_YYYYMMDD.sqlite.gz`
- `snapshot_registry`
- `db_hash`
- `features_hash`
- `source_summary_json`
- `event_state_json`

### 6.8 `07_sync_universe.py`

入力:

- `--preset broad_v2|broad_v3|broad_v4|broad_v5|cascade`
- `--loop`
- `--no-fetch`
- `--no-features`
- `--db-path`

出力:

- `instruments`
- 必要に応じて `price_raw`, `feature_weekly`
- sync summary JSON

用途:

- 学習対象を広げる。
- 新しい資産クラスを取り込む。
- ただし実運用候補は別途流動性とリスクで絞る。

### 6.9 `08_validate_data.py`

入力:

- `--as-of`
- `--symbols`
- `--asset-types`
- `--min-history-days`
- `--max-missing-rate`
- `--db-path`

出力:

- `data_quality_check`
- validation summary JSON

blocker条件:

- 実運用候補ETFの直近価格がstale
- 欠損率が閾値超過
- 調整後価格と未調整価格の比率が急変し、corporate actionで説明できない
- 週次リターンが極端で、分割・配当で説明できない
- cash proxyの価格系列が欠損

### 6.10 `09_backtest_weekly_rotation.py`

入力:

- `--snapshot`
- `--strategy`
- `--start`
- `--end`
- `--cost-bps`
- `--slippage-bps`
- `--tax-mode`
- `--walk-forward`
- `--db-path`

出力:

- `backtest_run`
- `backtest_metric`
- equity curve CSV
- trade list CSV

終了条件:

- strategy configが存在しない場合はcode 4
- snapshotが存在しない場合はcode 4
- 評価期間のデータ不足はcode 3

### 6.11 `10_risk_check.py`

入力:

- `--snapshot`
- `--strategy`
- `--decision`
- `--risk-config`
- `--db-path`

出力:

- `risk_check_result`
- adjusted target weight JSON

判定:

- `pass`: 候補を維持
- `reduce`: サイズ縮小
- `stop`: 新規建て停止
- `kill_switch`: 全停止、cash proxyへ退避候補

### 6.12 `11_generate_decision.py`

入力:

- `--snapshot`
- `--strategy`
- `--risk-check`
- `--db-path`

出力:

- `weekly_signal`
- `decision_log`
- human approval用YAML案

注意:

- decisionは発注ではない。
- `approval_required=true` を必須にする。
- LLM出力をdecisionの直接入力にしない。

### 6.13 `12_paper_trade.py`

入力:

- `--decision`
- `--approval-file`
- `--fill-model close_next_week|open_next_session|vwap_approx`
- `--db-path`

出力:

- `paper_trade_log`
- `tax_lot_log`
- TCA summary

ルール:

- 人間承認ファイルがない場合は実行しない。
- 約定価格仮定を必ず保存する。
- 実資金の注文は出さない。

### 6.14 `13_llm_report.py`

入力:

- `--snapshot`
- `--decision`
- `--task weekly_report|anomaly_summary|event_summary`
- `--model`
- `--prompt-version`

出力:

- Markdownレポート
- `llm_audit_log`

禁止:

- LLMがtarget weightを決める。
- LLMが銘柄を直接買い推奨に変える。
- LLMの判断でrisk checkを上書きする。

### 6.15 `14_audit_report.py`

入力:

- `--snapshot`
- `--decision`
- `--paper-latest`
- `--db-path`

出力:

- 監査Markdown
- fetch品質、event状態、risk判定、decision理由、paper約定の一覧

## 7. LLM利用箇所と禁止箇所

### 7.1 利用してよい箇所

- 仕様書、実装計画、運用手順の生成
- fetch失敗、partial、stale、欠損、異常リターンの要約
- FOMC、CPI、BOJ、NFPなどのイベント説明
- 週次レポートの文章化
- バックテスト結果の読みやすい説明
- 人間が確認すべき論点の列挙

### 7.2 禁止箇所

- 売買銘柄の最終決定
- target weightの決定
- risk checkの上書き
- event vetoの解除
- kill switchの解除
- 人間承認なしのpaper trade確定
- ライブ発注
- NISA資産への売買指示

### 7.3 LLM監査

LLMを使うCLIは、必ず次を保存する。

- `snapshot_id`
- model
- prompt version
- input hash
- output hash
- output path
- uncertainty flag

LLMが「不確実」「矛盾」「外部確認が必要」と判断した場合、売買系CLIはno tradeまたは人間確認へ倒す。

## 8. ベース戦略仕様

### 8.1 戦略名

`weekly_etf_rotation_v1`

### 8.2 目的

週次の中期モメンタムとボラティリティ制約を使い、高流動性ETFの中から相対的に有利な資産を選ぶ。イベント週やデータ品質不備がある場合は、cash-like ETFへ退避するか新規建てを停止する。

### 8.3 入力

- `feature_weekly`
- `event_log`
- `data_quality_check`
- `snapshot_registry`
- `risk_limits.yml`
- `strategy_version.config_json`

### 8.4 スコア

基本スコア:

```text
score = momentum_12w_skip1 - volatility_penalty - drawdown_penalty
```

初期値:

- momentum: 直近1週を除いた12週リターン
- volatility: 12週または26週realized volatility
- drawdown: 直近高値からの下落率
- cash proxy: 危険資産が全て閾値未満、またはevent veto時に候補

### 8.5 売買頻度

- 原則週1回。
- 週末データ確定後に計算し、翌週執行仮定でバックテストする。
- 1週ラグを必須にする。

### 8.6 制約

- 初期はロングのみ。
- レバレッジなし。
- ショートなし。
- 同時保有数は1から3銘柄までを検証対象にする。
- 単一資産クラス集中を制限する。
- event veto時は新規建て停止またはcash proxyへ退避する。

## 9. バックテスト仕様

### 9.1 必須条件

- 先読みバイアスを許さない。
- 週次判断時点で見えていないデータを使わない。
- 売買は1週ラグで約定する。
- 配当、分割、調整後価格の扱いを明示する。
- 税、手数料、スプレッド、スリッページを評価に含める。
- OOS、walk-forward、期間分割を含める。

### 9.2 指標

- CAGR
- annualized volatility
- Sharpe
- Sortino
- MaxDD
- Calmar
- hit rate
- turnover
- exposure
- average holding period
- cost drag
- tax drag
- worst week
- worst month
- recovery months

### 9.3 採用基準

初期採用には、少なくとも次を満たす。

- OOSで極端に崩れない。
- MaxDDがrisk configの上限以下。
- turnoverが税と手数料に対して過剰でない。
- cash proxy退避が正しく機能する。
- event vetoあり/なしの差分を説明できる。
- 2008年、2020年、2022年相当のストレス期間で破綻しない。

### 9.4 不採用条件

- in-sampleだけ良い。
- 少数イベントに成績が依存する。
- 取引回数が多すぎ、税後で優位性が消える。
- 特定銘柄のデータ異常で成績が作られている。
- cash proxyやvetoなしでしか成績が出ない。

## 10. リスク管理仕様

### 10.1 リスク制約

| 制約 | 初期仕様 |
|---|---|
| 最大DD | 戦略単位、口座単位で閾値管理 |
| 週次損失 | 閾値超過で新規建て停止 |
| 単一銘柄集中 | 100%集中は検証対象に残すが、実運用では上限を設定 |
| asset class集中 | 株式、債券、商品、cash proxyの偏りを制限 |
| ボラ目標 | 実現ボラが閾値超過ならサイズ縮小 |
| イベント週 | サイズ縮小または新規建て停止 |
| データ品質 | blockerならno trade |
| kill switch | DD、データ障害、イベント、手動停止で全停止 |

### 10.2 ステータス

- `pass`: 売買候補を維持
- `reduce`: target weightを縮小
- `stop`: 新規建て停止
- `kill_switch`: 全停止

### 10.3 100万円運用での注意

- 小口資金では税、スプレッド、取引単位の影響が大きい。
- 過剰回転は税後複利を損なう。
- 短期で200万円を狙うほど、破綻確率が上がる。仕様ではその破綻確率を制約で抑える。
- 100万円全額を単一高ボラ資産へ集中する仕様は、研究対象にはできても初期運用仕様にはしない。

## 11. イベントveto仕様

### 11.1 方針

イベント検知は攻めのシグナルではなく、停止、縮小、退避のvetoとして扱う。

### 11.2 対象イベント

- FOMC
- 米CPI
- 米雇用統計
- BOJ金融政策決定会合
- 主要国の急な金融政策変更
- 大幅な金利急変
- 株式指数の急落
- 為替の急変
- 主要provider障害
- 主要銘柄のstaleまたは異常値

### 11.3 判定

| severity | 動作 |
|---|---|
| info | レポートに記録 |
| warning | サイズ縮小候補 |
| blocker | 新規建て停止 |
| kill | kill switch |

### 11.4 解除

- LLMはvetoを解除できない。
- 自動解除は、イベント期間終了、データ復旧、risk check passを満たした場合のみ。
- 手動解除する場合は、承認者、理由、時刻をdecision logまたはaudit logに残す。

## 12. 紙運用仕様

### 12.1 目的

紙運用は、バックテストで見えない運用上の問題を確認するために行う。対象は、データ取得、週次判断、risk check、decision候補、人間承認、仮想約定、TCA、税ロット近似、レポート生成である。

### 12.2 期間

- 最低8週間。
- 望ましくは12週間。
- イベント週を少なくとも1回含むことが望ましい。

### 12.3 手順

1. 週次snapshotを作成する。
2. backtestを更新する。
3. risk checkを実行する。
4. decision candidateを生成する。
5. LLMレポートで説明を作る。
6. 人間がapproval YAMLを作成する。
7. paper tradeを記録する。
8. 翌週TCAと差分を監査する。

### 12.4 完了ゲート

紙運用から小額実運用へ進む条件:

- 8から12週間、CLIが再現可能に動く。
- snapshot、decision、paper trade、LLM auditが欠けない。
- blocker時にno tradeへ倒れる。
- event vetoが1回以上正常に動く、またはテストで再現済み。
- 税、手数料、スリッページ込みの成績が期待範囲内。
- 人間が各週のdecision理由を後から追える。

## 13. コンプライアンス・NISA分離仕様

### 13.1 NISA分離

- NISA資産はRenCrowの売買対象にしない。
- NISA銘柄、残高、積立設定をRenCrowのdecisionに混ぜない。
- RenCrowのレポートでNISAに触れる場合は、長期コア資産としての参考表示に限定する。
- NISAの含み益、損益、非課税枠をRenCrowのrisk budgetに含めない。

### 13.2 課税口座

- `tax_lot_log` は課税口座のみを対象にする。
- 税額は初期MVPでは近似でよいが、税前成績だけで採用判断しない。
- 実運用前に、売買手数料、為替手数料、スプレッド、約定単位を設定する。

### 13.3 情報利用

- 無料データ、ニュース本文、指数情報、issuer holdingsを第三者へ再配布しない。
- RenCrowから外部へシグナル配信しない。
- データ取得元、取得時刻、利用条件をログに残す。

### 13.4 人間承認

- 初期MVPでは、paper tradeもlive tradeも人間承認を必須にする。
- 承認ファイルには、承認者、承認時刻、対象decision、承認/却下、理由を含める。
- live orderは初期MVP対象外とする。

## 14. テスト仕様

### 14.1 単体テスト

- DB初期化
- instruments upsert
- market fetchの成功、partial、fail
- macro fetchの成功、partial、fail
- feature_weeklyの計算
- event detection
- snapshot hash
- universe cascade
- data quality blocker
- risk checkのpass/reduce/stop/kill
- LLM audit log保存

### 14.2 結合テスト

ローカルfixtureで次を通す。

```bash
python src/01_init_db.py --db-path /tmp/rencrow_test.db
python src/02_fetch_market.py --fixture fixtures/1306T_prices.csv --db-path /tmp/rencrow_test.db
python src/03_fetch_macro.py --fixture fixtures/macro_series.csv --db-path /tmp/rencrow_test.db
python src/04_build_features.py --week-end 2026-05-15 --db-path /tmp/rencrow_test.db
python src/05_detect_events.py --week-end 2026-05-15 --db-path /tmp/rencrow_test.db
python src/06_make_snapshot.py --week-end 2026-05-15 --db-path /tmp/rencrow_test.db
python src/08_validate_data.py --as-of 2026-05-15 --db-path /tmp/rencrow_test.db
python src/09_backtest_weekly_rotation.py --snapshot latest --db-path /tmp/rencrow_test.db
python src/10_risk_check.py --snapshot latest --strategy weekly_etf_rotation_v1 --db-path /tmp/rencrow_test.db
python src/11_generate_decision.py --snapshot latest --strategy weekly_etf_rotation_v1 --db-path /tmp/rencrow_test.db
```

### 14.3 回帰テスト

- 同一snapshotから同一decisionが再生成される。
- 同一config hashならbacktest resultが一致する。
- データが増えても過去snapshotのdecisionが変わらない。
- 取得失敗時にsuccess扱いにならない。
- LLMが失敗しても売買候補生成の数値結果は変わらない。

### 14.4 受け入れテスト

- CLIだけで週次運用フローが完走する。
- blockerデータを混ぜるとno tradeになる。
- event blockerを混ぜると新規建て停止になる。
- risk limit超過でreduceまたはstopになる。
- LLM reportがなくてもdecision candidateは生成できる。
- LLM reportがある場合、`llm_audit_log` が保存される。

## 15. 実装順序

### Phase 1: データ取得と品質検証

1. 既存 `07_sync_universe.py` を運用手順化する。
2. `08_validate_data.py` を追加する。
3. 欠損、stale、異常リターン、adjustment anomalyを `data_quality_check` に保存する。
4. fetch statusのsummaryを監査可能にする。

### Phase 2: feature_weeklyの再現性固定

1. feature configをhash化する。
2. 12週skip1 momentum、volatility、drawdown、MA gap、volume changeを明示する。
3. USD資産の円換算ルールを追加する。
4. feature生成の対象日と利用可能日を分離する。

### Phase 3: snapshot作成

1. `06_make_snapshot.py` の出力を運用標準にする。
2. `source_summary_json`、`event_state_json`、`features_hash` を監査する。
3. snapshotから過去decisionを再生成できるようにする。

### Phase 4: 週次ETFベース戦略バックテスト

1. `strategy_version` を追加する。
2. `09_backtest_weekly_rotation.py` を追加する。
3. 最小ユニバース `SPY`, `IEF`, `TLT`, `GLD`, `SHY` で検証する。
4. 1週ラグ、コスト、スリッページ、税近似を反映する。

### Phase 5: イベントveto

1. FOMC、CPI、NFP、BOJをevent calendarに入れる。
2. event severityを定義する。
3. blocker時に新規建て停止する。
4. LLMによる解除を禁止する。

### Phase 6: 税・手数料・スリッページ反映

1. cost configを追加する。
2. 税前、税後、コスト後の指標を分ける。
3. turnoverとtax dragを採用判定に入れる。

### Phase 7: リスク制約

1. `10_risk_check.py` を追加する。
2. DD、週次損失、集中、ボラ、イベント、データ品質を判定する。
3. `risk_check_result` を保存する。

### Phase 8: decision candidate生成

1. `11_generate_decision.py` を追加する。
2. `weekly_signal` と `decision_log` を保存する。
3. human approvalファイル案を生成する。

### Phase 9: 紙運用

1. `12_paper_trade.py` を追加する。
2. 8から12週間の紙運用を行う。
3. TCA、税ロット近似、監査レポートを保存する。

### Phase 10: LLMレポート補助

1. `13_llm_report.py` を追加する。
2. `llm_audit_log` を保存する。
3. LLM失敗時も数値系CLIが動くようにする。

## 16. 完了条件

### 16.1 MVP完了条件

- `01_init_db.py` から `08_validate_data.py` までがCLIで再現可能に動く。
- `feature_weekly` が同一入力から同一結果を生成する。
- snapshotが作成され、`snapshot_registry` にhash付きで保存される。
- 週次ETF回転戦略のbacktestが、税、手数料、スリッページ、1週ラグ込みで実行できる。
- event vetoがbacktestとdecisionに反映される。
- risk checkがpass/reduce/stop/killを返せる。
- decision candidateが生成され、人間承認前で止まる。
- LLM reportは任意であり、売買判断を直接変更しない。
- 主要なCLIにテストがある。

### 16.2 紙運用完了条件

- 8から12週間、週次フローを継続できる。
- fetch、validation、feature、snapshot、backtest、risk、decision、paper trade、reportのログが欠けない。
- no trade条件が少なくともテストで確認済み。
- event vetoが少なくともテストで確認済み。
- 紙運用のdecision理由を後から説明できる。
- 税後、コスト後の期待値が過剰に劣化していない。

### 16.3 小額実運用へ進む前の条件

- live order CLIはまだ作らない。
- 実運用する場合も、人間が証券会社画面で手動発注する。
- 小額実運用は、紙運用完了条件を満たした後に別仕様書で定義する。
- NISAは引き続き完全分離する。
- kill switchと手動停止手順を先に文書化する。

## 17. 運用時の原則

- 取得対象は学習用に広げ続けてよい。
- 実運用候補は広げすぎない。
- データが増えるほど、validationとsnapshotの重要度が上がる。
- LLMは便利な説明者であり、売買責任者ではない。
- 100万円を200万円へ近づける目的は維持するが、システムは破綻しやすい賭け方を弾く。
- 人間が放置できるのは、データ取得、検証、特徴量生成、snapshot、レポート生成までである。
- 売買候補、紙運用承認、実資金投入は、人間確認を残す。
