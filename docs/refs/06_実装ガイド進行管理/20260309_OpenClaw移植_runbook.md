# Runbook: OpenClaw移植 実機検証

**作成日**: 2026-03-09  
**対象ブランチ**: `integration/openclaw-parity`

---

## 1. 目的

`TTSを導入して` という依頼に対し、提案止まりではなく実再生まで完了することを実機で検証する。

---

## 2. 事前条件

- サービス起動済み（`picoclaw.service`）
- Viewer送信が利用可能
- TTS Providerキー（OpenAI/ElevenLabs）またはローカルTTS環境が有効

---

## 3. 実行手順

1. 依頼送信

```bash
curl -sS -X POST http://127.0.0.1:18790/viewer/send \
  -H 'Content-Type: application/json' \
  -d '{"message":"TTSを導入して"}'
```

2. ログ追跡

```bash
rg -n "ProcessMessage START|proposal|patch|E2E|playback|execution_report|ProcessMessage COMPLETE|ProcessMessage error" \
  ~/.picoclaw/logs/picoclaw.log | tail -n 200
```

3. 証跡確認

- `execution_report.status=passed`
- `E2E playback success`
- `ProcessMessage COMPLETE`

---

## 4. 失敗時の切り分け

### 4.1 Contract不正

- 症状: patch parse error / invalid format
- 対応: Contract Validatorの拒否条件と再生成プロンプトを確認

### 4.2 Provider不達

- 症状: API timeout / unauthorized
- 対応: フォールバック順（OpenAI→ElevenLabs→local）を確認

### 4.3 実行環境不整合

- 症状: 実装成功だが再生失敗
- 対応: 音声出力先、プレイヤー依存、権限を確認

---

## 5. 完了判定

次の3点がそろった場合のみ「完了」。

1. `execution_report.status=passed`
2. `E2E playback success`
3. `ProcessMessage COMPLETE`

