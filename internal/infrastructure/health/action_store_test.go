package health

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	healthadapter "github.com/Nyukimin/RenCrow_CORE/internal/adapter/health"
	healthapp "github.com/Nyukimin/RenCrow_CORE/internal/application/health"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainhealth "github.com/Nyukimin/RenCrow_CORE/internal/domain/health"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type actionStoreStub struct{ h actionpersistence.StoreHealth }

func (s actionStoreStub) Health() actionpersistence.StoreHealth { return s.h }

func TestActionStoreCheckReportsTheLayoutAndTheSnapshot(t *testing.T) {
	built := time.Date(2026, 10, 14, 0, 3, 0, 0, time.UTC)
	check := NewActionStoreCheck(actionStoreStub{actionpersistence.StoreHealth{
		Indexed: true, Layout: "v2", Segments: 3, CurrentSegment: "20261014-01",
		Segment0ReadOnly: true, Segment0MatchesLayout: true,
		Actions: 58900, Attempts: 59100, ActionBytes: 78_000_000, AttemptBytes: 41_000_000,
		BuildDuration: 2350 * time.Millisecond, Rebuilds: 1,
		LastRebuildReason: "bytes the snapshot was not told about", LastRebuildAt: built,
	}})

	result := check.Run(context.Background())
	if result.Name != "action_store" || result.Status != domainhealth.StatusOK {
		t.Fatalf("result = %+v", result)
	}
	want := map[string]any{
		"indexed": true, "layout": "v2", "segments": 3, "current_segment": "20261014-01",
		"segment0_read_only": true, "segment0_matches_layout": true, "record_mismatches": 0,
		"actions": 58900, "attempts": 59100, "action_bytes": int64(78_000_000), "attempt_bytes": int64(41_000_000),
		"build_ms": int64(2350), "rebuilds": int64(1),
		"last_rebuild_reason": "bytes the snapshot was not told about", "last_rebuild_at": "2026-10-14T00:03:00Z",
		"dirty": false, "broken": false, "inspect_failed": false,
	}
	for key, value := range want {
		if got, ok := result.Details[key]; !ok || got != value {
			t.Errorf("details[%q] = %#v (present=%v), want %#v", key, got, ok, value)
		}
	}
	if attention, _ := result.Details["attention"].([]string); len(attention) != 0 {
		t.Errorf("attention = %v on a store with nothing to look at", attention)
	}
	if !strings.Contains(result.Message, "v2") || !strings.Contains(result.Message, "20261014-01") {
		t.Errorf("message = %q, want the layout and the current segment", result.Message)
	}
}

func TestActionStoreCheckOmitsSegmentZeroFactsOffLayoutV2AndNamesNoRebuildYet(t *testing.T) {
	result := NewActionStoreCheck(actionStoreStub{actionpersistence.StoreHealth{
		Indexed: false, Layout: "v1", Segments: 1, CurrentSegment: "legacy",
	}}).Run(context.Background())
	for _, key := range []string{"segment0_read_only", "segment0_matches_layout", "last_rebuild_at"} {
		if _, ok := result.Details[key]; ok {
			t.Errorf("details has %q on a store with nothing to say about it", key)
		}
	}
	if result.Details["indexed"] != false || result.Details["last_rebuild_reason"] != "none" {
		t.Errorf("details = %v", result.Details)
	}
}

// Dirty, Broken and a rebuild are states the store settles by itself; a closed
// segment that is not the size recorded is a fact for a person. None of them
// takes the process out of /ready: the store keeps serving from its own snapshot,
// and the readiness check of the backup and of core-verify depends on it.
func TestActionStoreCheckNeverTakesTheProcessOutOfReady(t *testing.T) {
	cases := map[string]actionpersistence.StoreHealth{
		"dirty":           {Indexed: true, Layout: "v2", Dirty: true},
		"sealed":          {Indexed: true, Layout: "v2", CurrentSegmentSealed: true},
		"broken":          {Indexed: true, Layout: "v2", Broken: true},
		"mismatch":        {Indexed: true, Layout: "v2", RecordMismatches: 2},
		"segment 0":       {Indexed: true, Layout: "v2", Segment0MatchesLayout: false, RecordMismatches: 1},
		"inspect failed":  {Indexed: true, InspectFailed: true},
		"rebuilt a lot":   {Indexed: true, Layout: "v2", Rebuilds: 500, LastRebuildReason: "a log is shorter than the snapshot covers"},
		"nothing indexed": {},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			service := healthapp.NewHealthService(NewActionStoreCheck(actionStoreStub{h}))
			if !service.IsReady(context.Background()) {
				t.Fatalf("the action store check took the process out of /ready: %+v", service.RunChecks(context.Background()))
			}
		})
	}
}

