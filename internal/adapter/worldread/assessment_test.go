package worldread

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const (
	testAssessedAt = "2026-10-22T01:02:03Z"
)

func validAssessmentBody() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"world_id":       "rencrow-home",
		"timeline_id":    "timeline-1",
		"timeline_epoch": "epoch-1",
		"actor_id":       "mio",
		"assessed_at":    testAssessedAt,
		"work_set_state": "assigned",
		"policy_state":   "fresh",
		"work_presentation": map[string]any{
			"kind":              "presence",
			"presence_task_ids": []any{"task-presence"},
			"running_task_ids":  []any{"task-run"},
			"queued_task_ids":   []any{"task-queue"},
			"waiting_task_ids":  []any{"task-wait"},
		},
		"permitted_activity_ids":  []any{"idle", "read", "walk"},
		"executable_activity_ids": []any{"idle", "read"},
		"new_activity_allowed":    true,
		"blocked_reason":          "",
		"work_success":            false,
	}
}

func newAssessmentServer(t *testing.T, body map[string]any, status int) (*httptest.Server, *string, *string, *int32) {
	t.Helper()
	var gotPath, gotQuery string
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		gotPath = r.URL.EscapedPath()
		gotQuery = r.URL.RawQuery
		if r.Header.Get("Authorization") != "Bearer read-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Accept") != "application/json" {
			http.Error(w, "not acceptable", http.StatusNotAcceptable)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "failed", status)
			return
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal fixture: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	return server, &gotPath, &gotQuery, &calls
}

