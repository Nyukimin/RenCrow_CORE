package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dciapp "github.com/Nyukimin/RenCrow_CORE/internal/application/dci"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	eventpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeDataWriteDCIOwnerE2EThroughWorkerAndSQLite(t *testing.T) {
	corpus := t.TempDir()
	if err := writeDCIAdapterTestFile(filepath.Join(corpus, "spec.md"), "DCI owner evidence\n"); err != nil {
		t.Fatal(err)
	}
	store, events := newDCIAdapterStores(t)
	explorer := dciapp.NewExplorer(dciapp.Config{
		Enabled:      true,
		Allowlist:    []string{corpus},
		ActorKind:    "agent",
		ActorID:      "shiro",
		MaxEvidence:  2,
		MaxFilesRead: 2,
		Now:          func() time.Time { return time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC) },
	}, store, dciapp.WithEventAppender(events))
	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDCI(writeRegistry, store, explorer); err != nil {
		t.Fatalf("register dci write: %v", err)
	}
	routes := writeRegistry.Snapshot()
	var foundRoute *runtimeDataWriteRoute
	for i := range routes {
		if routes[i].Store == "dci" && routes[i].Operation == "search" {
			foundRoute = &routes[i]
			break
		}
	}
	if foundRoute == nil || foundRoute.Access != dataRecallAccessInternal || len(foundRoute.RequiredPayloadFields) != 1 || foundRoute.RequiredPayloadFields[0] != "query" || len(foundRoute.OptionalPayloadFields) != 0 {
		t.Fatalf("dci route contract=%#v", foundRoute)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, DisableToolHarness: true})
	ctx := runtimeDataWriteBContext(t, "dci-owner-1")
	payload := map[string]any{"query": "  DCI owner  "}
	first := dciAdapterExecuteWrite(t, worker, ctx, payload)
	if first.IdempotentReplay || first.SchemaVersion != "dci-search/v2" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.AuditRef == "" || first.IdempotencyKey != "dci-owner-1" || first.PolicyRevision != runtimeDataWritePolicyRevision {
		t.Fatalf("first dci receipt=%#v", first)
	}
	actionID := modulecore.ActionID(first.AuditRef)
	if err := actionID.Validate(); err != nil {
		t.Fatalf("AuditRef is not an ActionID: %v", err)
	}
	if strings.ContainsAny(first.AuditRef, "/:") {
		t.Fatalf("AuditRef uses a non-canonical request-derived format: %q", first.AuditRef)
	}
	stored, found, err := store.FindSearchResultByActionID(ctx, actionID)
	if err != nil || !found {
		t.Fatalf("stored dci result found=%v err=%v", found, err)
	}
	if err := domaindci.ValidateSearchResult(stored); err != nil {
		t.Fatalf("stored dci result invalid: %v", err)
	}
	if err := stored.Trace.TraceID.Validate(); err != nil {
		t.Fatalf("stored trace ID invalid: %v", err)
	}
	if stored.Trace.ActionID != actionID || stored.Pack.ActionID != actionID || stored.Trace.UserQuery != "DCI owner" || stored.Pack.Query != "DCI owner" || stored.Trace.ActorAttribution != domaindci.ActorAttributionAuthenticated || stored.Trace.ActorKind != "agent" || stored.Trace.ActorID != "shiro" || stored.Trace.IdempotencyKey != "dci-owner-1" || stored.Trace.Mode != "dci" {
		t.Fatalf("stored dci identity=%#v", stored)
	}
	if string(stored.Trace.TraceID) == first.AuditRef {
		t.Fatalf("trace ID and ActionID must be distinct: %q", first.AuditRef)
	}
	if len(stored.Pack.Evidence) == 0 {
		t.Fatalf("stored dci result has no evidence")
	}
	assertDCIAdapterEventGraph(t, events, stored)
	eventsBeforeReplay, err := events.ListByComponent(ctx, "dci", 100)
	if err != nil {
		t.Fatalf("list dci events before replay: %v", err)
	}
	tracesBeforeReplay, err := store.ListRecent(10)
	if err != nil || len(tracesBeforeReplay) != 1 {
		t.Fatalf("dci traces before replay=%#v err=%v", tracesBeforeReplay, err)
	}
	second := dciAdapterExecuteWrite(t, worker, ctx, payload)
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef || second.SchemaVersion != "dci-search/v2" {
		t.Fatalf("dci replay receipt=%#v first=%#v", second, first)
	}
	eventsAfterReplay, err := events.ListByComponent(ctx, "dci", 100)
	if err != nil || len(eventsAfterReplay) != len(eventsBeforeReplay) {
		t.Fatalf("dci replay appended events before=%d after=%d err=%v", len(eventsBeforeReplay), len(eventsAfterReplay), err)
	}
	tracesAfterReplay, err := store.ListRecent(10)
	if err != nil || len(tracesAfterReplay) != len(tracesBeforeReplay) {
		t.Fatalf("dci replay duplicated traces before=%d after=%d err=%v", len(tracesBeforeReplay), len(tracesAfterReplay), err)
	}
	changed := dciAdapterClonePayload(payload)
	changed["query"] = "different query"
	if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "dci", "operation": "search", "payload": changed}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("dci conflict response=%#v err=%v", response, err)
	}
	eventsAfterConflict, err := events.ListByComponent(ctx, "dci", 100)
	if err != nil || len(eventsAfterConflict) != len(eventsBeforeReplay) {
		t.Fatalf("dci conflict mutated events=%d before=%d err=%v", len(eventsAfterConflict), len(eventsBeforeReplay), err)
	}
	tracesAfterConflict, err := store.ListRecent(10)
	if err != nil || len(tracesAfterConflict) != 1 || tracesAfterConflict[0].UserQuery != "DCI owner" {
		t.Fatalf("dci conflict mutated traces=%#v err=%v", tracesAfterConflict, err)
	}
}

