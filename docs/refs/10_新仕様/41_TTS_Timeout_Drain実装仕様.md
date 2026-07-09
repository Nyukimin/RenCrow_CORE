# TTS Timeout / Drain 実装仕様

## 1. 位置づけ

この文書は、`docs/10_新仕様/07_STT_TTS仕様.md` と `docs/10_新仕様/06_IdleChat仕様.md` に定義した TTS 待ち合わせ仕様を、RenCrow 実装へ落とすための作業用仕様である。

07 は STT / TTS の振る舞い仕様、06 は IdleChat の機能仕様として扱う。この 41 は、実装対象、変更ファイル、状態管理、TDD、確認方法、停止条件を扱う。

## 2. 実装対象と非対象

### 2.1 実装対象

- IdleChat の発話単位 TTS 待ち timeout を 5 秒から 60 秒へ変更する。
- IdleChat の session drain timeout を 45 秒から 60 秒へ変更する。
- timeout 時に `tts_error=true` だけでなく `tts_error_kind=timeout` を記録できるようにする。
- session drain timeout 時に `session_audio_timeout` を記録できるようにする。
- timeout 後に遅れて届いた古い TTS audio を、現在 session / utterance / chunk の音声として再生しない。
- timeout 時も Viewer 表示本文は `display_only` として区切りよく描画できるようにする。
- TTS timeout / drain timeout / display-only fallback の local test を追加または更新する。

### 2.2 非対象

- TTS provider 自体の高速化。
- Irodori / SBV2 / RenCrow_TTS server の推論実装変更。
- TTS HTTP `/synthesis` の低レベル timeout 30 秒、WS chunk 待ち 60 秒、WS session 完了待ち 20 秒の契約変更。
- STT の streaming / final wait timeout 変更。
- Viewer UI の大規模再設計。
- IdleChat の story / forecast 生成品質改善。
- 実音声デバイス、OS audio 設定、外部 TTS server の運用設定変更。

## 3. 正本仕様

実装判断の正本は以下とする。

- `docs/10_新仕様/07_STT_TTS仕様.md`
- `docs/10_新仕様/06_IdleChat仕様.md`
- `docs/STT_TTS/AUDIO_Client仕様/TTS/ChatAudioSync仕様.md`
- `docs/STT_TTS/AUDIO_Client仕様/TTS/実装仕様.md`

判断の優先順位:

1. `07_STT_TTS仕様.md` の TTS 待ち timeout / session drain
2. `06_IdleChat仕様.md` の IdleChat TTS 待ち合わせ
3. `ChatAudioSync仕様.md` の chunk / session 同期
4. `TTS/実装仕様.md` の provider / transport timeout

## 4. 現行実装との差分

| 項目 | 現行 | 本仕様 |
| --- | --- | --- |
| 発話単位 TTS 待ち | `idleChatTTSWaitTimeout = 5s` | `60s` |
| session drain | `idleChatTTSSessionDrainTimeout = 45s` | `60s` |
| 発話 timeout 記録 | log に `tts_error=true` | `tts_error=true` + `tts_error_kind=timeout` |
| drain timeout 記録 | log に `tts_error=true` | `session_audio_timeout` |
| timeout 後の表示 | 実装依存 | `display_only` として本文表示は完了可 |
| timeout 後の遅延音声 | session / utterance 境界の明示が必要 | 古い音声は再生禁止 |

## 5. 変更ファイルと責務

| ファイル | 責務 |
| --- | --- |
| `internal/application/idlechat/orchestrator.go` | IdleChat timeout 定数。発話単位 / drain の既定値を 60 秒へ変更 |
| `internal/application/idlechat/orchestrator_monitor.go` | `waitForTTSDone` / `waitForTTSSessionDrain` の timeout log / 状態記録 |
| `internal/application/idlechat/orchestrator_tts_wait_test.go` | 発話 timeout / drain timeout / 60 秒既定値の contract test |
| `internal/application/idlechat/orchestrator_summary.go` | summary 発話で timeout が display-only / error 記録になることを確認 |
| `internal/application/idlechat/forecast_session_runner.go` | forecast session の TTS wait / drain が同じ契約に従うことを確認 |
| `internal/application/idlechat/story_mode_simple.go` | story 発話の TTS wait が同じ契約に従うことを確認 |
| `cmd/picoclaw/idlechat_tts*.go` | TTS done channel / session_id / utterance_id / chunk_index の追跡境界確認 |
| `cmd/picoclaw/idlechat_tts_test.go` | timeout 後の古い audio / done signal の扱いを必要に応じて補強 |
| `internal/adapter/viewer/assets/js/viewer.js` | 古い TTS audio event を現在 session / utterance / chunk へ混入させない境界確認 |
| `internal/adapter/viewer/viewer_memory_panel.test.mjs` または関連 Viewer test | display-only / stale audio rejection の contract test |

実装時により近い test file が存在する場合は、既存構造を優先してよい。ただし、TTS timeout 契約の test を story / forecast / summary のどれか 1 系統だけに閉じない。

## 6. 状態とイベント契約

### 6.1 発話単位 timeout

発話単位で 60 秒以内に TTS done channel が close しない場合、該当発話を音声 timeout として扱う。

必須記録:

- `tts_error=true`
- `tts_error_kind=timeout`
- `session_id`
- 可能なら `utterance_id`
- 可能なら `chunk_index`

