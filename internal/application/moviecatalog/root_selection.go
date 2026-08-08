package moviecatalog

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CardRoot is a D0 source used by the deterministic card projection.  Root
// IDs are source identifiers (assessment target IDs or imported manifest
// IDs), not persisted graph depth values.
type CardRoot struct {
	RootID          string   `json:"root_id"`
	RootIDs         []string `json:"root_ids"`
	Kind            string   `json:"kind"`
	TargetID        string   `json:"target_id"`
	TargetLabel     string   `json:"target_label"`
	TargetURL       string   `json:"target_url"`
	Familiarity     string   `json:"familiarity,omitempty"`
	Sentiment       string   `json:"sentiment,omitempty"`
	ValidationState string   `json:"validation_state"`
	ProvenanceURLs  []string `json:"provenance_urls"`
	Source          string   `json:"source"`
}

// RootSelection is an exported alias for callers that prefer a descriptive
// name when inspecting D0 eligibility before projecting cards.
type RootSelection = CardRoot

// SelectRoots returns only positive D0 roots.  An assessment row is an
// explicit user decision even when both dimensions are empty or negative, so
// fallback history/signals are considered only when no row exists.
func SelectRoots(db *sql.DB, limit int) ([]CardRoot, error) {
	if db == nil {
		return nil, fmt.Errorf("movie catalog database is nil")
	}
	if !tableExists(db, "movies") || !tableExists(db, "people") {
		return nil, fmt.Errorf("movie catalog tables are not initialized")
	}
	assessments, err := loadCatalogAssessments(db)
	if err != nil {
		return nil, err
	}
	roots := map[string]CardRoot{}
	add := func(root CardRoot) {
		root.Kind = strings.ToLower(strings.TrimSpace(root.Kind))
		root.TargetID = strings.TrimSpace(root.TargetID)
		if (root.Kind != "movie" && root.Kind != "person") || root.TargetID == "" {
			return
		}
		if root.ValidationState == "" {
			root.ValidationState = "confirmed"
		}
		if len(root.RootIDs) == 0 {
			root.RootID = strings.TrimSpace(root.RootID)
			if root.RootID == "" {
				root.RootID = root.TargetID
			}
			root.RootIDs = []string{root.RootID}
		}
		root.RootIDs = sortedUniqueStrings(root.RootIDs)
		if root.RootID == "" && len(root.RootIDs) > 0 {
			root.RootID = root.RootIDs[0]
		}
		key := root.Kind + "\x00" + root.TargetID
		if previous, ok := roots[key]; ok {
			previous.RootIDs = sortedUniqueStrings(append(previous.RootIDs, root.RootIDs...))
			if previous.Familiarity == "" {
				previous.Familiarity = root.Familiarity
			}
			if previous.Sentiment == "" {
				previous.Sentiment = root.Sentiment
			}
			if previous.TargetLabel == "" {
				previous.TargetLabel = root.TargetLabel
			}
			if previous.TargetURL == "" {
				previous.TargetURL = root.TargetURL
			}
			previous.ProvenanceURLs = sortedUniqueStrings(append(previous.ProvenanceURLs, root.ProvenanceURLs...))
			if previous.Source == "" {
				previous.Source = root.Source
			} else if root.Source != "" && !strings.Contains(previous.Source, root.Source) {
				previous.Source += "," + root.Source
			}
			if root.ValidationState != "" && previous.ValidationState == "" {
				previous.ValidationState = root.ValidationState
			}
			roots[key] = previous
			return
		}
		root.ProvenanceURLs = sortedUniqueStrings(root.ProvenanceURLs)
		roots[key] = root
	}

	if err := addAssessmentRoots(db, assessments, add); err != nil {
		return nil, err
	}
	if err := addFallbackRoots(db, assessments, add); err != nil {
		return nil, err
	}
	if tableExists(db, "movie_catalog_roots") {
		if err := addImportedRoots(db, add); err != nil {
			return nil, err
		}
	}

	out := make([]CardRoot, 0, len(roots))
	for _, root := range roots {
		out = append(out, root)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].TargetLabel != out[j].TargetLabel {
			return out[i].TargetLabel < out[j].TargetLabel
		}
		return out[i].TargetID < out[j].TargetID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type catalogAssessmentRow struct {
	Kind        string
	TargetID    string
	Familiarity string
	Sentiment   string
}

func loadCatalogAssessments(db *sql.DB) (map[string]catalogAssessmentRow, error) {
	rowsByKey := map[string]catalogAssessmentRow{}
	if !tableExists(db, "movie_catalog_assessments") {
		return rowsByKey, nil
	}
	rows, err := db.Query(`SELECT kind,target_id,familiarity,sentiment FROM movie_catalog_assessments`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row catalogAssessmentRow
		if err := rows.Scan(&row.Kind, &row.TargetID, &row.Familiarity, &row.Sentiment); err != nil {
			return nil, err
		}
		row.Kind = strings.ToLower(strings.TrimSpace(row.Kind))
		row.TargetID = strings.TrimSpace(row.TargetID)
		rowsByKey[row.Kind+"\x00"+row.TargetID] = row
	}
	return rowsByKey, rows.Err()
}

func addAssessmentRoots(db *sql.DB, assessments map[string]catalogAssessmentRow, add func(CardRoot)) error {
	rows, err := db.Query(`SELECT movie_id,title,url FROM movies ORDER BY movie_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, label, targetURL string
		if err := rows.Scan(&id, &label, &targetURL); err != nil {
			rows.Close()
			return err
		}
		assessment, exists := assessments["movie\x00"+id]
		if !exists || !positiveMovieAssessment(assessment) {
			continue
		}
		add(CardRoot{RootID: id, Kind: "movie", TargetID: id, TargetLabel: label, TargetURL: targetURL, Familiarity: assessment.Familiarity, Sentiment: assessment.Sentiment, ValidationState: "confirmed", Source: "assessment", ProvenanceURLs: []string{targetURL}})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	rows, err = db.Query(`SELECT person_id,name,url FROM people ORDER BY person_id`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, label, targetURL string
		if err := rows.Scan(&id, &label, &targetURL); err != nil {
			rows.Close()
			return err
		}
		assessment, exists := assessments["person\x00"+id]
		if !exists || !positivePersonAssessment(assessment) {
			continue
		}
		add(CardRoot{RootID: id, Kind: "person", TargetID: id, TargetLabel: label, TargetURL: targetURL, Familiarity: assessment.Familiarity, Sentiment: assessment.Sentiment, ValidationState: "confirmed", Source: "assessment", ProvenanceURLs: []string{targetURL}})
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func addFallbackRoots(db *sql.DB, assessments map[string]catalogAssessmentRow, add func(CardRoot)) error {
	if tableExists(db, "movie_watch_events") {
		rows, err := db.Query(`
SELECT m.movie_id,m.title,m.url
FROM movies m
WHERE NOT EXISTS (SELECT 1 FROM movie_catalog_assessments a WHERE a.kind='movie' AND a.target_id=m.movie_id)
  AND EXISTS (SELECT 1 FROM movie_watch_events w WHERE w.movie_id=m.movie_id)
ORDER BY m.movie_id`)
		if err != nil {
			// A database without the assessment table is valid; use a query
			// without that optional reference in that case.
			if !tableExists(db, "movie_catalog_assessments") {
				rows, err = db.Query(`SELECT m.movie_id,m.title,m.url FROM movies m WHERE EXISTS (SELECT 1 FROM movie_watch_events w WHERE w.movie_id=m.movie_id) ORDER BY m.movie_id`)
			}
		}
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, label, targetURL string
			if err := rows.Scan(&id, &label, &targetURL); err != nil {
				rows.Close()
				return err
			}
			if _, explicit := assessments["movie\x00"+id]; explicit {
				continue
			}
			add(CardRoot{RootID: id, Kind: "movie", TargetID: id, TargetLabel: label, TargetURL: targetURL, Familiarity: "seen", ValidationState: "confirmed", Source: "watch_fallback", ProvenanceURLs: []string{targetURL}})
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	if !tableExists(db, "movie_preference_signals") {
		return nil
	}
	rows, err := db.Query(`
SELECT p.person_id,p.name,p.url
FROM people p
WHERE NOT EXISTS (SELECT 1 FROM movie_catalog_assessments a WHERE a.kind='person' AND a.target_id=p.person_id)
  AND EXISTS (
    SELECT 1 FROM movie_preference_signals s
    WHERE s.target_id=p.person_id
      AND s.signal_type IN ('actor_affinity','person_affinity','director_affinity')
      AND s.weight > 0
  )
ORDER BY p.person_id`)
	if err != nil {
		if !tableExists(db, "movie_catalog_assessments") {
			rows, err = db.Query(`
SELECT p.person_id,p.name,p.url
FROM people p
WHERE EXISTS (
  SELECT 1 FROM movie_preference_signals s
  WHERE s.target_id=p.person_id
    AND s.signal_type IN ('actor_affinity','person_affinity','director_affinity')
    AND s.weight > 0
)
ORDER BY p.person_id`)
		}
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, label, targetURL string
		if err := rows.Scan(&id, &label, &targetURL); err != nil {
			return err
		}
		if _, explicit := assessments["person\x00"+id]; explicit {
			continue
		}
		add(CardRoot{RootID: id, Kind: "person", TargetID: id, TargetLabel: label, TargetURL: targetURL, Sentiment: "like", ValidationState: "confirmed", Source: "favorite_fallback", ProvenanceURLs: []string{targetURL}})
	}
	return rows.Err()
}

func addImportedRoots(db *sql.DB, add func(CardRoot)) error {
	rows, err := db.Query(`SELECT root_id,kind,target_id,target_label,target_url,validation_state,provenance_json,source_url FROM movie_catalog_roots ORDER BY root_id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var root CardRoot
		var provenanceJSON, sourceURL string
		if err := rows.Scan(&root.RootID, &root.Kind, &root.TargetID, &root.TargetLabel, &root.TargetURL, &root.ValidationState, &provenanceJSON, &sourceURL); err != nil {
			return err
		}
		root.RootID = strings.TrimSpace(root.RootID)
		root.RootIDs = []string{root.RootID}
		root.Source = "explicit_fetch"
		if sourceURL != "" {
			root.Source = "explicit_fetch"
		}
		if err := json.Unmarshal([]byte(provenanceJSON), &root.ProvenanceURLs); err != nil {
			return fmt.Errorf("decode root %q provenance: %w", root.RootID, err)
		}
		if !rootProjectionStateAllowed(root.ValidationState) {
			continue
		}
		if !targetExists(db, root.Kind, root.TargetID) {
			continue
		}
		if root.TargetLabel == "" || root.TargetURL == "" {
			label, targetURL, err := catalogTarget(db, root.Kind, root.TargetID)
			if err != nil {
				return err
			}
			root.TargetLabel, root.TargetURL = label, targetURL
		}
		add(root)
	}
	return rows.Err()
}

func positiveMovieAssessment(row catalogAssessmentRow) bool {
	return row.Familiarity == "seen" || row.Sentiment == "like"
}

func positivePersonAssessment(row catalogAssessmentRow) bool {
	return row.Familiarity == "known" || row.Sentiment == "like"
}

func targetExists(db *sql.DB, kind, id string) bool {
	var count int
	table, column := "movies", "movie_id"
	if kind == "person" {
		table, column = "people", "person_id"
	}
	if kind != "movie" && kind != "person" {
		return false
	}
	return db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+column+"=?", id).Scan(&count) == nil && count > 0
}

func catalogTarget(db *sql.DB, kind, id string) (string, string, error) {
	if kind == "movie" {
		var label, targetURL string
		err := db.QueryRow(`SELECT title,url FROM movies WHERE movie_id=?`, id).Scan(&label, &targetURL)
		return label, targetURL, err
	}
	var label, targetURL string
	err := db.QueryRow(`SELECT name,url FROM people WHERE person_id=?`, id).Scan(&label, &targetURL)
	return label, targetURL, err
}

func projectionStateAllowed(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "validated", "confirmed", "partial", "unresolved", "ready", "active":
		return true
	default:
		return false
	}
}

func rootProjectionStateAllowed(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "validated", "confirmed", "ready", "active":
		return true
	default:
		return false
	}
}

func sortedUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}
