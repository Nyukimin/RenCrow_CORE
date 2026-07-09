# RenCrow STT Streaming 実装作業仕様

## 1. 位置づけ

この文書は、`docs/10_新仕様/39_STT_Streaming暫定確定字幕仕様.md` から STT streaming の実装作業だけを切り出した作業用仕様である。

39 は protocol / behavior の仕様として扱う。この 40 は、実装対象、変更ファイル、検証、Goal 実行、停止条件、確認証跡を扱う。

## 2. 実装仕様

この章は、STT streaming 仕様を RenCrow 側と STT server 側に実装するための実装仕様である。単なるプロトコル説明ではなく、変更対象、責務境界、状態管理、検証条件を明示する。

### 2.1 実装対象と非対象

実装対象:

- RenCrow Viewer の microphone capture / STT WebSocket / 字幕表示 / normal chat input 接続。
- RenCrow STT bridge の WebSocket frame 透過。
- `scripts/stt_e2e_probe.py` と `scripts/stt_viewer_browser_e2e.js` による local / browser gate。
- 実装証跡の docs 更新。

RenCrow 側の非対象:

- 207 STT server の WhisperKit provider 実装そのもの。
- STT server の launchd / model path / VAD 閾値変更。
- 実マイク入力デバイスの OS 設定変更。
- STT server の `final` 終端保証の実装。これは server 側依頼仕様として `39_STT_Streaming暫定確定字幕仕様.md` に定義する。

### 2.2 変更ファイルと責務

| ファイル | 責務 |
| --- | --- |
| `internal/adapter/viewer/assets/js/viewer.js` | microphone capture、16kHz PCM16 変換、STT WS control、字幕 UI、final-only chat input 接続 |
| `internal/adapter/viewer/viewer.html` | STT caption / mic UI の DOM anchor |
| `internal/adapter/viewer/assets/css/viewer.css` | mic input level、暫定字幕、確定字幕、error caption の見た目 |
| `cmd/rencrow/stt_runtime_websocket.go` | `/stt` / `/stt-ws` / `/ws` の RenCrow STT bridge、text/binary frame 透過 |
| `cmd/rencrow/main_stt_gateway_test.go` | RenCrow STT bridge の JSON control / binary chunk / final 透過 test |
| `internal/adapter/viewer/viewer_stt_https.test.mjs` | Viewer STT contract test |
| `scripts/stt_e2e_probe.py` | direct/bridge/Tailscale WS probe。WAV decode -> PCM16 raw streaming |
| `scripts/stt_e2e_probe_test.py` | probe の protocol test |
| `scripts/stt_viewer_browser_e2e.js` | Playwright browser gate。実ブラウザの STT WS frame と `/viewer/send` を観測 |

### 2.3 Viewer 実装詳細

Viewer は通常 chat timeline 上の mic button を STT entrypoint とする。IdleChat には音声入力を直接流さない。

#### start

- `navigator.mediaDevices.getUserMedia({ audio: ... })` で microphone stream を取得する。
- `AudioContext` の input sample rate を取得する。
- `ScriptProcessor` で mono PCM を受け取り、16kHz PCM16 little-endian へ resample する。
- STT WebSocket open 後、音声 chunk より前に `start` control を送る。

#### streaming

- PCM16 chunk は WebSocket binary frame として送る。
- chunk は `chunkSamples=1600` を基準に送る。
- WAV header は送らない。
- 送信中の PCM16 から RMS を算出し、mic button の入力レベルとして表示する。
- 入力レベル表示は「音声がブラウザに届いた」証跡であり、STT 認識成功ではない。

#### partial / draft

- `partial` / `draft` は `partialCaptionText` に入れる。
- UI では `暫定字幕: <text>` と表示する。
- `partial` / `draft` は通常 chat input に入れない。
- `partial` / `draft` を final の fallback として送信しない。

#### final

- `final.text` を trim し、空でなければ `finalCaptionText` に入れる。
- UI では `確定字幕: <text>` と表示する。
- 通常 chat timeline でのみ `#inp` に入れ、既存の `send()` に接続する。
- `final` 受信後は `finalReceived=true` として扱い、final wait timeout を解除する。
- `final` 受信後の同一 utterance の `error` は、確定字幕と chat input 接続を上書きしない。
- `final` 受信後に mic stop された場合は、`stop` control を再送せず WebSocket close に寄せる。

#### stop

