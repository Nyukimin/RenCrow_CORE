package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPublicAcquisitionLeaseCanonicalIdentityCompetitionAndExpiryFence(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	first, claimed, err := store.ClaimPublicAcquisition(ctx, "HTTPS://Example.org:443/a?b=1#frag", now)
	if err != nil || !claimed {
		t.Fatalf("first claim=(%+v,%t,%v)", first, claimed, err)
	}
	if first.CanonicalURL != "https://example.org/a?b=1" {
		t.Fatalf("canonical URL=%q", first.CanonicalURL)
	}
	if _, claimed, err := store.ClaimPublicAcquisition(ctx, "https://example.org/a?b=1", now.Add(time.Second)); err != nil || claimed {
		t.Fatalf("competing canonical claim=(%t,%v), want unavailable", claimed, err)
	}

	expiredAt := first.ExpiresAt.Add(time.Second)
	second, claimed, err := store.ClaimPublicAcquisition(ctx, "https://example.org/a?b=1", expiredAt)
	if err != nil || !claimed || second.Token == first.Token {
		t.Fatalf("expired lease reclaim=(%+v,%t,%v)", second, claimed, err)
	}
	if released, err := store.ReleasePublicAcquisition(ctx, first, expiredAt); err != nil || released {
		t.Fatalf("stale lease release=(%t,%v), want fenced rejection", released, err)
	}
	if released, err := store.ReleasePublicAcquisition(ctx, second, expiredAt.Add(time.Second)); err != nil || !released {
		t.Fatalf("current lease release=(%t,%v), want success", released, err)
	}
}

func TestCanonicalPublicAcquisitionURLPreservesQueryAndRejectsCredentials(t *testing.T) {
	got, err := CanonicalPublicAcquisitionURL("http://EXAMPLE.org:80/path?b=2&a=1#section")
	if err != nil || got != "http://example.org/path?b=2&a=1" {
		t.Fatalf("canonical URL=(%q,%v)", got, err)
	}
	if _, err := CanonicalPublicAcquisitionURL("https://user:secret@example.org/path"); err == nil {
		t.Fatal("credential-bearing URL was accepted")
	}
}

func TestPublicSourceCompletionFencesExpiredTokenAndChecksCurrentSourceURL(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SaveSourceRegistryEntry(ctx, L1SourceRegistryEntry{
		SourceID: "web:source", URL: "https://example.org/original", Kind: L1SourceKindWebGather,
		TrustScore: 0.9, FetchInterval: time.Hour, LicenseNote: "test", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	old, claimed, err := store.ClaimPublicAcquisition(ctx, "https://example.org/original", now)
	if err != nil || !claimed {
		t.Fatalf("old claim=(%+v,%t,%v)", old, claimed, err)
	}
	current, claimed, err := store.ClaimPublicAcquisition(ctx, "https://example.org/original", old.ExpiresAt.Add(time.Second))
	if err != nil || !claimed {
		t.Fatalf("reclaimed lease=(%+v,%t,%v)", current, claimed, err)
	}
	if completed, err := store.CompletePublicSourceAcquisition(ctx, old, "web:source", now, L1SourceFetchStatusOK, ""); err != nil || completed {
		t.Fatalf("expired source completion=(%t,%v), want fenced rejection", completed, err)
	}
	entries, err := store.ListSourceRegistryEntries(ctx, false)
	if err != nil || len(entries) != 1 || !entries[0].LastFetchedAt.IsZero() {
		t.Fatalf("stale worker stamped source: entries=%+v err=%v", entries, err)
	}
	if _, err := store.SaveSourceRegistryEntry(ctx, L1SourceRegistryEntry{
		SourceID: "web:source", URL: "https://example.org/changed", Kind: L1SourceKindWebGather,
		TrustScore: 0.9, FetchInterval: time.Hour, LicenseNote: "test", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if completed, err := store.CompletePublicSourceAcquisition(ctx, current, "web:source", now, L1SourceFetchStatusOK, ""); err != nil || completed {
		t.Fatalf("old URL completion after registry edit=(%t,%v), want rejection", completed, err)
	}
	entries, err = store.ListSourceRegistryEntries(ctx, false)
	if err != nil || len(entries) != 1 || !entries[0].LastFetchedAt.IsZero() {
		t.Fatalf("old URL completion stamped edited source: entries=%+v err=%v", entries, err)
	}
}
