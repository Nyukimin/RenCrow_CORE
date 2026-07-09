# Domain Graph DB 経路実装仕様

## 目的

Domain Graph DB は、Movie / 漫画 / 音楽 / 小説 / ゲームなど、外部世界の作品・人物・組織・シリーズ・関係事実を保持する正本DBである。

検索 cache、Source Registry staging、汎用 KB、Qdrant、DuckDB と混同しない。検索や外部 fetch から得た候補は、必ず staging / validation を経由してから Domain Graph DB へ昇格する。

## 経路

### 1. 外部情報取得経路

```text
Web Gather / Source Registry / API / RSS / manual import
  -> L1 SQLite cache
  -> L1 staging
  -> validate
  -> Domain Graph DB assertions / relations
```

- L1 staging は外部入力の pending / validated / rejected を管理する hot store である。
- pending staging を Domain Graph DB へ promote してはいけない。
- validated staging のうち、外部世界の関係事実として採用するものだけを Domain Graph DB assertion へ promote する。
- Google Search、RSS、Web本文、Source Registry staging を Qdrant へ直接 upsert してはいけない。

### 2. ユーザー行動経路

```text
ユーザー発話 / Viewer 操作 / CLI import
  -> interaction event
  -> title observation
  -> preference signal
  -> topic candidate
```

- 「見た」「読んだ」「聴いた」「遊んだ」「好き」「苦手」は外部カタログ事実ではない。
- user event / preference signal は、作品・人物カタログ行の属性に混ぜない。
- 未解決タイトルは observation として残し、後で正規 item へ解決する。

### 3. 未解決解決経路

```text
title_observations unresolved/candidate
  -> Domain Graph DB items / works と照合
  -> resolved_item_id 更新
  -> 関連 interaction / signal を補正
```

- 解決できない入力を失敗として捨てない。
- 解決結果には resolution_note と resolved_at を残す。

### 4. 話題生成経路

```text
Domain Graph DB
  + user interactions
  + preference signals
  -> topic candidates
  -> Mio / IdleChat / Viewer
```

- Mio が外部DB全体を直接漁るのではなく、topic candidate を生成して渡す。
- topic candidate は evidence_json を必須にする。
- 根拠のないおすすめを確定口調で出してはいけない。

### 5. 意味検索同期経路

```text
Domain Graph DB summaries
  -> Qdrant
  -> RecallPack / semantic search
```

- Qdrant は検索用 index であり、正本ではない。
- 関係 edge と assertion の正本は Domain Graph DB に残す。
- Qdrant へ同期するのは、作品・人物・関係の要約や説明など、意味検索に使う文書表現だけである。

## 最小実装単位

1. Domain Graph assertion の保存 schema
2. validated L1 staging から Domain Graph assertion への promote
3. Source Registry Viewer API / client から `target=domain_graph` を指定できる入口
4. pending staging を promote できない境界テスト
5. promoted assertion が source URL / source ID / confidence / validation status / evidence を保持するテスト

## 次段実装

Domain Graph assertion を保存した後の一覧・検索 API は `53_Domain_Graph_Assertion一覧検索API実装仕様.md` を正とする。

## 初期 schema

最小実装では、共通 assertion table を L1 SQLite に追加する。

```sql
CREATE TABLE IF NOT EXISTS domain_graph_assertion (
  assertion_id TEXT PRIMARY KEY,
  staging_id TEXT NOT NULL UNIQUE,
  domain TEXT NOT NULL,
  entity_type TEXT NOT NULL,
  entity_id TEXT,
  relation_type TEXT,
  source_id TEXT NOT NULL,
  source_url TEXT NOT NULL DEFAULT '',
  raw_hash TEXT NOT NULL,
  summary TEXT NOT NULL DEFAULT '',
  confidence REAL NOT NULL DEFAULT 0.5,
  validation_status TEXT NOT NULL,
  evidence_json TEXT NOT NULL DEFAULT '{}',
  created_at TIMESTAMP NOT NULL,
  updated_at TIMESTAMP NOT NULL
);
```

この table は最終的な graph DB 製品ではなく、Domain Graph DB の正本境界を崩さないための最小共通保存先である。Movie 固有の `movies` / `people` / `movie_people` などは、Movie ドメイン詳細 DB として並存してよい。
