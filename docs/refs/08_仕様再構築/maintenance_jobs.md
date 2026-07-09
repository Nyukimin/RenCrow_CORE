# maintenance_jobs.md

## 目的

この文書は、RenCrow における Worker の定期整備ジョブを定義する。  
対象は Chat / Worker / Coder のうち、主として Worker が担う「整える仕事」であり、会話応答や実装作業とは分離して扱う。

本仕様の目的は次の3点である。

1. 履歴・索引・手順記憶の品質を維持すること  
2. 実行中セッションを汚さずに後処理を回すこと  
3. 失敗時に壊さず止まり、再実行しやすい形にすること

Worker の定期整備は、通知のためではなく、次の判断や作業を軽くするために存在する。

---

## 基本原則

Worker の定期整備は、次の原則に従う。

- 会話応答をブロックしない  
- 実装変更を行わない  
- 破壊的操作を行わない  
- 1ジョブ1目的を守る  
- 各ジョブは EventId を持つ  
- 失敗しても他ジョブへ連鎖させない  
- 結果は短い要約と機械可読な状態で残す

定期整備は原則として Worker が担当する。  
Chat は起動判断または結果参照のみを行い、Coder は定期整備に参加しない。

---

## ジョブ分類

定期整備ジョブは以下の4系統に分ける。

### 1. 起動時ジョブ

セッション開始時、またはサービス起動時に一度だけ走る整備。

目的は、前回の残骸を引きずらず、安全に開始できる状態へ戻すこと。

代表例:

- runtime/ の一時状態点検  
- 未完了 Event の確認  
- 壊れたロックファイルの検出  
- 保留中キューの整合確認  
- 前回失敗ジョブの再試行可否判定

### 2. 日次ジョブ

1日単位で回す整備。

目的は、履歴と索引の鮮度を保ち、翌日の検索コストを下げること。

代表例:

- 前日 Event の要約生成  
- 新規 skill 候補の抽出  
- 履歴検索インデックス更新  
- artifacts の参照整合チェック  
- 監査ログの圧縮またはローテーション

### 3. 週次ジョブ

1週間単位で回す整備。

目的は、蓄積データの棚卸しと品質調整を行うこと。

代表例:

- skill 候補の重複統合  
- 失敗頻度の高いイベント群の再集約  
- 古い索引の再構築  
- 参照切れ path の洗い出し  
- 長期間未使用 skill の棚卸し

### 4. 手動ジョブ

ユーザー要求または Chat 判断で起動する整備。

目的は、定期実行では足りない局所メンテナンスを行うこと。

代表例:

- 特定期間の Event 再集約  
- 特定 repo の履歴再索引  
- 特定 skill 群の再分類  
- 監査ログの抽出  
- エラー後の再整備

---

## ジョブ起動ルール

### 起動主体

ジョブの起動主体は次のいずれかとする。

- scheduler  
- service bootstrap  
- Chat  
- operator manual request

Worker は自律的に新しい種類のジョブを増やさない。  
ジョブ種別は事前定義されたものに限る。

### 起動条件

各ジョブは、次の条件を満たしたときのみ起動する。

- 同種ジョブが実行中でない  
- 高優先度の対話処理を阻害しない  
- 必要な最小入力がそろっている  
- 直前失敗時の再試行条件を満たす

### 排他制御

同一カテゴリのジョブは並列起動しない。  
たとえば `daily.index.refresh` と `daily.index.refresh` の同時実行は禁止する。

異なるカテゴリでも、同一書き込み先を持つものは排他対象とする。  
例として、`memory/skills/` へ書くジョブ同士は排他する。

---

## ジョブ共通ペイロード

すべての定期整備ジョブは、少なくとも次の構造を持つ。

```json
{
  "event_id": "EVT-20260412-000900",
  "root_event_id": "EVT-20260412-000900",
  "actor": "worker",
  "command": "/maintain",
  "job_name": "daily.index.refresh",
  "job_category": "startup|daily|weekly|manual",
  "objective": "履歴検索インデックスの更新",
  "scope": ["events/", "memory/", "artifacts/"],
  "constraints": [
    "non-destructive",
    "no-code-change",
    "no-external-expansion"
  ],
  "triggered_by": "scheduler",
  "scheduled_at": "2026-04-12T03:00:00+09:00"
}
```

---

## 日次ジョブ仕様

### daily.event.summarize

目的: 前日分 Event を短く要約し、検索と棚卸しを軽くする。  
入力: 前日の日付範囲に属する Event 群。  
出力: 日次要約ファイル、件数統計、異常イベント一覧。

処理内容:

- 前日 Event を収集する  
- actor 別件数を集計する  
- failed / blocked を抽出する  
- 重要イベントを短く要約する  
- summary を `artifacts/daily_summaries/` へ保存する

禁止事項:

- Event 内容の改変  
- 元ログ削除  
- 解釈の上書き

### daily.skill.candidate.extract

目的: 前日作業から再利用価値のある手順候補を抜き出す。  
入力: 前日 Event、Hook 記録、verify 結果。  
出力: skill 候補一覧。

抽出条件:

- 一度失敗したが解決した  
- repo 固有の罠を越えた  
- 検証手順が明確に残っている  
- 次回にも使い回せそう

保存先:

- `runtime/skill_candidates/` に一時保存  
- 確定保存は Chat 判断後に `memory/skills/` へ移す

### daily.index.refresh

目的: 履歴検索用インデックスの鮮度維持。  
入力: 新規 Event、要約、成果物メタデータ。  
出力: 更新済みインデックス。

