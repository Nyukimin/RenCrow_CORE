package research

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
)

type managementWatchStore struct {
	rows         map[string]l1sqlite.L1ResearchWatch
	dueCalls     int
	disableCalls int
	lastScope    string
	lastOwner    string
}

func (s *managementWatchStore) RegisterResearchWatch(context.Context, string, string, string, []string, time.Duration, time.Time) (*l1sqlite.L1ResearchWatch, error) {
	return nil, errors.New("not used")
}

func (s *managementWatchStore) GetResearchWatch(_ context.Context, scope, owner, id string) (l1sqlite.L1ResearchWatch, bool, error) {
	s.lastScope, s.lastOwner = scope, owner
	row, ok := s.rows[id]
	return row, ok && row.Scope == scope && row.OwnerID == owner, nil
}

func (s *managementWatchStore) ListSourceRegistryEntries(context.Context, bool) ([]l1sqlite.L1SourceRegistryEntry, error) {
	return nil, nil
}

func (s *managementWatchStore) DueResearchWatches(_ context.Context, scope, owner string, now time.Time, limit int) ([]l1sqlite.L1ResearchWatch, error) {
	s.dueCalls++
	s.lastScope, s.lastOwner = scope, owner
	var rows []l1sqlite.L1ResearchWatch
	for _, row := range s.rows {
		if row.Scope == scope && row.OwnerID == owner && row.Enabled && !row.NextDueAt.After(now) {
			rows = append(rows, row)
		}
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (s *managementWatchStore) DisableResearchWatch(_ context.Context, scope, owner, id string) (bool, error) {
	s.disableCalls++
	s.lastScope, s.lastOwner = scope, owner
	row, ok := s.rows[id]
	if !ok || row.Scope != scope || row.OwnerID != owner {
		return false, nil
	}
	row.Enabled = false
	row.LeaseToken = ""
	row.LeaseUntil = time.Time{}
	s.rows[id] = row
	return true, nil
}

func TestWatchManagementListDueIsReadonlyAndRedacted(t *testing.T) {
	store := newManagementWatchStore()
	adapter := NewWatchAdapter(store, nil)
	env := managementEnvelope()
	items, err := adapter.ListDue(context.Background(), env, 20)
	if err != nil || len(items) != 1 || items[0].WatchID != "rw_due0001" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if store.dueCalls != 1 || store.disableCalls != 0 || store.lastScope != env.DataScope || store.lastOwner != env.OwnerID {
		t.Fatalf("unexpected owner calls: %+v", store)
	}
	if items[0].Receipt.VerifiedAt.IsZero() || items[0].Receipt.Digest == "" || items[0].Receipt.Scope != env.DataScope {
		t.Fatalf("missing owner verification receipt: %+v", items[0].Receipt)
	}
	data, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"payload_json", "cursor_json", "lease_token", "lease_until", "secret cursor", "private watch payload"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("management response exposed %q: %s", forbidden, data)
		}
	}
}

func TestWatchManagementReadAndDisableAreExactAndVerified(t *testing.T) {
	store := newManagementWatchStore()
	adapter := NewWatchAdapter(store, nil)
	env := managementEnvelope()
	if _, err := adapter.Read(context.Background(), env, "rw_foreign01"); err == nil {
		t.Fatal("foreign watch was readable")
	}
	got, err := adapter.Disable(context.Background(), env, "rw_due0001")
	if err != nil || got.Enabled || got.Receipt.CanonicalID != got.WatchID || got.Receipt.Digest != store.rows[got.WatchID].PayloadDigest || got.Receipt.State != "committed" {
		t.Fatalf("disabled item=%+v err=%v", got, err)
	}
	if store.disableCalls != 1 || store.lastScope != env.DataScope || store.lastOwner != env.OwnerID {
		t.Fatalf("disable not owner-bound: %+v", store)
	}
	again, err := adapter.Disable(context.Background(), env, "rw_due0001")
	if err != nil || again.Enabled {
		t.Fatalf("repeated disable was not idempotently verified: %+v %v", again, err)
	}
	if _, err := adapter.Disable(context.Background(), env, "rw_foreign01"); err == nil {
		t.Fatal("foreign watch was disabled")
	}
	if store.disableCalls != 3 {
		t.Fatalf("expected exact owner attempts, got %d", store.disableCalls)
	}
}

func TestWatchManagementRejectsInvalidLimitIDAndCancelledContext(t *testing.T) {
	adapter := NewWatchAdapter(newManagementWatchStore(), nil)
	env := managementEnvelope()
	if _, err := adapter.ListDue(context.Background(), env, 21); err == nil {
		t.Fatal("oversized due limit accepted")
	}
	if _, err := adapter.Read(context.Background(), env, "rw_../foreign"); err == nil {
		t.Fatal("noncanonical watch id accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.ListDue(ctx, env, 20); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context err=%v", err)
	}
}

func newManagementWatchStore() *managementWatchStore {
	now := time.Now().UTC()
	payload := `{"opaque":"private watch payload"}`
	digest := sha256.Sum256([]byte(payload))
	return &managementWatchStore{rows: map[string]l1sqlite.L1ResearchWatch{
		"rw_due0001": {WatchID: "rw_due0001", OwnerID: "ren", Scope: "user", PayloadJSON: payload, PayloadDigest: hex.EncodeToString(digest[:]),
			CursorJSON: `{"opaque":"secret cursor"}`, Enabled: true, NextDueAt: now.Add(-time.Minute), LeaseToken: "private lease", LeaseUntil: now.Add(time.Hour), CreatedAt: now.Add(-time.Hour)},
		"rw_future1":   {WatchID: "rw_future1", OwnerID: "ren", Scope: "user", PayloadJSON: payload, PayloadDigest: hex.EncodeToString(digest[:]), Enabled: true, NextDueAt: now.Add(time.Hour)},
		"rw_foreign01": {WatchID: "rw_foreign01", OwnerID: "someone-else", Scope: "user", PayloadJSON: payload, PayloadDigest: hex.EncodeToString(digest[:]), Enabled: true, NextDueAt: now.Add(-time.Minute)},
	}}
}

func managementEnvelope() c.Envelope {
	return c.Envelope{TaskID: string(core.NewTaskID()), RunID: string(core.NewRunID()), TraceID: string(core.NewTraceID()), RequestID: string(core.NewRequestID()), Actor: "ren", OwnerID: "ren", DataScope: "user", PolicyRevision: "fixture.v1"}
}
