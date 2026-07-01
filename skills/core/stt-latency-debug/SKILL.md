# STT Latency Debug

## Purpose

RenCrow の STT 問題を、module ownership、live service 経路、送受信同時計測、Viewer input boundary に分けて調査する。

## When to Use

- ユーザーが STT、音声入力、partial、final、latency、mic、`/stt/chat-input` に関する不具合を依頼した。
- `RenCrow_STT` と `picoclaw_multiLLM/modules/stt` のどちらが owner か曖昧。
- Viewer voice input の実ブラウザ確認が必要。

## Ownership

- STT engine / model / transcription owner: `/home/nyukimi/RenCrow/RenCrow_STT`
- Core runtime gateway / Viewer integration owner: `/home/nyukimi/RenCrow/picoclaw_multiLLM`
- CLI audio-file input owner: `/home/nyukimi/RenCrow/RenCrow_CMD`

## Procedure

1. 症状が engine、gateway、Viewer、CLI のどこに見えるか分ける。
2. active service の WorkingDirectory と endpoint を確認する。
3. timing probe は送信と受信を同時に行う。send 完了後に receive を始めた数値を latency として扱わない。
4. audio fixture、browser mic、secure context、permission denied を区別する。
5. Viewer input では final transcript が chat input / send flow に到達したかを確認する。
6. live service 反映が必要なら `picoclaw-service-rebuild-restart` を使う。
7. 報告用ログ集約が必要なら既存 `stt-debug-report` skill を併用する。

## Verification

- owner module が明確。
- request / response timing が同時計測。
- transcript normalization と finalization の境界が確認済み。
- Viewer 経路の場合、実ブラウザで入力反映または失敗表示を確認済み。

## Safety

- receive-after-send の数値を latency として報告しない。
- `RenCrow_STT` の問題を core runtime だけで直そうとしない。
- browser permission failure を STT engine failure と混同しない。

