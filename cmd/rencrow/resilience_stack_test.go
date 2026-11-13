package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResilienceStackReconcileNoOpsForHealthyOrIntentionalMaintenance(t *testing.T) {
	plan := resilienceStackFixture(t)
	t.Run("healthy baseline", func(t *testing.T) {
		operator := newFakeResilienceStackOperator(plan, 42)
		result, err, _ := reconcileResilienceStackForTest(t, plan, 0, false, resilienceStackState{CorePID: 42}, operator)
		if err != nil {
			t.Fatal(err)
		}
		if result.Action != "noop" || result.Reason != "healthy" {
			t.Fatalf("unexpected result: %+v", result)
		}
		if operator.mutationCount() != 0 {
			t.Fatalf("healthy stack mutated: %v", operator.events)
		}
	})

	t.Run("maintenance marker", func(t *testing.T) {
		operator := newFakeResilienceStackOperator(plan, 0)
		result, err, stateStore := reconcileResilienceStackForTest(t, plan, 501, true, resilienceStackState{CorePID: 42}, operator)
		if err != nil {
			t.Fatal(err)
		}
		if result.Action != "noop" || result.Reason != "maintenance" {
			t.Fatalf("unexpected result: %+v", result)
		}
		if len(operator.events) != 0 {
			t.Fatalf("maintenance should not inspect or mutate launchd: %v", operator.events)
		}
		if stateStore.state.CorePID != 0 {
			t.Fatalf("maintenance should clear the previous CORE baseline: %+v", stateStore.state)
		}
	})
}

func TestResilienceStackCheckIsReadOnlyAndReportsDegradedDependencies(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.states["com.rencrow.redis"] = resilienceStackSnapshot{Loaded: true, Running: false, PID: 141}

	result, err := checkResilienceStack(context.Background(), plan, false, operator)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "checked" || result.Reason != "CORE healthy; dependencies degraded" || !reflect.DeepEqual(result.Degraded, []string{"com.rencrow.redis"}) {
		t.Fatalf("read-only check result = %+v", result)
	}
	if operator.mutationCount() != 0 {
		t.Fatalf("read-only check mutated launchd: %v", operator.events)
	}

	operator.events = nil
	result, err = checkResilienceStack(context.Background(), plan, true, operator)
	if err != nil || result.Action != "noop" || result.Reason != "maintenance" || len(operator.events) != 0 {
		t.Fatalf("maintenance check inspected or mutated launchd: result=%+v err=%v events=%v", result, err, operator.events)
	}
}

func TestResilienceStackProcessReadinessUsesLaunchdStateWithoutNetworkTarget(t *testing.T) {
	service := resilienceStackService{
		Label:     "com.rencrow.telemetry",
		Domain:    resilienceStackDomain,
		PlistPath: "/Library/LaunchDaemons/com.rencrow.telemetry.plist",
		Readiness: resilienceStackReadiness{Kind: "process", TimeoutSeconds: 5},
	}
	if err := validateResilienceStackReadiness(service); err != nil {
		t.Fatalf("process readiness without a network endpoint was rejected: %v", err)
	}
	for name, snapshot := range map[string]resilienceStackSnapshot{
		"unloaded":    {},
		"not running": {Loaded: true, Running: false, PID: 91},
		"missing pid": {Loaded: true, Running: true},
		"maintenance": {Loaded: true, Running: true, PID: 91, Disabled: true},
	} {
		if err := validateResilienceStackProcessSnapshot(snapshot); err == nil {
			t.Errorf("process readiness accepted %s snapshot: %+v", name, snapshot)
		}
	}
	if err := validateResilienceStackProcessSnapshot(resilienceStackSnapshot{Loaded: true, Running: true, PID: 91}); err != nil {
		t.Fatalf("running launchd process was not ready: %v", err)
	}
	for name, mutate := range map[string]func(*resilienceStackService){
		"address": func(s *resilienceStackService) { s.Readiness.Address = "127.0.0.1:9999" },
		"path":    func(s *resilienceStackService) { s.Readiness.Path = "/health" },
		"timeout": func(s *resilienceStackService) { s.Readiness.TimeoutSeconds = 0 },
	} {
		invalid := service
		mutate(&invalid)
		if err := validateResilienceStackReadiness(invalid); err == nil {
			t.Errorf("process readiness accepted %s configuration: %+v", name, invalid.Readiness)
		}
	}
}

