# STT_ノイズ抑制・誤り訂正仕様（AUDIO_Server仕様）

## 1. 目的
本仕様は、STTサーバ実装における品質向上機能として、
- ノイズ抑制（Noise Suppression）
- 誤り訂正（Error Correction）
をベンダー非依存で定義する。

## 2. スコープ
- 対象: STTサーバ系（STT Gateway / 前処理 / Provider Adapter）
- 非対象: 認識モデルの学習・再学習

## 3. 基本方針
- STT/TTS は Chat サーバ経由接続を前提とする。
- 生認識文字列（raw）を保持し、補正結果（normalized）を別管理する。
- 危険操作に関わる補正は自動確定せず、確認フローへ渡す。

## 4. 責務境界
- Server責務: ノイズ抑制、候補生成、補正判定（`decision`）まで。
- Chat責務: `decision` を受けた最終意図確定とユーザー確認。
- 原則: Server は実行命令を確定しない。

## 5. ノイズ抑制仕様

### 5.1 現状
- 採用方式は未確定（実装選定前提）。

### 5.2 要求
- 誤認識率の低減
- 会話遅延の許容範囲維持
- 過抑制による音声破綻回避

### 5.3 適用候補
1. Browser前処理
2. Browser側 RNNoise 等
3. Server側前処理

### 5.4 フェーズ方針
- Phase1: Server前処理のみ
- Phase2: 条件付きで補助処理を追加
- Phase3: A/B 結果で最終方式を固定

### 5.5 未検討事項（要決定）
- 採用方式（Browser/Server/併用）
- 既定ON/OFF
- しきい値・プロファイル（静音/騒音）

## 6. 誤り訂正仕様

### 6.1 パイプライン
1. raw 受信
2. 軽量正規化
3. 単語分割
4. 辞書照合
5. 未知語率算出
6. 判定フラグ決定

### 6.2 判定フラグ
- `OK`
- `RECONSTRUCT`
- `CONFIRM`

### 6.3 判定優先順位
1. 危険語かつ確信不足 -> `CONFIRM`
2. 未知語率が閾値超過 -> `RECONSTRUCT`
3. それ以外 -> `OK`

### 6.4 曖昧判定条件
- 対象候補が複数
- 操作候補が複数
- 指示語参照未解決
- 文骨格不足
- 危険語で確信が低い

### 6.5 LLM利用制約
- 初期方針: 正規化のみなら LLM 不要
- 利用対象: `RECONSTRUCT` の補正候補生成
- 非対象: 最終意図確定、危険操作判断、実行命令生成

## 7. データ契約

### 7.1 入力
- `final_text` (string)

### 7.2 出力
```json
{
  "raw_text": "string",
  "normalized_text": "string",
  "normalized_candidates": ["string"],
  "decision": "OK | RECONSTRUCT | CONFIRM",
  "needs_user_confirmation": true,
  "signals": {
    "unknown_token_ratio": 0.0,
    "danger_keyword_hit": false,
    "dictionary_version": "v1"
  }
}
```

## 8. エラー契約
- 辞書参照失敗: fail-open（rawで継続）
- 補正処理失敗: `OK` フォールバック + ログ記録
- 危険語検出: `CONFIRM` 優先

## 9. 運用要件
- 辞書はバージョン管理
- 補正前後テキストは監査可能に保持（PII配慮）
- 誤訂正率・遅延を定期レビュー

## 10. 暫定閾値（初期値）
- `unknown_token_ratio_threshold = 0.35`
- `danger_keyword_confidence_threshold = 0.70`
- `max_reconstruct_candidates = 3`
- `normalizer_timeout_ms = 80`

## 11. 実装例（現行採用）
- Provider: Whisper
- 辞書/補正ロジックは Chat/Server の軽量実装

本仕様は Whisper 固有に依存しない。Provider を他STTへ差し替えても、上記契約を維持すれば成立する。

## 12. 検証コマンド例
以下は実装/運用時に最低限確認するための例。実環境では URL・ポート・ファイルパスを置換する。

### 12.1 STT Gateway 疎通（health）
```bash
curl -sS "http://127.0.0.1:8787/health" | jq
```
期待:
- `{ "ok": true }` を返す。

### 12.2 STT Provider 推論疎通（multipart）
```bash
curl -sS -X POST "$STT_PROVIDER_URL" \
  -F "file=@sample.wav" \
  -F "response_format=json" | jq
```
期待:
- `text` フィールドを含む JSON が返る。

### 12.3 補正出力の確認（decision/signals）
```bash
# ログ出力先は実装に合わせて置換する
rg "decision|unknown_token_ratio|danger_keyword_hit|dictionary_version" "logs/stt.log"
```
期待:
- `decision` と `signals.*` が監査可能な形で記録される。

### 12.4 辞書バージョン反映確認
```bash
rg "dictionary_version" "logs/stt.log"
```
期待:
- 運用中の辞書バージョンが追跡できる。

### 12.5 失敗時フォールバック確認（fail-open）
```bash
# 失敗注入例: 到達不能URLを設定して起動
STT_PROVIDER_URL="http://127.0.0.1:9/inference" <stt-gateway起動コマンド>
```
期待:
- 補正処理失敗時に処理全体が停止せず、`OK` フォールバックまたは継続可能失敗として扱われる。
