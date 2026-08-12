package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	personrelatedcatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	_ "modernc.org/sqlite"
)

type runtimePersonRelatedCatalogLookup struct {
	movieCatalogPath string
	hobbyGraphPath   string
}

func (l *runtimePersonRelatedCatalogLookup) EligiblePeople(ctx context.Context, limit int) ([]personrelatedcatalogapp.EligiblePerson, error) {
	if l == nil || strings.TrimSpace(l.movieCatalogPath) == "" {
		return nil, fmt.Errorf("person related catalog lookup is unavailable")
	}
	movieDB, err := openRuntimeMovieCatalogReadOnly(l.movieCatalogPath)
	if err != nil {
		return nil, fmt.Errorf("open movie catalog read-only for eligible people: %w", err)
	}
	defer movieDB.Close()
	movieDB.SetMaxOpenConns(1)
	return personrelatedcatalogapp.EligiblePeople(ctx, movieDB, limit)
}

// prepareRuntimePersonRelatedCatalogLookup is the startup-only migration
// boundary. Both databases must be present and migratable before the unified
// query-only Tool is exposed.
func prepareRuntimePersonRelatedCatalogLookup(ctx context.Context, movieCatalogPath, hobbyGraphPath string) (*runtimePersonRelatedCatalogLookup, error) {
	movieCatalogPath, err := resolveRuntimePersonRelatedCatalogDatabasePath(movieCatalogPath, "movie catalog")
	if err != nil {
		return nil, err
	}
	hobbyGraphPath, err = resolveRuntimePersonRelatedCatalogWritableDatabasePath(hobbyGraphPath, "hobby graph")
	if err != nil {
		return nil, err
	}
	movieDB, err := openRuntimePersonRelatedCatalogReadWrite(movieCatalogPath)
	if err != nil {
		return nil, fmt.Errorf("open movie catalog for person related catalog migration: %w", err)
	}
	defer movieDB.Close()
	hobbyDB, err := openRuntimePersonRelatedCatalogReadWrite(hobbyGraphPath)
	if err != nil {
		return nil, fmt.Errorf("open hobby graph for person related catalog migration: %w", err)
	}
	defer hobbyDB.Close()
	movieDB.SetMaxOpenConns(1)
	hobbyDB.SetMaxOpenConns(1)
	if err := movieDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect movie catalog for person related catalog migration: %w", err)
	}
	if err := hobbyDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect hobby graph for person related catalog migration: %w", err)
	}
	if err := moviecatalogapp.EnsureIndexedLookupSchema(ctx, movieDB); err != nil {
		return nil, fmt.Errorf("prepare movie catalog indexed lookup for person related catalog: %w", err)
	}
	if err := personrelatedcatalogapp.EnsureSchema(ctx, movieDB, hobbyDB); err != nil {
		return nil, fmt.Errorf("prepare person related catalog schema: %w", err)
	}
	return &runtimePersonRelatedCatalogLookup{movieCatalogPath: movieCatalogPath, hobbyGraphPath: hobbyGraphPath}, nil
}

