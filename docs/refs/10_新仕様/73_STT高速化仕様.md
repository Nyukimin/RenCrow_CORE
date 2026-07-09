# 73 STT 高速化仕様

**関連**: STT_正本仕様.md §3（アーキテクチャ）/ §10（voice-bridge詳細）
**作成日**: 2026-06-09
**ステータス**: 未実装（実装待ち）

---

## 1. 目的

Viewer マイク入力における STT（音声→テキスト）の体感遅延を改善する。

対象フローは以下の通り。

```text
Viewer（Browser マイク入力）
  ↓ WebSocket
RenCrow Chat Server（Go / RenCrow_CORE） ← 透過プロキシ
  ↓ STT_GATEWAY_URL
RenCrow_STT（Python / RenCrow_STT/src/rencrow/stt/server.py）
  ↓ Inference
STT Provider（gemma4 / whisper / faster_whisper）
```

---

## 2. 現状の計測値（2026-06-08 実測）

対象音声: `jfk.wav`（11.0秒、終端無音 1.2秒）

| 経路 | 初回 partial | stop 送信 | final 受信 | stop→final |
|---|---:|---:|---:|---:|
| RenCrow STT bridge `/stt` 経由 | 3.51s | 12.27s | 14.99s | **2.72s** |
| RenCrow_STT 直 `/stt` | 2.44s | 12.27s | 17.43s | **5.16s** |
| HTTP provider 直叩き | — | — | 0.94〜6.96s | — |

### 遅延の内訳

**stop→final が遅い原因（RenCrow_STT）:**

```text
発話終了
  ↓ silence_final_ms = 900ms  ← 無音判定待ち
  ↓ _finalize() 呼び出し
  ↓ 全 PCM を一時ファイルへ書き込み
  ↓ Provider.transcribe_file() ← gemma4 推論
  ↓ final 送信
```

**初回 partial が遅い原因（RenCrow_STT）:**

```text
音声開始
  ↓ partial_audio_interval_ms = 8000ms  ← 8秒に1回しか発火しない
  ↓ OR min_partial_interval_ms = 800ms  ← 最短 800ms 間隔
  ↓ window_ms = 2000ms の PCM をファイル書き込み → 推論
  ↓ partial 送信
```

**旧RenCrow STT bridgeで重複していた stop 時 HTTP fallback:**

```text
stop 受信
  ↓ 全音声バッファを WAV に変換
  ↓ HTTP POST（毎回 TCP 確立 + multipart 送信）
  ↓ fallback timeout 最低 15 秒
  ↓ final 送信
```

この処理はRenCrow_STT側の終端判定・fallbackと重複するため、RenCrow STT bridgeから削除する。

---

## 3. 改善一覧

| # | 対象コンポーネント | 改善内容 | 優先度 |
|---|---|---|---|
| A | RenCrow_STT | `silence_final_ms` 短縮（900ms → 600ms） | **高** |
| B | RenCrow_STT | `partial_audio_interval_ms` 短縮（8000ms → 2500ms） | **高** |
| C | RenCrow STT bridge | stop時の独自HTTP fallback削除 | **高** |
| D | RenCrow STT bridge | stop時のcached partial独自final化削除 | **高** |
| E | RenCrow_STT | `min_partial_interval_ms` 短縮（800ms → 500ms） | 中 |
| F | RenCrow_STT | `window_ms` 短縮（2000ms → 1500ms） | 中 |
| G | RenCrow STT bridge | RenCrow_STTの `final` / `error` を終端イベントとして中継 | **高** |
| H | RenCrow_STT | StreamingSession パラメータを stt.yaml で設定可能にする | 中 |
| I | RenCrow_STT | `_finalize()` の partial fallback 閾値拡大（1s → 3s） | 低 |

---

## 4. RenCrow_STT 改善仕様

対象ファイル: `RenCrow_STT/src/rencrow/stt/server.py`

### 4.1 StreamingSession パラメータ変更【A / B / E / F】

`StreamingSession.__init__` の定数を以下に変更する。

| パラメータ | 現在値 | 変更後 |
|---|---:|---:|
| `self.silence_final_ms` | `900` | **`600`** |
| `self.partial_audio_interval_ms` | `8000` | **`2500`** |
| `self.min_partial_interval_ms` | `800` | **`500`** |
| `self.window_ms` | `2000` | **`1500`** |

### 4.2 partial fallback 閾値の拡大【I】

`_finalize()` 内の short-utterance partial fallback 条件を拡大する。

```python
# 変更前
if self.last_partial_text and len(self.pcm) < self.sample_rate:

# 変更後
if self.last_partial_text and (
    len(self.pcm) < self.sample_rate * 3
    or (
        self.last_voice_at is not None
        and (time.monotonic() - self.last_voice_at) * 1000 >= self.silence_final_ms
    )
):
```

**効果**: 3秒以内の短い発話で推論をスキップし、last\_partial\_text をそのまま final として返す。

