# STT仕様（AUDIO_Client仕様）

## 1. 目的
本仕様は、**Chat から見た STT 接続契約・呼び出し手順**をベンダー非依存で定義する。  
特定実装（例: Whisper）は本仕様に対する適用例として扱う。

## 2. 基本方針
- STT は **必ず Chat サーバ経由**で利用する。
- クライアントから STT Provider へ直接接続しない。
- Chat は STT Gateway を入口とし、Provider との中継を担当する。

## 3. 接続構成
```text
[Client/Browser]
  -> [Chat Server]
    -> [STT Gateway]
      -> [STT Provider]
```

## 4. 接続前提
- Chat 側の Provider 接続先は設定で管理する（例: `STT_PROVIDER_URL`）。
- 既定値はローカル接続を推奨（例: `http://127.0.0.1:8080/inference`）。
- リモート STT 利用時は FW・到達性・遅延を事前確認する。
- Chat API は HTTPS 提供を前提とし、`/health` と `/ready` の到達性を事前確認する。

## 5. Chat 側呼び出し手順
1. Browser が Chat（`/ws`）へ音声バイナリを送信する。  
2. Chat（STT Gateway）が音声を受信し、発話区間を判定する。  
3. Chat が STT Provider へ推論要求を転送する。  
4. Provider 応答を `draft` / `final` に整形して Browser へ返す。

## 6. 結果の扱い
- `draft`: 発話中の暫定文字列
- `final`: 発話確定文字列（後段入力に利用）
- `error`: UI継続可能な失敗通知

## 7. エラー契約（Client観点）
- Provider 応答失敗時は、Chat 側 fail-safe（空文字継続/通知）を実施する。
- `draft` が欠落しても `final` が取得できれば正常系として扱う。
- `error` 受信時も入力セッションは継続可能にする。

## 8. 運用要件
- STT timeout を設定し、無限待ちを避ける。
- 最小音声サイズしきい値を設定し、断片入力を抑制する。
- 通常運用系統と拡張系統を分離運用する。

## 9. 実装差分注記
- 現行実装では系統差（通常/拡張）により `final_pending` や MIME 処理が異なる。
- ただし本仕様の契約（`draft`/`final`/`error`）は Provider 非依存で維持する。

## 10. 実装例（現行採用）
- STT Gateway: `voice-bridge`
- STT Provider: Whisper
- Provider endpoint例: `/inference`

## 11. 現行実測メモ（最新版）
- Chat（RenCrow）: `https://<chat-host>:18790`
  - `GET /health` -> `{"status":"ok", ...}`
  - `GET /ready` -> `{"ready":true}`
- STT Provider（例）: `http://<stt-host>:8080`
  - `GET /health` -> `{"status":"ok"}`
  - `POST /inference` -> `{"text":"..."}`

本節は運用実測値であり、契約本体は本書の各節（Provider非依存）を正とする。
