# failure_recovery.md

## 目的

この文書は、RenCrow v0.1 における失敗時の戻り先、再試行条件、責務分離を定義する。
対象は Chat / Worker / Coder / Hook であり、失敗を「誰が悪いか」ではなく「どの段階へ戻すべきか」で扱う。

本仕様の目的は次の3つである。

1. 失敗後の挙動を会話ごとに揺らさないこと
2. 調査不足・実装失敗・権限拒否・検証失敗を混同しないこと
3. EventId を軸に、再試行と中断の理由を追跡可能にすること

---

## 基本原則

### 1. 失敗は4種類に分ける

RenCrow では、失敗を少なくとも次の4種類に分類する。

- 調査失敗
- 実装失敗
- 制約違反 / Hook 拒否
- 検証失敗

この分類を曖昧にしない。
同じ「できません」でも、戻る段階が異なるためである。

### 2. 失敗時は勝手に責務を越境しない

Worker は失敗したからといって実装に入らない。
Coder は失敗したからといって広範囲調査へ拡張しない。
Hook は拒否理由を返すが、代替案の確定までは行わない。
Chat だけが、次にどこへ戻すかを最終判断する。

### 3. 再試行は「同じ失敗を繰り返さない条件」があるときだけ行う

無条件の再試行は禁止する。
再試行する場合は、少なくとも次のいずれかを満たす必要がある。

- 対象範囲が狭まった
- 前回失敗理由が明示できた
- 追加根拠が得られた
- Hook 条件が満たされた
- 検証条件が変わった

### 4. 失敗は必ずイベントとして残す

失敗は会話本文だけに残してはならない。
最低でも event_type=`error` または `blocked` として保存し、
原因分類、発生段階、戻り先候補、再試行条件を付与する。

---

## 用語

### blocked

処理継続条件が不足しており、正常にも異常にも完了していない状態。
例: 権限不足、参照不足、Hook 拒否。

### failed

処理を実行した結果、目的を達成できなかった状態。
例: テスト失敗、修正後も症状再現、探索結果が矛盾。

### recoverable

追加条件を満たせば、同系統の流れで再試行可能な失敗。

### terminal

現フローではこれ以上安全に進められない失敗。
Chat がユーザーへ説明して終了または別方針へ切り替える必要がある。

---

## 共通エラーレコード

失敗イベントは、少なくとも次の情報を持つ。

```json
{
  "event_id": "EVT-20260412-000301",
  "root_event_id": "EVT-20260412-000100",
  "actor": "worker|coder|hook|chat",
  "status": "blocked|failed",
  "failure_class": "investigation|implementation|hook_denied|verification|integration|storage|runtime",
  "stage": "investigate|plan|implement|verify|integrate|persist",
  "reason": "短い理由",
  "details": "必要最小限の補足",
  "recoverable": true,
  "retry_preconditions": [
    "再試行条件1",
    "再試行条件2"
  ],
  "recommended_return_to": "chat|worker|coder.plan|coder.implement|coder.verify",
  "related_event_ids": [
    "EVT-20260412-000280"
  ]
}
```

---

## Chat の失敗回復仕様

### Chat の主な失敗パターン

1. 意図判定不能
2. 文脈不足
3. Worker/Coder の結果が衝突
4. 高リスク判断で通常フローに乗せられない
5. 永続化判断不能

### Chat の戻り規則

#### A. 意図判定不能

Chat は自分で曖昧なまま答えを作らない。
まず Worker へ `/investigate` を出し、対象候補の整理を取る。

戻り先:
- Worker

再試行条件:
- 対象候補が狭まること
- 必要な前提差分が抽出されること

#### B. Worker と Coder の結果衝突

Chat はその場で折衷しない。
まず衝突点を明文化し、必要に応じて Worker に再調査を戻す。

戻り先:
- 原則 Worker
- 実装修正の誤りが明白な場合のみ Coder `/verify`

再試行条件:
- 衝突点が項目化されていること
- どちらの主張がどの証拠に依存するか示されていること

#### C. 永続化判断不能

Chat は保存を急がない。
保存候補を `pending` として残し、次回判定へ回す。

戻り先:
- 戻さない
- 永続化を見送り、セッション終了可能

---

## Worker の失敗回復仕様

### Worker の主な失敗パターン

1. 調査範囲不足
2. 根拠不足
3. 根拠の衝突
4. 読み込み対象過多
5. 外部参照が必要だが権限外

