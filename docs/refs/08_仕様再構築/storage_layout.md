# storage_layout.md

## 目的

この文書は、RenCrow v0.1 における保存先の責務分離を定義する。  
目的は「どこに何を置くか」「誰が書けるか」「何を混ぜないか」を固定し、Chat / Worker / Coder / Hook の責務をストレージ上でも崩さないことにある。

本書は `chat_spec.md` `worker_spec.md` `coder_spec.md` `event_schema.md` `hook_policy.md` `commands.md` を支える共通基盤として扱う。

---

## 1. 基本原則

### 1.1 保存先は責務で分ける

保存先はファイル形式ではなく責務で分ける。

- `events/` は追跡のための記録
- `memory/` は再利用のための知識
- `artifacts/` は作業成果物
- `runtime/` は一時実行物
- `policy/` は固定ルール

### 1.2 人物記憶と手順記憶を混ぜない

ユーザーの好みや長期制約は `memory/profile/` に置く。  
再利用可能な作業手順は `memory/skills/` に置く。  
この2つは検索経路も更新権限も分ける。

### 1.3 履歴と知識を混ぜない

過去に何が起きたかは `events/` に残す。  
次回に使うべき形へ抽象化されたものだけを `memory/` に昇格する。

### 1.4 一時物と永続物を混ぜない

作業途中の中間生成物、キャッシュ、ロック、キュー、ステージングデータは `runtime/` に置く。  
`runtime/` の中身は再起動で消えても成立する前提で扱う。

### 1.5 すべての永続物は EventId とつながる

`memory/` `artifacts/` `audit/` に保存される永続物は、可能な限り source EventId を持つ。  
これにより「どの依頼から生まれたか」を逆引きできる。

---

## 2. 推奨トップレベル構成

```text
rencrow/
  config/
  policy/
  storage/
    events/
    memory/
      profile/
      skills/
      session/
      indexes/
    artifacts/
    audit/
    runtime/
      queue/
      cache/
      temp/
      locks/
  logs/
```

v0.1 では、実質的な保存対象は `storage/` 配下に集約する。  
`logs/` は人間向けまたは外部ツール向けの補助ログとして扱い、正本は `storage/events/` と `storage/audit/` に置く。

---

## 3. ディレクトリ仕様

## 3.1 `config/`

実行環境依存の設定を置く。  
秘密情報を含む設定は暗号化または環境変数参照を前提とする。

置いてよいもの:
- 接続先設定
- ストレージパス設定
- feature flag
- scheduler 設定

置いてはいけないもの:
- 人物記憶
- 手順記憶
- 実行履歴

---

## 3.2 `policy/`

固定ルールと契約文書を置く。  
AI が実行時に読む前提の静的文書群。

置いてよいもの:
- `chat_spec.md`
- `worker_spec.md`
- `coder_spec.md`
- `event_schema.md`
- `hook_policy.md`
- `commands.md`
- 将来の `safety_rules.md`
- 将来の `routing_rules.md`

置いてはいけないもの:
- セッションごとの実行結果
- 一時メモ
- 人物記憶の生データ

---

## 3.3 `storage/events/`

EventId 単位の正本履歴を置く。  
「何が起きたか」を残す層であり、「どう再利用するか」はここで決めない。

### 責務

- request / delegation / analysis / tool_pre / tool_post / verify / memory_write / stop / error / complete の記録
- actor ごとのイベント列の保存
- root_event_id / parent_event_id による系列追跡

### 推奨構成

```text
storage/events/
  2026/
    04/
      12/
        EVT-20260412-000100.json
        EVT-20260412-000118.json
        EVT-20260412-000121.json
```

### 保存形式

- 1 Event = 1 JSON を基本とする
- 追記専用で扱い、原則上書きしない
- 修正が必要な場合は correction event を別追加する

### 主な書き込み主体

- Chat
- Worker
- Coder
- Hook

### 主な読み取り主体

- Chat
- Worker
- Coder
- 索引生成ジョブ

### 置いてはいけないもの

- 会話の長期人物特性
- 再利用手順の完成版
- 大きなバイナリ成果物

---

## 3.4 `storage/memory/profile/`

人物記憶を置く。  
ユーザーの長期的な好み、禁止事項、運用制約など、Chat の振る舞いを変える情報のみを保存する。

### 責務

- ユーザーの長期嗜好
- 出力上の固定制約
- 環境上の継続制約
- 役割関係に影響する長期情報

### 更新権限

- 更新判断: Chat のみ
- 実保存: Worker または専用 MemoryWriter が代行可
- Worker / Coder が勝手に昇格してはいけない

### 推奨ファイル

```text
storage/memory/profile/
  user_profile.json
  conversation_preferences.json
  environment_constraints.json
```