- `finalReceived=false` の通常停止では、残 chunk を flush し、1 秒の silence tail を送り、`{ "type": "stop" }` を送る。
- `stop` 後は `final` / `error` / timeout / close のいずれかを待つ。
- timeout した場合は `STT final unavailable: timed out waiting for final` を caption と session state に表示し、通常 chat input には送らない。

#### error

- `finalReceived=false` の `error` は `errorCaptionText` に入れ、`STT error: <message>` と表示する。
- stale partial / final を成功扱いにしない。
- `finalReceived=true` の `error` は、確定済み utterance を取り消さない。log / warning に留める。

#### debug render

- STT message handling は debug panel render 例外で止めない。
- `renderDebugPanels()` は safe wrapper 経由で呼び、例外時も `partial` / `final` / `error` の本処理を継続する。

### 2.4 RenCrow STT bridge 実装詳細

RenCrow STT bridge は認識 text を生成しない。STT server への transport boundary である。

- Viewer からの text frame を STT server へそのまま転送する。
- Viewer からの binary frame を STT server へそのまま転送する。
- STT server からの text frame を Viewer へそのまま転送する。
- STT server からの binary frame が来た場合も破壊しない。
- `start` / `stop` / `final_pending` を JSON として解釈して書き換えない。
- `STT_GATEWAY_URL` / `RENCROW_STT_URL` 未設定時の fallback は正常系ではない。E2E 成功扱いしない。

### 2.5 Probe / browser gate 実装詳細

#### `scripts/stt_e2e_probe.py`

- WAV は `wave` で decode し、PCM16 raw だけを WS に送る。
- 接続後に `start` control を送る。
- binary chunk を順に送る。
- 必要に応じて realtime sleep と silence tail を入れる。
- 最後に `stop` を送る。
- `--require-ws-final` 指定時、WS round に `final` がなければ non-zero exit にする。

#### `scripts/stt_viewer_browser_e2e.js`

- Playwright Chromium で Viewer を開く。
- WebSocket frame sent / received を観測する。
- `/viewer/send` を route intercept し、実送信せずに request body を証跡として保持する。
- `sent_start`、`sent_stop`、`sent_binary`、`recv_final`、`chat_send_observed`、`send_message` を JSON に出す。
- fake microphone は診断用であり、実マイク E2E 成功の代替にしない。
- `--real-mic --headed` では、script が mic を開始し、人間が発話後に browser 上の mic button を押して停止する。

### 2.6 STT server 側実装依頼

207 STT server には、39 の通信契約に沿って次を依頼する。

- `final` は同一 `utterance` の終端 event とする。
- `final` 後に同じ `event_id` / `utterance` で `progress`、`partial`、`error` を返さない。
- `final` 後の `empty_transcript` / `NO_SPEECH_DETECTED` は返さない。
- `final` 後に audio frame が来た場合は、同一 utterance では無視するか、新しい utterance として新しい ID を割り当てる。
- `partial` を返した utterance が final で no speech になる場合は、VAD window、final 対象 audio range、PCM bytes / duration、partial が final 候補にならなかった理由を log に残す。

RenCrow Viewer は defensive に `final` 後 error を無視するが、これは server contract 違反の影響を抑えるための保護であり、server 側の終端保証を不要にするものではない。

## 3. 現行実装との差分

| 項目 | 現行 | 本仕様 |
| --- | --- | --- |
| Viewer start | 明示送信していない | 接続後に `start` を送る |
| Viewer stop | 明示送信していない | 停止時に `stop` を送る |
| Viewer audio | PCM16 raw chunk 送信済み | 継続 |
| partial / draft | `partial` / `draft` を lastRecognition として保持 | 暫定字幕 UI として明示し、Chat へ送らない |
| final 未到達時 | 停止時に latest partial を final 扱いで送る補助実装がある | 原則禁止。診断モード以外では Chat へ送らない |
| RenCrow STT bridge | text / binary frame を透過 | 継続 |
| Go fallback WS | provider に chunk ごと WAV 化して推論し `draft` を返す | fallback は正常系ではない。E2E 成功扱いしない |
| WS probe | WAV bytes を直送していた | PCM16 raw + `start` + `stop` に修正する |
| HTTP inference | 保存 WAV の推論確認に使用 | WS streaming の代替にしない |

## 4. 実装タスク一覧

### local regression

