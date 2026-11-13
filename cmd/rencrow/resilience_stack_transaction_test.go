package main

import (
	"context"
	"testing"
)

func TestResilienceStackPendingPartialStopRestartsWholeGraph(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	oldPIDs := make(map[string]int, len(plan.Services))
	for _, service := range plan.Services {
		oldPIDs[service.Label] = operator.states[service.Label].PID
	}
	operator.failStop = "com.rencrow.qdrant"
	_, err, store := reconcileResilienceStackForTest(t, plan, 0, false, resilienceStackState{CorePID: 41}, operator)
	if err == nil {
		t.Fatal("expected the injected mid-stop failure")
	}

	operator.failStop = ""
	result, err := reconcileResilienceStack(context.Background(), plan, 0, false, store.state, operator, store)
	if err != nil || result.Action != "restarted" {
		t.Fatalf("pending stop did not complete: result=%+v err=%v", result, err)
	}
	for _, service := range plan.Services {
		if got := operator.states[service.Label].PID; got == oldPIDs[service.Label] {
			t.Errorf("service %s retained old PID %d after pending whole-stack recovery: events=%v", service.Label, got, operator.events)
		}
	}
}

func TestResilienceStackRejectsDifferentPendingManifestBeforeLaunchdInspection(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	state := resilienceStackState{CorePID: 42, PendingPlanSHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
	_, err := reconcileResilienceStack(context.Background(), plan, 0, false, state, operator, &fakeResilienceStackStateStore{state: state})
	if err == nil {
		t.Fatal("pending recovery for a different manifest was accepted")
	}
	if len(operator.events) != 0 {
		t.Fatalf("manifest/state mismatch reached launchd inspection: %v", operator.events)
	}
}

func TestResilienceStackAllowsBoardAndTailnetJobsInUser501Domain(t *testing.T) {
	plan := resilienceStackFixture(t)
	var err error
	for _, index := range []int{4, 5} {
		service := plan.Services[index]
		service.Domain = resilienceStackUser501
		service.PlistPath, err = resilienceStackFixturePlistPath(service.Domain, service.Label)
		if err != nil {
			t.Fatal(err)
		}
		plan.Services[index] = service
	}
	plan.Services[5].Label = "com.rencrow.tailnet-browser"
	plan.Services[5].PlistPath, err = resilienceStackFixturePlistPath(plan.Services[5].Domain, plan.Services[5].Label)
	if err != nil {
		t.Fatal(err)
	}
	if err := testResilienceStackPlan(plan); err != nil {
		t.Fatalf("user/501 board and tailnet targets were rejected: %v", err)
	}
}
