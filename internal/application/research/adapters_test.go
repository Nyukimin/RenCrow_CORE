package research

import (
	"context"
	"database/sql"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appwebgather "github.com/Nyukimin/RenCrow_CORE/internal/application/webgather"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	_ "modernc.org/sqlite"
)

type searchStub struct {
	request modulewebgather.SearchRequest
	result  modulewebgather.SearchResponse
	err     error
	calls   int
}

func (s *searchStub) Search(_ context.Context, req modulewebgather.SearchRequest) (modulewebgather.SearchResponse, error) {
	s.request = req
	s.calls++
	return s.result, s.err
}

type fetchStub struct {
	request modulewebgather.FetchRequest
	result  modulewebgather.FetchResponse
	err     error
}

func (f *fetchStub) ClaimPublicAcquisition(_ context.Context, rawURL string, now time.Time) (l1sqlite.L1PublicAcquisitionLease, bool, error) {
	canonicalURL, err := l1sqlite.CanonicalPublicAcquisitionURL(rawURL)
	if err != nil {
		return l1sqlite.L1PublicAcquisitionLease{}, false, err
	}
	return l1sqlite.L1PublicAcquisitionLease{CanonicalURL: canonicalURL, Token: "test-acquisition-token", ExpiresAt: now.Add(l1sqlite.L1PublicAcquisitionLeaseTTL)}, true, nil
}

func (f *fetchStub) ReleasePublicAcquisition(context.Context, l1sqlite.L1PublicAcquisitionLease, time.Time) (bool, error) {
	return true, nil
}

func (f *fetchStub) FetchURL(_ context.Context, req modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	f.request = req
	return f.result, f.err
}

type stagingStub struct {
	item           l1sqlite.L1StagingItem
	readErr        error
	mutateReadback func(*l1sqlite.L1StagingItem)
}

type adapterRefreshPort struct{ plan c.RefreshPlan }

func (p *adapterRefreshPort) Prepare(_ context.Context, env c.Envelope, watchID string) (c.RefreshPlan, error) {
	if env != p.plan.Execution || watchID != p.plan.WatchID {
		return c.RefreshPlan{}, errors.New("fixture refresh plan identity mismatch")
	}
	return p.plan, nil
}

func (p *adapterRefreshPort) Finish(_ context.Context, env c.Envelope, plan c.RefreshPlan, execution c.RefreshExecution) (c.RefreshReceipt, error) {
	const cursor = `{"cursor":"next"}`
	now := time.Now().UTC()
	receipt := c.RefreshReceipt{WatchID: plan.WatchID, PlanID: plan.PlanID, State: "committed", Cursor: cursor, CursorCommitted: true, CursorAdvanced: cursor != plan.PreviousCursor}
	receipt.Receipt = c.Receipt{Owner: "fixture", OwnerRoute: "watch_cursor", AuditRef: "watch-attempt", CanonicalID: plan.WatchID, Digest: engine.RefreshCheckpointDigest(plan, execution, cursor), State: "committed", Scope: env.DataScope, CommittedAt: now, VerifiedAt: now}
	return receipt, nil
}

func (s *stagingStub) SaveStagingItem(_ context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error) {
	item.ID = item.Namespace + ":" + item.EventID
	item.CreatedAt = time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	s.item = item
	return &item, nil
}

func (s *stagingStub) FindStagingItemByNamespaceEventID(_ context.Context, namespace, eventID string) (l1sqlite.L1StagingItem, bool, error) {
	if s.readErr != nil {
		return l1sqlite.L1StagingItem{}, false, s.readErr
	}
	if s.item.Namespace != namespace || s.item.EventID != eventID {
		return l1sqlite.L1StagingItem{}, false, nil
	}
	item := s.item
	if s.mutateReadback != nil {
		s.mutateReadback(&item)
	}
	return item, true, nil
}

func (s *stagingStub) RecentStagingItems(_ context.Context, status string, _ int) ([]l1sqlite.L1StagingItem, error) {
	if s.item.ValidationStatus != status {
		return nil, nil
	}
	return []l1sqlite.L1StagingItem{s.item}, nil
}

