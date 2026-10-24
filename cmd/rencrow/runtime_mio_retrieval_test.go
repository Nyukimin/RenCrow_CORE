package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type mioRetrievalGlossaryLookupFunc func(context.Context, string, string, string, int) (any, error)

func (f mioRetrievalGlossaryLookupFunc) Lookup(ctx context.Context, operation, term, category string, limit int) (any, error) {
	return f(ctx, operation, term, category, limit)
}

type mioRetrievalCategoryRegistryFunc func(context.Context, domainconversation.CategoryRecallQuery) (domainconversation.CategoryRecallResult, error)

func (f mioRetrievalCategoryRegistryFunc) Recall(ctx context.Context, query domainconversation.CategoryRecallQuery) (domainconversation.CategoryRecallResult, error) {
	return f(ctx, query)
}

func TestRuntimeMioRetrieval_GlossaryRunsExactOwnerLookupButWithholdsUnversionedEvidence(t *testing.T) {
	var gotOperation, gotTerm, gotCategory string
	var gotLimit int
	lookup := mioRetrievalGlossaryLookupFunc(func(_ context.Context, operation, term, category string, limit int) (any, error) {
		gotOperation, gotTerm, gotCategory, gotLimit = operation, term, category, limit
		return glossaryapp.LookupResult{Operation: "define_term", Items: []glossaryapp.Item{{
			ID: "glossary-1", Term: "Mio", Explanation: "conversation agent", Source: "manual", Category: "agent", UpdatedAt: "2026-10-24T00:00:00Z",
		}}}, nil
	})
	registry := mioRetrievalCategoryRegistryFunc(func(context.Context, domainconversation.CategoryRecallQuery) (domainconversation.CategoryRecallResult, error) {
		t.Fatal("glossary request must not query catalog")
		return domainconversation.CategoryRecallResult{}, nil
	})
	port := newRuntimeMioRetrieval(lookup, registry)
	result, err := port.Retrieve(authenticatedMioRetrievalContext(t, "ren"), domainconversation.TurnInput{}, domainagent.MioRetrievalRequest{
		SourceKind: domainagent.MioRetrievalSourceGlossary, Query: " Mio ",
		References: []string{"must-not-be-used"}, InformationNeeded: "must-not-be-used",
	})
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if gotOperation != "define_term" || gotTerm != "Mio" || gotCategory != "" || gotLimit != 8 {
		t.Fatalf("owner lookup = operation %q, term %q, category %q, limit %d", gotOperation, gotTerm, gotCategory, gotLimit)
	}
	if result.SourceKind != domainagent.MioRetrievalSourceGlossary || result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
		t.Fatalf("unexpected glossary result: %#v", result)
	}
}

func TestRuntimeMioRetrieval_GlossaryEmptyIsNotFoundAndOwnerErrorIsUnavailable(t *testing.T) {
	ctx := authenticatedMioRetrievalContext(t, "ren")
	request := domainagent.MioRetrievalRequest{SourceKind: domainagent.MioRetrievalSourceGlossary, Query: "unknown"}

	empty := newRuntimeMioRetrieval(mioRetrievalGlossaryLookupFunc(func(context.Context, string, string, string, int) (any, error) {
		return glossaryapp.LookupResult{Operation: "define_term", Items: []glossaryapp.Item{}}, nil
	}), nil)
	result, err := empty.Retrieve(ctx, domainconversation.TurnInput{}, request)
	if err != nil || result.Status != domainagent.MioRetrievalStatusNotFound || len(result.Items) != 0 {
		t.Fatalf("empty successful owner result = %#v, err=%v", result, err)
	}

	failing := newRuntimeMioRetrieval(mioRetrievalGlossaryLookupFunc(func(context.Context, string, string, string, int) (any, error) {
		return nil, errors.New("owner unavailable")
	}), nil)
	result, err = failing.Retrieve(ctx, domainconversation.TurnInput{}, request)
	if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
		t.Fatalf("failed owner result = %#v, err=%v", result, err)
	}

	missing := newRuntimeMioRetrieval(nil, nil)
	result, err = missing.Retrieve(ctx, domainconversation.TurnInput{}, request)
	if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
		t.Fatalf("missing glossary owner result = %#v, err=%v", result, err)
	}

	malformed := newRuntimeMioRetrieval(mioRetrievalGlossaryLookupFunc(func(context.Context, string, string, string, int) (any, error) {
		return glossaryapp.LookupResult{Operation: "define_term", Items: []glossaryapp.Item{{Term: "unknown", Explanation: "no stable evidence"}}}, nil
	}), nil)
	result, err = malformed.Retrieve(ctx, domainconversation.TurnInput{}, request)
	if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
		t.Fatalf("partial glossary evidence was accepted: result=%#v err=%v", result, err)
	}
}

