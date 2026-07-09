# session_lifecycle.md

## 目的

この文書は、RenCrow における 1 セッションの開始から終了までの流れを固定するための仕様である。  
対象は Chat / Worker / Coder の 3 主体とし、共通基盤として EventId、Hook、runtime state、memory、artifacts を前提とする。

本仕様の目的は次の 4 つ。

1. セッション中の責務境界を明確にする  
2. 委譲と統合の順序を固定する  
3. 保存候補抽出と永続化判断を分離する  
4. 未完了・高リスク・失敗時の終了条件を明確にする

---

## 前提

- Chat は唯一の会話主体である
- Worker は独立コンテキストの調査専用機である
- Coder は Hook と Sandbox の内側でのみ実装・検証を行う
- すべての主要処理は EventId を持つ
- runtime state は短期状態であり、記憶の本体ではない
- 永続化は「候補抽出」と「保存確定」を分けて扱う

---

## セッションの定義

本仕様におけるセッションとは、Chat が 1 つのユーザー入力を受け取り、必要に応じて Worker / Coder へ委譲し、結果を統合して、ユーザーへの応答を完了するまでの処理単位をいう。

通常は 1 ユーザー発話 = 1 セッションとする。  
ただし、長い対話の中でも、明確に目的が変わった時点で新しい RootEventId を発行してよい。

---

## ライフサイクル全体像

標準ライフサイクルは次の順で進む。

1. Session Start  
2. Input Normalize  
3. Initial Triage  
4. Context Load  
5. Routing Decision  
6. Delegation Execution  
7. Result Integration  
8. Response Build  
9. Memory Candidate Extraction  
10. Persistence Decision  
11. Session Close

高リスク・失敗・未完了時は、この途中で分岐が入る。

---

## Phase 1: Session Start

### 目的

ユーザー入力を受け取り、セッションの作業単位を初期化する。

### 主担当

Chat

### 必須処理

- RootEventId を発行する
- runtime state を初期化する
- channel を確定する
- 受信時刻を記録する
- request.received イベントを出す

### 出力

```json
{
  "root_event_id": "EVT-20260412-000100",
  "session_id": "SES-20260412-000045",
  "channel": "voice|cli|web|mobile|message",
  "started_at": "2026-04-12T10:15:00+09:00",
  "status": "started"
}
```

### 注意点

- この時点では記憶の永続更新を行わない
- セッション開始時点で外部委譲はまだ行わない

---

## Phase 2: Input Normalize

### 目的

入力チャネル差を吸収し、Chat が扱う標準形式へ正規化する。

### 主担当

Chat

### 必須処理

- 音声入力なら文字列へ変換済みテキストを受け取る
- 不要なメタ情報を分離する
- 添付や参照パスを抽出する
- 指示本文、補足、明示制約を分ける

### 出力

```json
{
  "root_event_id": "EVT-20260412-000100",
  "user_text": "依頼本文",
  "attachments": ["path/to/file"],
  "explicit_constraints": [
    "非破壊優先",
    "共有環境を変更しない"
  ],
  "normalized": true
}
```

### 注意点

- 正規化は意味づけの前段であり、解釈と混ぜない
- ユーザーの文体や感情表現を、勝手に命令へ書き換えすぎない

---

## Phase 3: Initial Triage

### 目的

依頼の種類、危険度、委譲の必要性を一次判定する。

### 主担当

Chat

### 判定軸

- 会話完結か
- 調査が必要か
- 実装が必要か
- 検証が必要か
- 高リスクか
- 定期整備か

### 必須処理

- task_kind を暫定決定する
- risk_level を `low|medium|high` で付与する
- 調査先行か実装先行かを決める
- routing_rules.md に従って候補経路を決定する

### 出力

```json
{
  "root_event_id": "EVT-20260412-000100",
  "task_kind": "chat|investigate|plan|implement|verify|maintenance",
  "risk_level": "low|medium|high",
  "routing_candidate": "chat|worker|coder|multi_step"
}
```

### 注意点

- 調査と実装を同時開始しない
- 高リスク時は必ず段階を分ける

---

## Phase 4: Context Load

### 目的

必要最小限の記憶と履歴を読み込み、現在の判断材料をそろえる。

### 主担当

Chat

### 参照対象

- runtime state
- 人物記憶
- 手順記憶
- 履歴検索
- 直近セッションの関連 Event

### 必須処理

- memory.read イベントを出す
- 現タスクに必要な範囲だけを読む
- 人物記憶と手順記憶を混同しない
- 読み込んだ内容を runtime state へ展開する

