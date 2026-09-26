package llmops

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llmfit"
)

type fakeClock struct{ t time.Time }

func ptrFloat(v float64) *float64 { return &v }

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time               { return c.t }
func (c *fakeClock) Advance(d time.Duration)      { c.t = c.t.Add(d) }
func (c *fakeClock) At(d time.Duration) time.Time { return c.t.Add(d) }

func testProfile(name string) *domainllmops.NodeHardwareProfile {
	return &domainllmops.NodeHardwareProfile{
		NodeName: name, OS: "linux", CPUName: "cpu", CPUCores: 8, TotalRAMGB: 64, AvailableRAMGB: 32,
		HasGPU: true, GPUCount: 1, GPUs: []domainllmops.GPUProfile{{Name: "gpu", VRAMGB: 16, AvailableVRAMGB: ptrFloat(15)}},
		Backend: "vulkan", Source: domainllmops.SourceLLMFit,
	}
}

func testModel(id string, fit domainllmops.FitLevel) domainllmops.ModelFitAssessment {
	tps := 40.0
	return domainllmops.ModelFitAssessment{ModelID: id, Provider: "p", FitLevel: fit, Score: 80, Runtime: "llamacpp", UsableContext: 32768, EstimatedTPS: &tps, EstimateConfidence: domainllmops.EstimateConfidenceEstimated}
}

func defaultOptions(clock *fakeClock) Options {
	return Options{SystemTTL: 5 * time.Minute, ModelsTTL: 15 * time.Minute, HealthTTL: 30 * time.Second, TopLimit: 20, Now: clock.Now}
}

func TestNodeUsesCacheWithinTTLAndRefetchesAfterExpiry(t *testing.T) {
	clock := newFakeClock()
	mock := &llmfit.MockProvider{Profile: testProfile("a")}
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: mock}}, defaultOptions(clock))
	ctx := context.Background()

	first, ok := svc.Node(ctx, "node-a")
	if !ok || first.Status != StatusOnline || first.Profile == nil || first.Error != "" {
		t.Fatalf("first snapshot: ok=%v %+v", ok, first)
	}
	if first.CollectedAt == nil || !first.CollectedAt.Equal(clock.Now()) || !first.Profile.CollectedAt.Equal(clock.Now()) {
		t.Fatalf("collected_at must be stamped with the service clock: %+v", first)
	}
	if first.Profile.NodeID != "node-a" {
		t.Fatalf("profile node id must be the configured id, got %q", first.Profile.NodeID)
	}

	clock.Advance(10 * time.Second)
	second, _ := svc.Node(ctx, "node-a")
	if second.Status != StatusOnline {
		t.Fatalf("second status=%s", second.Status)
	}
	if _, system, _, _ := mock.Calls(); system != 1 {
		t.Fatalf("cache hit expected, system calls=%d", system)
	}
	if health, _, _, _ := mock.Calls(); health != 0 {
		t.Fatalf("health is implied by a fresh fetch within health ttl, calls=%d", health)
	}

	clock.Advance(25 * time.Second) // 35s since fetch: health ttl expired, system ttl fresh
	third, _ := svc.Node(ctx, "node-a")
	if third.Status != StatusOnline {
		t.Fatalf("third status=%s", third.Status)
	}
	if health, system, _, _ := mock.Calls(); health != 1 || system != 1 {
		t.Fatalf("expected health re-check only, health=%d system=%d", health, system)
	}

	clock.Advance(5 * time.Minute)
	fourth, _ := svc.Node(ctx, "node-a")
	if _, system, _, _ := mock.Calls(); system != 2 {
		t.Fatalf("system ttl expiry must refetch, calls=%d", system)
	}
	if fourth.CollectedAt == nil || !fourth.CollectedAt.Equal(clock.Now()) {
		t.Fatalf("refetched collected_at must advance: %+v", fourth)
	}
}

