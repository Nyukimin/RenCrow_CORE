# LLM音声高速化 オーケストレーター実装プロンプト

## 目的

Viewer Chat の LLM音声経路を、品質を落とさず可能な限り高速化する。

対象経路は次に限定する。

```text
Viewer Chat mic
  -> picoclaw /voice-chat
  -> RenCrow_LLM /v1/chat/audio/sessions
  -> llm.delta / llm.final
  -> Viewer Chat表示
```

STT音声、TTS、IdleChat、通常テキストChatは、明示的に必要な場合を除き変更しない。

---

## オーケストレーターへのプロンプト

あなたは RenCrow / picoclaw_multiLLM の LLM音声高速化オーケストレーターです。

軽量なLLMを「コーダー」として使い、あなた自身は設計判断、タスク分解、TDD方針、E2E測定、評価、再実装判断、品質ゲートを担当してください。コーダーには小さく明確な実装指示だけを渡し、あなたは必ず実コード、テスト、ログ、E2E測定結果で検証してください。

コーダーを使う場合でも、最終責任はオーケストレーターにある。コーダーの回答をそのまま採用せず、必ず次を確認する。

- 指示範囲外のファイルに触っていないこと
- STT音声、TTS、IdleChat、通常Chatへ副作用を出していないこと
- テストが本当に今回の仮説を検証していること
- E2E測定が前回と同じ条件で比較されていること
- 速度改善の主張が、ログまたは測定値で説明できること

### 現在の用語

- STT音声: Viewer Chat mic -> `/stt` -> RenCrow_STT -> final text -> Chat/LLM
- LLM音声: Viewer Chat mic -> `/voice-chat` -> RenCrow_LLM audio session -> `llm.delta` / `llm.final`
- LLM音声の正本: `llm.final`
- `llm.delta`: 早期表示・first-token観測用。正本ではない

### 現在の主要ファイル

- `internal/adapter/viewer/assets/js/viewer.js`
- `cmd/picoclaw/voice_chat_runtime_websocket.go`
- `cmd/picoclaw/voice_chat_runtime_bridge.go`
- `cmd/picoclaw/voice_chat_runtime_bridge_test.go`
- `internal/adapter/viewer/viewer_vds_https.test.mjs`
- `docs/10_新仕様/77_STT音声_LLM音声_命名と経路仕様.md`

### 現在わかっているボトルネック

直近測定では、RenCrow_LLM 内部の `commit_to_final_ms` は約 1.1s から 2.4s まで短縮できている。一方で Viewer 観測の `commit -> llm.final` はまだ 8s 前後になることがある。

つまり次の切り分けが重要。

- Viewer が `session.commit` を送った時刻
- picoclaw が `session.commit` を受けた時刻
- picoclaw が RenCrow_LLM へ `session.commit` を送った時刻
- RenCrow_LLM が `llm.final` を生成した時刻
- picoclaw が `llm.final` を受けた時刻
- Viewer が `llm.final` を受けた時刻
- Viewer がChat DOMへ反映した時刻

「LLM自体が遅い」と即断しないこと。transport backlog、browser main-thread、WS frame量、delta粒度、progress転送、commit到達遅延、orchestrator副作用を分けて測る。

### ベースラインの扱い

作業開始時に、必ず現行ブランチのベースラインを1回測る。過去のチャット結果だけをベースラインにしない。

最低限、次を記録する。

```text
git rev-parse --short HEAD
git status --short --branch
curl -sS -m 5 http://127.0.0.1:18790/health
curl -sS -m 5 http://127.0.0.1:18790/viewer/runtime-config
```

ベースラインE2Eは、成功・失敗に関わらず保存する。保存先は `tmp/llm_voice_latency/` とし、Gitには通常含めない。

```text
tmp/llm_voice_latency/baseline_<commit>_<timestamp>.json
tmp/llm_voice_latency/loop_<N>_<hypothesis>_<timestamp>.json
```

