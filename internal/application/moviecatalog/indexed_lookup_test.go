package moviecatalog

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func setupIndexedLookupDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:?_time_format=sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := ensureCatalogImportSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexedLookupSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
INSERT INTO movies(movie_id,title,title_lookup_key,url,synopsis) VALUES
 ('m1','Alien','alien','https://example.test/m1',''),
 ('m2','ALIEN','alien','https://example.test/m2',''),
 ('m3','Heat','heat','https://example.test/m3','');
INSERT INTO people(person_id,name,name_lookup_key,url,profile_json,biography) VALUES
 ('p1','Sigourney Weaver','sigourney weaver','https://example.test/p1','{}',''),
 ('p2','Al Pacino','al pacino','https://example.test/p2','{}','');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url) VALUES
 ('m1','p1','actor','test','Alien','Sigourney Weaver','https://example.test/m1','https://example.test/p1'),
 ('m3','p2','actor','test','Heat','Al Pacino','https://example.test/m3','https://example.test/p2');`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestEnsureIndexedLookupSchemaMigratesAndBackfills(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE movies(movie_id TEXT PRIMARY KEY,title TEXT NOT NULL,url TEXT NOT NULL,synopsis TEXT);
CREATE TABLE people(person_id TEXT PRIMARY KEY,name TEXT NOT NULL,url TEXT NOT NULL,profile_json TEXT,biography TEXT);
CREATE TABLE movie_people(movie_id TEXT NOT NULL,person_id TEXT NOT NULL,role TEXT NOT NULL,source TEXT NOT NULL,PRIMARY KEY(movie_id,person_id,role,source));
INSERT INTO movies(movie_id,title,url) VALUES('m1','  Heat  ','u');
INSERT INTO people(person_id,name,url) VALUES('p1','  AL PACINO  ','u');`)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndexedLookupSchema(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var movieKey, personKey string
	if err := db.QueryRow(`SELECT title_lookup_key FROM movies WHERE movie_id='m1'`).Scan(&movieKey); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT name_lookup_key FROM people WHERE person_id='p1'`).Scan(&personKey); err != nil {
		t.Fatal(err)
	}
	if movieKey != "heat" || personKey != "al pacino" {
		t.Fatalf("unexpected keys movie=%q person=%q", movieKey, personKey)
	}
}

func TestLookupPersonAndMovie(t *testing.T) {
	db := setupIndexedLookupDB(t)
	person, err := Lookup(db, LookupRequest{Kind: "person", Name: "  SIGOURNEY WEAVER  "})
	if err != nil {
		t.Fatal(err)
	}
	if person.Ambiguous || person.NotFound || len(person.People) != 1 || person.Detail == nil {
		t.Fatalf("unexpected person result: %+v", person)
	}
	links := person.Detail["links"].([]EdgeItem)
	if len(links) != 1 || links[0].MovieID != "m1" {
		t.Fatalf("unexpected person links: %+v", links)
	}

	movie, err := Lookup(db, LookupRequest{Kind: "movie", Name: "Heat"})
	if err != nil {
		t.Fatal(err)
	}
	if movie.Ambiguous || movie.NotFound || len(movie.Movies) != 1 || movie.Detail == nil {
		t.Fatalf("unexpected movie result: %+v", movie)
	}
	links = movie.Detail["links"].([]EdgeItem)
	if len(links) != 1 || links[0].PersonID != "p2" {
		t.Fatalf("unexpected movie links: %+v", links)
	}
}

func TestLookupCapsDirectLinksAndPrefersCastForMovie(t *testing.T) {
	db := setupIndexedLookupDB(t)
	_, err := db.Exec(`
INSERT INTO people(person_id,name,name_lookup_key,url,profile_json,biography) VALUES
 ('p3','Director','director','https://example.test/p3','{}',''),
 ('p4','Second Actor','second actor','https://example.test/p4','{}','');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name) VALUES
 ('m3','p3','director','test','Heat','Director'),
 ('m3','p4','cast','test','Heat','Second Actor');`)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Lookup(db, LookupRequest{Kind: "movie", Name: "Heat", Information: "cast", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	links := result.Detail["links"].([]EdgeItem)
	if len(links) != 1 || (links[0].Role != "cast" && links[0].Role != "actor") {
		t.Fatalf("movie lookup must return bounded cast links: %#v", links)
	}
}

func TestLookupReportsUnavailableEmptyProfile(t *testing.T) {
	db := setupIndexedLookupDB(t)
	result, err := Lookup(db, LookupRequest{Kind: "person", Name: "Al Pacino", Information: "profile"})
	if err != nil {
		t.Fatal(err)
	}
	if available, ok := result.Detail["information_available"].(bool); !ok || available {
		t.Fatalf("empty profile must be explicit: %#v", result.Detail)
	}
}

func TestLookupProjectsRequestedInformation(t *testing.T) {
	db := setupIndexedLookupDB(t)
	_, err := db.Exec(`
