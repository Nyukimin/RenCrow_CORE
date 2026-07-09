# 74 Viewer 音声直結 LLM WebSocket 契約

**親仕様**: `74_Viewer音声直結LLM_Streaming仕様.md`
**命名仕様**: `77_STT音声_LLM音声_命名と経路仕様.md`
**実装作業仕様**: `75_Viewer音声直結LLM_Streaming実装作業仕様.md`
**作成日**: 2026-06-10
**ステータス**: 設計確定（実装は `75_Viewer音声直結LLM_Streaming実装作業仕様.md`）

---

## 1. 概要

Viewer と rencrow 間の **Voice Direct Streaming (VDS)** WebSocket 契約。

短い呼称では、本契約の `/voice-chat` 経路を **LLM音声** と呼ぶ。RenCrow_STT を使う `/stt` 経路は **STT音声** であり、本契約の `llm.delta` / `llm.final` と STT の `partial` / `final` は別物である。

STT `/stt` 契約（`39_STT_Streaming暫定確定字幕仕様.md`）と対称に設計し、学習コストを下げる。

| 項目 | 値 |
| --- | --- |
| エンドポイント | `wss://<viewer-host>/voice-chat` |
| 互換 alias | `/voice-chat-ws` |
| 音声形式 | 16kHz / mono / PCM16 LE（STT と同一） |
| チャンク | binary frame（200ms 推奨、STT と同じ） |

---

## 2. セッションライフサイクル

```text
Viewer                         rencrow                    RenCrow_LLM
  | open WS                       |                              |
  |--- session.start (JSON) ----->|---- session.start ---------->|
  |<-- session.ready -------------|<-- session.ready ------------|
  |--- PCM chunk (binary) ------->|--- PCM chunk --------------->|
  |<-- session.progress ----------|<-- session.progress ---------|
  |--- session.commit (JSON) --->|--- session.commit ---------->|
  |<-- llm.delta (0..N) ----------|<-- llm.delta (stream) --------|
  |<-- llm.final -----------------|<-- llm.final ----------------|
  | close / next utterance        |                              |
```

**1 WebSocket 接続 = 複数 utterance 可**。utterance ごとに `session.start` → chunks → `session.commit` を繰り返す。

---

## 3. Client → Server（Viewer → rencrow）

### 3.1 `session.start`

発話開始。マイク ON 直後に 1 回送る。

```json
{
  "type": "session.start",
  "utterance_id": "utt-20260610-001",
  "sample_rate": 16000,
  "channels": 1,
  "format": "pcm16le",
  "voice_input_mode": "vds_sub",
  "prompt": "",
  "viewer_session_id": "viewer-session-abc",
  "channel": "viewer",
  "chat_id": "default"
}
```

| field | 必須 | 説明 |
| --- | --- | --- |
| `utterance_id` | yes | Viewer 生成 UUID。ログ相関用 |
| `sample_rate` | yes | 16000 固定（Phase 1） |
| `channels` | yes | 1 固定 |
| `format` | yes | `pcm16le` |
| `voice_input_mode` | yes | `vds_sub` / `parallel_caption` |
| `prompt` | no | 空なら server 既定プロンプト |
| `viewer_session_id` | no | Chat session 相関 |
| `channel`, `chat_id` | no | orchestrator ルーティング |

### 3.2 PCM chunk（binary）

- WebSocket **binary frame**
- PCM16 LE mono、任意長（推奨 200ms = 6400 bytes @ 16kHz）
- STT `/stt` と同一形式

### 3.3 `session.commit`

発話確定。マイク OFF / VAD 終端 / 無音判定後に送る。

```json
{
  "type": "session.commit",
  "utterance_id": "utt-20260610-001"
}
```

### 3.4 `session.cancel`

中断。ユーザーがテキスト入力開始、別 utterance 開始、ページ離脱時。

```json
{
  "type": "session.cancel",
  "utterance_id": "utt-20260610-001",
  "reason": "user_input"
}
```

---

## 4. Server → Client（rencrow → Viewer）

### 4.1 `session.ready`

```json
{
  "type": "session.ready",
  "utterance_id": "utt-20260610-001",
  "session_id": "vds-sess-7f3a",
  "accepted_format": "pcm16le",
  "sample_rate": 16000
}
```

### 4.2 `session.progress`

