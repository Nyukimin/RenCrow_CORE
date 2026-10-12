# Agent名表示: agent.thinking の from 固定の修正と本番配備

## 概要

`rencrowctl chat --to <agent>` などで、応答したAgentの名前が宛先に関係なく `mio>` と表示された。
原因は、COREが `agent.thinking` の `from` を `"mio"` に固定していたこと。修正して本番へ配備し、
宛先ごとにE2Eで確認した。

## 発生条件

- CHATの直接宛先が shiro / midori / kuro のとき（`agent.start` と `agent.response` は宛先のAgent、
  `agent.thinking` だけ mio になり、同じturnで食い違っていた）。
- 委譲route（WILD / ANALYZE）でも、トークンを生成するのは midori / kuro だが `agent.thinking` は mio だった。

## 事実

- `message_orchestrator_tts_lifecycle.go` と `distributed_orchestrator_tts_lifecycle.go` の
  `WithStreamHooks` が `l.emit("agent.thinking", "mio", ...)` と固定していた。
  `messageStreamHook` は speaker を受け取らなかった。初期投入から存在し、テストは `from` を検証していなかった。
- CMDは最初の `agent.thinking` の `from` をstream表示のラベルにしていた。
- Debug Viewerの `thinkingDisplayAgentID` は、`agent.start` の宛先が利用者（`user`）のとき
  `user` を返し、直接CHATのthinkingバブルが利用者名義になっていた（mioのthinkingは非表示）。
- PORTALは送信時のrecipientで表示しており、`agent.thinking.from` に依存しない。progress.js / timeline.js /
  monitor_helpers.go / monitor_reducers.go は `from` を汎用に扱い、mio固定は無い。

## 結論と修正

不変条件: 同じturnの `agent.thinking` / `agent.start` / `agent.response` の `from` は、実際に応答した
Agentと一致する（正本: `docs/06_Public_API仕様.md` のstreaming節）。

- CORE: `speaker` を stream hook に追加。CHATは recipient、PLAN / RESEARCH は mio、WILD は midori、
  ANALYZE は kuro を渡す。ANALYZE の後続の `agent.response`（`from=mio`, `to=user`）は利用者向けの中継で、
  delta の生成者ではない。
- Debug Viewer: 利用者宛の `agent.start` では送信元（応答Agent）を表示する。mio自身のバブルは従来どおり非表示。
- CMD: 同じtraceの `agent.start` が示す応答Agentを優先してラベルにし、deltaを確定する `agent.response` の
  `from` が表示済みラベルと違う場合は `応答Agent> <名前>` を表示する（CMD側のcommitは RenCrow_CMD を参照）。

## Failure Knowledge

- Failure / Problem: 応答Agentと表示名の不一致。
- Cause: 同じ概念（応答Agent）を route ごとの `agent.start` / `agent.response` と、共通の stream hook の
  固定値で別々に決めていた（Semantic Duplication）。
- Lesson: stream hook の生成者は呼び出し側 route が決め、共通部分に既定のAgent名を持たせない。
- Invariant: 上記の `from` 一致。
- Enforcement / Tests: `agent_label_invariant_test.go`（message / distributed の両dispatcherで CHAT 4宛先 +
  PLAN / RESEARCH / WILD / ANALYZE、両 lifecycle の speaker 透過）、`viewer_memory_panel.test.mjs`
  （thinkingバブル名）、CMDの `cli_chat_test.go`（`from` が食い違うイベント列）。

## 本番配備（要点のみ）

- 配備済みCOREバイナリは `main` の任意のcommitと一致しない構成だった（基準 `e2dd0b16` に `ca1d7685` と
  `c43b45ed` をcherry-pick。バイナリに NativeHarness 系のシンボルが無いことで確認）。`main` の先頭までの
  未配備変更（native delegation、ops admission、SSE `?from=now` など）を持ち込まないため、同じ基準に
  本修正 `97e5a3b6` だけをcherry-pickしてビルドした。CMDは配備済みの `2a5389d` に本修正だけを載せた。
- version文字列は `...+agentlabel-97e5a3b6`、Commit は `e2dd0b16+ca1d7685+c43b45ed+97e5a3b6`。
- 旧バイナリは `~/.local/bin/rencrow.bak-20261012-agent-label` と `rencrowctl.bak-20261012-agent-label`。
  ロールバックは bak を元の名前へ戻して `systemctl --user restart rencrow.service`（CLIは置換のみ）。
- 再起動からhealth 200まで247秒。
- E2E（宛先 midori / shiro / mio）: CLIのラベルが宛先と一致し、ジャーナルの `agent.start` /
  `agent.thinking` / `agent.response` の `from` も一致。`GET /viewer/status` のtask activityのownerも
  宛先と一致。
- 観測: 再起動直後の最初のmidori宛chatは応答生成後に `conversation turn conflict` で失敗し、再試行で成功した。
  TaskStoreのtransaction遅延（hot_bytes約100MB）と実行枠不足が同時に出ており、本件の変更とは別の既知領域。
  原因は未調査。

## 関連ファイル

- `internal/application/orchestrator/message_orchestrator_routes.go`、`distributed_orchestrator_routes.go`、
  `*_tts_lifecycle.go`、`distributed_orchestrator_execution.go`
- `internal/application/orchestrator/agent_label_invariant_test.go`
- `internal/adapter/viewer/assets/js/viewer.js`、`viewer_memory_panel.test.mjs`
- `docs/06_Public_API仕様.md`
