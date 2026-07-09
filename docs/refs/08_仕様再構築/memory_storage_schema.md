# memory_storage_schema.md

## 目的

この文書は、Mio の記憶システムで扱う保存スキーマを定義する。

ここでの対象は「思い出すための記憶」である。
原本保存や監査ログは対象外とし、それらは別系統で扱う。

このスキーマの目的は、完全保存ではなく、継続利用の中で

- Mio の人格が深まること
- れんとの関係性がズレずに育つこと
- 作業の再利用性が上がること
- 参照知識が関係つきで引けること

を支えることにある。

---

## 1. 共通原則

### 1.1 1レコード1役割

1つの記憶レコードには、1つの主目的だけを持たせる。

- 関係性記憶
- 本人格記憶
- 継続案件記憶
- 作業最適化記憶
- 参照知識

を混在させない。

### 1.2 原文全文を記憶へ入れない

記憶には、原文全文ではなく、想起に必要な断片・要約・リンクだけを持つ。
原文は source_preservation 側へ残す。

### 1.3 Chat が最終支配者

- Chat は全記憶を read できる
- Worker は作業系記憶の候補を作れる
- Coder は作業系記憶の候補を作れる
- 記憶への保存確定は Chat が行う

### 1.4 想起のためのメタデータを最初から持つ

記憶レコードには、保存内容だけでなく、想起判断に必要な情報を持たせる。

- 重要度
- 直近参照日
- 参照回数
- 鮮度
- 関連トピック
- 関連 Actor
- 昇格候補フラグ

---

## 2. 記憶層ごとの保存対象

Mio では、記憶を次の5層で扱う。

1. Reference Memory
2. Relationship Memory
3. Identity Memory
4. Long-Term Project Memory
5. Work Optimization Memory

短期記憶は runtime state として扱い、ここでは永続スキーマの対象外とする。

---

## 3. 共通スキーマ

全記憶レコードは、次の共通フィールドを持つ。

```json
{
  "memory_id": "MEM-20260412-000021",
  "memory_type": "reference|relationship|identity|project|work_optimization",
  "title": "短い題名",
  "summary": "1〜3文の想起用要約",
  "keywords": ["タグ1", "タグ2"],
  "source_refs": [
    {
      "source_kind": "record|event|artifact|external",
      "source_id": "REC-20260412-000311"
    }
  ],
  "related_memory_ids": ["MEM-20260401-000004"],
  "actor_scope": ["chat"],
  "topic_scope": ["memory-architecture", "relationship"],
  "importance": 0.82,
  "recency_score": 0.74,
  "access_count": 3,
  "last_accessed_at": "2026-04-12T10:00:00+09:00",
  "created_at": "2026-04-10T09:00:00+09:00",
  "updated_at": "2026-04-12T10:00:00+09:00",
  "ttl_policy": "none|daily-review|weekly-review|monthly-review|decay",
  "promotion_state": "candidate|promoted|stable|archived",
  "status": "active|dormant|archived"
}
```

### フィールド補足

`memory_type`
記憶の主役割。混在禁止。

`summary`
想起時にまず読む断片。全文の代わりに使う。

`source_refs`
原本保存や Event、成果物への参照。原本そのものは持たない。

`actor_scope`
通常参照を許可する Actor。

`importance`
保存時や昇格時に決まる重み。0〜1。

`recency_score`
自動計算。時間経過で減衰してよい。

`promotion_state`
まだ候補か、昇格済みか、安定しているかを示す。

---

## 4. Reference Memory スキーマ

### 目的

外部知識、仕様、参照資産、コード構造、知識関係を引くための記憶。

### 形式

```json
{
  "memory_id": "MEM-20260412-REF-0001",
  "memory_type": "reference",
  "title": "GraphRAGのCommunity層の役割",
  "summary": "Community は局所検索を越えて全体俯瞰を支える要約層である。",
  "keywords": ["GraphRAG", "community", "reference"],
  "entity_refs": ["ENT-graph-rag", "ENT-community"],
  "community_refs": ["COM-knowledge-retrieval"],
  "relation_edges": [
    {
      "relation": "supports",
      "target_memory_id": "MEM-20260412-REF-0002"
    }
  ],
  "source_refs": [
    {
      "source_kind": "external",
      "source_id": "https://zenn.dev/okikusan/articles/0f8295e7ecaa19"
    }
  ],
  "actor_scope": ["chat", "worker", "coder"],
  "topic_scope": ["retrieval", "memory"],
  "importance": 0.68,
  "recency_score": 0.61,
  "access_count": 1,
  "last_accessed_at": "2026-04-12T10:00:00+09:00",
  "created_at": "2026-04-12T10:00:00+09:00",
  "updated_at": "2026-04-12T10:00:00+09:00",
  "ttl_policy": "monthly-review",
  "promotion_state": "stable",
  "status": "active"
}
```

