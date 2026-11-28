package research

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type refreshTestFixture struct {
	store     *l1sqlite.L1SQLiteStore
	artifacts *engine.FileStore
	owner     *Adapters
	runner    *engine.Engine
	fetch     *fetchStub
	search    *searchStub
	watchID   string
	env       c.Envelope
	now       time.Time
}

type refreshDueWatchStore struct {
	*l1sqlite.L1SQLiteStore
	dueAt time.Time
}

type refreshOutcomeProbe struct {
	engine.RefreshPort
	finishErr     error
	revalidateErr error
}

func (p *refreshOutcomeProbe) Finish(ctx context.Context, env c.Envelope, plan c.RefreshPlan, execution c.RefreshExecution) (c.RefreshReceipt, error) {
	receipt, err := p.RefreshPort.Finish(ctx, env, plan, execution)
	p.finishErr = err
	return receipt, err
}

func (p *refreshOutcomeProbe) Revalidate(ctx context.Context, env c.Envelope, plan c.RefreshPlan) (c.RefreshPlan, error) {
	receipt, err := p.RefreshPort.(engine.RefreshResumePort).Revalidate(ctx, env, plan)
	p.revalidateErr = err
	return receipt, err
}

func (s *refreshDueWatchStore) RegisterResearchWatch(ctx context.Context, scope, ownerID, payload string, refs []string, cadence time.Duration, _ time.Time) (*l1sqlite.L1ResearchWatch, error) {
	return s.L1SQLiteStore.RegisterResearchWatch(ctx, scope, ownerID, payload, refs, cadence, s.dueAt)
}

func newRefreshTestFixture(t *testing.T) *refreshTestFixture {
	return newRefreshTestFixtureKind(t, false)
}