func TestResilienceStackReconcilePreservesPendingCursorsInMaintenance(t *testing.T) {
	plan := resilienceStackFixture(t)
	planHash, err := resilienceStackPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	state := resilienceStackState{CorePID: 42, PendingPlanSHA256: planHash, PendingPhase: resilienceStackPhaseStart, PendingStopCursor: len(plan.Services), PendingStartCursor: 2}
	operator := newFakeResilienceStackOperator(plan, 42)
	result, err, store := reconcileResilienceStackForTest(t, plan, 0, true, state, operator)
	if err != nil || result.Action != "noop" || store.state.CorePID != 0 {
		t.Fatalf("maintenance did not protect the pending transaction: result=%+v state=%+v err=%v", result, store.state, err)
	}
	if store.state.PendingPlanSHA256 != state.PendingPlanSHA256 || store.state.PendingPhase != state.PendingPhase || store.state.PendingStopCursor != state.PendingStopCursor || store.state.PendingStartCursor != state.PendingStartCursor {
		t.Fatalf("maintenance changed pending phase/cursors: before=%+v after=%+v", state, store.state)
	}
	if len(operator.events) != 0 {
		t.Fatalf("maintenance touched launchd: %v", operator.events)
	}
}

func TestResilienceStackReconcileRejectsInvalidPlanBeforeOperations(t *testing.T) {
	plan := resilienceStackFixture(t)
	plan.Services[0].PlistPath = "/tmp/com.rencrow.redis.plist"
	operator := newFakeResilienceStackOperator(plan, 42)
	if _, err, _ := reconcileResilienceStackForTest(t, plan, 0, false, resilienceStackState{CorePID: 42}, operator); err == nil {
		t.Fatal("unsafe plist path was accepted")
	}
	if len(operator.events) != 0 {
		t.Fatalf("invalid plan reached launchd: %v", operator.events)
	}
}

func TestResilienceStackReconcileRejectsInvalidCursorBeforeInspection(t *testing.T) {
	plan := resilienceStackFixture(t)
	planHash, err := resilienceStackPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	operator := newFakeResilienceStackOperator(plan, 42)
	state := resilienceStackState{PendingPlanSHA256: planHash, PendingPhase: resilienceStackPhaseStop, PendingStopCursor: len(plan.Services) + 1}
	if _, err := reconcileResilienceStack(context.Background(), plan, 0, false, state, operator, &fakeResilienceStackStateStore{state: state}); err == nil {
		t.Fatal("out-of-range stop cursor was accepted")
	}
	if len(operator.events) != 0 {
		t.Fatalf("invalid cursor reached launchd inspection: %v", operator.events)
	}
}

func TestResilienceStackReconcilePreflightsAllOwnersBeforeAnyStop(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	if _, err, _ := reconcileResilienceStackForTest(t, plan, 501, false, resilienceStackState{CorePID: 42}, operator); err == nil {
		t.Fatal("user/501 was allowed to reconcile a plan containing system jobs")
	}
	if operator.mutationCount() != 0 {
		t.Fatalf("partial authorization caused stack mutation: %v", operator.events)
	}
}

func TestResilienceStackAcceptsCanonicalUser501CoreTarget(t *testing.T) {
	plan := resilienceStackFixture(t)
	core := findResilienceStackService(t, plan, "com.rencrow.core")
	wantPath, err := resilienceStackFixturePlistPath(resilienceStackUser501, "com.rencrow.core")
	if err != nil {
		t.Fatal(err)
	}
	if core.Domain != resilienceStackUser501 || core.PlistPath != wantPath {
		t.Fatalf("CORE target = %s %s, want canonical user/501 path %s", core.Domain, core.PlistPath, wantPath)
	}
	if err := testResilienceStackPlan(plan); err != nil {
		t.Fatalf("canonical user/501 CORE plan was rejected: %v", err)
	}
	if err := resilienceStackPermission(501, resilienceStackUser501); err != nil {
		t.Fatalf("UID 501 should manage its user domain: %v", err)
	}
	if err := resilienceStackPermission(501, resilienceStackDomain); err == nil {
		t.Fatal("UID 501 must not be authorized for system jobs")
	}
	if err := resilienceStackPermission(502, resilienceStackUser501); err == nil {
		t.Fatal("unrelated UID must not be authorized for user/501")
	}
}

