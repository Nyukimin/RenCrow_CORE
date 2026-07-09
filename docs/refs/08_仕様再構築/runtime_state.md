# runtime_state.md

## 目的

本書は、RenCrow におけるセッション中の短期状態 `runtime state` の扱いを定義する。

対象は主に Chat が保持する一時状態であり、Worker / Coder が参照できるのは Chat から明示的に渡された断片に限る。

本仕様の目的は次の 4 つである。

1. セッション中に必要な文脈だけを保持する
2. 永続記憶に混ぜてはいけない情報を明確にする
3. Worker / Coder への委譲に必要な最小状態を固定する
4. セッション終了時に何を捨て、何を保存候補にするかを分離する

---

## 基本原則

### 1. runtime state は短期用である

runtime state は「今この会話や作業を進めるための足場」である。  
人物記憶や手順記憶の代わりではない。

### 2. Chat が主所有者である

runtime state の主所有者は Chat とする。  
Worker / Coder は自分専用の局所状態を持ってよいが、セッション全体の基準状態は Chat のみが管理する。

### 3. 推測を記憶化しない

会話中の仮説、保留、未検証の前提は runtime state には置いてよいが、永続記憶へは自動保存しない。

### 4. 1セッション 1状態を基本とする

RootEventId ごとに 1 つの runtime state を持つ。  
ChildEventId 単位の局所状態は、親の runtime state から派生する作業状態として扱う。

### 5. 調査結果と最終判断を混ぜない

Worker / Coder の返却結果は runtime state に一時的に積んでよい。  
ただし、Chat が統合判断する前の段階では「確定事項」として扱わない。

---

## 管理対象

runtime state が持つべき対象は以下に限る。

- 現在の話題
- このセッションでの目的
- 直近の判断履歴
- 未解決の確認点
- 現在の危険度
- 現在進行中の委譲タスク
- 直近の要約
- 応答生成に必要な一時メモ

逆に、以下は runtime state の対象外とする。

- ユーザーの長期的な好みや人格情報
- 再利用可能な手順そのもの
- 過去セッション全体の保存ログ
- 監査ログ本体
- 成果物ファイルそのもの

---

## runtime state の層

runtime state は、以下の 3 層に分ける。

### 1. session core

Chat が会話全体を進めるための中心状態。

保持対象:

- root_event_id
- current_topic
- session_objective
- current_stage
- risk_level
- working_summary
- unresolved_questions
- active_constraints

### 2. delegation registry

Worker / Coder に委譲したタスクの管理状態。

保持対象:

- 進行中タスク一覧
- 委譲先 actor
- command 種別
- 期待結果
- 現在ステータス
- タイムアウト / 再試行要否

### 3. response workspace

最終応答を組み立てるための一時作業領域。

保持対象:

- 直近の findings
- 採用候補の結論
- 不採用にした案
- ユーザーへ返すときの注意点
- 最終応答直前の要約

---

## 推奨スキーマ

### 1. session core

```json
{
  "root_event_id": "EVT-20260412-000100",
  "session_id": "SES-20260412-001",
  "channel": "web|voice|cli|mobile|message",
  "current_topic": "RenCrow の runtime state 設計",
  "session_objective": "runtime state の仕様書を作る",
  "current_stage": "discussing|investigating|planning|implementing|verifying|closing",
  "risk_level": "low|medium|high",
  "working_summary": "ここまでの短い整理",
  "unresolved_questions": [
    "永続化対象をどこで切るか"
  ],
  "active_constraints": [
    "調査と実装を混ぜない",
    "非破壊優先"
  ],
  "updated_at": "2026-04-12T10:12:34+09:00"
}
```

### 2. delegation registry

```json
{
  "active_delegations": [
    {
      "event_id": "EVT-20260412-000118",
      "target": "worker",
      "command": "/investigate",
      "objective": "既存の保存レイアウトを確認する",
      "status": "queued|running|blocked|completed|failed",
      "expected_output": "summary",
      "created_at": "2026-04-12T10:13:02+09:00",
      "updated_at": "2026-04-12T10:14:21+09:00"
    }
  ]
}
```

### 3. response workspace

```json
{
  "candidate_conclusions": [
    {
      "label": "採用案A",
      "summary": "Chat が短期状態を持ち、Worker/Coder は局所状態だけ持つ",
      "status": "selected|rejected|pending"
    }
  ],
  "latest_findings": [
    "runtime state は Chat 主所有にする",
    "人物記憶は含めない"
  ],
  "response_notes": [
    "次の自然な文書候補も添える"
  ],
  "final_response_draft": ""
}
```

---

## 永続化ルール

### 1. 原則

runtime state は永続記憶ではない。  
セッション終了後は破棄を基本とする。

### 2. 例外

以下は runtime state から保存候補として抽出してよい。

- 再利用可能な手順候補
- repo 固有の罠
- 復旧手順
- 明確なユーザー指示で長期有効なもの
- 履歴検索用のイベント要約

### 3. 保存してはいけないもの

