# 75 Viewer 音声直結 LLM Streaming 実装作業仕様

## 1. 位置づけ

| 項目 | 内容 |
| --- | --- |
| 設計仕様 | `74_Viewer音声直結LLM_Streaming仕様.md` |
| WS 契約 | `74_Viewer音声直結LLM_WS契約.md` |
| 参照実装パターン | `40_STT_Streaming実装作業仕様.md` / `cmd/rencrow/stt_runtime_websocket.go` |
| 本書の役割 | **Phase 1（Utterance Streaming MVP）** の実装対象・変更ファイル・状態機械・検証・停止条件を定義する |

本書は protocol の再定義ではない。契約の変更は 74 系を正とし、本書は **どこをどう実装するか** の作業仕様である。

---

## 2. 実装スコープ

### 2.1 Phase 1 実装対象

| # | 対象 | 内容 |
| --- | --- | --- |
| R1 | RenCrow_LLM | `/v1/chat/audio/sessions` WebSocket。utterance 単位 PCM 受信 → `session.commit` 後 streaming 推論 |
| P1 | rencrow | `/voice-chat` bridge。Viewer↔LLM 透過 + orchestrator 連携 + SSE イベント |
| V1 | Viewer | `voice_input_mode` 対応。`vds_sub` モードでマイク→VDS WS→Chat streaming 表示 |
| T1 | テスト | contract test / e2e probe / browser gate |
| O1 | Ops | runtime-config 拡張、debug panel、計測フィールド |

### 2.2 Phase 1 非対象（明示的に触らない）

- `parallel_caption`（Phase 1b。`vds_sub` 合格後）
- VDS 失敗時の STT final 自動 fallback（Phase 3）
- 発話中 incremental audio → LLM（Phase 2）
- Phase 0 `/viewer/send` WAV 一括 `input_audio` 経路の変更・削除
- IdleChat への VDS 接続
- Browser → RenCrow_LLM 直接接続
- RenCrow_STT server 側の protocol 変更
- Viewer 添付トレイへの WAV 追加（Phase 0 経路強化。VDS 本体ではない）

### 2.3 既定動作（回帰防止）

- `voice_input_mode` 未設定 / 不明値 → **`stt_primary`**
- `VOICE_CHAT_ENABLED=false`（既定）→ `/voice-chat` は **503 + error frame** または HTTP upgrade 拒否
- `stt_primary` 時の Viewer STT 挙動は **一切変更しない**

---

## 3. PR 分割（推奨）

| PR | 内容 | マージ条件 |
| --- | --- | --- |
| PR-L1 | RenCrow_LLM audio session WS + Python contract tests | LLM 単体 e2e green |
| PR-P1 | rencrow bridge + mock LLM tests | Go unit + mock WS green |
| PR-P2 | orchestrator `ProcessVoiceDirect` + SSE events | orchestrator contract test green |
| PR-V1 | Viewer `vds_sub` + runtime-config | browser/contract test green |
| PR-T1 | `scripts/vds_e2e_probe.py` + golden 計測 doc | A/B 記録 attached |
| PR-V2 | `parallel_caption` | PR-V1 + STT 回帰 green |

**禁止**: PR-L1〜P2 未完了で Viewer から LLM 直叩きを追加すること。

---

## 4. 設定・環境変数

### 4.1 rencrow

| キー | 既定 | 説明 |
| --- | --- | --- |
| `VOICE_CHAT_ENABLED` | `false` | `true` で `/voice-chat` 有効 |
| `RENCROW_LLM_CHAT_WS` | 空 | 明示 WS URL。例: `ws://192.168.1.207:8081/v1/chat/audio/sessions` |
| `VOICE_CHAT_GATEWAY_URL` | 空 | bridge 先。未設定時は Chat HTTP base から導出 |
| `VOICE_CHAT_DEFAULT_PROMPT` | 下記 | `session.start.prompt` 空時の server 既定 |
| `VOICE_CHAT_MIN_UTTERANCE_MS` | `300` | これ未満は `UTTERANCE_TOO_SHORT` |
| `VOICE_CHAT_COMMIT_TO_DELTA_TIMEOUT_SEC` | `30` | commit→初 delta |
| `VOICE_CHAT_COMMIT_TO_FINAL_TIMEOUT_SEC` | `120` | commit→final |