func TestDiscoverMapsWebGatherSearchResults(t *testing.T) {
	search := &searchStub{result: modulewebgather.SearchResponse{
		Provider: "bing_rss", Results: []modulewebgather.SearchResult{{
			URL: "https://example.org/path#fragment", Title: "Source", Snippet: "Preview", Rank: 2, SourceEngine: "bing_rss",
		}},
	}}
	a := NewAdapters(search, nil, nil)
	a.now = func() time.Time { return time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC) }
	got, err := a.Discover(context.Background(), engine.DiscoveryRequest{
		Request:  c.Request{Query: "topic", SourceKinds: []string{"web"}, Limits: c.Limits{Candidates: 4}},
		Provider: c.Capability{ProviderID: "bing_rss", Search: true, Enabled: true, Ready: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if search.request.Limit != 4 || search.request.Query != "topic" || len(got.Candidates) != 1 {
		t.Fatalf("unexpected search mapping: request=%+v candidates=%+v", search.request, got.Candidates)
	}
	if got.Candidates[0].URL != "https://example.org/path" || got.Candidates[0].SourceKind != "web" || got.Candidates[0].Rank != 2 {
		t.Fatalf("unexpected candidate: %+v", got.Candidates[0])
	}
}

func TestRefreshDiscoveryBypassesWarmSearchCacheAndOrdinaryDiscoveryDoesNot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l1.Close() })
	cache := appwebgather.NewL1SearchCache(l1)
	feedURL := "https://example.org/research.xml"
	staleURL := "https://example.org/stale-entry"
	liveURL := "https://example.org/new-entry"
	if err := cache.Save(ctx, "rss_atom", feedURL, []modulewebgather.SearchResult{{URL: staleURL, Title: "Cached", SourceEngine: "rss_atom"}}, 6*time.Hour); err != nil {
		t.Fatal(err)
	}
	search := &searchStub{result: modulewebgather.SearchResponse{Provider: "rss_atom", Results: []modulewebgather.SearchResult{{URL: liveURL, Title: "Live", SourceEngine: "rss_atom"}}}}
	searchUseCase := appwebgather.NewSearchUseCase(cache, map[string]modulewebgather.SearchProvider{"rss_atom": search})
	text := "A newly published feed entry."
	fetch := &fetchStub{result: modulewebgather.FetchResponse{URL: liveURL, FinalURL: liveURL, Status: "ok", ContentType: "text/html", ExtractedText: text, ExtractedChars: len([]rune(text))}}
	sourcePolicy := func(context.Context, c.Candidate) (bool, bool) { return true, true }
	owner := NewAdapters(searchUseCase, fetch, nil).WithSourcePolicy(sourcePolicy)
	capability := c.Capability{ProviderID: "rss_atom", SourceKinds: []string{"rss"}, Search: true, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true}
	ordinaryRequest := c.Request{Query: feedURL, SourceKinds: []string{"rss"}, Limits: c.Limits{Candidates: 4}}
	ordinary, err := owner.Discover(ctx, engine.DiscoveryRequest{Request: ordinaryRequest, Provider: capability})
	if err != nil || len(ordinary.Candidates) != 1 || ordinary.Candidates[0].URL != staleURL || search.calls != 0 {
		t.Fatalf("ordinary discovery did not reuse the warm cache: candidates=%+v calls=%d err=%v", ordinary.Candidates, search.calls, err)
	}
	env := c.Envelope{TaskID: "tsk_11111111-1111-7111-8111-111111111111", RunID: "run_11111111-1111-7111-8111-111111111111", TraceID: "trc_11111111-1111-7111-8111-111111111111", RequestID: "req_11111111-1111-7111-8111-111111111111", Actor: "research-test", OwnerID: "owner-a", DataScope: "user", PolicyRevision: "fixture.v1"}
	refreshRequest, err := engine.NormalizeRefreshPlan(c.Request{
		SchemaVersion: c.RequestSchema, Mode: "refresh", WatchID: "rw_12345678", Objective: "Follow the registered feed", Query: feedURL,
		SourceKinds: []string{"rss"}, Outputs: c.Outputs{Save: []c.SaveTarget{{Target: "artifact", Required: true}}},
		Limits:   c.Limits{SearchCalls: 20, Candidates: 100, FetchAttempts: 100, MaxDocuments: 50, DeadlineSeconds: 600, AnalysisCalls: 0},
		Language: "ja", CostProfile: "zero_incremental_cost",
	})
	if err != nil {
		t.Fatal(err)
	}
	plan := c.RefreshPlan{WatchID: "rw_12345678", PlanID: "fixture-plan", LeaseToken: "fixture-lease", Execution: env, Scope: env.DataScope, OwnerID: env.OwnerID, PolicyRevision: env.PolicyRevision, SourcePolicyRevision: "fixture-source-policy.v1", Request: refreshRequest, Sources: []c.RefreshSource{{SourceRef: "feed:registered", SeedURL: feedURL, SourceKind: "rss", ProviderID: "rss_atom"}}, PreviousCursor: "{}", PreviousItemHashes: map[string]string{}}
	plan.PlanDigest = engine.RefreshPlanDigest(plan)
	refreshPort := &adapterRefreshPort{plan: plan}
	artifacts, err := engine.NewFileStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifacts.Close() })
	runner := &engine.Engine{Ports: engine.Ports{Store: artifacts, Discovery: owner, Acquisition: owner, Owner: owner, Refresh: refreshPort, Capabilities: []c.Capability{capability}}}
	result, err := runner.Run(ctx, env, c.Request{SchemaVersion: c.RequestSchema, Mode: "refresh", WatchID: plan.WatchID})
	if err != nil || result.Status != "succeeded" || len(result.Candidates) != 1 || result.Candidates[0].URL != liveURL || search.calls != 1 || !search.request.Refresh {
		t.Fatalf("refresh discovery reused the stale search result: result=%+v provider_calls=%d search_request=%+v err=%v", result, search.calls, search.request, err)
	}
	_, err = owner.Discover(ctx, engine.DiscoveryRequest{Request: c.Request{Query: "https://example.org/uncached.xml", SourceKinds: []string{"rss"}, Limits: c.Limits{Candidates: 4}}, Provider: capability})
	if err != nil || search.request.Refresh || search.calls != 2 {
		t.Fatalf("ordinary live discovery was marked fresh: request=%+v calls=%d err=%v", search.request, search.calls, err)
	}
}

