package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type turnTestArchiveStore struct {
	*mockArchiveSQLiteStore
	byID      map[modulecore.ThreadID]*domconv.ThreadSummary
	getCalls  int
	saveCalls int
}

type countingTurnSummarizer struct {
	calls int
}

type failingTurnEmbedder struct{}

func (failingTurnEmbedder) Embed(context.Context, string) ([]float32, error) {
	return nil, errors.New("embedder failure")
}

func managerTurnTestRequest(turnID, sessionID string) domconv.ConversationTurnRequest {
	return domconv.ConversationTurnRequest{
		TurnID:         modulecore.TurnID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalTurnID, turnID)),
		TraceID:        modulecore.TraceID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalTraceID, "trace:"+turnID)),
		RootTaskID:     modulecore.TaskID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalTaskID, "root:"+turnID)),
		UserMessageID:  modulecore.MessageID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalMessageID, "user:"+turnID)),
		AgentMessageID: modulecore.MessageID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalMessageID, "agent:"+turnID)),
		SessionID:      canonicalManagerTurnSessionIDForTest(sessionID), OwnerID: "owner-manager",
		UserMessage: "user-" + turnID, AgentMessage: "agent-" + turnID, AgentSpeaker: domconv.SpeakerMio,
	}
}

func canonicalManagerTurnIdentityForTest(kind modulecore.CanonicalIDType, source string) string {
	id, err := modulecore.NewMigrationID(kind, "real_manager_turn_test", "identity", source)
	if err != nil {
		panic(err)
	}
	return id
}

func canonicalManagerTurnSessionIDForTest(source string) string {
	if id := modulecore.SessionID(source); id.Validate() == nil {
		return source
	}
	id, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "real_manager_turn_test", "session_id", source)
	if err != nil {
		panic(err)
	}
	return id
}

func canonicalProjectionEventsForTest(sessionID string) []l1sqlite.L1MemoryEvent {
	threadID := modulecore.NewThreadID()
	threadSeq := modulecore.ThreadSeq(1)
	threadKind := modulecore.ThreadKindUserConversation
	start := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	return []l1sqlite.L1MemoryEvent{
		{ID: string(modulecore.NewEventID()), SessionID: sessionID, ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind, Speaker: domconv.SpeakerUser, Message: "hello", Meta: map[string]interface{}{"domain": "test"}, CreatedAt: start},
		{ID: string(modulecore.NewEventID()), SessionID: sessionID, ThreadID: threadID, ThreadSeq: threadSeq, ThreadKind: threadKind, Speaker: domconv.SpeakerMio, Message: "hi", Meta: map[string]interface{}{"domain": "test"}, CreatedAt: start.Add(time.Second)},
	}
}

func TestConversationThreadFromL1ProjectionRejectsLegacySessionID(t *testing.T) {
	events := canonicalProjectionEventsForTest("legacy-session")
	thread, err := conversationThreadFromL1Projection(events, domconv.ThreadActive)
	if !errors.Is(err, domconv.ErrConversationTurnInvalid) || thread != nil {
		t.Fatalf("legacy session projection thread=%v err=%v, want invalid", thread, err)
	}
}

func TestConversationThreadFromL1ProjectionRejectsUnownedStatus(t *testing.T) {
	events := canonicalProjectionEventsForTest(string(modulecore.NewSessionID()))
	thread, err := conversationThreadFromL1Projection(events, domconv.ThreadArchived)
	if !errors.Is(err, domconv.ErrConversationTurnInvalid) || thread != nil {
		t.Fatalf("archived projection thread=%v err=%v, want invalid", thread, err)
	}
}