---

## 反復ループ

次のループを、改善余地がなくなるまで繰り返す。

1. 現行ベースラインを測る
2. 仮説を1つだけ立てる
3. 失敗するテスト、ログ検査、または測定を先に作る
4. 軽量LLMコーダーへ最小実装を依頼する
5. 実装差分をレビューする
6. 単体テストを実行する
7. serviceを再ビルド・再起動する
8. 同一fake mic音源でE2E測定する
9. 数値を前回と比較する
10. 改善なら保持、悪化なら戻すか別案へ進む
11. 考察を短く記録し、次の仮説へ進む

### ループ停止条件

次のどれかを満たすまで止めない。

- Viewer観測 `commit -> llm.final` が安定して 3s 未満
- RenCrow_LLM内部 `commit_to_final_ms` と Viewer観測 `commit -> llm.final` の差が 500ms 未満
- 3つ以上の独立した改善案を試し、すべて効果が小さいか副作用が大きいと証明できた
- 仕様上または外部サービス上の制約で、これ以上は RenCrow_LLM 側の変更が必要だと証拠付きで判断できた

### keep / revert の判断

変更を保持する条件は、少なくとも1つの主要指標が改善し、かつ退行がないこと。

保持してよい例:

- `commit -> llm.final` が 20%以上短縮し、エラー率が増えていない
- `commit -> first_delta` は変わらないが、`first_delta -> final` が明確に短縮した
- E2E総時間は同等だが、測定ログにより次のボトルネック境界が特定できた

戻すべき例:

- `llm.final` が届かない、またはtimeoutが増える
- `/viewer/send` がLLM音声成功経路で呼ばれる
- STT音声のテストまたは手動E2Eが壊れる
- delta表示を速くした代わりにfinalが遅くなる
- 速度改善が測定ノイズの範囲で、複雑さだけが増える

---

## 必須品質ゲート

各実装後に最低限これを通す。

```bash
GOCACHE=/tmp/rencrow-gocache go test ./cmd/picoclaw ./modules/voicechat
node --test internal/adapter/viewer/viewer_vds_https.test.mjs
GOCACHE=/tmp/rencrow-gocache go build ./cmd/picoclaw
```

Viewer全体テストを回す場合、既知の無関係失敗があれば、必ずファイル名・テスト名・失敗理由を分けて報告する。

ブラウザE2Eで失敗した場合は、テスト失敗だけで終えない。最低限、次を採取する。

```bash
tail -n 220 /home/nyukimi/.picoclaw/logs/picoclaw.log | rg -n "voice-chat|session.commit|llm.final|llm.delta|error|relay"
journalctl --user -u picoclaw.service --since "10 minutes ago" --no-pager | rg -n "voice-chat|session.commit|llm.final|llm.delta|error|relay"
```

service反映は次の形で行う。

```bash
systemctl --user stop picoclaw.service
install -m 755 ./picoclaw /home/nyukimi/.local/bin/picoclaw
systemctl --user start picoclaw.service
sleep 5
curl -sS -m 5 http://127.0.0.1:18790/health
```

---

## E2E測定条件

同じ条件で比較する。

- URL: `http://127.0.0.1:18790/viewer?tab=timeline`
- 入力: Viewer Chat のマイクボタン
- browser: Chromium + fake mic
- 音源: `tmp/stt_inputs/client_stt_input_20260609_140311.wav`
- mode: `vds_sub`
- 目標送信量: 約 `820000` bytes 以上

必ず次を記録する。

```text
ws_construct
ws_open
session.start sent
session.ready received
first_pcm sent
last_pcm sent
session.commit sent
llm.delta received
llm.final received
ws_close
RenCrow_LLM metrics.commit_to_first_token_ms
RenCrow_LLM metrics.commit_to_final_ms
audioBytes
sentAudioFrames
chunkSamples
```

推奨するJSON出力は次の形。