func TestAcquireUsesNonStagingHTTPAndRejectsPreviewOnly(t *testing.T) {
	fetch := &fetchStub{result: modulewebgather.FetchResponse{
		URL: "https://example.org", Status: "ok", TextExcerpt: "only preview", TextTruncated: true,
	}}
	a := NewAdapters(nil, fetch, nil)
	got, err := a.Acquire(context.Background(), c.Candidate{URL: "https://example.org"})
	if err == nil || got.Code != "unavailable" {
		t.Fatalf("preview-only fetch must not be represented as a document: result=%+v err=%v", got, err)
	}
	if fetch.request.StoreStaging || !fetch.request.StoreStagingSet || fetch.request.FetchProvider != modulewebgather.DefaultFetchProvider || fetch.request.Policy.MaxBodyBytes <= 0 {
		t.Fatalf("fetch did not use non-staging standard HTTP policy: %+v", fetch.request)
	}
}

func TestAcquireRejectsStructuralAuthInterstitialSignal(t *testing.T) {
	text := "Please sign in to continue."
	fetch := &fetchStub{result: modulewebgather.FetchResponse{
		URL: "https://example.org/source", Status: "ok", ContentType: "text/html", ExtractedText: text,
		ExtractedChars: len([]rune(text)), Diagnostics: map[string]any{"auth_interstitial": true},
	}}
	result, err := NewAdapters(nil, fetch, nil).Acquire(context.Background(), c.Candidate{URL: fetch.result.URL})
	if err == nil || result.Code != "unavailable" {
		t.Fatalf("structurally identified auth interstitial counted as a document: result=%+v err=%v", result, err)
	}
}

func TestAcquireRetentionRequiresInjectedSourcePolicy(t *testing.T) {
	text := "Fetched source text"
	response := modulewebgather.FetchResponse{
		URL: "https://example.org/source", Status: "ok", ExtractedText: text, ExtractedChars: len([]rune(text)),
		RawHash: modulewebgather.SHA256Text(text),
	}
	candidate := c.Candidate{URL: response.URL, SourceKind: "web", ProviderID: "test"}
	withoutPolicy := NewAdapters(nil, &fetchStub{result: response}, nil)
	doc, err := withoutPolicy.Acquire(context.Background(), candidate)
	if err != nil || doc.Document.ContentState != "fetched" || doc.Document.RetentionAllowed || doc.Document.Primary {
		t.Fatalf("missing source policy must keep retention and primary claims false: result=%+v err=%v", doc, err)
	}
	withPolicy := NewAdapters(nil, &fetchStub{result: response}, nil).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) {
		return true, true
	})
	doc, err = withPolicy.Acquire(context.Background(), candidate)
	if err != nil || !doc.Document.RetentionAllowed || !doc.Document.Primary || doc.Document.ExtractedHash != digestBytes([]byte(text)) {
		t.Fatalf("injected trusted source policy was not applied: result=%+v err=%v", doc, err)
	}
	if doc.Document.OriginalHash != "" {
		t.Fatal("WebGather extracted-text hash was incorrectly labeled as original-byte hash")
	}
}

func TestAcquireRedirectRequiresTrustedFinalURL(t *testing.T) {
	text := "Fetched source text"
	response := modulewebgather.FetchResponse{
		URL: "https://example.org/source", FinalURL: "https://redirect.example/source", Status: "ok",
		ExtractedText: text, ExtractedChars: len([]rune(text)),
	}
	candidate := c.Candidate{URL: response.URL, SourceKind: "web", ProviderID: "test"}
	adapter := NewAdapters(nil, &fetchStub{result: response}, nil).WithSourcePolicy(func(_ context.Context, candidate c.Candidate) (bool, bool) {
		return candidate.URL == "https://example.org/source", candidate.URL == "https://example.org/source"
	})
	got, err := adapter.Acquire(context.Background(), candidate)
	if err != nil || got.Document.RetentionAllowed || got.Document.Primary {
		t.Fatalf("redirect to an untrusted final URL must deny retention and primary: result=%+v err=%v", got, err)
	}
}

