# memory_policy.md

## 目的

この文書は、RenCrow v0.1 における保存方針を定義する。  
対象は `profile / skills / history / runtime candidate` の4系統であり、何を保存し、何を保存せず、誰が判断し、誰が書き込むかを固定する。

本書の目的は、記憶を一枚岩にせず、会話都合の一時情報と、再利用価値のある知識と、過去の履歴を明確に分離することにある。

本書は `chat_spec.md` `worker_spec.md` `coder_spec.md` `storage_layout.md` `runtime_state.md` `session_lifecycle.md` を支える共通方針として扱う。

---

## 1. 基本原則

### 1.1 記憶は「保存層」と「判断主体」を分ける

保存先があることと、保存してよいことは同義ではない。  
RenCrow では、保存の可否判断と、実際の書き込み処理を分離する。

- 保存可否の最終判断は原則として Chat が持つ
- 実際の保存処理は Worker が担う
- Coder は保存候補を出せるが、直接の永続化は行わない

### 1.2 記憶は4系統に分ける

RenCrow v0.1 では、記憶を次の4系統に分ける。

- `profile`: 人物・運用・環境に関する長期制約
- `skills`: 再利用可能な手順知識
- `history`: 過去イベントの履歴と検索結果
- `runtime candidate`: セッション中に抽出された保存候補

### 1.3 会話そのものは原則として記憶にしない

会話全文は履歴の一部ではあっても、知識ではない。  
RenCrow は「全部残す」よりも「次回に効く形へ抽象化して残す」を優先する。

### 1.4 保存は EventId とつなげる

人物記憶、手順記憶、履歴要約、保存候補は、可能な限り source EventId を持つ。  
これにより、後から「なぜその記憶があるのか」をたどれるようにする。

### 1.5 一時候補と確定記憶を混ぜない

セッション中に出た良さそうな気づきは、すぐに本記憶へ入れない。  
まず `runtime candidate` に置き、終了前または整備時に昇格判断する。

---

## 2. 記憶の4系統

## 2.1 `profile`

### 位置づけ

`profile` は、ユーザーや運用環境に関する長期制約を保持する層である。  
Chat が最も重く参照し、Worker と Coder は原則として直接参照しない。

### 置いてよい情報

- ユーザーの出力好み
- 応答上の禁止事項
- プロジェクト横断の重要ルール
- OS / shell / 実行環境の固定条件
- 共有環境を壊さないための恒常制約

### 置いてはいけない情報

- 一時的な依頼内容
- 直近だけ有効な作業メモ
- repo 固有の実装ノウハウ
- 成功したコマンド列そのもの
- 生の会話全文

### 更新判断主体

- Chat のみ

### 書き込み主体

- Worker のみ

### 参照主体

- Chat: 常時参照可
- Worker: 原則不可。必要時は Chat が制約へ変換して渡す
- Coder: 原則不可。必要時は Chat が constraints として渡す

### 保存基準

次の条件を満たすときのみ `profile` へ保存する。

- 数週間〜数か月以上効く可能性がある
- 応答や安全判断を継続的に変える
- 個別の1タスクではなく、将来の複数タスクに影響する
- 単なる感想ではなく運用上の制約または嗜好として明確

---

## 2.2 `skills`

### 位置づけ

`skills` は、再利用可能な手順知識を保持する層である。  
Chat は検索起点を持ち、Worker と Coder は必要に応じて参照できる。

### 置いてよい情報

- 復旧手順
- 検証手順
- repo 固有の罠と回避手順
- セットアップ手順
- 比較的安定した調査手順
- build / test / verify の定型フロー

### 置いてはいけない情報

- 単発の作業ログ
- その場限りの思いつき
- 生の diff 全文
- 一度も再利用価値が確認されていない断片
- 人物嗜好や対話トーン

### 更新判断主体

- Chat が最終判断
- Worker / Coder は候補提示のみ可

### 書き込み主体

- Worker のみ

### 参照主体

- Chat: 可
- Worker: 可
- Coder: 可

### 保存基準

