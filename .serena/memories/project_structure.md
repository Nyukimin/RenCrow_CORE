# RenCrow プロジェクト構造

## ディレクトリレイアウト
```
picoclaw/
├── cmd/picoclaw/              # メインアプリケーション
│   ├── main.go                # エントリーポイント
│   └── workspace/             # 埋め込みワークスペースファイル
│       ├── AGENT.md           # エージェント行動ガイド
│       ├── IDENTITY.md        # エージェントアイデンティティ
│       ├── SOUL.md            # エージェントソウル
│       ├── USER.md            # ユーザープリファレンス
│       ├── CHAT_PERSONA.md    # チャットペルソナ（Mio）
│       ├── PrimerMessage.md   # プライマーメッセージ
│       ├── memory/            # 長期メモリ
│       └── skills/            # ビルトインスキル
│
├── pkg/                       # 再利用可能なパッケージ
│   ├── agent/                 # エージェントコアロジック
│   │   ├── loop.go            # メインループ
│   │   ├── context.go         # コンテキスト構築
│   │   ├── memory.go          # メモリ管理
│   │   ├── router.go          # ルーティング
│   │   └── classifier.go      # カテゴリ分類器
│   ├── tools/                 # ツール実装
│   │   ├── registry.go        # ツールレジストリ
│   │   ├── filesystem.go      # ファイルシステムツール
│   │   ├── shell.go           # シェル実行
│   │   ├── web.go             # Web 検索
│   │   ├── message.go         # メッセージ送信
│   │   ├── spawn.go           # サブエージェント生成
│   │   └── cron.go            # スケジュール管理
│   ├── channels/              # チャットプラットフォーム
│   │   ├── manager.go         # チャネルマネージャ
│   │   ├── telegram.go        # Telegram 統合
│   │   ├── discord.go         # Discord 統合
│   │   ├── line.go            # LINE 統合
│   │   ├── slack.go           # Slack 統合
│   │   ├── qq.go              # QQ 統合
│   │   ├── dingtalk.go        # DingTalk 統合
│   │   ├── feishu_*.go        # Feishu 統合
│   │   └── onebot.go          # OneBot 統合
│   ├── providers/             # LLM プロバイダー
│   │   ├── types.go           # 共通型定義
│   │   ├── http_provider.go   # HTTP ベースプロバイダー
│   │   ├── claude_provider.go # Anthropic Claude
│   │   ├── ollama_provider.go # Ollama（推定）
│   │   └── ...                # その他のプロバイダー
│   ├── config/                # 設定管理
│   ├── logger/                # ロギング
│   ├── session/               # セッション管理
│   ├── state/                 # 状態管理
│   ├── bus/                   # メッセージバス
│   ├── cron/                  # Cron サービス
│   ├── heartbeat/             # ハートビートサービス
│   ├── health/                # ヘルスチェック
│   ├── voice/                 # 音声処理
│   ├── auth/                  # 認証
│   └── devices/               # デバイス管理
│
├── docs/                      # ドキュメント
│   ├── 00_ドキュメント分類一覧.md
│   ├── 01_正本仕様/           # 正式仕様（実装の一次参照）
│   ├── 02_v2統合分割仕様/     # v2 分割仕様
│   ├── 03_旧分割仕様アーカイブ/
│   ├── 04_監査差分分析/
│   ├── 05_LLM運用プロンプト設計/
│   ├── 06_実装ガイド進行管理/
│   ├── 07_コミュニティ広報/
│   └── 08_AIからの提案/
│
├── config/                    # 設定ファイル
│   └── config.example.json    # 設定例
│
├── scripts/                   # スクリプト
│   ├── ops_watchdog.sh        # 監視スクリプト
│   └── ops_watchdog_kick.sh   # 監視アクション
│
├── systemd/                   # systemd ユニットファイル
│   └── user/
│       ├── picoclaw-watchdog.service
│       └── picoclaw-watchdog.timer
│
├── Makefile                   # ビルドファイル
├── go.mod                     # Go モジュール定義
├── Dockerfile                 # Docker イメージ
├── docker-compose.yml         # Docker Compose 設定
└── .goreleaser.yaml           # リリース設定
```

## エントリーポイント
- `cmd/picoclaw/main.go`: メインアプリケーション
  - サブコマンド: `onboard`, `agent`, `gateway`, `status`, `cron`

## 主要パッケージの役割
- **agent**: エージェントループ、ルーティング、コンテキスト管理
- **tools**: ファイル操作、シェル、Web 検索などのツール
- **channels**: チャットプラットフォームとの統合
- **providers**: LLM プロバイダーとの通信
- **config**: 設定ファイルの読み込みと管理
- **session**: セッション履歴と状態の管理
- **bus**: コンポーネント間のメッセージング
