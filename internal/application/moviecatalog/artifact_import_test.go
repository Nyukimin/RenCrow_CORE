package moviecatalog

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestImportJSONLIsTransactionalAndPreservesEdges(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	artifact := strings.NewReader(`{"kind":"movie","movie_id":"1","title":"Movie","url":"https://eiga.com/movie/1/","cast":[{"person_id":"2","name":"Person","url":"https://eiga.com/person/2/","role":"actor"}]}
{"kind":"person","person_id":"2","name":"Person","url":"https://eiga.com/person/2/","profile":{"country":"Japan"},"biography":"bio","filmography":[{"movie_id":"1","title":"Movie","url":"https://eiga.com/movie/1/","role":"lead"}]}
`)
	result, err := ImportJSONL(context.Background(), db, artifact, "https://eiga.com/movie/1/")
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Movies != 1 || result.People != 1 || result.Edges != 2 || result.Records != 2 {
		t.Fatalf("unexpected import result: %+v", result)
	}
	var movies, people, edges, fetches int
	if err := db.QueryRow("SELECT COUNT(*) FROM movies").Scan(&movies); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM people").Scan(&people); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM movie_people").Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM fetch_log").Scan(&fetches); err != nil {
		t.Fatal(err)
	}
	if movies != 1 || people != 1 || edges != 2 || fetches != 1 {
		t.Fatalf("unexpected catalog counts movies=%d people=%d edges=%d fetches=%d", movies, people, edges, fetches)
	}
}

func TestImportJSONLRollsBackMalformedRecord(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	artifact := strings.NewReader(`{"kind":"movie","movie_id":"1","title":"Movie","url":"https://eiga.com/movie/1/"}
{"kind":"person","person_id":"","name":"Broken","url":"https://eiga.com/person/2/"}
`)
	if _, err := ImportJSONL(context.Background(), db, artifact, ""); err == nil {
		t.Fatal("expected malformed artifact error")
	}
	var movies int
	if err := db.QueryRow("SELECT COUNT(*) FROM movies").Scan(&movies); err != nil {
		t.Fatal(err)
	}
	if movies != 0 {
		t.Fatalf("transaction should roll back, movies=%d", movies)
	}
}
