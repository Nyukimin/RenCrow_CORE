# RenCrow_CORE Roadmap

このロードマップは 2026-07-15 時点の現行コードと採用済み仕様を基準にしています。詳細な実装状態は [docs/08_実装状況・ロードマップ.md](docs/08_実装状況・ロードマップ.md) を参照してください。

## 現在の重点項目

1. Advisor の採用・成果記録と日次 score 集計を production loop へ接続する。
2. Opportunity から Workstream、Artifact、Approval、RevenueEvent、Reflection までを同一 trace で接続する。
3. legacy-body の既存挙動を保ったまま、feature ownership を `internal/features/*` へ段階移行する。

## 品質改善候補

- 終了 signal 受信時の HTTP graceful shutdown と drain
- `cmd/rencrow-agent` の contract/unit test 拡充
- `internal/features/ops` に集中した route ownership の縮小
- Viewer の responsive、音声再生、外部 runtime を含む継続的な実機検証

## 方針

- 既存機能を削らない非破壊移行を優先する。
- 外部 module の実装本体を CORE に戻さない。
- 公開、外部送信、請求、契約、価格決定は承認なしに実行しない。
- 外部情報や Advisor 出力を無審査で確定 Knowledge に昇格しない。
- 安定していない契約を公開 package へ早期固定しない。

予定は互換性、安全性、検証結果によって更新されます。古い計画資料は現在の backlog とみなさず、保存用ブランチで履歴として参照します。
