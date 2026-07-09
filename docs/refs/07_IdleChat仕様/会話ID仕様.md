# RenCrow 会話ID仕様

**作成日**: 2026-05-26
**対象**: IdleChat / Chat / Viewer / TTS / STT
**ステータス**: 運用仕様

---

## 1. 目的

RenCrow の会話系処理では、IdleChat、通常 Chat、TTS、STT、Viewer が同時に動く。
そのため、会話セッション、発言、発話、音声チャンク、Viewer 操作対象を混同すると、次の問題が起きる。

- Mio / Shiro の会話順が入れ替わる
- 同じ発言が複数回表示される
- 古い IdleChat / TTS / STT の応答が現在の会話へ混入する
- TTS chunk を本文表示の根拠として扱ってしまう
- 複数 Viewer を開いたときにスピーカ、マイクの操作対象が不明になる

この仕様は、RenCrow の会話系 ID の単位と責務を定義し、表示、音声、入力、ログの整合性を保つための基準とする。

---

## 2. 基本単位

会話系処理では、次の単位を混同しない。

| 単位 | 意味 | 主な ID |
|---|---|---|
| セッション | ひとまとまりの会話、話題、処理単位 | `session_id` |
| メッセージ | 1つの発言本文 | `message_id`, `turn_index` |
| 発話 | TTS で読み上げる単位 | `utterance_id` |
| チャンク | stream や TTS 音声の分割単位 | `chunk_index`, `seq` |
| Viewer | 表示、音声、マイク操作を行うブラウザ単位 | `viewer_client_id` |
| イベント | 状態変化、ログ、配送単位 | `event_id`, `seq` |

基本関係は以下とする。

```text
session_id
  -> message_id + turn_index
    -> utterance_id
      -> chunk_index
```

`timestamp` は補助情報であり、表示順や重複判定の主キーにしてはいけない。

---

## 3. 共通ID

| ID | 用途 | 備考 |
|---|---|---|
| `session_id` | 会話、話題、処理セッションの識別 | IdleChat では原則 1 話題につき 1 つ |
| `message_id` | 1 発言の識別 | 重複表示、古い応答混入の防止に使う |
| `turn_index` | セッション内の発言順 | Mio / Shiro の順序保証に使う |
| `job_id` | 非同期処理、実行単位の追跡 | Worker 系や background job で使用する |
| `event_id` | ログ、イベント単位の追跡 | 重要処理に付与する |
| `seq` | event stream 上の配送順 | SSE / WebSocket 等の配送順確認用 |
| `timestamp` | 発生時刻 | 主キーではなく補助情報として扱う |

---

## 4. IdleChat のID

| ID | 用途 |
|---|---|
| `session_id` | 1 つの IdleChat 話題セッション |
| `active_session_id` | Viewer が現在表示すべき IdleChat セッション |
| `message_id` | IdleChat 内の 1 発言 |
| `turn_index` | IdleChat 内の発言順 |
| `active_transcript[]` | 現在セッションの表示用発言列 |
| `generation_id` | 現在の LLM 生成処理の識別 |
| `loop_reason` | IdleChat loop の検出理由 |

IdleChat の通常モードでは、1 つの話題を 1 つの `session_id` に対応させる。
1 話題が完了したら、その `session_id` は終了する。
次の話題は新しい `session_id` で開始する。

```text
話題A session 開始
  -> message_id / turn_index を付けて発言を記録
  -> summary
  -> break
話題A session 完了

話題B session 開始
  -> 新しい session_id を採番
```

同一 `session_id` 内で複数の話題を進めてはいけない。

---

## 5. turn_index 採番ルール

IdleChat の `turn_index` は、同一 `session_id` 内で表示対象となる発言順を表す単調増加の整数とする。

- 原則として 1 から開始する。
- 同一 `session_id` 内で同じ `turn_index` を再利用してはいけない。
- Mio / Shiro の通常発言、summary、generation error など、Viewer に会話行として表示するものは `message_id` と `turn_index` を持つ。
- topic 見出しなど会話行ではない表示要素は、会話 `turn_index` に混ぜない。必要な場合は別の表示種別として扱う。

`turn_index` は表示順の正本であり、TTS 到着順、SSE 到着順、`timestamp` より優先する。

---

## 6. message_id 安定性

同じ発言を表す hydrate、SSE event、TTS payload、fallback reveal は、同じ `message_id` を使わなければならない。

