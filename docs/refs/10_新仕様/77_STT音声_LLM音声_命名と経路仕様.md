# 77 STT音声 / LLM音声 命名と経路仕様

**作成日**: 2026-06-14
**ステータス**: 現行実装反映
**関連**: `39_STT_Streaming暫定確定字幕仕様.md` / `74_Viewer音声直結LLM_Streaming仕様.md` / `74_Viewer音声直結LLM_WS契約.md` / `79_LLM音声_発話区間判定仕様.md`

---

## 1. 目的

Viewer の Chat マイク入力には、RenCrow_STT を使う経路と RenCrow_LLM を使う経路がある。

会話・実装・ログ確認で混同しないように、以後は短い名前として次を使う。

| 呼称 | 正式な意味 |
| --- | --- |
| **STT音声** | RenCrow_STT で音声を文字起こしし、その final text を Chat / LLM へ渡す経路 |
| **LLM音声** | RenCrow_LLM に音声を直接渡し、LLM が音声内容を解釈して応答する経路 |

「LLM音声」は STT 専用サーバを使わない。マルチモーダル LLM の audio input による音声理解・文字起こし相当の経路である。

---

## 2. STT音声

### 2.1 経路

```text
Viewer Chat mic
  -> picoclaw /stt or /stt-ws
  -> RenCrow_STT
  -> partial / final text
  -> picoclaw /viewer/send or equivalent Chat text path
  -> RenCrow_LLM Chat
  -> Viewer Chat response
```

### 2.2 責務

| コンポーネント | 責務 |
| --- | --- |
| Viewer | マイク取得、STT WS 接続、partial/final 字幕表示、final text 投入 |
| picoclaw `/stt` | Viewer と RenCrow_STT の bridge |
| RenCrow_STT | 音声から partial/final text を生成 |
| RenCrow_LLM | STT final text を通常 Chat 入力として処理 |

### 2.3 使う場面

- STT の品質・字幕・final text を明示的に確認したい場合
- 音声を一度テキスト化してから Chat に渡したい場合
- STT partial/final の UI 表示やログを主目的にする場合

---

## 3. LLM音声

### 3.1 経路

```text
Viewer Chat mic
  -> picoclaw /voice-chat or /voice-chat-ws
  -> picoclaw voice-chat runtime
  -> RenCrow_LLM Chat audio input
  -> llm.delta / llm.final
  -> Viewer Chat response
```

### 3.2 現行実装

現行の `picoclaw` 実装では、Viewer からは WebSocket で PCM16 を受ける。

内部では Viewer の `session.start` / PCM16 binary / `session.commit` を RenCrow_LLM の audio session WebSocket に透過し、RenCrow_LLM からの `llm.delta` / `llm.final` を Viewer へ返す。

```text
Viewer /voice-chat WS
  session.start
  PCM16 binary chunks
  session.commit
    |
    v
picoclaw: WS frame relay
    |
    v
RenCrow_LLM /v1/chat/audio/sessions
    |
    v
picoclaw -> Viewer: llm.delta / llm.final
```

現行コードの主担当は次の通り。

| ファイル | 役割 |
| --- | --- |
| `internal/adapter/viewer/assets/js/viewer.js` | Chat マイク、`vdsState`、`/voice-chat` 接続、PCM 送信、`llm.delta/final` 表示 |
| `cmd/picoclaw/voice_chat_runtime_websocket.go` | `/voice-chat` route、disabled/unavailable handler、RenCrow_LLM audio session WS への透過 bridge |
| `cmd/picoclaw/voice_chat_runtime_input_audio.go` | 旧 Phase 0 の PCM -> WAV -> `input_audio` handler。通常の LLM音声経路では使わない |
| `cmd/picoclaw/voice_chat_runtime_bridge.go` | `llm.final` / delta idle を orchestrator の Voice Direct event へ接続 |
| `modules/voicechat/` | `/voice-chat` event 名、route、mode 名の共通契約 |

### 3.3 注意

- LLM音声は RenCrow_STT を呼ばない。
- LLM音声の出力は `llm.delta` / `llm.final` であり、STT の `partial` / `final` ではない。
- Viewer から見たマイク入口は同じでも、`voice_input_mode` により STT音声と LLM音声は別経路になる。
- 「LLMでSTTしている」と説明しない。正確には「RenCrow_LLM が audio input を直接解釈している」。

---

## 4. `voice_input_mode` と呼称の対応

| `voice_input_mode` | 主経路 | 呼称 | Chat 投入 |
| --- | --- | --- | --- |
| `stt_primary` | `/stt` | STT音声 | RenCrow_STT final text |
| `vds_sub` | `/voice-chat` | LLM音声 | RenCrow_LLM audio input |
| `parallel_caption` | `/stt` + `/voice-chat` | LLM音声 + caption 用 STT音声 | Chat は LLM音声。STT は字幕・比較用 |

`parallel_caption` では STT音声の final text を Chat 応答の正本にしない。Chat 応答の正本は LLM音声の `llm.final` である。

---

## 5. イベント名の違い

| 経路 | 入力 endpoint | 主な戻り event | 意味 |
| --- | --- | --- | --- |
| STT音声 | `/stt`, `/stt-ws` | `partial`, `final` | RenCrow_STT の文字起こし結果 |
| LLM音声 | `/voice-chat`, `/voice-chat-ws` | `llm.delta`, `llm.final` | RenCrow_LLM の音声理解・応答結果 |

ログ確認時は、`final` と `llm.final` を混同しない。

---

## 6. 検証名

検証や報告では、次の名前を使う。

| 検証名 | 内容 |
| --- | --- |
| STT音声 E2E | Viewer Chat mic -> `/stt` -> RenCrow_STT -> final text -> Chat |
| LLM音声 E2E | Viewer Chat mic -> `/voice-chat` -> RenCrow_LLM audio input -> `llm.final` -> Chat |
| fake mic LLM音声 E2E | Playwright/Chromium `--use-file-for-fake-audio-capture` で Viewer Chat mic に WAV を注入する LLM音声 E2E |

「ブラウザのマイク入力」と言う場合、実マイクか fake mic かを必ず明記する。

---

## 7. 実装上の原則

- STT音声と LLM音声は fallback 関係として曖昧に混ぜない。
- fallback を入れる場合は、正常系ではなく error path としてログ・UI に明示する。
- Viewer 表示、STT raw text、LLM final text、orchestrator `agent.response` を同じものとして扱わない。
- LLM音声で `/voice-chat` が `llm.final` を返した場合、Viewer Chat 表示はその `llm.final` を正本にする。
- STT音声で RenCrow_STT が `final` を返した場合、Chat へ渡す文字列はその STT final text を正本にする。
- LLM音声の発話区間判定は `79_LLM音声_発話区間判定仕様.md` に従う。マイク監視の継続と、新規発話として LLM へ送る採用判定は分離する。
