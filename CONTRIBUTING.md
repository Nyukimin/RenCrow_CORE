# Contributing to RenCrow_CORE

Issue や Pull Request を歓迎します。変更は一つの責務に絞り、既存 API、provider、store、CLI、Viewer、background job の挙動を意図せず削らないでください。

## 開発手順

1. Issue または説明文で目的、対象範囲、安全上の影響を明示する。
2. feature の ownership と外部 module 境界を確認する。
3. 既存挙動を固定する test を先に用意し、小さい単位で変更する。
4. 関係する公開仕様を更新する。
5. `go test`、`go vet`、必要な runtime/browser 検証結果を Pull Request に記載する。

代表的な検証コマンド:

```bash
go test ./modules/...
go test ./cmd/rencrow ./internal/features/... ./internal/adapter/viewer ./modules/...
go test ./...
go vet ./...
```

ローカル設定、token、API key、ログ、DB、生成物、個人データは commit しないでください。公開資料の追加・改名は `.public-docs-allowlist` も同時に更新し、仕様・実装仕様・解析データ・旧仕様を混在させないでください。

セキュリティ問題は公開 Issue に詳細を書かず、[SECURITY.md](SECURITY.md) の報告方法を利用してください。