### 補足

- 参照知識は relation_edges を持ってよい
- GraphRAG 的な entity / community / relation をここへ寄せる
- 本人格や関係性は入れない

---

## 5. Relationship Memory スキーマ

### 目的

れんと Mio の関係性を継続で深めるための記憶。

### 形式

```json
{
  "memory_id": "MEM-20260412-REL-0007",
  "memory_type": "relationship",
  "title": "れんは記憶を層で分ける設計を重視する",
  "summary": "れんは参照用DB、関係性記憶、長期記憶、短期記憶、作業最適化記憶を分けて扱う考えを強く持つ。",
  "keywords": ["relationship", "memory", "design-principle"],
  "relationship_kind": "preference|boundary|trust|conversation-style|shared-understanding",
  "emotional_weight": 0.55,
  "use_contexts": ["architecture-discussion", "memory-design"],
  "avoid_contexts": ["generic-coding-task"],
  "source_refs": [
    {
      "source_kind": "record",
      "source_id": "REC-20260412-CONV-0032"
    }
  ],
  "actor_scope": ["chat"],
  "topic_scope": ["relationship", "memory"],
  "importance": 0.91,
  "recency_score": 0.94,
  "access_count": 4,
  "last_accessed_at": "2026-04-12T13:00:00+09:00",
  "created_at": "2026-04-12T13:00:00+09:00",
  "updated_at": "2026-04-12T13:00:00+09:00",
  "ttl_policy": "none",
  "promotion_state": "stable",
  "status": "active"
}
```

### 補足

- Chat 専用を原則とする
- 好み、境界、通じやすい考え方、ズレやすい点を持つ
- Worker/Coder には直接渡さず、必要時に Chat が要約して渡す

---

## 6. Identity Memory スキーマ

### 目的

Mio の本人格を固定文ではなく、想起可能な人格断片として持つ。

### 形式

```json
{
  "memory_id": "MEM-20260412-ID-0003",
  "memory_type": "identity",
  "title": "Mio は関係性を大切にしつつ、事実優先で考える",
  "summary": "Mio は関係性を軽視しないが、判断では事実と妥当性を優先する。",
  "keywords": ["identity", "persona", "fact-priority"],
  "identity_axis": "tone|value|decision-style|relationship-stance|self-consistency",
  "activation_contexts": ["advice", "design-review", "correction"],
  "inhibition_contexts": ["simple-status-response"],
  "source_refs": [
    {
      "source_kind": "record",
      "source_id": "REC-20260412-CONV-0017"
    }
  ],
  "actor_scope": ["chat"],
  "topic_scope": ["persona", "identity"],
  "importance": 0.88,
  "recency_score": 0.73,
  "access_count": 2,
  "last_accessed_at": "2026-04-12T11:30:00+09:00",
  "created_at": "2026-04-12T11:30:00+09:00",
  "updated_at": "2026-04-12T11:30:00+09:00",
  "ttl_policy": "monthly-review",
  "promotion_state": "promoted",
  "status": "active"
}
```

### 補足

- Mio の本人格に効く断片だけを入れる
- 関係性記憶と混ぜない
- 直近で使いすぎた断片は想起で少し抑制してよい

---

## 7. Long-Term Project Memory スキーマ

### 目的

継続案件、設計判断、過去の重要な合意を保つ。

### 形式

```json
{
  "memory_id": "MEM-20260412-PRJ-0012",
  "memory_type": "project",
  "title": "RenCrow の中核は Chat / Worker / Coder の三層とする",
  "summary": "Chat は全記憶の統治者、Worker は役割人格つき作業者、Coder は作業用記憶のみの実装者とする。",
  "keywords": ["RenCrow", "architecture", "project-memory"],
  "project_name": "RenCrow",
  "decision_kind": "architecture|policy|constraint|roadmap",
  "decision_state": "proposed|adopted|deprecated",
  "related_artifacts": ["ART-20260412-ARCH-0001"],
  "source_refs": [
    {
      "source_kind": "event",
      "source_id": "EVT-20260412-000121"
    }
  ],
  "actor_scope": ["chat", "worker"],
  "topic_scope": ["rencrow", "architecture"],
  "importance": 0.95,
  "recency_score": 0.82,
  "access_count": 6,
  "last_accessed_at": "2026-04-12T14:00:00+09:00",
  "created_at": "2026-04-12T14:00:00+09:00",
  "updated_at": "2026-04-12T14:00:00+09:00",
  "ttl_policy": "monthly-review",
  "promotion_state": "stable",
  "status": "active"
}
```

