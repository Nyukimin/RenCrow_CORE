# Autonomous Executor 残課題

**最終更新**: 2026-03-19

---

## 概要

`Autonomous Executor` の共通実装は `fac526d Implement shared autonomous executor flow` で導入済み。

現時点の残課題は「実装そのものが未着手」というより、runtime 検証、監視整合、周辺未完機能の詰めが中心。

---

## 現在の到達点

- `CODE / OPS / PLAN / ANALYZE / RESEARCH` が共通 executor を通る
- `CHAT` は従来の会話フローを維持
- route-aware contract 正規化を導入
- `execution_report` に `route`, `capability_pack`, `failure_reason`, `constraints`, `artifacts`, `rollback`, `attempt_count` を保存
- `/entry` と通常 orchestrator で共通の evidence モデルを使用
- テスト通過
  - `internal/application/autonomous`
  - `internal/application/contract`
  - `internal/application/orchestrator/...`
  - `go test -c ./cmd/rencrow`

---

## 残課題

### 1. route 別 runtime 検証の完了

要確認:
- `CODE`
- `OPS`
- `PLAN`
- `ANALYZE`
- `RESEARCH`

確認観点:
- `entry.stage` が `received -> contract_ready -> planning -> applying -> verifying -> completed/failed` に沿って出るか
- `execution_report` に最終状態が保存されるか
- retry 後成功時に `Jobs` が最終的に `done` に収束するか
- `CHAT` が executor を通らず従来通り動くか

注意:
- 確認中、`/viewer/send` は叩き方により不安定だった
- `/health` と `/viewer/status` は正常
- handler 障害ではなく、検証手順側の揺れの可能性が高い

### 2. Viewer の live jobs / evidence 整合の詰め

現状:
- executor 導入で evidence は強化された
- ただし途中失敗を含む job の見せ方はまだ改善余地あり

確認・改善ポイント:
- `failure_kind` が途中失敗として残りつつ、最終成功 job は `done` を優先表示する
- `job detail` で途中失敗と最終成功を混同しない
- `attempt_count`, `repair_count` を UI で見せるか判断する

### 3. TTS Capability Pack の完成

現状:
- `tts_delivery` は executor 構造へ寄せたが、仕様上はまだ部分実装

残り:
- synth / playback / repair の failure taxonomy 整備
- provider unavailable 時の fallback 明確化
- TTS 正本仕様書との整合

### 4. TTS 正本仕様の更新

対象:
- `docs/TTS仕様/TTS感情音声システム仕様.md`

現状:
- `Draft`
- 実装途中の現行状態とズレがある

方針:
- archive ではなく、現況仕様へ更新する

### 5. STT は対象外

整理:
- `STT` はペンディング
- 現時点で実装予定なし
- Autonomous Executor 残課題には含めない

---

## 推奨順序\n\n1. route 別 runtime 検証\n2. Viewer / evidence 整合\n3. TTS Capability Pack の完成\n4. TTS 正本仕様更新

---

## メモ

- 作業ツリーには executor 実装とは別にルート `README.md` の未コミット差分が残っていた
- executor commit には含めていない
