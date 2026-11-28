package research

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

func TestWatchAdapterDerivesExactRegisteredSourceKinds(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, source := range []l1sqlite.L1SourceRegistryEntry{
		{SourceID: "web:source", URL: "https://example.org/source", Kind: l1sqlite.L1SourceKindWebGather, Enabled: true, TrustScore: 1, FetchInterval: time.Hour, LicenseNote: "retention authorized"},
		{SourceID: "rss:source", URL: "https://example.org/feed.xml", Kind: l1sqlite.L1SourceKindRSS, Enabled: true, TrustScore: 1, FetchInterval: time.Hour, LicenseNote: "retention authorized"},
		{SourceID: "api:source", URL: "https://example.org/api", Kind: l1sqlite.L1SourceKindOfficialAPI, Enabled: true, TrustScore: 1, FetchInterval: time.Hour, LicenseNote: "retention authorized"},
	} {
		if _, err := store.SaveSourceRegistryEntry(ctx, source); err != nil {
			t.Fatal(err)
		}
	}
	adapter := NewWatchAdapter(store, func(context.Context, c.Candidate) (bool, bool) { return true, true })
	env := c.Envelope{TaskID: string(core.NewTaskID()), RunID: string(core.NewRunID()), TraceID: string(core.NewTraceID()), RequestID: string(core.NewRequestID()), Actor: "ren", OwnerID: "ren", DataScope: "user", PolicyRevision: "fixture.v1"}
	register := func(objective string, refs, kinds []string) (c.WatchReceipt, c.WatchRequest, error) {
		request := c.WatchRequest{Objective: objective, SourceRefs: refs, SourceKinds: kinds, Cadence: "2h"}
		receipt, err := adapter.Register(ctx, env, request)
		return receipt, request, err
	}
	readKinds := func(t *testing.T, receipt c.WatchReceipt) c.WatchRequest {
		t.Helper()
		stored, found, err := store.GetResearchWatch(ctx, "user", "ren", receipt.WatchID)
		if err != nil || !found {
			t.Fatalf("watch readback missing: found=%v err=%v", found, err)
		}
		var request c.WatchRequest
		if err := json.Unmarshal([]byte(stored.PayloadJSON), &request); err != nil {
			t.Fatal(err)
		}
		return request
	}
	derivedRSS, _, err := register("RSS derived", []string{"rss:source"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := readKinds(t, derivedRSS).SourceKinds; len(got) != 1 || got[0] != "rss" {
		t.Fatalf("omitted SourceKinds did not derive RSS: %v", got)
	}
	explicitRSS, _, err := register("RSS explicit", []string{"rss:source"}, []string{"rss"})
	if err != nil || explicitRSS.State != "active" {
		t.Fatalf("explicit RSS kind rejected: receipt=%+v err=%v", explicitRSS, err)
	}
	if _, _, err := register("RSS mismatch", []string{"rss:source"}, []string{"web"}); err == nil {
		t.Fatal("explicit mismatching source kind was admitted")
	}
	mixed, _, err := register("Mixed stable", []string{"rss:source", "web:source"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := readKinds(t, mixed).SourceKinds; len(got) != 2 || got[0] != "rss" || got[1] != "web" {
		t.Fatalf("mixed source kinds were not sorted and deduplicated: %v", got)
	}
	mixedExplicit, _, err := register("Mixed stable", []string{"web:source", "rss:source"}, []string{"web", "rss", "web"})
	if err != nil {
		t.Fatalf("explicit mixed source set did not normalize: %v", err)
	}
	if mixedExplicit.WatchID != mixed.WatchID {
		t.Fatalf("equivalent sorted/deduplicated source sets did not preserve idempotency: %s != %s", mixedExplicit.WatchID, mixed.WatchID)
	}
	if got := readKinds(t, mixedExplicit).SourceKinds; len(got) != 2 || got[0] != "rss" || got[1] != "web" {
		t.Fatalf("explicit source kinds were not sorted and deduplicated: %v", got)
	}
	if _, _, err := register("Unsupported kind", []string{"api:source"}, nil); err == nil {
		t.Fatal("unsupported registry kind was admitted for future refresh")
	}
}

func TestWatchAdapterRegistersThroughExistingOwnerAndReadsExactReceipt(t *testing.T) {
	ctx := context.Background()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{SourceID: "official:source", URL: "https://example.org/source", Kind: l1sqlite.L1SourceKindWebGather, Enabled: true, TrustScore: 1, FetchInterval: time.Hour, LicenseNote: "fixture explicitly permits retention"})
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewWatchAdapter(store, func(context.Context, c.Candidate) (bool, bool) { return true, true })
	e := &engine.Engine{Ports: engine.Ports{Watch: adapter}}
	env := c.Envelope{TaskID: string(core.NewTaskID()), RunID: string(core.NewRunID()), TraceID: string(core.NewTraceID()), RequestID: string(core.NewRequestID()), Actor: "ren", OwnerID: "ren", DataScope: "user", PolicyRevision: "fixture.v1"}
	request := c.WatchRequest{Objective: "Follow the explicitly registered source", SourceRefs: []string{"official:source"}, SourceKinds: []string{"web"}, Cadence: "2h"}
	got, err := e.Watch(ctx, env, request)
	if err != nil || got.Status != "succeeded" || got.State != "active" || got.Receipt.State != "committed" || got.NextDueAt.Before(time.Now().Add(time.Hour)) {
		t.Fatalf("watch=%+v err=%v", got, err)
	}
	again, err := e.Watch(ctx, env, request)
	if err != nil || again.WatchID != got.WatchID || again.Receipt.CanonicalID != got.Receipt.CanonicalID {
		t.Fatalf("duplicate registration: %+v %v", again, err)
	}
	if _, found, err := store.GetResearchWatch(ctx, "user", "different-owner", got.WatchID); err != nil || found {
		t.Fatal("foreign watch exposed")
	}
	request.Cadence = "1m"
	if _, err = e.Watch(ctx, env, request); err == nil {
		t.Fatal("source cadence policy bypassed")
	}
	request.Cadence = "2h"
	request.InitialRun = true
	if _, err = e.Watch(ctx, env, request); err == nil {
		t.Fatal("unconnected initial run reported successful")
	}
	request.InitialRun = false
	request.Outputs.Save = []c.SaveTarget{{Target: "board", Required: true, DestinationRef: "unregistered"}}
	if _, err = e.Watch(ctx, env, request); err == nil {
		t.Fatal("unconnected board watch admitted")
	}
}