func TestResilienceStackReconcileDetectsCoreLossAndRestartsDependencyOrder(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	result, err, _ := reconcileResilienceStackForTest(t, plan, 0, false, resilienceStackState{CorePID: 41}, operator)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "restarted" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if got, want := result.Stopped, []string{"com.rencrow.portal", "com.rencrow.board-gateway.public", "com.rencrow.core", "com.rencrow.llm-gateway", "com.rencrow.qdrant", "com.rencrow.redis"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stop order = %v, want %v", got, want)
	}
	if got, want := result.Started, []string{"com.rencrow.redis", "com.rencrow.qdrant", "com.rencrow.llm-gateway", "com.rencrow.core", "com.rencrow.board-gateway.public", "com.rencrow.portal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("start order = %v, want %v", got, want)
	}

	mutationStart := firstEventIndex(operator.events, "stop:")
	if mutationStart < 0 {
		t.Fatalf("no stop phase observed: %v", operator.events)
	}
	phase := operator.events[mutationStart:]
	stopIndex := 0
	startIndex := -1
	for index, event := range phase {
		if strings.HasPrefix(event, "stop:") {
			if startIndex >= 0 {
				t.Fatalf("stop occurred after start phase began: %v", phase)
			}
			want := []string{"stop:com.rencrow.portal", "stop:com.rencrow.board-gateway.public", "stop:com.rencrow.core", "stop:com.rencrow.llm-gateway", "stop:com.rencrow.qdrant", "stop:com.rencrow.redis"}
			if stopIndex >= len(want) || event != want[stopIndex] {
				t.Fatalf("stop phase order diverged at %d: %v", stopIndex, phase)
			}
			stopIndex++
		}
		if strings.HasPrefix(event, "start:") && startIndex < 0 {
			startIndex = index
		}
	}
	if stopIndex != len(plan.Services) || startIndex < 0 {
		t.Fatalf("stop/start phases were incomplete: stopped=%d startedAt=%d events=%v", stopIndex, startIndex, phase)
	}
	for _, label := range []string{"com.rencrow.redis", "com.rencrow.qdrant", "com.rencrow.llm-gateway", "com.rencrow.core", "com.rencrow.board-gateway.public", "com.rencrow.portal"} {
		if index := firstEventIndex(phase[startIndex:], "start:"+label); index < 0 {
			t.Errorf("service %s did not start in forward dependency order: %v", label, phase)
		}
		if index := firstEventIndex(phase[startIndex:], "ready:"+label); index < 0 {
			t.Errorf("service %s did not pass readiness gating: %v", label, phase)
		}
	}
}