- Viewer: WebSocket open 時に `start` control を送る。
- Viewer: `stopSTT()` で `stop` control を送り、`final` / `error` / timeout を待ってから close する。
- Viewer: `partial` / `draft` を暫定字幕として表示する UI を追加する。
- Viewer: `final` 未到達時に latest partial を通常 chat 送信する fallback を削除または診断モードへ隔離する。
- Viewer: `final` 後 error 上書きを防ぎ、debug panel render 例外で STT message handling を止めない。
- Viewer tests: start / chunk / stop / final / final 後 error 保護の contract test を追加する。
- Probe: `scripts/stt_e2e_probe.py` を PCM16 raw + `start` + `stop` に修正する。
- Browser gate: `scripts/stt_viewer_browser_e2e.js` で browser WS frame と `/viewer/send` を観測する。
- Go tests: `/stt` proxy が JSON control と binary chunk を透過することを確認する。

### external dependency

- 207 STT server が `start` / PCM16 raw chunk / `stop` / `final` を安定して返すこと。
- 207 STT server log に `event_id` / `session_id` / VAD / partial / final / error が追跡可能に出ること。
- STT server が `final` を同一 utterance の終端 event として扱うこと。
- Tailscale Viewer 経由で `wss://<tailnet-host>/stt` が継続利用できること。

### blocked

- 実マイク E2E は、ブラウザ端末のマイク権限、入力デバイス、音量、207 STT server 稼働状態に依存する。
- `final` が返らない状態では通常 chat input 接続を完了扱いにしない。
- fallback / partial-only / HTTP file inference 成功は STT streaming E2E 成功ではない。

## 5. 検証チェックリスト

- [ ] Viewer runtime-config が正しい `stt_stream_url` を返す。
- [ ] Viewer が WebSocket open 後に `start` を送る。
- [ ] Viewer が PCM16 little-endian raw chunk を binary frame で送る。
- [ ] Viewer が stop 時に `{ "type": "stop" }` を送る。
- [ ] 207 direct WS で `ready` / `progress` / `partial` / `final` が返る。
- [ ] RenCrow `/stt` proxy WS で direct WS と同等の event が返る。
- [ ] Tailscale `wss://<tailnet-host>/stt` で同等の event が返る。
- [ ] `partial` / `draft` が Viewer 暫定字幕・ログに表示される。
- [ ] `partial` / `draft` が Chat / LLM に送られない。
- [ ] `final.text` だけが通常 chat input に送られる。
- [ ] `final` なし close は未確定終了として扱われる。
- [ ] `final` 後の `error` が確定字幕と chat input 接続を壊さない。
- [ ] debug panel render 例外で STT message handling が止まらない。
- [ ] no speech / timeout / invalid audio / proxy failure が error として見える。
- [ ] `scripts/stt_e2e_probe.py` が WAV whole file を WS に直送しない。
- [ ] `scripts/stt_viewer_browser_e2e.js` が `final -> /viewer/send` を必須 gate として扱える。
- [ ] HTTP `/v1/audio/transcriptions` は file inference として別枠で記録される。

## 6. 実装・確認メモ

### 2026-05-21 実装済み

- Viewer マイク入力レベル表示を追加した。
- Viewer WebSocket open 時に `start` control を送るようにした。
- Viewer 停止時に残り PCM16 chunk を送信後、`stop` control を送り、`final` / `error` / timeout / close を待って終了処理へ進むようにした。
- 207 STT の partial 推論に 6 秒以上かかるケースがあるため、Viewer の final 待ち timeout を 30 秒にした。
- 保存 WAV の realtime probe では 1 秒無音 tail を付けた場合に `final` が安定したため、Viewer 停止時も残り PCM16 chunk の後に 1 秒の無音 tail を送り、その後 `stop` control を送るようにした。
- `partial` / `draft` を通常 chat input へ送る停止時 fallback を削除し、`final.text` のみ通常 chat input に接続する contract test を追加した。
- `partial` / `draft` と `final` を入力欄とは別の STT 字幕 UI に表示するようにした。
- `scripts/stt_e2e_probe.py` を WAV decode -> PCM16 raw chunk -> `start` -> binary chunks -> `stop` protocol に修正し、`final` がない WS 結果を success 扱いしないようにした。
- `scripts/stt_e2e_probe.py` に `--require-ws-final` を追加し、WS round の `final` が欠ける場合は non-zero exit にした。
- `scripts/stt_viewer_browser_e2e.js` を追加し、Viewer browser 経由の `start` / PCM16 binary / `stop` / `final` / `/viewer/send` を Playwright で確認できるようにした。
- RenCrow STT bridge が JSON control と PCM16 binary chunk を透過する E2E test を追加した。
- Viewer は `final` 受信済み状態を保持し、`final` 後の stop で `stop` control を再送せず WS close に寄せるようにした。
- Viewer は `final` 後に STT server から `error` が届いても、確定字幕と chat input 接続を上書きしない。
- Viewer の STT message handling は debug panel render 例外で止めない。

