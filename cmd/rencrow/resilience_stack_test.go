package main

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"os/user"
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
	account, err := user.LookupId("501")
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(account.HomeDir, "Library", "LaunchAgents", "com.rencrow.core.plist")
	if core.Domain != resilienceStackUser501 || core.PlistPath != wantPath {
		t.Fatalf("CORE target = %s %s, want user/501 %s", core.Domain, core.PlistPath, wantPath)
	}
	if err := validateResilienceStackPlan(plan); err != nil {
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
	if want := []string{
		"stop:com.rencrow.portal",
		"stop:com.rencrow.board-gateway.public",
		"stop:com.rencrow.core",
		"stop:com.rencrow.llm-gateway",
		"stop:com.rencrow.qdrant",
		"stop:com.rencrow.redis",
		"start:com.rencrow.redis", "ready:com.rencrow.redis",
		"start:com.rencrow.qdrant", "ready:com.rencrow.qdrant",
		"start:com.rencrow.llm-gateway", "ready:com.rencrow.llm-gateway",
		"start:com.rencrow.core", "ready:com.rencrow.core",
		"start:com.rencrow.board-gateway.public", "ready:com.rencrow.board-gateway.public",
		"start:com.rencrow.portal", "ready:com.rencrow.portal",
		"inspect:com.rencrow.core",
	}; !reflect.DeepEqual(phase, want) {
		t.Fatalf("restart phase did not preserve stop/start/readiness order:\n got %v\nwant %v", phase, want)
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

func TestResilienceStackReconcilePersistsReceiptAfterPartialStopAndResumesWithoutRestopping(t *testing.T) {
	plan := resilienceStackFixture(t)
	operator := newFakeResilienceStackOperator(plan, 42)
	operator.failStop = "com.rencrow.qdrant"
	result, err, stateStore := reconcileResilienceStackForTest(t, plan, 0, false, resilienceStackState{CorePID: 41}, operator)
	if err == nil || result.Action != "partial" || stateStore.state.PendingPlanSHA256 == "" {
		t.Fatalf("stop failure did not leave an actionable receipt: result=%+v state=%+v err=%v", result, stateStore.state, err)
	}
	stopsBeforeResume := countEventsWithPrefix(operator.events, "stop:")
	operator.failStop = ""
	resumed, err := reconcileResilienceStack(context.Background(), plan, 0, false, stateStore.state, operator, stateStore)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Action != "restarted" || stateStore.state.PendingPlanSHA256 != "" {
		t.Fatalf("partial stop receipt did not complete: result=%+v state=%+v", resumed, stateStore.state)
	}
	if got := countEventsWithPrefix(operator.events, "stop:"); got != stopsBeforeResume {
		t.Fatalf("resume repeated stops already completed before failure: before=%d after=%d events=%v", stopsBeforeResume, got, operator.events)
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
	states    map[string]resilienceStackSnapshot
	events    []string
	failStart string
	failStop  string
	nextPID   int
}

type fakeResilienceStackStateStore struct {
	state  resilienceStackState
	writes int
}

func (s *fakeResilienceStackStateStore) Save(state resilienceStackState) error {
	s.state = state
	s.writes++
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
		nextPID: corePID + 100,
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

func (f *fakeResilienceStackOperator) Inspect(_ context.Context, service resilienceStackService) (resilienceStackSnapshot, error) {
	f.events = append(f.events, "inspect:"+service.Label)
	return f.states[service.Label], nil
}

func (f *fakeResilienceStackOperator) Readiness(_ context.Context, service resilienceStackService) error {
	f.events = append(f.events, "ready:"+service.Label)
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
	f.states[service.Label] = state
	return nil
}

func (f *fakeResilienceStackOperator) Bootstrap(_ context.Context, service resilienceStackService) error {
	f.events = append(f.events, "start:"+service.Label)
	if service.Label == f.failStart {
		return errors.New("injected bootstrap failure")
	}
	f.states[service.Label] = resilienceStackSnapshot{Loaded: true, Running: true, PID: f.nextPID}
	f.nextPID++
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
	account, err := user.LookupId("501")
	if err != nil {
		t.Fatal(err)
	}
	corePlist := filepath.Join(account.HomeDir, "Library", "LaunchAgents", "com.rencrow.core.plist")
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
