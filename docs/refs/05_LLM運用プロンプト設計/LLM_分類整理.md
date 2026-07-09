# LLM関連ドキュメントの分類整理

## 対象
- `docs/05_LLM運用プロンプト設計/LLM_deepseek運用仕様.md`
- `docs/05_LLM運用プロンプト設計/LLM_Ollama常駐管理.md`
- `docs/05_LLM運用プロンプト設計/LLM_ollama世代管理.md`
- `docs/05_LLM運用プロンプト設計/LLM_Worker_Spec_v1_0.md`

## 分類結果（結論）

### 1) コーディングするもの（実装対象）
- `docs/05_LLM運用プロンプト設計/LLM_Worker_Spec_v1_0.md`
  - Gateway側のJSONスキーマ検証
  - parse失敗時の再試行制御（1回）
  - timeout制御（60-120秒）
  - EventId採番とログ紐付け
  - API呼び出しパラメータ固定（`format: "json"`, `stream: false`, `keep_alive: -1`）

- `docs/05_LLM運用プロンプト設計/LLM_deepseek運用仕様.md` のうち実装が必要な部分
  - 固定prefixファイルの読み込み・固定順連結
  - 可変suffix（`TASK/DIFF/LOGS`）の末尾付与
  - `*_v1.md -> *_v2.md` 切替ロジック（設定値で参照先を変更）
  - Worker JSONをCoding LLM入力ブロックへ整形する変換処理

## 2) 運用するもの（Runbook/監視手順）
- `docs/05_LLM運用プロンプト設計/LLM_Ollama常駐管理.md`
  - Ubuntu監視、Windows復旧の二段運用
  - ヘルスチェック項目（`/api/tags`, loaded状態, p95, 連続失敗回数）
  - 通知・復旧フロー（検知 -> 通知 -> 復旧トリガー -> 再確認）

- `docs/05_LLM運用プロンプト設計/LLM_ollama世代管理.md`
  - `chat-vN` / `worker-vN` の世代運用
  - 安定時に `chat-v1` / `worker` へ上書き
  - 不要世代の掃除（`ollama rm`）

- `docs/05_LLM運用プロンプト設計/LLM_deepseek運用仕様.md` のうち運用部分
  - 固定prefixを直接編集しない（バージョン更新で吸収）
  - 文字列・順序固定によるキャッシュ効率維持
  - ガードレール（権限最小化、秘密情報非投入、ログ最小化）

## 3) プロンプトにするもの（Prompt資産）
- `docs/05_LLM運用プロンプト設計/LLM_Worker_Spec_v1_0.md`
  - 「JSONのみ出力」Prefix（6章）をそのままWorkerのsystem/user prefixとして利用

- `docs/05_LLM運用プロンプト設計/LLM_deepseek運用仕様.md`
  - `CODING_RULES_v1.md`
  - `ENV_CONSTRAINTS_v1.md`
  - `REPO_SNAPSHOT_v1.md`
  - `HOT_FILES_v1.md`
  - 上記4つを固定順で連結し、末尾に可変suffixを付加

- `docs/05_LLM運用プロンプト設計/LLM_ollama世代管理.md`
  - `Modelfile.chat` の `SYSTEM ...` 文
  - `Modelfile.worker` の `SYSTEM ...` 文（JSON専用）

## 実装/運用/プロンプトの境界ルール（重複防止）
- ルールA: 「手順」だけの記述は運用ドキュメント化し、アプリコードへ直書きしない
- ルールB: 「出力形式/口調/禁止事項」はプロンプトファイルへ分離し、業務ロジックと混在させない
- ルールC: 「再試行・検証・タイムアウト・ログ紐付け」はコードで保証する
- ルールD: 固定prefixは不変資産としてバージョン管理し、都度編集しない

## 最小タスクリスト（次アクション）
1. Worker I/O契約をGateway実装に反映（JSON parse + schema検証 + retry）
2. Coding LLM入力組み立てを `固定prefix + 可変suffix` に統一
3. プロンプト資産を `openclaw/prompts/...` にファイル分離
4. 監視・復旧手順をRunbook化し、監視閾値を実測値で調整
