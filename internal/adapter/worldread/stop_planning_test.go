package worldread

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// Failure knowledge: docs/world-r08-r10-open-decisions.md D-11, D-12, D-27.
//
// Failure / Problem: WORLD publishes a safe-stop planning decision for one
// running activity on the same assessment route (D-11). A consumer that reads
// only the action name, or that tolerates an unknown key inside the decision,
// would act on a posture, a cleanup action, or an occupancy release WORLD never
// granted - the exact class of failure D-12 records, where a caller supplies
// meaning that belongs to WORLD's owner mapping.
//
// Cause: the read client validated the assessment envelope but had no type for
// the decision, so the decision either stayed unreadable or reached a consumer
// as raw fields whose consistency (an action with no interruption rule still
// naming a stop state, a retained-occupancy reason attached to a granted
// release) nothing checked.
//
// Lesson / Invariant: CORE reads the decision as WORLD's closed vocabulary of
// continue / stop / hold plus the posture, cleanup, and occupancy fields that
// belong to it, and never more: only a stop carries a stop state, cleanup
// actions, or an occupancy decision, a retained-occupancy reason never
// accompanies a granted release, the decision always names back the activity
// that was asked about, and an unknown key inside it fails as ErrContract with
// no partially read decision. A stop decision is a plan: consuming it stops,
// moves, or releases nothing, cancels no CORE work, and writes no WORLD State.
// CORE still has no production route that consumes it (D-27), so no browser,
// runtime route, or production port changes here.
//
// Enforcement: this test plus the fail-closed validation in assessment.go.
//
// Tests: TestStopPlanningReadsContinueDecision, TestStopPlanningReadsStopDecision,
// TestStopPlanningReadsHoldDecision, TestStopPlanningRejectsInconsistentDecision,
// TestStopPlanningRejectsMissingDecisionField, TestStopPlanningRequiresPublishedDecision,
// TestAssessmentRejectsUnrequestedStopPlanning,
// TestStopPlanningRejectsUnknownDecisionField,
// TestStopPlanningRejectsInvalidRequestBeforeRequest.

// stopPlanningBody attaches one decision to an assessment body. Only
// activity_id, action, and reason vary here; each test overrides the fields its
// case is about, so an unset field stays at the value WORLD publishes when no
// stop effect is granted.
func stopPlanningBody(body map[string]any, activityID, action, reason string) map[string]any {
	body["stop_planning"] = map[string]any{
		"activity_id":             activityID,
		"action":                  action,
		"reason":                  reason,
		"stop_state":              "",
		"stop_actions":            []any{},
		"release_occupancy":       false,
		"clear_work_presentation": false,
	}
	return body
}

func validStopPlanningBody(activityID, action, reason string) map[string]any {
	return stopPlanningBody(validAssessmentBody(), activityID, action, reason)
}

func withStopPlanning(body map[string]any, fields map[string]any) map[string]any {
	plan := body["stop_planning"].(map[string]any)
	for key, value := range fields {
		plan[key] = value
	}
	return body
}

// isEmptyStopDecision checks the zero value field by field, because a rejected
// read must leave no field a consumer could act on.
func isEmptyStopDecision(decision StopDecision) bool {
	return decision.ActivityID == "" && decision.Action == "" && decision.Reason == "" &&
		decision.StopState == "" && len(decision.StopActions) == 0 && !decision.ReleaseOccupancy &&
		decision.RetainOccupancyReason == "" && !decision.ClearWorkPresentation
}