### 2026-05-21 確認済み

- `node --test internal/adapter/viewer/viewer_stt_https.test.mjs`
- `python3 -m py_compile scripts/stt_e2e_probe.py scripts/stt_e2e_probe_test.py`
- `python3 -m unittest scripts/stt_e2e_probe_test.py`
- `GOCACHE=/tmp/rencrow-gocache go test ./...`
- `git diff --check`
- `make install` 後に `rencrow.service` を再起動し、`http://127.0.0.1:18790/health`、local Viewer、Tailscale Viewer 200、配信中 HTML / JS 反映を確認した。
- 2026-05-21 09:18 UTC 時点の `tmp/client_stt_input_latest.wav` は、HTTP file inference / WS streaming ともに `NO_SPEECH_DETECTED` になるため、以後の成功証跡には使わない。
- `tmp/stt_inputs/client_stt_input_20260521_084443.wav` は HTTP file inference で `テストテストテストおわり` を返すことを確認し、この WAV を WS streaming probe の検証入力に使った。
- `tmp/stt_inputs/client_stt_input_20260521_084443.wav` を使い、次の WS endpoint で `ready` / `progress` / `final` を確認した。
  - `ws://192.168.1.207:8766/stt`
  - `ws://127.0.0.1:18790/stt`
  - `wss://fujitsu-ubunts.tailb07d8d.ts.net/stt`
- Playwright Chromium の fake microphone で local Viewer を開き、ブラウザ `getUserMedia` -> Viewer PCM16 chunk -> 207 STT の経路で `start` / binary chunk / `stop` 送信と `partial` 受信を確認した。同 run では 207 STT が `NO_SPEECH_DETECTED` を返し、Viewer は `STT recognition unavailable: 音声が検出されませんでした。` を session 表示に残し、通常 chat input へ送信しなかった。
- Playwright Chromium の fake microphone run で、207 STT から `final` / `error` が返らない場合に Viewer が `STT error: STT final unavailable: timed out waiting for final` を字幕欄と session 表示に残し、通常 chat input へ送信しないことを確認した。
- `python3 scripts/stt_e2e_probe.py --wav tmp/client_stt_input_latest.wav --provider-rounds 0 --ws-rounds 1 --ws-wait 20 --ws-url ws://127.0.0.1:18790/stt --require-ws-final` が、`final` なしの runtime result を exit code 2 として失敗扱いにすることを確認した。同 run では `ready` / `progress` までで timeout しており、STT streaming E2E 成功ではない。
- `python3 scripts/stt_e2e_probe.py --wav tmp/stt_inputs/client_stt_input_20260521_084443.wav --provider-rounds 0 --ws-rounds 1 --ws-wait 70 --ws-realtime --ws-tail-silence-ms 1000 --require-ws-final` を使い、次の WS endpoint で `final` が返ることを確認した。
  - `ws://192.168.1.207:8766/stt`
  - `ws://127.0.0.1:18790/stt`
  - `wss://fujitsu-ubunts.tailb07d8d.ts.net/stt`