func (l *runtimePersonRelatedCatalogLookup) Lookup(ctx context.Context, personName, category string, limit int) (any, error) {
	if l == nil || strings.TrimSpace(l.movieCatalogPath) == "" || strings.TrimSpace(l.hobbyGraphPath) == "" {
		return nil, fmt.Errorf("person related catalog lookup is unavailable")
	}
	if !validRuntimePersonRelatedCatalogCategory(category) {
		return nil, fmt.Errorf("person related catalog category %q is invalid", category)
	}
	movieDB, err := openRuntimeMovieCatalogReadOnly(l.movieCatalogPath)
	if err != nil {
		return nil, fmt.Errorf("open movie catalog read-only: %w", err)
	}
	defer movieDB.Close()
	movieDB.SetMaxOpenConns(1)
	if err := movieDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect movie catalog read-only: %w", err)
	}

	if category == personrelatedcatalogapp.CategoryMovie {
		resolution, err := moviecatalogapp.Lookup(movieDB, moviecatalogapp.LookupRequest{
			Kind: "person", Name: personName, Information: "profile", Limit: 2,
		})
		if err != nil {
			return nil, fmt.Errorf("movie catalog person resolution: %w", err)
		}
		if _, err := resolveRuntimePersonID(personName, resolution); err != nil {
			return nil, err
		}
		movieLimit := limit
		if movieLimit > 20 {
			movieLimit = 20
		}
		result, err := moviecatalogapp.Lookup(movieDB, moviecatalogapp.LookupRequest{
			Kind: "person", Name: personName, Information: "filmography", Limit: movieLimit,
		})
		if err != nil {
			return nil, fmt.Errorf("movie catalog filmography lookup: %w", err)
		}
		personID, err := runtimePersonIDFromUniqueResult(personName, result)
		if err != nil {
			return nil, err
		}
		movieResult, err := runtimeMovieRelatedCatalogResult(ctx, movieDB, personID, result)
		if err != nil {
			return nil, err
		}
		return movieResult, nil
	}

	personResult, err := moviecatalogapp.Lookup(movieDB, moviecatalogapp.LookupRequest{
		Kind: "person", Name: personName, Information: "profile", Limit: 2,
	})
	if err != nil {
		return nil, fmt.Errorf("movie catalog person resolution: %w", err)
	}
	personID, err := resolveRuntimePersonID(personName, personResult)
	if err != nil {
		return nil, err
	}
	hobbyDB, err := openRuntimePersonRelatedCatalogReadOnly(l.hobbyGraphPath)
	if err != nil {
		return nil, fmt.Errorf("open hobby graph read-only: %w", err)
	}
	defer hobbyDB.Close()
	// LookupWithCoverage enriches each relation while its bounded relation
	// cursor is open; retain read-only access but allow that nested query to
	// use a second connection instead of waiting on the single cursor.
	hobbyDB.SetMaxOpenConns(2)
	if err := hobbyDB.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect hobby graph read-only: %w", err)
	}
	items, err := personrelatedcatalogapp.LookupWithCoverage(ctx, hobbyDB, personID, category, limit)
	if err != nil {
		return nil, fmt.Errorf("hobby graph indexed lookup: %w", err)
	}
	return items, nil
}

// LookupByPersonID is the read-only Viewer projection boundary. Unlike the
// Tool-facing name lookup above, it never resolves a name or performs a
// collection; the selected movie-catalog person ID is used exactly as given.
func (l *runtimePersonRelatedCatalogLookup) LookupByPersonID(ctx context.Context, personID, category string, limit int) (personrelatedcatalogapp.LookupResult, error) {
	if l == nil || strings.TrimSpace(l.movieCatalogPath) == "" || strings.TrimSpace(l.hobbyGraphPath) == "" {
		return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("person related catalog lookup is unavailable")
	}
	personID = strings.TrimSpace(personID)
	if personID == "" {
		return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("person id is required")
	}
	if !validRuntimePersonRelatedCatalogCategory(category) {
		return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("person related catalog category %q is invalid", category)
	}
	if limit < 1 || limit > 50 {
		return personrelatedcatalogapp.LookupResult{}, personrelatedcatalogapp.ErrInvalidLimit
	}
	if ctx == nil {
		ctx = context.Background()
	}

	movieDB, err := openRuntimeMovieCatalogReadOnly(l.movieCatalogPath)
	if err != nil {
		return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("open movie catalog read-only: %w", err)
	}
	defer movieDB.Close()
	movieDB.SetMaxOpenConns(1)
	if err := movieDB.PingContext(ctx); err != nil {
		return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("connect movie catalog read-only: %w", err)
	}
	var personName, personURL string
	if err := movieDB.QueryRowContext(ctx, `SELECT name, url FROM people WHERE person_id = ?`, personID).Scan(&personName, &personURL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return personrelatedcatalogapp.LookupResult{}, &tools.PersonRelatedCatalogNotFoundError{PersonName: personID}
		}
		return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("resolve movie catalog person: %w", err)
	}
	if category == personrelatedcatalogapp.CategoryMovie {
		rows, err := movieDB.QueryContext(ctx, `
SELECT mp.movie_id, COALESCE(mp.movie_title, m.title), COALESCE(mp.movie_url, m.url),
       mp.role, mp.source, COALESCE(mp.person_name, ?), COALESCE(mp.person_url, ?)
FROM movie_people AS mp
LEFT JOIN movies AS m ON m.movie_id = mp.movie_id
WHERE mp.person_id = ?
ORDER BY COALESCE(mp.movie_title, m.title), mp.role, mp.source
LIMIT ?`, personName, personURL, personID, limit)
		if err != nil {
			return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("lookup movie catalog filmography: %w", err)
		}
		defer rows.Close()
		links := make([]moviecatalogapp.EdgeItem, 0, limit)
		for rows.Next() {
			var edge moviecatalogapp.EdgeItem
			if err := rows.Scan(&edge.MovieID, &edge.MovieTitle, &edge.MovieURL, &edge.Role, &edge.Source, &edge.PersonName, &edge.PersonURL); err != nil {
				return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("scan movie catalog filmography: %w", err)
			}
			edge.PersonID = personID
			edge.MovieFetched = true
			edge.PersonFetched = true
			links = append(links, edge)
		}
		if err := rows.Err(); err != nil {
			return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("read movie catalog filmography: %w", err)
		}
		return runtimeMovieRelatedCatalogResult(ctx, movieDB, personID, moviecatalogapp.LookupResult{
			Detail: map[string]any{"links": links},
		})
	}

	hobbyDB, err := openRuntimePersonRelatedCatalogReadOnly(l.hobbyGraphPath)
	if err != nil {
		return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("open hobby graph read-only: %w", err)
	}
	defer hobbyDB.Close()
	hobbyDB.SetMaxOpenConns(1)
	if err := hobbyDB.PingContext(ctx); err != nil {
		return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("connect hobby graph read-only: %w", err)
	}
	return personrelatedcatalogapp.LookupWithCoverage(ctx, hobbyDB, personID, category, limit)
}

