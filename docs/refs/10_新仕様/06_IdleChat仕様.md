# IdleChat 仕様

## 目的

IdleChat は、ユーザー操作がない時間にエージェント同士が自律的に会話する機能である。

Viewer と TTS を通じてリアルタイム表示・読み上げを行うが、raw response、view data、audio trigger は別契約として扱う。

## LLM alias

IdleChat では speaker ごとに LLM provider を分ける。

| speaker | 役割 | LLM alias |
| --- | --- | --- |
| Mio | 通常の会話側 participant | `Chat` |
| Shiro | Worker persona の会話側 participant | `ChatWorker` |
| Kuro | heavy participant | `Heavy` |
| Wild | wild participant | `Wild` |

`ChatWorker` は Worker サーバ上の alias であり、backing model と prompt は通常の `Worker` と同じである。違いは出力長 cap だけで、IdleChat の Shiro 発話は `ChatWorker=1024` を使う。通常の Shiro / Worker Core / Coder 検証は `Worker=4096` を使う。

IdleChat は request payload の `metadata` で用途判定しない。用途の切り分けは alias で行う。system prompt 内の `/no_think` や `この会話はidleChatです` は thinking / 表示本文制御のための指示であり、max_tokens cap の routing 根拠にしない。

LLM 呼び出しの queue wait と generation timeout は分けて扱う。特に Mio は `Chat` alias の短い体感応答が必要なため、本文生成で timeout が出た場合は `queue` 待ちと `generate` 実行時間を混同しない。詳細は `45_LLMクライアントキューTimeout仕様.md` を参照する。

## モード

| モード | 目的 | 主担当 |
| --- | --- | --- |
| normal idle | 通常の自律雑談 | `internal/application/idlechat/orchestrator*.go` |
| forecast | 未来展望・トピック探索 | `internal/application/idlechat/forecast_*.go` |
| story | 物語・昔話の読み上げ | `internal/application/idlechat/story_mode*.go` |

Viewer は IdleChat の Live Timeline、Summary Review、History を表示する。これらは raw response そのものではなく、表示用 state と診断情報の投影である。

## raw / view / audio 境界

| 種別 | 役割 |
| --- | --- |
| raw response | LLM が返した素の応答。診断・監査用。 |
| view data | Viewer 表示用に整形された本文。会話注入に使うのは view data。 |
| audio trigger | TTS と口パクを動かすための trigger。本文表示の唯一の根拠にしない。 |

fallback は成功扱いしない。空応答、invalid response、generation error は失敗または回復経路として扱い、Viewer / log に隠さない。

## story validator

story-simple は、LLM raw response を title / body に分離したあと `validateSimpleStoryDraft` を通す。

validator は次を確認する。

- 本文が短すぎない。
- 主人公改変が title または body に現れている。
- `もし〜だったら` / `もし〜なら` の仮説フレームだけで終わっていない。
- 解説、条件、メタ発言などの prompt 漏れを本文として扱わない。

validator を通らない story は本文配信を続けず、`invalid_story:<reason>` として Summary / History に残す。これは fallback 成功ではない。

## お題読み上げ

IdleChat のお題読み上げは、カテゴリごとに置換と生成を分ける。
この節の変換結果は読み上げ専用であり、Viewer の topic 表示、timeline、history、summary へ描画してはいけない。
カテゴリ分岐、`今日のお題。` の前置、Viewer 描画禁止、TTS 専用化は実装コードで決定的に実装し、LLM prompt の指示で制御しない。

- Single / Double / External / Movie / News / Forecast は、取得済み topic に `今日のお題。` を前置するだけの置換処理とする。
- 上記 6 カテゴリでは topic 本文を再生成、要約、言い換えしない。カテゴリ名、内部 strategy、seed、provider 名も読み上げ本文へ入れない。
- Story だけは、内部 topic（例: `物語: 金太郎 × 探偵`）を短いキャッチーなタイトルへ生成変換してよい。
- Story 生成タイトルは元話と改変軸を保持し、`物語:`、カテゴリ名、解説、あらすじを出さない。
- Story タイトル生成プロンプトは `prompts/idle_chat/story_topic_title.md` を正とする。ただし prompt は Story タイトル候補の生成だけを担当する。
- 最終読み上げ文字列は `今日のお題。<topic_or_story_title>` の 1 発話単位とし、TTS `speech_text` としてのみ扱う。Viewer の描画正本は変換前の topic / display event とする。