func TestConversationThreadFromL1ProjectionRejectsMismatchedCanonicalTuple(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*l1sqlite.L1MemoryEvent)
	}{
		{name: "session", mutate: func(event *l1sqlite.L1MemoryEvent) { event.SessionID = string(modulecore.NewSessionID()) }},
		{name: "thread id", mutate: func(event *l1sqlite.L1MemoryEvent) { event.ThreadID = modulecore.NewThreadID() }},
		{name: "thread sequence", mutate: func(event *l1sqlite.L1MemoryEvent) { event.ThreadSeq = 2 }},
		{name: "thread kind", mutate: func(event *l1sqlite.L1MemoryEvent) { event.ThreadKind = modulecore.ThreadKindIdleChat }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			events := canonicalProjectionEventsForTest(string(modulecore.NewSessionID()))
			testCase.mutate(&events[1])
			thread, err := conversationThreadFromL1Projection(events, domconv.ThreadActive)
			if !errors.Is(err, domconv.ErrConversationTurnInvalid) || thread != nil {
				t.Fatalf("mismatched projection thread=%v err=%v, want invalid", thread, err)
			}
		})
	}
}

func (s *countingTurnSummarizer) Summarize(_ context.Context, _ *domconv.Thread) (domconv.SummaryResidual, error) {
	s.calls++
	return domconv.SummaryResidual{Summary: "boundary summary", Keywords: []string{"boundary", "thread", "archive"}, Provider: "test"}, nil
}

func newTurnTestArchiveStore() *turnTestArchiveStore {
	return &turnTestArchiveStore{mockArchiveSQLiteStore: &mockArchiveSQLiteStore{}, byID: make(map[modulecore.ThreadID]*domconv.ThreadSummary)}
}

func (s *turnTestArchiveStore) GetThreadSummary(_ context.Context, threadID modulecore.ThreadID) (*domconv.ThreadSummary, error) {
	s.getCalls++
	if summary := s.byID[threadID]; summary != nil {
		return summary, nil
	}
	return nil, domconv.ErrThreadNotFound
}

func (s *turnTestArchiveStore) SaveThreadSummaryWithReceipt(_ context.Context, summary *domconv.ThreadSummary, receipt *domconv.ThreadSummaryReceipt) error {
	s.saveCalls++
	copySummary := *summary
	copySummary.Receipt = receipt
	s.byID[summary.ThreadID] = &copySummary
	return nil
}

