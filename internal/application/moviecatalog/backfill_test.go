package moviecatalog

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestNextBackfillTargetPrefersUnfetchedMovieBeforePerson(t *testing.T) {
	db := seedBackfillDB(t)
	defer db.Close()

	target, ok, err := NextBackfillTarget(context.Background(), db)
	if err != nil {
		t.Fatalf("next target: %v", err)
	}
	if !ok {
		t.Fatal("expected target")
	}
	if target.Kind != "movie" || target.ID != "200" || target.Title != "未取得映画" || target.URL != "https://eiga.com/movie/200/" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestNextBackfillTargetUsesPersonAfterMoviesAreFetched(t *testing.T) {
	db := seedBackfillDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO movies(movie_id,title,url,synopsis) VALUES('200','未取得映画','https://eiga.com/movie/200/','')`); err != nil {
		t.Fatalf("seed movie: %v", err)
	}

	target, ok, err := NextBackfillTarget(context.Background(), db)
	if err != nil {
		t.Fatalf("next target: %v", err)
	}
	if !ok {
		t.Fatal("expected target")
	}
	if target.Kind != "person" || target.ID != "300" || target.Title != "未取得人物" || target.URL != "https://eiga.com/person/300/" {
		t.Fatalf("unexpected target: %+v", target)
	}
}

func TestRunOnceInvokesCrawlerForOneTarget(t *testing.T) {
	path, db := seedBackfillDBPath(t)
	db.Close()
	var gotArgs []string
	svc := NewBackfillService(BackfillOptions{
		DBPath:       path,
		WorkspaceDir: "/repo",
		InitialDelay: -1,
		Timeout:      time.Second,
		Runner: func(ctx context.Context, workspaceDir string, args []string, timeout time.Duration) ([]byte, error) {
			if workspaceDir != "/repo" {
				t.Fatalf("unexpected workspace dir: %s", workspaceDir)
			}
			gotArgs = append([]string(nil), args...)
			return []byte("ok 1/1"), nil
		},
	})

	toolsRoot := filepath.Join(t.TempDir(), "RenCrow_Tools")
	t.Setenv("RENCROW_TOOLS_ROOT", toolsRoot)

	result, err := svc.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run once: %v", err)
	}
	if result.Status != "fetched" || result.Target.Kind != "movie" {
		t.Fatalf("unexpected result: %+v", result)
	}
	want := []string{
		filepath.Join(toolsRoot, "tools", "eiga_catalog", "eiga_catalog.py"),
		"--seed-url", "https://eiga.com/movie/200/",
		"--max-pages", "1",
		"--delay", "2",
		"--db", path,
		"--jsonl", filepath.Join(filepath.Dir(path), "eiga_catalog.jsonl"),
	}
	if len(gotArgs) != len(want) {
		t.Fatalf("args length mismatch\n got: %+v\nwant: %+v", gotArgs, want)
	}
	for i := range want {
		if gotArgs[i] != want[i] {
			t.Fatalf("arg[%d] = %q, want %q\nall args: %+v", i, gotArgs[i], want[i], gotArgs)
		}
	}
}

func TestJoinErrorOutputPreservesCrawlerOutput(t *testing.T) {
	got := joinErrorOutput("usage: crawler\nmissing file", errTestBackfill)
	if got != "usage: crawler\nmissing file\nbackfill test error" {
		t.Fatalf("unexpected joined output: %q", got)
	}
}

func TestNextBackfillTargetIdleWhenNoMissingLinks(t *testing.T) {
	db := seedBackfillDB(t)
	defer db.Close()
	if _, err := db.Exec(`
INSERT INTO movies(movie_id,title,url,synopsis) VALUES('200','未取得映画','https://eiga.com/movie/200/','');
INSERT INTO people(person_id,name,url,profile_json,biography) VALUES('300','未取得人物','https://eiga.com/person/300/','{}','');
`); err != nil {
		t.Fatalf("seed fetched rows: %v", err)
	}
	_, ok, err := NextBackfillTarget(context.Background(), db)
	if err != nil {
		t.Fatalf("next target: %v", err)
	}
	if ok {
		t.Fatal("expected no target")
	}
}

var errTestBackfill = backfillTestError{}

type backfillTestError struct{}

func (backfillTestError) Error() string { return "backfill test error" }

func seedBackfillDB(t *testing.T) *sql.DB {
	t.Helper()
	_, db := seedBackfillDBPath(t)
	return db
}

func seedBackfillDBPath(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "eiga_catalog.sqlite")
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`
CREATE TABLE movies(movie_id TEXT PRIMARY KEY, title TEXT NOT NULL, url TEXT NOT NULL, synopsis TEXT);
CREATE TABLE people(person_id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, profile_json TEXT, biography TEXT);
CREATE TABLE movie_people(
  movie_id TEXT NOT NULL,
  person_id TEXT NOT NULL,
  role TEXT NOT NULL,
  source TEXT NOT NULL,
  movie_title TEXT,
  person_name TEXT,
  movie_url TEXT,
  person_url TEXT,
  PRIMARY KEY(movie_id, person_id, role, source)
);
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
VALUES('200','100','出演','person_filmography','未取得映画','取得済人物','https://eiga.com/movie/200/','https://eiga.com/person/100/');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
VALUES('100','300','出演','movie_cast','取得済映画','未取得人物','https://eiga.com/movie/100/','https://eiga.com/person/300/');
INSERT INTO movies(movie_id,title,url,synopsis) VALUES('100','取得済映画','https://eiga.com/movie/100/','');
INSERT INTO people(person_id,name,url,profile_json,biography) VALUES('100','取得済人物','https://eiga.com/person/100/','{}','');
`); err != nil {
		db.Close()
		t.Fatalf("seed sqlite: %v", err)
	}
	return path, db
}