func TestRuntimeDataWriteDCIRejectsForbiddenPayloadFields(t *testing.T) {
	store, events := newDCIAdapterStores(t)
	explorer := dciapp.NewExplorer(dciapp.Config{Enabled: true, Allowlist: []string{t.TempDir()}, ActorKind: "agent", ActorID: "shiro"}, store, dciapp.WithEventAppender(events))
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDCI(registry, store, explorer); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	ctx := runtimeDataWriteBContext(t, "dci-owner-invalid")
	for _, extra := range []string{"action_id", "trace_id", "actor_id", "actor_kind", "event_id", "mode", "corpus_scope", "path", "created_at", "request_id", "unknown"} {
		payload := map[string]any{"query": "dci", extra: "model-owned"}
		response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "dci", "operation": "search", "payload": payload})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("extra field %q response=%#v err=%v", extra, response, err)
		}
	}
	if traces, err := store.ListRecent(10); err != nil || len(traces) != 0 {
		t.Fatalf("forbidden dci payload mutated traces=%#v err=%v", traces, err)
	}
	if events, err := events.ListByComponent(ctx, "dci", 10); err != nil || len(events) != 0 {
		t.Fatalf("forbidden dci payload appended events=%#v err=%v", events, err)
	}
}

type dciAdapterFindStore struct {
	result  domaindci.SearchResult
	found   bool
	err     error
	gotKey  string
	lookups int
}

func (s *dciAdapterFindStore) FindSearchResultByIdempotencyKey(_ context.Context, key string) (domaindci.SearchResult, bool, error) {
	s.gotKey = key
	s.lookups++
	return s.result, s.found, s.err
}

type dciAdapterSearcherFunc func(context.Context, string, modulecore.TraceID, modulecore.ActionID, string, string, string) (domaindci.SearchResult, error)

func (f dciAdapterSearcherFunc) SearchWithIdentity(ctx context.Context, query string, traceID modulecore.TraceID, actionID modulecore.ActionID, actorKind, actorID, idempotencyKey string) (domaindci.SearchResult, error) {
	return f(ctx, query, traceID, actionID, actorKind, actorID, idempotencyKey)
}