func TestRealConversationManagerCommitConversationTurnProjectsRedisIdempotently(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	redis := newMockRedisStore()
	manager := &RealConversationManager{
		redisStore: redis,
		l1Store:    l1,
	}
	request := managerTurnTestRequest("turn-manager-redis", "session-manager-redis")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	first, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || first.Status != domconv.ConversationTurnCompleted {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || !second.IdempotentReplay || second.Status != domconv.ConversationTurnCompleted {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	if len(redis.sessions) != 1 || len(redis.threads) != 1 {
		t.Fatalf("redis sessions=%d threads=%d, want one overwrite projection", len(redis.sessions), len(redis.threads))
	}
	thread := redis.threads[first.ThreadID]
	if thread == nil || len(thread.Turns) != 2 || thread.Turns[0].Msg != request.UserMessage || thread.Turns[1].Msg != request.AgentMessage {
		t.Fatalf("redis thread=%+v", thread)
	}
}

func TestRealConversationManagerThreadFollowerArchivesBoundaryAndReplaysWithoutSummarizer(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	archive := newTurnTestArchiveStore()
	redis := newMockRedisStore()
	summarizer := &countingTurnSummarizer{}
	manager := &RealConversationManager{
		redisStore:    redis,
		l1Store:       l1,
		archiveStore:  archive,
		vectordbStore: &mockVectorDBStore{},
		summarizer:    summarizer,
	}
	seed := managerTurnTestRequest("turn-manager-seed", "session-manager-boundary")
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	request := managerTurnTestRequest("turn-manager-boundary", seed.SessionID)
	request.Boundary = true
	request.BoundaryReason = "new topic"
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
	result, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || result.Status != domconv.ConversationTurnCompleted || result.ClosedThreadID == "" {
		t.Fatalf("boundary result=%+v err=%v", result, err)
	}
	if result.ThreadID == "" || result.ThreadSeq != 2 || result.ThreadKind != domconv.ThreadKindUserConversation || result.ClosedThreadSeq != 1 || result.ClosedThreadKind != domconv.ThreadKindUserConversation {
		t.Fatalf("boundary tuple=%q/%d/%q closed=%q/%d/%q", result.ThreadID, result.ThreadSeq, result.ThreadKind, result.ClosedThreadID, result.ClosedThreadSeq, result.ClosedThreadKind)
	}
	if summarizer.calls != 1 {
		t.Fatalf("summarizer calls=%d, want one", summarizer.calls)
	}
	archived := archive.byID[result.ClosedThreadID]
	if archived == nil || archived.Receipt == nil || archived.Receipt.SourceTurnCount != 2 || archived.SessionID != seed.SessionID || archived.ThreadID != result.ClosedThreadID || archived.ThreadSeq != result.ClosedThreadSeq || archived.ThreadKind != result.ClosedThreadKind {
		t.Fatalf("archived=%+v", archived)
	}
	if _, ok := redis.threads[result.ClosedThreadID]; ok {
		t.Fatalf("closed Redis thread was not cleaned up")
	}
	replay, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || !replay.IdempotentReplay {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if summarizer.calls != 1 {
		t.Fatalf("replay summarizer calls=%d, want unchanged", summarizer.calls)
	}
}

func TestRealConversationManagerRejectsTargetOnLegacyL1Store(t *testing.T) {
	manager := &RealConversationManager{l1Store: &mockL1Store{}}
	request := managerTurnTestRequest("turn-manager-legacy", "session-manager-legacy")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	result, err := manager.CommitConversationTurn(context.Background(), request)
	if !errors.Is(err, domconv.ErrConversationTurnUnavailable) || result.Status != domconv.ConversationTurnFailed || result.ErrorCode != domconv.ConversationTurnErrorUnavailable {
		t.Fatalf("legacy target result=%+v err=%v", result, err)
	}
}

func TestRealConversationManagerDrainGlobalFailureRetriesAcrossRestartAndTerminalizes(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	request := managerTurnTestRequest("turn-manager-drain-retry", "session-manager-drain-retry")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := l1.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("commit: %v", err)
	}
	manager := &RealConversationManager{l1Store: l1}
	for attempt := 1; attempt <= domconv.ConversationTurnMaxOutboxAttempts; attempt++ {
		if err := manager.DrainConversationTurnOutbox(ctx, 1); !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
			t.Fatalf("drain attempt %d error=%v, want unavailable", attempt, err)
		}
		receipt, err := l1.GetConversationTurnReceipt(ctx, string(request.TurnID))
		if err != nil {
			t.Fatalf("receipt attempt %d: %v", attempt, err)
		}
		if attempt < domconv.ConversationTurnMaxOutboxAttempts && receipt.Status != domconv.ConversationTurnPartial {
			t.Fatalf("receipt attempt %d=%+v, want partial", attempt, receipt)
		}
	}
	receipt, err := l1.GetConversationTurnReceipt(ctx, string(request.TurnID))
	if err != nil {
		t.Fatalf("terminal receipt: %v", err)
	}
	if receipt.Status != domconv.ConversationTurnFailed || receipt.ErrorCode != domconv.ConversationTurnErrorUnavailable {
		t.Fatalf("terminal receipt=%+v, want fixed failed unavailable", receipt)
	}
	if err := manager.DrainConversationTurnOutbox(ctx, 1); err != nil {
		t.Fatalf("terminal drain should be idle: %v", err)
	}
}

func TestRealConversationManagerDrainFailureAdvancesToNextKey(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	requests := []domconv.ConversationTurnRequest{
		managerTurnTestRequest("turn-manager-drain-first", "session-manager-drain-first"),
		managerTurnTestRequest("turn-manager-drain-second", "session-manager-drain-second"),
	}
	for i := range requests {
		requests[i].Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
		if _, err := l1.CommitConversationTurn(ctx, requests[i]); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	manager := &RealConversationManager{l1Store: l1}
	if err := manager.DrainConversationTurnOutbox(ctx, 2); !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("drain error=%v, want unavailable", err)
	}
	for i := range requests {
		claim, err := l1.ClaimConversationTurnOutbox(ctx, string(requests[i].TurnID), time.Now().UTC(), time.Minute)
		if err != nil || claim == nil || claim.Attempts != 2 {
			t.Fatalf("claim %d=%+v err=%v, want second attempt after one drain failure", i, claim, err)
		}
	}
}

func TestRealConversationManagerDrainFailureAdvancesWithinSameTurn(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	seed := managerTurnTestRequest("turn-manager-drain-same-seed", "session-manager-drain-same")
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	request := managerTurnTestRequest("turn-manager-drain-same-boundary", seed.SessionID)
	request.Boundary = true
	request.BoundaryReason = "drain sibling targets"
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
	if _, err := l1.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("boundary commit: %v", err)
	}
	manager := &RealConversationManager{l1Store: l1}
	if err := manager.DrainConversationTurnOutbox(ctx, 2); !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("drain error=%v, want unavailable", err)
	}
	for _, target := range request.Targets {
		claim, err := l1.ClaimConversationTurnOutbox(ctx, string(request.TurnID), time.Now().UTC(), time.Minute)
		if err != nil || claim == nil || claim.Target != string(target) || claim.Attempts != 2 {
			t.Fatalf("target %s claim=%+v err=%v, want one failure then second attempt", target, claim, err)
		}
	}
}

