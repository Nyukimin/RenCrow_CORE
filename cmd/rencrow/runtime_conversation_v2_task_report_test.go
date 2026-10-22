package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestResponseFirstReadbackAdmitsExecutesAndDeliversTaskReportThroughMio(t *testing.T) {
	ctx := context.Background()
	eventStore, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatalf("open EventStore: %v", err)
	}
	defer eventStore.Close()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatalf("open Task store: %v", err)
	}
	defer taskStore.Close()
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())

	provider := &responseFirstTaskReportProvider{}
	worker := &responseFirstTaskReportWorker{result: "The implementation follows the requested behavior."}
	publisher := newResponseFirstTestPublisher()
	timing, err := turntiming.NewBatchRecorderWithConfig(turntiming.RecorderConfig{BatchCapacity: 1, StampsPerTurn: 32, MemoryLimitBytes: 1 << 20})
	if err != nil {
		t.Fatalf("create response-first timing batch: %v", err)
	}
	readback := responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
		"shiro": {Provider: provider, SystemPrompt: "Trusted Shiro prompt"},
	}}
	runtime, err := newResponseFirstConversationRuntime(
		eventStore, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID, readback,
	)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	runtime.processing = &responseFirstV2Processing{timing: timing}
	if err := runtime.configureTaskReport(tasks, []string{"shiro"}, worker, nil); err != nil {
		t.Fatalf("configure Task/report runtime: %v", err)
	}

	accepted, err := runtime.Accept(ctx, responseFirstRequest(modulecore.NewSessionID(), "task-report-submit", "Please inspect this behavior."))
	if err != nil {
		t.Fatalf("Accept(): %v", err)
	}
	if err := runtime.Process(ctx, accepted); err != nil {
		t.Fatalf("Process(origin handoff): %v", err)
	}
	origin, found, err := eventStore.LoadCommittedOutcome(ctx, accepted.TurnID)
	if err != nil || !found || origin.ResultState != "awaiting_readback" {
		t.Fatalf("origin outcome state=%q found=%t err=%v, want committed delegation awaiting readback", origin.ResultState, found, err)
	}
	priorityProgressed, err := runtime.drainOneResponseFirstPriorityWork(ctx)
	if err != nil || !priorityProgressed {
		t.Fatalf("drainOneResponseFirstPriorityWork()=(progressed=%t, err=%v), want durable readback and Task admission", priorityProgressed, err)
	}
	executionProgressed, reportPending, err := runtime.drainOneResponseFirstExecutionWork(ctx)
	if err != nil || !executionProgressed || !reportPending {
		t.Fatalf("drainOneResponseFirstExecutionWork()=(progressed=%t reportPending=%t, err=%v), want completed worker report", executionProgressed, reportPending, err)
	}
	reportProgressed, retryRecovery, err := runtime.drainOneResponseFirstReportWork(ctx)
	if err != nil || !reportProgressed || retryRecovery {
		t.Fatalf("drainOneResponseFirstReportWork()=(progressed=%t retryRecovery=%t, err=%v), want accepted report reply", reportProgressed, retryRecovery, err)
	}

	if provider.readbackCalls != 1 || provider.reportReplyCalls != 1 || provider.delegateCalls != 1 {
		t.Fatalf("Mio/readback calls=(%d,%d,%d), want one delegation, one Shiro readback, and one report reply", provider.delegateCalls, provider.readbackCalls, provider.reportReplyCalls)
	}
	if worker.calls != 1 || len(worker.capabilities) != 0 {
		t.Fatalf("private worker calls=%d capabilities=%v, want one Shiro call with empty capabilities", worker.calls, worker.capabilities)
	}
	created, err := tasks.List(ctx, domaintask.Filter{Limit: 10})
	if err != nil || len(created) != 1 || created[0].Assignee != "shiro" || created[0].Status != domaintask.StatusSucceeded {
		t.Fatalf("Task owner list=%+v err=%v, want one completed Shiro Task", created, err)
	}
	if pending, err := tasks.ListPendingReports(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("pending Task reports=%+v err=%v, want delivered report", pending, err)
	}

	var reportInput modulecore.EventEnvelope
	for len(publisher.accepted) != 0 {
		event := <-publisher.accepted
		if event.EventType == "conversation.agent_report" {
			reportInput = event
		}
	}
	if reportInput.EventType != "conversation.agent_report" || reportInput.Payload["trusted_sender"] != "shiro" || reportInput.Payload["work_task_id"] != string(created[0].TaskID) {
		t.Fatalf("published accepted report input=%+v, want private report bound to the Shiro Task", reportInput)
	}
	committedReportReply, found, err := eventStore.LoadCommittedOutcome(ctx, reportInput.TurnID)
	if err != nil || !found {
		t.Fatalf("LoadCommittedOutcome(report reply) found=%t err=%v, want committed Mio reply", found, err)
	}
	if committedReportReply.ResultState != "completed" || !hasMioReportReply(committedReportReply.Events) {
		t.Fatalf("report reply result=%+v, want completed public Mio report reply", committedReportReply)
	}
	if err := timing.Freeze(); err != nil {
		t.Fatalf("freeze report timing batch: %v", err)
	}
	snapshot, err := timing.Snapshot()
	if err != nil {
		t.Fatalf("read report timing snapshot: %v", err)
	}
	if snapshot.TurnCount() != 1 {
		t.Fatalf("report timing turns=%d, want exactly one canonical report turn", snapshot.TurnCount())
	}
	timedTurn, ok := snapshot.Turn(0)
	if !ok {
		t.Fatal("report timing turn 0 is missing")
	}
	turnID, traceID, identified := timedTurn.Identity()
	if !identified || turnID != string(reportInput.TurnID) || traceID != string(reportInput.TraceID) {
		t.Fatalf("report timing identity=(%q,%q,%t), want persisted report turn=(%q,%q)", turnID, traceID, identified, reportInput.TurnID, reportInput.TraceID)
	}
	beginPoints, endPoints := make(map[uint16]int), make(map[uint16]int)
	acceptedPoints, foregroundCompletePoints := 0, 0
	var previousOffset int64
	for index := uint32(0); index < timedTurn.StampCount(); index++ {
		stamp, ok := timedTurn.Stamp(index)
		if !ok {
			t.Fatalf("report timing stamp %d is missing", index)
		}
		if stamp.OffsetNS < previousOffset {
			t.Fatalf("report timing offsets moved backwards: %d then %d", previousOffset, stamp.OffsetNS)
		}
		previousOffset = stamp.OffsetNS
		switch stamp.Phase {
		case turntiming.PhaseBegin:
			beginPoints[stamp.Point]++
		case turntiming.PhaseEnd:
			endPoints[stamp.Point]++
		case turntiming.PhasePoint:
			if stamp.Point == turntiming.PointV2Accepted {
				acceptedPoints++
			}
			if stamp.Point == turntiming.PointV2ForegroundComplete {
				foregroundCompletePoints++
			}
		}
	}
	if timedTurn.Dropped() != 0 || timedTurn.MissingSpanEnds() != 0 || !timedTurn.ForegroundDone() || timedTurn.PendingChildren() != 0 || snapshot.IsIncomplete() {
		t.Fatalf("report timing quality: dropped=%d missing_ends=%d foreground_done=%t pending_children=%d incomplete=%t", timedTurn.Dropped(), timedTurn.MissingSpanEnds(), timedTurn.ForegroundDone(), timedTurn.PendingChildren(), snapshot.IsIncomplete())
	}
	if acceptedPoints != 1 || foregroundCompletePoints != 1 {
		t.Fatalf("report accept/foreground points=(%d,%d), want one each", acceptedPoints, foregroundCompletePoints)
	}
	for _, point := range []uint16{
		turntiming.PointV2AsyncStart, turntiming.PointV2ReadView, turntiming.PointV2PromptComposition,
		turntiming.PointV2Generation,
		turntiming.PointV2Commit, turntiming.PointV2Publish,
	} {
		if beginPoints[point] != 1 || endPoints[point] != 1 {
			t.Errorf("report timing span %d begin/end=(%d,%d), want one complete span", point, beginPoints[point], endPoints[point])
		}
	}
	for _, event := range append([]modulecore.EventEnvelope{reportInput}, committedReportReply.Events...) {
		if key := findResponseFirstTimingBusinessField(event.Payload); key != "" {
			t.Errorf("business event %s contains diagnostic field %q", event.EventType, key)
		}
	}
}

