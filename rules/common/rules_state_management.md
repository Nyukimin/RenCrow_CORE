# rules_state_management.md - ID・キャッシュ・状態管理ルール

**作成日**: 2026-05-09
**目的**: ID、キャッシュ、キュー、派生状態の乱立を防ぎ、デバッグ可能な状態管理を保つ
**適用範囲**: セッション管理、ストリーム処理、UI 状態、音声/TTS/STT、キャッシュを扱うすべての実装

---

## 1. Single Source of Truth

状態には必ず主たる真実を定める。

- 表示の真実
- 永続化の真実
- 再生状態の真実
- デバッグログの真実

同じ意味の状態を複数の map、queue、DOM、cache、ログに分散して持つ場合は、それぞれの責務を明文化する。

---

## 2. ID の追加原則

新しい ID を追加する前に、既存 ID で表現できないか確認する。

確認すること。

- 既存の `session_id` など、すでに使える ID はないか
- その ID は何の単位を表すか
- 発話、応答、チャンク、セッション、リクエストを混同していないか
- 欠落時に合成 ID を作るなら、衝突条件を説明できるか
- その ID が表示、重複排除、キャッシュ、ログのどれに使われるか

ID は増やすほど整合性の責任も増える。
便利だからという理由で派生 ID を作ってはいけない。

---

## 3. ID の責務分離

ID は単位ごとに分けて扱う。

- session ID: 会話や処理セッションの単位
- request / response ID: 1 回の要求や応答の単位
- utterance ID: 1 つの発話の単位
- chunk index / chunk ID: 分割片の単位

異なる単位の ID を、重複排除や境界判定に流用してはいけない。
流用が必要な場合は、理由と衝突条件をテストで示す。

---

## 4. キャッシュの追加原則

キャッシュは性能改善や遅延吸収のための道具であり、整合性設計の代替ではない。

キャッシュを追加する前に確認すること。

- 何を高速化または吸収するのか
- 主たる真実はどこか
- キャッシュ不整合時にどう復旧するか
- キャッシュを破棄する境界は何か
- 古いキャッシュが次のセッションへ混ざらないか
- 空値や不正値を cache / stock / queue に入れる前に防いでいるか

---

## 5. キューと pending 状態

pending queue や再生 queue は、主たる状態にしてはいけない。

- pending は一時状態である
- session/topic/request 境界で破棄条件を持つ
- queue に入ったものが必ず消費または破棄されることを確認する
- queue の中身を表示済みの真実として扱わない

UI では、イベント受信、表示、音声再生、口パクを混同しない。

---

## 6. 表示・音声・副作用の分離

表示、音声、口パク、ログは別責務として扱う。
TTS / Viewer 同期の詳細は `docs/refs/01_正本仕様/15_TTS_Viewer同期.md` を補助参照し、正本との衝突時は`docs/02_正本仕様/`を優先する。

- 表示は、表示イベントまたは表示用 state を主たる入力とする
- 音声 chunk は、音声再生と口パクのきっかけであり、本文表示の唯一の根拠にしない
- 音声 chunk の `text` / `display_text` で本文を埋める、置換する、再構成しない
- `message_id` / `turn_index` の一致は対応付けであり、表示権限ではない
- 表示正本が無い場合は本文を補完せず、診断表示またはログへ倒す
- 発話完了は次音声の再生制御に使う。本文表示完了と混同しない
- デバッグログは観測であり、アプリ状態の代替にしない

IdleChat 通常会話の TTS chunk は、`message_id` に従属する同一 chunk 単位で `chunk_index`, `display_text`, `speech_text`（現行 `text`）, `audio_path` / `audio_url` を持つこと。
`display_text` と `speech_text` を別々に chunk 分割して、同じ index で対応したものとして扱ってはいけない。

### 6.1 TTS / ACK の正本ルール

TTS playback ACK は「実際に音声再生を担当している active audio owner の観測」だけを発話完了の根拠にする。

- `active_audio=false` の Viewer ACK で、backend の TTS pending / response 完了 / topic gate を消化してはいけない。
- `status=error` は診断であり、非 active Viewer から来た場合は pending 完了の根拠にしない。
- `status=fallback` は禁止。受信した場合は明示エラーとして記録し、成功扱いしない。
- 古い Viewer、audio disabled Viewer、non-active Viewer の ACK は観測ログとして残すだけにする。
- pending を閉じてよいのは、active audio owner の ACK、backend timeout、stop / interrupt / session cleanup など backend 正本の経路だけ。
- 1つの `response_id` に複数 `chunk_index` がある場合、`response_id` 単位の完了判定で chunk の未再生を隠してはいけない。
- ACK ログには少なくとも `session_id`, `response_id`, `utterance_id`, `message_id`, `turn_index`, `chunk_index` 相当, `viewer_client_id`, `active_audio`, `status`, `error_code` を追跡可能にする。

### 6.2 reset / clear / cleanup の責務

reset 系関数は、消してよい状態の責務を限定する。

- 音声再生 reset で消してよいのは、再生中 marker、active flag、current audio pointer など音声進行状態だけである
- 表示正本、ID 対応、履歴、重複排除、診断根拠を別責務の reset で消してはいけない
- 状態を消す場合は、所有者、寿命、再構築元を説明できること
- 同一 `message_id` の複数 chunk では、chunk 間の audio end / reset 後も DOM 本文が増殖しないことを確認する

---

## 7. 状態追加時のチェックリスト

map、set、cache、queue、derived key を追加する前に、次を満たす。

- 既存状態で足りない理由を説明できる
- 所有者となるモジュールが明確
- 破棄タイミングが明確
- セッション境界で混ざらない
- 空値や不正値を入れない
- テストまたはログで挙動を確認できる

満たせない場合は、状態を追加せず設計に戻る。
