package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	dciapp "github.com/Nyukimin/RenCrow_CORE/internal/application/dci"
	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteDCIOwnerE2EThroughWorkerAndJSONL(t *testing.T) {
	corpus := t.TempDir()
	if err := writeDCIAdapterTestFile(filepath.Join(corpus, "spec.md"), "DCI owner evidence\n"); err != nil {
		t.Fatal(err)
	}
	store := dcipersistence.NewJSONLStore(filepath.Join(t.TempDir(), "dci", "search_traces.jsonl"))
	explorer := dciapp.NewExplorer(dciapp.Config{
		Enabled:      true,
		Allowlist:    []string{corpus},
		MaxEvidence:  2,
		MaxFilesRead: 2,
		Now:          func() time.Time { return time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC) },
	}, store)
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
	if first.IdempotentReplay || first.SchemaVersion != "dci-search/v1" || first.MigrationState != "embedded_current" || first.ValidationState != "owner_validated" || first.AuditRef == "" || first.IdempotencyKey != "dci-owner-1" || first.PolicyRevision != runtimeDataWritePolicyRevision {
		t.Fatalf("first dci receipt=%#v", first)
	}
	traces, err := store.ListRecent(10)
	if err != nil || len(traces) != 1 || traces[0].EventID != first.AuditRef || traces[0].Actor != "shiro" || traces[0].Mode != "dci" || traces[0].UserQuery != "DCI owner" || traces[0].FinalEvidenceCount == 0 {
		t.Fatalf("persisted dci traces=%#v err=%v", traces, err)
	}
	second := dciAdapterExecuteWrite(t, worker, ctx, payload)
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
		t.Fatalf("dci replay receipt=%#v first=%#v", second, first)
	}
	traces, err = store.ListRecent(10)
	if err != nil || len(traces) != 1 {
		t.Fatalf("dci replay duplicated traces=%#v err=%v", traces, err)
	}
	changed := dciAdapterClonePayload(payload)
	changed["query"] = "different query"
	if response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "dci", "operation": "search", "payload": changed}); err != nil || response == nil || !response.IsError() {
		t.Fatalf("dci conflict response=%#v err=%v", response, err)
	}
	traces, err = store.ListRecent(10)
	if err != nil || len(traces) != 1 || traces[0].UserQuery != "DCI owner" {
		t.Fatalf("dci conflict mutated traces=%#v err=%v", traces, err)
	}
}

func TestRuntimeDataWriteDCIRejectsForbiddenPayloadFields(t *testing.T) {
	store := dcipersistence.NewJSONLStore(filepath.Join(t.TempDir(), "dci.jsonl"))
	explorer := dciapp.NewExplorer(dciapp.Config{Enabled: true, Allowlist: []string{t.TempDir()}}, store)
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteDCI(registry, store, explorer); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	ctx := runtimeDataWriteBContext(t, "dci-owner-invalid")
	for _, extra := range []string{"event_id", "actor", "mode", "corpus_scope", "path", "created_at", "request_id", "unknown"} {
		payload := map[string]any{"query": "dci", extra: "model-owned"}
		response, err := worker.ExecuteV2(ctx, "data.write", map[string]any{"store": "dci", "operation": "search", "payload": payload})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("extra field %q response=%#v err=%v", extra, response, err)
		}
	}
	if traces, err := store.ListRecent(10); err != nil || len(traces) != 0 {
		t.Fatalf("forbidden dci payload mutated traces=%#v err=%v", traces, err)
	}
}

type dciAdapterFindStore struct {
	trace domaindci.SearchTrace
	found bool
}

func (s *dciAdapterFindStore) FindSearchTraceByID(context.Context, string) (domaindci.SearchTrace, bool, error) {
	return s.trace, s.found, nil
}

type dciAdapterSearcherFunc func(context.Context, string, string, string) (domaindci.SearchResult, error)

func (f dciAdapterSearcherFunc) SearchWithIdentity(ctx context.Context, query, eventID, actor string) (domaindci.SearchResult, error) {
	return f(ctx, query, eventID, actor)
}

func TestRuntimeDataWriteDCIRejectsMalformedOwnerResult(t *testing.T) {
	store := &dciAdapterFindStore{}
	searcher := dciAdapterSearcherFunc(func(_ context.Context, query, eventID, actor string) (domaindci.SearchResult, error) {
		now := time.Date(2026, 8, 14, 5, 0, 0, 0, time.UTC)
		return domaindci.SearchResult{Trace: domaindci.SearchTrace{
			EventID: eventID, StartedAt: now, EndedAt: now, Actor: actor, Mode: "wrong-mode", UserQuery: query, Status: "completed",
		}}, nil
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

func TestRuntimeDataWriteDCIPropagatesSearcherStoreFailure(t *testing.T) {
	sentinel := errors.New("dci final store unavailable")
	store := &dciAdapterFindStore{}
	searcher := dciAdapterSearcherFunc(func(context.Context, string, string, string) (domaindci.SearchResult, error) {
		return domaindci.SearchResult{}, sentinel
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