func TestCommitRequiresScopeAndDigestAndVerifiesExactReadback(t *testing.T) {
	store := &stagingStub{}
	a := NewAdapters(nil, nil, store).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	text := "Complete research text"
	hash := digestBytes([]byte(text))
	request := engine.CommitRequest{
		Envelope: c.Envelope{RunID: "run_test", DataScope: "public"},
		Target:   c.SaveTarget{Target: KnowledgeStagingTarget},
		Document: &c.Document{
			SourceKind: "web", ProviderID: "bing_rss", SourceRef: "https://example.org/source", OriginalURL: "https://example.org/source",
			FinalURL: "https://example.org/source", ContentScope: "extracted_text", ContentState: "fetched", ExtractedHash: hash, Text: text,
			Complete: true, RetentionAllowed: true,
		},
		Digest: hash,
	}
	request.IdempotencyKey = IdempotencyKey(request.Envelope.RunID, request.Target.Target, request.Target.DestinationRef, hash)
	receipt, err := a.Commit(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Scope != request.Envelope.DataScope || receipt.Digest != hash || receipt.CanonicalID != store.item.ID || receipt.OwnerRoute != KnowledgeStagingTarget {
		t.Fatalf("receipt does not bind exact staged content and scope: %+v", receipt)
	}
	if store.item.Namespace != "kb:research" || store.item.RawHash != hash || store.item.Meta["research_scope"] != request.Envelope.DataScope {
		t.Fatalf("staged record lost digest or scope: %+v", store.item)
	}
	legacySourceID := modulewebgather.SourceIDFromURL("https://example.org/source")
	if store.item.EventID != modulewebgather.EventID(legacySourceID, "https://example.org/source", hash) || store.item.RawHash != hash {
		t.Fatalf("revisionless HTTP event/content hash compatibility changed: item=%+v", store.item)
	}
	if _, err := a.Verify(context.Background(), request.Envelope, receipt); err != nil {
		t.Fatalf("exact receipt verification failed: %v", err)
	}
	store.item.ValidationStatus = l1sqlite.L1StagingStatusValidated
	if _, err := a.Verify(context.Background(), request.Envelope, receipt); err != nil {
		t.Fatalf("validated staging record should remain verifiable: %v", err)
	}
	request.Envelope.DataScope = "user"
	if _, err := a.Verify(context.Background(), request.Envelope, receipt); err == nil {
		t.Fatal("verification accepted a receipt from another scope")
	}
}

func TestVerifyRejectsChangedStoredTextRightsOrCompleteness(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutate   func(*l1sqlite.L1StagingItem)
		policy   SourcePolicy
		finalURL string
	}{
		{name: "raw text changed", mutate: func(item *l1sqlite.L1StagingItem) { item.RawText += " changed" }, policy: func(context.Context, c.Candidate) (bool, bool) { return true, true }},
		{name: "retention revoked", mutate: func(item *l1sqlite.L1StagingItem) { item.Meta["retention_allowed"] = false }, policy: func(context.Context, c.Candidate) (bool, bool) { return true, true }},
		{name: "source policy revoked", mutate: func(*l1sqlite.L1StagingItem) {}, policy: func(context.Context, c.Candidate) (bool, bool) { return false, false }},
		{name: "redirect target revoked", finalURL: "https://redirect.example/source", mutate: func(*l1sqlite.L1StagingItem) {}, policy: func(_ context.Context, candidate c.Candidate) (bool, bool) {
			return candidate.URL != "https://redirect.example/source", true
		}},
		{name: "incomplete metadata", mutate: func(item *l1sqlite.L1StagingItem) { item.Meta["content_complete"] = false }, policy: func(context.Context, c.Candidate) (bool, bool) { return true, true }},
		{name: "rejected validation", mutate: func(item *l1sqlite.L1StagingItem) { item.ValidationStatus = l1sqlite.L1StagingStatusRejected }, policy: func(context.Context, c.Candidate) (bool, bool) { return true, true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &stagingStub{}
			adapter := NewAdapters(nil, nil, store).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
			text := "Complete source text"
			hash := digestBytes([]byte(text))
			finalURL := test.finalURL
			if finalURL == "" {
				finalURL = "https://example.org/source"
			}
			request := engine.CommitRequest{
				Envelope: c.Envelope{RunID: "run_verify", DataScope: "public"},
				Target:   c.SaveTarget{Target: KnowledgeStagingTarget},
				Document: &c.Document{SourceKind: "web", ProviderID: "direct", SourceRef: "source-fixture", OriginalURL: "https://example.org/source", FinalURL: finalURL, ContentScope: "extracted_text", ContentState: "fetched", ExtractedHash: hash, Text: text, Complete: true, RetentionAllowed: true},
				Digest:   hash,
			}
			request.IdempotencyKey = IdempotencyKey(request.Envelope.RunID, request.Target.Target, "", hash)
			receipt, err := adapter.Commit(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if test.mutate != nil {
				test.mutate(&store.item)
			}
			adapter.WithSourcePolicy(test.policy)
			if _, err := adapter.Verify(context.Background(), request.Envelope, receipt); err == nil {
				t.Fatal("verification accepted changed source content, rights, validation, or completeness")
			}
		})
	}
}

func TestCommitRejectsMissingRetentionAndUnstableIdempotency(t *testing.T) {
	a := NewAdapters(nil, nil, &stagingStub{})
	text := "retained text"
	hash := digestBytes([]byte(text))
	request := engine.CommitRequest{
		Envelope: c.Envelope{RunID: "run_test", DataScope: "public"},
		Target:   c.SaveTarget{Target: KnowledgeStagingTarget},
		Document: &c.Document{FinalURL: "https://example.org", ContentState: "fetched", ExtractedHash: hash, Text: text, Complete: true},
		Digest:   hash, IdempotencyKey: IdempotencyKey("run_test", KnowledgeStagingTarget, "", hash),
	}
	if _, err := a.Commit(context.Background(), request); err == nil {
		t.Fatal("commit accepted a document without retention permission")
	}
	request.Document.RetentionAllowed = true
	request.Document.ContentState = "fetched"
	request.IdempotencyKey = "unstable"
	if _, err := a.Commit(context.Background(), request); err == nil {
		t.Fatal("commit accepted an idempotency key not bound to scope and digest")
	}
	request.IdempotencyKey = IdempotencyKey(request.Envelope.RunID, request.Target.Target, request.Target.DestinationRef, hash)
	request.Target.DestinationRef = "kb:attacker-selected"
	if _, err := a.Commit(context.Background(), request); err == nil {
		t.Fatal("commit accepted an agent-selected staging namespace")
	}
}

func TestStagingNamespaceDerivesOnlyFromTrustedEnvelope(t *testing.T) {
	if got, err := stagingNamespace(c.Envelope{DataScope: "public"}); err != nil || got != "kb:research" {
		t.Fatalf("public scope mapping = %q, %v", got, err)
	}
	if got, err := stagingNamespace(c.Envelope{DataScope: "user", OwnerID: "owner-1"}); err != nil || got != "user:owner-1" {
		t.Fatalf("user scope mapping = %q, %v", got, err)
	}
	for _, envelope := range []c.Envelope{{DataScope: "user"}, {DataScope: "user", OwnerID: " owner-1 "}, {DataScope: "kb:research"}} {
		if _, err := stagingNamespace(envelope); err == nil {
			t.Fatalf("accepted unauthorized or noncanonical scope: %+v", envelope)
		}
	}
}

func TestCommitRejectsMismatchedReadback(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*l1sqlite.L1StagingItem)
	}{
		{name: "id", mutate: func(item *l1sqlite.L1StagingItem) { item.ID = "different" }},
		{name: "url", mutate: func(item *l1sqlite.L1StagingItem) { item.SourceURL = "https://other.example/" }},
		{name: "hash", mutate: func(item *l1sqlite.L1StagingItem) { item.RawHash = "different" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &stagingStub{mutateReadback: test.mutate}
			a := NewAdapters(nil, nil, store)
			text := "complete"
			hash := digestBytes([]byte(text))
			request := engine.CommitRequest{
				Envelope: c.Envelope{RunID: "run_test", DataScope: "public"},
				Target:   c.SaveTarget{Target: KnowledgeStagingTarget},
				Document: &c.Document{SourceKind: "web", ProviderID: "direct", SourceRef: "source-fixture", OriginalURL: "https://example.org/", FinalURL: "https://example.org/", ContentScope: "extracted_text", ContentState: "fetched", ExtractedHash: hash, Text: text, Complete: true, RetentionAllowed: true},
				Digest:   hash,
			}
			request.IdempotencyKey = IdempotencyKey(request.Envelope.RunID, request.Target.Target, request.Target.DestinationRef, hash)
			if _, err := a.Commit(context.Background(), request); err == nil {
				t.Fatal("commit accepted mismatched exact readback")
			}
		})
	}
}

