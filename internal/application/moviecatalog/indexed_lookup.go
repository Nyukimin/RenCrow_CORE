package moviecatalog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	DefaultLookupLimit = 10
	MaxLookupLimit     = 20
)

// LookupInputSchema is the owner-published form metadata for a frozen
// movie.catalog.lookup request. Validation and normalization remain owned by
// NormalizeLookupRequest.
func LookupInputSchema() json.RawMessage {
	value := struct {
		SchemaID string `json:"schema_id"`
		Version  int    `json:"version"`
		Fields   []any  `json:"fields"`
	}{
		SchemaID: "movie.catalog.lookup", Version: 1,
		Fields: []any{
			map[string]any{"name": "kind", "label": "対象", "help": "映画または人物を選びます。", "type": "enum", "required": true, "options": lookupKindInputOptions()},
			map[string]any{"name": "name", "label": "名前", "help": "既存カタログから完全一致で探します。", "type": "string", "required": true},
			map[string]any{"name": "information", "label": "表示情報", "help": "対象種別に合う情報を追加で読みます。", "type": "enum_by_kind", "required": false, "options_by_kind": map[string][]map[string]string{"movie": lookupInformationInputOptions("movie"), "person": lookupInformationInputOptions("person")}},
			map[string]any{"name": "limit", "label": "上限", "help": "候補と関連情報の最大件数です。", "type": "integer", "required": false, "default": DefaultLookupLimit, "minimum": 1, "maximum": MaxLookupLimit},
		},
	}
	encoded, _ := json.Marshal(value)
	return encoded
}

// LookupRequest is intentionally narrower than the Viewer fuzzy-search API.
// Name is resolved only through a normalized equality lookup.
type LookupRequest struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Information string `json:"information,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

// NormalizeLookupRequest is the shared validation and canonicalization
// contract for all exact indexed movie/person lookups. Callers can freeze its
// result before dispatch so retries never derive defaults from live state.
func NormalizeLookupRequest(req LookupRequest) (LookupRequest, error) {
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if !containsLookupInformation(LookupKinds(), kind) {
		return LookupRequest{}, fmt.Errorf("kind must be movie or person")
	}
	name := strings.TrimSpace(req.Name)
	if NormalizeLookupKey(name) == "" {
		return LookupRequest{}, fmt.Errorf("name is required")
	}
	limit := req.Limit
	if limit == 0 {
		limit = DefaultLookupLimit
	}
	if limit < 1 || limit > MaxLookupLimit {
		return LookupRequest{}, fmt.Errorf("limit must be between 1 and %d", MaxLookupLimit)
	}
	information := strings.ToLower(strings.TrimSpace(req.Information))
	if err := validateLookupInformation(kind, information); err != nil {
		return LookupRequest{}, err
	}
	return LookupRequest{Kind: kind, Name: name, Information: information, Limit: limit}, nil
}

type MovieLookupCandidate struct {
	MovieID string `json:"movie_id"`
	Title   string `json:"title"`
	URL     string `json:"url"`
}

type PersonLookupCandidate struct {
	PersonID string `json:"person_id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
}

type LookupResult struct {
	Kind        string                  `json:"kind"`
	Name        string                  `json:"name"`
	LookupKey   string                  `json:"lookup_key"`
	Information string                  `json:"information,omitempty"`
	Movies      []MovieLookupCandidate  `json:"movies,omitempty"`
	People      []PersonLookupCandidate `json:"people,omitempty"`
	Detail      map[string]any          `json:"detail,omitempty"`
	NotFound    bool                    `json:"not_found"`
	Ambiguous   bool                    `json:"ambiguous"`
}

