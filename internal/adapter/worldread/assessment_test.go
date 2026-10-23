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
			"running_task_ids":  []any{"task-presence"},
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
		strings.Join(assessment.WorkPresentation.RunningTaskIDs, ",") != "task-presence" ||
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
		{name: "executable outside permitted", mutate: func(b map[string]any) { b["executable_activity_ids"] = []any{"idle", "sleep"} }},
		{name: "executable list when permitted list is empty", mutate: func(b map[string]any) {
			b["permitted_activity_ids"] = []any{}
			b["executable_activity_ids"] = []any{"idle"}
			b["new_activity_allowed"] = false
			b["blocked_reason"] = "no_allowed_activity"
		}},
		{name: "work success claimed", mutate: func(b map[string]any) { b["work_success"] = true }},
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

// TestAssessmentRejectsContradictoryAssessment pins the assessment vocabulary
// and the internal consistency of WORLD's assessment contract: an unknown state
// or presentation kind, a presentation that does not follow from the WorkSet
// state and task ID lists, or a permission flag that contradicts its
// blocked_reason is a contract violation. CORE never guesses a meaning for an
// undefined enum value, never reads a presentation the WorkSet does not carry,
// and never treats a blocked assessment as an open permission.
func TestAssessmentRejectsContradictoryAssessment(t *testing.T) {
	emptyPresentation := func(b map[string]any, kind string) {
		b["work_presentation"] = map[string]any{
			"kind":              kind,
			"presence_task_ids": []any{},
			"running_task_ids":  []any{},
			"queued_task_ids":   []any{},
			"waiting_task_ids":  []any{},
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "unknown work set state", mutate: func(b map[string]any) { b["work_set_state"] = "assigneed" }},
		{name: "unknown policy state", mutate: func(b map[string]any) { b["policy_state"] = "stale" }},
		{name: "unknown presentation kind", mutate: func(b map[string]any) {
			b["work_presentation"].(map[string]any)["kind"] = "moving"
		}},
		{name: "assigned work set presented as none", mutate: func(b map[string]any) { emptyPresentation(b, "none") }},
		{name: "expired work set still presents presence", mutate: func(b map[string]any) {
			b["work_set_state"] = "expired"
		}},
		{name: "assigned work set without any task", mutate: func(b map[string]any) { emptyPresentation(b, "queued") }},
		{name: "presence kind without a presence task", mutate: func(b map[string]any) {
			b["work_presentation"].(map[string]any)["presence_task_ids"] = []any{}
		}},
		{name: "presence task outside running tasks", mutate: func(b map[string]any) {
			b["work_presentation"].(map[string]any)["presence_task_ids"] = []any{"task-elsewhere"}
		}},
		{name: "remote kind without a running task", mutate: func(b map[string]any) {
			b["work_presentation"].(map[string]any)["kind"] = "remote"
			b["work_presentation"].(map[string]any)["presence_task_ids"] = []any{}
			b["work_presentation"].(map[string]any)["running_task_ids"] = []any{}
		}},
		{name: "queued kind while work runs", mutate: func(b map[string]any) {
			b["work_presentation"].(map[string]any)["kind"] = "queued"
			b["work_presentation"].(map[string]any)["presence_task_ids"] = []any{}
		}},
		{name: "waiting kind while work runs", mutate: func(b map[string]any) {
			b["work_presentation"].(map[string]any)["kind"] = "waiting"
			b["work_presentation"].(map[string]any)["presence_task_ids"] = []any{}
		}},
		{name: "permission with a blocked reason", mutate: func(b map[string]any) { b["blocked_reason"] = "policy_missing" }},
		{name: "denied permission without a blocked reason", mutate: func(b map[string]any) {
			b["new_activity_allowed"] = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := validAssessmentBody()
			test.mutate(body)
			server, _, _, _ := newAssessmentServer(t, body, http.StatusOK)
			client := mustAssessmentClient(t, server.URL, "mio")

			assessment, err := client.Assessment(context.Background(), "mio")
			if !errors.Is(err, ErrContract) {
				t.Fatalf("Assessment() error = %v, assessment = %+v; want ErrContract", err, assessment)
			}
			if assessment.SchemaVersion != 0 || assessment.WorldID != "" || assessment.WorkSetState != "" ||
				assessment.WorkPresentation.Kind != "" || len(assessment.ExecutableActivityIDs) != 0 {
				t.Fatalf("a contradictory assessment must not return a partially read value: %+v", assessment)
			}
		})
	}
}