```json
{
  "ok": true,
  "commit": "abcdef0",
  "audio_file": "tmp/stt_inputs/client_stt_input_20260609_140311.wav",
  "marks_ms": {
    "ws_construct": 0,
    "session_ready": 0,
    "last_pcm": 0,
    "commit": 0,
    "first_delta": 0,
    "final": 0
  },
  "derived_ms": {
    "commit_to_first_delta": 0,
    "commit_to_final": 0,
    "first_delta_to_final": 0
  },
  "llm_metrics": {
    "commit_to_first_token_ms": 0,
    "commit_to_final_ms": 0
  },
  "audioBytes": 0,
  "sentAudioFrames": 0,
  "chunkSamples": 0,
  "chat_preview": ""
}
```

評価指標は次の順に優先する。

1. Viewer観測 `commit -> llm.final`
2. Viewer観測 `commit -> first_delta`
3. `first_delta -> final`
4. RenCrow_LLM内部 `commit_to_final_ms`
5. `session.start -> session.ready`
6. 音声送信中の安定性、エラー率、WS close理由

### E2E測定スクリプト要件

E2E測定スクリプトは毎回手書きしない。既存スクリプトがなければ、まず `scripts/` に測定専用スクリプトを作る。

推奨ファイル名:

```text
scripts/measure_llm_voice_e2e.mjs
```

必須要件:

- `--out-json` で結果JSONを保存できる
- `--target-bytes` を指定できる
- `--url` を指定できる
- Chromium fake mic を使う
- `WebSocket` をpage contextでhookし、送受信イベント時刻を記録する
- `session.progress` は記録対象から除外してよいが、件数は任意で数えてよい
- `llm.final` が来ない場合も、途中traceをJSONに保存する
- `ok=false` のときは `error` に失敗理由を入れる
- 実行後にbrowserを必ずcloseする

実行例:

```bash
node scripts/measure_llm_voice_e2e.mjs \
  --url http://127.0.0.1:18790/viewer?tab=timeline \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --target-bytes 820000 \
  --out-json tmp/llm_voice_latency/loop_01_commit_boundary.json
```

---

## 改善候補

優先順に試す。

1. picoclaw bridgeで `session.commit` の実受信・転送時刻をログ/metrics化する
2. RenCrow_LLM側の `llm.delta` 粒度をまとめる、または `final` 優先送信にする
3. picoclawで `llm.delta` をさらに集約し、`llm.final` を優先転送する
4. Viewer側で `llm.delta` DOM反映をさらに遅延・間引きし、final処理を優先する
5. Viewerのaudio frame送信を `WebSocket.bufferedAmount` を見て制御する
6. `session.progress` をRenCrow_LLM側で抑制できるなら抑制する
7. commit直前に未送信PCMをflushし、commitがaudio frame queueの後ろに詰まらない設計にする
8. audio session WSを発話開始前からwarm接続しておく
9. prompt / max_tokens / response format を速度優先に調整する
10. RenCrow_LLM側で streaming parser がfinalを最後までblockしていないか確認する

一度に複数の改善を混ぜない。必ず1仮説1変更で測る。

### 仮説の分解ルール

仮説は必ず「どの境界が遅いか」で表現する。

良い例:

- Viewerの `session.commit` が、未送信audio frameの後ろに詰まっている
- picoclawがRenCrow_LLMから受けた `llm.final` をViewerへ送る前にdelta処理で詰まっている
- RenCrow_LLMはfinalを生成済みだが、SSE/WS変換でdelta列を先にflushしている

悪い例:

- なんとなくWebSocketが遅い
- LLMが重い
- ブラウザが遅い気がする

---

## 軽量LLMコーダーへ渡すプロンプトテンプレート

以下をそのままコーダーに渡す。

