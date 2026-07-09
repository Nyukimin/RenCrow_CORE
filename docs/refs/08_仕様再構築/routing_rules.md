# routing_rules.md

## 目的

本書は、Chat が受け取った依頼に対して、

- 自分で応答する
- Worker に委譲する
- Coder に委譲する
- Worker → Coder の順で段階委譲する

のいずれを選ぶかを、機械的に判定できるようにするための分岐規則を定義する。

本書の目的は「賢そうに判断すること」ではなく、「責務を混ぜないこと」である。  
Chat は唯一の会話主体であり、Worker と Coder は Chat が構造化した依頼のみを処理する。

---

## 基本原則

### 1. Chat は最終応答主体である

ユーザーへの最終自然言語応答は常に Chat が行う。  
Worker と Coder は、ユーザーへの直接応答主体にならない。

### 2. 調査と実装を同時に始めない

不明点があるまま Coder に実装委譲しない。  
調査が必要なら、まず Worker に渡す。

### 3. コードを触る可能性があるなら Coder を経由する

Chat や Worker は実ファイル編集を行わない。  
変更、実行、検証が必要な時点で Coder を使う。

### 4. 危険度が高いほど段階を細かくする

危険度が高い依頼は、  
Chat 直答 → Worker 調査 → Coder /plan → Coder /implement → Coder /verify  
のように段階を分ける。

### 5. Chat は丸投げしない

委譲時には必ず以下を構造化して渡す。

- objective
- scope
- constraints
- expected_output
- relevant_memory_refs

---

## 判定の優先順位

Chat は以下の順で分岐判定する。

1. 会話だけで完結するか
2. 調査が必要か
3. コード変更または実行が必要か
4. 検証が必要か
5. 高リスクか
6. 定期整備タスクか

この順序を崩さない。

---

## ルーティング判定フロー

### Step 1: 会話完結判定

以下を満たす場合、Chat が自分で答える。

- 外部調査が不要
- 大量ログ読解が不要
- コード探索が不要
- 実ファイル変更が不要
- 過去イベント検索が不要、または軽微
- 危険操作の示唆がない

典型例:

- 概念説明
- 設計方針の相談
- 既知仕様の要約
- 既に揃っている材料の再整理

判定:
- `route = chat_direct`

---

### Step 2: 調査必要判定

以下のいずれかを満たす場合、まず Worker に委譲する。

- ログを読む必要がある
- 既存コードの位置を探す必要がある
- 類似実装を比較する必要がある
- 過去 Event を掘る必要がある
- 根拠確認なしに断定できない
- どのファイルを触るべきか不明
- 原因の候補が複数ある

典型例:

- 「どこでこの値を設定している？」
- 「この repo のログ保存経路を調べて」
- 「前にも似た障害があった？」
- 「候補の実装方針を比較して」

判定:
- `route = worker_investigate`

---

### Step 3: 実装必要判定

以下のいずれかを満たす場合、Coder を使う。

- ファイル編集が必要
- テスト実行が必要
- 実コマンド実行が必要
- 実装修正案ではなく、実修正が必要
- 動作確認が必要
- 差分を生成する必要がある

ただし、不明点があるまま /implement には進めない。  
対象範囲が曖昧なら先に Worker 調査、または Coder /plan を使う。

判定:
- `route = coder_plan`
- または `route = coder_implement`（対象範囲と手順が十分明確な場合のみ）

---

### Step 4: 検証必要判定

以下を満たす場合、Coder /verify を使う。

- 実装後の確認が未完
- テストが必要
- 差分確認が必要
- 実行結果の妥当性確認が必要
- 「直したはず」を「確認した」に変える必要がある

判定:
- `route = coder_verify`

---

### Step 5: 高リスク判定

以下の条件に当てはまる場合は、高リスク扱いとする。

- 共有環境を壊す可能性がある
- データ消失の可能性がある
- Move-Item 等の物理移動が絡む
- 権限昇格が必要
- ネットワーク接続の拡張が必要
- 未知の外部ツール導入が必要
- 本番相当データや実環境を触る
- 既存運用に波及する可能性がある

高リスク時は、以下を強制する。

- Chat 直答しない
- Worker で事前確認
- Coder は必ず /plan から開始
- /implement 前に制約を再確認
- /verify を省略しない

判定:
- `risk = high`
- `route = worker_investigate -> coder_plan -> coder_implement -> coder_verify`

---

### Step 6: 定期整備判定

以下は Worker /maintain に送る。

- ログ要約
- skill 候補抽出
- index 更新
- 参照整合チェック
- 古いイベントの圧縮
- 成果物一覧の再生成

判定:
- `route = worker_maintain`

---

## ルーティングマトリクス

| 条件 | 主担当 | command | 補足 |
|---|---|---|---|
| 会話だけで完結 | Chat | なし | Chat が直接返答 |
| 根拠調査が必要 | Worker | /investigate | 読む・探す・比べる |
| 実装方針だけ固めたい | Coder | /plan | 編集しない |
| 明確な修正を反映したい | Coder | /implement | Hook 必須 |
| 実装後の確認 | Coder | /verify | テスト・差分確認 |
| 定期整備・後処理 | Worker | /maintain | 非同期向き |
| 高リスク変更 | Worker→Coder | /investigate → /plan → /implement → /verify | 段階分割必須 |

