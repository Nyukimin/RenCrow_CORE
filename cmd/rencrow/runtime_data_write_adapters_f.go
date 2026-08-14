package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	dciapp "github.com/Nyukimin/RenCrow_CORE/internal/application/dci"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeDCISearchIDPrefix = "dci-search/sha256:"

type runtimeDCISearchStore interface {
	FindSearchTraceByID(context.Context, string) (domaindci.SearchTrace, bool, error)
}

type runtimeDCISearcher interface {
	SearchWithIdentity(context.Context, string, string, string) (domaindci.SearchResult, error)
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
	actor := strings.TrimSpace(scope.ActorID)
	eventID := runtimeDataWriteDerivedID(runtimeDCISearchIDPrefix, scope.RequestID)

	w.mu.Lock()
	defer w.mu.Unlock()

	existing, found, err := w.store.FindSearchTraceByID(ctx, eventID)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if err := domaindci.ValidateSearchTrace(existing); err != nil {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("dci existing search trace is invalid: %w", err)
		}
		if strings.TrimSpace(existing.UserQuery) != payload.Query || strings.TrimSpace(existing.Actor) != actor || strings.TrimSpace(existing.Mode) != "dci" {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("dci search idempotency payload mismatch")
		}
		return runtimeDataWriteOwnerResult{
			SchemaVersion:    "dci-search/v1",
			MigrationState:   "embedded_current",
			ValidationState:  "owner_validated",
			AuditRef:         existing.EventID,
			IdempotencyKey:   scope.RequestID,
			IdempotentReplay: true,
			PolicyRevision:   runtimeDataWritePolicyRevision,
		}, nil
	}

	result, err := w.searcher.SearchWithIdentity(ctx, payload.Query, eventID, actor)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeDCISearchResult(result, payload.Query, eventID, actor); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "dci-search/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         result.Trace.EventID,
		IdempotencyKey:   scope.RequestID,
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

func validateRuntimeDCISearchResult(result domaindci.SearchResult, query, eventID, actor string) error {
	trace := result.Trace
	if trace.EventID != eventID || trace.UserQuery != query || trace.Actor != actor || trace.Mode != "dci" {
		return fmt.Errorf("dci searcher returned an identity-mismatched trace")
	}
	if result.Pack.EventID != eventID || result.Pack.Query != query {
		return fmt.Errorf("dci searcher returned an identity-mismatched evidence pack")
	}
	if err := domaindci.ValidateSearchTrace(trace); err != nil {
		return fmt.Errorf("dci searcher returned an invalid trace: %w", err)
	}
	return nil
}

var _ runtimeDCISearcher = (*dciapp.Explorer)(nil)
