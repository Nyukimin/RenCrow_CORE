---
title: 調査 — CORE CLI durationとIdleChat summary契約不整合
date: 2026-08-26 17:16
status: confirmed
skill: debug-investigate
symptom: health CLIのduration_msがナノ秒値になり、正規のIdleChat 3点summaryがsanitizerで拒否される
frequency: durationは全CLI JSON出力で再現、summaryは直近4 session中2件
inputs: production CLI出力、IdleChat sessionログ、CORE sourceとtest
---

## 概要

2事象はともにLLMや物理backendの失敗ではなく、CORE出力境界の単位変換漏れと、
summary用契約へ通常発話用の構造拒否を誤適用した実装不整合だった。正本ルートと
domain型を変えず、CLI projectionとsummary sanitizerの責務境界で是正する。

## 調査経緯

### 仮説1: `time.Duration`の直列化が`duration_ms`の単位契約と食い違う

- **根拠**: 実測約2msのcheckがCLI JSONで約2,000,000となる一方、HTTPとtext statusは数msだった。
- **検証結果**: 確認。
- **証拠**:
  - `internal/domain/health.CheckResult.Duration`は`time.Duration`に`json:"duration_ms"`を付けている。
  - `runHealthCommand`と`status --deep --json`はその値を直接JSON化する。
  - HTTP health adapterとtext statusは`Duration.Milliseconds()`を明示的に使うため正常。
- **チェックリスト結果**:
  - 確証バイアス: HTTP、text、CLI JSONの3経路を比較し、domain全体ではなくCLI adapterだけの問題と切り分けた。
  - 頻度制約: Go JSON化は毎回同じなため、CLI JSONでの常時再現と一致する。
  - ライフサイクル: check実行からreport生成、CLI投影、HTTP投影まで追跡した。

### 仮説2: summaryが要求する番号付き3点を通常発話用漏洩検査が拒否する

- **根拠**: 生応答は指定された3観点を`1.`〜`3.`で返していたが、`summary sanitize failed`となった。
- **検証結果**: 確認。
- **証拠**:
  - summary promptは「いちばん面白かった点、話を前に進めた点、次に広がりそうな観点」の順を求める。
  - `hasInternalReasoningLeak`は複数の番号付き行を一律に内部推論と判定する。
  - sanitizer失敗時は会話本文の先頭200文字をsummaryとして保存するため、sessionとTTSは完了しても品質契約は未達になる。
- **チェックリスト結果**:
  - 確証バイアス: provider失敗と判断せず生応答を確認し、生成は成功したsanitizer失敗と分離した。
  - 頻度制約: 番号付きで返したsessionだけ失敗するため、4件中2件という間欠性に整合する。
  - ライフサイクル: generate、visible answer抽出、leak検査、fallback、保存、TTSまで追跡した。
  - 既存知見: 通常発話での推論漏洩拒否は弱めず、summary出力だけを別契約にする。

## 根本原因

- CLIは内部表現とwire表現を分けるprojectionを持たず、名前だけがmsのfieldへナノ秒の値を出した。
- IdleChatは「内部推論を拒否する」知識と「通常発話は構造化listにしない」知識を同じ関数に混ぜ、構造化summaryへ再利用した。

## 修正方針

1. CLI用health check projectionで`Milliseconds()`を一度だけ適用し、全3つのCLI JSON経路から再利用する。
2. 共通の漏洩marker検査と、通常発話の構造化list拒否を分ける。summaryは前者を維持したまま3点listを受理する。
3. 修正前の値と実際の生応答形式を固定した回帰testを先に失敗させる。

## 関連ソースファイル

- `cmd/rencrow/health_commands.go`
- `internal/domain/health/check.go`
- `internal/adapter/health/handler.go`
- `internal/application/idlechat/orchestrator_summary.go`
- `internal/application/idlechat/orchestrator_sanitize.go`
- `internal/application/idlechat/orchestrator_sanitize_leak.go`

## 教訓

- field名の単位は内部型のserializerへ暗黙に依存せず、adapterのcontract testで実値を固定する。
- sanitizerは「不正な内容」と「そのconsumerで許さない表現形式」を分離し、異なる出力契約間で後者を共有しない。

## 検証記録

- RED: CLIの5msと50msがそれぞれ`5e+06`と`5e+07`になることを回帰testで確認した。
- RED: 番号付き3点summaryが空になり、`summarizeByWorker`がtranscriptへfallbackすることを確認した。
- GREEN: `go test ./cmd/rencrow ./internal/application/idlechat -count=1`が成功した。
- GREEN: `go vet ./cmd/rencrow ./internal/application/idlechat`と`go build ./cmd/rencrow ./internal/application/idlechat`が成功した。
- 回帰: `go test ./application/... ./cmd/... ./internal/... ./modules/... ./pkg/... -count=1`が成功した。
- `go test ./...`はrepo内の`Tmp/test-runtime`にあるGoの一時build生成物までpackageとして走査するため
  setupで失敗した。これはsource packageの失敗と分離し、上記の明示source root回帰で検証した。
- 配備後のCLIとIdleChat正規経路の結果は、後続検証後に追記する。
