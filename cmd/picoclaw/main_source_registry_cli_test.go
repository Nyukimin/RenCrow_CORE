package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

type sourceRegistryCLIStoreStub struct {
	entries []conversationpersistence.L1SourceRegistryEntry
}

func TestRunSourceRegistryCommand_Sweep(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0"><channel><title>Test</title>
<item><title>AI Update</title><link>https://example.com/ai</link><description>Local LLM news</description><pubDate>Tue, 05 May 2026 10:00:00 GMT</pubDate></item>
</channel></rss>`))
	}))
	defer srv.Close()
	store, err := conversationpersistence.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	if _, err := store.SaveSourceRegistryEntry(ctx, conversationpersistence.L1SourceRegistryEntry{
		SourceID:      "rss:test",
		URL:           srv.URL,
		Kind:          conversationpersistence.L1SourceKindRSS,
		TrustScore:    0.9,
		FetchInterval: time.Hour,
		LicenseNote:   "rss",
		Enabled:       true,
		Meta:          map[string]interface{}{"category": "ai", "namespace": "kb:ai"},
	}); err != nil {
		t.Fatalf("SaveSourceRegistryEntry failed: %v", err)
	}
	var out, errOut bytes.Buffer

	code := runSourceRegistryCommand([]string{"sweep", "--limit", "1", "--min-trust", "0.5", "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("sweep should pass, code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"promoted_news":1`) {
		t.Fatalf("expected sweep result json, got %s", out.String())
	}
	news, err := store.RecentNewsItems(ctx, "ai", 10)
	if err != nil {
		t.Fatalf("RecentNewsItems failed: %v", err)
	}
	if len(news) != 1 || news[0].SummaryDraft != "AI Update" {
		t.Fatalf("unexpected news items: %+v", news)
	}
}

func (s *sourceRegistryCLIStoreStub) SaveSourceRegistryEntry(_ context.Context, entry conversationpersistence.L1SourceRegistryEntry) (*conversationpersistence.L1SourceRegistryEntry, error) {
	for i := range s.entries {
		if s.entries[i].SourceID == entry.SourceID {
			s.entries[i] = entry
			return &entry, nil
		}
	}
	s.entries = append(s.entries, entry)
	return &entry, nil
}

func (s *sourceRegistryCLIStoreStub) ListSourceRegistryEntries(_ context.Context, enabledOnly bool) ([]conversationpersistence.L1SourceRegistryEntry, error) {
	if !enabledOnly {
		return s.entries, nil
	}
	out := make([]conversationpersistence.L1SourceRegistryEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.Enabled {
			out = append(out, entry)
		}
	}
	return out, nil
}

func TestRunSourceRegistryCommand_SaveAndList(t *testing.T) {
	store := &sourceRegistryCLIStoreStub{}
	var out, errOut bytes.Buffer

	code := runSourceRegistryCommand([]string{
		"save",
		"--source-id", "rss:ai",
		"--url", "https://example.com/feed.xml",
		"--kind", "rss",
		"--trust-score", "0.8",
		"--interval-sec", "7200",
		"--license-note", "public feed",
		"--namespace", "kb:news",
		"--json",
	}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("save should pass, code=%d err=%s", code, errOut.String())
	}
	if len(store.entries) != 1 {
		t.Fatalf("expected 1 saved entry, got %d", len(store.entries))
	}
	got := store.entries[0]
	if got.SourceID != "rss:ai" || got.URL != "https://example.com/feed.xml" || got.FetchInterval != 2*time.Hour || got.Meta["namespace"] != "kb:news" {
		t.Fatalf("unexpected saved entry: %+v", got)
	}
	if !strings.Contains(out.String(), `"source_id":"rss:ai"`) {
		t.Fatalf("expected json output, got %s", out.String())
	}

	out.Reset()
	errOut.Reset()
	code = runSourceRegistryCommand([]string{"list", "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("list should pass, code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"source_id":"rss:ai"`) {
		t.Fatalf("expected listed entry, got %s", out.String())
	}
}

func TestRunSourceRegistryCommand_SaveRequiresFields(t *testing.T) {
	store := &sourceRegistryCLIStoreStub{}
	var out, errOut bytes.Buffer

	code := runSourceRegistryCommand([]string{"save", "--source-id", "rss:missing"}, store, &out, &errOut)
	if code == 0 {
		t.Fatal("save should fail without required fields")
	}
	if !strings.Contains(errOut.String(), "source-id, url, kind, license-note are required") {
		t.Fatalf("unexpected error: %s", errOut.String())
	}
}

func TestRunSourceRegistryCommand_Disable(t *testing.T) {
	store := &sourceRegistryCLIStoreStub{entries: []conversationpersistence.L1SourceRegistryEntry{{
		SourceID:      "rss:ai",
		URL:           "https://example.com/feed.xml",
		Kind:          conversationpersistence.L1SourceKindRSS,
		TrustScore:    0.8,
		FetchInterval: time.Hour,
		LicenseNote:   "public feed",
		Enabled:       true,
		Meta:          map[string]interface{}{"namespace": "kb:news"},
	}}}
	var out, errOut bytes.Buffer

	code := runSourceRegistryCommand([]string{"disable", "rss:ai", "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("disable should pass, code=%d err=%s", code, errOut.String())
	}
	if len(store.entries) != 1 || store.entries[0].Enabled {
		t.Fatalf("entry should be disabled: %+v", store.entries)
	}
	if !strings.Contains(out.String(), `"enabled":false`) {
		t.Fatalf("expected disabled json output, got %s", out.String())
	}
}
