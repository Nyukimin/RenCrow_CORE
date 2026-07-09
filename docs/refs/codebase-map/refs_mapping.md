---
generated_at: "2026-07-01T13:19:25+09:00"
run_id: run_20260701_131925
phase: 0
step: "0b"
profile: RenCrow_CORE
artifact: survey_mapping
---

## 概要

既存の`docs/調査/`を、今回の5つの解析module groupへ対応づける。外部`--refs`は指定されていないため、これはPhase 0bの既存調査マッピングのみである。

## 関連ドキュメント

- [アーキテクチャ総合.md](アーキテクチャ総合.md)
- [結合ポイントマップ.md](結合ポイントマップ.md)
- [ユースケース逆引き.md](ユースケース逆引き.md)
- [modules/潜在バグ一覧.md](modules/潜在バグ一覧.md)

## 既存調査マッピング

| 調査ファイル | 主な対応module group | 理由 |
|---|---|---|
| `docs/調査/20260527_viewer_live_multitab_e2e.md` | `adapter_viewer_channels`, `cmd_runtime` | Viewer live/multitab検証は`cmd/rencrow/routes.go`の`/viewer/*`登録と`internal/adapter/viewer`のSSE/handler群が中心。 |
| `docs/調査/20260527_viewer_state_inventory.md` | `adapter_viewer_channels`, `domain_application`, `infrastructure_persistence` | Viewer state inventoryは`MonitorStore`、job/evidence/memory状態、永続化storeの境界を横断する。 |
| `docs/調査/20260527_forecast_provider_news調査.md` | `domain_application`, `infrastructure_persistence`, `docs_config_data` | forecast/newsは`internal/application/idlechat`、webgather/provider、docs/config側の運用仕様と関係する。 |

## 解析時の扱い

- `docs/01_正本仕様/実装仕様.md`を実装判断の一次参照として扱う。
- Viewer / UIは`DESIGN.md`と`rules/rules_viewer_ui.md`を追加参照する。
- `docs/codebase-map`は今回新規生成した解析成果物であり、古い解析文書が再出現した場合は内容の鮮度を`manifest.json`で判定する。

