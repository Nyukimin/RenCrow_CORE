# Hobby Graph / Mio Topic 仕様

## 目的

RenCrow は、れんの趣味領域を Mio が会話・提案・雑談・IdleChat の話題に使える知識グラフとして保持する。

対象は映画だけではなく、音楽、小説、漫画、アニメ、演劇、ビデオゲーム、ボードゲームなどを含む。

この仕様は `49_Movie_Graph_Mio_Topic仕様.md` の上位仕様である。映画 DB は趣味 DB の一カテゴリだが、映画.com カタログを起点に自動成長する特殊カテゴリとして扱う。

## 基本原則

- 趣味 DB は単なるコレクション一覧ではなく、Mio の話題生成材料である。
- 作品、作者、出演者、制作会社、シリーズ、ジャンル、鑑賞・読了・プレイ履歴、興味メモ、推薦候補を関係として保存する。
- 外部カタログ事実と、れん個人の履歴・興味・評価を混同しない。
- 「見た」「読んだ」「聴いた」「遊んだ」「気になる」は作品属性ではなく、れん個人の interaction event として扱う。
- 未解決タイトルや表記揺れは捨てず、あとで正規作品 ID へ解決する。
- Mio が話題に出すときは、根拠となる履歴、人物、作品、関係を辿れる状態にする。

## 対象カテゴリ

| category | 例 | 主な interaction |
| --- | --- | --- |
| `movie` | 映画 | `watched` |
| `music` | アーティスト、アルバム、曲、ライブ | `listened`, `liked`, `attended` |
| `novel` | 小説、作家、シリーズ | `read`, `interested` |
| `manga` | 漫画、作者、シリーズ | `read`, `interested` |
| `anime` | TV アニメ、劇場アニメ、OVA | `watched`, `interested` |
| `theater` | 演劇、舞台、ミュージカル、落語、ライブビューイング | `watched`, `attended` |
| `video_game` | ビデオゲーム、シリーズ、スタジオ | `played`, `cleared`, `interested` |
| `board_game` | ボードゲーム、TRPG、カードゲーム | `played`, `owned`, `interested` |

カテゴリは固定 enum として始めるが、将来 `drama`、`art`、`podcast` などを追加できる設計にする。

## 映画カテゴリとの違い

映画:

- 映画.com の `movies` / `people` / `movie_people` を外部カタログ事実として持つ。
- バックグラウンド agent が映画.com sitemap、人物リンク、関連作から自動成長できる。
- `49_Movie_Graph_Mio_Topic仕様.md` の詳細仕様に従う。

映画以外:

- 基本はれんの興味・履歴として登録する。
- 自動収集は、れんが登録した作品・人物・シリーズを起点に限定する。
- 広域クロールや無差別なカタログ増殖は行わない。
- 公式サイト、Wikipedia 系、出版社・配信・販売ページ、手元メモ、ユーザー入力など、カテゴリごとの取得元を使う。
- 未確認の外部情報は候補として保持し、Mio が断定的に話題化しない。

## 共通データモデル

### Hobby Items

作品、人物、団体、シリーズ、イベントなど、趣味グラフ上のノードを表す。

