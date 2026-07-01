package moviecatalog

import (
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// QueryParams represents search/filter parameters for movies and people
type QueryParams struct {
	Query  string
	Role   string
	Source string
}

// MovieItem represents a movie in the catalog
type MovieItem struct {
	MovieID     string `json:"movie_id"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Synopsis    string `json:"synopsis"`
	PeopleCount int    `json:"people_count"`
	Watched     bool   `json:"watched"`
	WatchCount  int    `json:"watch_count"`
}

// PersonItem represents a person in the catalog
type PersonItem struct {
	PersonID          string `json:"person_id"`
	Name              string `json:"name"`
	URL               string `json:"url"`
	Profile           string `json:"profile"`
	Biography         string `json:"biography"`
	MovieCount        int    `json:"movie_count"`
	WatchedMovieCount int    `json:"watched_movie_count"`
	Favorite          bool   `json:"favorite"`
	PreferenceCount   int    `json:"preference_count"`
}

// EdgeItem represents a relationship between a movie and a person
type EdgeItem struct {
	MovieID        string `json:"movie_id"`
	MovieTitle     string `json:"movie_title"`
	MovieURL       string `json:"movie_url"`
	MovieFetched   bool   `json:"movie_fetched"`
	MovieWatched   bool   `json:"movie_watched"`
	PersonID       string `json:"person_id"`
	PersonName     string `json:"person_name"`
	PersonURL      string `json:"person_url"`
	PersonFetched  bool   `json:"person_fetched"`
	PersonFavorite bool   `json:"person_favorite"`
	Role           string `json:"role"`
	Source         string `json:"source"`
}

// WatchEventItem represents a movie watch event
type WatchEventItem struct {
	EventID       string `json:"event_id"`
	MovieID       string `json:"movie_id"`
	OriginalTitle string `json:"original_title"`
	WatchedAt     string `json:"watched_at"`
	Source        string `json:"source"`
	SourceBatchID string `json:"source_batch_id"`
	Note          string `json:"note"`
	CreatedAt     string `json:"created_at"`
}

// Stats returns catalog statistics (table counts)
func Stats(db *sql.DB) (map[string]int, error) {
	out := map[string]int{}
	for _, table := range []string{"movies", "people", "movie_people", "fetch_log"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			return nil, err
		}
		out[table] = n
	}
	for _, table := range []string{"movie_watch_events", "movie_title_observations", "movie_preference_signals", "movie_topic_candidates"} {
		if !tableExists(db, table) {
			continue
		}
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			return nil, err
		}
		out[table] = n
	}
	return out, nil
}

// Movies returns a paginated list of movies with optional filters
func Movies(db *sql.DB, params QueryParams, limit int, offset int) (int, []MovieItem, error) {
	where, args := moviesWhere(params)
	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM movies m "+where, args...).Scan(&total); err != nil {
		return 0, nil, err
	}
	args = append(args, limit, offset)
	hasWatchEvents := tableExists(db, "movie_watch_events")
	watchSelect := "0 AS watch_count"
	watchJoin := ""
	if hasWatchEvents {
		watchSelect = "COUNT(DISTINCT we.event_id) AS watch_count"
		watchJoin = "LEFT JOIN movie_watch_events we ON we.movie_id = m.movie_id"
	}
	rows, err := db.Query(`
SELECT m.movie_id, m.title, m.url, COALESCE(m.synopsis, ''),
       COUNT(DISTINCT mp.person_id) AS people_count,
       `+watchSelect+`
FROM movies m
LEFT JOIN movie_people mp ON mp.movie_id = m.movie_id
`+watchJoin+`
`+where+`
GROUP BY m.movie_id, m.title, m.url, m.synopsis
ORDER BY m.title
LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	items := []MovieItem{}
	for rows.Next() {
		var item MovieItem
		if err := rows.Scan(&item.MovieID, &item.Title, &item.URL, &item.Synopsis, &item.PeopleCount, &item.WatchCount); err != nil {
			return 0, nil, err
		}
		item.Watched = item.WatchCount > 0
		items = append(items, item)
	}
	return total, items, rows.Err()
}

func moviesWhere(params QueryParams) (string, []any) {
	conds := []string{}
	args := []any{}
	if q := strings.TrimSpace(params.Query); q != "" {
		like := "%" + q + "%"
		conds = append(conds, `(m.title LIKE ? OR m.synopsis LIKE ? OR EXISTS (
SELECT 1 FROM movie_people qmp WHERE qmp.movie_id = m.movie_id AND qmp.person_name LIKE ?
))`)
		args = append(args, like, like, like)
	}
	if role := strings.TrimSpace(params.Role); role != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM movie_people rmp WHERE rmp.movie_id = m.movie_id AND rmp.role = ?)")
		args = append(args, role)
	}
	if source := strings.TrimSpace(params.Source); source != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM movie_people smp WHERE smp.movie_id = m.movie_id AND smp.source = ?)")
		args = append(args, source)
	}
	if len(conds) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// People returns a paginated list of people with optional filters