次のいずれかに当てはまる場合、`skills` 候補とする。

- 一度失敗したが、原因確認→修正→再確認まで到達した
- repo 固有の構造的な罠を越えた
- 同種の依頼で再利用しやすい手順が得られた
- 調査・実装・検証のいずれかで明確な定型が見えた

### 推奨スキーマ

```json
{
  "skill_id": "SKILL-00021",
  "title": "WSL経由でWindows Ollama接続確認",
  "category": "setup|recovery|verification|repo-specific",
  "preconditions": ["WSL2有効", "Ollama起動済み"],
  "symptoms": ["接続失敗", "localhost経路不一致"],
  "steps": ["確認手順1", "確認手順2"],
  "verification": ["期待される結果"],
  "side_effects": ["副作用があれば記録"],
  "do_not_apply_when": ["適用禁止条件"],
  "source_event_ids": ["EVT-20260412-000121"]
}
```

---

## 2.3 `history`

### 位置づけ

`history` は、過去に何が起きたかをたどるための層である。  
これは「知識」ではなく「履歴」であり、まず事実を保持する。

`history` の正本は `storage/events/` にある。  
ただし検索性を高めるため、要約・索引・逆引き情報を別途持ってよい。

### 置いてよい情報

- EventId とその系列
- 実行コマンド要約
- 成果物パス
- 失敗要因の分類
- 類似障害の逆引きタグ
- 関連 skill_id

### 置いてはいけない情報

- Chat の人物判断
- 嗜好そのもの
- 手順へ抽象化済みの本体知識
- 一時的な保存候補

### 更新判断主体

- Chat: 検索起点のみ
- Worker: 整備方針の判断可

### 書き込み主体

- Chat / Worker / Coder / Hook が Event を出す
- Worker が history index を整備する

### 参照主体

- Chat: 可
- Worker: 可
- Coder: 必要最小限で可

### 保存基準

履歴は原則として捨てず、検索可能性のために整理する。  
ただし、`history` に置くのは「過去に起きたことの要約」であり、「次に使うべき知識」ではない。

### 推奨スキーマ

```json
{
  "event_id": "EVT-20260412-000121",
  "actor": "coder",
  "task_kind": "implement",
  "objective": "ログ保存の不具合修正",
  "scope": ["src/logging/", "tests/logging/"],
  "result": "completed",
  "key_findings": ["初期化順が逆だった"],
  "commands_run": ["pytest tests/logging"],
  "artifacts": ["logs/run-20260412.json"],
  "related_skill_ids": ["SKILL-00021"]
}
```

---

## 2.4 `runtime candidate`

### 位置づけ

`runtime candidate` は、セッション中に抽出された「保存候補」の一時層である。  
ここは本記憶ではない。終了時または整備時に昇格判断される。

### 置いてよい情報

- profile 候補
- skill 候補
- history index 候補
- 忘れるべきと判断された一時メモ
- 保留判断の理由

### 置いてはいけない情報

- 確定した profile 本体
- 確定した skills 本体
- 大量の生ログ
- 長期保存前提の成果物

### 更新判断主体

- Chat: 候補採用 / 棄却 / 保留の最終判断
- Worker / Coder: 候補生成可

### 書き込み主体

- Chat
- Worker
- Coder
- Hook

### 参照主体

- Chat: 可
- Worker: maintain 時に可
- Coder: 原則不可

### 保存基準

候補として置く条件は次の通り。

- セッション中に再利用価値らしきものが見えた
- まだ長期保存の妥当性が確定していない
- 本記憶へ入れるには抽象化が足りない
- 逆に、その場で捨てるには惜しい

### 候補の寿命

- セッション終了時に再評価する
- 日次または週次の Worker 整備で再評価してよい
- 一定期間放置された候補は自動で棄却してよい

---

## 3. 保存可否の判定ルール

## 3.1 `profile` に入れる条件

次の4条件をすべて満たすことを原則とする。

1. 長期に効く
2. 応答または安全判断を変える
3. 個別 task を超えて再利用される
4. 会話の感想ではなく、制約または嗜好として定義できる