// deniedAssessmentBody builds an assessment whose new-activity permission is
// closed for one policy state, with the WORLD reason and lists that state
// publishes.
func deniedAssessmentBody(policyState string, reason string, permitted []any) map[string]any {
	body := validAssessmentBody()
	body["work_set_state"] = "empty"
	body["policy_state"] = policyState
	body["work_presentation"] = map[string]any{
		"kind":              "none",
		"presence_task_ids": []any{},
		"running_task_ids":  []any{},
		"queued_task_ids":   []any{},
		"waiting_task_ids":  []any{},
	}
	body["permitted_activity_ids"] = permitted
	body["executable_activity_ids"] = []any{}
	body["new_activity_allowed"] = false
	body["blocked_reason"] = reason
	return body
}

// D-09, D-10: a LifePolicy that is missing, expired without a registered
// fallback, or holds no registered activity never opens a new life activity.
// WORLD publishes that as one closed reason vocabulary tied to the policy state
// plus an empty permitted set, so CORE fails closed on a route that grants a new
// activity without a permitted candidate, names a reason WORLD never produces,
// or keeps a permitted candidate while reporting the policy as missing.
func TestAssessmentRejectsPermissionThePolicyCannotGrant(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
	}{
		{name: "missing policy still grants a new activity", body: func() map[string]any {
			body := deniedAssessmentBody("missing", "", []any{"idle"})
			body["executable_activity_ids"] = []any{"idle"}
			body["new_activity_allowed"] = true
			return body
		}()},
		{name: "granted without a permitted activity", body: func() map[string]any {
			body := deniedAssessmentBody("fresh", "", []any{})
			body["new_activity_allowed"] = true
			return body
		}()},
		{name: "unknown blocked reason", body: deniedAssessmentBody("missing", "policy_absent", []any{})},
		{name: "missing policy keeps a permitted activity", body: deniedAssessmentBody("missing", "policy_missing", []any{"idle"})},
		{name: "expired policy names the missing reason", body: deniedAssessmentBody("expired", "policy_missing", []any{})},
		{name: "expired fallback denial names the missing reason", body: deniedAssessmentBody("expired", "no_registered_activity", []any{})},
		{name: "fresh policy names an expired reason", body: deniedAssessmentBody("fresh", "policy_expired_no_fallback", []any{})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _, _ := newAssessmentServer(t, test.body, http.StatusOK)
			client := mustAssessmentClient(t, server.URL, "mio")

			assessment, err := client.Assessment(context.Background(), "mio")
			if !errors.Is(err, ErrContract) {
				t.Fatalf("Assessment() error = %v, assessment = %+v; want ErrContract", err, assessment)
			}
			if assessment.NewActivityAllowed || len(assessment.PermittedActivityIDs) != 0 || assessment.BlockedReason != "" {
				t.Fatalf("a denied policy must not return a partially read value: %+v", assessment)
			}
		})
	}
}

// TestAssessmentReadsWorldDenials is the fail direction's counterpart: every
// denial WORLD really publishes stays readable, so the D-09 guard above cannot
// turn a legitimate `blocked` into a contract error.
func TestAssessmentReadsWorldDenials(t *testing.T) {
	tests := []struct {
		name        string
		policyState string
		reason      string
		permitted   []any
	}{
		{name: "policy missing", policyState: "missing", reason: "policy_missing", permitted: []any{}},
		{name: "expired without fallback", policyState: "expired", reason: "policy_expired_no_fallback", permitted: []any{}},
		{name: "fresh without allowed activity", policyState: "fresh", reason: "no_allowed_activity", permitted: []any{}},
		{name: "fresh without registered activity", policyState: "fresh", reason: "no_registered_activity", permitted: []any{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _, _ := newAssessmentServer(t, deniedAssessmentBody(test.policyState, test.reason, test.permitted), http.StatusOK)
			client := mustAssessmentClient(t, server.URL, "mio")

			assessment, err := client.Assessment(context.Background(), "mio")
			if err != nil {
				t.Fatalf("Assessment() error = %v", err)
			}
			if assessment.NewActivityAllowed || assessment.BlockedReason != test.reason ||
				assessment.PolicyState != test.policyState || len(assessment.PermittedActivityIDs) != 0 {
				t.Fatalf("assessment = %+v; want a readable denial %q", assessment, test.reason)
			}
		})
	}
}