### 4.3 設定の config 化【H】

StreamingSession のパラメータを `stt.yaml` から読み込めるようにする。

**`RenCrow_STT/src/rencrow/stt/config.py` — `STTConfig` に追加:**

```python
silence_final_ms: int = 600
partial_audio_interval_ms: int = 2500
min_partial_interval_ms: int = 500
window_ms: int = 1500
vad_threshold: float = 450.0
```

**`load_config()` 内 — `streaming` セクションのパース追加:**

```python
streaming = stt.get("streaming", {})
return STTConfig(
    # ... 既存フィールド ...
    silence_final_ms=int(streaming.get("silence_final_ms", 600)),
    partial_audio_interval_ms=int(streaming.get("partial_audio_interval_ms", 2500)),
    min_partial_interval_ms=int(streaming.get("min_partial_interval_ms", 500)),
    window_ms=int(streaming.get("window_ms", 1500)),
    vad_threshold=float(streaming.get("vad_threshold", 450.0)),
)
```

**`RenCrow_STT/configs/stt.yaml` — `streaming:` セクションを追加:**

```yaml
stt:
  # ... 既存設定 ...

  streaming:
    silence_final_ms: 600
    partial_audio_interval_ms: 2500
    min_partial_interval_ms: 500
    window_ms: 1500
    vad_threshold: 450.0
```

**`StreamingSession.__init__` — `State.config` から読み込みに変更:**

```python
self.window_ms = State.config.window_ms
self.min_partial_interval_ms = State.config.min_partial_interval_ms
self.partial_audio_interval_ms = State.config.partial_audio_interval_ms
self.silence_final_ms = State.config.silence_final_ms
self.vad_threshold = State.config.vad_threshold
```

---

## 5. RenCrow STT bridge 改善仕様

### 5.1 基本方針【C / D / G】

RenCrow STT bridge は、Viewer と RenCrow_STT の中継に責務を絞る。
stop後の終端判定、partial fallback採用、HTTP fallback採用はRenCrow_STTが行う。

bridge側で削除する重複機能:

- stop時にcached partialを独自に `final` 化する処理
- stop時にHTTP `/v1/audio/transcriptions` を呼ぶ処理
- gatewayの空 `final` をHTTP fallbackで置換する処理
- fallback専用の音声buffer / latest wav snapshot / timeout floor

### 5.2 bridge が保持する最低限の互換ガード

RenCrow_STTの新契約では provider定型文や空 `final` は送られない。
ただし旧実装や異常系に備え、bridgeは以下だけを行ってよい。

| 入力 | bridgeの扱い |
|---|---|
| `partial` / `draft` / `final` の provider定型文 | `type:error` に変換 |
| 空 `final` | `type:error` に変換 |
| `stt_fallback_status:"used"` 付き `final` | 追加fallbackせず、そのまま中継 |
| RenCrow_STTの `error` | 終端イベントとしてViewerへ中継 |

### 5.3 stop後の流れ

```text
Viewer stop
  ↓
RenCrow STT bridge は stop を RenCrow_STT へ中継
  ↓
RenCrow_STT が final または error を1つだけ返す
  ↓
RenCrow STT bridge はその終端イベントをViewerへ中継してsessionを閉じる
```

この方式では、bridge経由でもRenCrow_STT直でも終端イベントの意味が一致する。
また、RenCrow_STTがすでにfallbackを使った場合に、bridgeが同じ音声を再解析する二重処理を避けられる。

---

## 6. 期待効果

| 指標 | 現状 | 改善後（推定） |
|---|---|---|
| 初回 partial 表示 | 2.4〜3.5s | **1.0〜1.5s** |
| stop→final（bridge、RenCrow_STT終端イベント中継） | 2.72s | **RenCrow_STT直と同等** |
| stop→final（RenCrow_STT 直） | 5.16s | **3.0〜4.0s** |
| bridge側HTTP request コスト | stopごとに追加HTTP実行 | **削除** |

---

## 7. テスト仕様

### 7.1 計測条件

- 音声: `RenCrow_CORE/workspace/stt-bench/jfk.wav`（11.0秒）
- 計測: 送受信を並行計測（`scripts/stt_e2e_probe.py` 並行版、または専用スクリプト）
- 比較基準: 本仕様 §2 の実測値

### 7.2 合格基準

| 指標 | 合格ライン |
|---|---|
| 初回 partial 表示 | ≤ 2.0s |
| stop→final（bridge、RenCrow_STT終端イベント中継） | RenCrow_STT直の同一音声結果と大きく乖離しない |
| stop→final（RenCrow_STT 直） | ≤ 4.5s |
| final の正確性 | 既存テスト全通過 |

### 7.3 回帰テスト

- `RenCrow_STT/tests/` の全テストが通過すること
- `RenCrow_CORE/modules/stt/` の Go テストが通過すること
- `scripts/stt_e2e_probe.py` で WebSocket final が取得できること