```json
{
  "type": "session.progress",
  "utterance_id": "utt-20260610-001",
  "duration_sec": 3.2,
  "bytes_received": 102400
}
```

認識 text ではない。STT `progress` と同_role。

### 4.3 `llm.delta`

```json
{
  "type": "llm.delta",
  "utterance_id": "utt-20260610-001",
  "session_id": "vds-sess-7f3a",
  "job_id": "job-123",
  "seq": 1,
  "text": "おはよう"
}
```

- Viewer は Chat UI に streaming 追記
- 同一 utterance で `seq` 单调増加
- rencrow は parallel で SSE `agent.response` stream も emit してよい（Phase 1 は WS のみでも可）

### 4.4 `llm.final`

```json
{
  "type": "llm.final",
  "utterance_id": "utt-20260610-001",
  "session_id": "vds-sess-7f3a",
  "job_id": "job-123",
  "text": "おはようございます。…",
  "metrics": {
    "commit_to_first_token_ms": 4200,
    "commit_to_final_ms": 9800
  }
}
```

- utterance 終端 event
- 同一 utterance で `llm.final` 後に矛盾する `error` を返さない（STT final 契約と同型）

### 4.5 `error`

```json
{
  "type": "error",
  "utterance_id": "utt-20260610-001",
  "error_code": "LLM_SESSION_UNAVAILABLE",
  "message": "Voice direct LLM is unavailable"
}
```

| error_code | 意味 |
| --- | --- |
| `VOICE_CHAT_DISABLED` | server 設定で VDS 無効 |
| `LLM_SESSION_UNAVAILABLE` | RenCrow_LLM 接続不可 |
| `UTTERANCE_TOO_SHORT` | 音声長不足 |
| `SESSION_MISMATCH` | utterance_id 不一致 |
| `LLM_INFERENCE_FAILED` | 推論失敗 |

---

## 5. rencrow ↔ RenCrow_LLM 内部契約（Phase 1）

Viewer 契約を rencrow がそのまま LLM へ転送する **透過モード** を Phase 1 の default とする。

LLM 側 event 名 mapping:

| Viewer/rencrow | RenCrow_LLM |
| --- | --- |
| `session.start` | `session.start` |
| binary PCM | binary PCM |
| `session.commit` | `session.commit` |
| `session.ready` | `session.ready` |
| `llm.delta` | `llm.delta` |
| `llm.final` | `llm.final` |
| `error` | `error` |

LLM 側 WS path 案: `/v1/chat/audio/sessions`

---

## 6. STT との同時利用（`parallel_caption`）

| 接続 | 送るもの | 受け取るもの | Chat 投入 |
| --- | --- | --- | --- |
| `/stt` | 同一 PCM（duplicate send または tap） | partial / final | **しない** |
| `/voice-chat` | 同一 PCM | llm.delta / llm.final | **する** |

Phase 1 実装簡略案:

- Viewer が **同一 chunk を 2 WS に send**（実装単純、帯域 2 倍）
- Phase 2 で server-side tap を検討

---

## 7. タイムアウト

| 項目 | 値 |
| --- | --- |
| `session.start` → `session.ready` | 5s |
| `session.commit` → 初 `llm.delta` | 30s（Chat 初回 load 除く） |
| `session.commit` → `llm.final` | 120s |
| アイドル（chunk なし） | 60s で `session.cancel`（server 側） |

---

## 8. 計測フィールド

Viewer / rencrow / Ops で共通利用:

| metric | 定義 |
| --- | --- |
| `speech_start_ms` | マイク ON |
| `commit_sent_ms` | `session.commit` 送信 |
| `first_llm_delta_ms` | 初 `llm.delta` |
| `llm_final_ms` | `llm.final` |
| `commit_to_first_token_ms` | `llm.final.metrics` に含める |

既存 `metrics.latency`（SSE）とも二重記録し、Viewer debug panel で比較可能にする。

---

## 9. Phase 0 との差（明示）

| | Phase 0（暫定） | Phase 1（本契約） |
| --- | --- | --- |
| 入口 | `/viewer/send` multipart WAV | `/voice-chat` WS |
| 音声 | 完成ファイル | PCM chunk stream |
| LLM IN | `input_audio` 一括 | audio session stream |
| LLM OUT | SSE のみ | WS `llm.delta` + SSE |
| 最終形か | **No** | **Yes（Phase 1 baseline）** |