---

## 典型ルート

### 1. 概念説明

例:
- 「EventId の意味を整理して」
- 「Worker と Coder の違いを説明して」

ルート:
- Chat 直答

---

### 2. 調査だけ必要

例:
- 「この repo でログ初期化はどこ？」
- 「前回の類似障害を探して」

ルート:
- Chat → Worker /investigate → Chat → ユーザー

---

### 3. 調査のあと実装

例:
- 「この不具合を直したい。原因候補を見てから修正して」
- 「既存構造を見て、最小変更で直して」

ルート:
- Chat → Worker /investigate
- Chat が結果を統合
- Chat → Coder /plan
- 必要なら Coder /implement
- Coder /verify
- Chat → ユーザー

---

### 4. 実装だけでよい

例:
- 「この関数名を全部 rename して」
- 「この定数の値を差し替えてテストして」

前提:
- 対象ファイル・変更内容・影響範囲が十分明確

ルート:
- Chat → Coder /implement → Coder /verify → Chat → ユーザー

---

### 5. 整備タスク

例:
- 「前日の Event を要約して」
- 「skill 候補を整理して」

ルート:
- Chat or Scheduler → Worker /maintain

---

## Chat の判定項目

Chat は各依頼ごとに最低限以下を判定する。

```json
{
  "needs_investigation": true,
  "needs_code_change": false,
  "needs_verification": false,
  "risk_level": "low|medium|high",
  "can_answer_directly": false,
  "recommended_route": "worker_investigate"
}
```

---

## Chat の疑似コード

```text
on_user_request(request):
    parse_intent(request)
    assess_risk(request)
    read_relevant_memory()

    if can_answer_directly(request):
        return chat_direct_response()

    if is_maintenance_task(request):
        return delegate_to_worker("/maintain")

    if needs_investigation(request):
        worker_result = delegate_to_worker("/investigate")
        integrate(worker_result)

        if worker_result.status in ["failed", "blocked"]:
            return respond_with_gap_and_next_step()

        if not needs_code_change(request):
            return respond_from_investigation(worker_result)

    if needs_code_change(request):
        plan_result = delegate_to_coder("/plan")

        if plan_result.status in ["failed", "blocked"]:
            return respond_with_plan_issue(plan_result)

        implement_result = delegate_to_coder("/implement")

        if implement_result.status in ["failed", "blocked"]:
            return respond_with_implementation_issue(implement_result)

        verify_result = delegate_to_coder("/verify")
        return respond_from_verified_result(verify_result)

    return fallback_min_risk_response()
```

---

## Worker へ渡す条件

Chat は以下の条件で Worker を使う。

- 「調べる」が主動詞の依頼
- 変更前の現状把握が必要
- 根拠不足のまま進めたくない
- 複数候補の比較が必要
- ログ・履歴・既存資産の探索が必要

Worker に渡さないもの:

- ユーザーへの返答文作成そのもの
- 実ファイル編集
- 危険コマンド
- 最終結論の確定

---

## Coder へ渡す条件

Chat は以下の条件で Coder を使う。

- 変更が必要
- 実行が必要
- テストが必要
- 差分確認が必要
- 手元環境で再現確認したい

Coder に渡さないもの:

- 調査だけで済むこと
- 対象範囲が不明な依頼
- 高リスクなのに前提確認が終わっていない依頼
- 自由会話的な相談

---

## Worker と Coder を直結しない理由

Worker と Coder は互いに直接会話しない。  
必要な連携は必ず Chat を経由する。

理由は以下。

- 調査結果の意味づけは Chat が持つべきだから
- 人物記憶や会話文脈を Worker/Coder に漏らしすぎないため
- 誰が何を判断したかを明確にするため
- EventId の親子関係を単純に保つため

---

## 失敗時ルーティング

### Worker が失敗した場合

Chat は以下のどちらかを返す。

- 不足している根拠と必要な追加範囲
- 現時点で言える最小限の結論

勝手に Coder に送らない。

### Coder /plan が失敗した場合

Chat は以下を確認する。

- scope が広すぎないか
- constraints が厳しすぎないか
- 事前調査が不足していないか

必要なら Worker に戻す。

### Coder /implement が失敗した場合

Chat は失敗理由を次のいずれかに分類する。

- 権限不足
- 範囲不足
- 技術的失敗
- 検証失敗
- 安全制約違反

分類後、必要なら Worker 調査に戻す。

### Coder /verify が失敗した場合

Chat は「修正未完」として扱う。  
実装済みでも、検証不能なら完了扱いにしない。

---

## 最小リスク優先ルール

迷った場合の優先順位は以下。

1. Chat 直答
2. Worker /investigate
3. Coder /plan
4. Coder /implement
5. Coder /verify
6. 高リスク実装

つまり、より破壊的な方向へは、根拠と前提が揃った時だけ進む。

---

## v0.1 で固定する判断の一文

Chat は「まず自分で答えられるか」を見る。  
答えられない時は「まず Worker で調べる」。  
変更が必要になって初めて Coder を使う。  
高リスク時は、必ず段階を分ける。