既定プロンプト（`VOICE_CHAT_DEFAULT_PROMPT`）:

```text
この音声を聞いて、話している内容を日本語で要約し、最後に数字も書き出してください。
```

Chat WS URL 導出（`RENCROW_LLM_CHAT_WS` 未設定時）:

```text
local_llm.chat_base_url (http://host:8081)
  -> ws://host:8081/v1/chat/audio/sessions
```

実装: `inferVoiceChatGatewayURL()` を `inferSTTGatewayURL()` と同型で追加する。

### 4.2 Viewer runtime-config 拡張

`RuntimeConfig`（`internal/adapter/viewer/debug_system_handler.go`）に追加:

```go
VoiceChatStreamURL string `json:"voice_chat_stream_url,omitempty"`
VoiceInputMode     string `json:"voice_input_mode,omitempty"` // stt_primary | vds_sub | parallel_caption
VoiceChatEnabled   bool   `json:"voice_chat_enabled"`
```

生成規則:

- `voice_chat_stream_url`: リクエスト same-origin の `wss://host/voice-chat`（STT と同じ Tailscale 規則）
- `voice_chat_enabled`: `VOICE_CHAT_ENABLED && gateway configured`
- `voice_input_mode`: config または env。未設定は `stt_primary`

---

## 5. RenCrow_LLM 実装（PR-L1）

### 5.1 新規ファイル（案）

| ファイル | 責務 |
| --- | --- |
| `RenCrow_LLM/src/llm_server/audio_session_server.py` | WS session handler、PCM buffer、commit 後推論 |
| `RenCrow_LLM/src/llm_server/audio_session_types.py` | event DTO / validation |
| `RenCrow_LLM/tests/test_audio_session_contract.py` | WS 契約テスト |
| `RenCrow_LLM/docs/audio_session_ws.md` | LLM 側契約正本（74 WS 契約 §5 と同期） |

### 5.2 エンドポイント

- Path: `/v1/chat/audio/sessions`
- Upgrade: WebSocket
- Role: **Chat プロセスのみ**（Worker/Heavy/Wild 拒否）

### 5.3 セッション状態機械（LLM 側）

```text
IDLE
  | session.start (valid)
  v
RECEIVING
  | binary PCM*
  | session.commit
  v
INFERENCING
  | llm.delta*
  | llm.final
  v
IDLE

任意遷移:
  RECEIVING | INFERENCING -- session.cancel --> IDLE
  * -- error --> IDLE
```

**不変条件**:

- 1 接続あたり同時 INFERENCING は **1**
- `llm.final` 後、同一 `utterance_id` で `llm.delta` を返さない
- `llm.final` 後、同一 `utterance_id` で矛盾 `error` を返さない（STT final 契約と同型）

### 5.4 commit 後推論

1. 受信 PCM を一時 WAV に書き出す（既存 `input_audio` path 再利用可）
2. Chat backend へ **stream:true** で multimodal request
3. backend SSE delta → `llm.delta` に変換（`seq` 单调増加）
4. 完了 → `llm.final` + metrics

**Phase 1 では commit 後推論のみ**。発話中推論は Phase 2。

### 5.5 モデル・リソース

- `session.start` で受け取る `model` は `Chat` のみ受理
- Chat プロセスの単一リクエスト制約を守る（推論中の新 `session.start` は `error: LLM_BUSY`）

### 5.6 LLM 側テスト（必須）

`tests/test_audio_session_contract.py`:

- [ ] start → ready
- [ ] PCM chunks → progress
- [ ] commit → delta* → final
- [ ] cancel で infer しない
- [ ] too short → UTTERANCE_TOO_SHORT
- [ ] final 後 error なし
- [ ] golden WAV（PCM 再生）で非空 final

---

## 6. rencrow 実装（PR-P1 / PR-P2）

### 6.1 新規・変更ファイル

