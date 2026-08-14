package main

import (
	"context"
	"testing"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainglossary "github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	glossarypersistence "github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/persistence"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestRegisterRuntimeDataRecallGlossaryThroughWorkerAndSQLite(t *testing.T) {
	path := t.TempDir() + "/glossary.db"
	store, err := glossarypersistence.NewSQLiteGlossaryRepository(path)
	if err != nil {
		t.Fatalf("NewSQLiteGlossaryRepository failed: %v", err)
	}
	defer store.Close()
	canonical := domainglossary.NewGlossaryItem("Mio", "canonical agent", "manual", "agent")
	if err := store.Save(context.Background(), canonical); err != nil {
		t.Fatalf("seed canonical item: %v", err)
	}
	candidate := domainglossary.GlossaryCandidate{
		ID:          "glossary-candidate/sha256:recall",
		Term:        "CandidateTerm",
		Explanation: "candidate only",
		SourceURL:   "https://example.com/candidate",
		Category:    "new_word",
		ProposedBy:  "shiro",
		State:       domainglossary.GlossaryCandidateState,
		CreatedAt:   canonical.CreatedAt,
	}
	if err := store.SaveCandidate(context.Background(), candidate); err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	lookup, err := prepareRuntimeGlossaryLookup(context.Background(), path)
	if err != nil {
		t.Fatalf("prepare glossary lookup: %v", err)
	}

	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallGlossary(registry, lookup, store); err != nil {
		t.Fatalf("register glossary recall: %v", err)
	}
	routes := registry.Snapshot()
	if len(routes) != 3 {
		t.Fatalf("glossary recall routes = %#v", routes)
	}
	for _, route := range routes {
		if route.Store != "glossary" {
			t.Fatalf("unexpected route = %#v", route)
		}
		if route.Operation == "candidates" && route.Access != dataRecallAccessInternal {
			t.Fatalf("candidate route = %#v", route)
		}
		if route.Operation != "candidates" && route.Access != dataRecallAccessPublic {
			t.Fatalf("canonical route = %#v", route)
		}
	}

	worker := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})
	publicContext := runtimeDataRecallContext(t, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic})
	define := glossaryRecallExecute(t, worker, publicContext, "define_term", "Mio")
	if len(define.Records) != 1 || define.Records[0]["term"] != "Mio" || define.Records[0]["explanation"] != "canonical agent" {
		t.Fatalf("define_term result = %#v", define)
	}
	if _, leaked := define.Records[0]["source_url"]; leaked {
		t.Fatal("canonical glossary projection unexpectedly contains candidate source_url")
	}
	category := glossaryRecallExecute(t, worker, publicContext, "list_category", "agent")
	if len(category.Records) != 1 || category.Records[0]["term"] != "Mio" {
		t.Fatalf("list_category result = %#v", category)
	}

	internal := glossaryRecallExecute(t, worker, dataRecallInternalContext(t), "candidates", candidate.ID)
	if len(internal.Records) != 1 || internal.Records[0]["candidate_id"] != candidate.ID || internal.Records[0]["state"] != domainglossary.GlossaryCandidateState {
		t.Fatalf("candidate result = %#v", internal)
	}
	if _, leaked := internal.Records[0]["payload"]; leaked {
		t.Fatal("candidate projection leaked raw payload")
	}
	missing := glossaryRecallExecute(t, worker, dataRecallInternalContext(t), "candidates", "missing-candidate")
	if len(missing.Records) != 0 {
		t.Fatalf("missing candidate result = %#v", missing)
	}
}

func glossaryRecallExecute(t *testing.T, worker *toolsinfra.ToolRunner, ctx context.Context, operation, query string) runtimeDataRecallResult {
	t.Helper()
	response, err := worker.ExecuteV2(ctx, "data.recall", map[string]any{
		"store": "glossary", "operation": operation, "query": query, "limit": 10,
	})
	if err != nil || response == nil || response.IsError() {
		t.Fatalf("glossary data.recall response=%#v err=%v", response, err)
	}
	result, ok := response.Result.(runtimeDataRecallResult)
	if !ok {
		t.Fatalf("glossary data.recall result type=%T value=%#v", response.Result, response.Result)
	}
	return result
}