### 保存形式の例

```json
{
  "profile_id": "PROFILE-USER-001",
  "category": "environment_constraints",
  "items": [
    {
      "key": "no_physical_move_for_runtime_dirs",
      "value": true,
      "summary": "venvやsite-packages等の実行環境をMove-Itemで物理移動しない",
      "source_event_ids": ["EVT-20260412-000100"]
    }
  ],
  "updated_at": "2026-04-12T11:00:00+09:00"
}
```

### 置いてはいけないもの

- 一時的な会話話題
- 調査結果メモ
- 実装手順
- 作業ログ

---

## 3.5 `storage/memory/skills/`

手順記憶を置く。  
失敗から復旧した手順、repo 固有の罠、再利用可能な実装・検証・調査手順などを保存する。

### 責務

- setup 手順
- recovery 手順
- verification 手順
- repo 固有の罠
- build / run / test の定型

### 更新権限

- 候補抽出: Worker / Coder / Hook
- 保存可否判断: Chat
- 保存実行: Worker または専用 MemoryWriter

### 推奨構成

```text
storage/memory/skills/
  setup/
  recovery/
  verification/
  repo/
```

### ファイル名規約

`SKILL-<連番>__<slug>.json`

例:
- `SKILL-00021__wsl_ollama_connectivity_check.json`
- `SKILL-00022__logging_init_order_verification.json`

### 置いてはいけないもの

- 感想だけのメモ
- 一度きりのファイルパス断片
- 根拠の薄い推測

---

## 3.6 `storage/memory/session/`

短期記憶またはセッション補助データを置く。  
会話継続やタスク継続のための一時永続化であり、長期知識ではない。

### 責務

- 現在のトピック
- 未解決タスクの中間状態
- セッションごとの要点
- 再起動後の軽い復元用状態

### 推奨構成

```text
storage/memory/session/
  SESSION-20260412-001.json
  SESSION-20260412-002.json
```

### 運用ルール

- TTL を持たせる
- 一定期間後は archive せず削除または summary 化する
- profile / skills への昇格は Chat 判断が必要

---

## 3.7 `storage/memory/indexes/`

検索補助の索引を置く。  
これは正本ではなく、再構築可能な派生データとして扱う。

### 責務

- events 検索索引
- skills 検索索引
- artifacts メタデータ索引
- embedding / inverted index / tag index

### 主な書き込み主体

- Worker の `/maintain`
- 専用 indexer

### 運用ルール

- 壊れても再構築可能であること
- 正本データの代わりとして扱わないこと
- source path / source event id を持つこと

---

## 3.8 `storage/artifacts/`

作業成果物を置く。  
人間に渡すファイル、AI が生成した成果ファイル、参照可能な派生成果を保存する。

### 責務

- 生成した md / json / csv / docx / pdf 等の成果物
- 中間ではなく保持価値のある生成物
- Event に紐づく納品候補

### 推奨構成

```text
storage/artifacts/
  2026/
    04/
      12/
        EVT-20260412-000121/
          chat_spec.md
          worker_spec.md
          coder_spec.md
```

### 原則

- artifact は EventId ディレクトリ配下に置く
- artifact 自体にも source EventId をメタデータとして持たせる
- 後から検索しやすいよう manifest を持ってよい

### 置いてはいけないもの

- 生の監査ログ
- 一時 temp ファイル
- キャッシュ

---

## 3.9 `storage/audit/`

監査・実行証跡を置く。  
`events/` が意味単位の履歴なのに対し、`audit/` は tool 実行や Hook 判定の詳細証跡を置く。

### 責務

- PreToolUse / PostToolUse の判定結果
- 実行コマンド監査
- changed_files 一覧
- exit_code と duration
- denied 操作の記録

### 推奨構成

```text
storage/audit/
  2026/
    04/
      12/
        EVT-20260412-000121__tool_audit.jsonl
        EVT-20260412-000121__hook_decisions.jsonl
```

### 保存形式

- JSONL を推奨
- 時系列追記に向く形式とする

### 置いてはいけないもの

- 長期人物記憶
- skills 本体
- 作業成果物

---

## 3.10 `storage/runtime/`

一時実行物を置く。  
ここは永続保証をしない。

### 推奨構成

```text
storage/runtime/
  queue/
  cache/
  temp/
  locks/
```

#### `queue/`
未処理タスク、再試行待ちタスク、scheduler 投入タスク。

#### `cache/`
再計算コスト削減のための一時データ。壊れてもよい。

#### `temp/`
中間生成物、作業用の一時ファイル。

#### `locks/`
二重実行防止。

### 置いてはいけないもの

- 正本イベント
- 長期記憶
- 最終成果物

---

## 3.11 `logs/`

