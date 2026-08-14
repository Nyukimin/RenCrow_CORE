package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	musiccatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/musiccatalog"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

// registerRuntimeDataRecallHobbyGraph exposes public indexed music catalog
// projections and exact authenticated-user candidate/receipt reads. Lyrics
// metadata is intentionally not registered until its projection contract can
// be proven not to expose restricted or full-text content.
func registerRuntimeDataRecallHobbyGraph(r *runtimeDataRecallRegistry, lookup *runtimeMusicCatalogLookup) error {
	if r == nil || lookup == nil {
		return fmt.Errorf("hobby graph data recall unavailable")
	}
	if err := ensureRuntimeHobbyPreferenceCandidateSchema(context.Background(), lookup); err != nil {
		return err
	}
	if err := registerRuntimeDataRecallHobbyGraphMusic(r, lookup, "artist", "music_artist"); err != nil {
		return err
	}
	if err := registerRuntimeDataRecallHobbyGraphMusic(r, lookup, "song", "music_song"); err != nil {
		return err
	}
	if err := registerRuntimeDataRecallHobbyGraphPreferenceCandidate(r, lookup); err != nil {
		return err
	}
	return registerRuntimeDataRecallHobbyGraphRequests(r, lookup)
}

func registerRuntimeDataRecallHobbyGraphMusic(r *runtimeDataRecallRegistry, lookup *runtimeMusicCatalogLookup, kind, operation string) error {
	if r == nil || lookup == nil || (kind != "artist" && kind != "song") || strings.TrimSpace(operation) == "" {
		return fmt.Errorf("hobby graph music recall unavailable")
	}
	return r.Register("hobby_graph", operation, dataRecallAccessPublic, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		value, err := lookup.LookupMusic(ctx, kind, q.Query, "", q.Limit)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		record, err := runtimeHobbyGraphCatalogResultRecord(value)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, []map[string]any{record}), nil
	})
}

func registerRuntimeDataRecallHobbyGraphPreferenceCandidate(r *runtimeDataRecallRegistry, lookup *runtimeMusicCatalogLookup) error {
	if r == nil || lookup == nil {
		return fmt.Errorf("hobby graph preference candidate recall unavailable")
	}
	return r.Register("hobby_graph", "preference_candidate", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeHobbyGraphPreferenceRecallUserID(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		candidate, found, err := findRuntimeHobbyPreferenceCandidate(ctx, lookup, "candidate_id", q.Query, userID)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			if candidate.CandidateID != q.Query || candidate.UserID != userID {
				return runtimeDataRecallResult{}, fmt.Errorf("hobby graph preference candidate identity mismatch")
			}
			records = append(records, runtimeHobbyPreferenceCandidateProjection(candidate))
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func registerRuntimeDataRecallHobbyGraphRequests(r *runtimeDataRecallRegistry, lookup *runtimeMusicCatalogLookup) error {
	if r == nil || lookup == nil {
		return fmt.Errorf("hobby graph preference request recall unavailable")
	}
	return r.Register("hobby_graph", "requests", dataRecallAccessUser, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		userID, err := runtimeHobbyGraphPreferenceRecallUserID(ctx)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		candidate, found, err := findRuntimeHobbyPreferenceCandidate(ctx, lookup, "request_id", q.Query, userID)
		if err != nil {
			return runtimeDataRecallResult{}, err
		}
		records := []map[string]any{}
		if found {
			if candidate.RequestID != q.Query || candidate.UserID != userID {
				return runtimeDataRecallResult{}, fmt.Errorf("hobby graph preference request identity mismatch")
			}
			records = append(records, runtimeHobbyPreferenceCandidateProjection(candidate))
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, records), nil
	})
}

func runtimeHobbyGraphPreferenceRecallUserID(ctx context.Context) (string, error) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent {
		return "", fmt.Errorf("hobby graph preference recall scope is invalid")
	}
	userID := strings.TrimSpace(scope.AuthenticatedUserID)
	if userID == "" || !scope.Allows(domaintool.DataScopeUser) {
		return "", fmt.Errorf("hobby graph preference recall requires authenticated user scope")
	}
	return userID, nil
}

func findRuntimeHobbyPreferenceCandidate(ctx context.Context, lookup *runtimeMusicCatalogLookup, column, value, userID string) (runtimeHobbyPreferenceCandidate, bool, error) {
	if lookup == nil || strings.TrimSpace(lookup.dbPath) == "" {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("hobby graph preference candidate is unavailable")
	}
	if column != "candidate_id" && column != "request_id" {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("unsupported hobby graph preference candidate lookup column")
	}
	db, err := openRuntimeMusicCatalogReadOnly(lookup.dbPath)
	if err != nil {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("open hobby graph preference recall: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("connect hobby graph preference recall: %w", err)
	}
	row := db.QueryRowContext(ctx, `
SELECT candidate_id,request_id,user_id,actor_id,payload_hash,target_item_id,signal_type,note,state,created_at
FROM hobby_agent_preference_candidate WHERE `+column+` = ? AND user_id = ?`, value, userID)
	candidate, err := scanRuntimeHobbyPreferenceCandidate(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return runtimeHobbyPreferenceCandidate{}, false, nil
		}
		return runtimeHobbyPreferenceCandidate{}, false, err
	}
	return candidate, true, nil
}

func runtimeHobbyPreferenceCandidateProjection(candidate runtimeHobbyPreferenceCandidate) map[string]any {
	return map[string]any{
		"candidate_id":   candidate.CandidateID,
		"request_id":     candidate.RequestID,
		"user_id":        candidate.UserID,
		"actor_id":       candidate.ActorID,
		"payload_hash":   candidate.PayloadHash,
		"target_item_id": candidate.TargetID,
		"signal_type":    candidate.SignalType,
		"note":           candidate.Note,
		"state":          candidate.State,
		"created_at":     candidate.CreatedAt,
	}
}

func runtimeHobbyGraphCatalogResultRecord(value any) (map[string]any, error) {
	var result musiccatalogapp.CatalogResult
	switch typed := value.(type) {
	case musiccatalogapp.CatalogResult:
		result = typed
	case *musiccatalogapp.CatalogResult:
		if typed == nil {
			return nil, fmt.Errorf("hobby graph music catalog result is nil")
		}
		result = *typed
	default:
		return nil, fmt.Errorf("hobby graph music catalog result has unexpected type %T", value)
	}
	record := map[string]any{
		"status":     result.Status,
		"kind":       result.Kind,
		"name":       result.Name,
		"artist":     result.Artist,
		"items":      append([]musiccatalogapp.CatalogItem(nil), result.Items...),
		"candidates": append([]musiccatalogapp.CatalogItem(nil), result.Candidates...),
	}
	// Keep the wrapper status/candidate fields while also exposing the one
	// resolved safe item in the conventional item-level shape used by other
	// catalog recall routes. No lyrics, rights text, raw SQL or paths exist in
	// CatalogItem.
	if len(result.Items) == 1 {
		item := result.Items[0]
		record["item_id"] = item.ItemID
		record["item_kind"] = item.Kind
		record["title"] = item.Title
		record["subtitle"] = item.Subtitle
		record["canonical_source"] = item.CanonicalSource
		record["canonical_url"] = item.CanonicalURL
		record["metadata"] = item.Metadata
		record["relations"] = item.Relations
	}
	return record, nil
}
