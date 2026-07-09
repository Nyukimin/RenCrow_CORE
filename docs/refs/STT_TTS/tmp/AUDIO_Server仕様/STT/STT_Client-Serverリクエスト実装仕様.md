# STT Client-Serverリクエスト実装仕様（AUDIO_Client仕様）

## 1. 目的
本書は、`Client(Viewer/Browser)` から `Server(RenCrow STT Gateway)` へ送るリクエスト仕様を実装レベルで定義する。  
Server 内部の Provider 実装差分は本書の対象外とし、Client から見える契約のみを扱う。

## 2. スコープ
- 対象:
  - Client -> Server の WebSocket 接続
  - Client -> Server の制御メッセージ
  - 音声バイナリ送信
  - 受信イベント（`speech_start`/`draft`/`final`/`error`）の扱い
- 非対象:
  - Server -> Provider 実装詳細
  - STT モデル選定/学習
  - UIデザイン

## 3. 責務分離（必須）
- Client の責務:
  - マイク開始/停止（ユーザー操作にのみ連動）
  - 音声データ送信
  - Server 返信イベントの表示
- Server の責務:
  - 無音判定/発話区切り判定
  - `draft` / `final` の確定
  - timeout/retry/fail-open の制御

不変条件:
- Client は無音判定ロジックを持たない。
- Client は STT 接続断を契機にマイクを自動停止しない（再接続は通信層で吸収）。

## 4. 接続仕様

### 4.1 Endpoint
- URL: `wss://<chat-host>/stt-ws`（HTTPS環境）
- ローカル検証: `ws://127.0.0.1:18790/stt-ws`

### 4.2 接続ライフサイクル
1. User がマイク開始
2. Client が `/stt-ws` へ接続
3. `config` 送信
4. 音声バイナリを周期送信
5. User 停止時に `final_pending` 送信（任意だが推奨）
6. 接続クローズ

## 5. Client -> Server リクエスト契約

### 5.1 `config`（JSON）
```json
{ "type": "config", "mimeType": "audio/wav" }
```

要件:
- 接続直後に1回送る
- 互換のため `mimeType` は必須

### 5.2 音声バイナリ（binary）
- 推奨形式: WAV (mono, 16kHz, PCM16)
- 送信間隔目安: 300ms〜700ms
- 最小送信サイズは Server 側設定に従う

### 5.3 `final_pending`（JSON）
```json
{ "type": "final_pending" }
```

用途:
- ユーザー停止時に最終確定を促すための明示トリガー

## 6. Server -> Client 応答イベント契約

### 6.1 `speech_start`
```json
{ "type": "speech_start" }
```

### 6.2 `draft`
```json
{ "type": "draft", "text": "..." }
```

### 6.3 `final`
```json
{ "type": "final", "text": "..." }
```

### 6.4 `error`
```json
{ "type": "error", "error": "..." }
```

### 6.5 `status`（任意運用）
```json
{ "type": "status", "text": "stt provider timeout (retrying)" }
```

## 7. エラー時の Client 挙動（必須）
- `error` 受信時:
  - セッション継続（即停止しない）
  - ユーザーに非破壊通知
- `status` 受信時:
  - 進行状態として表示
  - エラー扱いにしない
- WebSocket切断時:
  - 録音状態は維持
  - 接続のみ再試行（短いバックオフ）

## 8. 実装チェックリスト（Client側）
- [ ] マイク開始/停止がユーザー操作にのみ連動する
- [ ] `/stt-ws` 接続直後に `config` を送る
- [ ] 音声バイナリを定周期で送信する
- [ ] `speech_start`/`draft`/`final`/`error` を表示できる
- [ ] WS切断時に録音を止めず再接続する
- [ ] 停止時 `final_pending` を送る

## 9. 検証コマンド（最小）
```bash
# 接続確認（Server側）
curl -i -sS https://<chat-host>/stt-ws

# WebSocket疎通
wscat -c wss://<chat-host>/stt-ws --no-check
# -> {"type":"config","mimeType":"audio/wav"} を送信

# Server準備状態
curl -sS https://<chat-host>/ready
```

## 10. 補足
- 本書は「Client -> Server リクエスト契約」の正本である。
- Server -> Provider の詳細は `AUDIO_Server仕様/STT` 側文書を参照する。

## 11. 本書の実装に必要な情報（作業の意味と実態リンク）

### 11.1 この作業の意味
- Client 側実装者が「何を送るべきか」「何を受け取るべきか」を固定し、実装ごとの解釈差をなくすため。
- Server 側変更時に、Client 互換性が壊れていないかを確認する基準点にするため。
- 仕様・実装・検証（DoD）の三点を同じ用語でつなぎ、保守コストを下げるため。

