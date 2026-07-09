# Movie Graph / Mio Topic 仕様

## 正本での位置付け

この仕様は、`docs/01_正本仕様/実装仕様.md` の **検索システムDB確定方針** における Domain Graph DB の Movie ドメイン詳細仕様である。

Movie Graph は、検索 cache、Source Registry staging、汎用 KB、Qdrant、DuckDB の代替ではない。映画.com などの外部ソースから得た作品・人物・関係事実を、検証済みの外部世界カタログとして保持する Domain Graph DB である。

検索・Web Gather・RSS・API などから得た候補は、まず L1 SQLite の cache / staging に置き、validation 後に Movie Graph へ promote する。Movie Graph から Qdrant へ同期する場合は、意味検索用の要約・説明文だけを対象にし、関係 edge の正本は Movie Graph 側に残す。

「見た」「好き」「話題にしたい」は外部カタログ事実ではなく、ユーザー固有の event / preference signal として扱う。

## 目的

映画データベースは単なる作品台帳ではなく、Mio がれんへ自然に話題を提供するための知識グラフとして扱う。

この仕様では、映画.com 由来の外部カタログ事実、れんの鑑賞履歴、嗜好シグナル、関連候補、バックグラウンド収集理由を分離して保持する。

## 基本原則

- `movies` / `people` / `movie_people` は映画.com 由来の外部カタログ事実である。
- 「見た」は映画そのものの属性ではなく、れん個人の鑑賞イベントである。
- Mio が使う正本は「作品を見たか」だけではなく、「そこから何を話題にできるか」である。
- バックグラウンド収集は件数増やしではなく、Mio の話題材料を増やす目的で実行する。
- 取得理由、取得元、根拠 URL、鑑賞履歴との距離を残さない収集は、Mio 用の知識として不十分である。
- 未解決タイトルは捨てず、あとで映画 ID へ解決できる候補として保持する。

## 現行カタログ

現行の映画.com カタログは次を持つ。

| table | 役割 |
| --- | --- |
| `movies` | 映画.com 作品ページ由来の作品事実 |
| `people` | 映画.com 人物ページ由来の人物事実 |
| `movie_people` | 映画と人物の関係。出演、監督、スタッフ、人物略歴、出演作など |
| `fetch_log` | 取得 URL、状態、取得時刻、エラー |

この層は外部事実のため、ユーザー固有の状態を直接混ぜない。

## 鑑賞履歴

れんが見た映画は、作品カラムではなく鑑賞イベントとして保存する。

推奨 schema:

```sql
CREATE TABLE IF NOT EXISTS movie_watch_events (
  event_id TEXT PRIMARY KEY,
  movie_id TEXT,
  original_title TEXT NOT NULL,
  watched_at TEXT,
  source TEXT NOT NULL,
  source_batch_id TEXT,
  note TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

意味:

| column | 意味 |
| --- | --- |
| `event_id` | 鑑賞イベント ID |
| `movie_id` | 解決済みの場合の映画.com movie_id |
| `original_title` | ユーザーが渡した元タイトル。表記揺れや省略を残す |
| `watched_at` | 実際に見た日。不明なら入力日や `NULL` を許容する |
| `source` | `user_list` / `manual` / `import` など |
| `source_batch_id` | 本日リストなど、まとめて取り込んだ単位 |
| `note` | 補足 |
| `created_at` | RenCrow が記録した時刻 |

同じ映画を複数回見た場合も、イベントを複数持てるようにする。

## 未解決タイトル

ユーザーが渡したタイトルが映画.com の movie_id に解決できない場合も、失敗として捨てない。

推奨 schema:

```sql
CREATE TABLE IF NOT EXISTS movie_title_observations (
  observation_id TEXT PRIMARY KEY,
  original_title TEXT NOT NULL,
  normalized_title TEXT NOT NULL,
  source TEXT NOT NULL,
  source_batch_id TEXT,
  status TEXT NOT NULL DEFAULT 'unresolved',
  resolved_movie_id TEXT,
  resolution_note TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  resolved_at TEXT
);
```

状態:

| status | 意味 |
| --- | --- |
| `unresolved` | まだ movie_id がない |
| `candidate` | 候補はあるが未確定 |
| `resolved` | movie_id に確定済み |
| `rejected` | 別作品や誤認として除外 |

## 嗜好シグナル

Mio は鑑賞履歴と映画グラフから、話題生成に使える嗜好シグナルを生成する。

推奨 schema:

```sql
CREATE TABLE IF NOT EXISTS movie_preference_signals (
  signal_id TEXT PRIMARY KEY,
  signal_type TEXT NOT NULL,
  target_id TEXT,
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
| `actor_affinity` | よく見ている俳優 |
| `director_affinity` | よく見ている監督 |
| `series_affinity` | コナン、ガンダム、鬼滅などシリーズ傾向 |
| `format_affinity` | MX4D、字幕、吹替、舞台挨拶中継など |
| `era_affinity` | 2020年代邦画、2010年代洋画など |
| `theme_affinity` | ミステリ、法廷、医療、青春、SF など |

`evidence_json` には、根拠になった `movie_watch_events`、`movies`、`people`、`movie_people` の ID を保存する。

## Mio 話題候補

Mio が会話に出せる候補は、単なるおすすめ作品ではなく、話題化できる根拠と一緒に保存する。

推奨 schema:

```sql
CREATE TABLE IF NOT EXISTS movie_topic_candidates (
  candidate_id TEXT PRIMARY KEY,
  topic_type TEXT NOT NULL,
  target_movie_id TEXT,
  target_person_id TEXT,
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
| `watched_followup` | 見た作品から俳優や監督をたどる |
| `person_pattern` | 同じ俳優・監督が複数回出ている |
| `unwatched_recommendation` | 未見候補として話題化する |
| `series_continuation` | シリーズ続編や関連作 |
| `contrast` | 見た作品と対照的な作品 |
| `trivia` | 人物略歴や制作関係からの小話 |

`status`:

| status | 意味 |
| --- | --- |
| `candidate` | まだ会話に出していない |
| `used` | Mio が会話に出した |
| `dismissed` | 話題として弱い、またはユーザーに合わない |
| `blocked` | 根拠不足、未検証、取得失敗 |

## バックグラウンド収集

バックグラウンド収集は、次のどれかの理由を必ず持つ。

| reason | 意味 |
| --- | --- |
| `watch_event_followup` | 見た映画から人物・関連作をたどる |
| `person_affinity_expand` | よく見る俳優・監督から出演作・監督作を広げる |
| `series_expand` | よく見るシリーズの関連作を広げる |
| `unresolved_title_resolution` | 未解決タイトルを movie_id に解決する |
| `topic_candidate_evidence` | Mio が話題に出す候補の根拠を補強する |
| `catalog_backfill` | カタログ欠落を補う |

推奨 schema:

```sql
CREATE TABLE IF NOT EXISTS movie_collection_runs (
  run_id TEXT PRIMARY KEY,
  reason TEXT NOT NULL,
  trigger_source TEXT NOT NULL,
  trigger_id TEXT,
  started_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  finished_at TEXT,
  status TEXT NOT NULL DEFAULT 'running',
  summary TEXT
);

CREATE TABLE IF NOT EXISTS movie_collection_targets (
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

バックグラウンド agent は、単に `missing link` を取るのではなく、次を説明できる target を作る。

```text
れんが見た映画 A
  -> 出演者 B
  -> B の出演作 C
  -> C は未視聴候補
  -> Mio が「この俳優さん、別作品ではこういう役もある」と話題にできる
```

## 推奨取得順

通常の優先順位:

1. `movie_watch_events` に紐づく未取得映画
2. 見た映画の出演者・監督・脚本
3. よく出てくる人物の未取得出演作
4. 未解決タイトルの解決
5. Mio 話題候補に必要な根拠補強
6. sitemap 起点の広域カタログ補完

sitemap 起点の広域取得は有効だが、Mio 用の価値が薄くなりやすいため、`catalog_backfill` として理由を分ける。

## Viewer 表示

Viewer の Movie Database では、最低限次を表示する。

- 映画一覧の「見た」バッジ
- 映画詳細の鑑賞履歴
- 人物詳細の「れんが見た作品に何本出ているか」
- Mio 話題候補の根拠
- 未解決タイトル一覧

「見た」は UI 表現であり、DB の正体は `movie_watch_events` である。

## Mio での利用

Mio は次の形式で話題材料を受け取る。

```text
topic: 最近よく見ている俳優
evidence:
  - watched_movie: 国宝
  - person: 吉沢亮
  - relation: 出演
candidate:
  - まだ見ていない関連作
talk_hint:
  - 「この俳優さん、最近れんさんの鑑賞履歴に何度か出てきてるね」
```

Mio は映画 DB から取得した事実と、れんの鑑賞履歴を混同してはいけない。

## 禁止事項

- `movies.seen` だけで鑑賞履歴を表現する。
- 映画.com 由来の外部事実にユーザー固有状態を混ぜる。
- 再取得で消える可能性がある場所に嗜好情報を保存する。
- 未解決タイトルを失敗として捨てる。
- バックグラウンド収集で、取得理由と親子関係を残さない。
- Mio が根拠のないおすすめを確定口調で出す。

## 実装メモ

- 現行の `RenCrow_Tools/tools/eiga_catalog/eiga_catalog.py` は `movies` へ `INSERT OR REPLACE` するため、ユーザー固有状態は別テーブルに保持する。
- `movie_watch_events` / `movie_title_observations` / `movie_preference_signals` / `movie_topic_candidates` は、既存カタログ DB に追加してよい。
- 将来、Source Registry / Memory Layers と接続する場合も、外部カタログ事実、ユーザー鑑賞履歴、Mio 話題候補の境界は維持する。