func TestVerifyPropagatesReadbackFailure(t *testing.T) {
	store := &stagingStub{readErr: errors.New("readback failed")}
	a := NewAdapters(nil, nil, store)
	_, err := a.Verify(context.Background(), c.Envelope{DataScope: "public"}, c.Receipt{
		OwnerRoute: KnowledgeStagingTarget, Scope: "public", AuditRef: "event", CanonicalID: "record",
	})
	if err == nil {
		t.Fatal("verification swallowed a readback failure")
	}
}

func TestResearchRevisionIdentityAndMetadataRoundTripThroughSQLiteOwner(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "research-owner.db")
	store, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	adapter := NewAdapters(nil, nil, store).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	envelope := c.Envelope{RunID: "run_revision", DataScope: "public"}
	text := "An exact immutable revision body."
	digest := digestBytes([]byte(text))
	published := time.Date(2026, 11, 20, 10, 11, 12, 123456789, time.UTC)
	makeRequest := func(revision string) engine.CommitRequest {
		doc := &c.Document{SourceKind: "mediawiki", ProviderID: "mediawiki", SourceRef: "https://wiki.example/page?oldid=" + revision,
			OriginalURL: "https://wiki.example/page", FinalURL: "https://wiki.example/page", Revision: revision,
			PublishedAt: &published, ContentScope: "revision_text", ContentState: "fetched", ExtractedHash: digest, Text: text, Complete: true, RetentionAllowed: true}
		req := engine.CommitRequest{Envelope: envelope, Target: c.SaveTarget{Target: KnowledgeStagingTarget}, Document: doc, Digest: digest}
		req.IdempotencyKey = IdempotencyKey(envelope.RunID, req.Target.Target, "", digest)
		return req
	}
	first, err := adapter.Commit(ctx, makeRequest("42"))
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := adapter.Commit(ctx, makeRequest("42"))
	if err != nil {
		t.Fatal(err)
	}
	if first.AuditRef != repeated.AuditRef || first.CanonicalID != repeated.CanonicalID || first.Digest != digest {
		t.Fatalf("same revision did not preserve owner identity: first=%+v repeated=%+v", first, repeated)
	}
	other, err := adapter.Commit(ctx, makeRequest("43"))
	if err != nil {
		t.Fatal(err)
	}
	if other.AuditRef == first.AuditRef || other.CanonicalID == first.CanonicalID || other.Digest != digest {
		t.Fatalf("different revision/body identity was not separated without changing content digest: first=%+v other=%+v", first, other)
	}
	recall, err := NewLocalRecall(nil, store, nil).Recall(ctx, envelope, c.RecallRequest{Query: "immutable revision body", Targets: []string{"documents"}, Limit: 20})
	if err != nil || len(recall.Hits) != 2 {
		t.Fatalf("revision-bound records were not recallable: result=%+v err=%v", recall, err)
	}
	seenRevisions, seenRefs := map[string]bool{}, map[string]bool{}
	for _, hit := range recall.Hits {
		seenRevisions[hit.Revision] = true
		seenRefs[hit.CanonicalRef] = true
	}
	if !seenRevisions["42"] || !seenRevisions["43"] || len(seenRefs) != 2 {
		t.Fatalf("recall lost acquired revision identity: hits=%+v", recall.Hits)
	}
	item, found, err := store.FindStagingItemByNamespaceEventID(ctx, "kb:research", first.AuditRef)
	if err != nil || !found {
		t.Fatalf("read first revision: found=%t err=%v", found, err)
	}
	if item.RawHash != digest || !item.PublishedAt.Equal(published) || item.Meta["revision"] != "42" || item.Meta["source_ref"] != "https://wiki.example/page?oldid=42" || item.Meta["published_at"] != published.Format(time.RFC3339Nano) {
		t.Fatalf("revision metadata was not durably preserved: item=%+v", item)
	}
	if _, err := adapter.Verify(ctx, envelope, first); err != nil {
		t.Fatalf("verify exact revision failed: %v", err)
	}
	if _, err := adapter.Reconcile(ctx, makeRequest("43")); err != nil {
		t.Fatalf("reconcile exact revision failed: %v", err)
	}

	// Tamper through a second SQLite connection to exercise owner readback rather than a stub.
	otherDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer otherDB.Close()
	var metaJSON string
	if err := otherDB.QueryRowContext(ctx, `SELECT meta_json FROM l1_staging_item WHERE namespace=? AND event_id=?`, "kb:research", first.AuditRef).Scan(&metaJSON); err != nil {
		t.Fatal(err)
	}
	metaJSON = strings.Replace(metaJSON, `"revision":"42"`, `"revision":"tampered"`, 1)
	if _, err := otherDB.ExecContext(ctx, `UPDATE l1_staging_item SET meta_json=? WHERE namespace=? AND event_id=?`, metaJSON, "kb:research", first.AuditRef); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Verify(ctx, envelope, first); err == nil {
		t.Fatal("verification accepted tampered revision metadata")
	}
}