func mustAssessmentClient(t *testing.T, baseURL string, actorIDs ...string) *Client {
	t.Helper()
	client, err := New(Config{
		BaseURL:       baseURL,
		Credential:    "read-token",
		WorldID:       "rencrow-home",
		TimelineID:    "timeline-1",
		TimelineEpoch: "epoch-1",
		ActorIDs:      actorIDs,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return client
}

func TestAssessmentReadsOneBoundActor(t *testing.T) {
	server, gotPath, gotQuery, calls := newAssessmentServer(t, validAssessmentBody(), http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio", "shiro")

	assessment, err := client.Assessment(context.Background(), "mio")
	if err != nil {
		t.Fatalf("Assessment() error = %v", err)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d; want 1", *calls)
	}
	if *gotPath != "/v1/worlds/rencrow-home/timelines/timeline-1/actors/mio/assessment" {
		t.Fatalf("path = %q", *gotPath)
	}
	if *gotQuery != "" {
		t.Fatalf("query = %q; assessment must send none", *gotQuery)
	}
	if assessment.SchemaVersion != 1 || assessment.WorldID != "rencrow-home" ||
		assessment.TimelineID != "timeline-1" || assessment.TimelineEpoch != "epoch-1" ||
		assessment.ActorID != "mio" {
		t.Fatalf("scope = %+v", assessment)
	}
	if assessment.AssessedAt.IsZero() || assessment.WorkSetState != "assigned" || assessment.PolicyState != "fresh" {
		t.Fatalf("states = %+v", assessment)
	}
	if assessment.WorkPresentation.Kind != "presence" ||
		strings.Join(assessment.WorkPresentation.PresenceTaskIDs, ",") != "task-presence" ||
		strings.Join(assessment.WorkPresentation.RunningTaskIDs, ",") != "task-run" ||
		strings.Join(assessment.WorkPresentation.QueuedTaskIDs, ",") != "task-queue" ||
		strings.Join(assessment.WorkPresentation.WaitingTaskIDs, ",") != "task-wait" {
		t.Fatalf("work presentation = %+v", assessment.WorkPresentation)
	}
	if strings.Join(assessment.PermittedActivityIDs, ",") != "idle,read,walk" {
		t.Fatalf("permitted = %v", assessment.PermittedActivityIDs)
	}
	if strings.Join(assessment.ExecutableActivityIDs, ",") != "idle,read" {
		t.Fatalf("executable = %v", assessment.ExecutableActivityIDs)
	}
	if !assessment.NewActivityAllowed || assessment.BlockedReason != "" || assessment.WorkSuccess {
		t.Fatalf("flags = %+v", assessment)
	}
}

func TestAssessmentEncodesTransportIDAndSendsNoQuery(t *testing.T) {
	body := validAssessmentBody()
	body["actor_id"] = "act 者"
	server, gotPath, gotQuery, _ := newAssessmentServer(t, body, http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "act 者")

	assessment, err := client.Assessment(context.Background(), "act 者")
	if err != nil {
		t.Fatalf("Assessment() error = %v", err)
	}
	if assessment.ActorID != "act 者" {
		t.Fatalf("actor = %q", assessment.ActorID)
	}
	if *gotPath != "/v1/worlds/rencrow-home/timelines/timeline-1/actors/"+testActorTransport+"/assessment" {
		t.Fatalf("path = %q", *gotPath)
	}
	if *gotQuery != "" {
		t.Fatalf("query = %q; assessment must send none", *gotQuery)
	}
}

// testActorTransport is the WORLD utf8-base64url-tilde-v1 transport form of
// "act 者", taken from the independent transport ID test.
const testActorTransport = "~YWN0IOiAhQ"

func TestAssessmentNeverRequestsActorOutsideViewScope(t *testing.T) {
	server, _, _, calls := newAssessmentServer(t, validAssessmentBody(), http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	_, err := client.Assessment(context.Background(), "shiro")
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Assessment(unscoped actor) error = %v; want ErrForbidden", err)
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Fatalf("calls = %d; want 0 for an actor outside the view scope", *calls)
	}
}

func TestAssessmentRejectsActorClaimOutsideViewScope(t *testing.T) {
	body := validAssessmentBody()
	body["actor_id"] = "shiro"
	server, _, _, _ := newAssessmentServer(t, body, http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	_, err := client.Assessment(context.Background(), "mio")
	if !errors.Is(err, ErrContract) {
		t.Fatalf("Assessment(actor mismatch) error = %v; want ErrContract", err)
	}
}

func TestAssessmentValidatesResponseContract(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "schema version", mutate: func(b map[string]any) { b["schema_version"] = 2 }},
		{name: "world", mutate: func(b map[string]any) { b["world_id"] = "other-world" }},
		{name: "timeline", mutate: func(b map[string]any) { b["timeline_id"] = "other-timeline" }},
		{name: "epoch", mutate: func(b map[string]any) { b["timeline_epoch"] = "epoch-2" }},
		{name: "missing assessed_at", mutate: func(b map[string]any) { delete(b, "assessed_at") }},
		{name: "zero assessed_at", mutate: func(b map[string]any) { b["assessed_at"] = "0001-01-01T00:00:00Z" }},
		{name: "missing work set state", mutate: func(b map[string]any) { b["work_set_state"] = "" }},
		{name: "missing policy state", mutate: func(b map[string]any) { b["policy_state"] = "" }},
		{name: "missing presentation", mutate: func(b map[string]any) { delete(b, "work_presentation") }},
		{name: "missing presentation kind", mutate: func(b map[string]any) { b["work_presentation"].(map[string]any)["kind"] = "" }},
		{name: "nil presence ids", mutate: func(b map[string]any) { b["work_presentation"].(map[string]any)["presence_task_ids"] = nil }},
		{name: "nil running ids", mutate: func(b map[string]any) { b["work_presentation"].(map[string]any)["running_task_ids"] = nil }},
		{name: "nil queued ids", mutate: func(b map[string]any) { b["work_presentation"].(map[string]any)["queued_task_ids"] = nil }},
		{name: "nil waiting ids", mutate: func(b map[string]any) { b["work_presentation"].(map[string]any)["waiting_task_ids"] = nil }},
		{name: "control task id", mutate: func(b map[string]any) {
			b["work_presentation"].(map[string]any)["queued_task_ids"] = []any{"task\x00queue"}
		}},
		{name: "missing permitted list", mutate: func(b map[string]any) { b["permitted_activity_ids"] = nil }},
		{name: "missing executable list", mutate: func(b map[string]any) { b["executable_activity_ids"] = nil }},
		{name: "control activity id", mutate: func(b map[string]any) { b["executable_activity_ids"] = []any{"read\x00"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := validAssessmentBody()
			test.mutate(body)
			server, _, _, _ := newAssessmentServer(t, body, http.StatusOK)
			client := mustAssessmentClient(t, server.URL, "mio")

			_, err := client.Assessment(context.Background(), "mio")
			if !errors.Is(err, ErrContract) {
				t.Fatalf("Assessment() error = %v; want ErrContract", err)
			}
		})
	}
}

func TestAssessmentEmptyListsStayEmpty(t *testing.T) {
	body := validAssessmentBody()
	body["work_set_state"] = "empty"
	body["policy_state"] = "missing"
	body["work_presentation"] = map[string]any{
		"kind":              "none",
		"presence_task_ids": []any{},
		"running_task_ids":  []any{},
		"queued_task_ids":   []any{},
		"waiting_task_ids":  []any{},
	}
	body["permitted_activity_ids"] = []any{}
	body["executable_activity_ids"] = []any{}
	body["new_activity_allowed"] = false
	body["blocked_reason"] = "policy_missing"
	server, _, _, _ := newAssessmentServer(t, body, http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	assessment, err := client.Assessment(context.Background(), "mio")
	if err != nil {
		t.Fatalf("Assessment() error = %v", err)
	}
	if assessment.WorkPresentation.Kind != "none" || len(assessment.WorkPresentation.RunningTaskIDs) != 0 ||
		len(assessment.PermittedActivityIDs) != 0 || len(assessment.ExecutableActivityIDs) != 0 {
		t.Fatalf("assessment = %+v", assessment)
	}
	if assessment.WorkPresentation.PresenceTaskIDs == nil || assessment.WorkPresentation.RunningTaskIDs == nil ||
		assessment.WorkPresentation.QueuedTaskIDs == nil || assessment.WorkPresentation.WaitingTaskIDs == nil ||
		assessment.PermittedActivityIDs == nil || assessment.ExecutableActivityIDs == nil {
		t.Fatalf("empty lists must stay non-nil: %+v", assessment)
	}
	if assessment.NewActivityAllowed || assessment.BlockedReason != "policy_missing" {
		t.Fatalf("flags = %+v", assessment)
	}
}

func TestAssessmentClassifiesTransportFailures(t *testing.T) {
	tests := []struct {
		status    int
		wantIs    error
		retryable bool
	}{
		{status: http.StatusUnauthorized, wantIs: ErrUnauthorized},
		{status: http.StatusForbidden, wantIs: ErrForbidden},
		{status: http.StatusServiceUnavailable, wantIs: ErrUnavailable, retryable: true},
		{status: http.StatusBadRequest, wantIs: ErrContract},
	}
	for _, test := range tests {
		server, _, _, _ := newAssessmentServer(t, validAssessmentBody(), test.status)
		client := mustAssessmentClient(t, server.URL, "mio")

		_, err := client.Assessment(context.Background(), "mio")
		if !errors.Is(err, test.wantIs) {
			t.Fatalf("status %d error = %v; want %v", test.status, err, test.wantIs)
		}
		var readErr *ReadError
		if !errors.As(err, &readErr) {
			t.Fatalf("status %d error = %T; want *ReadError", test.status, err)
		}
		if readErr.Op != "assessment" {
			t.Fatalf("op = %q; want assessment", readErr.Op)
		}
		if readErr.Retryable != test.retryable {
			t.Fatalf("status %d retryable = %v; want %v", test.status, readErr.Retryable, test.retryable)
		}
	}
}

func TestAssessmentValidatesArguments(t *testing.T) {
	server, _, _, calls := newAssessmentServer(t, validAssessmentBody(), http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	if _, err := client.Assessment(nil, "mio"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Assessment(nil ctx) error = %v; want ErrInvalidConfig", err)
	}
	if _, err := client.Assessment(context.Background(), ""); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Assessment(empty actor) error = %v; want ErrInvalidConfig", err)
	}
	if atomic.LoadInt32(calls) != 0 {
		t.Fatalf("calls = %d; want 0", *calls)
	}
}