func findResponseFirstTimingBusinessField(value any) string {
	diagnosticKeys := map[string]struct{}{
		"timing": {}, "performance": {}, "perf": {}, "duration_ms": {}, "latency_ms": {},
		"offset_ns": {}, "stamps": {}, "turn_index": {}, "missing_span_ends": {}, "dropped_stamps": {},
	}
	var inspect func(any) string
	inspect = func(candidate any) string {
		switch typed := candidate.(type) {
		case map[string]any:
			for key, nested := range typed {
				if _, found := diagnosticKeys[key]; found {
					return key
				}
				if nestedKey := inspect(nested); nestedKey != "" {
					return nestedKey
				}
			}
		case []any:
			for _, nested := range typed {
				if nestedKey := inspect(nested); nestedKey != "" {
					return nestedKey
				}
			}
		}
		return ""
	}
	return inspect(value)
}

func TestResponseFirstStartupRecoversAcceptedV2SessionsAndReplaysWithoutGeneration(t *testing.T) {
	ctx := context.Background()
	eventStore, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatalf("open EventStore: %v", err)
	}
	defer eventStore.Close()

	beforeRestartProvider := &responseFirstTaskReportProvider{directOnly: true}
	beforeRestart, err := newResponseFirstConversationRuntime(
		eventStore, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), beforeRestartProvider,
		newResponseFirstTestPublisher(), responseFirstTestOwnerID,
	)
	if err != nil {
		t.Fatalf("build pre-restart runtime: %v", err)
	}
	allowResponseFirstTestReservations(beforeRestart)
	acceptedTurns := []viewer.V2AcceptedTurn{
		{},
		{},
	}
	for index := range acceptedTurns {
		accepted, err := beforeRestart.Accept(ctx, responseFirstRequest(modulecore.NewSessionID(), fmt.Sprintf("recovery-submit-%d", index), "Recover this accepted turn."))
		if err != nil {
			t.Fatalf("Accept(%d): %v", index, err)
		}
		acceptedTurns[index] = accepted
	}
	if beforeRestartProvider.delegateCalls != 0 || beforeRestartProvider.reportReplyCalls != 0 {
		t.Fatalf("pre-restart generation calls=%d/%d, want accepted turns left pending", beforeRestartProvider.delegateCalls, beforeRestartProvider.reportReplyCalls)
	}

	afterRestartProvider := &responseFirstTaskReportProvider{directOnly: true}
	afterRestartPublisher := newResponseFirstTestPublisher()
	afterRestart, err := newResponseFirstConversationRuntime(
		eventStore, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), afterRestartProvider,
		afterRestartPublisher, responseFirstTestOwnerID,
	)
	if err != nil {
		t.Fatalf("build restarted runtime: %v", err)
	}
	if err := afterRestart.ResumeAllPendingAcceptedTurns(ctx); err != nil {
		t.Fatalf("ResumeAllPendingAcceptedTurns(): %v", err)
	}
	if afterRestartProvider.delegateCalls != len(acceptedTurns) {
		t.Fatalf("restarted Mio generation calls=%d, want %d accepted turns", afterRestartProvider.delegateCalls, len(acceptedTurns))
	}
	if err := afterRestart.ResumeAllPendingAcceptedTurns(ctx); err != nil {
		t.Fatalf("repeat ResumeAllPendingAcceptedTurns(): %v", err)
	}
	for _, accepted := range acceptedTurns {
		if err := afterRestart.Process(ctx, accepted); err != nil {
			t.Fatalf("Process(completed replay %s): %v", accepted.TurnID, err)
		}
	}
	if afterRestartProvider.delegateCalls != len(acceptedTurns) {
		t.Fatalf("Mio generated %d times after replay, want no extra generation beyond %d recovered turns", afterRestartProvider.delegateCalls, len(acceptedTurns))
	}
	for _, accepted := range acceptedTurns {
		if _, found, err := eventStore.LoadCommittedOutcome(ctx, accepted.TurnID); err != nil || !found {
			t.Fatalf("LoadCommittedOutcome(%s) found=%t err=%v, want durable recovered result", accepted.TurnID, found, err)
		}
	}
}

