# STT ゴールデンテストデータセット仕様

**最終更新**: 2026-06-09  
**関連**: Viewer Ops テスト録音、`scripts/stt_e2e_probe.py`、`scripts/stt_viewer_browser_e2e.js`

## 1. 目的

Viewer 実マイク録音から作成した **固定 WAV + 原文（期待テキスト）** ペアを、E2E・回帰・開発確認の共通入力として使う。

| 用途 | 推奨データセット |
|------|------------------|
| 通常の STT / LLM 回帰（成功系） | `golden_25s_v1` |
| 30 秒超・長尺ファイル推論の劣化再現 | `long_35s_v1` |
| Viewer 録音パイプライン確認 | いずれも可（raw + trim あり） |

## 2. 録音パイプライン（再現手順）

入口: **Viewer → Ops → Runtime / LLM / Audio → STT テスト録音**

1. **録音開始** — TTS / IdleChat を中断し、Chat 🎤 は無効
2. 読み上げ文を **1 回通し** で読む
3. **録音停止・保存** — 次の順で保存される（file-safe）:
   - `POST /viewer/stt/wav/raw` → トリム前
   - 両端無音トリム（`edgeOnly: true`）
   - `POST /viewer/stt/wav` → トリム後
   - `POST /viewer/stt/autotest` → HTTP file inference（`ws_rounds: 0`）

### 2.1 保存ファイル命名

| 種別 | latest | アーカイブ |
|------|--------|-----------|
| raw | `tmp/client_stt_input_latest_raw.wav` | `tmp/stt_inputs/client_stt_input_YYYYMMDD_HHMMSS_raw.wav` |
| trim | `tmp/client_stt_input_latest.wav` | `tmp/stt_inputs/client_stt_input_YYYYMMDD_HHMMSS.wav` |
| 原文 | — | `tmp/stt_inputs/client_stt_input_YYYYMMDD_HHMMSS_reference.txt` |
| STT 実測 | — | `tmp/stt_inputs/client_stt_input_YYYYMMDD_HHMMSS_stt.txt` |
| マニフェスト | — | `tmp/stt_test_golden_25s_dataset.json` 等 |

### 2.2 関連 API

| エンドポイント | 用途 |
|----------------|------|
| `POST /viewer/stt/wav/raw` | トリム前 WAV 保存 |
| `POST /viewer/stt/wav` | トリム後 WAV 保存 |
| `POST /viewer/stt/autotest` | `stt_e2e_probe.py` 実行（結果 `tmp/stt_e2e_from_mic_latest.json`） |

## 3. データセット一覧

### 3.1 `golden_25s_v1`（推奨・ゴールデン）

**用途**: E2E 成功系、STT 品質ベースライン、LLM 音声入力テストのデフォルト入力。

| 項目 | 値 |
|------|-----|
| ID | `golden_25s_v1` |
| captured_at | `20260609_140311` |
| マニフェスト | `tmp/stt_test_golden_25s_dataset.json` |
| 読み上げスクリプト | `tmp/viewer_test_recording_script_golden_25s.md` |
| trim WAV | `tmp/stt_inputs/client_stt_input_20260609_140311.wav` |
| raw WAV | `tmp/stt_inputs/client_stt_input_20260609_140311_raw.wav` |
| 原文 | `tmp/stt_inputs/client_stt_input_20260609_140311_reference.txt` |
| STT 実測 | `tmp/stt_inputs/client_stt_input_20260609_140311_stt.txt` |
| trim 長 | **25.32 s** |
| raw 長 | 26.96 s |

**原文（期待値）**

```text
おはようございます。RenCrowの音声テストです。今日は2026年6月9日、午後9時ごろです。私の名前はゆきみです。この音声は文字起こし比較用です。数字の確認もお願いします。123、4567、89012。
```

**2026-06-09 時点の STT 実測（file inference @ 192.168.1.207:8766）**

```text
おはようございます。デンクロの音声テストです。今日は2026年6月9日午後9時頃です。私の名前はゆきみです。この音声は文字起こし比較用です。数字の確認もお願いします。123456789012
```

**許容差分（回帰判定）**

| 項目 | 許容 |
|------|------|
| `RenCrow` 表記 | `RenCrow` / `デンクロ` / `エンクロ` 等の近音 |
| 句読点・スペース | 多少の差は許容 |
| 数字 | `123、4567、89012` と `123456789012` は同一視 |
| 末尾 | **ハルシネーションループなし**（必須） |
| 欠落 | 文の欠落なし（必須） |

**非許容**

- 89012 以降の無限数字ループ
- 後半文の丸ごと欠落

---

### 3.2 `long_35s_v1`（長尺回帰）

**用途**: 30 秒 Whisper チャンク超えの file inference 劣化再現、長尺 STT 調査。

| 項目 | 値 |
|------|-----|
| ID | `long_35s_v1` |
| captured_at | `20260609_135459` |
| マニフェスト | `tmp/stt_inputs/client_stt_input_20260609_135459_dataset.json` |
| 読み上げスクリプト | `tmp/viewer_test_recording_script.md` |
| trim WAV | `tmp/stt_inputs/client_stt_input_20260609_135459.wav` |
| raw WAV | `tmp/stt_inputs/client_stt_input_20260609_135459_raw.wav` |
| 原文 | `tmp/stt_inputs/client_stt_input_20260609_135459_reference.txt` |
| STT 実測 | `tmp/stt_inputs/client_stt_input_20260609_135459_stt.txt` |
| trim 長 | **34.59 s** |
| raw 長 | 35.83 s |