func TestNodeReportsStaleWhenHealthFailsInsideSystemTTL(t *testing.T) {
	clock := newFakeClock()
	mock := &llmfit.MockProvider{Profile: testProfile("a")}
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: mock}}, defaultOptions(clock))
	ctx := context.Background()

	if snap, _ := svc.Node(ctx, "node-a"); snap.Status != StatusOnline {
		t.Fatalf("status=%s", snap.Status)
	}
	fetchedAt := clock.Now()
	mock.Set(func(m *llmfit.MockProvider) { m.HealthErr = errors.New("connection refused") })
	clock.Advance(time.Minute)

	snap, _ := svc.Node(ctx, "node-a")
	if snap.Status != StatusStale || snap.Profile == nil || !strings.Contains(snap.Error, "connection refused") {
		t.Fatalf("expected stale with retained profile: %+v", snap)
	}
	if snap.CollectedAt == nil || !snap.CollectedAt.Equal(fetchedAt) {
		t.Fatalf("stale collected_at must be the last successful fetch: %+v", snap)
	}
}

func TestNodeReportsStaleAfterFetchFailureAndOfflineWithoutHistory(t *testing.T) {
	clock := newFakeClock()
	mock := &llmfit.MockProvider{SystemErr: errors.New("dial tcp: connection refused")}
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: mock}}, defaultOptions(clock))
	ctx := context.Background()

	offline, _ := svc.Node(ctx, "node-a")
	if offline.Status != StatusOffline || offline.Profile != nil || offline.CollectedAt != nil || !strings.Contains(offline.Error, "connection refused") {
		t.Fatalf("expected offline without history: %+v", offline)
	}

	mock.Set(func(m *llmfit.MockProvider) { m.SystemErr = nil; m.Profile = testProfile("a") })
	online, _ := svc.Node(ctx, "node-a")
	if online.Status != StatusOnline {
		t.Fatalf("recovered status=%s err=%s", online.Status, online.Error)
	}
	fetchedAt := clock.Now()

	mock.Set(func(m *llmfit.MockProvider) { m.SystemErr = errors.New("timeout") })
	clock.Advance(6 * time.Minute)
	stale, _ := svc.Node(ctx, "node-a")
	if stale.Status != StatusStale || stale.Profile == nil || stale.Profile.NodeName != "a" || stale.Error != "timeout" {
		t.Fatalf("expected stale with last value: %+v", stale)
	}
	if stale.CollectedAt == nil || !stale.CollectedAt.Equal(fetchedAt) {
		t.Fatalf("stale collected_at mismatch: %+v", stale)
	}
}

func TestNodesIsolatesFailuresPerNode(t *testing.T) {
	clock := newFakeClock()
	good := &llmfit.MockProvider{Profile: testProfile("a")}
	bad := &llmfit.MockProvider{SystemErr: errors.New("boom")}
	slowButFine := &llmfit.MockProvider{Profile: testProfile("c")}
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: good}, {ID: "node-b", Provider: bad}, {ID: "node-c", Provider: slowButFine}}, defaultOptions(clock))

	snaps := svc.Nodes(context.Background())
	if len(snaps) != 3 || snaps[0].NodeID != "node-a" || snaps[1].NodeID != "node-b" || snaps[2].NodeID != "node-c" {
		t.Fatalf("nodes must keep configuration order: %+v", snaps)
	}
	if snaps[0].Status != StatusOnline || snaps[2].Status != StatusOnline {
		t.Fatalf("healthy nodes must be online: %+v", snaps)
	}
	if snaps[1].Status != StatusOffline || snaps[1].Error != "boom" || snaps[1].Profile != nil {
		t.Fatalf("failing node must be offline with reason: %+v", snaps[1])
	}
}