人間向けのテキストログや運用補助ログ。  
正本ではないため、必要があれば再生成可能であるべき。

### 使い方

- デバッグ用の可読ログ
- ローテーション対象の補助ログ
- コンソール出力ミラー

### 注意

重要な履歴は `events/` または `audit/` にも必ず残す。  
`logs/` のみを唯一の証跡にしない。

---

## 4. 保存権限マトリクス

| パス | Chat | Worker | Coder | Hook |
|---|---|---|---|---|
| `storage/events/` | 書込可 | 書込可 | 書込可 | 書込可 |
| `storage/memory/profile/` | 判断可 | 原則不可 | 不可 | 不可 |
| `storage/memory/skills/` | 判断可 | 候補/保存可 | 候補可 | 候補可 |
| `storage/memory/session/` | 書込可 | 補助可 | 原則不可 | 不可 |
| `storage/memory/indexes/` | 読取可 | 書込可 | 読取可 | 不可 |
| `storage/artifacts/` | 生成指示 | 補助可 | 書込可 | 不可 |
| `storage/audit/` | 読取可 | 補助可 | 書込可 | 書込可 |
| `storage/runtime/` | 最小限 | 書込可 | 書込可 | 補助可 |
| `policy/` | 読取可 | 読取可 | 読取可 | 読取可 |

補足:
- `memory/profile` の実保存を Worker に委ねる場合でも、判断は Chat のみが行う
- Coder は `memory/profile` を直接読まない前提を維持する
- Hook は正本知識を書き換えない。候補または監査のみ

---

## 5. 命名規約

### 5.1 Event

- `EVT-YYYYMMDD-######.json`

### 5.2 Skill

- `SKILL-#####__<slug>.json`

### 5.3 Session

- `SESSION-YYYYMMDD-###.json`

### 5.4 Artifact ディレクトリ

- `storage/artifacts/YYYY/MM/DD/<EventId>/`

### 5.5 Audit

- `<EventId>__tool_audit.jsonl`
- `<EventId>__hook_decisions.jsonl`

---

## 6. ライフサイクル

### 6.1 Event の流れ

1. Chat が RootEventId を発行
2. 委譲ごとに ChildEventId を発行
3. 各 actor が `storage/events/` にイベントを書き込む
4. Hook の証跡は `storage/audit/` に追記する
5. Worker が必要に応じて `memory/indexes/` を更新する

### 6.2 Skill 化の流れ

1. Worker / Coder / Hook が skill candidate を出す
2. Chat が保存可否を判断する
3. Worker が `memory/skills/` に保存する
4. indexes を再構築する

### 6.3 Artifact の流れ

1. Coder または補助系が成果物を生成する
2. `artifacts/<date>/<EventId>/` に保存する
3. Event に artifact path を紐づける
4. 必要なら manifest を更新する

---

## 7. 迷いやすい境界の判断基準

### 7.1 「調査結果メモ」はどこに置くか

一次情報のままなら `events/`。  
次回も使う手順へ抽象化できたら `memory/skills/`。

### 7.2 「ユーザーの一度きりの希望」はどこに置くか

その会話だけなら `memory/session/`。  
長期的制約なら `memory/profile/`。

### 7.3 「テスト結果ファイル」はどこに置くか

監査証跡なら `audit/`。  
人に見せる成果レポートなら `artifacts/`。  
一時出力なら `runtime/temp/`。

### 7.4 「Hook の判定結果」はどこに置くか

正本は `audit/`。  
要約のみを Event に持たせてよい。

---

## 8. v0.1 でまだ固定しないもの

以下は v0.1 では詳細固定しない。

- embedding 実装方式
- DB を使うかファイルのみか
- object storage 連携
- 暗号化方式
- マルチマシン同期方式
- backup / retention の期間値

ただし、責務分離だけは先に固定する。

---

## 9. 最小実装例

```text
rencrow/
  policy/
    chat_spec.md
    worker_spec.md
    coder_spec.md
    event_schema.md
    hook_policy.md
    commands.md
    storage_layout.md
  storage/
    events/
      2026/04/12/EVT-20260412-000100.json
    memory/
      profile/
        user_profile.json
      skills/
        recovery/SKILL-00021__wsl_ollama_connectivity_check.json
      session/
        SESSION-20260412-001.json
      indexes/
        events_index.json
    artifacts/
      2026/04/12/EVT-20260412-000121/chat_spec.md
    audit/
      2026/04/12/EVT-20260412-000121__tool_audit.jsonl
    runtime/
      queue/
      cache/
      temp/
      locks/
```

---

## 10. 一文要約

`events/` は起きたこと、`memory/` は次回に使う知識、`artifacts/` は成果物、`audit/` は証跡、`runtime/` は消えてよい一時物。
