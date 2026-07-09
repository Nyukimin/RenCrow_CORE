# タスク完了時のワークフロー

## コード変更後の標準フロー

### 1. コード生成（必要な場合）
```bash
make generate
# または
go generate ./...
```
workspace/ の埋め込みファイルを更新した場合に必要。

### 2. フォーマット
```bash
make fmt
```
Go 標準フォーマッタでコードを整形。

### 3. 静的解析
```bash
make vet
```
go vet で潜在的な問題を検出。

### 4. テスト実行
```bash
make test
```
すべてのユニットテストを実行。

### 5. 統合チェック（推奨）
```bash
make check
```
上記の fmt + vet + test を一括実行。

## 新機能追加時

### 1. テストファースト
- `*_test.go` ファイルを作成
- テストケースを記述
- 実装
- テストが通ることを確認

### 2. ドキュメント更新
- README.md を更新（必要に応じて）
- godoc コメントを追加
- 仕様書を更新（`docs/01_正本仕様/実装仕様.md`）

### 3. 動作確認
```bash
# ビルド
make build

# ローカル実行テスト
./build/picoclaw-linux-amd64 onboard
./build/picoclaw-linux-amd64 agent -m "テストメッセージ"
```

## リリース前チェックリスト

- [ ] `make check` が成功
- [ ] すべてのテストが通過
- [ ] ドキュメントが更新されている
- [ ] CHANGELOG が更新されている（該当する場合）
- [ ] 設定例（config.example.json）が最新
- [ ] Docker ビルドが成功
```bash
docker compose --profile gateway build
```

## デバッグ時

### ログレベル変更
環境変数で制御:
```bash
export LOG_LEVEL=DEBUG
picoclaw agent
```

### 個別パッケージのテスト
```bash
go test -v ./pkg/agent/
go test -v ./pkg/tools/
```

### カバレッジ確認
```bash
go test -cover ./...
# または詳細レポート
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## ホットリロード開発
開発時は以下のワークフローを推奨:
```bash
# 変更 → ビルド → テスト のループ
while true; do
  make build && make test && ./build/picoclaw-linux-amd64 agent -m "test"
  sleep 2
done
```
または `entr` などのツールを使用。