### 補足

- 案件ごとの決定事項、制約、履歴を保持する
- Project Memory は事実と判断の層であり、感情の層ではない

---

## 8. Work Optimization Memory スキーマ

### 目的

作業効率と安全性を上げるための手順、罠、確認順を保持する。

### 形式

```json
{
  "memory_id": "MEM-20260412-WORK-0021",
  "memory_type": "work_optimization",
  "title": "Windows 共有環境では物理移動より非破壊手段を優先する",
  "summary": "venv や site-packages、モデル配置は Move-Item で移さず、まずジャンクションやコピー等の非破壊手段を優先する。",
  "keywords": ["work-optimization", "non-destructive", "windows"],
  "procedure_kind": "safety|recovery|verification|setup|repo-specific",
  "preconditions": ["Windows 環境", "共有依存あり"],
  "symptoms": ["配置変更が必要", "容量不足", "経路差し替え"],
  "recommended_steps": [
    "元と先の内容確認",
    "非破壊手段の検討",
    "削除前に両側確認"
  ],
  "do_not_apply_when": ["専用隔離環境で破壊的変更が許可済み"],
  "source_refs": [
    {
      "source_kind": "record",
      "source_id": "REC-20260402-CONV-0041"
    }
  ],
  "actor_scope": ["chat", "worker", "coder"],
  "topic_scope": ["operations", "safety"],
  "importance": 0.97,
  "recency_score": 0.79,
  "access_count": 5,
  "last_accessed_at": "2026-04-12T15:00:00+09:00",
  "created_at": "2026-04-02T12:00:00+09:00",
  "updated_at": "2026-04-12T15:00:00+09:00",
  "ttl_policy": "none",
  "promotion_state": "stable",
  "status": "active"
}
```

### 補足

- Worker/Coder が最もよく使う記憶層
- 手順、確認、禁止条件を持つ
- 「再利用可能な罠回避」に寄せる

---

## 9. 保存候補スキーマ

保存前は candidate として扱う。

```json
{
  "candidate_id": "MCAND-20260412-0009",
  "proposed_memory_type": "relationship|identity|project|work_optimization|reference",
  "proposed_by": "chat|worker|coder",
  "title": "候補題名",
  "summary": "候補要約",
  "reason": "なぜ保存価値があるか",
  "source_refs": [
    {
      "source_kind": "record",
      "source_id": "REC-20260412-CONV-0032"
    }
  ],
  "importance": 0.78,
  "first_seen_at": "2026-04-12T16:00:00+09:00",
  "review_state": "pending|accepted|rejected|merged"
}
```

### 候補から昇格する例

- 同種の内容が3回以上効いた
- 明示的な重要判断として合意された
- 安全性や作業効率に強く効いた
- Mio の本人格や関係性の軸として再利用された

---

## 10. 想起スキーマ

想起処理で返すのは、全文ではなく小さなパケットにする。

```json
{
  "recall_id": "RECALL-20260412-0011",
  "query_topic": "memory architecture",
  "actor": "chat",
  "selected_memories": [
    {
      "memory_id": "MEM-20260412-REL-0007",
      "summary": "れんは記憶の層分離を重視する",
      "recall_score": 0.93,
      "why_selected": ["relevance", "importance"]
    },
    {
      "memory_id": "MEM-20260412-PRJ-0012",
      "summary": "RenCrow は Chat / Worker / Coder の三層構成",
      "recall_score": 0.82,
      "why_selected": ["topic-match", "project-continuity"]
    }
  ],
  "suppressed_memories": [
    {
      "memory_id": "MEM-20260412-ID-0003",
      "reason": "recently-overused"
    }
  ],
  "created_at": "2026-04-12T16:05:00+09:00"
}
```

### 補足

- 1回の想起で大量に返さない
- 3〜5件程度を上限の目安にする
- 直近で使いすぎた人格断片は少し抑制してよい

---

## 11. 失効・減衰・昇格

### 11.1 減衰対象

- 一時的な project memory 候補
- 単発の relationship candidate
- 作業短期から昇格しなかった work candidate

### 11.2 原則無期限

- 安定化した relationship memory
- 安定化した identity memory
- 強い work optimization memory

### 11.3 月次見直し対象

- reference memory
- project memory
- 低頻度の identity memory

---

## 12. この文書の一文要約

Mio の記憶は、全文の倉庫ではなく、
関係性、人格、継続案件、作業手順、参照知識を役割別に分け、
原本保存を参照元として、少数断片を想起できるように整えた保存スキーマである。
