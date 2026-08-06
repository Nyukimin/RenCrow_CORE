---
title: 調査 — CORE失敗テストの修正
date: 2026-08-06 07:31 UTC
status: resolved
skill: debug-investigate
symptom: CORE全体テストでMio表現履歴、Mioペルソナ、会話要約fallbackの3テストが失敗
frequency: ローカル全体実行で再現。要約fallbackは非同期経路のため実行順で顕在化
inputs: テスト出力とソースコード
related: prompts/mio.md, internal/domain/agent/mio_prompt_context.go, internal/infrastructure/persistence/conversation/real_manager_threads.go
---

## 概要

失敗は実装の破損ではなく、テストの期待値が現在の正本・実装契約からずれていたことが原因だった。
テスト入力・期待文字列・fallback要約形式を現行契約に合わせ、対象テストとCOREのsource hierarchy全体が成功した。

## 調査経緯

### 仮説1: 表現履歴の上限実装が壊れている

- **根拠**: 8件投入時のテストが「各3件」を期待していた。
- **検証結果**: 棄却。
- **証拠**:
  - `normalizeMioHistoryItems`は同一表現を意図的にdeduplicateし、8個の同一`opening`は1件になる。
  - 一意な8件へ変更すると、各カテゴリの最新3件・900 rune以内の制約を満たした。
- **チェックリスト結果**:
  - □ 確証バイアス: 同一入力のdeduplicateを反証として確認。
  - □ 頻度制約: 毎回決定的なテスト入力で、頻度依存なし。
  - □ ライフサイクル: 履歴の正規化前後を確認。
  - □ 既存知見: 近接表現の重複抑止という既存契約と整合。

### 仮説2: Mioの固定人格が変更された

- **根拠**: テストだけが`ミオ（澪）`を要求していた。
- **検証結果**: 棄却。
- **証拠**:
  - `prompts/mio.md`と`DefaultMioPersona`は`Mio（澪）`を正本表記としている。
  - Chat Agent、patch適用境界など他の期待値は同じ固定promptで成立した。
- **チェックリスト結果**:
  - □ 確証バイアス: 実装ではなく正本promptを反証確認。
  - □ 頻度制約: deterministicな文字列比較で頻度依存なし。
  - □ ライフサイクル: persona生成からprompt保持まで確認。
  - □ 既存知見: canonical prompt-only policyと整合。

### 仮説3: background summaryのfallback保存が失敗している

- **根拠**: archiveに要約があるのに`Start:` prefix assertionが失敗していた。
- **検証結果**: 棄却。
- **証拠**:
  - `generateSimpleSummary`の実形式は`Start [speaker]: ...`であり、archive保存とRedis削除は完了していた。
  - `waitForBackgroundJobs`でgoroutine完了を待機後、期待prefixを`Start [`へ合わせると成功した。
- **チェックリスト結果**:
  - □ 確証バイアス: archive件数、summary形式、Redis削除を個別確認。
  - □ 頻度制約: timeout設定は5msだが、待機契約で完了を同期確認。
  - □ ライフサイクル: enqueue、timeout、simple fallback、archive、Redis deleteを確認。
  - □ 既存知見: fallbackは保存後にthreadを削除する既存契約と整合。

## 根本原因

- 表現履歴テストがdeduplicate前提を考慮せず同一値を投入していた。
- Personaテストの表記がcanonical promptの`Mio（澪）`から古い`ミオ（澪）`へずれていた。
- Summary fallbackテストのprefixが実装形式`Start [`と一致していなかった。

## 修正

1. 表現履歴テストを一意な8値で上限3件を検証するよう修正。
2. Personaテストの期待値を`Mio（澪）`へ修正。
3. fallback要約テストのprefixを`Start [`へ修正。

## 検証

- `go test ./internal/domain/agent ./internal/domain/conversation`
- `go test ./internal/infrastructure/persistence/conversation`
- `go test ./application/... ./cmd/... ./internal/... ./modules/...`
- `go vet ./cmd/... ./internal/... ./modules/...`
- `go build ./cmd/... ./internal/... ./modules/...`

すべて成功した。今回の修正ではruntime、Trade経路、Persona正本prompt本体を変更していない。

## 教訓

- 上限テストは重複除去の有無を明示し、同一値だけで件数を検証しない。
- 固定promptの文字列期待値は`prompts/`の正本と同期させる。
- 非同期fallbackテストは完了待機後に、実装の形式契約を検証する。
