package moviecatalog

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type CatalogImportResult struct {
	Movies  int
	People  int
	Edges   int
	Records int
}

type catalogContextExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type movieArtifactRecord struct {
	Kind            string                `json:"kind"`
	MovieID         string                `json:"movie_id"`
	Title           string                `json:"title"`
	URL             string                `json:"url"`
	Synopsis        string                `json:"synopsis"`
	Cast            []movieArtifactPerson `json:"cast"`
	Staff           []movieArtifactPerson `json:"staff"`
	RelatedPeople   []movieArtifactPerson `json:"related_people"`
	PersonID        string                `json:"person_id"`
	Name            string                `json:"name"`
	Profile         map[string]string     `json:"profile"`
	Biography       string                `json:"biography"`
	BiographyMovies []movieArtifactMovie  `json:"biography_movies"`
	Filmography     []movieArtifactMovie  `json:"filmography"`
}

type movieArtifactPerson struct {
	PersonID string `json:"person_id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Role     string `json:"role"`
}

type movieArtifactMovie struct {
	MovieID string `json:"movie_id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Role    string `json:"role"`
}

// ImportJSONLFile imports the sidecar's staged JSONL into the CORE-owned DB.
// The transaction makes a malformed artifact a no-op instead of leaving a
// partially imported catalog behind.
func ImportJSONLFile(ctx context.Context, db *sql.DB, path string, sourceURL string) (CatalogImportResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return CatalogImportResult{}, fmt.Errorf("open movie catalog artifact: %w", err)
	}
	defer file.Close()
	return ImportJSONL(ctx, db, file, sourceURL)
}

func ImportJSONL(ctx context.Context, db *sql.DB, reader io.Reader, sourceURL string) (CatalogImportResult, error) {
	if db == nil {
		return CatalogImportResult{}, fmt.Errorf("movie catalog database is nil")
	}
	if reader == nil {
		return CatalogImportResult{}, fmt.Errorf("movie catalog artifact is nil")
	}
	if err := ensureCatalogImportSchema(ctx, db); err != nil {
		return CatalogImportResult{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return CatalogImportResult{}, fmt.Errorf("begin movie catalog import: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	result := CatalogImportResult{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for lineNo := 1; scanner.Scan(); lineNo++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record movieArtifactRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return CatalogImportResult{}, fmt.Errorf("decode movie catalog artifact line %d: %w", lineNo, err)
		}
		switch strings.ToLower(strings.TrimSpace(record.Kind)) {
		case "movie":
			count, err := importMovieRecord(ctx, tx, record)
			if err != nil {
				return CatalogImportResult{}, fmt.Errorf("import movie catalog line %d: %w", lineNo, err)
			}
			result.Movies++
			result.Edges += count
		case "person":
			count, err := importPersonRecord(ctx, tx, record)
			if err != nil {
				return CatalogImportResult{}, fmt.Errorf("import movie catalog line %d: %w", lineNo, err)
			}
			result.People++
			result.Edges += count
		default:
			return CatalogImportResult{}, fmt.Errorf("unsupported artifact kind %q", record.Kind)
		}
		result.Records++
	}
	if err := scanner.Err(); err != nil {
		return CatalogImportResult{}, fmt.Errorf("read movie catalog artifact: %w", err)
	}
	if result.Records == 0 {
		return CatalogImportResult{}, fmt.Errorf("movie catalog artifact contains no records")
	}
	if sourceURL = strings.TrimSpace(sourceURL); sourceURL != "" {
		if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO fetch_log(url,status,error) VALUES(?,?,?)`, sourceURL, "ok", ""); err != nil {
			return CatalogImportResult{}, fmt.Errorf("record movie catalog fetch: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return CatalogImportResult{}, fmt.Errorf("commit movie catalog import: %w", err)
	}
	rollback = false
	return result, nil
}

func importMovieRecord(ctx context.Context, tx *sql.Tx, record movieArtifactRecord) (int, error) {
	record.MovieID = strings.TrimSpace(record.MovieID)
	record.Title = strings.TrimSpace(record.Title)
	record.URL = strings.TrimSpace(record.URL)
	if record.MovieID == "" || record.Title == "" || record.URL == "" {
		return 0, fmt.Errorf("movie_id, title and url are required")
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO movies(movie_id,title,url,synopsis) VALUES(?,?,?,?)`, record.MovieID, record.Title, record.URL, record.Synopsis); err != nil {
		return 0, err
	}
	edges := 0
	for _, group := range []struct {
		items  []movieArtifactPerson
		source string
	}{
		{record.Cast, "movie_cast"},
		{record.Staff, "movie_staff"},
	} {
		for _, person := range group.items {
			if err := upsertMoviePersonEdge(ctx, tx, record.MovieID, record.Title, record.URL, person, group.source); err != nil {
				return 0, err
			}
			edges++
		}
	}
	return edges, nil
}

func importPersonRecord(ctx context.Context, tx *sql.Tx, record movieArtifactRecord) (int, error) {
	record.PersonID = strings.TrimSpace(record.PersonID)
	record.Name = strings.TrimSpace(record.Name)
	record.URL = strings.TrimSpace(record.URL)
	if record.PersonID == "" || record.Name == "" || record.URL == "" {
		return 0, fmt.Errorf("person_id, name and url are required")
	}
	profile, err := json.Marshal(record.Profile)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO people(person_id,name,url,profile_json,biography) VALUES(?,?,?,?,?)`, record.PersonID, record.Name, record.URL, string(profile), record.Biography); err != nil {
		return 0, err
	}
	edges := 0
	for _, group := range []struct {
		items  []movieArtifactMovie
		source string
	}{
		{record.BiographyMovies, "person_biography"},
		{record.Filmography, "person_filmography"},
	} {
		for _, movie := range group.items {
			movie.MovieID = strings.TrimSpace(movie.MovieID)
			movie.Title = strings.TrimSpace(movie.Title)
			movie.URL = strings.TrimSpace(movie.URL)
			if movie.MovieID == "" || movie.Title == "" || movie.URL == "" {
				return 0, fmt.Errorf("person edge requires movie_id, title and url")
			}
			role := strings.TrimSpace(movie.Role)
			if role == "" {
				role = group.source
			}
			if _, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
VALUES(?,?,?,?,?,?,?,?)`, movie.MovieID, record.PersonID, role, group.source, movie.Title, record.Name, movie.URL, record.URL); err != nil {
				return 0, err
			}
			edges++
		}
	}
	return edges, nil
}