func TestRuntimeMioRetrieval_RequiresMatchingAcceptedUserProof(t *testing.T) {
	var lookupCalls, registryCalls int
	lookup := mioRetrievalGlossaryLookupFunc(func(context.Context, string, string, string, int) (any, error) {
		lookupCalls++
		return glossaryapp.LookupResult{Operation: "define_term", Items: []glossaryapp.Item{{Term: "term", Explanation: "meaning", Source: "manual", Category: "general", UpdatedAt: "2026-10-24"}}}, nil
	})
	registry := mioRetrievalCategoryRegistryFunc(func(context.Context, domainconversation.CategoryRecallQuery) (domainconversation.CategoryRecallResult, error) {
		registryCalls++
		return domainconversation.CategoryRecallResult{Status: domainconversation.CategoryRecallStatusCompleted}, nil
	})
	port := newRuntimeMioRetrieval(lookup, registry)
	request := domainagent.MioRetrievalRequest{SourceKind: domainagent.MioRetrievalSourceGlossary, Query: "term"}

	cases := []struct {
		name string
		ctx  context.Context
	}{
		{name: "absent scope proof", ctx: context.Background()},
		{name: "mismatched agent delegation", ctx: mismatchedMioRetrievalContext(t)},
		{name: "user proof without public scope", ctx: authenticatedUserOnlyMioRetrievalContext(t, "ren")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := port.Retrieve(tc.ctx, domainconversation.TurnInput{}, request)
			if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable {
				t.Fatalf("result = %#v, err=%v", result, err)
			}
		})
	}
	if lookupCalls != 0 || registryCalls != 0 {
		t.Fatalf("owners called without matching accepted User proof: glossary=%d catalog=%d", lookupCalls, registryCalls)
	}
}

func TestRuntimeMioRetrieval_RejectsUnsafeQueriesWithoutTruncation(t *testing.T) {
	var ownerCalls int
	lookup := mioRetrievalGlossaryLookupFunc(func(context.Context, string, string, string, int) (any, error) {
		ownerCalls++
		return glossaryapp.LookupResult{}, nil
	})
	port := newRuntimeMioRetrieval(lookup, nil)
	for _, query := range []string{strings.Repeat("x", runtimeMioRetrievalMaxQueryBytes+1), "term\x00suffix", "  \n\t "} {
		result, err := port.Retrieve(authenticatedMioRetrievalContext(t, "ren"), domainconversation.TurnInput{}, domainagent.MioRetrievalRequest{
			SourceKind: domainagent.MioRetrievalSourceGlossary, Query: query,
		})
		if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
			t.Fatalf("unsafe query %q result = %#v, err=%v", query, result, err)
		}
	}
	if ownerCalls != 0 {
		t.Fatalf("unsafe queries reached glossary owner %d times", ownerCalls)
	}
}