func newRefreshTestFixtureKind(t *testing.T, rss bool) *refreshTestFixture {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(root, "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	artifacts, err := engine.NewFileStore(filepath.Join(root, "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = artifacts.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	text := "A registered public research source."
	fetch := &fetchStub{result: modulewebgather.FetchResponse{URL: "https://example.org/research", FinalURL: "https://example.org/research", Status: "ok", ContentType: "text/plain", ExtractedText: text, ExtractedChars: len([]rune(text)), RawHash: modulewebgather.SHA256Text(text)}}
	search := &searchStub{result: modulewebgather.SearchResponse{Provider: "rss_atom", Results: []modulewebgather.SearchResult{}}}
	policy := func(context.Context, c.Candidate) (bool, bool) { return true, true }
	owner := NewAdapters(search, fetch, store).WithSourcePolicy(policy)
	sourceKind := l1sqlite.L1SourceKindWebGather
	sourceKindWire := "web"
	provider := "direct"
	if rss {
		sourceKind = l1sqlite.L1SourceKindRSS
		sourceKindWire = "rss"
		provider = "rss_atom"
	}
	if _, err := store.SaveSourceRegistryEntry(ctx, l1sqlite.L1SourceRegistryEntry{SourceID: "web:research", URL: "https://example.org/research", Kind: sourceKind, Enabled: true, TrustScore: 1, FetchInterval: time.Minute, LicenseNote: "public retention authorized", Meta: map[string]interface{}{"namespace": "web_gather"}}); err != nil {
		t.Fatal(err)
	}
	env := c.Envelope{TaskID: string(core.NewTaskID()), RunID: string(core.NewRunID()), TraceID: string(core.NewTraceID()), RequestID: string(core.NewRequestID()), Actor: "ren", OwnerID: "ren", DataScope: "user", PolicyRevision: "core.research.test.v1"}
	dueStore := &refreshDueWatchStore{L1SQLiteStore: store, dueAt: now.Add(-time.Second)}
	watch, err := NewWatchAdapter(dueStore, policy).Register(ctx, env, c.WatchRequest{Objective: "Track the registered public source", SourceRefs: []string{"web:research"}, SourceKinds: []string{sourceKindWire}, Cadence: "1m", Outputs: c.Outputs{Save: []c.SaveTarget{{Target: KnowledgeStagingTarget, Required: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &refreshTestFixture{store: store, artifacts: artifacts, owner: owner, fetch: fetch, search: search, watchID: watch.WatchID, env: env, now: now}
	refresh := NewRefreshAdapter(dueStore, policy, artifacts, owner).WithClock(func() time.Time { return fixture.now })
	capability := c.Capability{ProviderID: provider, SourceKinds: []string{sourceKindWire}, FetchText: true, Enabled: true, Ready: true, FreeAllowed: true, Search: rss}
	runner := &engine.Engine{Ports: engine.Ports{Store: artifacts, Owner: owner, Acquisition: owner, Discovery: owner, Refresh: refresh, Capabilities: []c.Capability{capability}}}
	runner.Ports.Refresh = &refreshOutcomeProbe{RefreshPort: refresh}
	fixture.runner = runner
	return fixture
}

func (f *refreshTestFixture) run(t *testing.T, env c.Envelope) c.Result {
	t.Helper()
	result, err := f.runner.Run(context.Background(), env, c.Request{SchemaVersion: c.RequestSchema, Mode: "refresh", WatchID: f.watchID})
	if err != nil {
		t.Fatalf("refresh run: %v", err)
	}
	return result
}

func (f *refreshTestFixture) adapter() *RefreshAdapter {
	return f.runner.Ports.Refresh.(*refreshOutcomeProbe).RefreshPort.(*RefreshAdapter)
}

func TestRefreshAdapterPrepareDiagnostics(t *testing.T) {
	f := newRefreshTestFixture(t)
	adapter := f.adapter()
	if _, err := adapter.Prepare(context.Background(), f.env, f.watchID); err != nil {
		t.Fatal(err)
	}
}

func TestRefreshAdapterCommitsExactEngineCollectionAndReconcilesLostReceipt(t *testing.T) {
	f := newRefreshTestFixture(t)
	result := f.run(t, f.env)
	if result.Status != "succeeded" || result.Counts.RefreshItemsNew != 1 || result.Counts.RecordsSaved != 1 || result.RefreshReceipt == nil || !result.RefreshReceipt.CursorCommitted {
		record, _ := f.artifacts.Load(context.Background(), result.RunID)
		var finishErr error
		if record.RefreshPlan != nil && record.RefreshExecution != nil {
			_, finishErr = f.adapter().Finish(context.Background(), f.env, *record.RefreshPlan, *record.RefreshExecution)
		}
		probe := f.runner.Ports.Refresh.(*refreshOutcomeProbe)
		t.Fatalf("first refresh did not commit verified evidence: %+v finish_error=%v engine_finish_error=%v", result, finishErr, probe.finishErr)
	}
	record, err := f.artifacts.Load(context.Background(), result.RunID)
	if err != nil || record.RefreshPlan == nil || record.RefreshExecution == nil || record.RefreshReceipt == nil {
		t.Fatalf("missing durable engine refresh journal: %+v err=%v", record, err)
	}
	adapter := f.adapter()
	reconciled, err := adapter.Finish(context.Background(), f.env, *record.RefreshPlan, *record.RefreshExecution)
	if err != nil || reconciled.Receipt.Digest != result.RefreshReceipt.Receipt.Digest || reconciled.Cursor != result.RefreshReceipt.Cursor {
		t.Fatalf("lost finish response was not reconciled exactly: %+v err=%v", reconciled, err)
	}
	watch, found, err := f.store.GetResearchWatch(context.Background(), "user", "ren", f.watchID)
	if err != nil || !found || watch.CursorJSON != reconciled.Cursor || watch.LeaseToken != "" || watch.LastSuccessAt.IsZero() {
		t.Fatalf("cursor checkpoint did not persist: %+v found=%v err=%v", watch, found, err)
	}
}

func TestRefreshAdapterFailureKeepsLiteralCursorAndRevalidatesSamePlanAtRetryDue(t *testing.T) {
	f := newRefreshTestFixture(t)
	f.fetch.err = errors.New("fixture source unavailable")
	initial := f.run(t, f.env)
	if initial.Status != "failed" && initial.Status != "partial" || initial.RefreshReceipt == nil || initial.RefreshReceipt.State != "retry_scheduled" || initial.RefreshReceipt.CursorCommitted {
		t.Fatalf("source failure was counted as success: %+v", initial)
	}
	watch, found, err := f.store.GetResearchWatch(context.Background(), "user", "ren", f.watchID)
	if err != nil || !found || watch.CursorJSON != initial.RefreshReceipt.Cursor || watch.LeaseToken != "" || !watch.NextDueAt.Equal(initial.RefreshReceipt.RetryAt) {
		t.Fatalf("failure changed cursor or failed to release lease: %+v err=%v", watch, err)
	}
	plan, found, err := f.store.GetResearchWatchRefresh(context.Background(), "user", "ren", f.watchID)
	if err != nil || !found || plan.State != "retry_scheduled" || plan.AttemptRef != initial.RefreshReceipt.AttemptRef || plan.LeaseToken == "" {
		t.Fatalf("retry journal missing exact plan: %+v err=%v", plan, err)
	}
	f.fetch.err = nil
	f.now = initial.RefreshReceipt.RetryAt.Add(time.Second)
	continued := f.env
	continued.RunID, continued.TraceID, continued.RequestID = string(core.NewRunID()), string(core.NewTraceID()), string(core.NewRequestID())
	result, err := f.runner.Resume(context.Background(), continued, initial.RunID)
	if err != nil || result.Status != "succeeded" || result.RunID != initial.RunID || result.ExecutionRunID != continued.RunID || result.Counts.RefreshItemsNew != 1 {
		currentWatch, _, _ := f.store.GetResearchWatch(context.Background(), "user", "ren", f.watchID)
		currentPlan, _, _ := f.store.GetResearchWatchRefresh(context.Background(), "user", "ren", f.watchID)
		t.Fatalf("same-task explicit retry failed: result=%+v err=%v revalidate_error=%v now=%s next_due=%s lease_until=%s journal_state=%s retry_at=%s", result, err, f.runner.Ports.Refresh.(*refreshOutcomeProbe).revalidateErr, f.now, currentWatch.NextDueAt, currentWatch.LeaseUntil, currentPlan.State, currentPlan.RetryAt)
	}
	updated, found, err := f.store.GetResearchWatchRefresh(context.Background(), "user", "ren", f.watchID)
	if err != nil || !found || updated.State != "committed" || updated.PlanDigest != plan.PlanDigest || updated.LeaseToken != plan.LeaseToken {
		t.Fatalf("retry changed the frozen plan or token: %+v err=%v", updated, err)
	}
}

func TestRefreshAdapterRejectsForeignResumeIdentityAndUnavailableTarget(t *testing.T) {
	f := newRefreshTestFixture(t)
	f.fetch.err = errors.New("fixture source unavailable")
	result := f.run(t, f.env)
	if result.RefreshReceipt == nil {
		t.Fatalf("missing retry receipt: %+v", result)
	}
	record, err := f.artifacts.Load(context.Background(), result.RunID)
	if err != nil || record.RefreshPlan == nil {
		t.Fatal(err)
	}
	adapter := f.adapter()
	foreign := f.env
	foreign.TaskID = string(core.NewTaskID())
	foreign.RunID, foreign.TraceID, foreign.RequestID = string(core.NewRunID()), string(core.NewTraceID()), string(core.NewRequestID())
	if _, err := adapter.Revalidate(context.Background(), foreign, *record.RefreshPlan); err == nil {
		t.Fatal("foreign Task was allowed to resume the watch")
	}
	if _, err := adapter.Prepare(context.Background(), f.env, "rw_invalid trailing"); err == nil {
		t.Fatal("invalid watch ID was claimed")
	}
}

func TestRefreshAdapterRejectsUnconnectedRegisteredOutput(t *testing.T) {
	f := newRefreshTestFixture(t)
	request := c.WatchRequest{Objective: "Track the registered public source", SourceRefs: []string{"web:research"}, SourceKinds: []string{"web"}, Cadence: "1m", Outputs: c.Outputs{Save: []c.SaveTarget{{Target: "board", Required: true, DestinationRef: "unconnected"}}}}
	payload, err := canonicalRefreshTestJSON(request)
	if err != nil {
		t.Fatal(err)
	}
	watch, err := f.store.RegisterResearchWatch(context.Background(), "user", "ren", string(payload), request.SourceRefs, time.Minute, f.now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	f.watchID = watch.WatchID
	adapter := f.adapter()
	if _, err := adapter.Prepare(context.Background(), f.env, f.watchID); err == nil {
		t.Fatal("unconnected board target was accepted for refresh")
	}
	stored, found, err := f.store.GetResearchWatch(context.Background(), "user", "ren", f.watchID)
	if err != nil || !found || stored.LeaseToken != "" || stored.CursorJSON != watch.CursorJSON {
		t.Fatalf("rejected plan did not release its exact lease without moving the cursor: %+v err=%v", stored, err)
	}
}

func TestRefreshAdapterZeroResultFromFeedIsSuccessful(t *testing.T) {
	f := newRefreshTestFixtureKind(t, true)
	result := f.run(t, f.env)
	if result.Status != "succeeded" || !result.Empty || result.Counts.DocumentsFetched != 0 || !result.RefreshReceipt.CursorCommitted || f.search.request.Query != "https://example.org/research" {
		t.Fatalf("zero-result refresh was not completed: %+v", result)
	}
}

func canonicalRefreshTestJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}