処理内容:

- 新規追加分のみ差分更新する  
- 壊れた参照があれば警告を記録する  
- 再構築が必要な場合でも、まず差分更新を試みる

禁止事項:

- 全再構築を常態化しない  
- 参照切れを黙って削除しない

### daily.audit.rotate

目的: 監査ログのサイズ制御と読みやすさ維持。  
入力: audit 配下のログ。  
出力: ローテーション済み監査ログ、圧縮済み旧ログ。

処理内容:

- 当日以外のログを日単位で束ねる  
- 圧縮または世代管理する  
- 参照メタデータを残す

禁止事項:

- 最新ログの欠落  
- Event との対応情報の喪失

---

## 週次ジョブ仕様

### weekly.skill.merge.review

目的: skill 候補の重複統合と品質点検。  
入力: `memory/skills/` と `runtime/skill_candidates/`。  
出力: 重複候補一覧、統合提案。

処理内容:

- タイトル類似  
- 症状類似  
- 手順重複  
- source_event_ids 重複

を基準に重複候補を出す。

注意:

このジョブは統合提案までとし、自動統合はしない。  
確定は Chat または明示的な管理フローで行う。

### weekly.failure.cluster

目的: 失敗イベントの偏りを把握し、再発防止の材料を作る。  
入力: 1週間分の failed / blocked Event。  
出力: 原因クラスタ要約。

分類例:

- Hook 拒否  
- 権限不足  
- 範囲不足  
- 検証失敗  
- 外部依存不整合

### weekly.index.rebuild.check

目的: 差分更新だけでは劣化しうる索引を点検する。  
入力: 現在の index メタデータ。  
出力: 再構築必要判定。

注意:

このジョブは原則として「再構築するかどうか」を判定する。  
重い再構築自体は別ジョブに分ける。

### weekly.path.integrity.scan

目的: 保存済み参照 path の健全性確認。  
入力: events / memory / artifacts に埋め込まれた参照先。  
出力: 参照切れ一覧。

禁止事項:

- 参照切れ path の自動削除  
- 元データの推測補完

---

## 起動時ジョブ仕様

### startup.runtime.sanity_check

目的: runtime の残留状態を安全に点検する。  
入力: `runtime/` 配下。  
出力: 開始可否、保留ファイル一覧。

処理内容:

- stale lock の検出  
- 中断 payload の存在確認  
- 一時ファイルの異常肥大確認  
- 前回異常終了の痕跡確認

### startup.pending.retry_review

目的: 前回失敗または中断された整備ジョブの再試行可否を判断する。  
入力: pending queue、failed job record。  
出力: retry / skip / manual review の判定。

注意:

Worker は自動再試行してよい条件を事前定義し、それを超えるものは manual review 扱いにする。

---

## 手動ジョブ仕様

### manual.reindex.target_range

目的: 特定範囲のみ再索引する。  
入力: path 範囲、日付範囲、対象カテゴリ。  
出力: 限定再索引結果。

### manual.rebuild.skill_candidates

目的: skill 候補抽出をやり直す。  
入力: 指定 Event 範囲。  
出力: 再抽出候補。

### manual.audit.export

目的: 指定条件で監査ログを抜き出す。  
入力: EventId 範囲、actor、job 名。  
出力: 抽出パッケージ。

---

## ジョブ出力仕様

各ジョブは、最低でも次の返却構造を持つ。

```json
{
  "event_id": "EVT-20260412-000900",
  "job_name": "daily.index.refresh",
  "status": "completed|failed|blocked|skipped",
  "summary": "差分更新を完了。参照切れ2件を検出。",
  "metrics": {
    "processed_count": 124,
    "warning_count": 2,
    "error_count": 0
  },
  "outputs": [
    "artifacts/index_refresh/2026-04-12.json"
  ],
  "next_action": "none|chat_review|manual_review|retry_later"
}
```

出力は、自然言語要約と機械可読メタデータを両方含むこと。

---

## 失敗時方針

定期整備ジョブは失敗しても、原則として次を守る。

- 元データを壊さない  
- 途中生成物を明示して止まる  
- failure 種別を分類して残す  
- 必要なら再試行可能状態で終わる

失敗分類は少なくとも以下を持つ。

- input_missing  
- lock_conflict  
- path_broken  
- index_error  
- permission_denied  
- unexpected_runtime_state

---

## 優先度ルール

Worker は対話処理を優先する。  
高優先度の会話応答と競合する場合、定期整備は次の優先順で延期または継続判定する。

1. 起動時ジョブ  
2. 破損防止系ジョブ  
3. 日次要約  
4. 日次索引更新  
5. 週次棚卸し

週次ジョブは、会話負荷が高い時間帯には起動しない方針を基本とする。

---

## 保存先ガイド

推奨保存先は以下。

- 起動時チェック結果: `runtime/startup_checks/`  
- 日次要約: `artifacts/daily_summaries/`  
- skill 候補: `runtime/skill_candidates/`  
- index 更新結果: `artifacts/index_refresh/`  
- 週次棚卸し: `artifacts/weekly_reviews/`  
- 監査ローテーション結果: `audit/rotated/`

保存先は `storage_layout.md` の責務分離に従うこと。

---

## v0.1 の要点

定期整備は、静かに整える。  
書き換えるのではなく、要約し、索引し、候補を抜き、壊れを検出する。  
Worker はこの役割に徹し、Chat の判断と Coder の実装を軽くするために働く。