- 同一 `message_id` の再受信は、新規 DOM 追加ではなく既存 DOM の更新または無視として扱う。
- 同一 `message_id` で本文、話者、`turn_index` が矛盾する場合はデータ不整合としてログに残し、古いまたは不正な event を表示へ反映しない。
- `message_id` は表示重複排除の正本であり、配送 event の `event_id` や TTS の `utterance_id` で代用してはいけない。

---

## 7. Chat のID

| ID | 用途 |
|---|---|
| `session_id` / `chat_id` | 通常 Chat 会話の識別 |
| `message_id` | ユーザー発言またはアシスタント応答の識別 |
| `job_id` | LLM request や Worker 処理の追跡 |
| `event_id` | ログ、状態更新イベントの追跡 |

通常 Chat では、ユーザー入力、LLM 応答、Worker 結果を同じ単位に混ぜない。
Viewer 表示、TTS、口パク、会話履歴には最終本文のみを使う。
reasoning、raw content、prompt 注入用データは通常 UI や次ターン prompt に混ぜない。

---

## 8. TTS のID

| ID | 用途 |
|---|---|
| `session_id` | TTS セッション識別 |
| internal TTS session ID | サーバ内部の TTS 処理単位 |
| public TTS session ID | Viewer へ公開する TTS 処理単位 |
| `response_id` | TTS 対象応答の識別 |
| `utterance_id` | 1 発話単位の識別 |
| `chunk_index` | 音声 chunk の順序 |
| `message_id` | 元になった会話メッセージ |
| `turn_index` | 元メッセージの会話順 |
| `character_id` | Mio / Shiro 等の話者識別 |
| `viewer_client_id` | 音声操作対象 Viewer の識別 |

TTS は音声再生、口パク、発話同期のために使う。
TTS chunk は本文表示の唯一の根拠にしてはいけない。
本文表示順は `message_id` と `turn_index` を優先する。

TTS が失敗、未応答、timeout になった場合でも、それだけを理由に会話セッション全体を停止しない。
ただし TTS 失敗はエラーとして扱い、成功や fallback として隠してはいけない。

---

## 9. STT のID

| ID | 用途 |
|---|---|
| `session_id` | STT 処理セッション |
| `event_id` | STT 開始、停止、結果イベント |
| `capture_session_id` | 録音キャプチャ単位 |
| `capture_event_id` | 録音イベント単位 |
| `viewer_client_id` | マイク操作対象 Viewer |
| WebSocket connection ID 相当 | 接続単位の識別 |

STT ボタン押下、Chat 入力開始、paste、composition / IME 入力開始など、通常 Chat へのユーザー介入が始まった場合は、IdleChat 停止を最優先で行う。
古い STT 応答が後から返っても、現在の Viewer 表示や会話状態へ反映してはいけない。

---

## 10. Viewer のID

| ID | 用途 |
|---|---|
| `viewer_client_id` | ブラウザ 1 つを識別 |
| `active_audio_viewer_id` | スピーカ操作対象の Viewer |
| `active_input_viewer_id` | マイク、入力操作対象の Viewer |
| `seenEventKeys` / event key | 重複イベント抑止 |
| DOM `data-message-id` | 表示済みメッセージの識別 |
| DOM `data-turn-index` | 表示順ソート用 |

Viewer は複数同時に開かれる可能性がある。
その場合でも、スピーカとマイクは常に単一ブラウザを操作対象とする。
音声操作対象は後出し優先とし、古い Viewer や古い session の音声イベントは破棄する。

---

## 11. Viewer 再同期の正本

Viewer の初期表示、再接続、表示崩れ回復では、現在の `active_session_id` に対応する `active_transcript[]` を表示正本として扱う。

- SSE event は差分更新として扱う。
- TTS event は音声再生、口パク、同期のために使う。
- hydrate、SSE、TTS、fallback reveal が競合した場合でも、同一 `message_id` の DOM は 1 つだけにする。
- DOM の最終表示順は `turn_index` 昇順で再整列する。

`active_transcript[]` と後続 event が矛盾する場合は、自然な会話として混ぜず、診断ログに残す。

---

## 12. 表示順序と重複排除

IdleChat / Chat の表示順は、原則として次の順序で判定する。

1. `active_session_id` が現在セッションと一致する
2. `message_id` が未表示、または同一メッセージの更新である
3. `turn_index` の昇順で並べる
4. 同一 `message_id` の DOM を複数作らない
5. 古い session、古い message、古い TTS chunk は表示へ反映しない

表示における主キーは `message_id` とする。
表示順の主キーは `turn_index` とする。
`timestamp`、TTS chunk 到着順、SSE 到着順だけで表示順を決めてはいけない。

---

## 13. 欠番・重複・逆順の扱い

