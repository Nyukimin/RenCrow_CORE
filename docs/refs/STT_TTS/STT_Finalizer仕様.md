# STT Finalizer仕様

## 対象

RenCrow本体のViewerマイク入力から `/stt` WebSocketを経由して、最終文字列を通常チャットへ投入するまでのfinalize契約を定義する。

RenCrow_STTは文字起こし品質とprovider readinessを担当する。RenCrow本体はViewerの録音停止、WebSocket stop control、final文字列投入、final未返却時のfallbackを担当する。

## 契約

- Viewerはmono PCM16 16kHz chunkを `/stt` へ送る。
- マイク停止時、Viewerは残りchunkをflushし、終端無音を送り、`{"type":"stop"}` を送ってからWebSocketを閉じる。
- Viewerはstop送信前にdraft/partialをfinal扱いしてチャット送信してはいけない。
- `/stt` は `{"type":"stop"}` と `{"type":"final_pending"}` をfinalize controlとして扱う。
- finalize control受信時にdraftがある場合、`/stt` は `{"type":"final","text":"..."}` を返す。
- Viewerがチャットへ投入する文字列は、server final、またはfinal待ちtimeout後のlocal draft fallbackに限る。
- timeout fallback時は `final_fallback=timeout` をSTT capture logへ残す。
- draftも無いままtimeoutした場合は `STT final unavailable` を表示する。

## 計測

- provider HTTP処理時間とマイク録音時間は分離して見る。
- stop受信からfinal返却までをstop-to-final latencyとして扱う。
- 長い録音時間はprovider遅延とはみなさない。

## 確認

- provider/server経路は `scripts/stt_e2e_probe.py` でWebSocket finalを確認する。
- Viewer経路は `scripts/stt_viewer_browser_e2e.js` で `sent_stop=true`、`chat_send_observed=true`、`recv_final=true` または `final_fallback=timeout` を確認する。