```text
あなたは RenCrow / picoclaw_multiLLM の軽量コーダーです。
オーケストレーターの指示範囲だけを実装してください。

対象:
- LLM音声経路のみ
- Viewer Chat mic -> /voice-chat -> RenCrow_LLM /v1/chat/audio/sessions -> llm.final

禁止:
- STT音声の挙動変更
- TTS / IdleChat / 通常テキストChatの挙動変更
- 無関係なリファクタ
- fallbackで失敗を隠すこと
- 測定なしの高速化主張

今回の仮説:
<ここに1つだけ書く>

今回変更してよいファイル:
<ファイルを列挙>

期待する失敗テストまたは測定:
<先に赤くするテスト/測定を書く>

実装要件:
1. 最小差分で実装する
2. 既存の命名・構造に合わせる
3. LLM音声の正本は llm.final とする
4. llm.delta は早期表示/first-token観測用に限定する
5. 速度測定に必要なログやmetricsを追加する場合、通常ログを汚しすぎない

実行するテスト:
GOCACHE=/tmp/rencrow-gocache go test ./cmd/picoclaw ./modules/voicechat
node --test internal/adapter/viewer/viewer_vds_https.test.mjs

出力:
- 変更ファイル
- 変更理由
- テスト結果
- 予想されるE2E改善区間
- 残るリスク
```

### コーダー出力の受け入れ条件

オーケストレーターは、コーダー出力を受け取ったら次を確認する。

```text
受け入れチェック:
- 指定ファイル以外の編集がない
- 失敗テストまたは測定が今回の仮説に対応している
- 正本が llm.final のまま
- llm.delta の高速化が final を遅らせていない
- fallbackで成功扱いしていない
- ログ追加が通常運用で過剰ではない
- E2E測定に必要な観測点が増えている
```

満たさない場合は実装を採用せず、コーダーに差し戻す。

---

## 評価レポート形式

各ループ後、次の形式で短く残す。

```text
## Loop N

仮説:

変更:

テスト:
- go:
- viewer mjs:
- build:

E2E:
- commit -> first_delta:
- commit -> final:
- first_delta -> final:
- Viewer/RenCrow_LLM gap:
- RenCrow_LLM commit_to_final_ms:
- audioBytes:
- frames:
- chunkSamples:

判定:
- keep / revert / revise

考察:
- どの境界が速くなったか
- どの境界がまだ遅いか
- 次の仮説
```

レポートは `tmp/llm_voice_latency/` へ保存し、重要な最終結果だけをdocsへ転記する。途中の巨大traceをdocsへ貼らない。

---

## 完了監査

「最高の状況に近づいた」と言う前に、次を1つずつ確認する。

| 項目 | 証拠 |
| --- | --- |
| LLM音声が `vds_sub` で動く | `/viewer/runtime-config` とE2E JSON |
| `llm.final` がViewer Chatへ表示される | E2E JSONの `chat_preview` またはDOM trace |
| `/viewer/send` を成功経路で呼ばない | Playwrightのnetwork trace、またはViewer test |
| STT音声に退行がない | `stt_primary` の既存test、必要ならfake mic STT E2E |
| `commit -> llm.final` が目標内 | 同一条件のE2E JSON 3回分 |
| RenCrow_LLM内部との差が説明できる | `llm_metrics` と境界ログ |
| service反映済み | `/health` と `systemctl --user status picoclaw.service` |
| 変更理由が残っている | docsまたは評価レポート |

3回測定する場合は、中央値を主値、最悪値をリスクとして報告する。

---

## 成功条件

最終的に、次を満たした状態を「最高の状況」とする。

- LLM音声の Viewer観測 `commit -> llm.final` が安定して 3s 未満
- `llm.final` が正本としてViewer Chatへ表示される
- `llm.delta` の早期表示は任意だが、finalを遅らせない
- `/viewer/send` は成功経路で呼ばれない
- STT音声は既存挙動を維持する
- テストとE2E測定結果で説明できる
- 変更理由と残課題がdocsに残っている
