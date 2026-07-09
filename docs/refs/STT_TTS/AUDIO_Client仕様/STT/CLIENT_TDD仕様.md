# Client TDD仕様（RenCrow_STT）

## 1. 目的

本書は、Chat から RenCrow_STT を組み込む実装を **TDD（Red -> Green -> Refactor）** で進めるための実行仕様です。  
実装者は `実装仕様.md` と本書をセットで参照してください。

---

## 2. スコープ

### 対象

- `GET /health`
- `GET /ready`
- `WS /stt`（主経路）
- `session_info` / `speech_start` / `draft` / `final` / `status` / `error`
- `config` / `final_pending` / 音声バイナリ送信
- `error.code` 別復旧処理

### 非対象

- STT エンジン内部（whisper.cpp / faster-whisper）の実装
- LLM 応答生成
- TTS 合成ロジック

---

## 3. TDD実施ルール（必須）

1. **Red**: 先に失敗テストを追加し、失敗を確認する
2. **Green**: 最小実装で対象テストのみ通す
3. **Refactor**: 回帰を維持したまま重複を除去する
4. 1サイクルは 1論点（1テストID）に限定する
5. 分岐判定は `error.code` で行い、文言一致で分岐しない

禁止事項:

- 失敗テスト未確認での実装着手
- 1PRで複数論点を同時に扱うこと
- `error.text` の文字列一致分岐

---

## 4. テストID規約

- ヘルスチェック: `CT-STT-HL-*`
- 接続/セッション: `CT-STT-WS-*`
- 音声送信/確定: `CT-STT-AU-*`
- エラー復旧: `CT-STT-ER-*`
- 統合フロー: `CT-STT-IF-*`

---

## 5. TDDバックログ（優先順）

## P1（必須）

- `CT-STT-HL-001`: `GET /health` が `{"ok":true}`
- `CT-STT-HL-002`: `GET /ready` が JSON を返却（`ready` フィールド必須）
- `CT-STT-WS-001`: `/stt` 接続直後に `session_info` 受信
- `CT-STT-AU-001`: `config` 送信後に音声送信できる
- `CT-STT-AU-002`: 音声送信で `final` を受信
- `CT-STT-ER-001`: `PROVIDER_UNAVAILABLE` で再接続バックオフ
- `CT-STT-ER-002`: `INVALID_PAYLOAD` をユーザー通知できる
- `CT-STT-IF-001`: `final` を既存 Router/Worker に投入できる

## P2（重要）

- `CT-STT-WS-002`: 互換経路 `/ws`, `/stt-ws` の接続確認（後方互換）
- `CT-STT-AU-003`: `final_pending` 送信で最終確定を促進
- `CT-STT-ER-003`: `PROVIDER_TIMEOUT` 時の再接続と継続動作
- `CT-STT-ER-004`: `AUDIO_TOO_SHORT` の再入力誘導

## P3（補強）

- `CT-STT-WS-003`: `draft` 無効時に `draft` 非受信であること
- `CT-STT-WS-004`: `status` イベントの表示整合性
- `CT-STT-IF-002`: `reply_*` 非依存で会話継続可能

---

## 6. サイクルテンプレート（実運用）

### 6.1 Red

- 失敗テスト追加（ID付き）
- 例: `go test ./... -run TestCT_STT_WS_001`
- 期待: 失敗

### 6.2 Green

- 最小実装のみ追加
- 期待: 対象IDテスト成功

### 6.3 Refactor

- 重複除去（接続ヘルパー、イベントデコード共通化）
- 期待: 対象ID + 回帰テスト成功

---

## 7. 受け入れ完了条件（DoD）

- P1 が全件 Green
- P2 は全件 Green、または未実施理由を記録
- `error.code` 分岐が全実装で統一
- 変更対象のLintが成功
- テストIDと仕様項目の追跡が可能

---

## 8. 実行コマンド例（サーバ起動済み前提）

```bash
curl -sS http://192.168.1.36:8090/health
curl -sS http://192.168.1.36:8090/ready
wscat -c ws://192.168.1.36:8090/stt
```

WS 接続後:

```json
{ "type": "config", "mimeType": "audio/wav" }
```

`session_info` と `final` の受信を確認する。

