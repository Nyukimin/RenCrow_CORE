package research

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

type recallSearchStub struct {
	request appkm.SearchRequest
	results []appkm.SearchResult
	err     error
}

func (s *recallSearchStub) Search(_ context.Context, request appkm.SearchRequest) ([]appkm.SearchResult, error) {
	s.request = request
	return s.results, s.err
}

func TestRecallMapsScopedKnowledgeCandidatesAndUnsupportedTargets(t *testing.T) {
	searcher := &recallSearchStub{results: []appkm.SearchResult{{
		RecordType: "news_knowledge", RecordID: "news-7", Scope: "user", UserID: "owner-1",
		Title: "A source", Summary: "Indexed summary", Visibility: "private",
		SourceUpdatedAt: "2026-11-27T10:00:00Z", IndexedAt: "2026-11-27T11:00:00Z",
		ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}}
	adapter := NewRecallAdapter(searcher, func(_ context.Context, env c.Envelope) bool { return env.OwnerID == "owner-1" })
	window := &c.TimeWindow{Since: time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)}
	got, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "user", OwnerID: "owner-1"}, c.RecallRequest{
		Query: "source", Targets: []string{"knowledge", "documents", "code"}, TimeWindow: window,
	})
	if err != nil {
		t.Fatal(err)
	}
	if searcher.request.Scope != (appkm.SearchScope{Scope: "user", UserID: "owner-1"}) || searcher.request.Query != "source" || searcher.request.Limit != 8 {
		t.Fatalf("owner search request lost scope/query/default limit: %+v", searcher.request)
	}
	if len(got.Hits) != 1 || got.Hits[0].EvidenceState != "candidate" || got.Hits[0].Target != "knowledge" || got.Hits[0].CanonicalRef == "" {
		t.Fatalf("indexed hit must remain an unverified candidate: %+v", got)
	}
	if !reflect.DeepEqual(got.Missing, []string{"documents", "code"}) {
		t.Fatalf("unsupported targets must stay missing: %+v", got.Missing)
	}
}

func TestRecallDistinguishesEmptySearchFromStoppedOwner(t *testing.T) {
	env := c.Envelope{DataScope: "public"}
	empty := NewRecallAdapter(&recallSearchStub{}, func(context.Context, c.Envelope) bool { return true })
	got, err := empty.Recall(context.Background(), env, c.RecallRequest{Query: "none", Targets: []string{"knowledge"}, Limit: 20})
	if err != nil || len(got.Hits) != 0 || len(got.Missing) != 0 {
		t.Fatalf("zero search matches are a normal empty result: got=%+v err=%v", got, err)
	}
	stopped := NewRecallAdapter(&recallSearchStub{err: errors.New("offline")}, func(context.Context, c.Envelope) bool { return true })
	got, err = stopped.Recall(context.Background(), env, c.RecallRequest{Query: "topic", Targets: []string{"knowledge"}})
	if err == nil || !reflect.DeepEqual(got.Missing, []string{"knowledge"}) {
		t.Fatalf("owner outage must be distinguishable from empty results: got=%+v err=%v", got, err)
	}
}

func TestRecallReadinessScopeAndTimeWindowAreEnforced(t *testing.T) {
	searcher := &recallSearchStub{results: []appkm.SearchResult{{
		RecordType: "creative_knowledge", RecordID: "creative-1", Scope: "public", Visibility: "public",
		SourceUpdatedAt: "2026-01-01T00:00:00Z", IndexedAt: "2026-01-02T00:00:00Z",
		ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}}}
	adapter := NewRecallAdapter(searcher, func(_ context.Context, env c.Envelope) bool { return env.DataScope == "public" })
	private, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "user", OwnerID: "owner-1"}, c.RecallRequest{Query: "topic", Targets: []string{"knowledge"}})
	if err != nil || len(private.Hits) != 0 || !reflect.DeepEqual(private.Missing, []string{"knowledge"}) || searcher.request.Query != "" {
		t.Fatalf("unready private search must not reach owner: result=%+v request=%+v err=%v", private, searcher.request, err)
	}
	window := &c.TimeWindow{Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)}
	public, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: "topic", Targets: []string{"knowledge"}, TimeWindow: window})
	if err != nil || len(public.Hits) != 0 || len(public.Missing) != 0 {
		t.Fatalf("out-of-window indexed result must be filtered: result=%+v err=%v", public, err)
	}
}

func TestRecallTimeWindowUsesInclusiveSinceExclusiveUntil(t *testing.T) {
	searcher := &recallSearchStub{results: []appkm.SearchResult{
		{RecordType: "news_knowledge", RecordID: "since", Scope: "public", Title: "Since", Visibility: "public", SourceUpdatedAt: "2026-11-01T00:00:00Z", IndexedAt: "2026-11-02T00:00:00Z", ContentSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{RecordType: "news_knowledge", RecordID: "until", Scope: "public", Title: "Until", Visibility: "public", SourceUpdatedAt: "2026-11-30T00:00:00Z", IndexedAt: "2026-11-30T00:00:00Z", ContentSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}}
	adapter := NewRecallAdapter(searcher, func(context.Context, c.Envelope) bool { return true })
	window := &c.TimeWindow{Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Until: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)}
	got, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: "topic", Targets: []string{"knowledge"}, TimeWindow: window})
	if err != nil || len(got.Hits) != 1 || got.Hits[0].Locator != "since" || len(got.Missing) != 0 {
		t.Fatalf("time window must include since and exclude until: result=%+v err=%v", got, err)
	}
	window.Until = window.Since
	if _, err := adapter.Recall(context.Background(), c.Envelope{DataScope: "public"}, c.RecallRequest{Query: "topic", Targets: []string{"knowledge"}, TimeWindow: window}); err == nil {
		t.Fatal("empty time window must be rejected")
	}
}

func TestRecallRejectsInvalidQueryScopeAndLimit(t *testing.T) {
	searcher := &recallSearchStub{}
	adapter := NewRecallAdapter(searcher, func(context.Context, c.Envelope) bool { return true })
	for _, tc := range []struct {
		name string
		env  c.Envelope
		req  c.RecallRequest
	}{
		{name: "empty query", env: c.Envelope{DataScope: "public"}, req: c.RecallRequest{Targets: []string{"knowledge"}}},
		{name: "bad user scope", env: c.Envelope{DataScope: "user"}, req: c.RecallRequest{Query: "x", Targets: []string{"knowledge"}}},
		{name: "limit too high", env: c.Envelope{DataScope: "public"}, req: c.RecallRequest{Query: "x", Targets: []string{"knowledge"}, Limit: 21}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := adapter.Recall(context.Background(), tc.env, tc.req); err == nil {
				t.Fatal("expected invalid recall request to fail")
			}
		})
	}
}
