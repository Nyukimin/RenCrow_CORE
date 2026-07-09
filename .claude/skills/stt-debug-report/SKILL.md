---
name: stt-debug-report
description: |
  STT デバッグ解析ワークフロー。tmp/ 配下のログを解析して tmp/stt_share_for_server.md を生成する。
  以下のときに使用:
  - 「STTのログをまとめて」「サーバーに報告する資料を作って」
  - 「STTデバッグを解析して」「stt_share_for_server.md を更新して」
  - STT 動作確認後に tmp/ のファイルを整理・集約したいとき
---

# STT Debug Report

## ワークフロー

```
tmp/client_stt_log.txt          ┐
tmp/stt_e2e_from_mic_latest.json├─ scripts/build_stt_share.py ─→ tmp/stt_share_for_server.md
tmp/stt_compare_report_*.md     │
tmp/voice_bridge_*.log          ┘
tmp/stt_server_analysis_latest.md
```

## 実行コマンド

プロジェクトルート（`/home/nyukimi/picoclaw_multiLLM`）から:

```bash
python3 /home/nyukimi/.claude/skills/stt-debug-report/scripts/build_stt_share.py \
  [--tmp-dir tmp] \
  [--output tmp/stt_share_for_server.md]
```

## スクリプトの動作

`scripts/build_stt_share.py` は以下を行う:

1. `tmp/` 配下の STT 関連ファイルを自動収集（存在するものだけ）
2. 各ファイルの sha256 とサイズを計算
3. `client_stt_log.txt` をパース → session_id・イベント列を抽出
4. `stt_e2e_from_mic_latest.json` をパース → 推論成否・レイテンシを抽出
5. 比較レポート（`stt_compare_report_*.md`）から件数サマリを抽出
6. `tmp/stt_share_for_server.md` を生成・上書き

## 収集対象ファイル

| ファイル | 内容 |
|---|---|
| `client_stt_log.txt` | ブラウザ側 STT イベントログ |
| `client_stt_input_latest.wav` | 最新マイク入力 WAV |
| `stt_e2e_from_mic_latest.json` | `stt_e2e_probe.py` の実行結果 |
| `stt_server_analysis_latest.md` / `.json` | サーバーログ解析結果 |
| `stt_compare_report_*.md` | client/server 比較レポート（最新1件） |
| `voice_bridge_*.log` | サーバーログ切り出し（最新1件） |
| `stt_inputs/*.wav` | アーカイブ WAV 一覧 |

## 送付先ドキュメント（依頼文）

生成後に以下のテンプレートと合わせてサーバー担当へ送付する:

- **依頼文**: `docs/STT_TTS/AUDIO_Client仕様/STT/stt_server_logging_request_path_agnostic_2026-04-13.md`
- **session_id 問い合わせ**: `docs/STT_TTS/AUDIO_Client仕様/STT/stt_server_inquiry_with_proof_2026-04-13.md`

## 注意

- スクリプトはプロジェクトルートから実行すること（`tmp/` の相対パス解決のため）
- ファイルが存在しない場合はそのセクションをスキップ（クラッシュしない）
- 生成済みの `tmp/stt_share_for_server.md` は上書きされる
