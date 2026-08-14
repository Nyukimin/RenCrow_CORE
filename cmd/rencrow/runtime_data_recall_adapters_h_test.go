package main

import (
	"testing"

	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
)

func TestDataRecallMovieCatalogPublicProjectionIsSafeAndBounded(t *testing.T) {
	records := runtimeMovieCatalogPublicRecords(moviecatalogapp.LookupResult{
		Kind:   "movie",
		Movies: []moviecatalogapp.MovieLookupCandidate{{MovieID: "m1", Title: "Heat", URL: "https://example.test/m1"}},
		Detail: map[string]any{"secret_path": "/private/movie.db", "links": []any{"must not leak"}},
	})
	if len(records) != 1 {
		t.Fatalf("public movie records=%#v", records)
	}
	if records[0]["movie_id"] != "m1" || records[0]["title"] != "Heat" || records[0]["url"] != "https://example.test/m1" {
		t.Fatalf("public movie record=%#v", records[0])
	}
	if _, leaked := records[0]["detail"]; leaked {
		t.Fatal("public movie projection must not expose lookup detail")
	}
	if _, leaked := records[0]["secret_path"]; leaked {
		t.Fatal("public movie projection must not expose storage paths")
	}
}

func TestDataRecallMovieCatalogPublicProjectionHandlesPeople(t *testing.T) {
	records := runtimeMovieCatalogPublicRecords(moviecatalogapp.LookupResult{
		Kind:   "person",
		People: []moviecatalogapp.PersonLookupCandidate{{PersonID: "p1", Name: "Al Pacino", URL: "https://example.test/p1"}},
	})
	if len(records) != 1 || records[0]["person_id"] != "p1" || records[0]["name"] != "Al Pacino" {
		t.Fatalf("public people record=%#v", records)
	}
}