func TestRuntimeMioRetrieval_CatalogUsesAuthenticatedScopeAndChatPolicy(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	var gotQuery domainconversation.CategoryRecallQuery
	registry := mioRetrievalCategoryRegistryFunc(func(_ context.Context, query domainconversation.CategoryRecallQuery) (domainconversation.CategoryRecallResult, error) {
		gotQuery = query
		return domainconversation.CategoryRecallResult{
			Status: domainconversation.CategoryRecallStatusCompleted,
			Records: []domainconversation.CategoryRecallRecord{{
				Category: "movie", SourceID: "movie_catalog", RecordID: "movie-1", Title: "Example movie", Summary: "Validated synopsis",
				ProvenanceURLs: []string{"https://example.test/movie-1"}, RetrievedAt: now, ValidatedAt: now,
				State: domainconversation.CategoryRecordStateValidated, Sensitivity: "normal", Scope: "ren", Roles: []string{"chat"},
			}},
		}, nil
	})
	port := newRuntimeMioRetrieval(nil, registry).withClock(func() time.Time { return now })
	result, err := port.Retrieve(authenticatedMioRetrievalContext(t, "ren"), domainconversation.TurnInput{}, domainagent.MioRetrievalRequest{
		SourceKind: domainagent.MioRetrievalSourceCatalog, Query: "映画 Example movie",
		References: []string{"/must-not-be-used"}, InformationNeeded: "must-not-be-used",
	})
	if err != nil {
		t.Fatalf("Retrieve returned error: %v", err)
	}
	if gotQuery.Message != "映画 Example movie" || gotQuery.UserScope != "ren" || gotQuery.Limit != 8 || !gotQuery.Time.Equal(now) {
		t.Fatalf("catalog owner query = %#v", gotQuery)
	}
	if result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
		t.Fatalf("unexpected catalog result: %#v", result)
	}
	// The owner query ran with the accepted User scope, but the registry result
	// has no owner revision/revocation proof for final publication.

	registry = mioRetrievalCategoryRegistryFunc(func(context.Context, domainconversation.CategoryRecallQuery) (domainconversation.CategoryRecallResult, error) {
		return domainconversation.CategoryRecallResult{Status: domainconversation.CategoryRecallStatusCompleted, Records: []domainconversation.CategoryRecallRecord{{
			Category: "movie", SourceID: "movie_catalog", RecordID: "worker-only", Title: "Hidden", Summary: "not for CHAT",
			ProvenanceURLs: []string{"https://example.test/hidden"}, RetrievedAt: now, ValidatedAt: now,
			State: domainconversation.CategoryRecordStateValidated, Sensitivity: "normal", Scope: "public", Roles: []string{"worker"},
		}}}, nil
	})
	result, err = newRuntimeMioRetrieval(nil, registry).withClock(func() time.Time { return now }).Retrieve(
		authenticatedMioRetrievalContext(t, "ren"), domainconversation.TurnInput{}, domainagent.MioRetrievalRequest{SourceKind: domainagent.MioRetrievalSourceCatalog, Query: "movie title"},
	)
	if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
		t.Fatalf("worker-only catalog evidence was not rejected: result=%#v err=%v", result, err)
	}
}

func TestRuntimeMioRetrieval_CatalogMapsEmptyAndPartialResultsCorrectly(t *testing.T) {
	ctx := authenticatedMioRetrievalContext(t, "ren")
	request := domainagent.MioRetrievalRequest{SourceKind: domainagent.MioRetrievalSourceCatalog, Query: "movie title"}

	empty := mioRetrievalCategoryRegistryFunc(func(context.Context, domainconversation.CategoryRecallQuery) (domainconversation.CategoryRecallResult, error) {
		return domainconversation.CategoryRecallResult{Status: domainconversation.CategoryRecallStatusCompleted, SourcesQueried: []string{"movie_catalog"}}, nil
	})
	result, err := newRuntimeMioRetrieval(nil, empty).Retrieve(ctx, domainconversation.TurnInput{}, request)
	if err != nil || result.Status != domainagent.MioRetrievalStatusNotFound {
		t.Fatalf("empty completed catalog query = %#v, err=%v", result, err)
	}

	partial := mioRetrievalCategoryRegistryFunc(func(context.Context, domainconversation.CategoryRecallQuery) (domainconversation.CategoryRecallResult, error) {
		return domainconversation.CategoryRecallResult{
			Status:   domainconversation.CategoryRecallStatusPartial,
			Records:  []domainconversation.CategoryRecallRecord{{Title: "some result", Summary: "still partial"}},
			Failures: []domainconversation.CategoryRecallFailure{{Code: domainconversation.CategoryRecallFailureSourceUnavailable}},
		}, nil
	})
	result, err = newRuntimeMioRetrieval(nil, partial).Retrieve(ctx, domainconversation.TurnInput{}, request)
	if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
		t.Fatalf("partial catalog query = %#v, err=%v", result, err)
	}
}

