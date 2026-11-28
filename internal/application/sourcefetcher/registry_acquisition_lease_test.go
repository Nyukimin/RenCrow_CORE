package sourcefetcher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

func TestRunSourceClaimsCanonicalURLBeforeNetworkAndReleasesAfterCompletion(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	entered := make(chan struct{}, 1)
	proceed := make(chan struct{})
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			entered <- struct{}{}
		}
		select {
		case <-proceed:
		case <-time.After(5 * time.Second):
			return
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>feed</title></channel></rss>`))
	}))
	defer server.Close()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for _, sourceID := range []string{"rss:first", "rss:second"} {
		if _, err := store.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{
			SourceID: sourceID, URL: server.URL + "/feed#fragment", Kind: l1sqlite.L1SourceKindRSS,
			TrustScore: 0.9, FetchInterval: time.Hour, LicenseNote: "test", Enabled: true,
			Meta: map[string]interface{}{"allow_localhost": true},
		}); err != nil {
			t.Fatal(err)
		}
	}
	type runResult struct {
		result SweepResult
		err    error
	}
	firstDone := make(chan runResult, 1)
	go func() {
		result, err := RunSource(ctx, store, "rss:first", now, SweepOptions{LimitPerSource: 1})
		firstDone <- runResult{result: result, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first source did not enter its HTTP request")
	}
	second, err := RunSource(ctx, store, "rss:second", now, SweepOptions{LimitPerSource: 1})
	if err == nil || second.Failed != 1 {
		t.Fatalf("competing source run=(%+v,%v), want busy acquisition failure", second, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("HTTP calls=%d while first lease held, want 1", hits.Load())
	}
	entries, err := store.ListSourceRegistryEntries(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.SourceID == "rss:second" && !entry.LastFetchedAt.IsZero() {
			t.Fatalf("busy worker changed source completion stamp: %+v", entry)
		}
	}
	close(proceed)
	select {
	case completed := <-firstDone:
		if completed.err != nil || completed.result.Failed != 0 {
			t.Fatalf("first run=(%+v,%v)", completed.result, completed.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first source did not complete")
	}
	if _, err := RunSource(ctx, store, "rss:second", now.Add(time.Minute), SweepOptions{LimitPerSource: 1}); err != nil {
		t.Fatalf("source could not acquire after completion: %v", err)
	}
}

func TestCancelledSourceAcquisitionReleasesWithoutStampingCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry, err := store.SaveSourceRegistryEntry(context.Background(), l1sqlite.L1SourceRegistryEntry{
		SourceID: "web:cancelled", URL: "https://example.org/cancelled", Kind: l1sqlite.L1SourceKindWebGather,
		TrustScore: 0.9, FetchInterval: time.Hour, LicenseNote: "test", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = withRegistrySourceAcquisition(ctx, store, *entry, time.Now().UTC(), func(context.Context) error {
		cancel()
		return nil
	})
	if err == nil || err != context.Canceled {
		t.Fatalf("cancelled acquisition error=%v, want context cancellation", err)
	}
	entries, err := store.ListSourceRegistryEntries(context.Background(), false)
	if err != nil || len(entries) != 1 || !entries[0].LastFetchedAt.IsZero() {
		t.Fatalf("cancelled acquisition stamped source: entries=%+v err=%v", entries, err)
	}
	lease, claimed, err := store.ClaimPublicAcquisition(context.Background(), entry.URL, time.Now().UTC())
	if err != nil || !claimed {
		t.Fatalf("cancelled acquisition did not release lease: claimed=%t err=%v", claimed, err)
	}
	if released, err := store.ReleasePublicAcquisition(context.Background(), lease, time.Now().UTC()); err != nil || !released {
		t.Fatalf("cleanup test lease=(%t,%v)", released, err)
	}
}
