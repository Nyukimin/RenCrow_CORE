# AUDIO_Client仕様（実装直結）

本ディレクトリは、**Chat から見た接続契約・呼び出し手順**を定義する。  
実装者は本READMEを起点に、順に仕様を反映する。

## 1. 実装対象

### STT
- `STT/STT仕様.md`
- `STT/STT_API.md`
- `STT/STT_ノイズ抑制・誤り訂正仕様.md`（参照のみ。正本はServer側）

### TTS
- `TTS/TTS仕様.md`
- `TTS/TTS_API.md`

## 2. 実装手順（Client側）
1. `*_API.md` を実装I/Fの一次参照として固定
2. Chat 入出力DTOを API 契約に合わせる
3. timeout/retry/fallback を `仕様.md` に合わせて設定
4. STT/TTS ともに Provider 非依存（抽象名）で呼び出す
5. 複数本音声（chunk/track）の再生順制御を実装

## 3. 実装ルール
- 単体で成立する文書のみを正規扱いにする
- Provider 名（Whisper/SBV2/Irodori）は実装例扱いに限定する
- 実装詳細は `AUDIO_Server仕様` 側を参照する

## 4. API-DOD（Client側）
- `STT_API.md`: `API-DOD-STT-C-*`
- `TTS_API.md`: `API-DOD-TTS-C-*`
- 実装PRでは、該当IDのチェック結果を記録する

## 5. Client側 DoD
- API 必須フィールドを漏れなく送受信
- `error` 受信時の継続動作を保証
- `audio_chunk_ready` 0回以上を許容
- `(track, chunk_index)` で再生順一意性を担保

## 6. レビュー記録
- 各 `*_API.md` の API-DOD 節にある検証コマンド例で実施する
- 実行証跡（ログ/レスポンス/URL）を `docs/STT_TTS/API_DOD_CHECKLIST.md` に集約する

## 7. 現行実測環境（最新版）
- Chat（RenCrow）例: `https://192.168.1.36:18790`
- STT Provider 例: `http://192.168.1.36:8080`
- TTS Gateway 例: `http://192.168.1.36:8765`
- 実測値は各 `STT仕様.md` / `TTS仕様.md` の「現行実測メモ（最新版）」を正とする
