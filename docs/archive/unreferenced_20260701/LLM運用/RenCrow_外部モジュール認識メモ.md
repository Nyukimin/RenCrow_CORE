# RenCrow 外部モジュール認識メモ

作成日: 2026-06-09

## 概要
RenCrow ルート配下には、`picoclaw_multiLLM` から参照できる独立モジュールとして `RenCrow_*` リポジトリが並んでいる。Serena MCP は `/home/nyukimi/RenCrow` をプロジェクトルートとして有効化することで、これらを横断的に参照できる。

## モジュール一覧
- `RenCrow_LLM`: MLX / `mlx-vlm` ベースの OpenAI 互換 LLM サーバ。Chat / Worker / Heavy / Wild をロール別ポートで起動し、alias proxy と管理 API を持つ。
- `RenCrow_STT`: STT 独立サーバ。HTTP `/v1/audio/transcriptions` と WebSocket `/stt` を提供し、現在のMac向け既定は Gemma4 provider。
- `RenCrow_TTS`: TTS 独立サーバ。Irodori-TTS と RenCrow voice/style API wrapper を含む。
- `RenCrow_Vision`: 画像・動画解析サーバ。`/v1/vision/analyze` で画像/動画を解析し、heuristic または OpenAI-compatible provider にproxyできる。
- `RenCrow_Image`: ComfyUI / Z-Image 系の画像生成・補正ランタイム仕様と運用スクリプトを持つ。
- `RenCrow_CMD`: RenCrow CLI / server binary。各 `RenCrow_*` サーバの外部疎通テストクライアントとしても使う。
- `RenCrow_Workspace`: `~/.picoclaw` の非秘密設定・プロンプト・共有可能なワークスペース資産。

## Chat音声直接入力への関係
- 音声経路の短い呼称は `docs/10_新仕様/77_STT音声_LLM音声_命名と経路仕様.md` に従う。
- **STT音声** は `Viewer -> RenCrow_STT -> text -> picoclaw_multiLLM -> RenCrow_LLM(Chat)` の経路。
- **LLM音声** は `Viewer -> picoclaw /voice-chat -> RenCrow_LLM audio input -> llm.final` の経路。
- `RenCrow_STT` はすでに Gemma4 12B IT 4bit をSTT providerとして使っている。
- `RenCrow_LLM` の現在の Chat backend も `/Users/yukimi/models/gemma-4-12B-it-4bit` で、Chat系音声直接入力の最有力実装先。
- ただし、現時点で確認した `RenCrow_LLM` の OpenAI互換仕様と `alias_proxy.py` は text / image / video / **input_audio** を受け付ける。
- `picoclaw_multiLLM` 側は `MessagePartAudio` を追加済み。`picoclaw chat --audio-direct WAV` または Viewer 添付 WAV で Chat LLM へ直接投入できる。

## 実装方針メモ
Chat系へ音声を直接入れる場合は、まず `RenCrow_LLM` に音声partを受ける公式インタフェースを追加し、`picoclaw_multiLLM` 側に `MessagePartAudio` と Viewer音声添付/送信経路を追加するのが自然。既存STTは字幕・ログ・fallback用に残し、速度測定で `STT -> text -> Chat` と `audio -> Chat` を比較する。
