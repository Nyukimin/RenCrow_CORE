package moviecatalog

import (
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

var ErrInvalidAssessment = errors.New("invalid movie catalog assessment")

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
	Familiarity string `json:"familiarity"`
	Sentiment   string `json:"sentiment"`
	Assessed    bool   `json:"assessed"`
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
	Familiarity       string `json:"familiarity"`
	Sentiment         string `json:"sentiment"`
	Assessed          bool   `json:"assessed"`
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
	assessmentSelect := "'' AS familiarity, '' AS sentiment, 0 AS assessed"
	assessmentJoin := ""
	assessmentGroup := ""
	if tableExists(db, "movie_catalog_assessments") {
		assessmentSelect = "COALESCE(a.familiarity, '') AS familiarity, COALESCE(a.sentiment, '') AS sentiment, CASE WHEN a.target_id IS NULL THEN 0 ELSE 1 END AS assessed"
		assessmentJoin = "LEFT JOIN movie_catalog_assessments a ON a.kind = 'movie' AND a.target_id = m.movie_id"
		assessmentGroup = ", a.target_id, a.familiarity, a.sentiment"
	}
	rows, err := db.Query(`
SELECT m.movie_id, m.title, m.url, COALESCE(m.synopsis, ''),
       COUNT(DISTINCT mp.person_id) AS people_count,
       `+watchSelect+`,
       `+assessmentSelect+`
FROM movies m
LEFT JOIN movie_people mp ON mp.movie_id = m.movie_id
`+watchJoin+`
`+assessmentJoin+`
`+where+`
GROUP BY m.movie_id, m.title, m.url, m.synopsis`+assessmentGroup+`
ORDER BY m.title
LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	items := []MovieItem{}
	for rows.Next() {
		var item MovieItem
		if err := rows.Scan(&item.MovieID, &item.Title, &item.URL, &item.Synopsis, &item.PeopleCount, &item.WatchCount, &item.Familiarity, &item.Sentiment, &item.Assessed); err != nil {
			return 0, nil, err
		}
		item.Watched = item.WatchCount > 0
		if !item.Assessed && item.Familiarity == "" && item.Watched {
			item.Familiarity = "seen"
		}
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
	assessmentSelect := "'' AS familiarity, '' AS sentiment, 0 AS assessed"
	assessmentJoin := ""
	assessmentGroup := ""
	if tableExists(db, "movie_catalog_assessments") {
		assessmentSelect = "COALESCE(a.familiarity, '') AS familiarity, COALESCE(a.sentiment, '') AS sentiment, CASE WHEN a.target_id IS NULL THEN 0 ELSE 1 END AS assessed"
		assessmentJoin = "LEFT JOIN movie_catalog_assessments a ON a.kind = 'person' AND a.target_id = p.person_id"
		assessmentGroup = ", a.target_id, a.familiarity, a.sentiment"
	}
	rows, err := db.Query(`
SELECT p.person_id, p.name, p.url, COALESCE(p.profile_json, ''), COALESCE(p.biography, ''),
       COUNT(DISTINCT mp.movie_id) AS movie_count,
       `+watchedMovieSelect+`,
       `+preferenceSelect+`,
       `+assessmentSelect+`
FROM people p
LEFT JOIN movie_people mp ON mp.person_id = p.person_id
`+watchJoin+`
`+preferenceJoin+`
`+assessmentJoin+`
`+where+`
GROUP BY p.person_id, p.name, p.url, p.profile_json, p.biography`+assessmentGroup+`
ORDER BY p.name
LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	items := []PersonItem{}
	for rows.Next() {
		var item PersonItem
		if err := rows.Scan(&item.PersonID, &item.Name, &item.URL, &item.Profile, &item.Biography, &item.MovieCount, &item.WatchedMovieCount, &item.PreferenceCount, &item.Familiarity, &item.Sentiment, &item.Assessed); err != nil {
			return 0, nil, err
		}
		item.Favorite = item.PreferenceCount > 0
		if !item.Assessed && item.Sentiment == "" && item.Favorite {
			item.Sentiment = "like"
		}
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
	if role := strings.TrimSpace(params.Role); role != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM movie_people rmp WHERE rmp.person_id = p.person_id AND rmp.role = ?)")
		args = append(args, role)
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
	assessment, err := AssessmentFor(db, "movie", id)
	if err != nil {
		return nil, err
	}
	movie.Familiarity = assessment.Familiarity
	movie.Sentiment = assessment.Sentiment
	movie.Assessed = assessment.Assessed
	if !movie.Assessed && movie.Familiarity == "" && movie.Watched {
		movie.Familiarity = "seen"
	}
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
	assessment, err := AssessmentFor(db, "person", id)
	if err != nil {
		return nil, err
	}
	person.Familiarity = assessment.Familiarity
	person.Sentiment = assessment.Sentiment
	person.Assessed = assessment.Assessed
	if !person.Assessed && person.Sentiment == "" && person.Favorite {
		person.Sentiment = "like"
	}
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

// Assessment stores the two user-controlled dimensions shown in the Viewer grid.
type Assessment struct {
	Familiarity string `json:"familiarity"`
	Sentiment   string `json:"sentiment"`
	Assessed    bool   `json:"assessed"`
}

// AssessmentRequest represents one checkbox-group update from the Viewer.
type AssessmentRequest struct {
	Kind        string
	TargetID    string
	TargetLabel string
	Dimension   string
	Value       string
	UpdatedBy   string
}

// SetAssessment updates one dimension while preserving the other dimension.
func SetAssessment(db *sql.DB, req AssessmentRequest) error {
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	req.TargetID = strings.TrimSpace(req.TargetID)
	req.TargetLabel = strings.TrimSpace(req.TargetLabel)
	req.Dimension = strings.ToLower(strings.TrimSpace(req.Dimension))
	req.Value = strings.ToLower(strings.TrimSpace(req.Value))
	req.UpdatedBy = strings.TrimSpace(req.UpdatedBy)
	if req.UpdatedBy == "" {
		req.UpdatedBy = "viewer"
	}
	if req.Kind != "movie" && req.Kind != "person" {
		return fmt.Errorf("%w: kind must be movie or person", ErrInvalidAssessment)
	}
	if req.TargetID == "" {
		return fmt.Errorf("%w: target_id is required", ErrInvalidAssessment)
	}
	if req.TargetLabel == "" {
		req.TargetLabel = req.TargetID
	}
	if req.Dimension != "familiarity" && req.Dimension != "sentiment" {
		return fmt.Errorf("%w: dimension must be familiarity or sentiment", ErrInvalidAssessment)
	}
	if err := validateAssessmentValue(req.Kind, req.Dimension, req.Value); err != nil {
		return err
	}
	if err := initAssessmentSchema(db); err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	table := "movies"
	idColumn := "movie_id"
	if req.Kind == "person" {
		table = "people"
		idColumn = "person_id"
	}
	var exists int
	if err := tx.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+idColumn+" = ?", req.TargetID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("%w: target does not exist", ErrInvalidAssessment)
	}

	familiarity := ""
	sentiment := ""
	if req.Dimension == "familiarity" {
		familiarity = req.Value
	} else {
		sentiment = req.Value
	}
	updateColumn := req.Dimension
	_, err = tx.Exec(`
INSERT INTO movie_catalog_assessments
  (kind, target_id, target_label, familiarity, sentiment, updated_by, updated_at)
VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
ON CONFLICT(kind, target_id) DO UPDATE SET
  target_label = excluded.target_label,
  `+updateColumn+` = excluded.`+updateColumn+`,
  updated_by = excluded.updated_by,
  updated_at = CURRENT_TIMESTAMP`,
		req.Kind, req.TargetID, req.TargetLabel, familiarity, sentiment, req.UpdatedBy)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// AssessmentFor returns a stored assessment or an empty assessment.
func AssessmentFor(db *sql.DB, kind string, targetID string) (Assessment, error) {
	if !tableExists(db, "movie_catalog_assessments") {
		return Assessment{}, nil
	}
	var out Assessment
	err := db.QueryRow(`
SELECT familiarity, sentiment
FROM movie_catalog_assessments
WHERE kind = ? AND target_id = ?`, kind, targetID).Scan(&out.Familiarity, &out.Sentiment)
	if errors.Is(err, sql.ErrNoRows) {
		return Assessment{}, nil
	}
	out.Assessed = err == nil
	return out, err
}

func validateAssessmentValue(kind string, dimension string, value string) error {
	if value == "" {
		return nil
	}
	if dimension == "sentiment" {
		if value == "like" || value == "dislike" {
			return nil
		}
		return fmt.Errorf("%w: sentiment must be like, dislike, or empty", ErrInvalidAssessment)
	}
	if kind == "movie" && (value == "seen" || value == "unseen") {
		return nil
	}
	if kind == "person" && (value == "known" || value == "unknown") {
		return nil
	}
	return fmt.Errorf("%w: unsupported familiarity value", ErrInvalidAssessment)
}

func initAssessmentSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS movie_catalog_assessments (
  kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  target_label TEXT NOT NULL,
  familiarity TEXT NOT NULL DEFAULT '',
  sentiment TEXT NOT NULL DEFAULT '',
  updated_by TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(kind, target_id)
);
CREATE INDEX IF NOT EXISTS idx_movie_catalog_assessments_target
  ON movie_catalog_assessments(kind, target_id);`)
	return err
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
