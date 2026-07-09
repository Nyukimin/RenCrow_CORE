# 実装仕様: OpenClaw移植 v1.0

**作成日**: 2026-03-09  
**ステータス**: 正本仕様（移植実装の一次参照）  
**統合ブランチ**: `integration/openclaw-parity`

---

## 1. 目的

本仕様は、OpenClawの能力を「Go基盤を維持したまま」段階移植するための実装仕様である。  
対象は機能単体ではなく、依頼を受けて実装完了まで遂行する **実装実行能力**。

---

## 2. 移植対象（Module Parity）

移植対象を以下4モジュールに固定する。

1. `Contract Layer`  
   - 依頼文 → 実行契約へ正規化
2. `Autonomous Executor`  
   - Plan→Apply→Verify→Repair→Verify
3. `Capability Pack`  
   - 例: TTS導入パック（実装・設定・検証）
4. `Evidence Layer`  
   - 実行証跡の永続化と表示

## 2.1 詳細仕様（分割実装）

実装は依存順で詳細仕様へ分割する。以下を一次参照とする。

1. `docs/02_OpenClaw移植詳細仕様/詳細実装仕様_01_実行基盤とセキュリティ境界.md`
2. `docs/02_OpenClaw移植詳細仕様/詳細実装仕様_02_チャネル網羅不足.md`
3. `docs/02_OpenClaw移植詳細仕様/詳細実装仕様_03_Tools体系の差.md`
4. `docs/02_OpenClaw移植詳細仕様/詳細実装仕様_04_Nodes_デバイス能力の差.md`
5. `docs/02_OpenClaw移植詳細仕様/詳細実装仕様_05_Gateway_Ops_CLIの差.md`
6. `docs/02_OpenClaw移植詳細仕様/詳細実装仕様_06_Security_Sandboxの差.md`
7. `docs/02_OpenClaw移植詳細仕様/詳細実装仕様_07_App_Platform導線の差.md`

---

## 3. Execution Contract 仕様

### 3.1 必須フィールド

- `goal`
- `acceptance`
- `constraints`
- `artifacts`
- `verification`
- `rollback`

### 3.2 契約ルール

- Coder出力は実行可能形式であることを必須化
- 非実行出力（設計文、疑似JSON、設定オブジェクトのみ）は reject
- reject時は再生成（上限回数あり）

### 3.3 完了判定

- `E2E実再生成功` を必須
- `ProcessMessage COMPLETE` 単体では完了扱いにしない

---

## 4. Autonomous Executor 仕様

### 4.1 実行順序

1. Contract生成
2. Coder提案取得
3. patch検証
4. 実行
5. E2E検証
6. 失敗時修復
7. 再検証
8. 証跡確定

### 4.2 失敗分類

- 依存不足
- 設定不備
- 実行環境不整合
- Provider利用不可
- patch形式不正

各分類ごとに修復ステップを固定し、無限ループ防止の最大反復を設ける。

---

## 5. TTS Capability Pack 仕様

### 5.1 対象

- 依頼: `TTSを導入して` / `TTS実装して`

### 5.2 Provider選択

優先順:

1. OpenAI
2. ElevenLabs
3. Local TTS

上位不可時は自動フォールバック。

### 5.3 受入条件（固定）

- 音声ファイル生成成功
- 実再生成功
- 実行証跡保存成功

---

## 6. 安全境界

完全自動運用を前提に、以下は禁止:

- `rm -rf` 等の破壊的削除
- `git reset --hard` 等の不可逆操作
- 機密情報の外部送信

---

## 7. Evidence 仕様

`execution_report` に以下を必須保存:

- jobID
- 入力契約
- 実行ステップ
- 検証結果
- 生成音声情報
- 最終判定

Viewerでは証跡を一次情報として表示する。

---

## 8. テスト仕様

### 8.1 Contract Test

- 曖昧依頼が実行契約へ正規化される
- 非実行出力をrejectできる

### 8.2 Executor Test

- Plan→Apply→Verify→Repair の遷移が成立
- 失敗分類ごとの修復ロジックが動作

### 8.3 E2E Test

- `TTSを導入して` で実再生まで完了
- `execution_report.status=passed`

### 8.4 Regression Test

- 既存ルーティング/分散実行/IdleChat停止条件に回帰なし

---

## 9. 進行管理

- 統合先: `integration/openclaw-parity`
- 機能ブランチ:
  - `feat/openclaw-contract-layer`
  - `feat/openclaw-autonomous-executor`
  - `feat/openclaw-tts-capability-pack`
  - `feat/openclaw-evidence-layer`
