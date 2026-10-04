package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	domainglossary "github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
)

func TestBuildGlossaryRuntimeUsesSelectedRemoteOwnerWithoutLocalFallback(t *testing.T) {
	root := t.TempDir()
	localPath := filepath.Join(root, "must-not-open", "glossary.db")
	owner := &runtimeGlossaryRemoteStub{item: &domainglossary.GlossaryItem{
		ID: "glossary-remote-1", Term: "remote term", Explanation: "remote explanation",
		Source: "remote source", Category: "runtime", CreatedAt: time.Unix(10, 0).UTC(), UpdatedAt: time.Unix(20, 0).UTC(),
	}}
	runtime := buildGlossaryRuntime(&config.Config{Glossary: config.GlossaryConfig{
		Enabled: true, DBPath: localPath,
	}}, owner)
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("selected remote Glossary owner fell back to local database %q", localPath)
	}
	if runtime.IndexedLookup != owner || runtime.CandidateStore != owner {
		t.Fatalf("runtime owner selection = lookup:%T candidate:%T, want selected remote owner", runtime.IndexedLookup, runtime.CandidateStore)
	}
	ctx := context.Background()
	if got, err := runtime.RecentContext(ctx, 5); err != nil || got == "" {
		t.Fatalf("remote RecentContext=%q err=%v", got, err)
	}
	if got, err := runtime.RecentTopics(ctx, 5); err != nil || len(got) != 1 || got[0] != "remote term: remote explanation" {
		t.Fatalf("remote RecentTopics=%v err=%v", got, err)
	}
	lookup, err := runtime.IndexedLookup.Lookup(ctx, "define_term", "remote term", "", 1)
	if err != nil {
		t.Fatalf("remote indexed lookup: %v", err)
	}
	if result, ok := lookup.(glossaryapp.LookupResult); !ok || len(result.Items) != 1 || result.Items[0].Term != "remote term" {
		t.Fatalf("remote indexed lookup DTO = %#v (%T)", lookup, lookup)
	}
	response := httptest.NewRecorder()
	runtime.RecentHandler.ServeHTTP(response, httptest.NewRequest("GET", "/", nil))
	if response.Code != 200 {
		t.Fatalf("remote Viewer recent status=%d body=%s", response.Code, response.Body.String())
	}
	var viewerPayload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &viewerPayload); err != nil || viewerPayload["items"] == nil {
		t.Fatalf("remote Viewer recent payload=%v err=%v", viewerPayload, err)
	}
	candidate := domainglossary.GlossaryCandidate{
		ID: "candidate-1", Term: "candidate term", Explanation: "proposal", SourceURL: "https://example.test/source",
		Category: "runtime", ProposedBy: "mio", State: domainglossary.GlossaryCandidateState, CreatedAt: time.Unix(30, 0).UTC(),
	}
	if err := runtime.CandidateStore.SaveCandidate(ctx, candidate); err != nil {
		t.Fatalf("remote candidate save: %v", err)
	}
	got, found, err := runtime.CandidateStore.FindCandidateByID(ctx, candidate.ID)
	if err != nil || !found || got != candidate {
		t.Fatalf("remote candidate read=(%+v,%v) err=%v", got, found, err)
	}
}

type runtimeGlossaryRemoteStub struct {
	item       *domainglossary.GlossaryItem
	candidates map[string]domainglossary.GlossaryCandidate
}

func (stub *runtimeGlossaryRemoteStub) Save(_ context.Context, item *domainglossary.GlossaryItem) error {
	stub.item = item
	return nil
}

func (stub *runtimeGlossaryRemoteStub) FindByTerm(_ context.Context, term string) (*domainglossary.GlossaryItem, error) {
	if stub.item != nil && stub.item.Term == term {
		return stub.item, nil
	}
	return nil, nil
}

func (stub *runtimeGlossaryRemoteStub) FindRecent(context.Context, int) ([]*domainglossary.GlossaryItem, error) {
	if stub.item == nil {
		return []*domainglossary.GlossaryItem{}, nil
	}
	return []*domainglossary.GlossaryItem{stub.item}, nil
}

func (stub *runtimeGlossaryRemoteStub) FindByCategory(_ context.Context, category string, _ int) ([]*domainglossary.GlossaryItem, error) {
	if stub.item != nil && stub.item.Category == category {
		return []*domainglossary.GlossaryItem{stub.item}, nil
	}
	return []*domainglossary.GlossaryItem{}, nil
}

func (*runtimeGlossaryRemoteStub) Delete(context.Context, string) error { return nil }

func (stub *runtimeGlossaryRemoteStub) SaveCandidate(_ context.Context, candidate domainglossary.GlossaryCandidate) error {
	if stub.candidates == nil {
		stub.candidates = make(map[string]domainglossary.GlossaryCandidate)
	}
	stub.candidates[candidate.ID] = candidate
	return nil
}

func (stub *runtimeGlossaryRemoteStub) FindCandidateByID(_ context.Context, id string) (domainglossary.GlossaryCandidate, bool, error) {
	candidate, ok := stub.candidates[id]
	return candidate, ok, nil
}

func (stub *runtimeGlossaryRemoteStub) Lookup(_ context.Context, operation, term, category string, _ int) (any, error) {
	if stub.item == nil || (operation == "define_term" && stub.item.Term != term) || (operation == "list_category" && stub.item.Category != category) {
		return glossaryapp.LookupResult{Operation: operation, Items: []glossaryapp.Item{}}, nil
	}
	return glossaryapp.LookupResult{Operation: operation, Items: []glossaryapp.Item{{
		ID: stub.item.ID, Term: stub.item.Term, Explanation: stub.item.Explanation, Source: stub.item.Source,
		Category: stub.item.Category, UpdatedAt: stub.item.UpdatedAt.Format(time.RFC3339Nano),
	}}}, nil
}

var _ runtimeGlossaryRemoteOwner = (*runtimeGlossaryRemoteStub)(nil)