### 注意点

- Worker/Coder へ人物記憶を丸渡ししない
- 過去ログ全文を無制限にロードしない

---

## Phase 5: Routing Decision

### 目的

Chat が自分で応答するか、Worker/Coder に委譲するかを確定する。

### 主担当

Chat

### 分岐規則

#### A. Chat 完結

次を満たすときは Chat がそのまま返答する。

- 調査不要
- 実装不要
- 高リスクでない
- 過去記憶参照だけで足りる

#### B. Worker 委譲

次を満たすときは Worker に `/investigate` または `/maintain` を送る。

- 大量読解が必要
- 類似実装比較が必要
- 過去 Event の探索が必要
- 索引更新や整備が必要

#### C. Coder 委譲

次を満たすときは Coder に `/plan` `/implement` `/verify` を段階的に送る。

- 実ファイル編集が必要
- テストや検証が必要
- 差分確認が必要

### 必須処理

- delegation.created イベントを出す
- task_payloads.md に従って payload を構築する
- 1 payload 1 objective を守る

### 注意点

- Worker と Coder に同一 objective を同時配布しない
- 調査不足のまま Coder へ飛ばさない

---

## Phase 6: Delegation Execution

### 目的

Worker または Coder にタスクを実行させる。

### 主担当

Worker または Coder

### 6-A. Worker 実行

Worker は以下の順で動く。

1. TaskStart Hook  
2. 調査対象確認  
3. 探索・比較・読解  
4. 要約作成  
5. ResultBeforeReturn Hook  
6. skill candidate 抽出  
7. Chat に返却

### 6-B. Coder 実行

Coder は以下の順で動く。

1. command 受信  
2. PreToolUse Hook  
3. 計画または実装または検証  
4. PostToolUse Hook  
5. 差分・テスト・結果整理  
6. Stop Hook  
7. skill candidate 抽出  
8. Chat に返却

### 注意点

- Worker/Coder は直接ユーザーへ返さない
- Worker/Coder は自律的に人物記憶を更新しない
- 失敗時は原因分類を付けて返す

---

## Phase 7: Result Integration

### 目的

Worker/Coder から返ってきた結果を Chat が統合し、最終応答または次段階委譲に接続する。

### 主担当

Chat

### 必須処理

- result.integrated イベントを出す
- 返却 status を確認する
- evidence / changes / verification を分離して扱う
- 調査結果から直接答えるか、次に Coder へ渡すかを決める
- 未解決リスクがあれば response workspace に残す

### 分岐

#### A. 完了

統合結果だけでユーザー応答を作れる場合、そのまま Response Build へ進む。

#### B. 再委譲

Worker の調査結果に基づき実装が必要になった場合、Coder へ渡す。

#### C. 差し戻し

Coder が範囲不足・権限不足・調査不足で止まった場合、Chat は必要なら Worker へ戻す。

### 注意点

- Worker の調査結果をそのまま最終文に貼り付けない
- Coder の変更結果と検証結果を混同しない

---

## Phase 8: Response Build

### 目的

ユーザー向けの最終応答文を構築する。

### 主担当

Chat

### 必須処理

- 現セッションの結論を 1 本にまとめる
- 変更した場合は何を変えたかを説明する
- 未解決点があれば残件として明示する
- 必要な時だけ成果物パスやファイル参照を含める

### 注意点

- 内部イベント列をそのまま露出しない
- Worker/Coder の中間文をそのまま見せない
- 推測を事実として混ぜない

---

## Phase 9: Memory Candidate Extraction

### 目的

今回のセッションから、永続化に値する候補だけを抽出する。

### 主担当

Chat（判断） + Worker（整形）

### 候補の種類

- 手順記憶候補
- 履歴検索用要約候補
- 人物記憶候補
- 監査ログ候補

### 必須処理

- memory.update_decided イベントを出す
- runtime state 全体を保存対象にしない
- 「保存候補」と「保存確定」を分ける

### 注意点

- 会話で一時的に出た感想やノイズを記憶しない
- 人物記憶は Chat だけが更新可否を決める
- Worker/Coder は候補を出してよいが、確定しない

---

## Phase 10: Persistence Decision

### 目的

候補から実際に永続化する内容を確定する。

### 主担当

Chat

### 保存先の判断

- 手順記憶 → `memory/skills/`
- 人物記憶 → `memory/profile/`
- 履歴要約 → `events/summary/`
- 監査情報 → `audit/`
- 成果物参照 → `artifacts/`

