package moviecatalog

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCardsSelectsPositiveRootsUsesFallbackAndStopsAtD1(t *testing.T) {
	db, err := sql.Open("sqlite", "file:card-projection?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE movies(movie_id TEXT PRIMARY KEY, title TEXT NOT NULL, url TEXT NOT NULL, synopsis TEXT);
CREATE TABLE people(person_id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, profile_json TEXT, biography TEXT);
CREATE TABLE movie_people(movie_id TEXT NOT NULL, person_id TEXT NOT NULL, role TEXT NOT NULL, source TEXT NOT NULL, movie_title TEXT, person_name TEXT, movie_url TEXT, person_url TEXT);
CREATE TABLE movie_watch_events(event_id TEXT PRIMARY KEY, movie_id TEXT NOT NULL, original_title TEXT, watched_at TEXT, source TEXT, source_batch_id TEXT, note TEXT, created_at TEXT);
CREATE TABLE movie_preference_signals(signal_id TEXT PRIMARY KEY, signal_type TEXT NOT NULL, target_id TEXT, target_label TEXT NOT NULL, weight REAL NOT NULL, evidence_json TEXT NOT NULL, generated_by TEXT NOT NULL, generated_at TEXT NOT NULL);
CREATE TABLE movie_catalog_assessments(kind TEXT NOT NULL, target_id TEXT NOT NULL, target_label TEXT NOT NULL, familiarity TEXT NOT NULL DEFAULT '', sentiment TEXT NOT NULL DEFAULT '', updated_by TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(kind,target_id));
CREATE TABLE movie_catalog_roots(root_id TEXT PRIMARY KEY, manifest_id TEXT NOT NULL, kind TEXT NOT NULL, target_id TEXT NOT NULL, target_label TEXT NOT NULL, target_url TEXT NOT NULL, validation_state TEXT NOT NULL, provenance_json TEXT NOT NULL, source_url TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE movie_related_credits(credit_id TEXT PRIMARY KEY, movie_id TEXT NOT NULL, target_kind TEXT NOT NULL, target_id TEXT, target_label TEXT NOT NULL, target_url TEXT, relation_type TEXT NOT NULL, source TEXT NOT NULL, validation_state TEXT NOT NULL, provenance_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO movies VALUES ('m1','Seen Movie','https://eiga.com/movie/1/',''),('m2','Watched Movie','https://eiga.com/movie/2/',''),('m3','D1 Movie','https://eiga.com/movie/3/',''),('m4','Explicit Movie','https://eiga.com/movie/4/',''),('m5','Suppressed Movie','https://eiga.com/movie/5/','');
INSERT INTO people VALUES ('p1','Known Person','https://eiga.com/person/1/','',''),('p2','D1 Person','https://eiga.com/person/2/','',''),('p3','D2 Person','https://eiga.com/person/3/','',''),('p4','Negative Person','https://eiga.com/person/4/','','');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url) VALUES
 ('m1','p2','出演','eiga','Seen Movie','D1 Person','https://eiga.com/movie/1/','https://eiga.com/person/2/'),
 ('m2','p1','出演','eiga','Watched Movie','Known Person','https://eiga.com/movie/2/','https://eiga.com/person/1/'),
 ('m3','p1','出演','eiga','D1 Movie','Known Person','https://eiga.com/movie/3/','https://eiga.com/person/1/'),
 ('m3','p3','出演','eiga','D1 Movie','D2 Person','https://eiga.com/movie/3/','https://eiga.com/person/3/'),
 ('m4','p2','出演','eiga','Explicit Movie','D1 Person','https://eiga.com/movie/4/','https://eiga.com/person/2/'),
 ('m5','p4','出演','eiga','Suppressed Movie','Negative Person','https://eiga.com/movie/5/','https://eiga.com/person/4/');
INSERT INTO movie_watch_events VALUES ('watch-2','m2','Watched Movie','2026-08-08','viewer','','','2026-08-08');
INSERT INTO movie_watch_events VALUES ('watch-m5','m5','Suppressed Movie','2026-08-08','viewer','','','2026-08-08');
INSERT INTO movie_preference_signals VALUES ('pref-p1','actor_affinity','p1','Known Person',1.0,'{}','viewer','2026-08-08');
INSERT INTO movie_preference_signals VALUES ('pref-p4','actor_affinity','p4','Negative Person',1.0,'{}','viewer','2026-08-08');
INSERT INTO movie_catalog_assessments VALUES ('movie','m1','Seen Movie','seen','','viewer','2026-08-08');
INSERT INTO movie_catalog_assessments VALUES ('movie','m5','Suppressed Movie','','','viewer','2026-08-08');
INSERT INTO movie_catalog_assessments VALUES ('person','p1','Known Person','known','','viewer','2026-08-08');
INSERT INTO movie_catalog_assessments VALUES ('person','p4','Negative Person','','dislike','viewer','2026-08-08');
INSERT INTO movie_catalog_roots VALUES ('fetch-root-4','manifest-4','movie','m4','Explicit Movie','https://eiga.com/movie/4/','confirmed','["https://eiga.com/movie/4/"]','https://eiga.com/movie/4/','2026-08-08','2026-08-08');
`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	_, cards, err := Cards(db, 100, 0)
	if err != nil {
		t.Fatalf("Cards: %v", err)
	}
	byKey := map[string]Card{}
	for _, card := range cards {
		byKey[card.Kind+":"+card.TargetID+":"+card.TargetLabel] = card
	}
	if _, ok := byKey["movie:m5:Suppressed Movie"]; ok {
		t.Fatal("empty explicit assessment must suppress watch fallback")
	}
	if _, ok := byKey["person:p4:Negative Person"]; ok {
		t.Fatal("negative explicit assessment must suppress favorite fallback")
	}
	for _, key := range []string{"movie:m1:Seen Movie", "movie:m2:Watched Movie", "person:p1:Known Person", "person:p2:D1 Person", "movie:m3:D1 Movie", "movie:m4:Explicit Movie"} {
		if _, ok := byKey[key]; !ok {
			t.Fatalf("missing expected card %q; cards=%+v", key, cards)
		}
	}
	for _, key := range []string{"movie:m1:Seen Movie", "movie:m2:Watched Movie", "person:p1:Known Person", "movie:m4:Explicit Movie"} {
		if card := byKey[key]; card.Depth != 0 {
			t.Fatalf("%s must be D0: %+v", key, card)
		}
	}
	if card, ok := byKey["person:p2:D1 Person"]; !ok || card.Depth != 1 {
		t.Fatalf("p2 must be D1 from m1/m4: got card=%+v exists=%v", card, ok)
	}
	if card, ok := byKey["movie:m3:D1 Movie"]; !ok || card.Depth != 1 {
		t.Fatalf("m3 must be D1 from p1: got card=%+v exists=%v", card, ok)
	}
	if _, ok := byKey["person:p3:D2 Person"]; ok {
		t.Fatal("D1 card must not be re-expanded into D2")
	}
}

func TestCardsReturnsPartialSourceWorkCreditWithoutHobbyMaterialization(t *testing.T) {
	db, err := sql.Open("sqlite", "file:card-projection-partial-credit?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	artifact := strings.NewReader(`{"record_type":"manifest","schema_version":"movie-catalog-graph/v2","artifact_id":"artifact-partial","input":{"kind":"movie","seed_url":"https://eiga.com/movie/10/"},"root_node_ids":["movie:10"],"node_count":2,"edge_count":1,"validation_state":"confirmed","provenance_urls":["https://eiga.com/movie/10/"]}
{"record_type":"node","schema_version":"movie-catalog-graph/v2","node_id":"movie:10","kind":"movie","label":"Root","url":"https://eiga.com/movie/10/","validation_state":"validated","provenance_urls":["https://eiga.com/movie/10/"]}
{"record_type":"node","schema_version":"movie-catalog-graph/v2","node_id":"source_work:novel","kind":"source_work","label":"原作小説","url":"https://example.test/work/novel","validation_state":"partial","provenance_urls":["https://eiga.com/movie/10/"]}
{"record_type":"edge","schema_version":"movie-catalog-graph/v2","edge_id":"credit-1","from_node_id":"movie:10","to_node_id":"source_work:novel","relation_type":"原作","validation_state":"validated","provenance_urls":["https://eiga.com/movie/10/"]}`)
	if _, err := ImportJSONL(context.Background(), db, artifact, ""); err != nil {
		t.Fatalf("partial credit import: %v", err)
	}
	_, cards, err := Cards(db, 100, 0)
	if err != nil {
		t.Fatalf("Cards: %v", err)
	}
	var found bool
	for _, card := range cards {
		if card.Kind == "source_work" && card.TargetLabel == "原作小説" {
			found = true
			if card.TargetID != "" || card.Depth != 1 || card.ValidationState != "partial" || len(card.ProvenanceURLs) != 1 {
				t.Fatalf("unexpected partial source work card: %+v", card)
			}
		}
	}
	if !found {
		t.Fatalf("partial source work card is missing: %+v", cards)
	}
	if tableExists(db, "hobby_graph") {
		t.Fatal("movie catalog import must not create hobby_graph canonical storage")
	}
}

func TestCardsDeduplicatesRootsAndCreditsWithoutMaterializingDepth(t *testing.T) {
	db, err := sql.Open("sqlite", "file:card-projection-dedupe?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE movies(movie_id TEXT PRIMARY KEY, title TEXT NOT NULL, url TEXT NOT NULL, synopsis TEXT);
CREATE TABLE people(person_id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, profile_json TEXT, biography TEXT);
CREATE TABLE movie_people(movie_id TEXT NOT NULL, person_id TEXT NOT NULL, role TEXT NOT NULL, source TEXT NOT NULL, movie_title TEXT, person_name TEXT, movie_url TEXT, person_url TEXT);
CREATE TABLE movie_catalog_assessments(kind TEXT NOT NULL, target_id TEXT NOT NULL, target_label TEXT NOT NULL, familiarity TEXT NOT NULL DEFAULT '', sentiment TEXT NOT NULL DEFAULT '', updated_by TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(kind,target_id));
CREATE TABLE movie_related_credits(credit_id TEXT PRIMARY KEY, movie_id TEXT NOT NULL, target_kind TEXT NOT NULL, target_id TEXT, target_label TEXT NOT NULL, target_url TEXT, relation_type TEXT NOT NULL, source TEXT NOT NULL, validation_state TEXT NOT NULL, provenance_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
INSERT INTO movies VALUES ('m1','Root One','https://eiga.com/movie/1/',''),('m2','Root Two','https://eiga.com/movie/2/','');
INSERT INTO people VALUES ('p1','Shared Person','https://eiga.com/person/1/','','');
INSERT INTO movie_people VALUES ('m1','p1','出演','eiga','Root One','Shared Person','https://eiga.com/movie/1/','https://eiga.com/person/1/');
INSERT INTO movie_people VALUES ('m2','p1','出演','eiga','Root Two','Shared Person','https://eiga.com/movie/2/','https://eiga.com/person/1/');
INSERT INTO movie_catalog_assessments VALUES ('movie','m1','Root','seen','','viewer','2026-08-08');
INSERT INTO movie_catalog_assessments VALUES ('movie','m2','Root Two','','like','viewer','2026-08-08');
INSERT INTO movie_related_credits VALUES ('credit-1','m1','music','music-1','Theme','https://example.test/music/1','theme_song','eiga','validated','["https://example.test/music/1"]','2026-08-08','2026-08-08');
`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, cards, err := Cards(db, 100, 0)
	if err != nil {
		t.Fatalf("Cards: %v", err)
	}
	var personCount, musicCount int
	for _, card := range cards {
		if card.Kind == "person" && card.TargetID == "p1" {
			personCount++
			if card.Depth != 1 || len(card.RootIDs) != 2 {
				t.Fatalf("person card must merge two roots at minimum depth: %+v", card)
			}
		}
		if card.Kind == "music" && card.TargetID == "music-1" {
			musicCount++
			if card.Depth != 1 || card.ValidationState != "validated" || len(card.ProvenanceURLs) != 1 {
				t.Fatalf("unexpected music card: %+v", card)
			}
		}
	}
	if personCount != 1 || musicCount != 1 {
		t.Fatalf("expected deduped person/music cards, person=%d music=%d cards=%+v", personCount, musicCount, cards)
	}
	for _, table := range []string{"movie_catalog_assessments", "movie_related_credits"} {
		columns, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			t.Fatal(err)
		}
		defer columns.Close()
		for columns.Next() {
			var cid int
			var name, typ string
			var notnull, pk int
			var dflt interface{}
			if err := columns.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if name == "depth" {
				t.Fatalf("derived depth must not be materialized in %s", table)
			}
		}
	}
}

func TestCardsProjectsUnfetchedMoviePeopleEdgesAsPartialD1(t *testing.T) {
	db, err := sql.Open("sqlite", "file:card-projection-left-join?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	if err := ensureCatalogImportSchema(context.Background(), db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
	_, err = db.Exec(`
CREATE TABLE movie_catalog_assessments(
  kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  target_label TEXT NOT NULL,
  familiarity TEXT NOT NULL DEFAULT '',
  sentiment TEXT NOT NULL DEFAULT '',
  updated_by TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(kind,target_id)
);
INSERT INTO movies(movie_id,title,url) VALUES ('root-movie','Root Movie','https://eiga.com/movie/root-movie/');
INSERT INTO people(person_id,name,url) VALUES ('root-person','Root Person','https://eiga.com/person/root-person/');
INSERT INTO movie_catalog_assessments(kind,target_id,target_label,familiarity,sentiment,updated_by,updated_at) VALUES
 ('movie','root-movie','Root Movie','seen','','viewer','2026-08-08'),
 ('person','root-person','Root Person','known','','viewer','2026-08-08');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url) VALUES
 ('root-movie','missing-person','出演','eiga','Root Movie','Unfetched Person','https://eiga.com/movie/root-movie/','https://eiga.com/person/missing-person/'),
 ('missing-movie','root-person','出演','eiga','Unfetched Movie','Root Person','https://eiga.com/movie/missing-movie/','https://eiga.com/person/root-person/');
`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, cards, err := Cards(db, 100, 0)
	if err != nil {
		t.Fatalf("Cards: %v", err)
	}
	byKey := map[string]Card{}
	for _, card := range cards {
		byKey[card.Kind+":"+card.TargetID] = card
	}
	person, ok := byKey["person:missing-person"]
	if !ok || person.Depth != 1 || person.TargetLabel != "Unfetched Person" || person.TargetURL != "https://eiga.com/person/missing-person/" || person.ValidationState != "partial" {
		t.Fatalf("unfetched person edge must remain partial D1: exists=%v card=%+v", ok, person)
	}
	movie, ok := byKey["movie:missing-movie"]
	if !ok || movie.Depth != 1 || movie.TargetLabel != "Unfetched Movie" || movie.TargetURL != "https://eiga.com/movie/missing-movie/" || movie.ValidationState != "partial" {
		t.Fatalf("unfetched movie edge must remain partial D1: exists=%v card=%+v", ok, movie)
	}
}