func TestRealConversationManagerDrainCapsLimitAt100(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	requests := make([]domconv.ConversationTurnRequest, 101)
	for i := range requests {
		requests[i] = managerTurnTestRequest(fmt.Sprintf("turn-manager-drain-limit-%03d", i), fmt.Sprintf("session-manager-drain-limit-%03d", i))
		requests[i].Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
		if _, err := l1.CommitConversationTurn(ctx, requests[i]); err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
	}
	manager := &RealConversationManager{l1Store: l1}
	if err := manager.DrainConversationTurnOutbox(ctx, 101); !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("drain error=%v, want unavailable", err)
	}
	claim, err := l1.ClaimConversationTurnOutbox(ctx, string(requests[100].TurnID), time.Now().UTC(), time.Minute)
	if err != nil || claim == nil || claim.Attempts != 1 {
		t.Fatalf("capped row claim=%+v err=%v, want untouched first attempt", claim, err)
	}
}

func TestRealConversationManagerDelayedRedisReplayUsesLatestActiveProjection(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	first := managerTurnTestRequest("turn-manager-delayed-first", "session-manager-delayed")
	first.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	firstResult, err := l1.CommitConversationTurn(ctx, first)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	second := managerTurnTestRequest("turn-manager-delayed-second", first.SessionID)
	secondResult, err := l1.CommitConversationTurn(ctx, second)
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if firstResult.ThreadID != secondResult.ThreadID {
		t.Fatalf("thread advanced unexpectedly: first=%s second=%s", firstResult.ThreadID, secondResult.ThreadID)
	}
	redis := newMockRedisStore()
	manager := &RealConversationManager{l1Store: l1, redisStore: redis}
	if err := manager.DrainConversationTurnOutbox(ctx, 1); err != nil {
		t.Fatalf("delayed drain: %v", err)
	}
	thread := redis.threads[secondResult.ThreadID]
	if thread == nil || len(thread.Turns) != 4 || thread.Turns[2].Msg != second.UserMessage || thread.Turns[3].Msg != second.AgentMessage {
		t.Fatalf("latest active projection=%+v, want both turns", thread)
	}
	session := redis.sessions[second.SessionID]
	if session == nil || session.LastThreadID != secondResult.ThreadID {
		t.Fatalf("projected session=%+v", session)
	}
}

func TestRealConversationManagerFollowerEmbedderFailureDoesNotArchive(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	archive := newTurnTestArchiveStore()
	manager := &RealConversationManager{
		redisStore:    newMockRedisStore(),
		l1Store:       l1,
		archiveStore:  archive,
		vectordbStore: &mockVectorDBStore{},
		embedder:      failingTurnEmbedder{},
		summarizer:    &countingTurnSummarizer{},
	}
	seed := managerTurnTestRequest("turn-manager-embed-seed", "session-manager-embed")
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	request := managerTurnTestRequest("turn-manager-embed-boundary", seed.SessionID)
	request.Boundary = true
	request.BoundaryReason = "embed failure"
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetThreadFollowers}
	result, err := manager.CommitConversationTurn(ctx, request)
	if !errors.Is(err, domconv.ErrConversationTurnUnavailable) || result.Status != domconv.ConversationTurnPartial || result.ErrorCode != domconv.ConversationTurnErrorUnavailable {
		t.Fatalf("embed failure result=%+v err=%v", result, err)
	}
	if archive.saveCalls != 0 || len(archive.byID) != 0 {
		t.Fatalf("embed failure archived summary: saves=%d byID=%v", archive.saveCalls, archive.byID)
	}
}