Viewer または Orchestrator は、同一 `session_id` 内で次の状態を検出した場合、会話 ID 不整合として扱う。

- 同じ `message_id` が複数の `turn_index` を持つ
- 同じ `turn_index` に複数の異なる会話行が割り当てられる
- `turn_index` に欠番がある状態で後続発言だけが表示される
- 現在の `active_session_id` と異なる session の event が届く
- `message_id`、話者、本文、`turn_index` の組み合わせが hydrate / SSE / TTS 間で矛盾する

この場合、fallback 成功として扱わず、診断ログに記録し、可能であれば `active_transcript[]` から再同期する。
再同期できない場合は、古い event または不整合 event を破棄し、Viewer に自然な会話として混入させない。

---

## 14. 複数Viewer時の遅延応答破棄

`active_audio_viewer_id` または `active_input_viewer_id` が切り替わった後、旧 Viewer から届いた TTS ack、STT result、音声 chunk、入力 result は、現在の会話進行へ反映してはいけない。

旧 Viewer 由来の event は診断ログには残してよいが、表示、TTS 待ち解除、STT 送信、IdleChat 停止解除の根拠にしない。

---

## 15. raw / view / audio / prompt の境界

| 種別 | 役割 | 混入禁止先 |
|---|---|---|
| raw response | LLM が返した素の応答。診断、監査用 | Viewer 本文、TTS、次ターン prompt |
| view data | Viewer 表示用に整形された本文 | raw 診断欄以外の raw 保存領域 |
| audio trigger | TTS と口パクを動かすための trigger | 本文表示の唯一根拠 |
| prompt injection data | 次ターン prompt に注入する文脈 | Viewer 本文、TTS |

表示、音声、口パク、ログ、会話履歴、prompt 注入データは混同しない。
fallback は成功扱いしない。
空 content は生成エラーとして扱う。

---

## 16. エラー時の扱い

次の場合は成功ではなくエラーとして扱う。

- `content` が空
- LLM 応答が内部推論 leak と判定される
- generation error が発生した
- TTS request / response が失敗または timeout した
- STT request / response が失敗または timeout した
- 古い session の応答が現在 session に混入しそうになった

エラー時は、会話履歴、Viewer ログ、または診断ログに「生成エラー」等のエラー状態を明示する。
fallback 文で自然な会話として続けたように見せてはいけない。

TTS が返ってこないことはエラーであるが、会話システム全体の停止要因ではない。
TTS 失敗時も、表示・会話状態・次処理の継続可否を TTS 成否だけに依存させない。

---

## 17. 禁止事項

- `timestamp` だけで表示順を決める
- TTS chunk だけを本文表示の根拠にする
- 同じ `message_id` の DOM を複数作る
- `session_id` が違う古い応答を現在の会話に混ぜる
- 1 つの IdleChat `session_id` 内で複数話題を扱う
- エラーや空 content を fallback 成功として扱う
- reasoning、raw content、prompt 注入用データを Viewer 本文に混ぜる
- 新しい ID を乱立させ、既存の `session_id` / `message_id` / `turn_index` で表現できる単位を重複定義する

---

## 18. 検証観点

実装や修正時は、最低限次を確認する。

- IdleChat で 1 話題につき 1 つの `session_id` が使われる
- 各発言に `message_id` と `turn_index` が付く
- `turn_index` が同一 `session_id` 内で単調増加し、重複しない
- Mio / Shiro の会話順が `turn_index` 昇順で守られる
- hydrate、SSE event、TTS event、fallback reveal が同じ `message_id` を重複表示しない
- TTS payload に元メッセージの `message_id` と `turn_index` が含まれる
- 古い session の TTS chunk が現在表示へ混入しない
- Viewer 再接続時に `active_transcript[]` から表示を復元できる
- 欠番、重複、逆順、session 不一致が fallback 成功として扱われない
- Viewer を複数開いても、音声、マイク操作対象が単一 Viewer として扱われる
- 旧 Viewer から届いた TTS ack / STT result が会話進行へ反映されない
- 空 content、generation error、TTS timeout が fallback 成功として扱われない

---

## 19. 期待される状態

IdleChat では、1 話題ごとに 1 つの `session_id` があり、各発言には `message_id` と `turn_index` が付く。
TTS はその発言に紐づく `utterance_id` と `chunk_index` を持つ。
Viewer は `active_session_id`、`message_id`、`turn_index` を使って、重複、順序逆転、古い応答混入を防ぐ。

この仕様に反する表示崩れ、重複表示、会話順序逆転、古い応答混入はバグとして扱う。
