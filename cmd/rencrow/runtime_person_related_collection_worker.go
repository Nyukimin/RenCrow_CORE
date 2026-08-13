package main

import (
	"context"
	"fmt"
	"time"

	personrelatedcatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
)

const (
	runtimePersonCollectionInterval = 30 * time.Second
	runtimePersonCollectionCycle    = 24 * time.Hour
)

type runtimePersonRelatedCollectionWorker struct {
	collector        *runtimePersonRelatedCatalogCollector
	movieCatalogPath string
	hobbyGraphPath   string
	interval         time.Duration
	now              func() time.Time
}

type runtimePersonRelatedCollectionRunResult struct {
	PersonID  string
	Category  string
	Advanced  bool
	CycleDone bool
}

type personRelatedCollectionRunner interface {
	RunOnce(context.Context) (runtimePersonRelatedCollectionRunResult, error)
	Interval() time.Duration
}

func prepareRuntimePersonRelatedCollectionWorker(collector *runtimePersonRelatedCatalogCollector, now func() time.Time) (*runtimePersonRelatedCollectionWorker, error) {
	if collector == nil {
		return nil, fmt.Errorf("person related collection worker collector is unavailable")
	}
	if now == nil {
		now = time.Now
	}
	return &runtimePersonRelatedCollectionWorker{
		collector: collector, movieCatalogPath: collector.movieCatalogPath, hobbyGraphPath: collector.hobbyGraphPath,
		interval: runtimePersonCollectionInterval, now: now,
	}, nil
}

func (w *runtimePersonRelatedCollectionWorker) Interval() time.Duration {
	if w == nil || w.interval <= 0 {
		return runtimePersonCollectionInterval
	}
	return w.interval
}

func startRuntimePersonRelatedCollectionWorker(runner personRelatedCollectionRunner, reporter backgroundJobFailureReporter) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if runner == nil {
		return cancel
	}
	go func() {
		run := func() {
			if _, err := runner.RunOnce(ctx); err != nil && ctx.Err() == nil {
				reporter.Failed("person_related_collection", err, "bounded D1 person category collection failed")
			}
		}
		run()
		ticker := time.NewTicker(runner.Interval())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
	return cancel
}

func (w *runtimePersonRelatedCollectionWorker) RunOnce(ctx context.Context) (runtimePersonRelatedCollectionRunResult, error) {
	var result runtimePersonRelatedCollectionRunResult
	if w == nil || w.collector == nil {
		return result, fmt.Errorf("person related collection worker is unavailable")
	}
	now := w.now().UTC()
	movieDB, err := openRuntimeMovieCatalogReadOnly(w.movieCatalogPath)
	if err != nil {
		return result, err
	}
	defer movieDB.Close()
	movieDB.SetMaxOpenConns(1)
	hobbyDB, err := openRuntimePersonRelatedCatalogReadWrite(w.hobbyGraphPath)
	if err != nil {
		return result, err
	}
	defer hobbyDB.Close()
	hobbyDB.SetMaxOpenConns(1)
	if err := personrelatedcatalog.EnsureHobbySchema(ctx, hobbyDB); err != nil {
		return result, err
	}
	state, err := personrelatedcatalog.LoadCollectionSweepState(ctx, hobbyDB)
	if err != nil {
		return result, err
	}
	if !state.NextCycleAt.IsZero() && now.Before(state.NextCycleAt) {
		return result, nil
	}
	var person personrelatedcatalog.EligiblePerson
	var found bool
	if state.CategoryIndex > 0 {
		person, found, err = personrelatedcatalog.EligiblePersonByID(ctx, movieDB, state.CursorPersonID)
	} else {
		person, found, err = personrelatedcatalog.NextEligiblePersonByID(ctx, movieDB, state.CursorPersonID)
	}
	if err != nil {
		return result, err
	}
	if !found {
		state = personrelatedcatalog.CollectionSweepState{NextCycleAt: now.Add(runtimePersonCollectionCycle)}
		if err := personrelatedcatalog.SaveCollectionSweepState(ctx, hobbyDB, state); err != nil {
			return result, err
		}
		result.CycleDone = true
		return result, nil
	}
	category := personrelatedcatalog.CollectionSweepCategories[state.CategoryIndex]
	result.PersonID, result.Category = person.MovieCatalogPersonID, category
	_, collectErr := w.collector.Collect(ctx, person.Name, category)
	state.CursorPersonID = person.MovieCatalogPersonID
	state.NextCycleAt = time.Time{}
	state.CategoryIndex++
	if state.CategoryIndex >= len(personrelatedcatalog.CollectionSweepCategories) {
		state.CategoryIndex = 0
	}
	if err := personrelatedcatalog.SaveCollectionSweepState(ctx, hobbyDB, state); err != nil {
		return result, err
	}
	result.Advanced = true
	if collectErr != nil {
		return result, collectErr
	}
	return result, nil
}