---

## 8. エラー提示仕様

### 8.1 原則

provider のエラー定型文（例: `申し訳ございませんが、音声ファイルが添付されていない…`）を
**partial / final の字幕として表示してはならない**。

ただし、エラーを黙って捨ててもよいわけではない。
**何らかのエラーをユーザーに提示する手段は常に必要**とする。

- 字幕欄へのエラー表示
- toast 通知
- STT capture log への記録

のいずれか（できれば複数）で、ユーザーが「認識に失敗した」と分かる状態にする。

### 8.2 現状の提示経路

| 層 | 既存手段 | 条件 |
|---|---|---|
| Viewer | `type: error` 受信 → `setSTTCaptionError` + toast | `msg.type === 'error'` |
| Viewer | final 待ち timeout → `STT final unavailable` | draft なしで timeout |
| Viewer | WebSocket 切断 → `STT websocket unavailable` | `ws.onerror` / `ws.onclose` |
| RenCrow_STT | `_send_error(error_code, message)` | `STREAM_TOO_SHORT`, `NO_SPEECH_DETECTED` 等 |
| RenCrow Go | `sendSTTError(conn, message)` | provider 失敗、設定不備 |

### 8.3 問題（現状）

gemma4 が streaming partial 推論で返すエラー定型文は、RenCrow_STT 側で
`stt_stream_partial_decode_result` として成功扱いされ、`type: partial` / `type: final` で
クライアントへ送られている（`error_code` なし）。

RenCrow Go 側の `NormalizeTranscriptText` はこの定型文を空文字に落とせるが、
**bridge 透過時はフィルタ前のテキストがそのまま届く**場合がある。

結果として、ユーザーには「誤った確定文字列」が見えるか、
フィルタ後に何も表示されず失敗が分からない、のどちらかになりうる。

### 8.4 改善方針

#### RenCrow_STT（必須）

1. provider 返答テキストを partial / final 送信前に検査する。
2. エラー定型文に一致した場合は `partial` / `final` を送らず、`_send_error` を使う。

```python
# server.py に追加する検査対象（RenCrow modules/stt と同期）
PROVIDER_ERROR_PHRASES = (
    "音声ファイルが添付されていない",
    "音声をアップロードしていただければ",
    "書き起こしを行うことができ",
    "申し訳ございませんが",
)
```

3. ログは `stt_stream_partial_decode_ignored`（reason=`provider_error_phrase`）とし、
   成功結果ではなくエラー経路として記録する。
4. ユーザー向け `message` は短く固定する（例: `音声認識に失敗しました。もう一度お試しください。`）。
   内部ログには provider 原文を残す。

#### RenCrow STT bridge（推奨）

1. gateway から受けた `partial` / `draft` / `final` に対し `NormalizeTranscriptText` を適用する（既存）。
2. 正規化後が空かつ原文がエラー定型文を含む場合、
   **透過せず** `type: error` に変換して Viewer へ送る。

```json
{"type":"error","error":"音声認識に失敗しました。もう一度お試しください。"}
```

3. gateway `final` の正規化後が空なら、追加HTTP fallbackせず `type:error` で明示する。
   空の final を送らない。

#### Viewer（変更最小）

既存の `type: error` ハンドラをそのまま使う。新規 UI は不要。

- 字幕: `setSTTCaptionError`
- toast: `showToast('認識エラー', 'error')`
- capture log: `recordSTTCaptureEvent('error', ...)`

### 8.5 エラー提示の優先順位

```text
1. server が type: error を返す          → 字幕エラー + toast（チャット送信しない）
2. final が空 / 定型文のみ              → 字幕エラー + toast（チャット送信しない）
3. final 待ち timeout、draft あり        → local draft fallback（既存契約）
4. final 待ち timeout、draft なし        → "STT final unavailable"（既存契約）
```

**禁止**: エラー定型文を `確定文字列` としてチャットへ投入すること。

### 8.6 受け入れ基準（エラー提示）

- provider がエラー定型文のみ返した場合、Viewer に `確定文字列` が表示されない。
- 同条件で `認識エラー` toast または字幕エラーが表示される。
- `stt_stream_partial_decode_ignored` または `type: error` がログに残る。
- HTTP `/stt/file` の正常系は回帰しない。

---

## 9. 参照

- `docs/01_正本仕様/STT_正本仕様.md` — STT 正本仕様
- `docs/10_新仕様/07_STT_TTS仕様.md` — STT/TTS 仕様
- `docs/10_新仕様/40_STT_Streaming実装作業仕様.md` — STT Streaming 実装仕様
- `RenCrow_STT/src/rencrow/stt/server.py` — StreamingSession 実装
- `RenCrow_STT/configs/stt.yaml` — 設定ファイル
- `cmd/rencrow/stt_runtime_http.go` — HTTP provider 呼び出し
- `cmd/rencrow/stt_runtime_websocket.go` — RenCrow STT bridge