func TestNodeModelsCachesPerQueryAndAppliesTopLimit(t *testing.T) {
	clock := newFakeClock()
	mock := &llmfit.MockProvider{Profile: testProfile("a"), Models: []domainllmops.ModelFitAssessment{testModel("m1", domainllmops.FitLevelGood)}}
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: mock}}, defaultOptions(clock))
	ctx := context.Background()

	snap, ok := svc.NodeModels(ctx, "node-a", ModelsQuery{})
	if !ok || snap.Status != StatusOnline || len(snap.Models) != 1 || snap.Error != "" {
		t.Fatalf("models snapshot: ok=%v %+v", ok, snap)
	}
	if snap.Models[0].NodeID != "node-a" || !snap.Models[0].CollectedAt.Equal(clock.Now()) {
		t.Fatalf("models must carry node id and service clock: %+v", snap.Models[0])
	}
	if mock.LastQuery.Limit != 20 {
		t.Fatalf("default limit must be the configured top limit, got %d", mock.LastQuery.Limit)
	}

	if _, _ = svc.NodeModels(ctx, "node-a", ModelsQuery{}); mock.TopCalls != 1 {
		t.Fatalf("same query must hit cache, calls=%d", mock.TopCalls)
	}
	if _, _ = svc.NodeModels(ctx, "node-a", ModelsQuery{UseCase: "coding"}); mock.TopCalls != 2 {
		t.Fatalf("different use_case must be a separate cache entry, calls=%d", mock.TopCalls)
	}
	if _, _ = svc.NodeModels(ctx, "node-a", ModelsQuery{Limit: 5}); mock.TopCalls != 3 {
		t.Fatalf("different limit must be a separate cache entry, calls=%d", mock.TopCalls)
	}
	if mock.LastQuery.Limit != 5 {
		t.Fatalf("explicit limit must be forwarded, got %d", mock.LastQuery.Limit)
	}
	if _, _ = svc.NodeModels(ctx, "node-a", ModelsQuery{MinFit: "marginal", Runtime: "mlx", MaxContext: 8192}); mock.TopCalls != 4 {
		t.Fatalf("min_fit/runtime/max_context must be part of the key, calls=%d", mock.TopCalls)
	}

	clock.Advance(16 * time.Minute)
	if _, _ = svc.NodeModels(ctx, "node-a", ModelsQuery{}); mock.TopCalls != 5 {
		t.Fatalf("models ttl expiry must refetch, calls=%d", mock.TopCalls)
	}
}

func TestNodeModelsStaleAndOffline(t *testing.T) {
	clock := newFakeClock()
	mock := &llmfit.MockProvider{ModelsErr: errors.New("HTTP 500")}
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: mock}}, defaultOptions(clock))
	ctx := context.Background()

	offline, _ := svc.NodeModels(ctx, "node-a", ModelsQuery{})
	if offline.Status != StatusOffline || offline.Models == nil || len(offline.Models) != 0 || offline.Error != "HTTP 500" || offline.CollectedAt != nil {
		t.Fatalf("expected offline with empty non-nil models: %+v", offline)
	}

	mock.Set(func(m *llmfit.MockProvider) {
		m.ModelsErr = nil
		m.Models = []domainllmops.ModelFitAssessment{testModel("m1", domainllmops.FitLevelGood)}
	})
	if snap, _ := svc.NodeModels(ctx, "node-a", ModelsQuery{}); snap.Status != StatusOnline || len(snap.Models) != 1 {
		t.Fatalf("recovered: %+v", snap)
	}
	fetchedAt := clock.Now()

	mock.Set(func(m *llmfit.MockProvider) { m.ModelsErr = errors.New("HTTP 500") })
	clock.Advance(20 * time.Minute)
	stale, _ := svc.NodeModels(ctx, "node-a", ModelsQuery{})
	if stale.Status != StatusStale || len(stale.Models) != 1 || stale.Error != "HTTP 500" || stale.CollectedAt == nil || !stale.CollectedAt.Equal(fetchedAt) {
		t.Fatalf("expected stale with retained models: %+v", stale)
	}
}

func TestNodeModelsRejectsInvalidQueryWithoutProviderCall(t *testing.T) {
	clock := newFakeClock()
	mock := &llmfit.MockProvider{Models: []domainllmops.ModelFitAssessment{testModel("m1", domainllmops.FitLevelGood)}}
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: mock}}, defaultOptions(clock))
	_, err := ValidateModelsQuery(ModelsQuery{MinFit: "bogus"})
	if err == nil {
		t.Fatal("invalid min_fit must be rejected")
	}
	if _, ok := svc.NodeModels(context.Background(), "missing", ModelsQuery{}); ok {
		t.Fatal("unknown node must report ok=false")
	}
	if _, ok := svc.Node(context.Background(), "missing"); ok {
		t.Fatal("unknown node must report ok=false")
	}
	if mock.TopCalls != 0 || mock.SystemCalls != 0 {
		t.Fatalf("unknown node must not call any provider: %+v", mock)
	}
}

