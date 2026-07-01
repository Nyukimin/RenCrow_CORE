import importlib.util
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
MODULE = ROOT / "tools" / "eiga_catalog" / "eiga_catalog.py"
spec = importlib.util.spec_from_file_location("eiga_catalog", MODULE)
eiga_catalog = importlib.util.module_from_spec(spec)
assert spec.loader is not None
sys.modules["eiga_catalog"] = eiga_catalog
spec.loader.exec_module(eiga_catalog)


MOVIE_HTML = """
<html><head>
<script type="application/ld+json">[{
  "@context":"http://schema.org",
  "@type":"Movie",
  "name":"マージン・コール",
  "url":"https://eiga.com/movie/57573/",
  "description":"金融危機を描く社会派サスペンス。",
  "director":[{"@type":"Person","name":"J・C・チャンダー","url":"https://eiga.com/person/91505/"}],
  "actor":[{"@type":"Person","name":"ケビン・スペイシー","url":"https://eiga.com/person/30003/"}]
}]</script>
</head><body>
<h1 class="page-title">マージン・コール</h1>
<section id="staff-cast">
  <dl class="movie-staff">
    <dt>脚本</dt><dd><a href="/person/91505/">J・C・チャンダー</a></dd>
  </dl>
  <ul class="movie-cast">
    <li><a class="person" href="/person/67063/"><p><span>ポール・ベタニー</span></p></a></li>
  </ul>
</section>
</body></html>
"""


PERSON_HTML = """
<html><body>
<h1 class="page-title">ケビン・スペイシー</h1>
<div class="profile"><dl>
  <dt>英語表記</dt><dd>Kevin Spacey</dd>
  <dt>誕生日</dt><dd>1959年7月26日</dd>
  <dt>出身</dt><dd class="sns"><a>Instagram</a> <a>X(旧Twitter)</a></dd>
</dl></div>
<div class="txt-block">
  <p class="txt" style="text-indent:0;">「<a href="/movie/17531/">セブン</a>」で知られる俳優。</p>
</div>
</body></html>
"""


FILMOGRAPHY_HTML = """
<html><body>
<a href="/movie/17531/">
  <div><p class="label">出演</p></div>
  <p class="title">セブン</p>
</a>
<a href="/movie/58257/">
  <div><p class="label">製作総指揮</p></div>
  <p class="title">キャプテン・フィリップス</p>
</a>
</body></html>
"""