## 3.2 `skills` に入れる条件

次のいずれかを満たすこと。

- 複数回使えそうな手順である
- 失敗からの復旧筋が明確である
- 検証方法まで一続きで書ける
- repo / 環境特有の罠を越えた知識である

## 3.3 `history` に入れる条件

`history` は原則イベント起点で保持する。  
ただし、索引や要約は「検索しやすさ」を目的とし、「解釈」しすぎない。

## 3.4 `runtime candidate` に入れる条件

保存価値の可能性はあるが、直ちに本記憶へ入れるには早いものを置く。  
迷うものは本記憶ではなく `runtime candidate` に送る。

---

## 4. 保存しないもの

RenCrow v0.1 では、次のものは原則として保存対象にしない。

- 単発の雑談断片
- その場の感情的反応
- 生の会話全文をそのまま profile 化したもの
- 一度きりのコマンド履歴を skills 化したもの
- 根拠のない推測
- 保存理由が説明できないメモ
- Chat が最終判断していない候補の本保存

---

## 5. 役割ごとの責任分担

## 5.1 Chat

### 責務

- 記憶参照の起点になる
- `profile` 更新の最終判断を持つ
- `skills` 昇格の最終判断を持つ
- `runtime candidate` の採用 / 棄却 / 保留を決める

### 禁止事項

- 大量の索引更新を自分でしない
- 手順記憶の実保存を自分でしない
- Coder に profile を丸ごと見せない

## 5.2 Worker

### 責務

- `history` 索引を整備する
- `skills` を永続化する
- `runtime candidate` を再評価する
- `maintain` 実行時に冗長候補を整理する

### 禁止事項

- 人物記憶の最終判断をしない
- profile 本体を勝手に更新しない
- history を knowledge に勝手に昇格しない

## 5.3 Coder

### 責務

- `skills` 候補を出す
- 復旧手順、検証手順、repo 固有の罠を抽出する

### 禁止事項

- 直接の永続化をしない
- `profile` を読まない
- `history` を勝手に抽象化しない

---

## 6. 典型フロー

### 6.1 profile 更新フロー

1. Chat が会話から長期制約の可能性を検出する
2. `runtime candidate` に profile 候補を置く
3. 終了前または次回確認で長期有効性を評価する
4. 妥当なら Worker が `memory/profile/` へ保存する

### 6.2 skills 更新フロー

1. Worker または Coder が手順候補を抽出する
2. `runtime candidate` に skill 候補を置く
3. Chat が再利用価値を判断する
4. Worker が `memory/skills/` へ保存する

### 6.3 history 整備フロー

1. Chat / Worker / Coder / Hook が Event を出す
2. Worker が Event を日次または週次で索引化する
3. Chat が次回検索の起点として使う

---

## 7. ディレクトリ対応

本書の4系統は、`storage_layout.md` では次の場所に対応する。

- `profile` → `storage/memory/profile/`
- `skills` → `storage/memory/skills/`
- `history` → `storage/events/` および `storage/memory/indexes/`
- `runtime candidate` → `storage/runtime/` 配下の候補領域

v0.1 では、`runtime candidate` の正本は永続記憶ではなく一時領域に置く。

---

## 8. 最小チェックリスト

保存前に最低限確認する項目は以下。

### profile

- 長期に効くか
- 応答または安全判断を変えるか
- 一時依頼ではないか
- source EventId を持てるか

### skills

- 再利用可能か
- 手順と検証がセットで書けるか
- 単発ログではないか
- 適用禁止条件を書けるか

### history

- 事実として追跡可能か
- root / parent 関係が保たれているか
- skill や artifact への逆引きがあるか

### runtime candidate

- いま本保存するには早すぎるか
- 捨てるには惜しいか
- 再評価の理由が書けるか

---

## 9. 一文で言うと

`profile` は長期制約、  
`skills` は再利用手順、  
`history` は過去事実、  
`runtime candidate` は昇格待ち、  
として扱う。