func TestStopPlanningReadsContinueDecision(t *testing.T) {
	server, gotPath, gotQuery, calls := newAssessmentServer(
		t, validStopPlanningBody("read", "continue", "permitted"), http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	decision, err := client.StopPlanning(context.Background(), "mio", "read")
	if err != nil {
		t.Fatalf("StopPlanning() error = %v", err)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d; want 1", *calls)
	}
	if *gotPath != "/v1/worlds/rencrow-home/timelines/timeline-1/actors/mio/assessment" {
		t.Fatalf("path = %q", *gotPath)
	}
	if *gotQuery != "stop_planning_activity_id=read" {
		t.Fatalf("query = %q; the decision is planned for exactly one activity ID", *gotQuery)
	}
	if decision.ActivityID != "read" || decision.Action != StopPlanningContinue ||
		decision.Reason != "permitted" {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.StopState != "" || len(decision.StopActions) != 0 ||
		decision.ReleaseOccupancy || decision.RetainOccupancyReason != "" {
		t.Fatalf("a continue decision granted a stop effect: %+v", decision)
	}
	if decision.ClearWorkPresentation {
		t.Fatalf("decision = %+v; continue clears nothing", decision)
	}
}

func TestStopPlanningReadsStopDecision(t *testing.T) {
	// WORLD's own derivation: a missing LifePolicy stops the running activity,
	// and a fresh empty WorkSet is the only state that clears the presentation.
	base := deniedAssessmentBody("missing", "policy_missing", []any{})
	body := withStopPlanning(stopPlanningBody(base, "read", "stop", "policy_missing"), map[string]any{
		"stop_state":              "standing",
		"stop_actions":            []any{"stop:read"},
		"clear_work_presentation": true,
	})
	server, _, _, calls := newAssessmentServer(t, body, http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	decision, err := client.StopPlanning(context.Background(), "mio", "read")
	if err != nil {
		t.Fatalf("StopPlanning() error = %v", err)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d; want 1", *calls)
	}
	if decision.Action != StopPlanningStop || decision.StopState != "standing" ||
		strings.Join(decision.StopActions, ",") != "stop:read" || !decision.ClearWorkPresentation {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.ReleaseOccupancy {
		t.Fatalf("decision = %+v; WORLD releases no occupancy without measured geometry (D-17, D-18)", decision)
	}
}

func TestStopPlanningReadsHoldDecision(t *testing.T) {
	server, _, _, calls := newAssessmentServer(
		t, validStopPlanningBody("sleep", "hold", "not_interruptible"), http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	decision, err := client.StopPlanning(context.Background(), "mio", "sleep")
	if err != nil {
		t.Fatalf("StopPlanning() error = %v", err)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d; want 1", *calls)
	}
	if decision.ActivityID != "sleep" || decision.Action != StopPlanningHold ||
		decision.Reason != "not_interruptible" {
		t.Fatalf("decision = %+v", decision)
	}
	if decision.StopState != "" || len(decision.StopActions) != 0 ||
		decision.ReleaseOccupancy || decision.RetainOccupancyReason != "" {
		t.Fatalf("a hold grants no posture, action, or release: %+v", decision)
	}
	if decision.ClearWorkPresentation {
		t.Fatalf("decision = %+v; a hold clears nothing while a work set is assigned", decision)
	}
}

func TestStopPlanningRejectsInconsistentDecision(t *testing.T) {
	tests := []struct {
		name   string
		action string
		reason string
		fields map[string]any
	}{
		{name: "action outside vocabulary", action: "pause", reason: "permitted", fields: nil},
		{name: "empty action", action: "", reason: "permitted", fields: nil},
		{name: "missing reason", action: "continue", reason: "", fields: nil},
		{name: "continue names a stop state", action: "continue", reason: "permitted",
			fields: map[string]any{"stop_state": "standing"}},
		{name: "continue names cleanup", action: "continue", reason: "permitted",
			fields: map[string]any{"stop_actions": []any{"stop:read"}}},
		{name: "continue releases occupancy", action: "continue", reason: "permitted",
			fields: map[string]any{"release_occupancy": true}},
		{name: "hold names a stop state", action: "hold", reason: "not_interruptible",
			fields: map[string]any{"stop_state": "sitting"}},
		{name: "hold names cleanup", action: "hold", reason: "not_interruptible",
			fields: map[string]any{"stop_actions": []any{"stop:sleep"}}},
		{name: "hold releases occupancy", action: "hold", reason: "not_interruptible",
			fields: map[string]any{"release_occupancy": true}},
		{name: "stop without a stop state", action: "stop", reason: "policy_missing",
			fields: map[string]any{"stop_actions": []any{"stop:read"}}},
		{name: "stop without cleanup", action: "stop", reason: "policy_missing",
			fields: map[string]any{"stop_state": "standing"}},
		{name: "stop names retained occupancy while releasing", action: "stop", reason: "policy_missing",
			fields: map[string]any{"stop_state": "standing", "stop_actions": []any{"stop:read"},
				"release_occupancy": true, "retain_occupancy_reason": "presence_work_holds_occupancy"}},
		{name: "stop carries an invalid cleanup ID", action: "stop", reason: "policy_missing",
			fields: map[string]any{"stop_state": "standing", "stop_actions": []any{"stop:read\n"}}},
		{name: "decision names another activity", action: "continue", reason: "permitted",
			fields: map[string]any{"activity_id": "walk"}},
		{name: "decision carries an invalid activity ID", action: "continue", reason: "permitted",
			fields: map[string]any{"activity_id": "read "}},
		{name: "decision clears the presentation of an assigned work set", action: "continue", reason: "permitted",
			fields: map[string]any{"clear_work_presentation": true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _, calls := newAssessmentServer(
				t, withStopPlanning(validStopPlanningBody("read", test.action, test.reason), test.fields), http.StatusOK)
			client := mustAssessmentClient(t, server.URL, "mio")

			decision, err := client.StopPlanning(context.Background(), "mio", "read")
			if !errors.Is(err, ErrContract) {
				t.Fatalf("error = %v; want ErrContract", err)
			}
			if !isEmptyStopDecision(decision) {
				t.Fatalf("decision = %+v; a rejected decision must be empty", decision)
			}
			if *calls != 1 {
				t.Fatalf("calls = %d; want 1", *calls)
			}
		})
	}
}

func TestStopPlanningRejectsMissingDecisionField(t *testing.T) {
	tests := []struct {
		name   string
		fields map[string]any
	}{
		{name: "activity_id", fields: map[string]any{"activity_id": nil}},
		{name: "action", fields: map[string]any{"action": nil}},
		{name: "reason", fields: map[string]any{"reason": nil}},
		{name: "stop_state", fields: map[string]any{"stop_state": nil}},
		{name: "stop_actions", fields: map[string]any{"stop_actions": nil}},
		{name: "release_occupancy", fields: map[string]any{"release_occupancy": nil}},
		{name: "clear_work_presentation", fields: map[string]any{"clear_work_presentation": nil}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := validStopPlanningBody("read", "continue", "permitted")
			plan := body["stop_planning"].(map[string]any)
			for key := range test.fields {
				delete(plan, key)
			}
			server, _, _, calls := newAssessmentServer(t, body, http.StatusOK)
			client := mustAssessmentClient(t, server.URL, "mio")

			decision, err := client.StopPlanning(context.Background(), "mio", "read")
			if !errors.Is(err, ErrContract) {
				t.Fatalf("error = %v; want ErrContract", err)
			}
			if !isEmptyStopDecision(decision) {
				t.Fatalf("decision = %+v; a partial decision must not be read", decision)
			}
			if *calls != 1 {
				t.Fatalf("calls = %d; want 1", *calls)
			}
		})
	}
}

func TestStopPlanningRequiresPublishedDecision(t *testing.T) {
	server, _, gotQuery, calls := newAssessmentServer(t, validAssessmentBody(), http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	decision, err := client.StopPlanning(context.Background(), "mio", "read")
	if !errors.Is(err, ErrContract) {
		t.Fatalf("error = %v; want ErrContract", err)
	}
	if !isEmptyStopDecision(decision) {
		t.Fatalf("decision = %+v", decision)
	}
	if *calls != 1 || *gotQuery != "stop_planning_activity_id=read" {
		t.Fatalf("calls = %d, query = %q", *calls, *gotQuery)
	}
}

func TestAssessmentRejectsUnrequestedStopPlanning(t *testing.T) {
	server, _, gotQuery, calls := newAssessmentServer(
		t, validStopPlanningBody("read", "continue", "permitted"), http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	assessment, err := client.Assessment(context.Background(), "mio")
	if !errors.Is(err, ErrContract) {
		t.Fatalf("error = %v; want ErrContract", err)
	}
	if assessment.ActorID != "" {
		t.Fatalf("assessment = %+v; an unreadable response returns nothing", assessment)
	}
	if *calls != 1 || *gotQuery != "" {
		t.Fatalf("calls = %d, query = %q", *calls, *gotQuery)
	}
}

func TestStopPlanningRejectsUnknownDecisionField(t *testing.T) {
	body := withStopPlanning(validStopPlanningBody("read", "stop", "policy_missing"), map[string]any{
		"stop_state":    "standing",
		"stop_actions":  []any{"stop:read"},
		"move_actor_to": "bedroom",
	})
	server, _, _, calls := newAssessmentServer(t, body, http.StatusOK)
	client := mustAssessmentClient(t, server.URL, "mio")

	decision, err := client.StopPlanning(context.Background(), "mio", "read")
	if !errors.Is(err, ErrContract) {
		t.Fatalf("error = %v; want ErrContract", err)
	}
	if !isEmptyStopDecision(decision) {
		t.Fatalf("decision = %+v", decision)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d; want 1", *calls)
	}
}

func TestStopPlanningRejectsInvalidRequestBeforeRequest(t *testing.T) {
	tests := []struct {
		name       string
		actorID    string
		activityID string
		wantErr    error
	}{
		{name: "empty actor", actorID: "", activityID: "read", wantErr: ErrInvalidConfig},
		{name: "unbound actor", actorID: "kuro", activityID: "read", wantErr: ErrForbidden},
		{name: "empty activity", actorID: "mio", activityID: "", wantErr: ErrInvalidConfig},
		{name: "blank activity", actorID: "mio", activityID: " ", wantErr: ErrInvalidConfig},
		{name: "activity with a control character", actorID: "mio", activityID: "read\n", wantErr: ErrInvalidConfig},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, _, _, calls := newAssessmentServer(
				t, validStopPlanningBody("read", "continue", "permitted"), http.StatusOK)
			client := mustAssessmentClient(t, server.URL, "mio")

			decision, err := client.StopPlanning(context.Background(), test.actorID, test.activityID)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v; want %v", err, test.wantErr)
			}
			if !isEmptyStopDecision(decision) {
				t.Fatalf("decision = %+v", decision)
			}
			if *calls != 0 {
				t.Fatalf("calls = %d; no request may be sent", *calls)
			}
		})
	}
}