| ファイル | 責務 |
| --- | --- |
| `cmd/rencrow/voice_chat_runtime_websocket.go` | `/voice-chat` handler。Viewer WS accept |
| `cmd/rencrow/voice_chat_runtime_config.go` | env / URL 導出 |
| `cmd/rencrow/voice_chat_runtime_bridge.go` | LLM WS 透過、event 正規化 |
| `cmd/rencrow/voice_chat_runtime_websocket_test.go` | mock LLM upstream の frame 透過 test |
| `cmd/rencrow/routes.go` | route 登録 |
| `modules/voicechat/contracts.go` | **新 module**。event 型、route path、error code |
| `modules/voicechat/bridge_plan.go` | enabled / gateway URL plan（`modules/stt` 同型） |
| `internal/application/orchestrator/voice_direct.go` | `ProcessVoiceDirectRequest` |
| `internal/application/orchestrator/message_orchestrator.go` | 公開メソッド追加（既存 ProcessMessage は変更最小） |
| `internal/adapter/viewer/debug_system_handler.go` | runtime-config 拡張 |

### 6.2 ルート登録

`registerSTTAndAudioRoutes` または隣接関数に追加:

```go
registerVoiceChatRoutes(mux, voiceChatRuntime)
```

Paths（`modules/voicechat/contracts.go`）:

- `/voice-chat`（primary）
- `/voice-chat-ws`（alias）

`VOICE_CHAT_ENABLED=false` 時:

- WebSocket upgrade 前に JSON error を返すか、upgrade 後即 `error{VOICE_CHAT_DISABLED}` を送って close

### 6.3 bridge 責務（STT bridge と同型）

RenCrow voice-chat bridge は **LLM 応答を生成しない**。transport boundary である。

| 方向 | 動作 |
| --- | --- |
| Viewer → LLM | text/binary frame を破壊せず転送 |
| LLM → Viewer | text frame を破壊せず転送 |
| binary from LLM | 来ても破壊しない（Phase 1 非想定） |

**禁止（STT 73 仕様と同じ教訓）**:

- commit 時の独自 HTTP `input_audio` fallback
- LLM final 前の cached delta 独自 final 化
- `/viewer/send` への暗黙フォールバック

### 6.4 orchestrator 連携（PR-P2）

#### 新規 DTO

```go
type ProcessVoiceDirectRequest struct {
    UtteranceID   string
    SessionID     string
    Channel       string
    ChatID        string
    ViewerSession string
    Prompt        string
    SampleRate    int
    Channels      int
    // commit 時点で LLM へ渡す。bridge から PCM/WAV path または reader
    AudioWAVPath  string
}
```

#### フロー

```text
voice_chat bridge: session.commit 受信
  -> PCM buffer を WAV 化（tmp、defer remove）
  -> jobID 採番
  -> emit routing.decision (route=CHAT, reason=voice_direct)
  -> ProcessVoiceDirect(ctx, req)
       -> LLM provider streaming（既存 OpenAI stream path）
       -> on token: emit llm.delta to Viewer WS + metrics.latency first_token
  -> on complete: emit llm.final to Viewer WS + agent.response SSE + metrics.latency response_complete
```

**IdleChat へ流さない**。`isVoiceChatAllowed()` 相当の server 側ガードを orchestrator 入口に置く。

#### SSE イベント（既存再利用）

| event | 条件 |
| --- | --- |
| `routing.decision` | VDS job 開始時 |
| `metrics.latency` | `network/server_received`, `llm/dispatch_start`, `llm/first_token`, `llm/response_complete` |
| `agent.response` | 確定本文（非 streaming 表示向け。stream 中は delta も可） |
| `agent.error` | 推論失敗 |

WS `llm.delta` と SSE streaming の **二重表示** を避ける Phase 1 方針:

- **Viewer Chat UI は SSE `agent.response` を正**とする
- WS `llm.delta` は字幕横の debug / latency panel 用（実装簡略化）
- または WS delta で Chat bubble を更新し、SSE は final のみ（どちらか **一つ** を PR-V1 で固定。混在禁止）

**推奨（Phase 1）**: Chat UI = SSE stream hook のみ。WS `llm.delta` は Ops debug に限定。

### 6.5 rencrow テスト（必須）

`voice_chat_runtime_websocket_test.go`:

- [ ] disabled → error
- [ ] session.start / binary / commit 透過
- [ ] llm.delta / llm.final 透過
- [ ] utterance_id mismatch → SESSION_MISMATCH
- [ ] LLM upstream 切断 → LLM_SESSION_UNAVAILABLE

`voice_direct` orchestrator test:

- [ ] emits routing.decision + first_token + response_complete
- [ ] does not call STT provider
- [ ] does not route to IdleChat

---

## 7. Viewer 実装（PR-V1）

### 7.1 変更ファイル

| ファイル | 責務 |
| --- | --- |
| `internal/adapter/viewer/assets/js/viewer.js` | `vdsState`, mode 分岐, VDS WS, mic 制御 |
| `internal/adapter/viewer/viewer.html` | debug panel anchor（任意） |
| `internal/adapter/viewer/assets/css/viewer.css` | VDS streaming 表示（最小。UI 大幅変更禁止） |
| `internal/adapter/viewer/viewer_vds_https.test.mjs` | contract test（新規） |
| `internal/adapter/viewer/runtime_config_test.go` | voice_chat fields |

### 7.2 `vdsState`（`sttState` 対称）

最低フィールド:

```javascript
const vdsState = {
  voiceInputMode: 'stt_primary',
  voiceChatURL: '',
  voiceChatEnabled: false,
  ws: null,
  isRecording: false,
  isStopping: false,
  streamReady: false,
  utteranceID: '',
  sessionID: '',
  sentAudioBytes: 0,
  sentAudioSamples: 0,
  sampleRate: 16000,
  captureLog: [],
  llmDeltaText: '',
  llmFinalText: '',
  errorText: '',
  latencySpeechStartMS: 0,
  latencyCommitMS: 0,
  latencyFirstDeltaMS: 0,
  latencyFinalMS: 0,
};
```

### 7.3 マイク ON/OFF 分岐

#### `stt_primary`（既存）

- 変更なし。`/stt` WS のみ。

#### `vds_sub`（Phase 1 新規）

**マイク ON**:

1. `loadViewerRuntimeConfig()` で `voice_chat_enabled` 確認。false なら toast + abort
2. `utterance_id` 生成（`crypto.randomUUID()`）
3. `/voice-chat` WS open
4. `session.start` 送信（契約 74 準拠）
5. `session.ready` 待ち。timeout 5s
6. 既存 PCM capture pipeline から **binary chunk を VDS WS に send**（STT WS は開かない）

**マイク OFF**:

1. 残 chunk flush + 1s silence tail（STT と同じ）
2. `session.commit` 送信
3. `llm.final` or `error` or timeout 120s 待ち
4. WS close（または次 utterance 用に open 維持は Phase 1b。Phase 1 は **1 utterance = 1 WS** でよい）

**Chat 投入**:

- **`send()` / `/viewer/send` を呼ばない**
- 応答は SSE `agent.response` で表示

### 7.4 中断（barge-in）

`handleChatInputIntent` 拡張:

- `vds_sub` 録音中 → `session.cancel` + WS close
- `stt_primary` 既存どおり `abortSTTImmediately`

### 7.5 STT 回帰ガード

- `voice_input_mode === 'stt_primary'` のとき、VDS コードパスに入らないこと
- 既存 `viewer_stt_https.test.mjs` **全 pass 維持**

### 7.6 Viewer テスト（必須）

`viewer_vds_https.test.mjs`:

- [ ] runtime-config に voice_chat fields
- [ ] vds_sub: mic on → session.start before binary
- [ ] vds_sub: mic off → session.commit
- [ ] vds_sub: **no `/viewer/send` on success path**
- [ ] vds_sub: llm.final 前に error → error caption
- [ ] stt_primary: VDS WS 未使用

---

## 8. E2E / 計測（PR-T1）

### 8.1 新規スクリプト

`scripts/vds_e2e_probe.py`（`stt_e2e_probe.py` 対称）:

- golden WAV → PCM16 → `/voice-chat` WS
- `session.start` → chunks → `session.commit`
- `llm.final` 必須オプション
- metrics JSON 出力

`scripts/vds_e2e_probe_test.py`: protocol unit tests

`scripts/vds_viewer_browser_e2e.js`（任意 Phase 1b）:

- Playwright で VDS mode
- **`/viewer/send` 未呼び出し** を gate

### 8.2 合格ライン（Phase 1）

golden 25s（`client_stt_input_20260609_140311.wav`）:

| 指標 | 合格 |
| --- | --- |
| commit → first_token | ≤ 15s（warm 後） |
| commit → llm.final | ≤ 25s |
| STT 主経路回帰 | 既存 STT E2E 非劣化 |
| `/viewer/send` 呼び出し | vds_sub 成功 path で **0 回** |

