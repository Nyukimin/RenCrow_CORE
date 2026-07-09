# RenCrow コードスタイルと規約

## ファイルヘッダー
すべての Go ファイルには以下のヘッダーを含める：
```go
// RenCrow - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 RenCrow contributors
```

## 命名規則
- **パッケージ**: 小文字、短く簡潔（例: `agent`, `tools`, `config`）
- **公開識別子**: PascalCase（例: `AgentLoop`, `MessageBus`）
- **非公開識別子**: camelCase（例: `processOptions`, `sessionKey`）
- **定数**: PascalCase（公開）または camelCase（非公開）
- **ファイル名**: スネークケース（例: `agent_loop.go`, `context_test.go`）

## パッケージ構造
```
picoclaw/
├── cmd/picoclaw/           # メインアプリケーション
│   └── main.go             # エントリーポイント
├── pkg/                    # 再利用可能なパッケージ
│   ├── agent/              # エージェントループとロジック
│   ├── tools/              # ツール実装
│   ├── channels/           # チャットプラットフォーム統合
│   ├── providers/          # LLM プロバイダー
│   ├── config/             # 設定管理
│   ├── logger/             # ロギング
│   ├── session/            # セッション管理
│   └── ...                 # その他のパッケージ
```

## テスト
- テストファイル: `*_test.go`
- テスト関数: `func Test<FunctionName>(t *testing.T)`
- サブテスト使用推奨（table-driven tests）
- 例: `TestBuildMessages_WorkOverlayInjected`

## コメント
- 公開関数/型には godoc スタイルのコメントを記述
- コメントは宣言する識別子で開始（例: `// AgentLoop manages...`）
- 複雑なロジックには説明コメントを追加

## エラーハンドリング
- エラーは常に返す、パニックは最小限に
- `errors.New()` または `fmt.Errorf()` を使用
- エラーメッセージは小文字で開始（慣例）

## 並行処理
- `sync.Mutex`, `sync.RWMutex`, `sync.Map`, `atomic` を適切に使用
- goroutine リークに注意
- context を使用してキャンセル可能に

## ロギング
- `pkg/logger` パッケージを使用
- ログレベル: ERROR、INFO、DEBUG
- 構造化ログ形式（key=value）

## 設定
- `config/config.example.json` を参照
- 環境変数でオーバーライド可能
- `~/.picoclaw/config.json` がデフォルトパス