// NormalizeLookupKey is the complete normalization contract for indexed
// catalog lookup. Import and query paths must use this same function.
func NormalizeLookupKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// EnsureIndexedLookupSchema performs the one-time migration/backfill needed by
// existing catalogs. Query paths never call this function or mutate schema.
func EnsureIndexedLookupSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("movie catalog database is nil")
	}
	for _, migration := range []struct {
		table  string
		column string
		source string
	}{
		{"movies", "title_lookup_key", "title"},
		{"people", "name_lookup_key", "name"},
	} {
		exists, err := tableColumnExists(ctx, db, migration.table, migration.column)
		if err != nil {
			return err
		}
		if !exists {
			if _, err := db.ExecContext(ctx, "ALTER TABLE "+migration.table+" ADD COLUMN "+migration.column+" TEXT NOT NULL DEFAULT ''"); err != nil {
				return fmt.Errorf("add movie catalog lookup column %s.%s: %w", migration.table, migration.column, err)
			}
		}
		rows, err := db.QueryContext(ctx, "SELECT rowid, "+migration.source+" FROM "+migration.table+" WHERE "+migration.column+" = ''")
		if err != nil {
			return fmt.Errorf("read movie catalog lookup backfill %s: %w", migration.table, err)
		}
		type update struct {
			rowID int64
			key   string
		}
		updates := []update{}
		for rows.Next() {
			var item update
			var source string
			if err := rows.Scan(&item.rowID, &source); err != nil {
				rows.Close()
				return err
			}
			item.key = NormalizeLookupKey(source)
			updates = append(updates, item)
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for _, item := range updates {
			if _, err := db.ExecContext(ctx, "UPDATE "+migration.table+" SET "+migration.column+" = ? WHERE rowid = ?", item.key, item.rowID); err != nil {
				return fmt.Errorf("backfill movie catalog lookup key %s: %w", migration.table, err)
			}
		}
	}
	_, err := db.ExecContext(ctx, `
CREATE INDEX IF NOT EXISTS idx_movies_title_lookup_key ON movies(title_lookup_key);
CREATE INDEX IF NOT EXISTS idx_people_name_lookup_key ON people(name_lookup_key);
CREATE INDEX IF NOT EXISTS idx_movie_people_person_id ON movie_people(person_id);`)
	if err != nil {
		return fmt.Errorf("create movie catalog lookup indexes: %w", err)
	}
	return nil
}

