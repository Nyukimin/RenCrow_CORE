# 78 LLM音声高速化 実行記録

## 目的

`78_LLM音声高速化_オーケストレーター実装プロンプト.md` に基づき、Viewer Chat の LLM音声経路を測定し、`llm.final` を正本として高速化する。

対象経路:

```text
Viewer Chat mic
  -> picoclaw /voice-chat
  -> RenCrow_LLM /v1/chat/audio/sessions
  -> llm.delta / llm.final
  -> Viewer Chat表示
```

## 追加した測定基盤

### Viewer fake mic E2E

`scripts/measure_llm_voice_e2e.mjs`

主な記録項目:

- `ws_construct`
- `ws_open`
- `session.start`
- `session.ready`
- `first_pcm`
- `last_pcm`
- `session.commit`
- `llm.delta`
- `llm.final`
- RenCrow_LLM `metrics`
- `/viewer/send` 呼び出し有無

実行例:

```bash
node scripts/measure_llm_voice_e2e.mjs \
  --url 'http://127.0.0.1:18790/viewer?tab=timeline' \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --target-bytes 820000 \
  --out-json tmp/llm_voice_latency/loop_after_mac_pull.json
```

### RenCrow_LLM 直結 delta gate

`scripts/vds_e2e_probe.py`

`--max-delta-events` で live RenCrow_LLM が `llm.delta` を出しすぎていないかを機械判定する。

実行例:

```bash
python3 scripts/vds_e2e_probe.py \
  --ws-url ws://192.168.1.207:8081/v1/chat/audio/sessions \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --rounds 1 \
  --wait 90 \
  --chunk-ms 200 \
  --require-llm-final \
  --max-delta-events 1
```

期待:

- 最新 RenCrow_LLM 反映後: exit code `0`
- 未反映または delta backlog あり: exit code `4`

## 実装済み commit

### picoclaw_multiLLM

- `10949bb test: LLM音声E2E測定スクリプトを追加`
- `1a9e0b6 test: LLM音声bridge境界metricsを追加`
- `0c539cb test: RenCrow_LLM直結delta件数ゲートを追加`

### RenCrow_LLM

- `a69090b fix: LLM音声のfinal送信を優先`
- `1651d8a fix: LLM音声backendをfinal優先の非streamingにする`

## 測定で分かったこと

picoclaw bridge metrics 追加後の Viewer fake mic E2E では、picoclaw は `session.commit` を RenCrow_LLM へ即時転送していた。

代表値:

```text
picoclaw_commit_recv_to_sent_ms: 0.1 ms
picoclaw_commit_sent_to_final_recv_ms: 5875.3 ms
RenCrow_LLM commit_to_final_ms: 1431.4 ms
Viewer commit -> final: 6332 ms
/viewer/send: 0
```

Chat restart 後も、live RenCrow_LLM 直結 probe では raw `llm.delta` が多数返っていた。

代表値:

```text
delta_event_count: 120
direct commit -> final: 3361.6 ms
RenCrow_LLM commit_to_final_ms: 3304.8 ms
delta gate: FAIL (delta_event_count=120 > 1)
```

このため、現在の live Chat process は `RenCrow_LLM` の最新 commit `1651d8a` をまだ読んでいない。

## Mac 側反映手順

Linux / picoclaw 側から LLM Ops proxy で `Chat` restart はできるが、Mac 側 checkout の `git pull` はできない。

Mac で以下を実行する。

```bash
cd ~/RenCrow/RenCrow_LLM
git pull
uv run mlx-restart Chat
```

その後、Linux 側で delta gate を実行する。

```bash
python3 scripts/vds_e2e_probe.py \
  --ws-url ws://192.168.1.207:8081/v1/chat/audio/sessions \
  --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
  --rounds 1 \
  --wait 90 \
  --chunk-ms 200 \
  --require-llm-final \
  --max-delta-events 1
```

delta gate が pass したら、Viewer fake mic E2E を3回測る。

```bash
for i in 1 2 3; do
  node scripts/measure_llm_voice_e2e.mjs \
    --url 'http://127.0.0.1:18790/viewer?tab=timeline' \
    --wav tmp/stt_inputs/client_stt_input_20260609_140311.wav \
    --target-bytes 820000 \
    --out-json "tmp/llm_voice_latency/final_after_mac_pull_${i}.json"
done
```

上の2段階は次の verifier で一括実行できる。

```bash
node scripts/verify_llm_voice_latency.mjs \
  --out-dir tmp/llm_voice_latency \
  --rounds 3 \
  --max-delta-events 1
```

この verifier は direct RenCrow_LLM delta gate が失敗した場合、Viewer E2E へ進まず exit code `4` で停止する。Mac 側 `git pull` 未反映時の現 live では次のように失敗した。

```text
direct_gate.code: 4
summary.passed: false
summary.failures: ["direct_delta_gate_exit=4"]
viewer_results: []
```

## 完了判定

完了には次が必要。

- direct RenCrow_LLM delta gate が pass する
- Viewer fake mic E2E 3回で `llm.final` が表示される
- 3回の `commit -> llm.final` 中央値が 3s 未満、または RenCrow_LLM内部との差が 500ms 未満
- `/viewer/send` が成功経路で呼ばれない
- STT音声向け既存テストが通る
- picoclaw service 反映済みで `/health` が ok

現状は Mac 側 `git pull` 未実施のため未完了。

## Mac 反映後の最終測定

Mac 側で `git pull` と `uv run mlx-restart Chat` を実施後、picoclaw 側の final relay 順序も修正し、service へ反映した。

修正内容:

- RenCrow_LLM: audio session backend を final 優先の非streamingへ変更
- RenCrow_LLM: `llm.delta` は多量送信せず、`llm.final` を正本として返す
- picoclaw: gateway から受けた `llm.final` を Viewer へ先に relay し、その後で `ProcessVoiceDirect` を実行する
- 測定: `null` mark を `0` と誤解する `deriveTimings` 集計バグを修正

最終 verifier:

```bash
node scripts/verify_llm_voice_latency.mjs \
  --out-dir tmp/llm_voice_latency \
  --rounds 3 \
  --max-delta-events 1
```

結果:

```text
direct_gate.code: 0
rounds: 3
ok_count: 3
viewer_send_count: 0
commit_to_final_ms values: 1718, 1280, 1611
commit_to_final_ms median: 1611
commit_to_final_ms worst: 1718
RenCrow_LLM commit_to_final_ms median: 1594.7
viewer_gap_ms values: 95.1, 13.6, 16.3
viewer_gap_ms median: 16.3
viewer_gap_ms worst: 95.1
passed: true
```

判定:

- Viewer観測 `commit -> llm.final` は安定して 3s 未満
- RenCrow_LLM 内部との差は 500ms 未満
- `/viewer/send` は成功経路で呼ばれていない
- `llm.final` は Viewer Chat に表示されている
- direct RenCrow_LLM delta gate は pass

この時点で `78_LLM音声高速化_オーケストレーター実装プロンプト.md` の成功条件を満たした。
