# 79 LLM音声 発話区間判定仕様

**作成日**: 2026-06-15
**ステータス**: Viewer VDS 実装反映済み
**関連**: `74_Viewer音声直結LLM_Streaming仕様.md` / `74_Viewer音声直結LLM_WS契約.md` / `77_STT音声_LLM音声_命名と経路仕様.md`

---

## 1. 目的

LLM音声で、Viewer Chat のマイク入力をどこからどこまで 1 発話として扱うかを定義する。

この仕様では、マイクデバイスの ON/OFF と、LLM に送る発話区間の採用を分離する。

- マイク監視は永続 ON になり得る。
- 永続 ON であっても、すべての音を LLM音声の新規発話として採用してはいけない。
- 1 発話は VAD、無音終端、最小発話時間、最大発話時間、出力中の割り込み規則で決める。

---

## 2. 用語

| 用語 | 意味 |
| --- | --- |
| マイク監視 | Browser が `getUserMedia` で音声 stream を取得し、入力 level / VAD を見ている状態 |
| 発話受付 | 入力音を LLM音声の候補として採用できる状態 |
| 発話区間 | `session.start` から PCM chunk 送信、`session.commit` までの 1 utterance |
| end silence | 発話中に続く終端判定用の無音 |
| cooldown | 応答確定直後など、マイク監視は継続しても新規発話として採用しない時間 |
| barge-in | assistant 出力中にユーザー発話を検出した場合、出力を止めてユーザー入力を優先する動作 |

---

## 3. 基本方針

LLM音声は 1 click = 1 utterance 固定ではなく、永続 ON マイクを許容する。

ただし、永続 ON は「常に LLM へ送る」という意味ではない。Viewer はマイク stream を監視しながら、状態機械により送信可否を制御する。

```text
listening
  -> speech_detected
  -> streaming_audio
  -> end_silence
  -> committing
  -> waiting_llm
  -> assistant_output
  -> cooldown
  -> listening
```

---

## 4. 発話開始判定

Viewer は VAD により発話開始を判定する。

発話開始条件:

- 現在状態が `listening` または barge-in 許可中の `assistant_output`
- 入力 level が VAD start threshold 以上
- cooldown 中ではない
- 最小発話候補として扱える連続音声がある

発話開始時の処理:

- `session.start` を送る。
- PCM16 chunk の streaming 送信を開始する。
- Chat / IdleChat / TTS など、競合する出力があれば必要に応じて中断要求を出す。

---

## 5. 発話終端判定

発話中に end silence が **500 msec** 継続したら、1 発話の終端とみなす。

終端時の処理:

- PCM tail silence を必要量だけ送る。
- `session.commit` を送る。
- 状態を `waiting_llm` にする。
- `llm.delta` / `llm.final` を待つ。

500 msec は LLM音声の発話区間判定の基準値であり、STT音声の字幕確定条件とは独立して扱う。

---

## 6. 最小発話時間

誤検出を防ぐため、短すぎる音は発話として採用しない。

実装値は別途調整可能だが、仕様上は次を満たすこと。

- クリック音、咳、環境ノイズだけで `session.commit` しない。
- 最小発話時間未満で end silence に入った場合は、送信済み session を abort するか、LLM 応答を Chat 正本として扱わない。
- 捨てた理由を debug trace または log に残す。

---

## 7. 最大発話時間

無限録音を防ぐため、1 発話には最大長を設ける。

最大長に達した場合:

- その時点までの PCM を 1 発話として `session.commit` する。
- UI には長時間入力による commit であることを trace できるようにする。
- 以後は `waiting_llm` に遷移し、同じ発話へ追加 PCM を送らない。

---

## 8. assistant 出力中の扱い

LLM音声では、assistant 出力中でもユーザー割り込みを許可する。

つまり、assistant / TTS / Chat 応答の再生中にユーザー発話を検出した場合、入力を無視しない。

barge-in 条件:

- 入力 level が VAD start threshold 以上
- 短い環境音ではなく、最小発話時間を満たす見込みがある
- 現在の assistant 出力を中断できる

