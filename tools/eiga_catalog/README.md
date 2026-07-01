# Eiga Catalog

映画.com の公開ページから、映画・人物・映画↔人物リンクを取得するローカル補助ツール。

## 位置づけ

- RenCrow 本体 runtime には組み込まない。
- 取得結果は SQLite / JSONL に保存する。
- robots.txt を確認し、許可された `/movie/{id}/`、`/person/{id}/`、sitemap を使う。
- `/search`、`/global_search`、`/person/*/article/`、`/movie/*/news/` など robots.txt で禁止された経路は使わない。
- 全量取得は時間がかかるため、必ず `--max-pages`、`--delay`、再開可能な出力DBを使う。

## 取得できるもの

- 映画: `movie_id`, `title`, `url`, `synopsis`
- 映画のキャスト: `movie_people` edge (`source=movie_cast`)
- 映画のスタッフ: `movie_people` edge (`source=movie_staff`)
- 人物: `person_id`, `name`, `url`, `profile`, `biography`
- 人物の略歴内映画リンク: `movie_people` edge (`source=person_biography`)
- 人物の関連作品ページ `/person/{id}/movie/`: `movie_people` edge (`source=person_filmography`)

## 例

単一映画からキャスト・スタッフを取り、リンク先人物も追う:

```bash
python3 tools/eiga_catalog/eiga_catalog.py \
  --seed-url https://eiga.com/movie/57573/ \
  --follow-links \
  --include-person-filmography \
  --max-pages 20 \
  --delay 2 \
  --output-dir tmp/eiga_catalog_margin_call
```

sitemap から候補を確認するだけ:

```bash
python3 tools/eiga_catalog/eiga_catalog.py \
  --all-from-sitemap \
  --max-sitemaps 1 \
  --dry-run
```

sitemap 起点の本取得:

```bash
python3 tools/eiga_catalog/eiga_catalog.py \
  --all-from-sitemap \
  --max-pages 100 \
  --delay 2 \
  --include-person-filmography \
  --output-dir tmp/eiga_catalog
```

## 出力

- `eiga_catalog.sqlite`
- `eiga_catalog.jsonl`

SQLite tables:

- `movies`
- `people`
- `movie_people`
- `fetch_log`

`movie_people` に `movie_id` と `person_id` の両方を保存するため、映画から人物、人物から映画の双方向リンクを引ける。

## 注意

- 大量取得はサイト負荷になるため、低頻度で実行する。
- 取得済みデータを RenCrow memory へ直接 promoted 扱いで入れない。
- 本文は出典URL付きの外部カタログとして保持し、必要なら別工程で検証・要約・取り込みする。