func TestResponseFirstProcessDrainUsesBoundedProcessOwnedContext(t *testing.T) {
	dispatcher := &readbackDrainSpy{calls: make(chan context.Context, 1)}
	runtime := &responseFirstConversationRuntime{dispatcher: dispatcher, dispatchWake: make(chan struct{}, 1)}
	stop := runtime.startResponseFirstDrain()
	if stop == nil {
		t.Fatal("startResponseFirstDrain() returned nil for configured dispatcher")
	}
	defer stop()
	select {
	case ctx := <-dispatcher.calls:
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("process drain dispatched without a bounded operation context")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("process-owned drain did not resume dispatch work")
	}
}

func TestResponseFirstProcessDrainStopWaitIsBounded(t *testing.T) {
	previousWait := responseFirstTaskDrainShutdownWait
	responseFirstTaskDrainShutdownWait = 25 * time.Millisecond
	t.Cleanup(func() { responseFirstTaskDrainShutdownWait = previousWait })

	dispatcher := &responseFirstUncancelableDrain{
		started: make(chan struct{}, 1), release: make(chan struct{}), returned: make(chan struct{}),
	}
	t.Cleanup(func() {
		select {
		case <-dispatcher.release:
		default:
			close(dispatcher.release)
		}
	})
	runtime := &responseFirstConversationRuntime{dispatcher: dispatcher, dispatchWake: make(chan struct{}, 1)}
	stop := runtime.startResponseFirstDrain()
	if stop == nil {
		t.Fatal("startResponseFirstDrain() returned nil for configured dispatcher")
	}
	select {
	case <-dispatcher.started:
	case <-time.After(2 * time.Second):
		t.Fatal("background drain did not enter the blocking dispatcher")
	}

	stopDone := make(chan struct{})
	go func() {
		stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("process drain stop blocked past its bounded shutdown wait")
	}
	select {
	case <-dispatcher.returned:
		t.Fatal("dispatcher returned before its release gate opened")
	default:
	}

	close(dispatcher.release)
	select {
	case <-dispatcher.returned:
	case <-time.After(time.Second):
		t.Fatal("background drain did not exit after the blocking dispatcher was released")
	}
	stop()
}