func TestPyPIRevisionScopeAndPublishedAtRoundTrip(t *testing.T) {
	store := &stagingStub{}
	adapter := NewAdapters(nil, nil, store).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	body, digest := "A package description.", digestBytes([]byte("A package description."))
	published := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
	req := engine.CommitRequest{Envelope: c.Envelope{RunID: "run_pypi", DataScope: "public"}, Target: c.SaveTarget{Target: KnowledgeStagingTarget}, Digest: digest,
		Document: &c.Document{SourceKind: "pypi", ProviderID: "pypi", SourceRef: "https://pypi.org/project/demo/1.2/", Revision: "1.2", OriginalURL: "https://pypi.org/project/demo/1.2/", FinalURL: "https://pypi.org/project/demo/1.2/", PublishedAt: &published, ContentScope: "package_description", ContentState: "fetched", ExtractedHash: digest, Text: body, Complete: true, RetentionAllowed: true}}
	req.IdempotencyKey = IdempotencyKey(req.Envelope.RunID, req.Target.Target, "", digest)
	receipt, err := adapter.Commit(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Verify(context.Background(), req.Envelope, receipt); err != nil {
		t.Fatal(err)
	}
	if store.item.Meta["content_scope"] != "package_description" || store.item.Meta["revision"] != "1.2" || !store.item.PublishedAt.Equal(published) {
		t.Fatalf("PyPI source version was not preserved: %+v", store.item)
	}
}

func TestCommitRejectsUnregisteredSourceContentScopeCombination(t *testing.T) {
	text := "scope fixture"
	digest := digestBytes([]byte(text))
	store := &stagingStub{}
	adapter := NewAdapters(nil, nil, store).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	req := engine.CommitRequest{Envelope: c.Envelope{RunID: "run_scope", DataScope: "public"}, Target: c.SaveTarget{Target: KnowledgeStagingTarget}, Digest: digest,
		Document: &c.Document{SourceKind: "mediawiki", ProviderID: "mediawiki", SourceRef: "page", Revision: "42", OriginalURL: "https://wiki.example/page", FinalURL: "https://wiki.example/page", ContentScope: "extracted_text", ContentState: "fetched", ExtractedHash: digest, Text: text, Complete: true, RetentionAllowed: true}}
	req.IdempotencyKey = IdempotencyKey(req.Envelope.RunID, req.Target.Target, "", digest)
	if _, err := adapter.Commit(context.Background(), req); err == nil {
		t.Fatal("commit accepted an unregistered provider/content-scope combination")
	}
	if store.item.EventID != "" {
		t.Fatalf("rejected content-scope combination was persisted: %+v", store.item)
	}
}

type researchEngineStore struct {
	artifacts map[string][]byte
	runs      map[string]c.RunRecord
}

func newResearchEngineStore() *researchEngineStore {
	return &researchEngineStore{artifacts: make(map[string][]byte), runs: make(map[string]c.RunRecord)}
}

func (s *researchEngineStore) Put(_ context.Context, data []byte) (c.ArtifactRef, error) {
	hash := digestBytes(data)
	ref := "memory:" + hash
	s.artifacts[ref] = append([]byte(nil), data...)
	return c.ArtifactRef{Kind: "artifact", Hash: hash, Ref: ref}, nil
}

func (s *researchEngineStore) Get(_ context.Context, ref c.ArtifactRef) ([]byte, error) {
	data, ok := s.artifacts[ref.Ref]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), data...), nil
}