barge-in 発生時の処理:

- 現在の assistant 表示・TTS・IdleChat 出力に中断要求を出す。
- 再生中 audio は停止対象にする。
- 新しい LLM音声発話区間として `session.start` する。
- 中断理由は `vds_voice_start` または同等の reason で trace / log に残す。

---

## 9. cooldown

`llm.final` 直後、TTS 停止直後、または error / timeout 直後は cooldown に入る。

cooldown 中:

- マイク監視は継続してよい。
- 入力 level 表示は継続してよい。
- 新しい `session.start` は送らない。
- VAD 反応は debug trace に残してよいが、発話として採用しない。

cooldown の目的は、自己音声、スピーカー残響、直前応答の末尾、WebSocket close 直後の揺れを新規発話として扱わないことである。

cooldown の具体値は実測で調整する。ただし、0 msec 固定は禁止する。

2026-06-15 時点の Viewer VDS 実装値は **900 msec** とする。

---

## 10. `llm.final` 後の状態

`llm.final` は LLM音声の 1 発話の完了を意味する。

`llm.final` 後:

- 完了した WebSocket session は閉じる。
- 完了した発話の timers / chunk buffer / session id は cleanup する。
- マイク stream を必ず停止する必要はない。
- 永続 ON モードでは、状態を `cooldown` にしてから `listening` に戻す。
- one-shot モードでは、マイク stream を停止して `idle` に戻してよい。

重要: `llm.final` 直後に cooldown なしで `listening` に戻してはならない。

---

## 11. 表示と正本

LLM音声の Chat 表示正本は `llm.final` である。

- `llm.delta` は途中表示に使ってよい。
- `llm.final` 到達時に表示を確定する。
- `llm.final` は Mio の対話応答として扱う。音声内容の文字起こし、要約、確認文を生成するタスクにしてはいけない。
- Viewer から RenCrow_LLM に渡す prompt は「入力音声をユーザー発話として扱い、Mio として自然に返答する」ことを明示する。
- `音声内容を入力してください`、`音声ファイルをアップロードしてください`、`音声が提供されていない` などの no-audio/meta 応答は Chat 正本にしない。
- STT音声の `final` と LLM音声の `llm.final` を混同しない。
- Viewer の debug trace、raw log、Chat 表示、orchestrator event は別物として扱う。

---

## 12. 現行実装との差分

以前の安全策として、Viewer VDS は `llm.final` 後にマイク stream まで停止する one-shot 挙動になっていた。

これは意図しない連続 `/voice-chat` session を防ぐ暫定策であり、本仕様の最終形ではない。

本仕様に合わせる場合は、次を実装する。

- マイク監視状態と発話受付状態を分離する。
- `llm.final` 後は WebSocket session を閉じ、cooldown を経て `listening` に戻す。
- assistant 出力中は barge-in を許可し、出力を中断して新規発話を開始する。
- end silence は 500 msec とする。
- cooldown 中の VAD 反応では `session.start` しない。

2026-06-15 に Viewer VDS は本仕様へ合わせて更新済み。

実装値:

| 項目 | 値 |
| --- | --- |
| end silence | 500 msec |
| cooldown | 900 msec |
| 最小発話時間 | 250 msec |
| 最大発話時間 | 30000 msec |

---

## 13. 検証要件

最低限、次を E2E で確認する。

| 検証 | 期待 |
| --- | --- |
| 単発発話 | Browser mic -> `/voice-chat` -> `llm.final` が 1 回返る |
| end silence | 発話終了から 500 msec の無音で `session.commit` される |
| final 後 cooldown | `llm.final` 直後の残響で次 session が始まらない |
| 永続 ON 復帰 | cooldown 後に次のユーザー発話を新規 session として受け付ける |
| barge-in | assistant/TTS 出力中のユーザー発話で出力が中断され、新しい LLM音声発話が開始される |
| ノイズ棄却 | 短いクリック音や咳だけでは Chat 正本応答を作らない |

E2E 報告では、実マイクか fake mic かを明記する。
