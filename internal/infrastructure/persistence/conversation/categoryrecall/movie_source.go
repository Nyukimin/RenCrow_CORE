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
	hasFetchedAt, err := tableColumnExists(db, "movies", "fetched_at")
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	fetchedAtExpr := "''"
	if hasFetchedAt {
		fetchedAtExpr = "COALESCE(fetched_at, '')"
	}
	predicate, args := lexicalPredicate(query.Message, "title", "synopsis")
	args = append(args, boundedLimit(query.Limit))
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT movie_id, title, COALESCE(synopsis, ''), COALESCE(url, ''), %s FROM movies WHERE %s ORDER BY title LIMIT ?", fetchedAtExpr, predicate), args...)
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
		if !queryMatches(query.Message, title, summary) {
			continue
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
	hasFetchedAt, err := tableColumnExists(db, "people", "fetched_at")
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	fetchedAtExpr := "''"
	if hasFetchedAt {
		fetchedAtExpr = "COALESCE(fetched_at, '')"
	}
	predicate, args := lexicalPredicate(query.Message, "name", "biography")
	args = append(args, boundedLimit(query.Limit))
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT person_id, name, COALESCE(biography, ''), COALESCE(url, ''), %s FROM people WHERE %s ORDER BY name LIMIT ?", fetchedAtExpr, predicate), args...)
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
		if !queryMatches(query.Message, name, biography) {
			continue
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

func (s *MovieCatalogSource) StartupEntityHints(ctx context.Context) (map[string][]string, error) {
	db, err := openReadOnlySQLite(s.path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	hints := map[string][]string{}
	if exists, _ := tableExists(db, "movies"); exists {
		rows, err := db.QueryContext(ctx, `SELECT title FROM movies WHERE TRIM(title) <> '' ORDER BY title`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var title string
			if err := rows.Scan(&title); err != nil {
				rows.Close()
				return nil, err
			}
			hints["movie"] = append(hints["movie"], strings.TrimSpace(title))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	if exists, _ := tableExists(db, "people"); exists {
		rows, err := db.QueryContext(ctx, `SELECT name FROM people WHERE TRIM(name) <> '' ORDER BY name`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return nil, err
			}
			hints["person"] = append(hints["person"], strings.TrimSpace(name))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return hints, nil
}