### 必須処理

- 保存対象と保存先を明示する
- source_event_ids を紐づける
- 保存不要と判断したものは破棄する

### 注意点

- runtime state を丸ごと永続化しない
- 保存のしすぎで profile / skills / events の境界を壊さない

---

## Phase 11: Session Close

### 目的

セッションを終了し、次セッションへ持ち越すべき短期情報と破棄すべき一時情報を分離する。

### 主担当

Chat

### 必須処理

- response.completed イベントを出す
- session status を `completed|failed|blocked|deferred` のいずれかで確定する
- delegation registry を閉じる
- response workspace をクリアする
- 次セッションへ持ち越す pending topic があれば最小限だけ残す

### セッション終了状態

#### completed

目的達成済み。必要な保存処理も終わっている。

#### failed

技術的または権限的に完了できなかった。

#### blocked

外部条件不足、入力不足、アクセス不足などで停止。

#### deferred

今回の会話では結論を出さず、将来の別セッションへ持ち越す。

### 注意点

- 完了していない delegation を放置したまま completed にしない
- 一時作業用のメモを session close 後も残し続けない

---

## 失敗時ライフサイクル

### 1. Worker 失敗

Worker が失敗した場合、Chat は以下を行う。

- 失敗理由を確認する
- 範囲不足か、参照不足か、技術失敗かを分類する
- 必要に応じて調査範囲を再定義する
- その場で断定応答はしない

### 2. Coder 失敗

Coder が失敗した場合、Chat は以下を行う。

- 権限不足 / 範囲不足 / 技術失敗 / 検証失敗 を確認する
- 調査不足なら Worker に戻す
- 実装失敗なら `/plan` 段階に戻す
- 安全上怪しい場合は終了してユーザーへ説明する

### 3. Chat 統合失敗

Chat が結果を統合できない場合は、次を優先する。

1. いまある事実だけを整理する  
2. 未確定部分を明示する  
3. 必要な追加タスクだけを切り出す

---

## 高リスク時ライフサイクル

高リスク判定時は、通常フローを短縮してはいけない。  
必ず次の順で段階化する。

1. Chat が high risk を付与  
2. 必要なら Worker に調査委譲  
3. Coder は `/plan` から開始  
4. 計画確認後に `/implement` へ進む  
5. `/verify` を省略しない  
6. Response Build でリスクと残件を明示する

高リスクの例:

- 共有環境変更
- 削除・上書き破壊
- 物理移動を伴う構成変更
- ネットワークや外部実行系の自動有効化
- 本番環境に近い設定変更

---

## セッション中に保持してよい一時状態

保持してよいものは以下。

- 現在の objective
- 現在の delegation 一覧
- 未統合の調査結果
- 応答組み立て中の要点メモ
- pending topic

保持し続けてはいけないものは以下。

- 調査中に読んだ全文ログ
- 一時的な感想メモ
- 中間生成物の全部
- 保存不要と判断した candidate

---

## セッション終了チェックリスト

Session Close 前に Chat は最低限以下を確認する。

- RootEventId がある
- delegation の status が確定している
- response workspace が統合済みである
- 保存候補と非保存情報が分離されている
- 終了状態が `completed|failed|blocked|deferred` のいずれかで確定している
- ユーザー応答が完了している

---

## 最小ステートマシン

```text
START
  -> NORMALIZE
  -> TRIAGE
  -> CONTEXT_LOAD
  -> ROUTE
    -> CHAT_ONLY -> RESPONSE_BUILD -> CANDIDATE_EXTRACT -> PERSIST_DECIDE -> CLOSE
    -> WORKER -> INTEGRATE
         -> RESPONSE_BUILD -> CANDIDATE_EXTRACT -> PERSIST_DECIDE -> CLOSE
         -> CODER_PLAN -> CODER_IMPLEMENT -> CODER_VERIFY -> INTEGRATE -> RESPONSE_BUILD -> CANDIDATE_EXTRACT -> PERSIST_DECIDE -> CLOSE
    -> CODER_PLAN -> CODER_IMPLEMENT -> CODER_VERIFY -> INTEGRATE -> RESPONSE_BUILD -> CANDIDATE_EXTRACT -> PERSIST_DECIDE -> CLOSE
    -> MAINTAIN -> CLOSE
```

---

## 一文での要約

セッションは、Chat が始めて、必要なときだけ Worker/Coder に渡し、最後に Chat が統合して閉じる。  
保存するのは session 全体ではなく、価値があると判断した候補だけである。
