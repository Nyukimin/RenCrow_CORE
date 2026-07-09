# RenCrow_TTS_02 起動手順（Chat連携向け）

## 通常起動（HTTP）

前提:
- `D:\RenCrow` 側で SBV2 A/B (`8765`, `8766`) が起動済み

```powershell
cd D:\GenerativeAI\RenCrow_TTS_02
powershell -NoProfile -ExecutionPolicy Bypass -File .\start-rencrow-tts-sbv2.ps1
curl.exe -sS http://127.0.0.1:8770/health/ready
```

## HTTPS起動（Chat本体向け）

前提:
- `certs/rencrow-tts.crt`（PEM証明書）
- `certs/rencrow-tts.key`（PEM秘密鍵）

```powershell
cd D:\GenerativeAI\RenCrow_TTS_02
powershell -NoProfile -ExecutionPolicy Bypass -File .\start-rencrow-tts-sbv2-https.ps1
curl.exe -k -sS https://127.0.0.1:8770/health/ready
```

## Chat側で使う接続先

- `RENCROW_TTS_URL=https://127.0.0.1:8770`
- `RENCROW_TTS_WS_URL=wss://127.0.0.1:8770/sessions`