func runtimePersonIDFromUniqueResult(personName string, result moviecatalogapp.LookupResult) (string, error) {
	if result.NotFound || len(result.People) == 0 {
		return "", &tools.PersonRelatedCatalogNotFoundError{PersonName: personName}
	}
	if result.Ambiguous || len(result.People) > 1 {
		return "", &tools.PersonRelatedCatalogAmbiguousError{Candidates: runtimePersonCandidates(result.People)}
	}
	return strings.TrimSpace(result.People[0].PersonID), nil
}

func runtimeMovieRelatedCatalogResult(ctx context.Context, movieDB *sql.DB, personID string, result moviecatalogapp.LookupResult) (personrelatedcatalogapp.LookupResult, error) {
	items := []personrelatedcatalogapp.RelatedCatalogItem{}
	links, _ := result.Detail["links"].([]moviecatalogapp.EdgeItem)
	for _, edge := range links {
		item := personrelatedcatalogapp.RelatedCatalogItem{
			PersonRefID:          personID,
			MovieCatalogPersonID: personID,
			Category:             personrelatedcatalogapp.CategoryMovie,
			RelationType:         edge.Role,
			Source:               edge.Source,
			EvidenceURL:          edge.PersonURL,
			ValidationState:      "validated",
			ItemID:               edge.MovieID,
			ItemType:             "movie",
			DisplayName:          edge.MovieTitle,
			NameOriginal:         edge.MovieTitle,
			NameState:            "original",
			SourceRecordID:       edge.MovieID,
			CanonicalURL:         edge.MovieURL,
		}
		if item.DisplayName == "" {
			item.DisplayName = item.ItemID
		}
		if item.NameOriginal == "" {
			item.NameOriginal = item.DisplayName
		}
		var synopsis string
		if strings.TrimSpace(edge.MovieID) != "" {
			err := movieDB.QueryRowContext(ctx, `SELECT COALESCE(synopsis, '') FROM movies WHERE movie_id = ?`, edge.MovieID).Scan(&synopsis)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return personrelatedcatalogapp.LookupResult{}, fmt.Errorf("movie synopsis lookup: %w", err)
			}
		}
		if strings.TrimSpace(synopsis) != "" {
			item.SummaryJA = synopsis
			item.SummaryState = "source_summary"
			item.SummarySourceURL = edge.MovieURL
		} else {
			item.SummaryState = "unavailable"
		}
		items = append(items, item)
	}
	coverage := personrelatedcatalogapp.SummaryCoverage{Total: len(items)}
	for _, item := range items {
		if item.SummaryState == "source_summary" || item.SummaryState == "translated_summary" {
			coverage.Ready++
		} else {
			coverage.Unavailable++
		}
	}
	return personrelatedcatalogapp.LookupResult{Items: items, SummaryCoverage: coverage}, nil
}