func TestResponseFirstProcessDrainPrioritizesReadbackAndReportWhileWorkerBlocks(t *testing.T) {
	ctx := context.Background()
	baseStore, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatalf("open EventStore: %v", err)
	}
	defer baseStore.Close()
	store := &responseFirstDrainTrackingStore{
		SQLiteStore:      baseStore,
		executionSources: make(chan eventstore.DelegatedExecutionSource, 2),
	}
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatalf("open Task store: %v", err)
	}
	defer taskStore.Close()
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	provider := &responseFirstDrainProgressProvider{readbackStarted: make(chan struct{}, 4)}
	worker := &responseFirstBlockingTaskReportWorker{
		started: make(chan context.Context, 1), finished: make(chan error, 1),
	}
	publisher := newResponseFirstTestPublisher()
	readback := responseFirstReadbackWiring{Assignees: map[string]agent.ReadbackAssigneeConfig{
		"shiro": {Provider: provider, SystemPrompt: "Trusted Shiro prompt"},
	}}
	runtime, err := newResponseFirstConversationRuntime(
		store, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), provider, publisher, responseFirstTestOwnerID, readback,
	)
	if err != nil {
		t.Fatalf("build response-first runtime: %v", err)
	}
	allowResponseFirstTestReservations(runtime)
	if err := runtime.configureTaskReport(tasks, []string{"shiro"}, worker, nil); err != nil {
		t.Fatalf("configure Task/report runtime: %v", err)
	}

	first, err := runtime.Accept(ctx, responseFirstRequest(modulecore.NewSessionID(), "drain-priority-first", "Review the first request."))
	if err != nil {
		t.Fatalf("Accept(first): %v", err)
	}
	if err := runtime.Process(ctx, first); err != nil {
		t.Fatalf("Process(first): %v", err)
	}
	select {
	case <-publisher.committed:
	case <-time.After(2 * time.Second):
		t.Fatal("first delegated turn was not published before background drain")
	}

	stop := runtime.startResponseFirstDrain()
	if stop == nil {
		t.Fatal("startResponseFirstDrain() returned nil for configured Task/report runtime")
	}
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()
	var workerCtx context.Context
	select {
	case workerCtx = <-worker.started:
	case <-time.After(3 * time.Second):
		t.Fatal("background drain did not start the delegated worker")
	}
	select {
	case <-provider.readbackStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first readback did not run before delegated worker generation")
	}

	second, err := runtime.Accept(ctx, responseFirstRequest(modulecore.NewSessionID(), "drain-priority-second", "Review the second request."))
	if err != nil {
		t.Fatalf("Accept(second): %v", err)
	}
	if err := runtime.Process(ctx, second); err != nil {
		t.Fatalf("Process(second): %v", err)
	}
	select {
	case <-provider.readbackStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("second readback did not progress while the first delegated worker was blocked")
	}
	select {
	case <-publisher.committed:
	case <-time.After(2 * time.Second):
		t.Fatal("second origin turn was not published")
	}

	var source eventstore.DelegatedExecutionSource
	select {
	case source = <-store.executionSources:
	case <-time.After(2 * time.Second):
		t.Fatal("delegated execution source was not loaded")
	}
	taskStates, runStates, err := tasks.ReadTaskRuns(ctx, []modulecore.TaskID{source.TaskID})
	if err != nil {
		t.Fatalf("ReadTaskRuns(): %v", err)
	}
	runs := runStates[source.TaskID]
	if _, found := taskStates[source.TaskID]; !found || len(runs) != 1 {
		t.Fatalf("Task/run state task=%t runs=%+v, want the worker's one active run", found, runs)
	}
	runID := runs[0].RunID
	report := conversation.ReportEnvelope{
		ReportID: modulecore.NewResponseID(), ReportRevision: 1,
		DelegationID: source.Handoff.DelegationID, DelegationRevision: source.Handoff.Revision,
		WorkTaskID: source.TaskID, WorkRunID: runID, Status: conversation.ReportStatusSucceeded,
		Summary: "Independent report lane fixture", Result: "The report lane can proceed independently.",
		EvidenceRefs: []modulecore.EvidenceID{}, ArtifactRefs: []modulecore.ArtifactID{}, Limitations: []string{}, Sender: "shiro",
	}
	if err := conversation.ValidateReportEnvelope(report, source.Handoff); err != nil {
		t.Fatalf("ValidateReportEnvelope(): %v", err)
	}
	if _, err := tasks.CompleteWithReport(ctx, source.TaskID, runID, "shiro", domaintask.StatusSucceeded, report); err != nil {
		t.Fatalf("CompleteWithReport(): %v", err)
	}
	deadlineTimer := time.NewTimer(4 * time.Second)
	defer deadlineTimer.Stop()
	reportReplyCommitted := false
	for !reportReplyCommitted {
		select {
		case bundle := <-publisher.committed:
			if hasMioReportReply(bundle.Events) {
				reportReplyCommitted = true
			}
		case <-deadlineTimer.C:
			t.Fatal("Mio report reply did not progress while delegated worker generation was blocked")
		}
	}
	deadline, ok := workerCtx.Deadline()
	if !ok {
		t.Fatal("delegated worker context has no bounded deadline")
	}
	if remaining := time.Until(deadline); remaining < 90*time.Second || remaining >= responseFirstTaskLeaseDuration {
		t.Fatalf("delegated worker deadline has %s remaining, want a bounded deadline below the 2m lease and above the old 30s drain timeout", remaining)
	}
	select {
	case err := <-worker.finished:
		t.Fatalf("worker finished before shutdown cancellation: %v", err)
	default:
	}
	if claims := store.executionClaimCount(); claims != 1 {
		t.Fatalf("delegated execution lease claim count=%d while worker was blocked, want exactly one", claims)
	}

	stopDone := make(chan struct{})
	go func() {
		stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		stopped = true
	case <-time.After(3 * time.Second):
		t.Fatal("process drain shutdown did not cancel and drain its blocked worker lane")
	}
	select {
	case <-worker.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("delegated worker did not observe process shutdown cancellation")
	}
	if claims := store.executionClaimCount(); claims != 1 {
		t.Fatalf("delegated execution lease claim count after shutdown=%d, want one claim and no duplicate", claims)
	}
}