## STT との境界

STT input は通常 chat に流す。IdleChat に直接流さない。

ユーザー入力が来た場合、IdleChat は中断または状態更新の対象になるが、音声入力そのものを IdleChat 会話として扱わない。

## 主な実装箇所

| 領域 | 主担当 |
| --- | --- |
| orchestrator lifecycle | `internal/application/idlechat/orchestrator.go`, `orchestrator_constructor.go`, `orchestrator_modes.go` |
| response generation | `internal/application/idlechat/orchestrator_response_*.go` |
| sanitize / invalid response | `internal/application/idlechat/orchestrator_sanitize_*.go` |
| loop detection | `internal/application/idlechat/orchestrator_loop_*.go` |
| topic generation | `internal/application/idlechat/topic_generator_*.go`, `orchestrator_topics.go` |
| forecast | `internal/application/idlechat/forecast_*.go` |
| story | `internal/application/idlechat/story_mode*.go` |
| quality review | `internal/application/idlechat/quality_review.go` |
| Viewer handlers | `cmd/rencrow/runtime_idlechat_handlers.go`, `internal/adapter/viewer/*idlechat*` |
| TTS bridge | `cmd/rencrow/idlechat_tts*.go`, `internal/infrastructure/tts/rencrow_tts_*.go` |

## raw response 診断

IdleChat では、編集後の view data と LLM の素の raw response を分ける。

- raw response は空応答、invalid response、generation error、provider 出力異常の診断に使う。
- view data は Viewer 表示と会話注入に使う。
- Summary Review / History は、表示状態、境界、終了状態を追うための観測面である。
- fallback は成功扱いにしない。fallback に落ちた場合は失敗経路として記録する。

## event 契約

IdleChat は Viewer / TTS / log に event を出す。

- Viewer 向け event は表示用 state を更新する。
- TTS event は音声再生と口パクを起動する。
- log は診断・追跡のために残す。

`idlechat.viewer` を TTS 用に使わない。`idlechat.tts` を Viewer 表示本文として扱わない。

## TTS 待ち合わせ

IdleChat は TTS の完了を待って会話テンポと口パクを揃える。ただし、TTS 未完了で IdleChat 全体を永久停止させてはいけない。

発話単位の TTS 待ち上限は 60 秒を基準とする。5 秒固定で打ち切る通常仕様は採用しない。60 秒以内に音声が用意できた場合は、スピーカ ON では audio playback 完了に同期して次発話へ進む。

60 秒を超えた発話は、音声系 timeout として `tts_error=true` / `tts_error_kind=timeout` を記録する。表示本文は `display_only` として区切りのよいところまで描画してよいが、音声・口パク・TTS provider 成功として扱わない。

session 終了時の drain は、未完了音声を無制限に待つ処理ではない。drain の UI 待ち上限は 60 秒を基準とし、残った音声は `session_audio_timeout` として閉じる。

timeout 後に遅れて届いた音声は、session_id / utterance_id / chunk_index が現在の発話と一致しない限り再生しない。古い音声を次 session に持ち越してはいけない。

## 検証

確認対象:

- start / stop / status が動く。
- normal / forecast / story の開始条件が維持される。
- raw response と view data が分離される。
- audio trigger が本文表示と混ざらない。
- fallback が成功扱いされない。
- invalid response / generation error が隠れない。
- story validator を通らない story が `invalid_story:<reason>` として残る。
- TTS 完了待ち、60 秒 timeout、display-only fallback、drain、break が成立する。

主な確認:

```bash
GOCACHE=/tmp/rencrow-gocache go test ./internal/application/idlechat
GOCACHE=/tmp/rencrow-gocache go test ./cmd/rencrow
GOCACHE=/tmp/rencrow-gocache go test -count=1 -tags=e2e ./test/e2e
```
