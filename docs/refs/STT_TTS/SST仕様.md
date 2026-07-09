れんさん、MDならこの形でそのまま `docs/stt_mac_low_latency.md` に置けます。

````markdown
# RenCrow Mac向け低遅延STT仕様  
## WhisperKit + Core ML + large-v3-turbo 版

## 1. 結論

M5 Max / 128GB unified memory 環境で、RenCrowのローカルSTTを低遅延に動かす場合、第一候補は以下とする。

```text
WhisperKit + Core ML + large-v3-turbo
````

理由は、Apple Silicon上でCore ML、Neural Engine、Metalを活用できるため。
Python中心の `faster-whisper` よりも、Macネイティブ環境では低遅延を狙いやすい。

なお、M5 Maxの128GBは厳密にはVRAMではなく、CPU/GPU/Neural Engineで共有する unified memory として扱う。

---

## 2. 採用方針

RenCrowのSTT実装は、環境ごとにproviderを切り替えられる構成にする。

```text
Mac / Apple Silicon:
  WhisperKit + Core ML + large-v3-turbo

Windows / NVIDIA GPU:
  faster-whisper + large-v3-turbo

Linux / 組み込み寄り:
  whisper.cpp または sherpa-onnx を検討
```

STT APIの外側は共通化し、内部実装だけを差し替え可能にする。

---

## 3. 目的

RenCrowに、Mac上で動作する低遅延ローカル音声入力を追加する。

音声入力は、RenCrow Chat本体に直接組み込まず、独立したSTT providerとして実装する。
STT結果は、Chatに通常のユーザー入力として渡す。

全体の流れは以下。

```text
Microphone
  ↓
WhisperKit STT Helper
  ↓
Core ML / Apple Silicon
  ↓
Transcribed Text
  ↓
RenCrow Chat
  ↓
LLM
  ↓
SBV2 Voice Output
```

---

## 4. 基本アーキテクチャ

RenCrow本体はPythonのまま維持する。
Mac向けSTT処理のみ、Swift製ヘルパーまたはMacネイティブプロセスとして分離する。

```text
RenCrow Chat / Python
  ↓ HTTP or local socket
WhisperKit STT Helper / Swift
  ↓
Core ML / ANE / GPU
  ↓
Text
  ↓
RenCrow Chat input queue
```

この構成により、Chat本体、LLM制御、記憶システムを壊さずに、STT部分だけをMac向けに最適化できる。

---

## 5. ディレクトリ構成案

```text
rencrow/
  stt/
    server.py
    provider_base.py
    config.py
    schemas.py
    README.md

    providers/
      whisperkit_provider/
        README.md
        whisperkit_helper/
          Package.swift
          Sources/
            WhisperKitHelper/
              main.swift

      faster_whisper_provider/
        transcriber.py
        README.md

      whisper_cpp_provider/
        README.md
```

---

## 6. Provider設計

STT処理は共通インターフェースに統一する。

```python
class STTProvider:
    def transcribe_file(self, path: str) -> STTResult:
        ...

    def transcribe_stream(self, audio_chunk: bytes) -> STTPartialResult:
        ...
