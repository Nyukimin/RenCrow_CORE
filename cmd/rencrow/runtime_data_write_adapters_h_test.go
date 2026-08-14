package main

import (
	"context"
	"testing"

	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	domainglossary "github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	glossarypersistence "github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/persistence"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRuntimeDataWriteGlossaryCandidateOwnerE2EThroughWorkerAndSQLite(t *testing.T) {
	path := t.TempDir() + "/glossary.db"
	store, err := glossarypersistence.NewSQLiteGlossaryRepository(path)
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	canonical := domainglossary.NewGlossaryItem("Mio", "canonical agent", "manual", "agent")
	if err := store.Save(context.Background(), canonical); err != nil {
		t.Fatalf("seed canonical item: %v", err)
	}

	writeRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteGlossary(writeRegistry, store); err != nil {
		t.Fatalf("register glossary write: %v", err)
	}
	assertRuntimeDataWriteEContract(t, writeRegistry.Snapshot(), runtimeDataWriteRoute{
		Store:                 "glossary",
		Operation:             "propose_term_candidate",
		Access:                dataRecallAccessInternal,
		RequiredPayloadFields: []string{"category", "explanation", "source_url", "term"},
	})
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: writeRegistry, DisableToolHarness: true})
	ctx := runtimeDataWriteOwnerContext(t, "glossary-owner-1", false)
	payload := map[string]any{
		"term":        " CandidateTerm ",
		"explanation": " candidate explanation ",
		"source_url":  "https://example.com/candidate",
		"category":    " New_Word ",
	}

	first := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "glossary", "propose_term_candidate", payload)
	if first.IdempotentReplay || first.SchemaVersion != "glossary-candidate/v1" || first.AuditRef == "" || first.IdempotencyKey != "glossary-owner-1" || first.PolicyRevision != runtimeDataWritePolicyRevision {
		t.Fatalf("first receipt = %#v", first)
	}
	candidate, found, err := store.FindCandidateByID(context.Background(), first.AuditRef)
	if err != nil || !found {
		t.Fatalf("saved candidate = %#v found=%v err=%v", candidate, found, err)
	}
	if candidate.Term != "CandidateTerm" || candidate.Explanation != "candidate explanation" || candidate.SourceURL != "https://example.com/candidate" || candidate.Category != "new_word" || candidate.ProposedBy != "shiro" || candidate.State != domainglossary.GlossaryCandidateState {
		t.Fatalf("saved candidate fields = %#v", candidate)
	}

	second := runtimeDataWriteOwnerExecuteWrite(t, worker, ctx, "glossary", "propose_term_candidate", payload)
	if !second.IdempotentReplay || second.AuditRef != first.AuditRef {
		t.Fatalf("replay receipt = %#v first=%#v", second, first)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}
	reopened, err := glossarypersistence.NewSQLiteGlossaryRepository(path)
	if err != nil {
		t.Fatalf("reopen glossary store: %v", err)
	}
	defer reopened.Close()
	reopenedRegistry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteGlossary(reopenedRegistry, reopened); err != nil {
		t.Fatalf("register reopened glossary write: %v", err)
	}
	reopenedWorker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: reopenedRegistry, DisableToolHarness: true})
	replayed := runtimeDataWriteOwnerExecuteWrite(t, reopenedWorker, ctx, "glossary", "propose_term_candidate", payload)
	if !replayed.IdempotentReplay || replayed.AuditRef != first.AuditRef {
		t.Fatalf("reopen replay receipt = %#v first=%#v", replayed, first)
	}

	changed := runtimeDataWriteOwnerClonePayload(payload)
	changed["explanation"] = "different explanation"
	response, err := reopenedWorker.ExecuteV2(ctx, "data.write", map[string]any{
		"store": "glossary", "operation": "propose_term_candidate", "payload": changed,
	})
	if err != nil || response == nil || !response.IsError() {
		t.Fatalf("conflicting replay response=%#v err=%v", response, err)
	}
	candidate, found, err = reopened.FindCandidateByID(context.Background(), first.AuditRef)
	if err != nil || !found || candidate.Explanation != "candidate explanation" {
		t.Fatalf("conflict mutated candidate=%#v found=%v err=%v", candidate, found, err)
	}

	lookup, err := prepareRuntimeGlossaryLookup(context.Background(), path)
	if err != nil {
		t.Fatalf("prepare canonical lookup: %v", err)
	}
	value, err := lookup.Lookup(context.Background(), "define_term", "CandidateTerm", "", 10)
	if err != nil {
		t.Fatalf("canonical candidate lookup: %v", err)
	}
	result, ok := value.(glossaryapp.LookupResult)
	if !ok {
		t.Fatalf("canonical lookup result type=%T value=%#v", value, value)
	}
	if len(result.Items) != 0 {
		t.Fatalf("candidate leaked into canonical lookup: %#v", result.Items)
	}
}

func TestRuntimeDataWriteGlossaryCandidateRejectsForbiddenAndUnsafePayload(t *testing.T) {
	store, err := glossarypersistence.NewSQLiteGlossaryRepository(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer store.Close()
	registry := newRuntimeDataWriteRegistry()
	if err := registerRuntimeDataWriteGlossary(registry, store); err != nil {
		t.Fatal(err)
	}
	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataWrite: registry, DisableToolHarness: true})
	base := map[string]any{"term": "Candidate", "explanation": "definition", "source_url": "https://example.com/source", "category": "new_word"}
	for _, key := range []string{"id", "state", "proposed_by", "created_at", "request_id", "actor"} {
		payload := runtimeDataWriteOwnerClonePayload(base)
		payload[key] = "model-owned"
		response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "glossary-forbidden-"+key, false), "data.write", map[string]any{
			"store": "glossary", "operation": "propose_term_candidate", "payload": payload,
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("forbidden field %q response=%#v err=%v", key, response, err)
		}
	}
	for _, test := range []struct {
		name  string
		field string
		value string
	}{
		{"http source", "source_url", "http://example.com/source"},
		{"unsafe category", "category", "../private"},
	} {
		payload := runtimeDataWriteOwnerClonePayload(base)
		payload[test.field] = test.value
		response, err := worker.ExecuteV2(runtimeDataWriteOwnerContext(t, "glossary-unsafe-"+test.name, false), "data.write", map[string]any{
			"store": "glossary", "operation": "propose_term_candidate", "payload": payload,
		})
		if err != nil || response == nil || !response.IsError() {
			t.Fatalf("unsafe %s response=%#v err=%v", test.name, response, err)
		}
	}
}