- `scripts/stt_viewer_browser_e2e.js` は、送信した binary PCM frame 数、PCM byte 数、16kHz mono PCM16 換算秒数、受信 event type、直近受信 frame、`/viewer/send` request body、network failure を結果 JSON に出す。実マイク gate で失敗した場合は、この JSON を一次証跡として使う。
- `node scripts/stt_viewer_browser_e2e.js --wav tmp/stt_inputs/client_stt_input_20260521_084443.wav --speak-ms 20000 --partial-timeout-ms 30000 --final-timeout-ms 90000 --no-require-final --no-require-send` では、ブラウザから約 21.43 秒分の PCM16 を送信し、207 STT から `partial` を受信したが、停止時に `empty_transcript` error となり、`/viewer/send` は発火しなかった。この結果は fake microphone の診断証跡であり、STT streaming E2E 成功ではない。
- `node scripts/stt_viewer_browser_e2e.js --wav tmp/stt_inputs/client_stt_input_20260521_084443.wav --speak-ms 20000 --partial-timeout-ms 30000 --final-timeout-ms 90000` が exit code 0 となり、`recv_final=true`、`saw_final=true`、`chat_send_observed=true`、`send_message="ですと"` を返す run を確認した。ただし次 run では 207 STT が `empty_transcript` を返したため、fake microphone は依然として実マイク E2E の代替証跡にはしない。
- 207 STT server 実装更新後、`curl http://192.168.1.207:8766/health` が `status=ready`、`provider.status=ok`、`model_loaded=true` を返すことを確認した。
- 207 STT server 実装更新後、`python3 scripts/stt_e2e_probe.py --wav tmp/stt_inputs/client_stt_input_20260521_084443.wav --provider-rounds 0 --ws-rounds 1 --ws-wait 70 --ws-realtime --ws-tail-silence-ms 1000 --require-ws-final` を次の WS endpoint に対して実行し、すべて exit code 0、`events=["ready","ready","progress","progress","final"]`、`final="ちょっと"` を確認した。
  - `ws://192.168.1.207:8766/stt`
  - `ws://127.0.0.1:18790/stt`
  - `wss://fujitsu-ubunts.tailb07d8d.ts.net/stt`
- 207 STT server 実装更新後、`node scripts/stt_viewer_browser_e2e.js --wav tmp/stt_inputs/client_stt_input_20260521_084443.wav --speak-ms 20000 --partial-timeout-ms 30000 --final-timeout-ms 90000` が exit code 0 となり、`saw_partial=true`、`saw_final=true`、`recv_final=true`、`recv_error=false`、`chat_send_observed=true`、`send_message="ですと"` を返すことを確認した。これは browser fake microphone gate の成功証跡であり、実マイク E2E 成功の代替にはしない。
- Playwright Chromium の fake microphone は、run ごとに `partial` / `NO_SPEECH_DETECTED` / timeout の揺れがあり、`final` -> 通常 chat input 送信の完了証跡にはできなかった。
- `node scripts/stt_viewer_browser_e2e.js --no-require-final --no-require-send` で、fake microphone でも browser が `start` / binary chunk / `stop` を送り、`final` がない場合は通常 chat input へ送らないことを確認した。
- `node scripts/stt_viewer_browser_e2e.js --partial-timeout-ms 15000 --final-timeout-ms 15000` は fake microphone で `final` / `/viewer/send` がない状態を exit code 2 として失敗扱いにすることを確認した。実マイク確認ではこの script を final 必須 gate として使う。

### 残る未確認

- 実ブラウザのマイク操作で、Mic ON -> 入力レベル -> `partial` 表示 -> `stop` -> `final` -> 通常 chat input 送信までを 1 セッション通して確認すること。
- no speech は実 runtime 表示まで確認済み。provider timeout / invalid audio / proxy failure などの error path は、実 runtime 表示として網羅確認すること。

実マイク gate:

```bash
node scripts/stt_viewer_browser_e2e.js --real-mic --headed --partial-timeout-ms 30000 --final-timeout-ms 70000
```

headed browser が開くと script がマイクを開始する。十分な音量で発話し、ブラウザ上のマイクボタンをクリックして停止する。script は mic off を検出してから `final` と `/viewer/send` を待つ。この command が exit code 0 で、`recv_final=true`、`chat_send_observed=true`、`send_message` 非空を返した場合だけ、実ブラウザ実マイク STT E2E 成功とする。

自動停止で確認したい場合は、発話時間を指定する。

```bash
node scripts/stt_viewer_browser_e2e.js --real-mic --headed --speak-ms 6000 --partial-timeout-ms 30000 --final-timeout-ms 70000
```

## 7. 分類

| 分類 | 項目 |
| --- | --- |
| local regression | Viewer start/stop control、partial UI、final-only Chat 接続、probe 修正、RenCrow STT bridge test |
| external dependency | 207 STT server、WhisperKit、MacBook 207 launchd、Tailscale Serve |
| blocked | 実マイク・実ブラウザ・207 runtime が必要な E2E、final 未返却時の chat 接続 |

## 8. Goal 実行用作業ルール

