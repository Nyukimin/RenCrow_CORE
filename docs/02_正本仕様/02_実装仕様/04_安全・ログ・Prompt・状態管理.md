# 統合実装仕様: 安全・ログ・Prompt・状態管理

- status: active
- lifecycle: canonical child
- owner: RenCrow_CORE
- parent_spec: `../02_実装仕様.md`
- source_spec: `../02_実装仕様.md`の2026-07-15分割前章
- last_reviewed: 2026-07-15
- scope: security、log、Prompt、state、設定値、テスト観点

## 7. セキュリティ仕様
- CODE以外でクラウド利用禁止
- `/local` 中はクラウド呼び出し禁止
- 機密情報（token/secret/PII）はクラウド送信前に除外
- ログ保存はマスク後のみ
- サニタイズ責務は `pkg/security/sanitizer.go` に一本化

---

## 7. ログ仕様

### 7.1 イベント種別（固定）
- `router.decision`
- `classifier.error`
- `worker.success`
- `worker.fail`
- `route.override`
- `loop.stop`
- `final.route`
- `approval.requested`（2026-02-24追加）
- `approval.granted`（2026-02-24追加）
- `approval.denied`（2026-02-24追加）
- `approval.auto_approved`（2026-02-24追加）
- `coder.plan_generated`（2026-02-24追加）

### 7.2 最低保存項目
- `initial_route`, `final_route`
- `classifier_route`, `classifier_confidence`
- `worker_calls`, `needs_next_loop`, `risk`, `fit`
- `reroute_used`, `stop_reason`, `error_reason`

### 7.3 承認フロー関連の保存項目（2026-02-24追加）
- `job_id`: ジョブ識別子
- `approval_status`: `pending` / `granted` / `denied` / `auto_approved`
- `approval_requested_at`: 承認要求時刻
- `approval_decided_at`: 承認決定時刻
- `approver`: 承認者（ユーザーID）
- `coder_output`: Coder の生成した plan/patch/risk の要約

### 7.4 JobID 必須化（2026-03-01追加）
すべての作業・応答に JobID を必須とする：
- ログ保存時に必ず `job_id` フィールドを含める
- JobID 形式: `job_<YYYYMMDD>_<連番>`（例: `job_20260301_001`）
- JobID はセッション単位でインクリメント
- トレーサビリティ向上のため、すべてのエージェント間通信で JobID を伝播

### 7.5 画像送信監査ログ（現行）

- ログカテゴリ: `provider.http`
- 送信前: `LLM image audit before request`
  - `source_path`, `url_type`, `image_url`（要約）, `image_url_length`
  - `local_exists_before`, `local_size_before_bytes`
  - `included`, `drop_reason`
- タイムアウト時: `LLM image audit after timeout`
  - `local_exists_after_timeout`, `local_size_after_timeout_bytes`

---

## 8. Prompt/宣言仕様
- persona/policy/overlay を固定順で連結
- system prompt はアプリ側のみが構築
- route変更時のみ1行宣言（CHATは宣言なし）

---

## 9. 状態管理仕様
- `session_id` はチャネル単位で一意管理（Slackはthread単位推奨）
- `short_memory` 中心で運用し `recent_turns` は最小限
- ANALYZE構造化データの保存先は実装で1つに統一:
  - `pkg/memory/store.go` 拡張
  - または `pkg/analyze/store.go` 新設

---

## 10. 設定値と閾値

この章の`routing.classifier.*`と`loop.*`は分割前文書に残る旧config案である。現行のMio採用閾値はcode上の`0.7`で、CODE専用`0.8`gateと共通loop configは現行contractではない。runtime接続の現在値は`../03_Runtime_Config.md`、route契約は`02_ルーティング・ループ制御.md`を優先する。

### 10.0 Config 選択とスナップショット

実運用 Config の正本はユーザー環境側の `.rencrow/config.yaml` とする。

- Windows: `C:\Users\nyuki\.rencrow\config.yaml`
- macOS / Linux: `$HOME/.rencrow/config.yaml`

起動時の Config 解決順は以下とする。

1. `RENCROW_CONFIG` が設定されている場合、その path を読む。
2. `RENCROW_CONFIG` が未設定の場合、ユーザー環境側の `.rencrow/config.yaml` を読む。
3. 上記が存在しない、読めない、または validation に失敗する場合は起動失敗とする。

Repo 内の `run/local-windows-runtime.yaml` は実運用 Config のスナップショットであり、自動 fallback として読んではいけない。

Repo 内スナップショットの用途は、差分確認、復元、再現、明示的な検証起動に限定する。検証で使う場合は `RENCROW_CONFIG=run/local-windows-runtime.yaml` のように明示する。

LLM / TTS / STT のサーバ所在は、実運用 Config の `rencrow.llm` / `rencrow.tts` / `rencrow.stt` を正本とする。既存の `local_llm` / `llm_ops` / `tts` / `stt` は移行期間の互換設定として扱う。

- `routing.classifier.min_confidence = 0.6`
- `routing.classifier.min_confidence_for_code = 0.8`
- `loop.max_loops = 3`
- `loop.max_millis = 90000`
- `loop.allow_auto_reroute_once = true`
- `loop.allow_chat_propose_reroute_once = true`
- RenCrow_LLM 経由では `options.num_ctx` や backend Modelfile 値を request に含めない。
- Ollama 直結 fallback のみ `options.num_ctx` と `keep_alive: -1` を使う。
- `providers.ollama_restart_command`（任意）: legacy Ollama 直結時の再起動コマンド（例: `systemctl --user restart ollama`）
- ヘルスチェック制約:
  - RenCrow_LLM 経由では `/health` と `/v1/models` で公開 alias と backend 状態を確認する。
  - legacy Ollama 直結では `ModelRequirement.MaxContext` を使い、大きすぎる `num_ctx` によるロード失敗を防止する。
  - `ModelRequirement.MinContext`: 指定時、context_length がこれ未満の場合は NG（必要に応じて使用）

### 10.1 Coder3 設定（2026-02-24追加）
- `coder3.provider = "anthropic"`: Claude API 専用
- `coder3.model = "claude-sonnet-4.5"`: 使用モデル
- `coder3.max_tokens = 16000`: 1ジョブあたりの上限
- `coder3.retry_max = 2`: 連続リトライ回数制限
- `coder3.timeout_sec = 60`: タイムアウト

### 10.2 承認フロー設定（2026-02-24追加）
- `approval.required_by_default = true`: デフォルトで承認必須
- `approval.auto_approve.enabled = false`: Auto-Approve 無効（デフォルト）
- `approval.auto_approve.scope.allowed_task_types = ["design", "review"]`: 許可タスク種別
- `approval.auto_approve.scope.allowed_paths_prefix = ["docs/"]`: 許可パスプレフィックス
- `approval.auto_approve.scope.deny_operations = ["delete", "rename", "push_public"]`: 禁止操作
- `approval.auto_approve.ttl_minutes = 60`: 有効期限（分）
- `approval.hard_require_approval = ["delete", "rename", "send_sensitive", "push_public", "cost_over_limit"]`: 強制承認が必要な操作

---

## 11. テスト観点
- 正常系: command優先、rules確定、classifierフォールバック、reroute最大1回
- 異常系: 分類器JSON不正、ワーカーJSON不正、`local_only`中CODE、`risk=high`確認停止
- 回帰: 非対象チャネルへ副作用なし、宣言文切替の維持

---