### Worker の禁止事項

- 失敗を理由に実装へ進まない
- 推測で欠落を埋めない
- 調査対象を勝手に外部まで拡張しない

### Worker の戻り規則

#### A. 調査範囲不足

必要なファイル、ログ、EventId が不足している場合、`blocked` として返す。

戻り先:
- Chat

Chat の次アクション:
- 対象範囲を追加して Worker 再委譲
- またはユーザーへ不足情報を説明

#### B. 根拠不足

探索は行えたが、断定に足る根拠がない場合は、結論ではなく候補集合として返す。

戻り先:
- Chat

Chat の次アクション:
- 候補を絞る追加調査
- 高リスクなら Coder へ進めない

#### C. 根拠衝突

複数ソースが矛盾する場合は、Worker 自身が最終判断しない。
衝突点、優先度不明点、追加で見るべき対象を返す。

戻り先:
- Chat
- 必要に応じて Worker 再調査

#### D. 読み込み対象過多

1回の調査で範囲が広すぎる場合、Worker は勝手に分割してよいが、要約単位を維持する。
ただし、探索が破綻した場合は `failed` ではなく `blocked` を優先する。

戻り先:
- Chat

再試行条件:
- 範囲が分割されること
- 調査目的が1つに絞られること

---

## Coder の失敗回復仕様

### Coder の主な失敗パターン

1. `/plan` で影響範囲が定まらない
2. `/implement` で Hook に拒否される
3. `/implement` で変更が成立しない
4. `/verify` でテスト失敗
5. 差分はあるが安全性が担保できない

### Coder の禁止事項

- 勝手に調査モードへ拡張しない
- Hook 拒否を回避するために別手段へ逃げない
- 検証失敗後に黙って追加修正し続けない
- 実装と検証を際限なく往復しない

### Coder の戻り規則

#### A. `/plan` 失敗

影響範囲、変更対象、検証対象のいずれかが不明なら `/plan` は完了扱いにしない。

戻り先:
- Chat

Chat の次アクション:
- Worker に追加調査
- スコープ縮小後に `/plan` 再実行

#### B. Hook 拒否

PreToolUse Hook で拒否された場合、Coder は代替コマンドを自己判断で実行しない。
拒否理由と、必要なら安全な代替方針候補だけを返す。

戻り先:
- Chat

Chat の次アクション:
- 制約を維持したままスコープ変更
- 必要なら `/plan` に戻す
- 明示許可がある場合のみ別フローを選ぶ

#### C. 実装失敗

ファイル変更やコマンド実行を行ったが目的達成に至らなかった場合、
Coder は「何を試し、どこで失敗したか」を返す。
この時点で広範囲再探索はしない。

戻り先:
- 原則 Chat
- 追加調査が必要なら Worker
- 単純な再計画で足りるなら Coder `/plan`

#### D. 検証失敗

`/verify` でテスト失敗や期待不一致が出た場合、Coder は失敗内容を分類する。

分類例:
- 既存不具合露出
- 今回変更による回帰
- 環境依存失敗
- 検証手順不足

戻り先:
- 回帰なら Coder `/plan`
- 環境依存なら Chat
- 根本原因不明なら Worker

#### E. 差分はあるが安全性不明

変更はできたが、副作用評価や検証範囲が不足している場合は `completed` にしない。
`partial` ではなく `blocked` として扱ってよい。

戻り先:
- Chat
- 必要に応じて Worker または `/verify`

---

## Hook の失敗回復仕様

### Hook の役割

Hook は拒否・監査・整形の強制点であり、会話主体ではない。
失敗時も判断を広げず、理由を構造化して返す。

### Hook の主な失敗パターン

1. 安全ポリシー違反
2. Sandbox 外要求
3. 許可されていないネットワーク操作
4. 監査ログ出力失敗
5. Stop 条件未達

### Hook の戻り規則

#### A. 安全ポリシー違反

戻り先:
- Chat

備考:
- Coder は代替実行しない
- Chat が制約維持のまま再計画する

#### B. 監査ログ出力失敗

PostToolUse Hook が監査記録を書けない場合、原則 `blocked` とする。
監査できない実行継続は避ける。

戻り先:
- Chat
- 必要に応じて Worker `/maintain`

#### C. Stop 条件未達