func TestRuntimeMioRetrieval_DoesNotQueryUnrelatedCatalogSources(t *testing.T) {
	source := &mioRetrievalCategorySource{categories: []string{"movie"}}
	registry := domainconversation.NewCategoryRecallRegistry(source).SetMarkers(map[string][]string{"movie": {"映画"}})
	result, err := newRuntimeMioRetrieval(nil, registry).Retrieve(
		authenticatedMioRetrievalContext(t, "ren"), domainconversation.TurnInput{}, domainagent.MioRetrievalRequest{SourceKind: domainagent.MioRetrievalSourceCatalog, Query: "today weather"},
	)
	if err != nil || result.Status != domainagent.MioRetrievalStatusUnavailable || source.calls != 0 {
		t.Fatalf("unrelated catalog request queried a source: result=%#v calls=%d err=%v", result, source.calls, err)
	}
}

func TestRuntimeMioRetrieval_OtherRegisteredSourcesAreUnavailable(t *testing.T) {
	port := newRuntimeMioRetrieval(nil, nil)
	for _, source := range []domainagent.MioRetrievalSourceKind{
		domainagent.MioRetrievalSourceConversationMemory,
		domainagent.MioRetrievalSourceUserMemory,
		domainagent.MioRetrievalSourceKnowledgeMemory,
		domainagent.MioRetrievalSourceWebSearch,
	} {
		result, err := port.Retrieve(authenticatedMioRetrievalContext(t, "ren"), domainconversation.TurnInput{}, domainagent.MioRetrievalRequest{SourceKind: source, Query: "term"})
		if err != nil || result.SourceKind != source || result.Status != domainagent.MioRetrievalStatusUnavailable || len(result.Items) != 0 {
			t.Fatalf("source %q result = %#v, err=%v", source, result, err)
		}
	}
}

func TestBuildConversationRuntimeExposesSingleBuiltCategoryRecallRegistry(t *testing.T) {
	cfg := &config.Config{Storage: config.StorageConfig{Databases: config.DatabasePathsConfig{MovieCatalog: t.TempDir() + "/missing.db"}}}
	runtime := buildConversationRuntime(cfg, primaryLLMProviders{}, nil, nil, runtimeStorageOwnerBundle{})
	if runtime.CategoryRecallRegistry == nil {
		t.Fatal("conversation runtime did not expose its built category recall registry")
	}
	result, err := runtime.CategoryRecallRegistry.Recall(context.Background(), domainconversation.CategoryRecallQuery{Message: "映画", UserScope: "ren", Limit: 8})
	if err != nil || result.Status != domainconversation.CategoryRecallStatusUnavailable {
		t.Fatalf("exposed category registry was not the configured owner registry: result=%#v err=%v", result, err)
	}
}

func authenticatedMioRetrievalContext(t *testing.T, userID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		"request-"+userID, domaintool.ActorKindUser, userID, userID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func authenticatedUserOnlyMioRetrievalContext(t *testing.T, userID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		"request-"+userID, domaintool.ActorKindUser, userID, userID,
		[]string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func mismatchedMioRetrievalContext(t *testing.T) context.Context {
	t.Helper()
	ctx, err := domaintool.DeriveAgentToolExecutionScope(authenticatedMioRetrievalContext(t, "ren"), "mio-request", "mio", "chat", "chat", false)
	if err != nil {
		t.Fatalf("DeriveAgentToolExecutionScope: %v", err)
	}
	mismatched := domaintool.ToolExecutionScope{
		RequestID: "other-request", ActorKind: domaintool.ActorKindAgent, ActorID: "mio", AuthenticatedUserID: "other-user",
		AllowedDataScopes:    []string{domaintool.DataScopePublic, domaintool.DataScopeUser},
		AuthenticationSource: domaintool.AuthenticationSourceAgentOrchestrator, AgentRole: "chat", Purpose: "chat",
	}
	return domaintool.WithToolExecutionScope(ctx, mismatched)
}

type mioRetrievalCategorySource struct {
	categories []string
	calls      int
}

func (*mioRetrievalCategorySource) ID() string { return "mio-retrieval-test-source" }

func (s *mioRetrievalCategorySource) Categories() []string {
	return append([]string(nil), s.categories...)
}

func (s *mioRetrievalCategorySource) Search(context.Context, domainconversation.CategoryRecallQuery) (domainconversation.CategoryRecallResult, error) {
	s.calls++
	return domainconversation.CategoryRecallResult{}, fmt.Errorf("unexpected category search")
}