func People(db *sql.DB, params QueryParams, limit int, offset int) (int, []PersonItem, error) {
	where, args := peopleWhere(params)
	var total int
	if err := db.QueryRow("SELECT COUNT(*) FROM people p "+where, args...).Scan(&total); err != nil {
		return 0, nil, err
	}
	args = append(args, limit, offset)
	hasWatchEvents := tableExists(db, "movie_watch_events")
	watchedMovieSelect := "0 AS watched_movie_count"
	watchJoin := ""
	if hasWatchEvents {
		watchedMovieSelect = "COUNT(DISTINCT CASE WHEN we.event_id IS NOT NULL THEN mp.movie_id END) AS watched_movie_count"
		watchJoin = "LEFT JOIN movie_watch_events we ON we.movie_id = mp.movie_id"
	}
	hasPreferenceSignals := tableExists(db, "movie_preference_signals")
	preferenceSelect := "0 AS preference_count"
	preferenceJoin := ""
	if hasPreferenceSignals {
		preferenceSelect = "COUNT(DISTINCT pref.signal_id) AS preference_count"
		preferenceJoin = `
LEFT JOIN movie_preference_signals pref
  ON pref.target_id = p.person_id
 AND pref.signal_type IN ('actor_affinity', 'person_affinity', 'director_affinity')
 AND pref.weight > 0`
	}
	rows, err := db.Query(`
SELECT p.person_id, p.name, p.url, COALESCE(p.profile_json, ''), COALESCE(p.biography, ''),
       COUNT(DISTINCT mp.movie_id) AS movie_count,
       `+watchedMovieSelect+`,
       `+preferenceSelect+`
FROM people p
LEFT JOIN movie_people mp ON mp.person_id = p.person_id
`+watchJoin+`
`+preferenceJoin+`
`+where+`
GROUP BY p.person_id, p.name, p.url, p.profile_json, p.biography
ORDER BY p.name
LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	items := []PersonItem{}
	for rows.Next() {
		var item PersonItem
		if err := rows.Scan(&item.PersonID, &item.Name, &item.URL, &item.Profile, &item.Biography, &item.MovieCount, &item.WatchedMovieCount, &item.PreferenceCount); err != nil {
			return 0, nil, err
		}
		item.Favorite = item.PreferenceCount > 0
		items = append(items, item)
	}
	return total, items, rows.Err()
}

func peopleWhere(params QueryParams) (string, []any) {
	conds := []string{}
	args := []any{}
	if q := strings.TrimSpace(params.Query); q != "" {
		like := "%" + q + "%"
		conds = append(conds, `(p.name LIKE ? OR p.biography LIKE ? OR EXISTS (
SELECT 1 FROM movie_people qmp WHERE qmp.person_id = p.person_id AND qmp.movie_title LIKE ?
))`)
		args = append(args, like, like, like)
	}
	if len(conds) == 0 {
		return "", args
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// MovieDetail returns detailed information about a specific movie
func MovieDetail(db *sql.DB, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("movie id is required")
	}
	var movie MovieItem
	if err := db.QueryRow("SELECT movie_id, title, url, COALESCE(synopsis, '') FROM movies WHERE movie_id = ?", id).Scan(&movie.MovieID, &movie.Title, &movie.URL, &movie.Synopsis); err != nil {
		return nil, err
	}
	watchEvents, err := WatchEvents(db, id, 20)
	if err != nil {
		return nil, err
	}
	movie.WatchCount = len(watchEvents)
	movie.Watched = movie.WatchCount > 0
	edges, err := Edges(db, "movie_id", id, 200)
	if err != nil {
		return nil, err
	}
	return map[string]any{"movie": movie, "links": edges, "watch_events": watchEvents}, nil
}

// PersonDetail returns detailed information about a specific person
func PersonDetail(db *sql.DB, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("person id is required")
	}
	var person PersonItem
	if err := db.QueryRow("SELECT person_id, name, url, COALESCE(profile_json, ''), COALESCE(biography, '') FROM people WHERE person_id = ?", id).Scan(&person.PersonID, &person.Name, &person.URL, &person.Profile, &person.Biography); err != nil {
		return nil, err
	}
	preferenceCount, err := personPreferenceCount(db, id)
	if err != nil {
		return nil, err
	}
	person.PreferenceCount = preferenceCount
	person.Favorite = person.PreferenceCount > 0
	edges, err := Edges(db, "person_id", id, 200)
	if err != nil {
		return nil, err
	}
	person.MovieCount = len(edges)
	for _, edge := range edges {
		if edge.MovieWatched {
			person.WatchedMovieCount++
		}
	}
	return map[string]any{"person": person, "links": edges}, nil
}

// Edges returns movie-person relationships
func Edges(db *sql.DB, column string, id string, limit int) ([]EdgeItem, error) {
	if column != "movie_id" && column != "person_id" {
		return nil, fmt.Errorf("unsupported edge column")
	}
	hasWatchEvents := tableExists(db, "movie_watch_events")
	watchSelect := "0"
	if hasWatchEvents {
		watchSelect = "EXISTS(SELECT 1 FROM movie_watch_events we WHERE we.movie_id = movie_people.movie_id)"
	}
	hasPreferenceSignals := tableExists(db, "movie_preference_signals")
	personPreferenceSelect := "0"
	if hasPreferenceSignals {
		personPreferenceSelect = `EXISTS(
SELECT 1 FROM movie_preference_signals pref
WHERE pref.target_id = movie_people.person_id
  AND pref.signal_type IN ('actor_affinity', 'person_affinity', 'director_affinity')
  AND pref.weight > 0
)`
	}
	rows, err := db.Query(`
SELECT movie_id, COALESCE(movie_title, ''), COALESCE(movie_url, ''),
       person_id, COALESCE(person_name, ''), COALESCE(person_url, ''),
       EXISTS(SELECT 1 FROM movies m WHERE m.movie_id = movie_people.movie_id),
       `+watchSelect+`,
       EXISTS(SELECT 1 FROM people p WHERE p.person_id = movie_people.person_id),
       `+personPreferenceSelect+`,
       role, source
FROM movie_people
WHERE `+column+` = ?
ORDER BY source, role, movie_title, person_name
LIMIT ?`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EdgeItem{}
	for rows.Next() {
		var item EdgeItem
		if err := rows.Scan(&item.MovieID, &item.MovieTitle, &item.MovieURL, &item.PersonID, &item.PersonName, &item.PersonURL, &item.MovieFetched, &item.MovieWatched, &item.PersonFetched, &item.PersonFavorite, &item.Role, &item.Source); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// WatchEvents returns watch events for a specific movie
func WatchEvents(db *sql.DB, movieID string, limit int) ([]WatchEventItem, error) {
	if !tableExists(db, "movie_watch_events") {
		return []WatchEventItem{}, nil
	}
	rows, err := db.Query(`
SELECT event_id, movie_id, COALESCE(original_title, ''), COALESCE(watched_at, ''),
       COALESCE(source, ''), COALESCE(source_batch_id, ''), COALESCE(note, ''), COALESCE(created_at, '')
FROM movie_watch_events
WHERE movie_id = ?
ORDER BY watched_at DESC, created_at DESC
LIMIT ?`, movieID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []WatchEventItem{}
	for rows.Next() {
		var item WatchEventItem
		if err := rows.Scan(&item.EventID, &item.MovieID, &item.OriginalTitle, &item.WatchedAt, &item.Source, &item.SourceBatchID, &item.Note, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func personPreferenceCount(db *sql.DB, personID string) (int, error) {
	if !tableExists(db, "movie_preference_signals") {
		return 0, nil
	}
	var n int
	err := db.QueryRow(`
SELECT COUNT(DISTINCT signal_id)
FROM movie_preference_signals
WHERE target_id = ?
  AND signal_type IN ('actor_affinity', 'person_affinity', 'director_affinity')
  AND weight > 0`, personID).Scan(&n)
	return n, err
}

func tableExists(db *sql.DB, name string) bool {
	var count int
	err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&count)
	return err == nil && count > 0
}

// PreferenceRequest represents a request to set/unset a preference
type PreferenceRequest struct {
	Kind        string
	TargetID    string
	TargetLabel string
	Favorite    bool
	SignalType  string
	Weight      float64
	GeneratedBy string
}

// SetPersonFavorite sets or unsets a person as favorite
func SetPersonFavorite(db *sql.DB, req PreferenceRequest) error {
	if err := initPreferenceSchema(db); err != nil {
		return err
	}
	if !req.Favorite {
		_, err := db.Exec(`
DELETE FROM movie_preference_signals
WHERE target_id = ?
  AND signal_type IN ('actor_affinity', 'person_affinity', 'director_affinity')
  AND generated_by IN ('viewer', 'user')`, req.TargetID)
		return err
	}
	evidence := map[string]any{
		"source":       "viewer",
		"target_id":    req.TargetID,
		"target_label": req.TargetLabel,
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	_, err = db.Exec(`
INSERT OR REPLACE INTO movie_preference_signals
  (signal_id, signal_type, target_id, target_label, weight, evidence_json, generated_by, generated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		preferenceSignalID(req.SignalType, req.TargetID),
		req.SignalType,
		req.TargetID,
		req.TargetLabel,
		req.Weight,
		string(evidenceJSON),
		req.GeneratedBy,
	)
	return err
}

// PersonPreferenceCount returns the preference count for a person
func PersonPreferenceCount(db *sql.DB, personID string) (int, error) {
	return personPreferenceCount(db, personID)
}

func initPreferenceSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS movie_preference_signals (
  signal_id TEXT PRIMARY KEY,
  signal_type TEXT NOT NULL,
  target_id TEXT,
  target_label TEXT NOT NULL,
  weight REAL NOT NULL DEFAULT 1.0,
  evidence_json TEXT NOT NULL,
  generated_by TEXT NOT NULL,
  generated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_movie_preference_signals_target ON movie_preference_signals(target_id);
CREATE INDEX IF NOT EXISTS idx_movie_preference_signals_type ON movie_preference_signals(signal_type);`)
	return err
}

func preferenceSignalID(signalType string, targetID string) string {
	h := sha1.New()
	h.Write([]byte(signalType + ":" + targetID))
	return fmt.Sprintf("sig_%x", h.Sum(nil)[:8])
}