type responseFirstTaskReportProvider struct {
	delegateCalls    int
	readbackCalls    int
	reportReplyCalls int
	directOnly       bool
}

func (p *responseFirstTaskReportProvider) Name() string { return "response-first-task-report-test" }

func (p *responseFirstTaskReportProvider) GenerateConversation(_ context.Context, scope conversation.ConversationScope, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	var content string
	switch scope.Purpose {
	case conversation.PurposeReadback:
		p.readbackCalls++
		content = `{"schema_version":"agent.readback.v1","status":"accepted","understood_goal":"Review the behavior","acknowledged_constraint_keys":["keep_scope"],"expected_output":"A concise report","text":"I understand and accept the assignment."}`
	case conversation.PurposeReportReply:
		p.reportReplyCalls++
		content = `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"Shiro's report supports the requested behavior."}}`
	case conversation.PurposeMioTurn:
		if p.directOnly {
			p.delegateCalls++
			content = `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"Recovered response."}}`
			break
		}
		p.delegateCalls++
		sourceIDs := []modulecore.MessageID{scope.InputID}
		encoded, err := json.Marshal(conversation.AgentEmission{
			SchemaVersion: "mio.turn.v1", Kind: conversation.EmissionKindDelegate,
			UserMessage: conversation.AgentUserMessage{To: "user", Text: "I will ask Shiro to review this."},
			Delegation: &conversation.AgentDelegation{
				To: "shiro", Goal: "Review the behavior", Instruction: "Inspect the request and return evidence.",
				Constraints:    []conversation.AgentConstraint{{Key: "keep_scope", Text: "Stay within the requested scope.", SourceMessageIDs: sourceIDs}},
				ExpectedOutput: "A concise report", SourceMessageIDs: sourceIDs,
			},
		})
		if err != nil {
			return llm.GenerateResponse{}, err
		}
		content = string(encoded)
	default:
		return llm.GenerateResponse{}, fmt.Errorf("unexpected Mio conversation purpose %q", scope.Purpose)
	}
	return llm.GenerateResponse{Content: content, RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 8}, nil
}