実装は終わったが、検証や差分確認が不足している場合、Stop Hook は完了を拒否する。

戻り先:
- Coder `/verify`
- または Coder `/plan`

---

## 典型ケース別の戻り表

| failure_class | 発生主体 | 典型原因 | 戻り先 | 備考 |
|---|---|---|---|---|
| investigation | Worker | 根拠不足 | Chat | Chat が追加調査範囲を決める |
| investigation | Worker | 範囲過大 | Chat | スコープ分割して再委譲 |
| implementation | Coder | 修正失敗 | Chat | 調査不足なら Worker へ |
| hook_denied | Hook/Coder | 危険操作 | Chat | 制約維持のまま再計画 |
| verification | Coder | 回帰発生 | Coder `/plan` | 原因が明確な場合 |
| verification | Coder | 原因不明 | Worker | 根本原因の再調査 |
| integration | Chat | 結果衝突 | Worker | 証拠の再整理 |
| storage | Worker/Hook | 保存失敗 | Chat | 永続化見送り可 |
| runtime | Chat | 状態不整合 | Chat | 必要なら安全終了 |

---

## 再試行ポリシー

### 1. 自動再試行してよいもの

次の条件を満たす場合のみ、内部的な単回再試行を許可する。

- 一時的 I/O 失敗
- ロック競合
- 監査ログ書き込みの一時失敗
- 非破壊で副作用がない再実行

最大回数は 1 回。
2 回目以降は Chat に戻す。

### 2. 自動再試行してはいけないもの

- Hook で拒否された危険操作
- スコープ不明の `/implement`
- 根拠不足のままの Worker 再探索
- 検証失敗後の無限修正ループ
- Sandbox 外実行

---

## 保存候補と失敗の関係

失敗そのものも手順記憶候補になりうる。
特に次の条件を満たす場合は `skill_candidate` を出してよい。

- 一度 blocked になったが、安全な戻し方が定義できた
- Hook 拒否から非破壊な代替ルートへ戻せた
- 検証失敗から原因分類と再計画の型が得られた

例:
- Move-Item を使わずジャンクションで回避した
- 共有 CUDA を触らず venv 側で閉じた
- 回帰テスト失敗から影響範囲限定の `/plan` に戻した

---

## Chat への返却メッセージ最小要件

Worker / Coder / Hook は失敗時、少なくとも次を Chat に返す。

1. どの段階で止まったか
2. なぜ止まったか
3. これは blocked か failed か
4. 再試行条件は何か
5. 次に戻すべき段階はどこか

自由文だけで返してはならない。

---

## 最小返却例

### Worker 失敗例

```json
{
  "event_id": "EVT-20260412-000220",
  "status": "blocked",
  "failure_class": "investigation",
  "stage": "investigate",
  "reason": "対象ログが不足しており根拠が足りない",
  "recoverable": true,
  "retry_preconditions": [
    "auth.log の対象期間を追加",
    "関連 EventId を 2 件以上参照"
  ],
  "recommended_return_to": "chat"
}
```

### Coder 検証失敗例

```json
{
  "event_id": "EVT-20260412-000241",
  "status": "failed",
  "failure_class": "verification",
  "stage": "verify",
  "reason": "回帰テスト tests/logging/test_rotation.py が失敗",
  "recoverable": true,
  "retry_preconditions": [
    "影響範囲を logging モジュールに限定して再計画",
    "rotation 設定初期化順を再確認"
  ],
  "recommended_return_to": "coder.plan"
}
```

### Hook 拒否例

```json
{
  "event_id": "EVT-20260412-000251",
  "status": "blocked",
  "failure_class": "hook_denied",
  "stage": "implement",
  "reason": "Move-Item による実行環境の物理移動は禁止",
  "recoverable": true,
  "retry_preconditions": [
    "ジャンクションまたはコピー方針へ切り替える"
  ],
  "recommended_return_to": "chat"
}
```

---

## まとめ

失敗時の基本は次の通りとする。

- Worker は調査失敗を調査のまま返す
- Coder は実装失敗を実装のまま返す
- Hook は拒否理由だけを返し、越権しない
- Chat だけが戻り先を最終決定する
- 再試行には条件が必要
- 失敗は EventId 付きで必ず残す

一文で言うと、RenCrow の failure recovery は
「止まった場所をごまかさず、その段階にふさわしい場所へ戻す」
ことで成立する。
