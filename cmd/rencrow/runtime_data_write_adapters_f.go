package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	dciapp "github.com/Nyukimin/RenCrow_CORE/internal/application/dci"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type runtimeDCISearchStore interface {
	FindSearchResultByIdempotencyKey(context.Context, string) (domaindci.SearchResult, bool, error)
}

type runtimeDCISearcher interface {
	SearchAs(context.Context, string, modulecore.TaskID, modulecore.RunID, string, string, string) (domaindci.SearchResult, error)
}

type runtimeDCISearchWritePayload struct {
	Query string `json:"query"`
}

type runtimeDCISearchWriter struct {
	mu       sync.Mutex
	store    runtimeDCISearchStore
	searcher runtimeDCISearcher
}

func registerRuntimeDataWriteDCI(r *runtimeDataWriteRegistry, store runtimeDCISearchStore, searcher runtimeDCISearcher) error {
	if r == nil || store == nil || searcher == nil {
		return fmt.Errorf("dci data write unavailable")
	}
	writer := &runtimeDCISearchWriter{store: store, searcher: searcher}
	return r.RegisterWithContract("dci", "search", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"query"},
	}, writer.write)
}

func (w *runtimeDCISearchWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeDCISearchPayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	actorKind := string(scope.ActorKind)
	actorID := scope.ActorID
	if err := domaindci.ValidateActor(actorKind, actorID); err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("dci owner actor: %w", err)
	}
	idempotencyKey := scope.RequestID

	w.mu.Lock()
	defer w.mu.Unlock()

	existing, found, err := w.store.FindSearchResultByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if err := validateRuntimeDCISearchResult(existing, payload.Query, existing.Trace.TraceID, existing.Trace.ActionID, actorKind, actorID, idempotencyKey); err != nil {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("dci existing search result is invalid: %w", err)
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion:    "dci-search/v2",
			MigrationState:   "embedded_current",
			ValidationState:  "owner_validated",
			AuditRef:         string(existing.Trace.ActionID),
			IdempotencyKey:   idempotencyKey,
			IdempotentReplay: true,
			PolicyRevision:   runtimeDataWritePolicyRevision,
		}, nil
	}

	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("dci execution identity: %w", err)
	}
	result, err := w.searcher.SearchAs(ctx, payload.Query, identity.TaskID, identity.RunID, actorKind, actorID, idempotencyKey)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeDCISearchResult(result, payload.Query, result.Trace.TraceID, result.Trace.ActionID, actorKind, actorID, idempotencyKey); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "dci-search/v2",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         string(result.Trace.ActionID),
		IdempotencyKey:   idempotencyKey,
		IdempotentReplay: false,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}, nil
}

func decodeRuntimeDCISearchPayload(payload map[string]any) (runtimeDCISearchWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{"query": {}}); err != nil {
		return runtimeDCISearchWritePayload{}, err
	}
	var decoded runtimeDCISearchWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeDCISearchWritePayload{}, err
	}
	decoded.Query = strings.TrimSpace(decoded.Query)
	if decoded.Query == "" {
		return runtimeDCISearchWritePayload{}, fmt.Errorf("query is required")
	}
	return decoded, nil
}

func validateRuntimeDCISearchResult(result domaindci.SearchResult, query string, traceID modulecore.TraceID, actionID modulecore.ActionID, actorKind, actorID, idempotencyKey string) error {
	if err := domaindci.ValidateSearchResult(result); err != nil {
		return fmt.Errorf("dci searcher returned an invalid result: %w", err)
	}
	trace := result.Trace
	if trace.TraceID != traceID || trace.ActionID != actionID || trace.UserQuery != query || trace.ActorAttribution != domaindci.ActorAttributionAuthenticated || trace.ActorKind != actorKind || trace.ActorID != actorID || trace.IdempotencyKey != idempotencyKey || trace.Mode != "dci" {
		return fmt.Errorf("dci searcher returned an identity-mismatched trace")
	}
	if result.Pack.ActionID != actionID || result.Pack.Query != query || result.Pack.ActionID != trace.ActionID {
		return fmt.Errorf("dci searcher returned an identity-mismatched evidence pack")
	}
	return nil
}

var _ runtimeDCISearcher = (*dciapp.OwnedSearcher)(nil)
