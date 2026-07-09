# RenCrow 開発コマンド

## ビルドとインストール
```bash
# 依存関係のダウンロード
make deps

# ビルド（現在のプラットフォーム用）
make build

# 全プラットフォーム用ビルド
make build-all

# インストール（~/.local/bin/picoclaw）
make install

# アンインストール
make uninstall

# 完全アンインストール（設定も削除）
make uninstall-all
```

## コード生成
```bash
# go:generate ディレクティブを実行
make generate
# または
go generate ./...
```

## テストとコード品質
```bash
# テスト実行
make test
# または
go test ./...

# コードフォーマット
make fmt
# または
go fmt ./...

# 静的解析
make vet
# または
go vet ./...

# 全チェック（fmt + vet + test）
make check
```

## 実行
```bash
# 初期化
picoclaw onboard

# エージェントモード（対話）
picoclaw agent -m "メッセージ"

# 対話モード
picoclaw agent

# ゲートウェイモード
picoclaw gateway

# ステータス確認
picoclaw status

# スケジュールタスク管理
picoclaw cron list
picoclaw cron add "タスク名" "スケジュール"
```

## 監視ツール（watchdog）
```bash
# watchdog インストール
make install-watchdog

# 有効化
make enable-watchdog

# 無効化
make disable-watchdog

# ステータス確認
make watchdog-status

# 手動実行
make watchdog-run-once

# テスト
make test-watchdog-mock
```

## Docker
```bash
# ビルドと起動
docker compose --profile gateway up -d

# ログ確認
docker compose logs -f picoclaw-gateway

# 停止
docker compose --profile gateway down

# 再ビルド
docker compose --profile gateway build --no-cache
```

## クリーンアップ
```bash
# ビルドアーティファクト削除
make clean

# Go モジュールキャッシュクリア
go clean -modcache
```