func TestDecodeConversationTurnOutboxPayloadIsCanonicalAndBound(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	request := managerTurnTestRequest("turn-payload-integrity", "session-payload-integrity")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := l1.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("commit: %v", err)
	}
	outbox, err := l1.ClaimConversationTurnOutbox(ctx, string(request.TurnID), time.Now().UTC(), time.Minute)
	if err != nil || outbox == nil {
		t.Fatalf("claim: %+v err=%v", outbox, err)
	}
	valid := *outbox
	if _, err := decodeConversationTurnOutboxPayload(&valid); err != nil {
		t.Fatalf("valid payload rejected: %v", err)
	}
	var changed conversationTurnOutboxPayload
	if err := json.Unmarshal([]byte(valid.PayloadJSON), &changed); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	changed.ThreadSeq++
	seqPayloadChanged := valid
	seqPayloadJSON, _ := json.Marshal(changed)
	seqPayloadChanged.PayloadJSON = string(seqPayloadJSON)
	if _, err := decodeConversationTurnOutboxPayload(&seqPayloadChanged); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("payload sequence mismatch error=%v, want invalid", err)
	}
	outboxSeqChanged := valid
	outboxSeqChanged.ThreadSeq++
	if _, err := decodeConversationTurnOutboxPayload(&outboxSeqChanged); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("outbox sequence mismatch error=%v, want invalid", err)
	}
	changed.ThreadSeq = valid.ThreadSeq
	changed.ThreadKind = domconv.ThreadKindIdleChat
	kindPayloadChanged := valid
	kindPayloadJSON, _ := json.Marshal(changed)
	kindPayloadChanged.PayloadJSON = string(kindPayloadJSON)
	if _, err := decodeConversationTurnOutboxPayload(&kindPayloadChanged); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("payload kind mismatch error=%v, want invalid", err)
	}
	outboxKindChanged := valid
	outboxKindChanged.ThreadKind = domconv.ThreadKindIdleChat
	if _, err := decodeConversationTurnOutboxPayload(&outboxKindChanged); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("outbox kind mismatch error=%v, want invalid", err)
	}
	unknown := valid
	unknown.PayloadJSON = strings.TrimSuffix(valid.PayloadJSON, "}") + `,"unknown":"x"}`
	if _, err := decodeConversationTurnOutboxPayload(&unknown); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("unknown payload error=%v, want invalid", err)
	}
	if err := json.Unmarshal([]byte(valid.PayloadJSON), &changed); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	changed.TraceID = modulecore.NewTraceID()
	traceChanged := valid
	traceJSON, _ := json.Marshal(changed)
	traceChanged.PayloadJSON = string(traceJSON)
	if _, err := decodeConversationTurnOutboxPayload(&traceChanged); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("trace binding error=%v, want invalid", err)
	}
	changed.TraceID = valid.TraceID
	changed.OwnerID = ""
	ownerChanged := valid
	ownerJSON, _ := json.Marshal(changed)
	ownerChanged.PayloadJSON = string(ownerJSON)
	if _, err := decodeConversationTurnOutboxPayload(&ownerChanged); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("owner binding error=%v, want invalid", err)
	}
	trailing := valid
	trailing.PayloadJSON = valid.PayloadJSON + "{}"
	if _, err := decodeConversationTurnOutboxPayload(&trailing); !errors.Is(err, domconv.ErrConversationTurnInvalid) {
		t.Fatalf("trailing payload error=%v, want invalid", err)
	}
}