func TestResilienceStackReconcileReportsAndResumesPartialRecovery(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.failStart = "com.rencrow.qdrant"
	core := findResilienceStackService(t, plan, "com.rencrow.core")
	operator.states[core.Label] = resilienceStackSnapshot{Loaded: false, Running: false}

	result, err, stateStore := reconcileResilienceStackForTest(t, plan, 0, false, resilienceStackState{CorePID: 42}, operator)
	if err == nil {
		t.Fatal("expected the injected start failure")
	}
	if result.Action != "partial" || !reflect.DeepEqual(result.Started, []string{"com.rencrow.redis"}) {
		t.Fatalf("partial recovery was not reported accurately: result=%+v err=%v", result, err)
	}
	if operator.mutationCount() == 0 || containsEvent(operator.events, "start:com.rencrow.portal") {
		t.Fatalf("recovery continued after a service failed to start: %v", operator.events)
	}
	if stateStore.state.PendingPlanSHA256 == "" {
		t.Fatal("partial recovery did not persist its pending-plan receipt")
	}

	stopCount := countEventsWithPrefix(operator.events, "stop:")
	operator.failStart = ""
	resumed, err := reconcileResilienceStack(context.Background(), plan, 0, false, stateStore.state, operator, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Action != "restarted" || stateStore.state.PendingPlanSHA256 != "" {
		t.Fatalf("pending recovery did not complete and clear its receipt: result=%+v state=%+v", resumed, stateStore.state)
	}
	if got := countEventsWithPrefix(operator.events, "stop:"); got != stopCount {
		t.Fatalf("resume repeated the stop phase: before=%d after=%d events=%v", stopCount, got, operator.events)
	}
}

func TestResilienceStackReconcilePersistsStopCursorAndResumesRemainingServices(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.failStop = "com.rencrow.qdrant"
	result, err, stateStore := reconcileResilienceStackForTest(t, plan, 0, false, resilienceStackState{CorePID: 41}, operator)
	if err == nil || result.Action != "partial" || stateStore.state.PendingPlanSHA256 == "" {
		t.Fatalf("stop failure did not leave an actionable receipt: result=%+v state=%+v err=%v", result, stateStore.state, err)
	}
	operator.failStop = ""
	resumed, err := reconcileResilienceStack(context.Background(), plan, 0, false, stateStore.state, operator, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Action != "restarted" || stateStore.state.PendingPlanSHA256 != "" {
		t.Fatalf("partial stop receipt did not complete: result=%+v state=%+v", resumed, stateStore.state)
	}
	if got := countEventsWithPrefix(operator.events, "stop:"); got != len(plan.Services)+1 {
		t.Fatalf("resume did not stop the complete graph after one failed attempt: want=%d got=%d events=%v", len(plan.Services)+1, got, operator.events)
	}
	for _, service := range plan.Services {
		want := 1
		if service.Label == "com.rencrow.qdrant" {
			want = 2 // One failed Bootout attempt, then one successful retry.
		}
		if got := countExactEvents(operator.events, "stop:"+service.Label); got != want {
			t.Errorf("stop for %s occurred %d times, want %d: %v", service.Label, got, want, operator.events)
		}
	}
}

func TestResilienceStackReconcileRecoversBootoutSuccessBeforeCursorSave(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	store := &fakeResilienceStackStateStore{state: resilienceStackState{CorePID: 41}}
	operator.afterBootout = func(label string) {
		if label == "com.rencrow.portal" {
			store.failNextSave = true
		}
	}

	result, err := reconcileResilienceStack(context.Background(), plan, 0, false, store.state, operator, store)
	if err == nil || result.Action != "partial" {
		t.Fatalf("expected cursor persistence failure after successful bootout: result=%+v err=%v", result, err)
	}
	if store.state.PendingPhase != resilienceStackPhaseStop || store.state.PendingStopCursor != 0 {
		t.Fatalf("failed cursor save should leave the previous durable stop cursor: %+v", store.state)
	}
	if snapshot := operator.states["com.rencrow.portal"]; snapshot.Loaded || snapshot.Running {
		t.Fatalf("injected bootout did not take effect before save failure: %+v", snapshot)
	}

	operator.afterBootout = nil
	resumed, err := reconcileResilienceStack(context.Background(), plan, 0, false, store.state, operator, store)
	if err != nil || resumed.Action != "restarted" {
		t.Fatalf("recovery did not resume after cursor save failure: result=%+v err=%v", resumed, err)
	}
	if got := countExactEvents(operator.events, "stop:com.rencrow.portal"); got != 1 {
		t.Fatalf("crash-after-bootout repeated the stop action %d times: %v", got, operator.events)
	}
}

func TestResilienceStackReconcileRecoversBootstrapSuccessBeforeCursorSave(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	store := &fakeResilienceStackStateStore{state: resilienceStackState{CorePID: 41}}
	operator.afterBootstrap = func(label string) {
		if label == "com.rencrow.redis" {
			store.failNextSave = true
		}
	}

	result, err := reconcileResilienceStack(context.Background(), plan, 0, false, store.state, operator, store)
	if err == nil || result.Action != "partial" {
		t.Fatalf("expected cursor persistence failure after successful bootstrap: result=%+v err=%v", result, err)
	}
	if store.state.PendingPhase != resilienceStackPhaseStart || store.state.PendingStartCursor != 0 {
		t.Fatalf("failed cursor save should leave the previous durable start cursor: %+v", store.state)
	}
	startEvent := firstEventIndex(operator.events, "start:com.rencrow.redis")
	if startEvent < 0 {
		t.Fatalf("redis was not bootstrapped before injected cursor failure: %v", operator.events)
	}

	operator.afterBootstrap = nil
	resumed, err := reconcileResilienceStack(context.Background(), plan, 0, false, store.state, operator, store)
	if err != nil || resumed.Action != "restarted" {
		t.Fatalf("recovery did not resume after cursor save failure: result=%+v err=%v", resumed, err)
	}
	if got := countExactEvents(operator.events, "start:com.rencrow.redis"); got != 1 {
		t.Fatalf("crash-after-bootstrap repeated start %d times: %v", got, operator.events)
	}
	for _, event := range operator.events[startEvent+1:] {
		if strings.HasPrefix(event, "stop:") {
			t.Fatalf("start phase stopped a service after start had begun: %v", operator.events)
		}
	}
}

func TestResilienceStackReconcileRewindsCursorToUnreadyDependency(t *testing.T) {
	plan := resilienceStackFixture(t)
	planHash, err := resilienceStackPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.states["com.rencrow.core"] = resilienceStackSnapshot{}
	operator.failReadiness = "com.rencrow.qdrant"
	state := resilienceStackState{
		CorePID:            0,
		PendingPlanSHA256:  planHash,
		PendingPhase:       resilienceStackPhaseStart,
		PendingStopCursor:  len(plan.Services),
		PendingStartCursor: 3,
	}
	store := &fakeResilienceStackStateStore{state: state}

	result, err := reconcileResilienceStack(context.Background(), plan, 0, false, state, operator, store)
	if err == nil || result.Action != "partial" {
		t.Fatalf("unready dependency did not block start resume: result=%+v err=%v", result, err)
	}
	if containsEvent(operator.events, "start:com.rencrow.core") || operator.mutationCount() != 0 {
		t.Fatalf("CORE started while a declared dependency was not ready: %v", operator.events)
	}
	if store.state.PendingStartCursor != 1 || store.state.PendingPhase != resilienceStackPhaseStart {
		t.Fatalf("blocked start did not persist the qdrant recovery cursor: %+v", store.state)
	}
	operator.failReadiness = ""
	resumed, err := reconcileResilienceStack(context.Background(), plan, 0, false, store.state, operator, store)
	if err != nil || resumed.Action != "restarted" || store.state.PendingPlanSHA256 != "" {
		t.Fatalf("start cursor did not resume after dependency recovery: result=%+v state=%+v err=%v", resumed, store.state, err)
	}
}

func TestResilienceStackReconcileRewindsAfterPreviouslyStartedServiceDrops(t *testing.T) {
	plan := resilienceStackFixture(t)
	planHash, err := resilienceStackPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.states["com.rencrow.redis"] = resilienceStackSnapshot{}
	state := resilienceStackState{
		PendingPlanSHA256:  planHash,
		PendingPhase:       resilienceStackPhaseStart,
		PendingStopCursor:  len(plan.Services),
		PendingStartCursor: len(plan.Services),
	}
	store := &fakeResilienceStackStateStore{state: state}

	result, err := reconcileResilienceStack(context.Background(), plan, 0, false, state, operator, store)
	if err == nil || result.Action != "partial" {
		t.Fatalf("final verification did not report the dropped service: result=%+v err=%v", result, err)
	}
	if store.state.PendingStartCursor != 0 || store.state.PendingPhase != resilienceStackPhaseStart {
		t.Fatalf("final verification did not rewind to redis: %+v", store.state)
	}
	if operator.mutationCount() != 0 {
		t.Fatalf("cursor repair unexpectedly mutated launchd before the next pass: %v", operator.events)
	}

	resumed, err := reconcileResilienceStack(context.Background(), plan, 0, false, store.state, operator, store)
	if err != nil || resumed.Action != "restarted" || store.state.PendingPlanSHA256 != "" {
		t.Fatalf("rewound stack did not recover on the next pass: result=%+v state=%+v err=%v", resumed, store.state, err)
	}
	if countExactEvents(operator.events, "start:com.rencrow.redis") != 1 {
		t.Fatalf("redis was not restarted exactly once after its drop: %v", operator.events)
	}
}

func TestResilienceStackStartWaitsForLoadedServiceReadinessBeforeRestart(t *testing.T) {
	plan := resilienceStackFixture(t)
	planHash, err := resilienceStackPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.readinessFailures = map[string]int{"com.rencrow.redis": 1}
	state := resilienceStackState{
		PendingPlanSHA256:  planHash,
		PendingPhase:       resilienceStackPhaseStart,
		PendingStopCursor:  len(plan.Services),
		PendingStartCursor: 0,
	}
	store := &fakeResilienceStackStateStore{state: state}

	result, err := reconcileResilienceStack(context.Background(), plan, 0, false, state, operator, store)
	if err != nil || result.Action != "restarted" {
		t.Fatalf("loaded service did not recover within readiness grace: result=%+v err=%v", result, err)
	}
	if operator.mutationCount() != 0 {
		t.Fatalf("stack restarted a loaded service before its readiness grace elapsed: %v", operator.events)
	}
	if got := countExactEvents(operator.events, "ready:com.rencrow.redis"); got < 2 {
		t.Fatalf("loaded service readiness was not retried during its grace period: got %d checks, events=%v", got, operator.events)
	}
	if store.state.PendingPlanSHA256 != "" {
		t.Fatalf("completed recovery retained its pending receipt: %+v", store.state)
	}
}

func TestResilienceStackStartCancellationDuringReadinessGraceDoesNotRestart(t *testing.T) {
	plan := resilienceStackFixture(t)
	planHash, err := resilienceStackPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.failReadiness = "com.rencrow.redis"
	state := resilienceStackState{
		PendingPlanSHA256:  planHash,
		PendingPhase:       resilienceStackPhaseStart,
		PendingStopCursor:  len(plan.Services),
		PendingStartCursor: 0,
	}
	store := &fakeResilienceStackStateStore{state: state}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := reconcileResilienceStack(ctx, plan, 0, false, state, operator, store)
	if !errors.Is(err, context.Canceled) || result.Action != "partial" {
		t.Fatalf("readiness grace did not respect cancellation: result=%+v err=%v", result, err)
	}
	if operator.mutationCount() != 0 || store.state.PendingStartCursor != 0 {
		t.Fatalf("canceled readiness grace mutated launchd or advanced the cursor: events=%v state=%+v", operator.events, store.state)
	}
}

func TestResilienceStackStartRequiresRunningPIDBeforeSavingCursor(t *testing.T) {
	plan := resilienceStackFixture(t)
	planHash, err := resilienceStackPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.afterReadiness = func(label string) {
		if label == "com.rencrow.redis" {
			operator.states[label] = resilienceStackSnapshot{}
		}
	}
	state := resilienceStackState{
		PendingPlanSHA256:  planHash,
		PendingPhase:       resilienceStackPhaseStart,
		PendingStopCursor:  len(plan.Services),
		PendingStartCursor: 0,
	}
	store := &fakeResilienceStackStateStore{state: state}

	result, err := reconcileResilienceStack(context.Background(), plan, 0, false, state, operator, store)
	if err == nil || result.Action != "partial" {
		t.Fatalf("start cursor advanced after service stopped during readiness check: result=%+v err=%v", result, err)
	}
	if store.state.PendingStartCursor != 0 {
		t.Fatalf("start cursor remained advanced without a loaded process PID: %+v", store.state)
	}
	for _, saved := range store.saved {
		if saved.PendingStartCursor > 0 {
			t.Fatalf("start cursor was persisted before confirming a loaded process PID: saves=%+v", store.saved)
		}
	}
}

func TestResilienceStackReconcileConservativelyMigratesLegacyPendingReceipt(t *testing.T) {
	plan := resilienceStackFixture(t)
	planHash, err := resilienceStackPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.states["com.rencrow.portal"] = resilienceStackSnapshot{}
	operator.states["com.rencrow.core"] = resilienceStackSnapshot{}
	legacy := resilienceStackState{CorePID: 41, PendingPlanSHA256: planHash}
	store := &fakeResilienceStackStateStore{state: legacy}

	result, err := reconcileResilienceStack(context.Background(), plan, 0, false, legacy, operator, store)
	if err != nil || result.Action != "restarted" {
		t.Fatalf("legacy pending receipt was not conservatively completed: result=%+v err=%v", result, err)
	}
	if len(result.Started) != len(plan.Services) {
		t.Fatalf("legacy recovery did not restart the whole approved graph: started=%v", result.Started)
	}
	if store.state.PendingPlanSHA256 != "" || store.state.PendingPhase != "" || store.state.PendingStopCursor != 0 || store.state.PendingStartCursor != 0 {
		t.Fatalf("completed migration retained pending phase state: %+v", store.state)
	}
	mutations := operator.mutationCount()
	second, err := reconcileResilienceStack(context.Background(), plan, 0, false, store.state, operator, store)
	if err != nil || second.Action != "noop" || operator.mutationCount() != mutations {
		t.Fatalf("completed legacy migration repeated on next run: result=%+v err=%v events=%v", second, err, operator.events)
	}
}

func TestResilienceStackReconcileRejectsPendingStateForDifferentManifestBeforeInspection(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	state := resilienceStackState{CorePID: 42, PendingPlanSHA256: strings.Repeat("0", 64), PendingPhase: resilienceStackPhaseStop}
	if _, err := reconcileResilienceStack(context.Background(), plan, 0, false, state, operator, &fakeResilienceStackStateStore{state: state}); err == nil {
		t.Fatal("pending transaction for a different manifest was accepted")
	}
	if len(operator.events) != 0 {
		t.Fatalf("invalid pending state reached launchd inspection: %v", operator.events)
	}
}

func TestResilienceStackAcceptsUser501BoardAndTailnetJobs(t *testing.T) {
	plan := resilienceStackFixture(t)
	board := findResilienceStackService(t, plan, "com.rencrow.board-gateway.public")
	board.Domain = resilienceStackUser501
	var err error
	board.PlistPath, err = resilienceStackFixturePlistPath(board.Domain, board.Label)
	if err != nil {
		t.Fatal(err)
	}
	plan.Services[4] = board
	tailnet := resilienceStackService{
		Label:        "com.rencrow.tailnet-browser",
		Domain:       resilienceStackUser501,
		Dependencies: []string{"com.rencrow.core"},
		Readiness:    resilienceStackReadiness{Kind: "tcp", Address: "127.0.0.1:18891", TimeoutSeconds: 5},
	}
	tailnet.PlistPath, err = resilienceStackFixturePlistPath(tailnet.Domain, tailnet.Label)
	if err != nil {
		t.Fatal(err)
	}
	plan.Services = append(plan.Services, tailnet)
	if err := testResilienceStackPlan(plan); err != nil {
		t.Fatalf("existing user/501 board and tailnet jobs were rejected: %v", err)
	}
}

func TestResilienceStackCanonicalPlistPathRejectsUnsafeUserHomes(t *testing.T) {
	fixtureHome, err := resilienceStackFixtureHomePath()
	if err != nil {
		t.Fatal(err)
	}
	separator := string(filepath.Separator)
	for _, home := range []string{
		"relative",
		filepath.VolumeName(fixtureHome) + separator,
		separator + "Users" + separator + "example" + separator + ".." + separator + "other",
		separator + "Users" + separator + separator + "example",
	} {
		if _, err := canonicalResilienceStackPlistPathForHome(resilienceStackUser501, "com.rencrow.core", home); err == nil {
			t.Errorf("accepted unsafe user home %q", home)
		}
	}
	if _, err := canonicalResilienceStackPlistPathForHome(resilienceStackUser501, "../com.rencrow.core", fixtureHome); err == nil {
		t.Error("accepted a target label containing path traversal")
	}
}

func TestResilienceStackReconcileDoesNotRestartHealthyCoreForDependencyOutage(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.states["com.rencrow.redis"] = resilienceStackSnapshot{Loaded: true, Running: false, PID: 141}
	result, err, _ := reconcileResilienceStackForTest(t, plan, 0, false, resilienceStackState{CorePID: 42}, operator)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "noop" || result.Reason != "CORE healthy; dependencies degraded" {
		t.Fatalf("dependency outage should not trigger a whole-stack restart: %+v", result)
	}
	if !reflect.DeepEqual(result.Degraded, []string{"com.rencrow.redis"}) || operator.mutationCount() != 0 {
		t.Fatalf("degraded service was not reported without mutation: result=%+v events=%v", result, operator.events)
	}
}

type fakeResilienceStackOperator struct {
	states            map[string]resilienceStackSnapshot
	events            []string
	failStart         string
	failStop          string
	failReadiness     string
	readinessFailures map[string]int
	readinessChecks   map[string]int
	afterReadiness    func(string)
	afterBootout      func(string)
	afterBootstrap    func(string)
	nextPID           int
}

type fakeResilienceStackStateStore struct {
	state        resilienceStackState
	writes       int
	failNextSave bool
	saved        []resilienceStackState
}

func (s *fakeResilienceStackStateStore) Save(state resilienceStackState) error {
	s.writes++
	if s.failNextSave {
		s.failNextSave = false
		return errors.New("injected state save failure")
	}
	s.state = state
	s.saved = append(s.saved, state)
	return nil
}

func reconcileResilienceStackForTest(t *testing.T, plan resilienceStackPlan, uid int, maintenance bool, state resilienceStackState, operator resilienceStackOperator) (resilienceStackResult, error, *fakeResilienceStackStateStore) {
	t.Helper()
	store := &fakeResilienceStackStateStore{state: state}
	result, err := reconcileResilienceStack(context.Background(), plan, uid, maintenance, state, operator, store)
	return result, err, store
}

func newFakeResilienceStackOperator(plan resilienceStackPlan, corePID int) *fakeResilienceStackOperator {
	operator := &fakeResilienceStackOperator{
		states:  make(map[string]resilienceStackSnapshot, len(plan.Services)),
		nextPID: corePID + 200,
	}
	for _, service := range plan.Services {
		pid := corePID + 100
		if service.Label == "com.rencrow.core" {
			pid = corePID
		}
		operator.states[service.Label] = resilienceStackSnapshot{Loaded: true, Running: true, PID: pid}
	}
	return operator
}

func (f *fakeResilienceStackOperator) ValidateTarget(context.Context, resilienceStackService) error {
	return nil
}

func (f *fakeResilienceStackOperator) resilienceStackCanonicalPlistPath(domain, label string) (string, error) {
	return resilienceStackFixturePlistPath(domain, label)
}

func (f *fakeResilienceStackOperator) Inspect(_ context.Context, service resilienceStackService) (resilienceStackSnapshot, error) {
	f.events = append(f.events, "inspect:"+service.Label)
	return f.states[service.Label], nil
}

func (f *fakeResilienceStackOperator) Readiness(_ context.Context, service resilienceStackService) error {
	f.events = append(f.events, "ready:"+service.Label)
	if f.readinessChecks == nil {
		f.readinessChecks = make(map[string]int)
	}
	f.readinessChecks[service.Label]++
	if service.Readiness.Kind == "process" {
		if err := validateResilienceStackProcessSnapshot(f.states[service.Label]); err != nil {
			return err
		}
	}
	if f.readinessChecks[service.Label] <= f.readinessFailures[service.Label] {
		return errors.New("injected readiness grace failure")
	}
	if service.Label == f.failReadiness {
		if f.afterReadiness != nil {
			f.afterReadiness(service.Label)
		}
		return errors.New("injected readiness failure")
	}
	if f.afterReadiness != nil {
		f.afterReadiness(service.Label)
	}
	return nil
}

func (f *fakeResilienceStackOperator) Bootout(_ context.Context, service resilienceStackService) error {
	f.events = append(f.events, "stop:"+service.Label)
	if service.Label == f.failStop {
		return errors.New("injected bootout failure")
	}
	state := f.states[service.Label]
	state.Loaded = false
	state.Running = false
	state.PID = 0
	f.states[service.Label] = state
	if f.afterBootout != nil {
		f.afterBootout(service.Label)
	}
	return nil
}

func (f *fakeResilienceStackOperator) Bootstrap(_ context.Context, service resilienceStackService) error {
	f.events = append(f.events, "start:"+service.Label)
	if service.Label == f.failStart {
		return errors.New("injected bootstrap failure")
	}
	f.states[service.Label] = resilienceStackSnapshot{Loaded: true, Running: true, PID: f.nextPID}
	f.nextPID++
	if f.afterBootstrap != nil {
		f.afterBootstrap(service.Label)
	}
	return nil
}

func (f *fakeResilienceStackOperator) mutationCount() int {
	count := 0
	for _, event := range f.events {
		if strings.HasPrefix(event, "stop:") || strings.HasPrefix(event, "start:") {
			count++
		}
	}
	return count
}

func resilienceStackFixture(t *testing.T) resilienceStackPlan {
	t.Helper()
	corePlist, err := resilienceStackFixturePlistPath(resilienceStackUser501, "com.rencrow.core")
	if err != nil {
		t.Fatal(err)
	}
	return resilienceStackPlan{
		Schema:   "rencrow.launchd-stack/v1",
		Complete: true,
		Services: []resilienceStackService{
			{Label: "com.rencrow.redis", Domain: "system", PlistPath: "/Library/LaunchDaemons/com.rencrow.redis.plist", Readiness: resilienceStackReadiness{Kind: "tcp", Address: "127.0.0.1:6379", TimeoutSeconds: 5}},
			{Label: "com.rencrow.qdrant", Domain: "system", PlistPath: "/Library/LaunchDaemons/com.rencrow.qdrant.plist", Readiness: resilienceStackReadiness{Kind: "tcp", Address: "127.0.0.1:6333", TimeoutSeconds: 5}},
			{Label: "com.rencrow.llm-gateway", Domain: "system", PlistPath: "/Library/LaunchDaemons/com.rencrow.llm-gateway.plist", Readiness: resilienceStackReadiness{Kind: "tcp", Address: "127.0.0.1:8090", TimeoutSeconds: 5}},
			{Label: "com.rencrow.core", Domain: resilienceStackUser501, PlistPath: corePlist, Dependencies: []string{"com.rencrow.redis", "com.rencrow.qdrant", "com.rencrow.llm-gateway"}, Readiness: resilienceStackReadiness{Kind: "http", Address: "127.0.0.1:18790", Path: "/health/live", TimeoutSeconds: 5}},
			{Label: "com.rencrow.board-gateway.public", Domain: "system", PlistPath: "/Library/LaunchDaemons/com.rencrow.board-gateway.public.plist", Dependencies: []string{"com.rencrow.core"}, Readiness: resilienceStackReadiness{Kind: "tcp", Address: "127.0.0.1:18792", TimeoutSeconds: 5}},
			{Label: "com.rencrow.portal", Domain: "system", PlistPath: "/Library/LaunchDaemons/com.rencrow.portal.plist", Dependencies: []string{"com.rencrow.core"}, Readiness: resilienceStackReadiness{Kind: "http", Address: "127.0.0.1:18791", Path: "/health/ready", TimeoutSeconds: 5}},
		},
	}
}

func resilienceStackFixtureHomePath() (string, error) {
	return filepath.Abs(filepath.Join("testdata", "resilience-stack-user-home"))
}

func resilienceStackFixturePlistPath(domain, label string) (string, error) {
	home, err := resilienceStackFixtureHomePath()
	if err != nil {
		return "", err
	}
	return canonicalResilienceStackPlistPathForHome(domain, label, home)
}

func testResilienceStackPlan(plan resilienceStackPlan) error {
	return validateResilienceStackPlanWithResolver(plan, resilienceStackFixturePlistPath)
}

func findResilienceStackService(t *testing.T, plan resilienceStackPlan, label string) resilienceStackService {
	t.Helper()
	for _, service := range plan.Services {
		if service.Label == label {
			return service
		}
	}
	t.Fatalf("service %q missing from fixture", label)
	return resilienceStackService{}
}

func firstEventIndex(events []string, prefix string) int {
	for i, event := range events {
		if len(event) >= len(prefix) && event[:len(prefix)] == prefix {
			return i
		}
	}
	return -1
}

func containsEvent(events []string, want string) bool {
	for _, event := range events {
		if event == want {
			return true
		}
	}
	return false
}

func countEventsWithPrefix(events []string, prefix string) int {
	count := 0
	for _, event := range events {
		if strings.HasPrefix(event, prefix) {
			count++
		}
	}
	return count
}

func countExactEvents(events []string, want string) int {
	count := 0
	for _, event := range events {
		if event == want {
			count++
		}
	}
	return count
}
