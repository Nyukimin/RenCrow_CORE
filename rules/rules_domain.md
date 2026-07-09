# rules_domain.md - RenCrow ドメイン固有ルール

**作成日**: 2026-02-24
**最終更新**: 2026-05-09
**プロジェクト名**: RenCrow (`RenCrow_CORE`)
**目的**: RenCrow 固有の技術詳細、実装名、運用制約だけを定義する

---

## 0. このファイルの役割

このファイルは RenCrow 固有の補足ルールである。

一般的な Go / テスト / ログ / 状態管理 / セキュリティ / アーキテクチャのルールは `rules/common/` に置く。
このファイルには、RenCrow の実装名、ローカル LLM 運用、ルーティング、セッション、ヘルスチェックなど、このプロジェクトでしか意味を持たない内容だけを書く。

共通ルールは以下を参照する。

- `rules/common/rules_backend.md`
- `rules/common/rules_testing.md`
- `rules/common/rules_logging.md`
- `rules/common/rules_state_management.md`
- `rules/common/rules_observation_verification.md`
- `rules/common/rules_architecture.md`

---

## 1. LLM プロバイダー統合

RenCrow の LLM 呼び出しは、Chat / Worker / Coder の責務分離を前提にする。

主なプロバイダー用途:

| プロバイダー | 用途 | 認証方式 |
| --- | --- | --- |
| Ollama | Chat / Worker ローカル実行 | なし |
| DeepSeek | Coder1 相当の仕様整理 | API キー |
| OpenAI | Coder2 相当の実装 | API キー |
| Claude | Coder3 相当の高品質推論 | API キー |

API キーは環境変数または安全なシークレット管理から取得する。
設定ファイルやテストコードへ平文保存してはいけない。

対象環境変数:

- `ANTHROPIC_API_KEY`
- `DEEPSEEK_API_KEY`
- `OPENAI_API_KEY`

---

## 2. Ollama 運用制約

RenCrow は低スペック環境でのローカル LLM 常駐を前提にする。

- Chat / Worker モデルは `keep_alive: -1` を前提にする
- MaxContext 8192 を超える前提で設計しない
- 起動時、LLM 呼び出し前、失敗時の再試行前にヘルスチェックする
- Ollama のモデルロード状態と context_length を確認する

Ollama の一般的な接続確認やリトライ方針は `rules/common/rules_backend.md` と `rules/common/rules_architecture.md` に従う。

---

## 3. ルーティング

ルーティング判断の正本は `rules/routing-policy.md` とする。

RenCrow の主なカテゴリ:

- `CHAT`
- `PLAN`
- `ANALYZE`
- `OPS`
- `RESEARCH`
- `CODE`
- `CODE1`
- `CODE2`
- `CODE3`

優先順位:

1. 明示コマンド
2. ルール辞書
3. 分類器
4. 安全側フォールバック

曖昧な入力や危険な入力は、安易に `OPS` や `CODE` に流さず、まず `CHAT` / `PLAN` / `ANALYZE` へ戻す。

---

## 4. セッション管理

RenCrow では `session_id` を追跡の中心にする。

- セッションごとに必要最小限の状態だけを持つ
- 長期記憶は外部ストアや Obsidian などへ逃がす
- 日次カットオーバーで古いセッションを整理する
- 派生 ID を増やす前に、既存の `session_id` で表現できないか確認する

ID、cache、queue、pending 状態の追加判断は `rules/common/rules_state_management.md` に従う。

---

## 5. ログとトレーサビリティ

RenCrow の実行ログでは、後から判断と実行を追えることを優先する。

最低限意識する項目:

- `job_id`
- `session_id`
- selected category
- selected route
- execution status
- executed commands
- failed commands
- git commit hash
- coder output summary

重要イベント例:

- `router.decision`
- `classifier.error`
- `worker.success`
- `worker.fail`
- `worker.executed`
- `worker.rollback`
- `coder.plan_generated`
- `route.override`
- `final.route`

ログ一般、マスキング、調査記録は `rules/common/rules_logging.md` に従う。

---

## 6. 再起動とヘルスチェック

RenCrow / RenCrow の再起動前には、既存の関連作業を必ず全停止する。

最低限の順序:

1. `systemctl --user stop rencrow.service`
2. 残存する `rencrow` プロセス停止
3. `:18790` の listen が消えたことを確認
4. `http://127.0.0.1:18790/health` が失敗することを確認
5. ビルド
6. service 起動
7. health が `200 OK` になることを確認

`rencrow.service` は `~/.local/bin/rencrow` を自動再起動することがある。
プロセスだけ止めて再起動してはいけない。

---

## 7. 実機 / Viewer / 音声系の確認

Viewer、IdleChat、STT、TTS のような実機状態を伴う機能では、テスト通過だけで完了扱いしない。

- 実機または Playwright で対象フローを追う
- 1 セッション以上の開始から終了まで確認する
- 表示ログ、イベントログ、画面状態を照合する
- TTS / STT / Viewer 表示の責務を混同しない

詳細は `rules/common/rules_observation_verification.md` と `rules/common/rules_state_management.md` に従う。

---

## 8. テストと Lint

RenCrow 固有の確認コマンドは、対象範囲に合わせて選ぶ。

代表例:

```bash
go test ./cmd/rencrow ./internal/adapter/viewer ./internal/application/idlechat
node --test internal/adapter/viewer/*.test.mjs
```

全体の TDD、Lint、カバレッジ、E2E 方針は `rules/common/rules_testing.md` と `rules/common/rules_backend.md` に従う。

---

## 9. RenCrow の言語選択

技術選定の共通原則は `rules/common/rules_architecture.md` に従う。
RenCrow では、以下を標準とする。

### 9.1 CLI

恒久的な RenCrow / RenCrow CLI は Go を第一候補にする。

対象:

- service 操作
- health / debug / diagnosis
- config 検証
- IdleChat / STT / TTS の実機診断
- RenCrow 内部 API と密接に関わる運用コマンド

理由:

- 本体が Go である
- 1 バイナリで配布しやすい
- systemd / Linux 運用と相性がよい
- 既存 config 型や内部 API と統合しやすい

### 9.2 Viewer / Playwright E2E

Viewer、ブラウザ、DOM、console、WebSocket を検証する E2E は Node.js Playwright を第一候補にする。

依存は `package.json` / `package-lock.json` で管理する。
Python の `requirements.txt` へ入れない。

### 9.3 Python を使ってよい領域

Python は以下の補助用途で使ってよい。

- 音声ファイル処理
- STT / Whisper 周辺の実験
- ログ解析
- データ加工
- 一時的な調査スクリプト

恒久運用 CLI にする場合は、Go へ寄せるべきか再検討する。

### 9.4 判断に迷った場合

迷った場合は以下の順で選ぶ。

1. RenCrow 本体や運用 CLIなら Go
2. Viewer / browser E2E なら Node.js
3. 音声・データ・解析補助なら Python
4. 例外が必要なら、理由と依存管理方法を明記する

---

## 10. このファイルの更新ルール

以下の場合だけ、このファイルを更新対象にする。

- RenCrow 固有の実装名やルーティングが変わった
- RenCrow 固有の運用手順が変わった
- RenCrow 固有のセッション、LLM、Viewer、音声系の制約が増えた

一般化できる内容は、このファイルではなく `rules/common/` に追加する。
