# TTS仕様（AUDIO_Server仕様）

## 1. 目的
本仕様は、**TTSサーバが提供する API・挙動・運用要件**をベンダー非依存で定義する。  
特定実装（例: SBV2, Irodori）は本仕様に対する適用例として扱う。

## 2. スコープ
- 対象: TTS Gateway（HTTP/WS受け口）と TTS Provider（音声生成エンジン）連携
- 非対象: 音声モデルの学習・再学習

## 3. 基本方針
- TTS は Chat サーバ経由で利用する。
- Provider 差分は Adapter 層で吸収する。
- 音声出力は **単発だけでなく複数本（複数チャンク/複数トラック）** を前提にする。

## 4. 提供責務
- Chat から受けたテキストを音声化し、再生可能な参照を返す。
- Bridge経路（`/health/ready`, `/synthesize`, `WS /sessions`）を提供する。
- Direct経路（`POST /synthesis`）を補助として提供する。

## 5. 提供モード
- Bridgeモード（本線）
- Directモード（補助/互換）

## 6. 挙動要件
- `ready` は推論可能状態を返す。
- WSセッションでは `session_start -> text_delta* -> session_end` を受理する。
- 生成結果は `audio_chunk_ready` を0回以上返却し、最後に `session_completed` を返す。
- 複数本音声を扱うため、順序情報（`chunk_index` 等）を必須で扱う。

## 7. 複数本音声要件
- 1応答内で複数チャンク（文節単位）を生成できること。
- 再生順を保持するメタデータを返すこと。
- chunk欠落時でもセッション全体を即失敗にせず、継続/スキップを選択可能にする。
- 将来の複数話者/複数トラック（例: narrator + assistant）に拡張可能な設計にする。

## 8. エラー要件
- 入力不正は `400` 系、内部失敗は `500` 系。
- WSは `type=error` を返し、セッション単位で復旧可能にする。
- timeout を明示し、無限待ちを作らない。

## 9. 運用要件
- 接続基盤は Tailscale を標準とする。
- `audio_url` を返せる配信経路を準備する。
- 同時推論数は保守的に制限し、安定性を優先する。
- 既定ポートは `8765`（環境に応じて変更可）。
- GPU運用時はデバイス設定を明示し、起動後に `GET /health/ready` で `device` / `cuda_available` を確認する。

## 10. 実装差分注記
- Provider固有APIのみでは Bridge契約を満たさない場合がある。
- Chat互換性を満たすため、Bridge API同居または変換層で補完する。

## 11. 実装例（現行採用）
- 現行例: SBV2
- 将来候補: Irodori 等
- いずれも本仕様の契約（Bridge/Direct/複数本音声）を満たす場合に採用可能。