```sql
CREATE TABLE IF NOT EXISTS hobby_items (
  item_id TEXT PRIMARY KEY,
  category TEXT NOT NULL,
  item_type TEXT NOT NULL,
  title TEXT NOT NULL,
  normalized_title TEXT NOT NULL,
  subtitle TEXT,
  canonical_source TEXT,
  canonical_url TEXT,
  external_ids_json TEXT NOT NULL DEFAULT '{}',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

代表的な `item_type`:

| category | item_type |
| --- | --- |
| `music` | `artist`, `album`, `track`, `live_event`, `label` |
| `novel` | `work`, `author`, `series`, `publisher` |
| `manga` | `work`, `creator`, `series`, `magazine`, `publisher` |
| `anime` | `work`, `person`, `studio`, `series`, `season` |
| `theater` | `production`, `person`, `company`, `venue`, `performance` |
| `video_game` | `game`, `creator`, `studio`, `publisher`, `series`, `platform` |
| `board_game` | `game`, `designer`, `publisher`, `mechanic`, `series` |

### Hobby Relations

趣味 item 同士の関係を保存する。

```sql
CREATE TABLE IF NOT EXISTS hobby_relations (
  relation_id TEXT PRIMARY KEY,
  from_item_id TEXT NOT NULL,
  to_item_id TEXT NOT NULL,
  relation_type TEXT NOT NULL,
  source TEXT NOT NULL,
  evidence_url TEXT,
  evidence_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

代表的な `relation_type`:

| relation_type | 例 |
| --- | --- |
| `created_by` | 小説 -> 作家、漫画 -> 作者、ゲーム -> 開発者 |
| `performed_by` | 音楽曲 -> アーティスト、舞台 -> 俳優 |
| `directed_by` | アニメ・演劇・ゲーム -> 監督 |
| `published_by` | 小説・漫画・ゲーム・ボードゲーム -> 出版社 |
| `developed_by` | ゲーム -> 開発会社 |
| `part_of_series` | 作品 -> シリーズ |
| `adapted_from` | アニメ -> 漫画、小説 -> 原作 |
| `same_creator_as` | 同一作者・同一スタジオ由来 |
| `similar_theme` | テーマが近い |
| `recommended_after` | これを好むなら次候補 |

### Hobby Interactions

れん個人の行動・関心をイベントとして保存する。

```sql
CREATE TABLE IF NOT EXISTS hobby_interactions (
  interaction_id TEXT PRIMARY KEY,
  item_id TEXT,
  category TEXT NOT NULL,
  interaction_type TEXT NOT NULL,
  original_title TEXT NOT NULL,
  occurred_at TEXT,
  source TEXT NOT NULL,
  source_batch_id TEXT,
  rating REAL,
  note TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

代表的な `interaction_type`:

| interaction_type | 意味 |
| --- | --- |
| `watched` | 見た |
| `read` | 読んだ |
| `listened` | 聴いた |
| `played` | 遊んだ |
| `cleared` | クリアした |
| `attended` | 現地・配信・中継で参加した |
| `owned` | 所有している |
| `interested` | 気になっている |
| `dropped` | 途中でやめた |
| `liked` | 好きだと明示した |
| `disliked` | 合わないと明示した |

同じ作品への複数回 interaction を許可する。

### Title Observations

item に解決できない入力を残す。

```sql
CREATE TABLE IF NOT EXISTS hobby_title_observations (
  observation_id TEXT PRIMARY KEY,
  category TEXT NOT NULL,
  original_title TEXT NOT NULL,
  normalized_title TEXT NOT NULL,
  source TEXT NOT NULL,
  source_batch_id TEXT,
  status TEXT NOT NULL DEFAULT 'unresolved',
  resolved_item_id TEXT,
  resolution_note TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at TEXT
);
```

## 嗜好シグナル

Mio は interaction と graph から嗜好シグナルを生成する。

```sql
CREATE TABLE IF NOT EXISTS hobby_preference_signals (
  signal_id TEXT PRIMARY KEY,
  category TEXT,
  signal_type TEXT NOT NULL,
  target_item_id TEXT,
  target_label TEXT NOT NULL,
  weight REAL NOT NULL DEFAULT 1.0,
  evidence_json TEXT NOT NULL,
  generated_by TEXT NOT NULL,
  generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

代表的な `signal_type`:

| signal_type | 例 |
| --- | --- |
| `creator_affinity` | よく読む作家、よく見る監督、好きな作曲家 |
| `performer_affinity` | よく聴くアーティスト、よく見る俳優・声優 |
| `series_affinity` | 追っているシリーズ |
| `genre_affinity` | SF、ミステリ、青春、戦略ゲームなど |
| `format_affinity` | ライブ、舞台挨拶、長編、短編、協力ゲームなど |
| `theme_affinity` | 記憶、法廷、都市、旅、成長、政治など |
| `mechanic_affinity` | ボードゲームやゲームの好きな仕組み |
| `era_affinity` | 90年代漫画、2020年代邦画など |

嗜好シグナルは確定事実ではなく推定である。Mio は「たぶん」「最近多い」など、適切な距離感で使う。

## Mio 話題候補

```sql
CREATE TABLE IF NOT EXISTS hobby_topic_candidates (
  candidate_id TEXT PRIMARY KEY,
  category TEXT,
  topic_type TEXT NOT NULL,
  target_item_id TEXT,
  title TEXT NOT NULL,
  reason TEXT NOT NULL,
  evidence_json TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'candidate',
  generated_by TEXT NOT NULL,
  generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  used_at TEXT
);
```

代表的な `topic_type`:

| topic_type | 例 |
| --- | --- |
| `followup` | 見た・読んだ・遊んだ作品から広げる |
| `creator_pattern` | 同じ作者・監督・作曲家・デザイナーが多い |
| `performer_pattern` | 同じ俳優・声優・アーティストが多い |
| `cross_media_bridge` | 漫画からアニメ、ゲームから小説など |
| `untried_recommendation` | 未視聴・未読・未プレイ候補 |
| `contrast` | 好みと少し違うが話題になりそうな候補 |
| `memory_hook` | 過去の会話や体験と接続できる話題 |

## バックグラウンド収集

映画以外のバックグラウンド収集は、れんの登録・履歴・興味・Mio 話題候補を起点にする。

```sql
CREATE TABLE IF NOT EXISTS hobby_collection_runs (
  run_id TEXT PRIMARY KEY,
  category TEXT,
  reason TEXT NOT NULL,
  trigger_source TEXT NOT NULL,
  trigger_id TEXT,
  started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at TEXT,
  status TEXT NOT NULL DEFAULT 'running',
  summary TEXT
);

CREATE TABLE IF NOT EXISTS hobby_collection_targets (
  run_id TEXT NOT NULL,
  target_url TEXT NOT NULL,
  target_kind TEXT NOT NULL,
  target_id TEXT,
  reason TEXT NOT NULL,
  parent_kind TEXT,
  parent_id TEXT,
  status TEXT NOT NULL DEFAULT 'pending',
  fetched_at TEXT,
  error TEXT,
  PRIMARY KEY(run_id, target_url)
);
```

代表的な `reason`:

| reason | 意味 |
| --- | --- |
| `interaction_followup` | 登録済み interaction から作者・シリーズ・関連作をたどる |
| `creator_affinity_expand` | よく出る作者・監督・アーティストから広げる |
| `series_expand` | 追っているシリーズを補完する |
| `unresolved_title_resolution` | 未解決タイトルを正規 item に解決する |
| `topic_candidate_evidence` | Mio 話題候補の根拠を補強する |
| `manual_interest_expand` | れんが明示した興味範囲から広げる |

禁止:

- 映画以外で、ユーザー起点なしに広域カタログを無差別に増やす。
- 取得理由と親子関係を残さずにデータだけ増やす。
- robots / terms / API 制約を回避する。
- 未検証の候補を Mio が確定情報として話す。

## カテゴリ別の登録方針

### Music

主な item:

- `artist`
- `album`
- `track`
- `live_event`
- `label`

主な relations:

- `performed_by`
- `included_in_album`
- `composed_by`
- `part_of_series`
- `similar_theme`

基本は、れんが聴いた曲、好きなアーティスト、参加したライブから登録する。

### Novel

主な item:

- `work`
- `author`
- `series`
- `publisher`

基本は、読了、積読、気になる作家、シリーズから登録する。

### Manga

主な item:

- `work`
- `creator`
- `series`
- `magazine`
- `publisher`

基本は、読んだ作品、追っているシリーズ、作者から登録する。

### Anime

主な item:

- `work`
- `season`
- `person`
- `studio`
- `series`

基本は、見た作品、原作との関係、監督・脚本・スタジオ・声優を話題化できる形で登録する。

### Theater

主な item:

- `production`
- `performance`
- `person`
- `company`
- `venue`

基本は、見た舞台、配信、中継、出演者、劇団、会場から登録する。

### Video Game

主な item:

- `game`
- `studio`
- `publisher`
- `creator`
- `series`
- `platform`

基本は、遊んだ、クリアした、積んでいる、気になるゲームから登録する。

### Board Game

主な item:

- `game`
- `designer`
- `publisher`
- `mechanic`
- `series`

基本は、遊んだ、所有している、気になる、好きなメカニクスから登録する。

## Viewer 表示

趣味 DB Viewer では、最低限次を表示する。

- カテゴリ切替
- item 一覧
- interaction バッジ
- 関連人物・作者・シリーズ
- 未解決タイトル
- Mio 話題候補
- 嗜好シグナル
- 収集 run と target の理由

映画カテゴリでは `49_Movie_Graph_Mio_Topic仕様.md` の Movie Database 表示を優先し、将来 Hobby DB Viewer に統合してよい。

## Mio での利用

Mio には、次のような話題素材として渡す。

```text
topic: 最近よく触れている作家・監督・アーティスト
category: manga
evidence:
  - interaction: read
  - item: 作品A
  - creator: 作者B
signal:
  - creator_affinity 作者B weight=0.82
talk_hint:
  - 「最近この作者さんの作品が続いてるね」
candidate:
  - 未読の関連作C
```

Mio は、れんの履歴に基づく話題と外部カタログ由来の一般情報を区別して話す。

## 実装順序

1. `movie_watch_events` 相当を一般化した `hobby_interactions` を追加する。
2. `hobby_items` / `hobby_relations` / `hobby_title_observations` を追加する。
3. 映画カテゴリを `movie_*` 専用テーブルと `hobby_*` 横断テーブルのどちらからも参照できるようにする。
4. 手動登録 API / CLI / Viewer を作る。
5. Mio 話題候補生成を `hobby_topic_candidates` に保存する。
6. バックグラウンド収集を `hobby_collection_runs` / `hobby_collection_targets` に接続する。
7. 映画以外のカテゴリ別 importer を必要に応じて追加する。

## 禁止事項

- 趣味カテゴリごとに完全に別設計の DB を乱立させる。
- 外部カタログ事実とれん個人の履歴を同じ row に混ぜる。
- `seen` / `read` / `played` のような boolean カラムだけで履歴を表現する。
- 自動収集を件数目標だけで実行する。
- Mio が根拠のない推薦を断定する。
- 未解決入力や表記揺れを保存せずに捨てる。

## `49_Movie_Graph_Mio_Topic仕様.md` との関係

- `50_Hobby_Graph_Mio_Topic仕様.md` は横断仕様である。
- `49_Movie_Graph_Mio_Topic仕様.md` は映画カテゴリの詳細仕様である。
- 映画は自動成長 DB として扱う。
- 映画以外は、れんの興味登録を起点にした管理対象 DB として扱う。
- Mio の話題生成では、映画と他カテゴリを横断してよい。