- 未検証の推測
- 途中の迷い
- 単なる下書き
- ユーザーの感情解釈
- その場限りの一時制約
- Worker / Coder の生ログ全文

---

## lifecycle

### 1. 開始時

Chat はユーザー入力を受けた時点で session core を初期化する。

初期化対象:

- root_event_id
- current_topic
- session_objective
- risk_level の初期値
- active_constraints の初期値
- working_summary を空で作成

### 2. 進行中

Chat は以下の節目で runtime state を更新する。

- ユーザー意図が明確になった時
- Worker / Coder に委譲した時
- 調査結果が戻った時
- 実装結果が戻った時
- 最終結論が固まった時

### 3. 終了時

Chat は session close 時に以下を行う。

1. working_summary を最終更新する
2. 保存候補があるかを判定する
3. 保存候補のみ Worker に渡す
4. runtime state 本体を破棄またはアーカイブ対象外として終了する

---

## Chat / Worker / Coder の境界

### Chat

Chat は runtime state 全体を保持・更新できる唯一の actor である。

### Worker

Worker は自分の task 実行に必要な局所状態のみを持つ。  
例:

- 読んだファイル一覧
- 比較対象一覧
- 候補のふるい落とし理由
- 調査要約の途中メモ

これらは Worker task 完了後に破棄し、必要部分だけを返却結果へ圧縮する。

### Coder

Coder は自分の task 実行に必要な局所状態のみを持つ。  
例:

- 変更対象ファイル
- 実行済みコマンド一覧
- テスト結果
- 未解決エラー

これらも task 完了後に破棄し、必要部分だけを構造化結果として返す。

### 禁止

Worker / Coder は Chat の session core を直接更新しない。  
Worker / Coder は人物記憶に相当する状態を持たない。

---

## フィールド設計ルール

### 必須にするもの

- 現在の話題
- 現在の目的
- 現在の危険度
- 未解決項目
- 進行中委譲
- 直近要約

### 必須にしないもの

- 完璧な会話全文
- すべての候補案
- 全ログのコピー
- 細かすぎる時系列イベント列

詳細なイベント列は `events/` に置き、runtime state には要約だけを持つ。

### サイズ制限

runtime state は「今の判断に必要な最小限」に保つ。  
推奨として、`working_summary` は短く保ち、委譲結果の全文をそのまま埋め込まない。

---

## 更新ポリシー

### 1. overwrite 優先

runtime state は履歴簿ではない。  
現在の判断に不要になった情報は上書きまたは削除してよい。

### 2. append が必要なもの

以下だけは配列管理が自然である。

- unresolved_questions
- active_delegations
- latest_findings
- candidate_conclusions

### 3. 収束したら削る

問題が解決したら、`unresolved_questions` から外す。  
委譲が終わったら `active_delegations` から外すか完了状態で圧縮する。

---

## エラー時の扱い

### 1. Worker / Coder 失敗時

失敗イベントが返ってきた場合、Chat は runtime state に以下を追加する。

- failure_summary
- failure_class
- retry_needed
- route_change_needed

ただし、生ログ全文は入れない。

### 2. セッション中断時

セッション中断時は、runtime state をそのまま人物記憶や手順記憶へ昇格させない。  
必要なら Event 要約のみ残す。

### 3. 矛盾発生時

調査結果と既存前提が矛盾する場合、runtime state には「確定」として書き換えず、`candidate_conclusions` で競合状態を持つ。

---

## Hook との関係

runtime state 自体に Hook は持たないが、更新節目は Hook と連動してよい。

代表例:

- Delegation 前: 現在の objective と constraints を確定
- Result 受領後: working_summary と unresolved_questions を更新
- Stop 前: final_response_draft と保存候補判定を更新

---

## 保存先との対応

runtime state は `runtime/` 配下に置く。  
推奨レイアウト例:

```text
runtime/
  sessions/
    SES-20260412-001.json
  delegations/
    EVT-20260412-000118.json
  drafts/
    EVT-20260412-000100-response.json
```

ただし、`runtime/` は永続記憶の本体ではない。  
セッション終了後のクリーンアップ対象とする。

---

## 最小保存例

```json
{
  "root_event_id": "EVT-20260412-000100",
  "session_id": "SES-20260412-001",
  "current_topic": "runtime_state.md の作成",
  "session_objective": "短期状態の境界を定義する",
  "current_stage": "closing",
  "risk_level": "low",
  "working_summary": "Chat が短期状態を持ち、Worker/Coder は局所状態のみ保持する方針で整理済み",
  "unresolved_questions": [],
  "active_constraints": [
    "人物記憶と混ぜない",
    "保存候補だけを抽出する"
  ],
  "active_delegations": [],
  "latest_findings": [
    "runtime state は永続記憶ではない",
    "イベント詳細は events に置く"
  ],
  "updated_at": "2026-04-12T10:30:00+09:00"
}
```

---

## 一文で言うと

runtime state は、Chat が今この会話を進めるための短期足場であり、  
記憶の本体ではなく、保存候補を見極めるための作業領域である。