---

## 9. 現行実装との差分

| 項目 | 現行 | Phase 1 後 |
| --- | --- | --- |
| マイク Chat 投入 | STT final → text send | `vds_sub`: VDS WS → LLM |
| LLM 音声入力 | なし（text のみ） | PCM stream → LLM session |
| LLM 出力 | SSE agent.response | 同左（stream hook） |
| Phase 0 audio | CLI/添付 WAV 一括 | **変更しない** |
| runtime-config | STT URL のみ | + voice_chat URL/mode |
| RenCrow_LLM | `input_audio` batch のみ | + audio session WS |

---

## 10. 実装タスク一覧

### 10.1 RenCrow_LLM（PR-L1）

- [ ] `audio_session_server.py` 追加
- [ ] Chat launcher に WS route 登録
- [ ] commit→stream 推論（既存 alias_proxy multimodal 再利用）
- [ ] `test_audio_session_contract.py`
- [ ] `docs/audio_session_ws.md`

### 10.2 rencrow（PR-P1/P2）

- [ ] `modules/voicechat/*`
- [ ] `voice_chat_runtime_*.go`
- [ ] routes 登録 + env
- [ ] runtime-config 拡張
- [ ] `ProcessVoiceDirect` + tests
- [ ] bridge 透過 tests

### 10.3 Viewer（PR-V1）

- [ ] runtime-config 読み込み
- [ ] `vdsState` + mic 分岐
- [ ] VDS WS lifecycle
- [ ] barge-in cancel
- [ ] `viewer_vds_https.test.mjs`
- [ ] runtime_config_test 更新

### 10.4 E2E（PR-T1）

- [ ] `vds_e2e_probe.py` + tests
- [ ] golden 25s / jfk 11s 計測 MD を `tmp/` に記録

### 10.5 Phase 1b（別 PR）

- [ ] `parallel_caption`（dual WS send）
- [ ] 1 WS 複数 utterance 再利用

---

## 11. 検証チェックリスト

### 11.1 契約

- [ ] Viewer↔rencrow event 名が 74 WS 契約と一致
- [ ] rencrow↔LLM event 名が 74 §5 と一致
- [ ] PCM16 LE mono 16kHz のみ（Phase 1）

### 11.2 回帰

- [ ] `voice_input_mode=stt_primary` で STT E2E 全 pass
- [ ] IdleChat に VDS 入力が流れない
- [ ] Phase 0 `--audio-direct` CLI 非劣化

### 11.3 エラー

- [ ] disabled / busy / too short / upstream down が `error` code 付きで見える
- [ ] final 後 error で Chat 本文を上書きしない

### 11.4 性能

- [ ] golden 25s で commit→final ≤ 25s（warm）
- [ ] Phase 0 72s 系の `/viewer/send` multipart 経路を VDS 本番 path に使っていない

---

## 12. 停止条件・ブロック

| 条件 | 対応 |
| --- | --- |
| RenCrow_LLM WS 未実装 | Viewer PR を merge しない |
| commit→final > 25s が解消しない | `vds_sub` を runtime default にしない |
| SSE と WS で Chat が二重表示 | 実装方針を 1 つに固定するまで PR-V1 merge 禁止 |
| STT 回帰失敗 | 該当 PR revert |
| Chat LLM busy で VDS 連続失敗 | UI に `LLM_BUSY` 表示。自動 retry しない（Phase 1） |

---

## 13. ロールアウト

1. `VOICE_CHAT_ENABLED=true`（staging のみ）
2. runtime-config `voice_input_mode=vds_sub`（Ops 手動）
3. golden / jfk 計測合格
4. `parallel_caption` で STT 字幕比較
5. 本番 default は **引き続き `stt_primary`**

---

## 14. 関連ドキュメント

- 設計: `74_Viewer音声直結LLM_Streaming仕様.md`
- WS 契約: `74_Viewer音声直結LLM_WS契約.md`
- チェックリスト: `75_Viewer音声直結LLM_Streaming実装チェックリスト.md`
- STT 参照: `39_STT_Streaming暫定確定字幕仕様.md` / `40_STT_Streaming実装作業仕様.md`
