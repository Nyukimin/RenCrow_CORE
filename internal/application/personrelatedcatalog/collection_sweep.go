package personrelatedcatalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const CollectionSweepName = "assessed_person_d0_v1"

var CollectionSweepCategories = []string{CategoryDrama, CategoryAward, CategoryMusic, CategoryAnime, CategoryNovel, CategoryManga}

type CollectionSweepState struct {
	CursorPersonID string
	CategoryIndex  int
	NextCycleAt    time.Time
}

func LoadCollectionSweepState(ctx context.Context, db *sql.DB) (CollectionSweepState, error) {
	if err := requireHobbySchema(ctx, db); err != nil {
		return CollectionSweepState{}, err
	}
	var state CollectionSweepState
	var next string
	err := db.QueryRowContext(ctx, `SELECT cursor_person_id,category_index,next_cycle_at FROM hobby_collection_sweep_state WHERE sweep_name=?`, CollectionSweepName).Scan(&state.CursorPersonID, &state.CategoryIndex, &next)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("load collection sweep state: %w", err)
	}
	if strings.TrimSpace(next) != "" {
		state.NextCycleAt, err = time.Parse(time.RFC3339, next)
		if err != nil {
			return CollectionSweepState{}, fmt.Errorf("parse collection sweep next cycle: %w", err)
		}
	}
	return state, nil
}

func SaveCollectionSweepState(ctx context.Context, db *sql.DB, state CollectionSweepState) error {
	if state.CategoryIndex < 0 || state.CategoryIndex >= len(CollectionSweepCategories) {
		return ErrInvalidLimit
	}
	next := ""
	if !state.NextCycleAt.IsZero() {
		next = state.NextCycleAt.UTC().Format(time.RFC3339)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO hobby_collection_sweep_state(sweep_name,cursor_person_id,category_index,next_cycle_at,updated_at)
VALUES(?,?,?,?,CURRENT_TIMESTAMP)
ON CONFLICT(sweep_name) DO UPDATE SET cursor_person_id=excluded.cursor_person_id,category_index=excluded.category_index,next_cycle_at=excluded.next_cycle_at,updated_at=CURRENT_TIMESTAMP`,
		CollectionSweepName, strings.TrimSpace(state.CursorPersonID), state.CategoryIndex, next)
	if err != nil {
		return fmt.Errorf("save collection sweep state: %w", err)
	}
	return nil
}

// NextEligiblePersonByID uses the same D0 eligibility as EligiblePeople
// (explicitly assessed people only), but provides a durable ID cursor for
// bounded background collection.
func NextEligiblePersonByID(ctx context.Context, movieDB *sql.DB, afterID string) (EligiblePerson, bool, error) {
	if err := requireMovieSelectionSchema(ctx, movieDB); err != nil {
		return EligiblePerson{}, false, err
	}
	var person EligiblePerson
	err := movieDB.QueryRowContext(ctx, `
WITH eligible AS (
  SELECT target_id FROM movie_catalog_assessments WHERE kind='person' AND (familiarity='known' OR sentiment='like')
)
SELECT p.person_id,p.name,p.url,COALESCE(pa.familiarity,''),COALESCE(pa.sentiment,'')
FROM eligible e JOIN people p ON p.person_id=e.target_id
LEFT JOIN movie_catalog_assessments pa ON pa.kind='person' AND pa.target_id=p.person_id
WHERE p.person_id>? ORDER BY p.person_id LIMIT 1`, strings.TrimSpace(afterID)).Scan(
		&person.MovieCatalogPersonID, &person.Name, &person.URL, &person.Familiarity, &person.Sentiment)
	if errors.Is(err, sql.ErrNoRows) {
		return EligiblePerson{}, false, nil
	}
	if err != nil {
		return EligiblePerson{}, false, fmt.Errorf("select next D1 eligible person: %w", err)
	}
	return person, true, nil
}