class EigaCatalogTest(unittest.TestCase):
    def test_parse_movie_extracts_synopsis_cast_staff(self):
        movie = eiga_catalog.parse_movie(MOVIE_HTML, "https://eiga.com/movie/57573/")
        self.assertEqual(movie.movie_id, "57573")
        self.assertEqual(movie.title, "マージン・コール")
        self.assertIn("金融危機", movie.synopsis)
        self.assertIn(("30003", "ケビン・スペイシー", "出演"), [(p.person_id, p.name, p.role) for p in movie.cast])
        self.assertIn(("91505", "J・C・チャンダー", "監督"), [(p.person_id, p.name, p.role) for p in movie.staff])
        self.assertIn(("91505", "J・C・チャンダー", "脚本"), [(p.person_id, p.name, p.role) for p in movie.staff])

    def test_parse_person_extracts_profile_biography_and_filmography(self):
        person = eiga_catalog.parse_person(PERSON_HTML, "https://eiga.com/person/30003/", FILMOGRAPHY_HTML)
        self.assertEqual(person.person_id, "30003")
        self.assertEqual(person.name, "ケビン・スペイシー")
        self.assertEqual(person.profile["英語表記"], "Kevin Spacey")
        self.assertNotIn("出身", person.profile)
        self.assertIn("知られる俳優", person.biography)
        self.assertIn(("17531", "セブン"), [(m.movie_id, m.title) for m in person.biography_movies])
        self.assertIn(("58257", "キャプテン・フィリップス", "製作総指揮"), [(m.movie_id, m.title, m.role) for m in person.filmography])

    def test_store_persists_bidirectional_link_edges(self):
        movie = eiga_catalog.parse_movie(MOVIE_HTML, "https://eiga.com/movie/57573/")
        person = eiga_catalog.parse_person(PERSON_HTML, "https://eiga.com/person/30003/", FILMOGRAPHY_HTML)
        with tempfile.TemporaryDirectory() as td:
            db = Path(td) / "catalog.sqlite"
            jsonl = Path(td) / "catalog.jsonl"
            store = eiga_catalog.EigaStore(db, jsonl)
            try:
                store.save_movie(movie)
                store.save_person(person)
            finally:
                store.close()
            conn = sqlite3.connect(db)
            rows = conn.execute("SELECT movie_id, person_id, role, source FROM movie_people ORDER BY source, role").fetchall()
            conn.close()
            self.assertIn(("57573", "30003", "出演", "movie_cast"), rows)
            self.assertIn(("17531", "30003", "出演", "person_filmography"), rows)
            self.assertTrue(jsonl.read_text(encoding="utf-8").count("\n") >= 2)

    def test_mark_watched_titles_keeps_user_events_separate_from_catalog(self):
        movie = eiga_catalog.parse_movie(MOVIE_HTML, "https://eiga.com/movie/57573/")
        with tempfile.TemporaryDirectory() as td:
            db = Path(td) / "catalog.sqlite"
            jsonl = Path(td) / "catalog.jsonl"
            store = eiga_catalog.EigaStore(db, jsonl)
            try:
                store.save_movie(movie)
            finally:
                store.close()

            stats = eiga_catalog.mark_watched_titles(
                db,
                ["（字）マージン・コール", "未登録映画"],
                "2026-06-03",
                "user_list",
                "batch_1",
            )
            self.assertEqual(stats["input"], 2)
            self.assertEqual(stats["events"], 2)
            self.assertEqual(stats["resolved"], 1)
            self.assertEqual(stats["unresolved"], 1)

            conn = sqlite3.connect(db)
            events = conn.execute("SELECT movie_id, original_title, watched_at, source_batch_id FROM movie_watch_events ORDER BY original_title").fetchall()
            observations = conn.execute("SELECT original_title, status, resolved_movie_id FROM movie_title_observations ORDER BY original_title").fetchall()
            movie_columns = [row[1] for row in conn.execute("PRAGMA table_info(movies)").fetchall()]
            conn.close()
            self.assertIn(("57573", "（字）マージン・コール", "2026-06-03", "batch_1"), events)
            self.assertIn((None, "未登録映画", "2026-06-03", "batch_1"), events)
            self.assertIn(("（字）マージン・コール", "resolved", "57573"), observations)
            self.assertIn(("未登録映画", "unresolved", None), observations)
            self.assertNotIn("seen", movie_columns)

    def test_mark_favorite_people_stores_actor_affinity_signal(self):
        person = eiga_catalog.parse_person(PERSON_HTML, "https://eiga.com/person/30003/", FILMOGRAPHY_HTML)
        with tempfile.TemporaryDirectory() as td:
            db = Path(td) / "catalog.sqlite"
            jsonl = Path(td) / "catalog.jsonl"
            store = eiga_catalog.EigaStore(db, jsonl)
            try:
                store.save_person(person)
            finally:
                store.close()

            stats = eiga_catalog.mark_favorite_people(
                db,
                ["ケビン・スペイシー", "未登録俳優"],
                "actor_affinity",
                "favorite_people_test",
                "user",
                1.0,
                "manual favorite",
            )
            self.assertEqual(stats["input"], 2)
            self.assertEqual(stats["signals"], 2)
            self.assertEqual(stats["resolved"], 1)
            self.assertEqual(stats["unresolved"], 1)

            conn = sqlite3.connect(db)
            rows = conn.execute(
                "SELECT signal_type, target_id, target_label, generated_by FROM movie_preference_signals ORDER BY target_label"
            ).fetchall()
            people_columns = [row[1] for row in conn.execute("PRAGMA table_info(people)").fetchall()]
            conn.close()
            self.assertIn(("actor_affinity", "30003", "ケビン・スペイシー", "user"), rows)
            self.assertIn(("actor_affinity", None, "未登録俳優", "user"), rows)
            self.assertNotIn("favorite", people_columns)


if __name__ == "__main__":
    unittest.main()