func TestModelMatrixSearchesEachNode(t *testing.T) {
	clock := newFakeClock()
	found := &llmfit.MockProvider{SearchResults: []domainllmops.ModelFitAssessment{testModel("other", domainllmops.FitLevelGood), testModel("Qwen3-Coder-30B", domainllmops.FitLevelMarginal)}}
	missing := &llmfit.MockProvider{SearchResults: []domainllmops.ModelFitAssessment{testModel("other", domainllmops.FitLevelGood)}}
	broken := &llmfit.MockProvider{SearchErr: errors.New("connection refused")}
	svc := NewCapabilityService([]Node{{ID: "a", Provider: found}, {ID: "b", Provider: missing}, {ID: "c", Provider: broken}}, defaultOptions(clock))

	entries := svc.ModelMatrix(context.Background(), "qwen3-coder-30b")
	if len(entries) != 3 {
		t.Fatalf("entries=%d", len(entries))
	}
	if entries[0].NodeID != "a" || entries[0].Status != StatusOnline || entries[0].Assessment == nil || entries[0].Assessment.FitLevel != domainllmops.FitLevelMarginal || entries[0].Assessment.NodeID != "a" {
		t.Fatalf("node a must resolve the model case-insensitively: %+v", entries[0])
	}
	if entries[1].NodeID != "b" || entries[1].Status != StatusOnline || entries[1].Assessment != nil {
		t.Fatalf("node b must be online without assessment: %+v", entries[1])
	}
	if entries[2].NodeID != "c" || entries[2].Status != StatusOffline || entries[2].Assessment != nil || entries[2].Error != "connection refused" {
		t.Fatalf("node c must be offline: %+v", entries[2])
	}
	if found.LastQuery.Keyword != "qwen3-coder-30b" || !found.LastQuery.IncludeTooTight {
		t.Fatalf("matrix search must pass keyword and include too_tight: %+v", found.LastQuery)
	}
	if _ = svc.ModelMatrix(context.Background(), "qwen3-coder-30b"); found.SearchCalls != 1 {
		t.Fatalf("matrix lookups must be cached, calls=%d", found.SearchCalls)
	}
}

func TestRefreshForcesRefetchAndKeepsPreviousOnFailure(t *testing.T) {
	clock := newFakeClock()
	good := &llmfit.MockProvider{Profile: testProfile("a"), Models: []domainllmops.ModelFitAssessment{testModel("m1", domainllmops.FitLevelGood)}}
	bad := &llmfit.MockProvider{Profile: testProfile("b"), Models: []domainllmops.ModelFitAssessment{testModel("m2", domainllmops.FitLevelGood)}}
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: good}, {ID: "node-b", Provider: bad}}, defaultOptions(clock))
	ctx := context.Background()

	svc.Nodes(ctx)
	svc.NodeModels(ctx, "node-a", ModelsQuery{UseCase: "coding"})
	svc.NodeModels(ctx, "node-b", ModelsQuery{})
	firstFetch := clock.Now()
	clock.Advance(time.Minute)

	bad.Set(func(m *llmfit.MockProvider) { m.SystemErr = errors.New("HTTP 503") })
	result := svc.Refresh(ctx)
	if len(result.Refreshed) != 1 || result.Refreshed[0] != "node-a" {
		t.Fatalf("refreshed=%v", result.Refreshed)
	}
	if result.Failed == nil || result.Failed["node-b"] != "HTTP 503" {
		t.Fatalf("failed=%v", result.Failed)
	}
	if good.SystemCalls != 2 || good.TopCalls != 2 {
		t.Fatalf("refresh must force system+top refetch on node-a: system=%d top=%d", good.SystemCalls, good.TopCalls)
	}

	a, _ := svc.Node(ctx, "node-a")
	if a.Status != StatusOnline || a.CollectedAt == nil || !a.CollectedAt.Equal(clock.Now()) {
		t.Fatalf("node-a must carry the refreshed timestamp: %+v", a)
	}
	b, _ := svc.Node(ctx, "node-b")
	if b.Status != StatusStale || b.Profile == nil || b.CollectedAt == nil || !b.CollectedAt.Equal(firstFetch) || b.Error != "HTTP 503" {
		t.Fatalf("node-b must keep the previous value as stale: %+v", b)
	}
	bModels, _ := svc.NodeModels(ctx, "node-b", ModelsQuery{})
	if bModels.Status != StatusStale || len(bModels.Models) != 1 {
		t.Fatalf("node-b models must be retained as stale: %+v", bModels)
	}

	// Non-default query variants for a refreshed node are dropped so they are re-fetched lazily.
	if _, _ = svc.NodeModels(ctx, "node-a", ModelsQuery{UseCase: "coding"}); good.TopCalls != 3 {
		t.Fatalf("query variant must be refetched after refresh, top calls=%d", good.TopCalls)
	}
	if _, _ = svc.NodeModels(ctx, "node-a", ModelsQuery{}); good.TopCalls != 3 {
		t.Fatalf("default query must be served from the refreshed cache, top calls=%d", good.TopCalls)
	}
}

