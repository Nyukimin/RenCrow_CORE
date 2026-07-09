# 詳細実装仕様 01: LLM プロバイダー拡張

**更新日**: 2026-03-26
**ステータス**: 差分整理（未実装）
**親文書**: `OpenClaw機能差分比較表_20260326.md`

---

## 1. 概要

RenCrow は現在 Ollama / Anthropic / OpenAI / DeepSeek の 4 プロバイダーを静的 config で管理している。
OpenClaw は 35+ プロバイダーを `provider/model` 形式で統一管理し、フォールバックチェーン・API キーローテーション・OAuth 認証・Thinking レベル制御を提供する。

**現状の差**:

| 項目 | OpenClaw | RenCrow |
|------|---------|---------|
| プロバイダー数 | 35+ | 4 |
| フォールバックチェーン | ✓ | ✗ |
| API キーローテーション | ✓ | ✗ |
| OAuth 認証（ChatGPT/Copilot/Gemini） | ✓ | ✗ |
| Thinking レベル（off〜xhigh） | ✓ | ✗ |
| /model 動的切替 | ✓ | ✗ |
| モデル品質ランクベース選択 | ✓ | △（CapabilityAdaptation v1.0 設計済み・未実装）|

---

## 2. OpenClaw の設計

### 2.1 provider/model 形式

```yaml
model: "anthropic/claude-opus-4-6"
model: "ollama/qwen3.5:27b"
model: "openai/gpt-4o"
```

プロバイダーとモデルを `/` 区切りで統一。設定ファイルで任意に差し替え可能。

### 2.2 フォールバックチェーン

```yaml
model:
  primary: "anthropic/claude-sonnet-4-6"
  fallbacks:
    - "openai/gpt-4o"
    - "ollama/qwen3.5:9b"
```

プライマリが利用不可の場合、順番に次を試みる。

### 2.3 API キーローテーション

複数 API キーをラウンドロビンし、レート制限（429）を検知したら自動的に次のキーに切り替える。

### 2.4 Thinking レベル

```
/think off | minimal | low | medium | high | xhigh
```

モデルに渡す「思考深度」を動的に変更。xhigh は Claude の extended thinking に相当。

---

## 3. RenCrow の現状

### 3.1 現行プロバイダー管理

`config.yaml` の `providers:` セクションに静的記述:

```yaml
providers:
  claude:
    model: "claude-sonnet-4-6"
    max_tokens: 8192
    temperature: 0.3
  ollama:
    chat_model: "Chat"
    worker_model: "Worker"
  openai:
    model: "gpt-4o-mini"
  deepseek:
    model: "deepseek-chat"
```

エージェント（Mio/Shiro/Coder1〜3）はプロバイダーを固定で持つ。

### 3.2 CapabilityAdaptation v1.0（設計済み・未実装）

`NodeCapabilities.LLMs[]` に `Quality` ランクを持ち、`SelectLLM()` でルートごとの最低品質要件を満たす LLM を選択する設計がある。
これが実装されれば OpenClaw のフォールバックチェーン相当の動作を得られる。

詳細: [07_機能拡張.md §3](../01_正本仕様/07_機能拡張.md)

---

## 4. 実装方針

### Priority 1: フォールバックチェーン

**CapabilityAdaptation v1.0 Phase 3** として実装予定。
`SelectLLMDegraded()` が minQuality 要件を下げて再試行する縮退動作が設計済み。

### Priority 2: 追加プロバイダー

近い将来に有用なプロバイダー候補:

| プロバイダー | 用途 | 優先度 |
|-----------|------|--------|
| Google Gemini | 高コンテキスト・マルチモーダル | 中 |
| Groq | 超高速推論（Ollama 補完） | 中 |
| LM Studio / vLLM | ローカル OpenAI 互換 | 低 |
| OpenRouter | マルチプロバイダーゲートウェイ | 低 |

追加時の変更範囲: `internal/infrastructure/llm/{provider}/` の新規実装のみ。`LLMProvider` インターフェースは変更不要。

### Priority 3: API キーローテーション

`providers.*.api_keys: []` で複数キーを受け付け、レート制限検知時に自動ローテーション。
`internal/infrastructure/llm/key_rotator.go` として LLMProvider ラッパーで実装する。

### Priority 4: /think コマンド

`ChatMessage` に `ThinkingLevel` フィールドを追加し、プロバイダーが対応していれば extended thinking を有効化。
Anthropic は `betas: ["interleaved-thinking-2025-05-14"]` + `thinking.budget_tokens` で対応可能。

---

## 5. スコープ外（RenCrow では不要と判断）

| 機能 | 理由 |
|------|------|
| OAuth 認証（ChatGPT/Copilot） | ライセンス・利用規約リスク |
| 25+ 海外専用プロバイダー | 現状の用途で不要 |

---

## 6. 既知の制約

- Ollama の `keep_alive: -1` 常駐管理は RenCrow 独自の最適化であり、OpenClaw に同等機能はない。これは維持する。
- Temperature は現行エージェント別に最適化済み。フォールバック先でも同じ値を引き継ぐことが必要。
