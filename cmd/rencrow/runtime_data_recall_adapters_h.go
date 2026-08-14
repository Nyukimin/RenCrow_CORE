package main

import (
	"context"
	"fmt"
	"strings"

	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

// registerRuntimeDataRecallMovieCatalog exposes the canonical public indexed
// lookup and the owner-private candidate/request projections. Private records
// are scoped by the trusted authenticated user and never by model arguments.
func registerRuntimeDataRecallMovieCatalog(r *runtimeDataRecallRegistry, lookup *runtimeMovieCatalogLookup) error {
	if r == nil || lookup == nil {
		return fmt.Errorf("movie catalog data recall unavailable")
	}
	if err := lookup.ensureRuntimeMoviePreferenceCandidateSchema(context.Background()); err != nil {
		return err
	}
	if err := registerRuntimeDataRecallMovieCatalogPublic(r, lookup); err != nil {
		return err
	}
	if err := r.Register("movie_catalog", "preference_candidate", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeMovieCatalogRecallUserScope(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		candidate, found, err := lookup.findRuntimeMoviePreferenceCandidateByID(ctx, userID, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			if candidate.ID != q.Query || candidate.UserID != userID {
				return runtimeDataRecallResult{}, fmt.Errorf("movie preference candidate identity mismatch")
			}
			records = append(records, runtimeMoviePreferenceCandidateProjection(candidate, false))
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	}); err != nil {
		return err
	}
	return r.Register("movie_catalog", "requests", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeMovieCatalogRecallUserScope(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		candidate, found, err := lookup.findRuntimeMoviePreferenceCandidateByRequestID(ctx, userID, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			if candidate.RequestID != q.Query || candidate.UserID != userID {
				return runtimeDataRecallResult{}, fmt.Errorf("movie preference request identity mismatch")
			}
			records = append(records, runtimeMoviePreferenceCandidateProjection(candidate, true))
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallMovieCatalogPublic(r *runtimeDataRecallRegistry, lookup *runtimeMovieCatalogLookup) error {
	if err := r.Register("movie_catalog", "movies", dataRecallAccessPublic, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		value, err := lookup.Lookup(ctx, "movie", q.Query, "all", q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		result, err := runtimeMovieCatalogLookupResult(value)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, runtimeMovieCatalogPublicRecords(result)), nil
	}); err != nil {
		return err
	}
	return r.Register("movie_catalog", "people", dataRecallAccessPublic, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		value, err := lookup.Lookup(ctx, "person", q.Query, "all", q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		result, err := runtimeMovieCatalogLookupResult(value)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, runtimeMovieCatalogPublicRecords(result)), nil
	})
}

func runtimeMovieCatalogLookupResult(value any) (moviecatalogapp.LookupResult, error) {
	switch result := value.(type) {
	case moviecatalogapp.LookupResult:
		return result, nil
	case *moviecatalogapp.LookupResult:
		if result != nil {
			return *result, nil
		}
	}
	return moviecatalogapp.LookupResult{}, fmt.Errorf("movie catalog lookup returned an invalid result")
}

func runtimeMovieCatalogPublicRecords(result moviecatalogapp.LookupResult) []map[string]any {
	if strings.EqualFold(strings.TrimSpace(result.Kind), "person") {
		records := make([]map[string]any, 0, len(result.People))
		for _, item := range result.People {
			records = append(records, map[string]any{"person_id": item.PersonID, "name": item.Name, "url": item.URL})
		}
		return records
	}
	records := make([]map[string]any, 0, len(result.Movies))
	for _, item := range result.Movies {
		records = append(records, map[string]any{"movie_id": item.MovieID, "title": item.Title, "url": item.URL})
	}
	return records
}

func runtimeMovieCatalogRecallUserScope(ctx context.Context) (string, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil {
		return "", fmt.Errorf("movie catalog private recall scope is unavailable")
	}
	userID := strings.TrimSpace(scope.AuthenticatedUserID)
	if userID == "" || !scope.Allows(domaintool.DataScopeUser) {
		return "", fmt.Errorf("movie catalog private recall requires authenticated user scope")
	}
	return userID, nil
}

func runtimeMoviePreferenceCandidateProjection(candidate runtimeMoviePreferenceCandidate, includeRequest bool) map[string]any {
	record := map[string]any{
		"candidate_id": candidate.ID,
		"target_kind":  candidate.TargetKind,
		"target_id":    candidate.TargetID,
		"familiarity":  candidate.Familiarity,
		"sentiment":    candidate.Sentiment,
		"note":         candidate.Note,
		"state":        candidate.State,
		"created_at":   candidate.CreatedAt,
	}
	if includeRequest {
		record["request_id"] = candidate.RequestID
	}
	return record
}