// Lookup resolves an exact normalized name and expands direct links only when
// the match is unique. Required schema/indexes are checked fail-closed.
func Lookup(db *sql.DB, req LookupRequest) (LookupResult, error) {
	if db == nil {
		return LookupResult{}, fmt.Errorf("movie catalog database is nil")
	}
	canonical, err := NormalizeLookupRequest(req)
	if err != nil {
		return LookupResult{}, err
	}
	kind, name, limit, information := canonical.Kind, canonical.Name, canonical.Limit, canonical.Information
	key := NormalizeLookupKey(name)
	if err := validateIndexedLookupSchema(db); err != nil {
		return LookupResult{}, err
	}
	result := LookupResult{Kind: kind, Name: name, LookupKey: key, Information: information}
	if kind == "movie" {
		rows, err := db.Query(`SELECT movie_id,title,url FROM movies INDEXED BY idx_movies_title_lookup_key WHERE title_lookup_key = ? ORDER BY movie_id LIMIT ?`, key, limit)
		if err != nil {
			return LookupResult{}, err
		}
		defer rows.Close()
		for rows.Next() {
			var item MovieLookupCandidate
			if err := rows.Scan(&item.MovieID, &item.Title, &item.URL); err != nil {
				return LookupResult{}, err
			}
			result.Movies = append(result.Movies, item)
		}
		if err := rows.Err(); err != nil {
			return LookupResult{}, err
		}
		result.NotFound, result.Ambiguous = len(result.Movies) == 0, len(result.Movies) > 1
		if len(result.Movies) == 1 {
			result.Detail, err = MovieDetail(db, result.Movies[0].MovieID)
			if err != nil {
				return LookupResult{}, err
			}
			result.Detail = projectMovieLookupDetail(result.Detail, information, limit)
		}
		return result, nil
	}
	rows, err := db.Query(`SELECT person_id,name,url FROM people INDEXED BY idx_people_name_lookup_key WHERE name_lookup_key = ? ORDER BY person_id LIMIT ?`, key, limit)
	if err != nil {
		return LookupResult{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item PersonLookupCandidate
		if err := rows.Scan(&item.PersonID, &item.Name, &item.URL); err != nil {
			return LookupResult{}, err
		}
		result.People = append(result.People, item)
	}
	if err := rows.Err(); err != nil {
		return LookupResult{}, err
	}
	result.NotFound, result.Ambiguous = len(result.People) == 0, len(result.People) > 1
	if len(result.People) == 1 {
		result.Detail, err = PersonDetail(db, result.People[0].PersonID)
		if err != nil {
			return LookupResult{}, err
		}
		result.Detail = projectPersonLookupDetail(result.Detail, information, limit)
	}
	return result, nil
}

func validateLookupInformation(kind, information string) error {
	if containsLookupInformation(LookupInformationOptions(kind), information) {
		return nil
	}
	return fmt.Errorf("information %q is not valid for kind %q", information, kind)
}

func LookupInformationOptions(kind string) []string {
	switch kind {
	case "movie":
		return []string{"", "overview", "cast", "staff"}
	case "person":
		return []string{"", "profile", "filmography"}
	default:
		return nil
	}
}

func LookupKinds() []string { return []string{"movie", "person"} }

func lookupKindInputOptions() []map[string]string {
	return []map[string]string{{"value": "movie", "label": "映画"}, {"value": "person", "label": "人物"}}
}

func lookupInformationInputOptions(kind string) []map[string]string {
	labels := map[string]string{
		"": "基本情報", "overview": "概要", "cast": "出演者", "staff": "スタッフ",
		"profile": "プロフィール", "filmography": "出演作品",
	}
	options := LookupInformationOptions(kind)
	result := make([]map[string]string, 0, len(options))
	for _, value := range options {
		result = append(result, map[string]string{"value": value, "label": labels[value]})
	}
	return result
}

func containsLookupInformation(options []string, value string) bool {
	for _, option := range options {
		if option == value {
			return true
		}
	}
	return false
}

func projectMovieLookupDetail(detail map[string]any, information string, limit int) map[string]any {
	if information == "" {
		limitLookupLinks(detail, limit)
		return detail
	}
	projected := map[string]any{"movie": detail["movie"]}
	if information == "overview" {
		movie, _ := detail["movie"].(MovieItem)
		projected["information_available"] = strings.TrimSpace(movie.Synopsis) != ""
		return projected
	}
	links, _ := detail["links"].([]EdgeItem)
	selected := make([]EdgeItem, 0, min(limit, len(links)))
	for _, link := range links {
		cast := isMovieCastEdge(link)
		if (information == "cast" && !cast) || (information == "staff" && cast) {
			continue
		}
		selected = append(selected, link)
		if len(selected) >= limit {
			break
		}
	}
	projected["links"] = selected
	projected["information_available"] = len(selected) > 0
	return projected
}

func projectPersonLookupDetail(detail map[string]any, information string, limit int) map[string]any {
	if information == "" {
		limitLookupLinks(detail, limit)
		return detail
	}
	projected := map[string]any{"person": detail["person"]}
	if information == "profile" {
		person, _ := detail["person"].(PersonItem)
		profile := strings.TrimSpace(person.Profile)
		projected["information_available"] = strings.TrimSpace(person.Biography) != "" || (profile != "" && profile != "{}")
		return projected
	}
	links, _ := detail["links"].([]EdgeItem)
	if len(links) > limit {
		links = links[:limit]
	}
	projected["links"] = links
	projected["information_available"] = len(links) > 0
	return projected
}

func limitLookupLinks(detail map[string]any, limit int) {
	links, ok := detail["links"].([]EdgeItem)
	if ok && len(links) > limit {
		detail["links"] = links[:limit]
	}
}

func isMovieCastEdge(edge EdgeItem) bool {
	value := strings.ToLower(strings.TrimSpace(edge.Role + " " + edge.Source))
	for _, marker := range []string{"actor", "cast", "出演", "俳優", "声優"} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func tableColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &dataType, &notnull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func validateIndexedLookupSchema(db *sql.DB) error {
	for _, check := range []struct{ kind, name string }{
		{"column", "movies.title_lookup_key"}, {"column", "people.name_lookup_key"},
		{"index", "idx_movies_title_lookup_key"}, {"index", "idx_people_name_lookup_key"},
		{"index", "idx_movie_people_person_id"},
	} {
		var count int
		var err error
		if check.kind == "index" {
			err = db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=?`, check.name).Scan(&count)
		} else {
			parts := strings.Split(check.name, ".")
			var exists bool
			exists, err = tableColumnExists(context.Background(), db, parts[0], parts[1])
			if exists {
				count = 1
			}
		}
		if err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required movie catalog lookup %s %s is missing", check.kind, check.name)
		}
	}
	return nil
}
