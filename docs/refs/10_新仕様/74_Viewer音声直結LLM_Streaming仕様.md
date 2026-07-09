# 74 Viewer 音声直結 LLM Streaming 仕様

**関連**: `39_STT_Streaming暫定確定字幕仕様.md` / `77_STT音声_LLM音声_命名と経路仕様.md` / `RenCrow_LLM/docs/RenCrow連携仕様.md` / `73_STT高速化仕様.md`
**作成日**: 2026-06-10
**ステータス**: 設計確定（実装は `75_Viewer音声直結LLM_Streaming実装作業仕様.md`）

---

## 1. 目的

Viewer マイク入力に対し、**STT を経由しない音声直結サブ経路**を追加する。

短い呼称は `77_STT音声_LLM音声_命名と経路仕様.md` に従う。本仕様の VDS / `/voice-chat` 経路は **LLM音声**、RenCrow_STT を使う `/stt` 経路は **STT音声** と呼ぶ。

最終形は **ストリーミング** とする。

- **音声 IN**: 発話中に PCM チャンクを逐次送信する
- **LLM OUT**: トークン/本文を逐次 Viewer へ返す
- **STT 主経路は維持**する。字幕・ログ・fallback・比較用として残す

```text
【主経路・現行】
Viewer → rencrow /stt → RenCrow_STT → final text
       → rencrow /viewer/send → RenCrow_LLM → Viewer

【サブ経路・本仕様】
Viewer → rencrow /voice-chat → RenCrow_LLM（streaming audio in）
       → rencrow /viewer/events → Viewer
```

「Viewer → LLM → Viewer」は **STT を Chat 投入から除外**する意味であり、rencrow を省略する意味ではない。Viewer の入力口・出力口は変えない。

---

## 2. 非目的

- Browser から RenCrow_LLM（`:8081`）への直接接続
- STT モジュールの削除
- IdleChat への音声直結
- Phase 1 以前の **完成 WAV 一括 `input_audio`** を最終形とみなすこと（あくまで Phase 0 暫定）

---

## 3. 用語

| 用語 | 定義 |
| --- | --- |
| VDS | Voice Direct Streaming。本仕様の音声直結サブ経路 |
| utterance | 1 回の発話区間。VAD / マイク OFF / `stop` で区切る |
| audio chunk | Viewer から送る 16kHz mono PCM16 LE binary frame |
| llm_delta | LLM 生成テキストの途中 chunk。Viewer 表示用 |
| llm_final | 1 utterance に対する LLM 応答の確定 |
| caption-only STT | 字幕表示のみ。Chat / LLM へ text を渡さない STT 利用 |
| voice_input_mode | Viewer の音声入力モード（後述） |

---

## 4. モード設計

Viewer は `voice_input_mode` で挙動を切り替える。設定源は **server config → Viewer runtime config** を優先する。

| モード | マイク ON 時 | Chat 投入 | 字幕 |
| --- | --- | --- | --- |
| `stt_primary` | `/stt` のみ | STT `final` → text | STT partial/final |
| `vds_sub` | `/voice-chat` のみ | 音声 streaming → LLM | なし（Phase 2 で optional caption） |
| `parallel_caption` | `/stt` + `/voice-chat` | VDS のみ | STT partial/final（Chat へは渡さない） |

**既定値**: `stt_primary`（現行互換）

**推奨運用**:

- 比較・検証: `parallel_caption`
- 本番サブ経路試験: `vds_sub`
- 現行維持: `stt_primary`

---

## 5. アーキテクチャ

### 5.1 論理構成

```text
[Browser Viewer]
  mic capture (16kHz mono PCM16)
  voice_input_mode
        |
        +-- /stt WS  -----------------> RenCrow_STT (既存)
        |                                 partial / final
        |
        +-- /voice-chat WS (新) ------> rencrow voice-chat bridge
                                              |
                                              v
                                        RenCrow_LLM Chat
                                        (streaming audio session)
                                              |
                                              v
                                        llm_delta / llm_final
        |
        +-- /viewer/events SSE <------- rencrow orchestrator
              agent.response
              metrics.latency
```

### 5.2 境界責務

| コンポーネント | 責務 |
| --- | --- |
| **Viewer** | マイク取得、モード別 WS 接続、字幕/LLM ストリーム表示、発話区間制御 |
| **rencrow `/voice-chat`** | Viewer↔RenCrow_LLM 間の音声セッション中継、LLM stream を orchestrator event へ変換 |
| **rencrow orchestrator** | Chat ルーティング、stream hook、`agent.response` / `metrics.latency` 発行 |
| **RenCrow_LLM** | 音声セッション受付、音声 buffer/window 処理、Chat モデル推論、token stream 返却 |
| **RenCrow_STT** | 変更なし（主経路・字幕用） |