func (s *researchEngineStore) Load(_ context.Context, runID string) (c.RunRecord, error) {
	record, ok := s.runs[runID]
	if !ok {
		return c.RunRecord{}, fs.ErrNotExist
	}
	return record, nil
}

func (s *researchEngineStore) Save(_ context.Context, record c.RunRecord) error {
	s.runs[record.Envelope.RunID] = record
	return nil
}

func TestEngineRunCollectsAndCommitsThroughAdaptersToTemporaryL1(t *testing.T) {
	staging, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "research.db"))
	if err != nil {
		t.Fatalf("create temporary L1 store: %v", err)
	}
	defer staging.Close()

	text := "A complete source document for the integration test."
	fetch := &fetchStub{result: modulewebgather.FetchResponse{
		URL: "https://example.org/source", FinalURL: "https://example.org/source", Status: "ok",
		ContentType: "text/html", Title: "Source", ExtractedText: text, ExtractedChars: len([]rune(text)),
	}}
	ports := NewAdapters(nil, fetch, staging).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) {
		return true, false
	})
	ownerStore := newResearchEngineStore()
	engineInstance := &engine.Engine{Ports: engine.Ports{
		Acquisition: ports, Owner: ports, Store: ownerStore,
		Capabilities: []c.Capability{{ProviderID: "direct", SourceKinds: []string{"web"}, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true}},
	}}
	envelope := c.Envelope{
		TaskID: "tsk_research_test", RunID: "run_research_test", TraceID: "trc_research_test",
		RequestID: "req_research_test", Actor: "owner", DataScope: "public", PolicyRevision: "policy-test",
	}
	request := c.Request{
		SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "Collect a bounded source",
		SourceKinds: []string{"web"}, SeedURLs: []string{"https://example.org/source"},
		Outputs: c.Outputs{
			Save:     []c.SaveTarget{{Target: KnowledgeStagingTarget, Required: true}},
			Evidence: c.EvidenceRequirements{MinDocuments: 1, MinIndependentSources: 1},
		},
		Limits: c.Limits{Candidates: 1, FetchAttempts: 1, MaxDocuments: 1, DeadlineSeconds: 30},
	}
	result, err := engineInstance.Run(context.Background(), envelope, request)
	if err != nil {
		t.Fatalf("run research engine: %v", err)
	}
	if result.Status != "succeeded" || result.Counts.DocumentsFetched != 1 || result.Counts.RecordsSaved != 1 || len(result.Receipts) != 1 {
		t.Fatalf("engine did not complete collection and commit: %+v", result)
	}
	receipt, err := ports.Verify(context.Background(), envelope, result.Receipts[0])
	if err != nil {
		t.Fatalf("verify durable receipt: %v", err)
	}
	if receipt.State != "committed" || receipt.Scope != "public" || receipt.Digest != digestBytes([]byte(text)) {
		t.Fatalf("receipt does not match engine contract: %+v", receipt)
	}
	item, found, err := staging.FindStagingItemByNamespaceEventID(context.Background(), "kb:research", receipt.AuditRef)
	if err != nil || !found || item.ID != receipt.CanonicalID || item.RawText != text || item.RawHash != receipt.Digest {
		t.Fatalf("temporary L1 record does not match verified receipt: found=%v item=%+v err=%v", found, item, err)
	}
}

type adapterRefreshStub struct{ plan c.RefreshPlan }

func (r *adapterRefreshStub) Prepare(context.Context, c.Envelope, string) (c.RefreshPlan, error) {
	return r.plan, nil
}

func (r *adapterRefreshStub) Finish(_ context.Context, _ c.Envelope, plan c.RefreshPlan, _ c.RefreshExecution) (c.RefreshReceipt, error) {
	return c.RefreshReceipt{
		WatchID: plan.WatchID, PlanID: plan.PlanID, State: "retry_scheduled", Cursor: plan.PreviousCursor,
		AttemptRef: strings.Repeat("a", 64), RetryAt: time.Now().UTC().Add(time.Minute),
	}, nil
}