func TestRuntimeDataWriteDCIRejectsMalformedOwnerResult(t *testing.T) {
	store := &dciAdapterFindStore{}
	searcher := dciAdapterSearcherFunc(func(_ context.Context, query string, traceID modulecore.TraceID, actionID modulecore.ActionID, actorKind, actorID, idempotencyKey string) (domaindci.SearchResult, error) {
		result := validDCIAdapterResult(query, traceID, actionID, actorKind, actorID, idempotencyKey)
		result.Trace.Mode = "wrong-mode"
		return result, nil
	})
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDCI(registry, store, searcher); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	response, err := worker.ExecuteV2(runtimeDataWriteBContext(t, "dci-owner-malformed"), "data.write", map[string]any{
		"store": "dci", "operation": "search", "payload": map[string]any{"query": "query"},
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("malformed owner response=%#v err=%v", response, err)
	}
}

func TestRuntimeDataWriteDCIRejectsMismatchedTraceID(t *testing.T) {
	store := &dciAdapterFindStore{}
	searcher := dciAdapterSearcherFunc(func(_ context.Context, query string, _ modulecore.TraceID, actionID modulecore.ActionID, actorKind, actorID, idempotencyKey string) (domaindci.SearchResult, error) {
		return validDCIAdapterResult(query, modulecore.NewTraceID(), actionID, actorKind, actorID, idempotencyKey), nil
	})
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDCI(registry, store, searcher); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	response, err := worker.ExecuteV2(runtimeDataWriteBContext(t, "dci-owner-trace-mismatch"), "data.write", map[string]any{
		"store": "dci", "operation": "search", "payload": map[string]any{"query": "query"},
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("trace mismatch response=%#v err=%v", response, err)
	}
}

func TestRuntimeDataWriteDCIRejectsLegacyReplayResult(t *testing.T) {
	traceID := modulecore.NewTraceID()
	actionID := modulecore.NewActionID()
	result := validDCIAdapterResult("query", traceID, actionID, "agent", "shiro", "dci-owner-legacy")
	result.Trace.ActorAttribution = domaindci.ActorAttributionLegacyUnattributed
	result.Trace.ActorKind = ""
	result.Trace.ActorID = ""
	store := &dciAdapterFindStore{result: result, found: true}
	searcher := dciAdapterSearcherFunc(func(context.Context, string, modulecore.TraceID, modulecore.ActionID, string, string, string) (domaindci.SearchResult, error) {
		t.Fatal("legacy replay must not invoke searcher")
		return domaindci.SearchResult{}, nil
	})
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDCI(registry, store, searcher); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	response, err := worker.ExecuteV2(runtimeDataWriteBContext(t, "dci-owner-legacy"), "data.write", map[string]any{
		"store": "dci", "operation": "search", "payload": map[string]any{"query": "query"},
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("legacy replay response=%#v err=%v", response, err)
	}
}

func TestRuntimeDataWriteDCIPropagatesStoreFailure(t *testing.T) {
	sentinel := errors.New("dci idempotency lookup unavailable")
	store := &dciAdapterFindStore{err: sentinel}
	searcher := dciAdapterSearcherFunc(func(context.Context, string, modulecore.TraceID, modulecore.ActionID, string, string, string) (domaindci.SearchResult, error) {
		t.Fatal("searcher must not run after lookup failure")
		return domaindci.SearchResult{}, nil
	})
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDCI(registry, store, searcher); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	response, err := worker.ExecuteV2(runtimeDataWriteBContext(t, "dci-owner-store-failure"), "data.write", map[string]any{
		"store": "dci", "operation": "search", "payload": map[string]any{"query": "query"},
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("store failure response=%#v err=%v", response, err)
	}
}

func newDCIAdapterStores(t *testing.T) (*dcipersistence.SQLiteStore, *eventpersistence.SQLiteStore) {
	t.Helper()
	dciStore, err := dcipersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "dci.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	eventStore, err := eventpersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		_ = dciStore.Close()
		t.Fatalf("event store: %v", err)
	}
	t.Cleanup(func() {
		_ = eventStore.Close()
		_ = dciStore.Close()
	})
	return dciStore, eventStore
}

func validDCIAdapterResult(query string, traceID modulecore.TraceID, actionID modulecore.ActionID, actorKind, actorID, idempotencyKey string) domaindci.SearchResult {
	now := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
	return domaindci.SearchResult{
		Pack: domaindci.EvidencePack{ActionID: actionID, Query: query},
		Trace: domaindci.SearchTrace{
			TraceID: traceID, ActionID: actionID, StartedAt: now, EndedAt: now,
			ActorAttribution: domaindci.ActorAttributionAuthenticated, ActorKind: actorKind, ActorID: actorID,
			IdempotencyKey: idempotencyKey, Mode: "dci", UserQuery: query, Status: "completed",
		},
	}
}

func assertDCIAdapterEventGraph(t *testing.T, events *eventpersistence.SQLiteStore, result domaindci.SearchResult) {
	t.Helper()
	got, err := events.ListByComponent(context.Background(), "dci", 100)
	if err != nil {
		t.Fatalf("list dci events: %v", err)
	}
	if err := modulecore.ValidateEventEnvelopeGraph(got); err != nil {
		t.Fatalf("dci event graph invalid: %v", err)
	}
	required := map[string]bool{
		"dci.search.requested": false,
		"dci.search.started":   false,
		"dci.source.selected":  false,
		"dci.file.read":        false,
		"dci.evidence.created": false,
		"dci.search.completed": false,
	}
	for _, event := range got {
		if event.TraceID != result.Trace.TraceID || event.ActionID != result.Trace.ActionID || event.ActorKind != "agent" || event.ActorID != "shiro" {
			t.Fatalf("dci event identity=%#v result=%#v", event, result)
		}
		if _, ok := required[event.EventType]; ok {
			required[event.EventType] = true
		}
	}
	for eventType, present := range required {
		if !present {
			t.Errorf("dci event type %q missing from %#v", eventType, got)
		}
	}
	for _, step := range result.Trace.Steps {
		if step.EventType != "dci.file.read" {
			t.Fatalf("step event type=%q", step.EventType)
		}
		event, found, err := events.GetByID(context.Background(), step.EventID)
		if err != nil || !found || event.EventType != "dci.file.read" {
			t.Fatalf("step event=%#v found=%v err=%v", event, found, err)
		}
	}
	for _, evidence := range result.Pack.Evidence {
		if err := evidence.EvidenceID.Validate(); err != nil {
			t.Fatalf("evidence ID=%q: %v", evidence.EvidenceID, err)
		}
		if err := evidence.CreatedByEventID.Validate(); err != nil {
			t.Fatalf("evidence event ID=%q: %v", evidence.CreatedByEventID, err)
		}
		if strings.HasPrefix(string(evidence.EvidenceID), "evt_") || strings.HasPrefix(string(evidence.CreatedByEventID), "evd_") || string(evidence.EvidenceID) == string(evidence.CreatedByEventID) {
			t.Fatalf("evidence IDs are not independent typed IDs: %#v", evidence)
		}
		event, found, err := events.GetByID(context.Background(), evidence.CreatedByEventID)
		if err != nil || !found || event.EventType != "dci.evidence.created" || event.EvidenceID != evidence.EvidenceID {
			t.Fatalf("evidence event=%#v evidence=%#v found=%v err=%v", event, evidence, found, err)
		}
	}
}

func writeDCIAdapterTestFile(path, contents string) error {
	return os.WriteFile(path, []byte(contents), 0644)
}

func dciAdapterExecuteWrite(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, payload map[string]any) runtimeDataWriteReceipt {
	t.Helper()
	response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "dci", "operation": "search", "payload": payload})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("dci data.write response=%#v err=%v", response, err)
	}
	receipt, ok := response.Result.(runtimeDataWriteReceipt)
	if !ok {
		t.Fatalf("dci data.write result type=%T value=%#v", response.Result, response.Result)
	}
	return receipt
}

func dciAdapterClonePayload(payload map[string]any) map[string]any {
	clone := make(map[string]any, len(payload))
	for key, value := range payload {
		clone[key] = value
	}
	return clone
}
