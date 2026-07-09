# Request: STT Streaming 40 Client Source Preparation

## 目的

`40_STT_Streaming実装作業仕様.md` を source-level で実装・検証するため、RenCrow / RenCrow Viewer Go クライアント側リポジトリをこちらで触れる状態にしてください。

`39_STT_Streaming暫定確定字幕仕様.md` は参考資料で、実装の主仕様は `40_STT_Streaming実装作業仕様.md` です。

## 必要なもの

以下のどれか 1 つをお願いします。

1. 実装対象リポジトリのローカルパスを教える
2. `/Users/yukimi/GenerativeAI/RenCrow_Code` に実装対象ソースを配置する
3. clone URL と branch 名を教える
4. Ubuntu 側など別マシンにある場合、こちらから読める SSH / rsync / tar 配布手順を教える

## 期待するソースツリー

最低限、以下のファイルが存在するリポジトリが必要です。

```text
internal/adapter/viewer/assets/js/viewer.js
internal/adapter/viewer/viewer.html
internal/adapter/viewer/assets/css/viewer.css
cmd/rencrow/stt_runtime_websocket.go
cmd/rencrow/main_stt_gateway_test.go
internal/adapter/viewer/viewer_stt_https.test.mjs
scripts/stt_e2e_probe.py
scripts/stt_e2e_probe_test.py
scripts/stt_viewer_browser_e2e.js
```

## 準備後にこちらで実行する確認

ソースが用意できたら、まずこの監査を実行します。

```bash
/Users/yukimi/GenerativeAI/ops/macbook207/audit_stt_streaming_40_source_tree.sh /path/to/repo
```

`/Users/yukimi/GenerativeAI/RenCrow_Code` に配置した場合は、以下で確認します。

```bash
/Users/yukimi/GenerativeAI/ops/macbook207/audit_stt_streaming_40_source_tree.sh /Users/yukimi/GenerativeAI/RenCrow_Code
```

これが通ったら、`40_STT_Streaming実装作業仕様.md` に沿って以下を進めます。

- Viewer 側 STT streaming 実装確認・不足修正
- RenCrow STT bridge の frame 透過確認・不足修正
- `scripts/stt_e2e_probe.py` の PCM16 raw streaming 確認・不足修正
- browser E2E gate の確認・不足修正
- source-level tests の実行

## こちらで予定している検証コマンド

実装リポジトリ root から以下を実行予定です。

```bash
node --test internal/adapter/viewer/viewer_stt_https.test.mjs
node --test internal/adapter/viewer/viewer_memory_panel.test.mjs
python3 -m py_compile scripts/stt_e2e_probe.py scripts/stt_e2e_probe_test.py
python3 -m unittest scripts/stt_e2e_probe_test.py
GOCACHE=/tmp/rencrow-gocache go test ./cmd/rencrow ./internal/adapter/viewer ./internal/infrastructure/stt -count=1
git diff --check
```

## 補足

稼働中 Viewer の live asset 監査では、Viewer 側の主要挙動はかなり確認できています。

```bash
/Users/yukimi/GenerativeAI/ops/macbook207/audit_stt_streaming_40_live_assets.sh
```

ただしこれは配信中 HTML/JS/CSS の black-box 監査であり、source-level 実装完了の証拠にはできません。`40` 完了には実装リポジトリと source-level tests が必要です。

