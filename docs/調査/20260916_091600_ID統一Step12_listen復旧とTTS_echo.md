# ID統一 Step 12 再開時のlisten復旧とTTS echo実測

- 記録時刻: 2026-09-16 09:16 JST
- 対象: RenCrow_CORE Step 12 RequestID/ResponseID
- 結論: CORE `:18790` は復旧。Gateway echo 契約は実合成で成立。CORE Action/Attempt TTS receipt は未取得。GPU は Gemma4 へ戻した。

## Failure / Problem / Cause / Lesson / Invariant / Enforcement / Tests

- Failure: 再起動後 CORE が listen せず、TTS E2E が閉じられない。
- Problem: 再起動回復が Task JSONL を件数分フルスキャンしていた。加えて resilience が listen 前に再起動していた。
- Cause:
  1. `RecoverInterruptedAgentRuns` と `recoverActionRunsAfterRestart` が Get/ListRuns を1件ずつ呼び、ReadTransaction ごとに task_state/task_run を再読した。
  2. `rencrow-resilience.timer` が2分間隔で `:18790` 未listenを修復し、約6分で CORE を巻き戻した。
  3. TTS Gateway は Irodori warmup 必須。実合成では `request_id` が CORE の `req_*` と一致し、`target_request_id` は `tts_*`。
  4. CORE `/entry` の TTS は `tts runtime is not configured`。browser-only で local player が無いため `configured()` が false。
- Lesson: 再起動回復は canonical Task/Run を一括 List する。TTS Gate は Gateway echo と CORE receipt が別物。browser-only では entry player を必須にしない。
- Invariant: RequestID は CORE mint。provider `tts_*` は target/ExternalRef。
- Enforcement: 回復の List 回数テスト。Gateway echo_match。resilience は listen 後に戻す。
- Tests: `TestRecoverInterruptedAgentRunsQueuesOnlyDurableCheckpoint` の List 回数。Gateway 実合成 HTTP 200 echo_match true。

## 配備証拠

- CORE source `92ca8de` を `~/.local/bin/rencrow` へ配備。startup_total 約192s、`/health/ready` true。
- SuperAgent interrupted run recovery: queued=0 blocked=0（一括 List 後は即完了）。
- Gateway POST `/api/tts` + `X-RenCrow-TTS-Request-Id`: echo_match true、target_is_tts true、audio_set true。
- Gemma4 Scheduled Task: Running / models ready。Irodori: Disabled。