### 5.3 既存資産の再利用

| 既存 | 再利用方針 |
| --- | --- |
| Viewer PCM capture / resample | そのまま |
| `/stt` bridge 実装 | パターン参考。`/voice-chat` は別 handler |
| `MessagePartAudio` / `input_audio` | Phase 0 の file 投入。Phase 1+ は session API へ |
| orchestrator `WithStreamHooks` | LLM OUT streaming はそのまま利用 |
| `metrics.latency` | `llm/first_token`, `llm/response_complete` を VDS でも発行 |

---

## 6. フェーズ計画

### Phase 0: 暫定（実装済み・非最終）

- Viewer 添付 WAV / CLI `--audio-direct`
- 完成ファイルを `input_audio` として 1 回 POST
- **ストリーミングではない**。性能比較・配線確認のみ

### Phase 1: Utterance Streaming（MVP 目標）

**音声 IN**

- Viewer は `/voice-chat` WS を開く
- `start` → PCM chunk 逐次 → `stop`
- rencrow は utterance 単位で RenCrow_LLM へ音声セッションを開き、**発話確定後に推論開始**
- RenCrow_LLM 側は WS または内部 session API で chunk を受け取る

**LLM OUT**

- RenCrow_LLM `stream:true` の delta を rencrow が受け、即 `/viewer/events` へ転送
- Viewer は `llm_delta`（WS）と `agent.response`（SSE）の両方または SSE のみで表示

**到達 UX**

- 発話終了 → 初 token までの latency を STT 主経路より短くすることを目標
- 字幕は `parallel_caption` 時のみ STT 側

### Phase 2: Incremental Audio（将来）

- 発話中の window ごとに LLM へ partial audio を送り、**早い llm_delta** を狙う
- STT partial と同様、途中結果は表示のみ / 確定は `stop` 後
- RenCrow_LLM / mlx-vlm 側の incremental multimodal 対応が前提

### Phase 3: 統合最適化

- `parallel_caption` の STT final と VDS llm_final の diff ログ
- fallback: VDS 失敗時 STT final → text Chat へ自動切替
- 25s 長尺での rencrow オーバーヘッド解消（前回計測 72s 問題）

---

## 7. RenCrow_LLM 拡張（Phase 1）

現行 `input_audio` は **完成 WAV 1 発** のみ。VDS 用に session API を追加する。

### 7.1 新エンドポイント（案）

**WebSocket** `GET /v1/chat/audio/sessions`（名称は実装時確定）

| direction | message | 説明 |
| --- | --- | --- |
| client → server | `session.start` | `model`, `messages`(system), `think`, `max_tokens`, audio format |
| client → server | binary PCM16 chunk | utterance 音声 |
| client → server | `session.commit` | 発話確定。推論開始 |
| client → server | `session.cancel` | 中断 |
| server → client | `session.ready` | session_id |
| server → client | `session.progress` | 受信秒数 |
| server → client | `llm.delta` | `{ "text": "..." }` |
| server → client | `llm.final` | 確定本文 |
| server → client | `error` | エラー |

**HTTP 代替（内部のみ）**: rencrow が LLM と co-located なら chunked upload API でも可。Viewer からは rencrow WS のみ公開。

### 7.2 推論契約

- Chat ロール（`model: Chat`）のみ
- `stream: true` 固定（Phase 1）
- 1 session = 1 utterance = 1 Chat turn
- 同時 session 数: Chat プロセスは 1 リクエスト処理のため **1**（RenCrow連携仕様と同じ）

---

## 8. rencrow 拡張

### 8.1 新ルート

| path | 種別 | 説明 |
| --- | --- | --- |
| `/voice-chat` | WebSocket | Viewer 向け VDS 入口（primary） |
| `/voice-chat-ws` | WebSocket | 互換 alias（`/stt-ws` と同様） |

環境変数:

- `RENCROW_LLM_CHAT_WS` または既存 Chat base URL から WS URL を導出
- `VOICE_CHAT_ENABLED=true` で有効化

### 8.2 voice-chat bridge 責務

1. Viewer WS を accept
2. RenCrow_LLM audio session WS を open
3. text/binary frame を透過または正規化
4. `llm.delta` を orchestrator へ stream 注入
5. `llm.final` 到達で通常 Chat job を完了扱い
6. **STT bridge と同様、独自 HTTP fallback を二重に持たない**