### 11.2 まず確認すべき正本（仕様）
- STT API 契約: [`STT_API.md`](./STT_API.md)
- STT 要求仕様: [`STT仕様.md`](./STT仕様.md)
- STT 実装仕様: [`STT実装仕様.md`](./STT実装仕様.md)
- 現状サマリ: [`STT_Server側_現状サマリ.md`](./STT_Server側_現状サマリ.md)
- DoD 記録: [`STT_API_DOD_2026-04-10.md`](./STT_API_DOD_2026-04-10.md)

### 11.3 実態（現行コード）へのリンク
- STT Gateway 主実装（HTTP/WS + VAD + Whisper 連携）  
  [`webui/voice-bridge/server.js`](../../../../../webui/voice-bridge/server.js)
- STT Gateway HTTPS 実装（挙動差分確認用）  
  [`webui/voice-bridge/server-https.js`](../../../../../webui/voice-bridge/server-https.js)

### 11.4 本書の各項目と実態の対応

| 本書の論点 | 作業の意味 | 実態リンク |
|---|---|---|
| 接続エンドポイント (`/stt-ws`) | Client が接続失敗しないための入口固定 | `server.js` / `server-https.js` の `WebSocketServer` path 設定 |
| `config` 送信 (`mimeType` 必須) | 音声フォーマット解釈を一致させるため | `server.js` / `server-https.js` の `msg.type === 'config'` 分岐 |
| binary 音声送信 | STT 推論の入力品質を担保するため | `server.js` の WAV 判定・VAD 処理、`server-https.js` の `transcribeBuffer` 呼び出し |
| `final_pending` | 停止操作時の最終確定制御を明示するため | `server.js`（no-op 互換処理）/ `server-https.js`（final待ち処理） |
| `speech_start`/`draft`/`final` | Client 表示更新タイミングを統一するため | `server.js` / `server-https.js` の `send({ type: ... })` |
| `error`/`status` | 障害時も UX を壊さず継続運用するため | `server.js` / `server-https.js` のエラー送信・ログ出力周辺 |

### 11.5 実装時の確認順（推奨）
1. 本書の契約を確定（本ファイル）
2. `STT_API.md` で API 粒度を確認
3. `server.js` / `server-https.js` の現行挙動を照合
4. `STT_API_DOD_2026-04-10.md` と `API_DOD_CHECKLIST.md` で検証項目を確定

### 11.6 現状差分（仕様 vs 実装）と対応優先度

| 優先度 | 論点 | 仕様（本書） | 現状実装 | 対応方針（作業の意味） | 実態リンク |
|---|---|---|---|---|---|
| 高 | WS エンドポイント | `/stt-ws` | `/ws` | Client 契約を固定するため、`/stt-ws` を正規入口として実装し、必要なら `/ws` を後方互換に残す | [`webui/voice-bridge/server.js`](../../../../../webui/voice-bridge/server.js), [`webui/voice-bridge/server-https.js`](../../../../../webui/voice-bridge/server-https.js) |
| 高 | `final_pending` の意味 | 最終確定を促す明示トリガー | `server.js` は no-op、`server-https.js` は final 待ちで実装差あり | 停止時の確定挙動を一貫させるため、主系実装を 1 つの契約に統一する | [`webui/voice-bridge/server.js`](../../../../../webui/voice-bridge/server.js), [`webui/voice-bridge/server-https.js`](../../../../../webui/voice-bridge/server-https.js) |
| 中 | `error` ペイロード | `{ "type": "error", "error": "..." }` | `{ "type": "error", "text": "..." }` | Client 例外処理の分岐を単純化するため、キー仕様を統一する | [`webui/voice-bridge/server.js`](../../../../../webui/voice-bridge/server.js), [`webui/voice-bridge/server-https.js`](../../../../../webui/voice-bridge/server-https.js) |
| 中 | `status` イベント | 任意だが契約例あり | 未送信 | timeout/retry/fail-open 状態を可視化し、UI 側の非破壊通知を安定化する | [`webui/voice-bridge/server.js`](../../../../../webui/voice-bridge/server.js) |
| 中 | retry 制御 | Server 責務として明示 | `STT_MAX_RETRY` 設定はあるが実 retry は限定的 | Provider 不安定時の回復性を上げるため、retry 戦略を仕様に合わせて実装する | [`webui/voice-bridge/server.js`](../../../../../webui/voice-bridge/server.js), [`STT_API.md`](./STT_API.md) |
| 低 | ローカル検証値 | `ws://127.0.0.1:18790/stt-ws` | 既定ポートが 8090 系 | 検証手順の混乱防止のため、運用ポート方針を仕様に合わせて明文化する | [`STT_API_DOD_2026-04-10.md`](./STT_API_DOD_2026-04-10.md), [`webui/voice-bridge/server.js`](../../../../../webui/voice-bridge/server.js) |

