package glossary

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewGlossaryModuleAndClose(t *testing.T) {
	module, err := NewGlossaryModule(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewGlossaryModule failed: %v", err)
	}
	if module.Repository == nil || module.Service == nil || module.MioAdapter == nil {
		t.Fatalf("module dependencies were not initialized: %#v", module)
	}
	if err := module.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestGlossaryModuleCloseNilSafeAndSyncFeedsEmpty(t *testing.T) {
	module := &GlossaryModule{}
	if err := module.Close(); err != nil {
		t.Fatalf("nil repository close failed: %v", err)
	}

	module, err := NewGlossaryModule(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewGlossaryModule failed: %v", err)
	}
	defer module.Close()

	saved, err := module.SyncFeeds(context.Background(), nil)
	if err != nil {
		t.Fatalf("SyncFeeds empty failed: %v", err)
	}
	if saved != 0 {
		t.Fatalf("expected no saved items for empty feed list, got %d", saved)
	}
}

func TestGlossaryModuleSyncFeedsSavesParsedItems(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <title>Glossary</title>
    <item>
      <title>Kyoto city guide</title>
      <description>Kyoto is a location term.</description>
    </item>
  </channel>
</rss>`))
	}))
	defer server.Close()

	module, err := NewGlossaryModule(t.TempDir() + "/glossary.db")
	if err != nil {
		t.Fatalf("NewGlossaryModule failed: %v", err)
	}
	defer module.Close()

	saved, err := module.SyncFeeds(context.Background(), []string{server.URL})
	if err != nil {
		t.Fatalf("SyncFeeds failed: %v", err)
	}
	if saved != 1 {
		t.Fatalf("expected one saved item, got %d", saved)
	}

	item, err := module.Service.SearchByTerm(context.Background(), "Kyoto")
	if err != nil {
		t.Fatalf("SearchByTerm failed: %v", err)
	}
	if item.Category != "location" {
		t.Fatalf("unexpected saved category: %#v", item)
	}
}