func TestActionStoreCheckNamesWhatAPersonShouldLookAt(t *testing.T) {
	result := NewActionStoreCheck(actionStoreStub{actionpersistence.StoreHealth{
		Indexed: true, Layout: "v2", Segments: 3, CurrentSegment: "20261014-01", RecordMismatches: 1,
	}}).Run(context.Background())
	attention, _ := result.Details["attention"].([]string)
	if len(attention) != 1 || attention[0] != "record_mismatch" {
		t.Fatalf("attention = %v, want [record_mismatch]", attention)
	}
	if !strings.Contains(result.Message, "record_mismatch") {
		t.Fatalf("message = %q does not name the attention", result.Message)
	}

	result = NewActionStoreCheck(actionStoreStub{actionpersistence.StoreHealth{
		Indexed: true, Layout: "v2", CurrentSegmentSealed: true,
	}}).Run(context.Background())
	attention, _ = result.Details["attention"].([]string)
	if len(attention) != 1 || attention[0] != "current_segment_sealed" || result.Details["current_segment_sealed"] != true {
		t.Fatalf("attention = %v details = %v, want [current_segment_sealed]", attention, result.Details)
	}

	result = NewActionStoreCheck(actionStoreStub{actionpersistence.StoreHealth{Indexed: true, InspectFailed: true}}).Run(context.Background())
	attention, _ = result.Details["attention"].([]string)
	if len(attention) != 1 || attention[0] != "layout_unreadable" {
		t.Fatalf("attention = %v, want [layout_unreadable]", attention)
	}
	if result.Details["layout"] != "unknown" {
		t.Fatalf("layout = %v, want unknown", result.Details["layout"])
	}
}

// What /health returns for a real store: no ID, no record text, no path.
func TestActionStoreCheckOnARealStoreCarriesNoIDNoTextNoPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "private-looking-workspace-name", "actions")
	store, err := actionpersistence.NewJSONLStoreWithOptions(root, actionpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 1, 0, 0, 0, time.UTC)
	task, run := modulecore.NewTaskID(), modulecore.NewRunID()
	attemptID := modulecore.NewAttemptID()
	action := domainaction.Action{
		ActionID: modulecore.NewActionID(), TaskID: task, RunID: run, Kind: domainaction.KindTool,
		Name: "distinctive-name-for-the-leak-test", Status: domainaction.StatusOpen,
		CurrentAttemptID: attemptID, CreatedAt: now, UpdatedAt: now,
	}
	attempt := domainaction.Attempt{
		AttemptID: attemptID, ActionID: action.ActionID, StartReason: domainaction.AttemptStartReasonFirst,
		Status: domainaction.AttemptStatusRunning, StartedAt: now,
	}
	if err := store.Transaction(context.Background(), func(tx domainaction.Store) error {
		if err := tx.SaveAction(context.Background(), action); err != nil {
			return err
		}
		return tx.SaveAttempt(context.Background(), attempt)
	}); err != nil {
		t.Fatal(err)
	}

	handler := healthadapter.NewHandler(healthapp.NewHealthService(NewActionStoreCheck(store)))
	recorder := httptest.NewRecorder()
	handler.HandleHealth(recorder, httptest.NewRequest("GET", "/health", nil))
	if recorder.Code != 200 {
		t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, secret := range []string{
		string(action.ActionID), string(attempt.AttemptID), string(task), string(run),
		"distinctive-name-for-the-leak-test", "private-looking-workspace-name", root,
	} {
		if strings.Contains(body, secret) {
			t.Fatalf("/health carries %q: %s", secret, body)
		}
	}

	var parsed struct {
		Checks []struct {
			Name    string         `json:"name"`
			Status  string         `json:"status"`
			Details map[string]any `json:"details"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Checks) != 1 || parsed.Checks[0].Name != "action_store" || parsed.Checks[0].Status != "ok" {
		t.Fatalf("checks = %+v", parsed.Checks)
	}
	details := parsed.Checks[0].Details
	if details["layout"] != "v1" || details["actions"] != float64(1) || details["indexed"] != true {
		t.Fatalf("details = %v", details)
	}
}