func TestRefreshTargetsSpecificNodes(t *testing.T) {
	clock := newFakeClock()
	a := &llmfit.MockProvider{Profile: testProfile("a")}
	b := &llmfit.MockProvider{Profile: testProfile("b")}
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: a}, {ID: "node-b", Provider: b}}, defaultOptions(clock))

	result := svc.Refresh(context.Background(), "node-b", "ghost")
	if len(result.Refreshed) != 1 || result.Refreshed[0] != "node-b" {
		t.Fatalf("refreshed=%v", result.Refreshed)
	}
	if result.Failed["ghost"] == "" {
		t.Fatalf("unknown node must be reported in failed: %v", result.Failed)
	}
	if a.SystemCalls != 0 || b.SystemCalls != 1 {
		t.Fatalf("only node-b must be refreshed: a=%d b=%d", a.SystemCalls, b.SystemCalls)
	}
}

func TestDisabledServiceReportsDisabledWithoutCallingProviders(t *testing.T) {
	clock := newFakeClock()
	mock := &llmfit.MockProvider{Profile: testProfile("a")}
	opts := defaultOptions(clock)
	opts.Disabled = true
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: mock}}, opts)
	ctx := context.Background()

	snaps := svc.Nodes(ctx)
	if len(snaps) != 1 || snaps[0].Status != StatusDisabled {
		t.Fatalf("snaps=%+v", snaps)
	}
	if models, ok := svc.NodeModels(ctx, "node-a", ModelsQuery{}); !ok || models.Status != StatusDisabled || models.Models == nil {
		t.Fatalf("models=%+v ok=%v", models, ok)
	}
	if entries := svc.ModelMatrix(ctx, "m"); len(entries) != 1 || entries[0].Status != StatusDisabled {
		t.Fatalf("matrix=%+v", entries)
	}
	if result := svc.Refresh(ctx); len(result.Refreshed) != 0 || result.Failed["node-a"] == "" {
		t.Fatalf("refresh=%+v", result)
	}
	if h, s, top, search := mock.Calls(); h+s+top+search != 0 {
		t.Fatal("disabled service must not call providers")
	}
}

func TestEmptyServiceReturnsEmptyCollections(t *testing.T) {
	svc := NewCapabilityService(nil, defaultOptions(newFakeClock()))
	if nodes := svc.Nodes(context.Background()); nodes == nil || len(nodes) != 0 {
		t.Fatalf("nodes must be an empty non-nil slice: %v", nodes)
	}
	if entries := svc.ModelMatrix(context.Background(), "m"); entries == nil || len(entries) != 0 {
		t.Fatalf("matrix must be an empty non-nil slice: %v", entries)
	}
	if result := svc.Refresh(context.Background()); result.Refreshed == nil || result.Failed == nil {
		t.Fatalf("refresh result must have non-nil collections: %+v", result)
	}
	if ids := svc.NodeIDs(); ids == nil || len(ids) != 0 {
		t.Fatalf("node ids must be an empty non-nil slice: %v", ids)
	}
}

func TestNewCapabilityServiceAppliesDefaultsForZeroOptions(t *testing.T) {
	svc := NewCapabilityService([]Node{{ID: "node-a", Provider: &llmfit.MockProvider{Profile: testProfile("a")}}}, Options{})
	if svc.systemTTL <= 0 || svc.modelsTTL <= 0 || svc.healthTTL <= 0 || svc.topLimit <= 0 || svc.now == nil {
		t.Fatalf("zero options must fall back to defaults: %+v", svc)
	}
	if snap, ok := svc.Node(context.Background(), "node-a"); !ok || snap.Status != StatusOnline {
		t.Fatalf("snap=%+v ok=%v", snap, ok)
	}
}
