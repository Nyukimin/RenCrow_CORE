# Movie Watch Event 実装仕様

## 目的

`49_Movie_Graph_Mio_Topic仕様.md` のうち、最初の実装単位として「れんが見た映画」を映画DBへ登録し、Viewer と Mio 話題生成の入口にする。

## 対象

対象:

- ユーザーが渡した映画タイトルリストを「見た」履歴として保存する。
- 映画.com movie_id に解決できたタイトルは `movie_id` と紐付ける。
- 解決できないタイトルも失わず、未解決 title observation として保存する。
- Viewer の Movie Database で「見た」状態と鑑賞履歴を見えるようにする。
- 好きな俳優・人物を、カタログ本体とは別の嗜好シグナルとして保存する。

対象外:

- 嗜好シグナル自動生成。
- Mio の話題候補自動生成。
- 映画.com の追加検索クロール。
- 映画以外の Hobby Graph 実装。

## DB schema

### movie_watch_events

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
| `event_id` | 鑑賞イベント ID。batch と行番号とタイトルから安定生成する |
| `movie_id` | 解決済みの場合の映画.com movie_id |
| `original_title` | ユーザーが渡した元タイトル |
| `watched_at` | 見た日。不明なら取り込み日 |
| `source` | `user_list` など |
| `source_batch_id` | 本日リストなどの取り込み単位 |
| `note` | 補足 |
| `created_at` | RenCrow が保存した時刻 |

同じ作品を複数回見た場合を表現できるよう、boolean ではなく event として保存する。

### movie_title_observations

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
| `resolved` | movie_id へ解決済み |
| `candidate` | 候補が複数あり未確定 |
| `unresolved` | ローカルDBから候補を見つけられない |
| `rejected` | 誤認として除外 |

### movie_preference_signals

`49_Movie_Graph_Mio_Topic仕様.md` の推奨 schema を使う。

人物への手動「好き」設定は、次の値で保存する。

| column | 値 |
| --- | --- |
| `signal_type` | `actor_affinity` |
| `target_id` | 解決できた場合の `people.person_id` |
| `target_label` | ユーザーが渡した人物名 |
| `weight` | 既定 `1.0` |
| `generated_by` | 既定 `user` |
| `evidence_json` | 取り込み batch、元ラベル、解決状態、補足 |

`people` に `favorite` カラムは追加しない。映画.com 再取得で人物カタログが更新されても、好き設定が消えないようにする。

## タイトル解決

実装位置:

- `RenCrow_Tools/tools/eiga_catalog/eiga_catalog.py`

解決順:

1. `movies.title` の正規化一致。
2. `movie_people.movie_title` の正規化一致。
3. 一意な部分一致。
4. 複数候補なら `candidate`。
5. 候補なしなら `unresolved`。

正規化では、全角半角、空白、記号、上映形式 prefix、字幕・吹替・MX4D・AT・舞台挨拶中継などを吸収する。

## CLI

```bash
python3 /home/nyukimi/RenCrow/RenCrow_Tools/tools/eiga_catalog/eiga_catalog.py \
  --mark-watched-file tmp/movie_watch_today.txt \
  --watched-at 2026-06-03 \
  --watch-source user_list \
  --watch-batch-id user_movie_list_20260603 \
  --db tmp/eiga_catalog_smoke/eiga_catalog.sqlite \
  --jsonl tmp/eiga_catalog_smoke/eiga_catalog.jsonl
```

出力は JSON で、`input`、`events`、`resolved`、`candidate`、`unresolved` を返す。

好きな俳優・人物の登録:

```bash
python3 /home/nyukimi/RenCrow/RenCrow_Tools/tools/eiga_catalog/eiga_catalog.py \
  --mark-favorite-people-file tmp/favorite_people.txt \
  --favorite-batch-id favorite_people_20260604 \
  --favorite-generated-by user \
  --db tmp/eiga_catalog_smoke/eiga_catalog.sqlite \
  --jsonl tmp/eiga_catalog_smoke/eiga_catalog.jsonl
```

出力は JSON で、`input`、`signals`、`resolved`、`candidate`、`unresolved` を返す。

## Viewer API

`/viewer/movie-catalog?action=stats`

- `movie_watch_events` が存在する場合は件数を返す。
- `movie_title_observations` が存在する場合は件数を返す。

`/viewer/movie-catalog?action=movies`

各 movie item に次を追加する。

```json
{
  "watched": true,
  "watch_count": 1
}
```

`/viewer/movie-catalog?action=movie&id=...`

detail に次を追加する。

```json
{
  "watch_events": [
    {
      "event_id": "watch_xxx",
      "movie_id": "57573",
      "original_title": "マージン・コール",
      "watched_at": "2026-06-03",
      "source": "user_list",
      "source_batch_id": "user_movie_list_20260603"
    }
  ]
}
```

`/viewer/movie-catalog?action=person&id=...`

人物 detail では、その人物の関連映画のうち、れんが見た映画数を `watched_movie_count` として返す。各 link には `movie_watched` を返す。
人物に `actor_affinity` などの正の嗜好シグナルがある場合、`favorite: true` と `preference_count` を返す。

`/viewer/movie-catalog?action=movie&id=...`

映画 detail の人物 link には、嗜好シグナルがある人物について `person_favorite: true` を返す。

`/viewer/movie-catalog/preference`

人物の好き設定を更新する。

```json
{
  "kind": "person",
  "target_id": "92657",
  "target_label": "吉沢亮",
  "favorite": true,
  "signal_type": "actor_affinity",
  "generated_by": "viewer"
}
```

`favorite: true` なら `movie_preference_signals` へ追加する。`favorite: false` なら同じ人物の `actor_affinity` / `person_affinity` / `director_affinity` を削除する。
Viewer からの手動操作は `generated_by=viewer` とする。

## Viewer UI

Movie Database tab では次を表示する。

- 映画一覧の「見た」バッジ。
- 映画詳細の鑑賞履歴。
- 人物一覧の「見た映画」数。
- 人物一覧・人物詳細の「好き」バッジ。
- 人物一覧の「好き」列ではチェックボタンで好き設定をON/OFFできる。
- 人物詳細の関連映画 link に「見た」バッジ。
- 映画詳細の人物 link に「好き」バッジ。

## 成功条件

- 既存の映画.com カタログ取得を壊さない。
- `movies` に `seen` カラムを追加しない。
- 再取得の `INSERT OR REPLACE INTO movies` で鑑賞履歴が消えない。
- 今日渡された映画リストの全行が `movie_watch_events` に入る。
- 解決できないタイトルも `movie_title_observations` に残る。
- Viewer API で watched 状態が読める。
- 好きな俳優・人物は `movie_preference_signals` に入り、`people` の再取得で消えない。

## 検証

最低限:

```bash
python3 -m unittest tests.tools_eiga_catalog.test_eiga_catalog
GOCACHE=/tmp/rencrow-gocache go test ./internal/adapter/viewer -run 'TestHandleMovieCatalog' -count=1
node --test internal/adapter/viewer/viewer_static_contract_test.mjs
```

live 確認:

```bash
curl -sS 'http://127.0.0.1:18790/viewer/movie-catalog?action=stats'
curl -sS 'http://127.0.0.1:18790/viewer/movie-catalog?action=movies&q=国宝'
```
