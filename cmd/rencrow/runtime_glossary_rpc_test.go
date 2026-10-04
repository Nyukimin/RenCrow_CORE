package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	domainglossary "github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	glossarypersistence "github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/persistence"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

func TestBuildGlossaryRuntimeUsesRealSelectedRPCOwnerWithoutLocalFallback(t *testing.T) {
	root := t.TempDir()
	owner, err := glossarypersistence.NewSQLiteGlossaryRepository(filepath.Join(root, "host-glossary.db"))
	if err != nil {
		t.Fatalf("open host Glossary owner: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	item := &domainglossary.GlossaryItem{
		ID: "glossary-remote-rpc-1", Term: "rpc term", Explanation: "typed remote explanation",
		Source: "fixture", Category: "runtime", CreatedAt: time.Unix(10, 0).UTC(), UpdatedAt: time.Unix(20, 0).UTC(),
	}
	if err := owner.Save(context.Background(), item); err != nil {
		t.Fatalf("seed host Glossary owner: %v", err)
	}
	handler, err := storagehost.NewHandler(storagehost.HandlerConfig{Token: "runtime-glossary-test", JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	if err := storagehost.RegisterGlossaryGroup(handler, owner); err != nil {
		t.Fatalf("register Glossary group: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := storagehost.NewClient(storagehost.ClientConfig{Endpoint: server.URL, Token: "runtime-glossary-test", HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatalf("Glossary client handshake: %v", err)
	}
	remote := storagehost.NewGlossaryStoreClient(client)
	localPath := filepath.Join(root, "missing-parent", "local-glossary.db")
	runtime := buildGlossaryRuntime(&config.Config{Glossary: config.GlossaryConfig{Enabled: true, DBPath: localPath}}, remote)
	if _, err := os.Stat(localPath); err == nil {
		t.Fatalf("selected remote Glossary owner fell back to local database %q", localPath)
	}
	if runtime.IndexedLookup != remote || runtime.CandidateStore != remote {
		t.Fatalf("runtime owner selection = lookup:%T candidate:%T, want selected RPC client", runtime.IndexedLookup, runtime.CandidateStore)
	}
	if got, err := runtime.RecentContext(context.Background(), 5); err != nil || !strings.Contains(got, "rpc term") {
		t.Fatalf("remote Mio RecentContext=%q err=%v", got, err)
	}
	if got, err := runtime.RecentTopics(context.Background(), 5); err != nil || len(got) != 1 || got[0] != "rpc term: typed remote explanation" {
		t.Fatalf("remote Mio RecentTopics=%v err=%v", got, err)
	}
	lookup, err := runtime.IndexedLookup.Lookup(context.Background(), "define_term", "rpc term", "", 1)
	if err != nil {
		t.Fatalf("remote indexed lookup: %v", err)
	}
	result, ok := lookup.(glossaryapp.LookupResult)
	if !ok || len(result.Items) != 1 || result.Items[0].Term != "rpc term" || result.Items[0].UpdatedAt == "" {
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
		ID: "candidate-rpc-1", Term: "candidate term", Explanation: "proposal", SourceURL: "https://example.test/source",
		Category: "runtime", ProposedBy: "mio", State: domainglossary.GlossaryCandidateState, CreatedAt: time.Unix(30, 0).UTC(),
	}
	if err := runtime.CandidateStore.SaveCandidate(context.Background(), candidate); err != nil {
		t.Fatalf("remote candidate save: %v", err)
	}
	got, found, err := runtime.CandidateStore.FindCandidateByID(context.Background(), candidate.ID)
	if err != nil || !found || got != candidate {
		t.Fatalf("remote candidate read=(%+v,%v) err=%v", got, found, err)
	}
}

func TestBuildGlossaryRuntimeLocalDefaultStillOpensConfiguredOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local-glossary.db")
	runtime := buildGlossaryRuntime(&config.Config{Glossary: config.GlossaryConfig{Enabled: true, DBPath: path}}, nil)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("local Glossary default did not open configured owner: %v", err)
	}
	if runtime.CandidateStore == nil || runtime.IndexedLookup == nil || runtime.RecentHandler == nil {
		t.Fatalf("local Glossary runtime incomplete: %+v", runtime)
	}
}
