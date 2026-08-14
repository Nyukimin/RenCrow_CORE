package main

import (
	"context"
	"fmt"

	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	domainglossary "github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type runtimeGlossaryLookupExecutor interface {
	Lookup(context.Context, string, string, string, int) (any, error)
}

type runtimeGlossaryCandidateFinder interface {
	FindCandidateByID(context.Context, string) (domainglossary.GlossaryCandidate, bool, error)
}

// registerRuntimeDataRecallGlossary registers the canonical indexed reads and,
// when supplied, the exact internal candidate lookup. The variadic form keeps
// the canonical read usable while candidate storage is unavailable.
func registerRuntimeDataRecallGlossary(r *runtimeDataRecallRegistry, lookup runtimeGlossaryLookupExecutor, candidates ...runtimeGlossaryCandidateFinder) error {
	if err := registerRuntimeDataRecallGlossaryLookup(r, lookup); err != nil {
		return err
	}
	if len(candidates) == 0 || candidates[0] == nil {
		return nil
	}
	return registerRuntimeDataRecallGlossaryCandidates(r, candidates[0])
}

func registerRuntimeDataRecallGlossaryLookup(r *runtimeDataRecallRegistry, lookup runtimeGlossaryLookupExecutor) error {
	if r == nil || lookup == nil {
		return fmt.Errorf("glossary lookup unavailable")
	}
	if err := r.Register("glossary", "define_term", dataRecallAccessPublic, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		value, err := lookup.Lookup(ctx, "define_term", q.Query, "", q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		result, err := runtimeGlossaryLookupResult(value)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, runtimeGlossaryItemRecords(result)), nil
	}); err != nil {
		return err
	}
	return r.Register("glossary", "list_category", dataRecallAccessPublic, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		value, err := lookup.Lookup(ctx, "list_category", "", q.Query, q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		result, err := runtimeGlossaryLookupResult(value)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, runtimeGlossaryItemRecords(result)), nil
	})
}

func registerRuntimeDataRecallGlossaryCandidates(r *runtimeDataRecallRegistry, candidates runtimeGlossaryCandidateFinder) error {
	if r == nil || candidates == nil {
		return fmt.Errorf("glossary candidate recall unavailable")
	}
	return r.Register("glossary", "candidates", dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		candidate, found, err := candidates.FindCandidateByID(ctx, q.Query)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			if candidate.ID != q.Query {
				return runtimeDataRecallResult{}, fmt.Errorf("glossary candidate id mismatch")
			}
			if err := domainglossary.ValidateGlossaryCandidate(candidate); err != nil {
				return runtimeDataRecallResult{}, fmt.Errorf("stored glossary candidate is invalid: %w", err)
			}
			records = append(records, map[string]any{
				"candidate_id": candidate.ID, "term": candidate.Term, "explanation": candidate.Explanation,
				"source_url": candidate.SourceURL, "category": candidate.Category,
				"proposed_by": candidate.ProposedBy, "state": candidate.State, "created_at": candidate.CreatedAt,
			})
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func runtimeGlossaryLookupResult(value any) (glossaryapp.LookupResult, error) {
	switch result := value.(type) {
	case glossaryapp.LookupResult:
		return result, nil
	case *glossaryapp.LookupResult:
		if result != nil {
			return *result, nil
		}
	}
	return glossaryapp.LookupResult{}, fmt.Errorf("glossary lookup returned an invalid result")
}

func runtimeGlossaryItemRecords(result glossaryapp.LookupResult) []map[string]any {
	records := make([]map[string]any, 0, len(result.Items))
	for _, item := range result.Items {
		records = append(records, map[string]any{
			"id": item.ID, "term": item.Term, "explanation": item.Explanation,
			"source": item.Source, "category": item.Category, "updated_at": item.UpdatedAt,
		})
	}
	return records
}

var _ runtimeGlossaryLookupExecutor = (*runtimeGlossaryLookup)(nil)