```

初期実装では、最低限 `transcribe_file()` を実装する。
次フェーズで `transcribe_stream()` を追加する。

---

## 7. API仕様

STTサーバ側のAPIは、既存仕様と互換にする。

### 7.1 ヘルスチェック

```text
GET /health
```

レスポンス例。

```json
{
  "status": "ok",
  "provider": "whisperkit",
  "model": "large-v3-turbo",
  "device": "apple_silicon",
  "ready": true
}
```

---

### 7.2 音声ファイル文字起こし

```text
POST /stt/file
```

入力。

```text
multipart/form-data
file: audio.wav
```

レスポンス例。

```json
{
  "text": "ルミナ、今日の予定を確認して。",
  "language": "ja",
  "duration": 2.84,
  "segments": [
    {
      "start": 0.0,
      "end": 2.84,
      "text": "ルミナ、今日の予定を確認して。"
    }
  ]
}
```

---

### 7.3 Chat連携用入力

```text
POST /stt/chat-input
```

レスポンス例。

```json
{
  "type": "user_input",
  "source": "local_stt",
  "provider": "whisperkit",
  "text": "ルミナ、RenCrowの状態を確認して。",
  "confidence_note": null,
  "event_id": "evt_stt_20260506_000001"
}
```

---

## 8. 設定仕様

`config.yaml` 例。

```yaml
stt:
  provider: whisperkit
  language: ja
  model: large-v3-turbo
  mode: low_latency
  vad: true
  stream: true

  endpoint:
    host: 127.0.0.1
    port: 8765

  debug:
    save_audio: false
    save_transcript: true
```

providerは以下から選択可能にする。

```yaml
stt:
  provider: whisperkit
```

```yaml
stt:
  provider: faster_whisper
```

```yaml
stt:
  provider: whisper_cpp
```

---

## 9. WhisperKit Provider仕様

Mac / Apple Silicon向けの第一候補provider。

### 採用技術

```text
WhisperKit
Core ML
large-v3-turbo
Swift
macOS native process
```

### 要件

```text
- WhisperKitを利用して音声を文字起こしする
- large-v3-turbo相当のモデルを使用する
- 日本語認識を初期値にする
- 低遅延モードを優先する
- VADを有効化する
- RenCrow本体とはHTTPまたはlocal socketで接続する
- 出力JSONはRenCrow共通STT API形式に合わせる
```

---

## 10. RenCrow Chat側連携

STT結果は、Chat側で通常のユーザー入力として扱う。

```text
STT Result
  ↓
Chat input queue
  ↓
User message event
  ↓
LangGraph / Router
  ↓
LLM response
```

Chat側では、入力由来を保持する。

```json
{
  "event_id": "evt_stt_20260506_000001",
  "input_type": "voice",
  "stt_provider": "whisperkit",
  "text": "ルミナ、今日のニュースを教えて。",
  "created_at": "2026-05-06T10:00:00+09:00"
}
```

これにより、後で以下の制御が可能になる。

```text
- 音声入力時だけ返答を短くする
- STT誤認識の再確認を挟む
- 「ルミナ」「クラリス」「ノクス」など呼びかけ語を検出する
- 割り込み入力として扱う
```

---

## 11. 初期実装範囲

初期実装では、以下までを対象とする。

```text
- WhisperKit providerの基本実装
- /health
- /stt/file
- /stt/chat-input
- 日本語音声ファイルの文字起こし
- Chat投入用JSON生成
- EventId付与
- エラーJSON返却
- provider切り替え設定
```

---

## 12. 初期実装では対象外

以下は初期実装では行わない。

```text
- 完全リアルタイム逐次文字起こし
- 話者分離
- 複数マイク対応
- ノイズキャンセル
- LLMによる誤認識補正
- 音声コマンド専用分類
- SBV2との半二重制御
```

---

## 13. 次フェーズ候補

初期実装後に追加する。

```text
- ストリーミングSTT
- マイク常時待機モード
- 無音検出による自動送信
- 途中発話のpartial result表示
- Chat側の割り込み入力
- 句読点・固有名詞補正
- 「ルミナ」などウェイクワード的呼びかけ検出
- SBV2音声出力中の入力抑制
```

---

## 14. 受け入れ条件

```text
1. Mac上でWhisperKit helperが起動できる
2. RenCrowのSTT APIからprovider=whisperkitとして認識できる
3. GET /health が正常応答する
4. POST /stt/file にwavを投げると日本語テキストが返る
5. POST /stt/chat-input がChat投入用JSONを返す
6. EventIdが付与される
7. STTエラー時にChat本体を落とさない
8. provider設定を変更すれば faster-whisper 版へ切り替え可能
9. Chatを経由しないSTT単体テストができる
```

---

## 15. エラー処理

想定エラー。

```text
- WhisperKit helper起動失敗
- モデルロード失敗
- 音声ファイル形式不正
- 無音のみ
- 認識結果が空
- STT処理タイムアウト
- provider未設定
- provider通信失敗
```

エラーレスポンス例。

```json
{
  "status": "error",
  "provider": "whisperkit",
  "error_code": "NO_SPEECH_DETECTED",
  "message": "音声が検出されませんでした。",
  "text": ""
}
```

Chat側は、`text` が空の場合はLLMへ送らない。

---

## 16. ログ仕様

STT処理ごとにEventIdを付与する。

```json
{
  "event_id": "evt_stt_20260506_000001",
  "source": "local_stt",
  "provider": "whisperkit",
  "model": "large-v3-turbo",
  "device": "apple_silicon",
  "duration_sec": 2.84,
  "processing_ms": 320,
  "text_length": 21,
  "status": "success",
  "created_at": "2026-05-06T10:00:00+09:00"
}
```

音声ファイルそのものは、初期状態では保存しない。

```yaml
stt:
  debug:
    save_audio: false
