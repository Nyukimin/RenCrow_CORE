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

func TestImportJSONLPersonRootSurvivesPartialFilmographyEdges(t *testing.T) {
	db, err := sql.Open("sqlite", "file:person-partial-v1?mode=memory&cache=shared&_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()

	artifact := strings.NewReader(`{"kind":"person","person_id":"74046","name":"役所広司","url":"https://eiga.com/person/74046/","filmography":[{"movie_id":"106274","title":"","url":"https://eiga.com/movie/106274/interview/","role":""},{"movie_id":"","title":"Label-only movie","url":"","role":""},{"movie_id":"","title":"","url":"","role":""}]}`)
	result, err := ImportJSONL(context.Background(), db, artifact, "https://eiga.com/person/74046/")
	if err != nil {
		t.Fatalf("partial v1 person import: %v", err)
	}
	if result.Movies != 0 || result.People != 1 || result.Edges != 2 || result.Records != 1 {
		t.Fatalf("unexpected partial v1 result: %+v", result)
	}
	var people, edges int
	if err := db.QueryRow("SELECT COUNT(*) FROM people").Scan(&people); err != nil {
		t.Fatalf("count people: %v", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM movie_people").Scan(&edges); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if people != 1 || edges != 2 {
		t.Fatalf("root and partial edges must commit: people=%d edges=%d", people, edges)
	}
	var movieID, movieTitle, movieURL string
	if err := db.QueryRow(`SELECT movie_id,movie_title,movie_url FROM movie_people WHERE movie_id='106274'`).Scan(&movieID, &movieTitle, &movieURL); err != nil {
		t.Fatalf("read URL-only partial edge: %v", err)
	}
	if movieID != "106274" || movieTitle != "" || movieURL != "https://eiga.com/movie/106274/interview/" {
		t.Fatalf("partial edge fields changed: id=%q title=%q url=%q", movieID, movieTitle, movieURL)
	}
	if err := db.QueryRow(`SELECT movie_id,movie_title,movie_url FROM movie_people WHERE movie_title='Label-only movie'`).Scan(&movieID, &movieTitle, &movieURL); err != nil {
		t.Fatalf("read label-only partial edge: %v", err)
	}
	if movieID != "" || movieTitle != "Label-only movie" || movieURL != "" {
		t.Fatalf("label-only partial edge fields changed: id=%q title=%q url=%q", movieID, movieTitle, movieURL)
	}
}