func upsertMoviePersonEdge(ctx context.Context, tx *sql.Tx, movieID string, movieTitle string, movieURL string, person movieArtifactPerson, source string) error {
	person.PersonID = strings.TrimSpace(person.PersonID)
	person.Name = strings.TrimSpace(person.Name)
	person.URL = strings.TrimSpace(person.URL)
	if person.PersonID == "" || person.Name == "" || person.URL == "" {
		return fmt.Errorf("movie edge requires person_id, name and url")
	}
	role := strings.TrimSpace(person.Role)
	if role == "" {
		role = source
	}
	_, err := tx.ExecContext(ctx, `
INSERT OR REPLACE INTO movie_people(movie_id,person_id,role,source,movie_title,person_name,movie_url,person_url)
VALUES(?,?,?,?,?,?,?,?)`, movieID, person.PersonID, role, source, movieTitle, person.Name, movieURL, person.URL)
	return err
}

func ensureCatalogImportSchema(ctx context.Context, execer catalogContextExecer) error {
	_, err := execer.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS movies (
  movie_id TEXT PRIMARY KEY,
  title TEXT NOT NULL,
  url TEXT NOT NULL,
  synopsis TEXT,
  fetched_at TEXT DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS people (
  person_id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  url TEXT NOT NULL,
  profile_json TEXT,
  biography TEXT,
  fetched_at TEXT DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS movie_people (
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
CREATE TABLE IF NOT EXISTS fetch_log (
  url TEXT PRIMARY KEY,
  status TEXT NOT NULL,
  fetched_at TEXT DEFAULT CURRENT_TIMESTAMP,
  error TEXT
);`)
	return err
}