### 8.3 orchestrator 接続

VDS は `/viewer/send` を経由しない。代わりに:

```text
voice-chat bridge
  -> MessageOrchestrator.SubmitVoiceDirect(ctx, VoiceDirectRequest)
  -> LLM provider streaming
  -> events: routing.decision, metrics.latency, agent.response
```

`VoiceDirectRequest` 最低フィールド:

- `session_id`, `channel`, `chat_id`
- `system_prompt` / viewer alias
- `utterance_id`
- `audio_format`（sample_rate, channels）

---

## 9. Viewer 拡張

### 9.1 UI / 状態

`vdsState`（`sttState` と対称）:

- `isRecording`, `ws`, `streamReady`
- `llmDeltaText`, `llmFinalText`
- `latencySpeechStartMS`, `latencyFirstTokenMS`
- `voiceInputMode`

マイク OFF 時:

- `stt_primary`: 既存どおり STT final → `send()`
- `vds_sub`: `/voice-chat` で `stop` → LLM stream 表示（text send しない）
- `parallel_caption`: STT は字幕のみ、VDS が Chat 応答

### 9.2 表示

| イベント | 表示先 |
| --- | --- |
| STT partial/final | 字幕欄（caption） |
| VDS llm_delta | Chat 吹き出し（streaming） |
| VDS llm_final / agent.response | Chat 確定 |
| error | toast + 字幕/Chat エラー |

### 9.3 中断

- ユーザーがテキスト入力開始 → VDS `session.cancel` + STT `abort`（既存 `handleChatInputIntent` 拡張）
- TTS 再生中の barge-in 規則は STT 主経路と同一

---

## 10. エラー・fallback

| 条件 | 動作 |
| --- | --- |
| `/voice-chat` 接続不可 | toast。`parallel_caption` なら STT final → text fallback |
| LLM session error | `error` 表示。設定により STT final fallback |
| 空 utterance | Chat 投入しない |
| LLM timeout | `agent.error`。Ops log に session_id |

fallback 方針:

- **自動 fallback は Phase 3**。Phase 1 は手動モード切替のみ

---

## 11. 性能目標（Phase 1）

計測 WAV: `golden_25s_v1`（`client_stt_input_20260609_140311.wav`）

| 指標 | STT 主経路（現行） | VDS 目標 |
| --- | --- | --- |
| 発話終了 → LLM first token | STT stop→final + LLM | **RenCrow_LLM 直 audio 9s 前後 + α** |
| 発話終了 → 確定応答 | 実測 ~33s（rencrow STT 経路） | **< 20s**（初期合格ライン） |
| 字幕 | あり | `parallel_caption` 時のみ |

前回暫定計測（WAV 一括）で rencrow audio 72s となった原因は Phase 1 実装前に調査対象とする。

---

## 12. セキュリティ

- Viewer → rencrow のみ。RenCrow_LLM へ Browser 直接接続しない
- `/voice-chat` は Viewer 同一 origin / Tailscale Serve 配下
- session_id / utterance_id を log に残し、Ops で追跡可能にする

---

## 13. テスト計画

| 層 | テスト |
| --- | --- |
| RenCrow_LLM | audio session WS contract test |
| rencrow | voice-chat bridge unit + golden WAV utterance e2e |
| Viewer | fake mic → `/voice-chat` → llm_delta 表示 |
| 回帰 | `stt_primary` モードで既存 STT E2E が壊れない |

スクリプト:

- 新規: `scripts/vds_e2e_probe.py`（STT probe と対称）
- 既存 golden dataset 再利用

---

## 14. 実装順序（推奨）

1. **RenCrow_LLM** audio session WS（Phase 1 最小）
2. **rencrow** `/voice-chat` bridge + orchestrator `SubmitVoiceDirect`
3. **Viewer** `vds_sub` モード（WS 接続 + llm_delta 表示）
4. **計測** golden 25s / jfk 11s A/B
5. **Viewer** `parallel_caption`
6. **fallback / 長尺最適化**（Phase 3）

---

## 15. 関連ドキュメント

- **実装作業仕様**: `75_Viewer音声直結LLM_Streaming実装作業仕様.md`
- **実装チェックリスト**: `75_Viewer音声直結LLM_Streaming実装チェックリスト.md`
- WebSocket 契約詳細: `74_Viewer音声直結LLM_WS契約.md`
- STT 字幕仕様: `39_STT_Streaming暫定確定字幕仕様.md`
- 性能改善: `73_STT高速化仕様.md`
- RenCrow_LLM multimodal: `RenCrow_LLM/docs/RenCrow連携仕様.md`