type responseFirstTaskReportWorker struct {
	result       string
	calls        int
	capabilities []string
}

func (w *responseFirstTaskReportWorker) ExecutePrivateWork(_ context.Context, _ conversation.TurnInput, capabilities []string) (string, error) {
	w.calls++
	w.capabilities = append([]string(nil), capabilities...)
	return w.result, nil
}

var _ conversationruntime.DelegatedPrivateWorker = (*responseFirstTaskReportWorker)(nil)

type responseFirstDrainProgressProvider struct {
	readbackStarted chan struct{}
}

func (p *responseFirstDrainProgressProvider) Name() string {
	return "response-first-drain-progress-test"
}

func (p *responseFirstDrainProgressProvider) GenerateConversation(_ context.Context, scope conversation.ConversationScope, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	var content string
	switch scope.Purpose {
	case conversation.PurposeMioTurn:
		sourceIDs := []modulecore.MessageID{scope.InputID}
		encoded, err := json.Marshal(conversation.AgentEmission{
			SchemaVersion: "mio.turn.v1", Kind: conversation.EmissionKindDelegate,
			UserMessage: conversation.AgentUserMessage{To: "user", Text: "I will ask Shiro to review this."},
			Delegation: &conversation.AgentDelegation{
				To: "shiro", Goal: "Review the behavior", Instruction: "Inspect the request and return evidence.",
				Constraints:    []conversation.AgentConstraint{{Key: "keep_scope", Text: "Stay within the requested scope.", SourceMessageIDs: sourceIDs}},
				ExpectedOutput: "A concise report", SourceMessageIDs: sourceIDs,
			},
		})
		if err != nil {
			return llm.GenerateResponse{}, err
		}
		content = string(encoded)
	case conversation.PurposeReadback:
		select {
		case p.readbackStarted <- struct{}{}:
		default:
		}
		content = `{"schema_version":"agent.readback.v1","status":"accepted","understood_goal":"Review the behavior","acknowledged_constraint_keys":["keep_scope"],"expected_output":"A concise report","text":"I understand and accept the assignment."}`
	case conversation.PurposeReportReply:
		content = `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"Shiro's report supports the requested behavior."}}`
	default:
		return llm.GenerateResponse{}, fmt.Errorf("unexpected response-first purpose %q", scope.Purpose)
	}
	return llm.GenerateResponse{Content: content, RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(), TokensUsed: 8}, nil
}