この実装作業仕様を Goal に設定して実装する場合は、未完了項目を小さな検証済み commit 単位で順に処理する。

各単位では、実装前に次を定義する。

- 対象: Viewer / RenCrow STT bridge / probe / docs / runtime 確認のどれか。
- 変更範囲: 触るファイルと触らないファイル。
- 検証コマンド: Node test、Go test、`git diff --check`、runtime / Viewer 確認のどれを行うか。
- 完了条件: `partial` / `draft` / `final`、Chat input、error 表示、bridge 透過など、何が確認できれば完了か。

実装後は、該当テストと必要な runtime / Viewer 確認を行う。確認済みの関連ファイルだけを選択的に stage し、日本語 commit message で commit する。commit 後は push する。push できたら、不要なユーザー確認を待たずに次の未完了項目へ進む。

### 推奨 commit 単位

1. `scripts/stt_e2e_probe.py` を PCM16 raw + `start` + `stop` protocol に修正する。
2. Viewer から WebSocket open 時に `start` を送る。
3. Viewer 停止時に `stop` を送り、`final` / `error` / timeout を待ってから close する。
4. Viewer の `partial` / `draft` 暫定字幕 UI を追加する。
5. `final` 未到達時に latest partial を通常 chat 送信する fallback を削除または診断モードへ隔離する。
6. Viewer の final 後 error 上書き防止と debug render 例外隔離を実装する。
7. RenCrow STT bridge の JSON control / binary chunk 透過 test を追加する。
8. runtime / Viewer E2E 確認結果をこの作業仕様または残課題台帳へ反映する。

各 commit は 1 つの責務だけを持つ。Viewer / RenCrow STT bridge / probe / docs / runtime 証跡を、責務が曖昧なまま 1 commit に混ぜない。

### 標準検証

変更内容に応じて、以下から必要なものを選ぶ。

```bash
node --test internal/adapter/viewer/viewer_stt_https.test.mjs
node --test internal/adapter/viewer/viewer_memory_panel.test.mjs
GOCACHE=/tmp/rencrow-gocache go test ./cmd/rencrow ./internal/adapter/viewer ./internal/infrastructure/stt -count=1
git diff --check
```

runtime / Viewer に触れた場合は、必要に応じて次も確認する。

- `systemctl --user stop rencrow.service` を含むクリーン停止後に `make install` / restart する。
- `http://127.0.0.1:18790/health` が OK である。
- `/viewer/runtime-config` が 207 STT / TTS endpoint を返す。
- LAN または Tailscale Viewer で Mic ON から `ready` / `progress` / `partial` / `final` を 1 セッション追う。
- Tailscale Viewer 配信に関わる場合は `tailscale serve status --json` と Viewer HTTPS 200 を確認する。

### stage / commit / push ルール

- worktree 全体を一括 stage しない。
- 確認済みの関連ファイルだけを `git add` する。
- 日本語 commit message で commit する。
- commit 後は push する。
- push 後に、commit hash、検証コマンド、次に進む対象を短く報告する。
- push 対象に未確認差分、別責務の差分、live E2E 生成物、一時生成物、`vault/` が混ざりそうな場合は停止する。

### 停止条件

次の場合だけ作業を止めて報告または質問する。

- テスト失敗や runtime / Viewer 確認失敗が、現在の作業範囲内で短時間に解消できない。
- 変更が複数領域へ広がり、1 commit の責務が曖昧になった。
- 207 STT server、MacBook launchd、ブラウザ実マイク、Tailscale、外部 secret など、作業者側で準備できない依存が必要になった。
- destructive operation、依存追加、CI / deploy 設定変更、ファイル削除、セーフガード変更が必要になった。
- push 対象に未確認差分、別責務の差分、または live E2E 証跡ファイルが混ざりそうになった。
- Goal の達成条件が、`39_STT_Streaming暫定確定字幕仕様.md` または `docs/01_正本仕様/STT_正本仕様.md` と矛盾している。

### 禁止事項

- blocked / skipped / fail を成功扱いしない。
- fallback 成功、partial-only、HTTP file inference 成功を STT streaming E2E 成功扱いしない。
- `partial` / `draft` を通常 chat input として送る変更を、明示的な診断モードなしに入れない。
- Viewer 表示、STT log、Chat input、TTS 音声、口パク trigger を混同しない。
- live E2E 証跡、`tmp/` の録音、`vault/`、一時生成物を明示指示なく commit しない。