INSERT INTO people(person_id,name,name_lookup_key,url,profile_json,biography) VALUES
 ('p3','Director','director','https://example.test/p3','{}','director profile');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name) VALUES
 ('m3','p3','director','movie_staff','Heat','Director');`)
	if err != nil {
		t.Fatal(err)
	}

	overview, err := Lookup(db, LookupRequest{Kind: "movie", Name: "Heat", Information: "overview", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := overview.Detail["links"]; exists {
		t.Fatalf("overview leaked links: %#v", overview.Detail)
	}

	cast, err := Lookup(db, LookupRequest{Kind: "movie", Name: "Heat", Information: "cast", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	castLinks := cast.Detail["links"].([]EdgeItem)
	if len(castLinks) != 1 || castLinks[0].PersonID != "p2" {
		t.Fatalf("unexpected cast: %#v", castLinks)
	}

	staff, err := Lookup(db, LookupRequest{Kind: "movie", Name: "Heat", Information: "staff", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	staffLinks := staff.Detail["links"].([]EdgeItem)
	if len(staffLinks) != 1 || staffLinks[0].PersonID != "p3" {
		t.Fatalf("unexpected staff: %#v", staffLinks)
	}

	profile, err := Lookup(db, LookupRequest{Kind: "person", Name: "Al Pacino", Information: "profile", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := profile.Detail["links"]; exists {
		t.Fatalf("profile leaked links: %#v", profile.Detail)
	}

	filmography, err := Lookup(db, LookupRequest{Kind: "person", Name: "Al Pacino", Information: "filmography", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if links := filmography.Detail["links"].([]EdgeItem); len(links) != 1 || links[0].MovieID != "m3" {
		t.Fatalf("unexpected filmography: %#v", links)
	}
}

func TestLookupDuplicateAndNotFoundDoNotGuess(t *testing.T) {
	db := setupIndexedLookupDB(t)
	duplicate, err := Lookup(db, LookupRequest{Kind: "movie", Name: "alien"})
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Ambiguous || duplicate.Detail != nil || len(duplicate.Movies) != 2 {
		t.Fatalf("unexpected duplicate result: %+v", duplicate)
	}
	missing, err := Lookup(db, LookupRequest{Kind: "person", Name: "Nobody"})
	if err != nil {
		t.Fatal(err)
	}
	if !missing.NotFound || missing.Detail != nil {
		t.Fatalf("unexpected missing result: %+v", missing)
	}
}

func TestLookupValidationAndMissingIndexFailClosed(t *testing.T) {
	db := setupIndexedLookupDB(t)
	for _, req := range []LookupRequest{
		{Kind: "", Name: "Heat"}, {Kind: "actor", Name: "Al Pacino"},
		{Kind: "movie", Name: " "}, {Kind: "movie", Name: "Heat", Limit: 21},
		{Kind: "movie", Name: "Heat", Information: "profile"},
		{Kind: "person", Name: "Al Pacino", Information: "cast"},
	} {
		if _, err := Lookup(db, req); err == nil {
			t.Fatalf("expected validation error for %+v", req)
		}
	}
	if _, err := db.Exec(`DROP INDEX idx_people_name_lookup_key`); err != nil {
		t.Fatal(err)
	}
	if _, err := Lookup(db, LookupRequest{Kind: "person", Name: "Al Pacino"}); err == nil {
		t.Fatal("expected missing-index error")
	}
}

func TestIndexedLookupQueryPlansNeverScanTargets(t *testing.T) {
	db := setupIndexedLookupDB(t)
	assertSearchPlan(t, db, "idx_movies_title_lookup_key", "movies", `SELECT movie_id,title,url FROM movies INDEXED BY idx_movies_title_lookup_key WHERE title_lookup_key = ? ORDER BY movie_id LIMIT ?`, "heat", 10)
	assertSearchPlan(t, db, "idx_people_name_lookup_key", "people", `SELECT person_id,name,url FROM people INDEXED BY idx_people_name_lookup_key WHERE name_lookup_key = ? ORDER BY person_id LIMIT ?`, "al pacino", 10)
	assertSearchPlan(t, db, "idx_movie_people_person_id", "movie_people", `SELECT movie_id FROM movie_people WHERE person_id = ? LIMIT ?`, "p2", 200)
}

func assertSearchPlan(t *testing.T, db *sql.DB, indexName, table, query string, args ...any) {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	upper := strings.ToUpper(plan)
	if !strings.Contains(upper, "SEARCH "+strings.ToUpper(table)) || !strings.Contains(plan, indexName) {
		t.Fatalf("expected indexed SEARCH using %s; plan:\n%s", indexName, plan)
	}
	if strings.Contains(upper, "SCAN "+strings.ToUpper(table)) {
		t.Fatalf("unexpected target table scan; plan:\n%s", plan)
	}
}