type responseFirstBlockingTaskReportWorker struct {
	started  chan context.Context
	finished chan error
}

func (w *responseFirstBlockingTaskReportWorker) ExecutePrivateWork(ctx context.Context, _ conversation.TurnInput, _ []string) (string, error) {
	w.started <- ctx
	<-ctx.Done()
	err := ctx.Err()
	w.finished <- err
	return "", err
}

type responseFirstUncancelableDrain struct {
	started  chan struct{}
	release  chan struct{}
	returned chan struct{}
}

func (d *responseFirstUncancelableDrain) DispatchOne(context.Context) (conversationruntime.DelegationDispatchResult, error) {
	d.started <- struct{}{}
	<-d.release
	close(d.returned)
	return conversationruntime.DelegationDispatchResult{}, nil
}

type responseFirstDrainTrackingStore struct {
	*eventstore.SQLiteStore
	mu               sync.Mutex
	executionClaims  int
	executionSources chan eventstore.DelegatedExecutionSource
}

func (s *responseFirstDrainTrackingStore) ClaimWork(ctx context.Context, input eventstore.WorkClaimInput) ([]eventstore.LeasedWorkItem, error) {
	claimed, err := s.SQLiteStore.ClaimWork(ctx, input)
	if input.Kind == conversationruntime.ExecuteDelegatedTaskWorkKind {
		s.mu.Lock()
		s.executionClaims += len(claimed)
		s.mu.Unlock()
	}
	return claimed, err
}

func (s *responseFirstDrainTrackingStore) LoadDelegatedExecution(ctx context.Context, input eventstore.DelegatedExecutionSourceInput) (eventstore.DelegatedExecutionSource, error) {
	source, err := s.SQLiteStore.LoadDelegatedExecution(ctx, input)
	if err == nil {
		select {
		case s.executionSources <- source:
		default:
		}
	}
	return source, err
}

func (s *responseFirstDrainTrackingStore) executionClaimCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executionClaims
}

func hasMioReportReply(events []modulecore.EventEnvelope) bool {
	for _, event := range events {
		if event.EventType == "agent.response" && event.Payload["kind"] == "report_reply" &&
			event.Payload["visibility"] == "public" && event.Payload["from"] == "mio" && event.Payload["text"] != "" {
			return true
		}
	}
	return false
}
