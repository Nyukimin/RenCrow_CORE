package categoryrecall

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	_ "modernc.org/sqlite"
)

type MovieCatalogSource struct {
	path string
}

func NewMovieCatalogSource(path string) *MovieCatalogSource {
	return &MovieCatalogSource{path: strings.TrimSpace(path)}
}

func (s *MovieCatalogSource) ID() string { return "movie_catalog" }

func (s *MovieCatalogSource) Categories() []string { return []string{"movie", "person"} }

func (s *MovieCatalogSource) Search(ctx context.Context, query domconv.CategoryRecallQuery) (domconv.CategoryRecallResult, error) {
	db, err := openReadOnlySQLite(s.path)
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	defer db.Close()
	category := normalizeCategory(query.Category)
	if category == "person" {
		return s.searchPeople(ctx, db, query)
	}
	return s.searchMovies(ctx, db, query)
}

func (s *MovieCatalogSource) searchMovies(ctx context.Context, db *sql.DB, query domconv.CategoryRecallQuery) (domconv.CategoryRecallResult, error) {
	exists, err := tableExists(db, "movies")
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	if !exists {
		return domconv.CategoryRecallResult{}, errUnavailable("movies table is missing")
	}
	if err := requireMovieLookupSchema(db, "movies", "title_lookup_key", "idx_movies_title_lookup_key"); err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	hasFetchedAt, err := tableColumnExists(db, "movies", "fetched_at")
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	fetchedAtExpr := "''"
	if hasFetchedAt {
		fetchedAtExpr = "COALESCE(fetched_at, '')"
	}
	lookupKey := strings.ToLower(strings.TrimSpace(query.Message))
	if lookupKey == "" {
		return domconv.CategoryRecallResult{}, nil
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT movie_id, title, COALESCE(synopsis, ''), COALESCE(url, ''), %s FROM movies INDEXED BY idx_movies_title_lookup_key WHERE title_lookup_key = ? ORDER BY movie_id LIMIT ?", fetchedAtExpr), lookupKey, boundedLimit(query.Limit))
	if err != nil {
		return domconv.CategoryRecallResult{}, errUnavailable("movies query failed: " + err.Error())
	}
	defer rows.Close()
	result := domconv.CategoryRecallResult{}
	for rows.Next() {
		var id, title, summary, sourceURL, fetchedAt string
		if err := rows.Scan(&id, &title, &summary, &sourceURL, &fetchedAt); err != nil {
			return result, err
		}
		title = strings.TrimSpace(title)
		summary = strings.TrimSpace(summary)
		if summary == "" {
			summary = title
		}
		retrievedAt := parseSourceTime(fetchedAt)
		result.Records = append(result.Records, domconv.CategoryRecallRecord{
			Category: normalizeCategory(query.Category), SourceID: s.ID(), RecordID: id,
			Title: title, Summary: summary, ProvenanceURLs: nonEmptyStrings(sourceURL),
			RetrievedAt: retrievedAt, ValidatedAt: retrievedAt, State: domconv.CategoryRecordStateValidated,
			Sensitivity: "normal", Scope: "public", Roles: []string{"chat", "worker", "heavy", "creative"}, Score: 1,
		})
		if len(result.Records) >= boundedLimit(query.Limit) {
			break
		}
	}
	return result, rows.Err()
}

func (s *MovieCatalogSource) searchPeople(ctx context.Context, db *sql.DB, query domconv.CategoryRecallQuery) (domconv.CategoryRecallResult, error) {
	exists, err := tableExists(db, "people")
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	if !exists {
		return domconv.CategoryRecallResult{}, errUnavailable("people table is missing")
	}
	if err := requireMovieLookupSchema(db, "people", "name_lookup_key", "idx_people_name_lookup_key"); err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	hasFetchedAt, err := tableColumnExists(db, "people", "fetched_at")
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	fetchedAtExpr := "''"
	if hasFetchedAt {
		fetchedAtExpr = "COALESCE(fetched_at, '')"
	}
	lookupKey := strings.ToLower(strings.TrimSpace(query.Message))
	if lookupKey == "" {
		return domconv.CategoryRecallResult{}, nil
	}
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT person_id, name, COALESCE(biography, ''), COALESCE(url, ''), %s FROM people INDEXED BY idx_people_name_lookup_key WHERE name_lookup_key = ? ORDER BY person_id LIMIT ?", fetchedAtExpr), lookupKey, boundedLimit(query.Limit))
	if err != nil {
		return domconv.CategoryRecallResult{}, errUnavailable("people query failed: " + err.Error())
	}
	defer rows.Close()
	result := domconv.CategoryRecallResult{}
	for rows.Next() {
		var id, name, biography, sourceURL, fetchedAt string
		if err := rows.Scan(&id, &name, &biography, &sourceURL, &fetchedAt); err != nil {
			return result, err
		}
		name = strings.TrimSpace(name)
		biography = strings.TrimSpace(biography)
		if biography == "" {
			biography = name
		}
		retrievedAt := parseSourceTime(fetchedAt)
		result.Records = append(result.Records, domconv.CategoryRecallRecord{
			Category: "person", SourceID: s.ID(), RecordID: id,
			Title: name, Summary: biography, ProvenanceURLs: nonEmptyStrings(sourceURL),
			RetrievedAt: retrievedAt, ValidatedAt: retrievedAt, State: domconv.CategoryRecordStateValidated,
			Sensitivity: "normal", Scope: "public", Roles: []string{"chat", "worker", "heavy", "creative"}, Score: 1,
		})
		if len(result.Records) >= boundedLimit(query.Limit) {
			break
		}
	}
	return result, rows.Err()
}

func (s *MovieCatalogSource) StartupEntityHints(context.Context) (map[string][]string, error) {
	return map[string][]string{}, nil
}

func requireMovieLookupSchema(db *sql.DB, table, column, index string) error {
	exists, err := tableColumnExists(db, table, column)
	if err != nil {
		return errUnavailable("lookup column check failed: " + err.Error())
	}
	if !exists {
		return errUnavailable(table + "." + column + " is missing")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name=?`, index, table).Scan(&count); err != nil {
		return errUnavailable("lookup index check failed: " + err.Error())
	}
	if count != 1 {
		return errUnavailable(index + " is missing")
	}
	return nil
}