```

---

## 17. 実装指示文

Cursor / Coder / Codexに渡す指示文。

```text
RenCrowにMac向け低遅延STT providerを追加してください。

目的:
M5 Max / 128GB unified memory 環境で、ローカル音声入力をできるだけ低遅延に日本語テキスト化し、RenCrow Chatへ通常のuser_inputとして渡すことです。

採用:
- WhisperKit
- Core ML
- large-v3-turbo
- macOS native / Swift helper
- RenCrow本体とはHTTPまたはlocal socketで接続

設計:
- STT API仕様は既存の /health, /stt/file, /stt/chat-input と互換にする
- Macでは WhisperKit provider を使う
- Windows/NVIDIAでは faster-whisper provider を使えるようにする
- provider切り替えは config.yaml で行う
- RenCrow本体はPythonのまま維持する
- STT処理のみSwift helperとして分離する

必須要件:
- rencrow/stt/provider_base.py に共通IFを定義する
- rencrow/stt/providers/whisperkit_provider/ を追加する
- /health で provider=whisperkit を返せる
- /stt/file でwavファイルを日本語文字起こしできる
- /stt/chat-input でChat投入用JSONを返す
- EventIdを各STT処理に付与する
- エラー時にChat本体を落とさない
- 音声が検出されない場合は text="" とし、error_code=NO_SPEECH_DETECTED を返す
- 初期実装では音声ファイルを保存しない
- README.md にセットアップ、起動、テスト手順を書く

受け入れ条件:
1. WhisperKit helperがMac上で起動できる
2. GET /health が正常応答する
3. POST /stt/file にwavを投げると日本語テキストが返る
4. POST /stt/chat-input がRenCrow Chat用JSONを返す
5. Chatを経由しないSTT単体テストができる
6. provider設定を変えれば faster-whisper 版へ切り替え可能
7. エラーがJSONで返る
8. 既存のRenCrow本体処理を壊さない

注意:
既存のPython環境、CUDA環境、共有ツールチェーンは変更しないでください。
Mac向けSTTは独立providerとして非破壊に追加してください。
```

---

## 18. 最終判断

M5 Max / 128GB unified memoryで低遅延STTを狙う場合の優先順位は以下。

```text
低遅延最優先:
  WhisperKit + Core ML + large-v3-turbo

実装の簡単さ優先:
  faster-whisper + large-v3-turbo

CLI / 移植性優先:
  whisper.cpp + Core ML / Metal

ストリーミング・組み込み寄り:
  sherpa-onnx
```

RenCrowのMac向けSTTは、WhisperKitを第一候補とする。

```

このMDで、前回の `faster-whisper` 仕様に対して「Mac本命はWhisperKit」として自然に差し替えられます。
```