func runtimePersonLookupResult(result moviecatalogapp.LookupResult) (any, error) {
	if result.NotFound || len(result.People) == 0 {
		return nil, &tools.PersonRelatedCatalogNotFoundError{PersonName: result.Name}
	}
	if result.Ambiguous || len(result.People) > 1 {
		return nil, &tools.PersonRelatedCatalogAmbiguousError{Candidates: runtimePersonCandidates(result.People)}
	}
	return result, nil
}

func resolveRuntimePersonID(personName string, result moviecatalogapp.LookupResult) (string, error) {
	if result.NotFound || len(result.People) == 0 {
		return "", &tools.PersonRelatedCatalogNotFoundError{PersonName: personName}
	}
	if result.Ambiguous || len(result.People) > 1 {
		return "", &tools.PersonRelatedCatalogAmbiguousError{Candidates: runtimePersonCandidates(result.People)}
	}
	personID := strings.TrimSpace(result.People[0].PersonID)
	if personID == "" {
		return "", fmt.Errorf("movie catalog person resolution returned an empty person id")
	}
	return personID, nil
}

func runtimePersonCandidates(people []moviecatalogapp.PersonLookupCandidate) []tools.PersonRelatedCatalogCandidate {
	candidates := make([]tools.PersonRelatedCatalogCandidate, 0, len(people))
	for _, person := range people {
		candidates = append(candidates, tools.PersonRelatedCatalogCandidate{PersonID: person.PersonID, Name: person.Name, URL: person.URL})
	}
	return candidates
}

func validRuntimePersonRelatedCatalogCategory(category string) bool {
	switch category {
	case personrelatedcatalogapp.CategoryMovie, personrelatedcatalogapp.CategoryDrama,
		personrelatedcatalogapp.CategoryAward, personrelatedcatalogapp.CategoryMusic,
		personrelatedcatalogapp.CategoryAnime, personrelatedcatalogapp.CategoryNovel,
		personrelatedcatalogapp.CategoryManga:
		return true
	default:
		return false
	}
}

func resolveRuntimePersonRelatedCatalogDatabasePath(configuredPath, label string) (string, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return "", fmt.Errorf("%s database path is not configured", label)
	}
	absPath, err := filepath.Abs(configuredPath)
	if err != nil {
		return "", fmt.Errorf("resolve %s database path: %w", label, err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return "", fmt.Errorf("stat %s database: %w", label, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s database path is a directory", label)
	}
	return absPath, nil
}

func resolveRuntimePersonRelatedCatalogWritableDatabasePath(configuredPath, label string) (string, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return "", fmt.Errorf("%s database path is not configured", label)
	}
	absPath, err := filepath.Abs(configuredPath)
	if err != nil {
		return "", fmt.Errorf("resolve %s database path: %w", label, err)
	}
	info, err := os.Stat(absPath)
	if err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("%s database path is a directory", label)
		}
		return absPath, nil
	}
	if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat %s database: %w", label, err)
	}
	parentInfo, parentErr := os.Stat(filepath.Dir(absPath))
	if parentErr != nil {
		return "", fmt.Errorf("stat %s database parent: %w", label, parentErr)
	}
	if !parentInfo.IsDir() {
		return "", fmt.Errorf("%s database parent is not a directory", label)
	}
	return absPath, nil
}

func openRuntimePersonRelatedCatalogReadWrite(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+dbPath+"?_time_format=sqlite&_pragma=busy_timeout(5000)")
}

func openRuntimePersonRelatedCatalogReadOnly(dbPath string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_time_format=sqlite")
}