**原文（期待値）**

```text
おはようございます。RenCrowの音声テストです。今日は2026年6月9日、午後9時ごろです。私の名前はゆきみです。この音声は、文字起こしとLLMへの直接入力の比較に使います。数字の確認もお願いします。123、4567、89012。最後に質問です。今日の作業は何から始めればいいですか。
```

**安定 prefix（2026-06-09 時点で STT が一致していた範囲）**

```text
おはようございます。RenCrowの音声テストです。今日は2026年6月9日、午後9時ごろです。私の名前はゆきみです。この音声は、文字起こしとLLMへの直接入力の比較に使います。数字の確認もお願いします。123、4567、
```

**既知の劣化（期待する“失敗の形”）**

- `89012` 以降: 数字ループハルシネーション
- 最終質問文: 欠落
- **prefix までの一致** を長尺回帰の合格条件とする（改善時は全文一致を目指す）

## 4. E2E / 確認コマンド

### 4.1 HTTP file inference（trim WAV）

```bash
cd RenCrow_CORE

# ゴールデン 25s（推奨）
python3 scripts/stt_e2e_probe.py \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --provider-url http://192.168.1.207:8766/v1/audio/transcriptions \
  --provider-rounds 1 \
  --ws-rounds 0 \
  --require-provider-text

# 長尺 35s（劣化再現）
python3 scripts/stt_e2e_probe.py \
  --wav tmp/stt_inputs/client_stt_input_20260609_135459.wav \
  --provider-url http://192.168.1.207:8766/v1/audio/transcriptions \
  --provider-rounds 1 \
  --ws-rounds 0
```

### 4.2 WebSocket streaming（trim WAV）

```bash
# ゴールデン 25s — WS final 必須
python3 scripts/stt_e2e_probe.py \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --provider-rounds 0 \
  --ws-rounds 1 \
  --ws-wait 70 \
  --ws-realtime \
  --ws-tail-silence-ms 1000 \
  --ws-url ws://127.0.0.1:18790/stt \
  --require-ws-final
```

### 4.3 Viewer 経由 autotest（latest 保存後）

```bash
# Ops テスト録音停止後、サーバ側 JSON を確認
cat tmp/stt_e2e_from_mic_latest.json
```

### 4.4 Browser fake mic E2E

```bash
node scripts/stt_viewer_browser_e2e.js \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --speak-ms 28000 \
  --partial-timeout-ms 30000 \
  --final-timeout-ms 90000
```

## 5. 開発での使い方

| シナリオ | 入力 | 確認ポイント |
|---------|------|-------------|
| STT provider 変更 | `140311.wav` | 全文認識・数字・末尾ループなし |
| STT 長尺 chunk 調査 | `135459.wav` | prefix 一致 / 89012 以降の劣化再現 |
| LLM マルチモーダル | `140311.wav` | `input_audio` 投入後の応答（`scripts/llm_golden_audio_probe.py` または `rencrow chat --audio-direct`） |
| Viewer 録音パイプライン | 新規録音 → raw/trim 比較 | raw ≈ trim + 前後無音、Ops UI 秒数 |
| トリムロジック変更 | `*_raw.wav` をローカル trim | `stt_test_record_utils.js` の `edgeOnly` |

**latest ポインタ**

- `tmp/stt_test_dataset_latest.json` — 直近保存セットへの参照
- 固定 ID テストでは **アーカイブパス（上表）を直接指定** すること（`latest` は上書きされる）

## 6. マニフェスト JSON スキーマ（最小）

```json
{
  "id": "golden_25s_v1",
  "captured_at": "20260609_140311",
  "raw_wav": "tmp/stt_inputs/..._raw.wav",
  "trimmed_wav": "tmp/stt_inputs/....wav",
  "reference_txt": "tmp/stt_inputs/..._reference.txt",
  "stt_txt": "tmp/stt_inputs/..._stt.txt",
  "reference_text": "...",
  "stt_text": "...",
  "raw_duration_sec": 26.96,
  "trimmed_duration_sec": 25.32,
  "status": "recorded"
}
```

## 7. 旧データセットとの関係

| ファイル | 状態 |
|---------|------|
| `tmp/stt_inputs/client_stt_input_20260521_084443.wav` | 旧 WS probe 用。本仕様のゴールデンには **使わない** |
| `tmp/client_stt_input_latest.wav` | 常に最新録音で上書き。CI 固定入力に使わない |

## 8. 参照

- `docs/09_Viewer/Viewer仕様.md` §12.1（Ops テスト録音）
- `docs/01_正本仕様/STT_正本仕様.md` §11（ゴールデンデータセット）
- `tmp/viewer_test_recording_script_golden_25s.md`
- `tmp/viewer_test_recording_script.md`
- `internal/adapter/viewer/assets/js/stt_test_record_utils.js`