表示本文が存在する場合は `display_only` として Viewer へ描画してよい。これは「音声なし表示」であり、TTS 成功ではない。

### 6.2 session drain timeout

session 終了時に未完了 TTS がある場合、drain は 60 秒だけ待つ。60 秒を超えて残る音声は session 境界で閉じる。

必須記録:

- `session_audio_timeout`
- `session_id`
- `remaining_index`
- `remaining_count`

drain timeout 後は次 session へ進んでよい。ただし、前 session の audio chunk を次 session の音声として再生してはいけない。

### 6.3 stale audio rejection

timeout 後に遅れて届いた audio chunk は、現在の session / utterance / chunk と一致しない限り再生しない。

判定キー:

- `session_id`
- `utterance_id`
- `chunk_index`
- `response_id` がある場合は補助キーとして使う

一致しない audio event は debug log に stale / ignored として残す。本文表示の根拠や lipSync trigger に使わない。

### 6.4 speaker OFF

スピーカ OFF 時は従来どおり音声再生を行わず、chunk 間 500ms の表示テンポ fallback を使う。

この 500ms は TTS 生成 timeout でも drain timeout でもない。`tts_error_kind=timeout` と混同しない。

## 7. TDD 実装手順

### 7.1 Red

先に以下の失敗 test を追加または更新する。

1. 発話単位 timeout の既定値が 60 秒である。
2. `waitForTTSDone` が timeout 時に `tts_error_kind=timeout` を記録する。
3. session drain timeout の既定値が 60 秒である。
4. `waitForTTSSessionDrain` が timeout 時に `session_audio_timeout` を記録する。
5. timeout 後の stale audio が次 session で再生対象にならない。
6. timeout 時も display-only 表示が本文の session / utterance 境界を壊さない。

### 7.2 Green

最小変更で次を実装する。

- timeout 定数を 60 秒へ変更する。
- timeout log / event に `tts_error_kind=timeout` と `session_audio_timeout` を追加する。
- done channel / audio event の session 境界判定を補強する。
- Viewer 側で現在 session と一致しない TTS event を再生しない。

### 7.3 Refactor

重複した timeout 記録処理が出た場合のみ、既存の IdleChat event / log helper に寄せる。

新しい cache / pending queue / 独自 ID は原則追加しない。既存の `session_id`、`utterance_id`、`chunk_index`、`response_id` で表現できるかを先に確認する。

## 8. 受け入れ基準

### 8.1 local test

最低限、以下を通す。

```bash
GOCACHE=/tmp/picoclaw-gocache go test ./internal/application/idlechat
GOCACHE=/tmp/picoclaw-gocache go test ./cmd/picoclaw
node internal/adapter/viewer/viewer_memory_panel.test.mjs
```

Viewer test file は既存構造に合わせて変更してよい。Node test が複数に分かれている場合は、TTS event / audio sync に最も近い test を実行する。

### 8.2 live / browser 確認

UI / Viewer の動作を変える場合は、最低 1 セッションを browser で追う。

確認内容:

- TTS が 60 秒以内に完了した発話は、スピーカ ON で audio playback 完了後に次へ進む。
- TTS が 60 秒を超えた発話は、音声エラーとして記録される。
- timeout した発話の本文は display-only として区切りよく表示される。
- drain timeout 後、次 session へ進める。
- 遅れて届いた古い音声が次 session で再生されない。
- 音声失敗を TTS / lipSync 成功として表示しない。

外部 TTS server を意図的に遅延させられない場合は、test double または route fulfill で TTS event 遅延 / 欠番を再現し、実 TTS 成功 E2E とは分けて報告する。

## 9. ログ / Viewer 表示

timeout は silent にしない。

発話 timeout log 例:

```text
[IdleChat] TTS completion wait timed out after 60s; continuing conversation (tts_error=true tts_error_kind=timeout session=<session_id> utterance=<utterance_id>)
```

drain timeout log 例:

```text
[IdleChat] TTS session drain timed out after 60s; continuing next session (session=<session_id> remaining_index=<n>/<total> session_audio_timeout=true)
```

Viewer では、通常本文と音声状態を混同しない。表示できるなら `display_only` / `TTS audio timeout` / `session audio timeout` のいずれかを debug / Ops / IdleChat history に残す。

## 10. 停止条件

以下を検出したら、実装を広げず停止して報告する。

- TTS event に session_id / utterance_id / chunk_index がなく、古い音声の識別ができない。
- display-only 表示のために本文表示ロジックを大規模に作り替える必要がある。
- TTS timeout 対応が STT / normal chat / Worker / Coder の責務へ波及する。
- Viewer が音声再生成功と display-only を同じ state でしか表現できない。
- 外部 TTS server の仕様変更が必要になる。
- 既存テストでは timeout / stale audio を再現できず、検証方法の追加設計が必要になる。

## 11. 実装後に更新する文書

実装完了後、確認結果に応じて以下を更新する。

- `docs/10_新仕様/13_実装項目インベントリ.md`
- `docs/10_新仕様/17_E2E残課題.md`
- `docs/10_新仕様/32_E2E_runtime確認チェックリスト.md`
- `docs/10_新仕様/06_IdleChat仕様.md`
- `docs/10_新仕様/07_STT_TTS仕様.md`

更新時は、local test、browser 確認、実 TTS E2E を混同しない。