func TestRefreshAcquisitionForcesWebGatherRefresh(t *testing.T) {
	envelope := c.Envelope{
		TaskID: "tsk_11111111-1111-7111-8111-111111111111", RunID: "run_11111111-1111-7111-8111-111111111111",
		TraceID: "trc_11111111-1111-7111-8111-111111111111", RequestID: "req_11111111-1111-7111-8111-111111111111",
		Actor: "owner", DataScope: "public", PolicyRevision: "policy-test",
	}
	watchID := "rw_watch123"
	plan := c.RefreshPlan{
		WatchID: watchID, PlanID: "plan_123", LeaseToken: "lease-test", Execution: envelope, Scope: "public", PolicyRevision: envelope.PolicyRevision,
		SourcePolicyRevision: "source-policy.v1", PreviousCursor: "cursor-old",
		PreviousItemHashes: map[string]string{},
		Request: c.Request{
			SchemaVersion: c.RequestSchema, Mode: "refresh", WatchID: watchID, Objective: "Refresh registered source",
			Query: "https://example.org/source", SourceKinds: []string{"web"}, CostProfile: "zero_incremental_cost",
			Outputs: c.Outputs{Save: []c.SaveTarget{{Target: KnowledgeStagingTarget, Required: true}}},
			Limits:  c.Limits{SearchCalls: 20, Candidates: 100, FetchAttempts: 100, MaxDocuments: 50, DeadlineSeconds: 600},
		},
		Sources: []c.RefreshSource{{SourceRef: "source:registered", SeedURL: "https://example.org/source", SourceKind: "web"}},
	}
	plan.PlanDigest = engine.RefreshPlanDigest(plan)
	fetch := &fetchStub{err: errors.New("test acquisition stop after observing refresh flag")}
	adapter := NewAdapters(nil, fetch, nil)
	refresh := &adapterRefreshStub{plan: plan}
	service := &engine.Engine{Ports: engine.Ports{
		Store: newResearchEngineStore(), Acquisition: adapter, Owner: adapter, Refresh: refresh,
		Capabilities: []c.Capability{{ProviderID: "direct", SourceKinds: []string{"web"}, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true}},
	}}
	_, _ = service.Run(context.Background(), envelope, c.Request{SchemaVersion: c.RequestSchema, Mode: "refresh", WatchID: watchID})
	if !fetch.request.Refresh {
		t.Fatal("scheduled refresh acquisition did not bypass WebGather cache")
	}
}

// A later run may see a preview-only WebGather cache entry. It must reuse
// the exact scope-owned staging body, not fail or fetch the same body again.
func TestResearchCacheBodyReuseAcrossRuns(t *testing.T) {
	root := t.TempDir()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	artifactStore, err := engine.NewFileStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer artifactStore.Close()
	fetcher := &researchCachedFetcher{}
	adapter := NewAdapters(nil, fetcher, store).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
	service := &engine.Engine{Ports: engine.Ports{
		Store: artifactStore, Acquisition: adapter, Owner: adapter,
		Capabilities: []c.Capability{{ProviderID: "direct", SourceKinds: []string{"web"}, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true}},
	}}
	input := c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: "Collect public source", SeedURLs: []string{"https://example.org/document"}}
	envelope := c.Envelope{TaskID: "tsk_11111111-1111-7111-8111-111111111111", RunID: "run_11111111-1111-7111-8111-111111111111", TraceID: "trc_11111111-1111-7111-8111-111111111111", RequestID: "req_11111111-1111-7111-8111-111111111111", Actor: "ren", OwnerID: "ren", DataScope: "user", PolicyRevision: "test"}
	first, err := service.Run(context.Background(), envelope, input)
	if err != nil || first.Status != "succeeded" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	envelope.RunID = "run_22222222-2222-7222-8222-222222222222"
	second, err := service.Run(context.Background(), envelope, input)
	if err != nil || second.Status != "succeeded" {
		t.Fatalf("cached source could not be used: %+v err=%v", second, err)
	}
	if fetcher.refreshes != 0 {
		t.Fatal("scope-owned complete source was unnecessarily fetched again")
	}
	envelope.RunID = "run_33333333-3333-7333-8333-333333333333"
	envelope.OwnerID = "another-owner"
	third, err := service.Run(context.Background(), envelope, input)
	if err != nil || third.Status != "succeeded" {
		t.Fatalf("different owner could not refresh its own source: %+v err=%v", third, err)
	}
	if fetcher.refreshes != 1 {
		t.Fatalf("different owner reused another scope's staged text: refreshes=%d", fetcher.refreshes)
	}
}

type researchCachedFetcher struct{ calls, refreshes int }

func (f *researchCachedFetcher) FetchURL(_ context.Context, r modulewebgather.FetchRequest) (modulewebgather.FetchResponse, error) {
	f.calls++
	if r.Refresh {
		f.refreshes++
	}
	text := "Complete public source text, including its final paragraph."
	response := modulewebgather.FetchResponse{URL: r.URL, FinalURL: r.URL, Status: "ok", HTTPStatus: 200, RawHash: "sha256:" + digestBytes([]byte(text)), ExtractedChars: len([]rune(text)), ExtractedText: text, ContentType: "text/plain"}
	if f.calls > 1 && !r.Refresh {
		response.ExtractedText = ""
		response.TextPreview = "Complete public source"
		response.Diagnostics = map[string]any{"cache_hit": true}
	}
	return response, nil
}
