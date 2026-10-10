package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAcceptOPSInputExactReplayReadAndFirstConversationTurn(t *testing.T) {
	store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
	request := newOPSAcceptanceTestRequest("ops-request-exact", "ren", "  Keep this exact: 雪だるま ☃️\nsecond line  ")
	ctx := opsAcceptanceUserContext(t, request.RequestID, request.OwnerID)

	first, err := store.AcceptOPSInput(ctx, request)
	if err != nil {
		t.Fatalf("AcceptOPSInput: %v", err)
	}
	if first.AcceptanceSequence != 1 || first.DeclaredOrigin != domconv.AcceptedOPSInputOriginAutomation ||
		first.ThreadID != request.FirstThreadID || first.ThreadSeq != 1 || first.ThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("unexpected acceptance receipt: %+v", first)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("receipt validation: %v", err)
	}
	assertOPSAcceptanceRowCounts(t, store, 1, 1, 1, 1, 1)

	serializedReceipt, err := jsonMarshal(first)
	if err != nil {
		t.Fatalf("marshal receipt: %v", err)
	}
	if strings.Contains(string(serializedReceipt), request.RawMessage) || strings.Contains(string(serializedReceipt), "raw_message") {
		t.Fatal("acceptance receipt duplicated raw input text")
	}

	read, err := store.ReadAcceptedOPSInput(ctx, domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID})
	if err != nil || read.RawMessage != request.RawMessage || read.Receipt.RawRecordID != first.RawRecordID {
		t.Fatalf("exact user read = %+v err=%v", read, err)
	}
	derivedShiro, err := domaintool.DeriveAgentToolExecutionScope(ctx, request.RequestID, "shiro", "worker", "ops", true)
	if err != nil {
		t.Fatalf("derive Shiro read scope: %v", err)
	}
	shiroRead, err := store.ReadAcceptedOPSInput(derivedShiro, domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID})
	if err != nil || shiroRead.RawMessage != request.RawMessage {
		t.Fatalf("exact derived Shiro read = %+v err=%v", shiroRead, err)
	}

	retry := request
	retry.SessionID = modulecore.NewSessionID()
	retry.FirstThreadID = modulecore.NewThreadID()
	retry.TaskID = modulecore.NewTaskID()
	retry.TurnID = modulecore.NewTurnID()
	retry.TraceID = modulecore.NewTraceID()
	retry.UserMessageID = modulecore.NewMessageID()
	retry.AgentMessageID = modulecore.NewMessageID()
	retry.DeclaredOrigin = domconv.AcceptedOPSInputOriginAutomation
	replayed, err := store.AcceptOPSInput(ctx, retry)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if !replayed.IdempotentReplay || replayed.AcceptanceSequence != first.AcceptanceSequence ||
		replayed.SessionID != first.SessionID || replayed.ThreadID != first.ThreadID ||
		replayed.TaskID != first.TaskID || replayed.TurnID != first.TurnID || replayed.TraceID != first.TraceID ||
		replayed.UserMessageID != first.UserMessageID || replayed.AgentMessageID != first.AgentMessageID ||
		replayed.RawRecordID != first.RawRecordID || replayed.ManifestID != first.ManifestID {
		t.Fatalf("retry did not return original identities and sequence: first=%+v retry=%+v", first, replayed)
	}
	assertOPSAcceptanceRowCounts(t, store, 1, 1, 1, 1, 1)

	turnResult, err := store.CommitConversationTurn(ctx, domconv.ConversationTurnRequest{
		TurnID: first.TurnID, TraceID: first.TraceID, RootTaskID: first.TaskID,
		UserMessageID: first.UserMessageID, AgentMessageID: first.AgentMessageID,
		SessionID: string(first.SessionID), OwnerID: first.OwnerID, Domain: "ops",
		UserMessage: request.RawMessage, AgentMessage: "accepted", AgentSpeaker: domconv.SpeakerShiro,
	})
	if err != nil {
		t.Fatalf("normal first CommitConversationTurn: %v", err)
	}
	if turnResult.ThreadID != first.ThreadID || turnResult.ThreadSeq != 1 || turnResult.ThreadKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("normal turn changed the accepted first thread: receipt=%+v result=%+v", first, turnResult)
	}
	var activeThreadID string
	var activeSeq int64
	var activeKind string
	var activeCount int
	if err := store.db.QueryRowContext(context.Background(), `
SELECT thread_id, thread_seq, thread_kind, message_count
FROM conversation_active_thread WHERE session_id = ?`, first.SessionID).Scan(&activeThreadID, &activeSeq, &activeKind, &activeCount); err != nil {
		t.Fatalf("read active thread after normal turn: %v", err)
	}
	if activeThreadID != string(first.ThreadID) || activeSeq != 1 || activeKind != string(modulecore.ThreadKindUserConversation) || activeCount != 2 {
		t.Fatalf("active first thread after normal turn = %q/%d/%s/%d", activeThreadID, activeSeq, activeKind, activeCount)
	}
	assertOPSAcceptanceRowCounts(t, store, 1, 1, 1, 1, 1)
	afterTurn, err := store.ReadAcceptedOPSInput(ctx, domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID})
	if err != nil || afterTurn.RawMessage != request.RawMessage {
		t.Fatalf("exact read after normal turn = %+v err=%v", afterTurn, err)
	}
}

func TestAcceptedOPSInputHumanOriginPersistsExactRawAndConflictsWithAutomation(t *testing.T) {
	store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
	request := newOPSAcceptanceTestRequest("ops-request-human-origin", "ren", "  人の原文 🐦\n\tkeep bytes  ")
	request.DeclaredOrigin = domconv.AcceptedOPSInputOrigin("human")
	ctx := opsAcceptanceUserContext(t, request.RequestID, request.OwnerID)

	receipt, err := store.AcceptOPSInput(ctx, request)
	if err != nil {
		t.Fatalf("AcceptOPSInput Human: %v", err)
	}
	if receipt.DeclaredOrigin != domconv.AcceptedOPSInputOrigin("human") || receipt.AcceptanceSequence != 1 {
		t.Fatalf("Human acceptance receipt = %+v", receipt)
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("Human acceptance receipt validation: %v", err)
	}
	read, err := store.ReadAcceptedOPSInput(ctx, domconv.AcceptedOPSInputReadRequest{RequestID: request.RequestID, OwnerID: request.OwnerID})
	if err != nil || read.Receipt.DeclaredOrigin != domconv.AcceptedOPSInputOrigin("human") || read.RawMessage != request.RawMessage {
		t.Fatalf("Human exact read = %+v err=%v", read, err)
	}

	changedOrigin := request
	changedOrigin.DeclaredOrigin = domconv.AcceptedOPSInputOriginAutomation
	if _, err := store.AcceptOPSInput(ctx, changedOrigin); !errors.Is(err, domconv.ErrAcceptedOPSInputConflict) {
		t.Fatalf("same request with Automation origin error = %v, want conflict", err)
	}
	var storedOrigin string
	if err := store.db.QueryRowContext(context.Background(), `SELECT declared_origin FROM conversation_ops_input_acceptance WHERE owner_id = ? AND request_id = ?`, request.OwnerID, request.RequestID).Scan(&storedOrigin); err != nil {
		t.Fatalf("read persisted declared origin: %v", err)
	}
	if storedOrigin != string(domconv.AcceptedOPSInputOrigin("human")) {
		t.Fatalf("persisted declared origin=%q want human", storedOrigin)
	}
	assertOPSAcceptanceRowCounts(t, store, 1, 1, 1, 1, 1)
}

func TestAcceptOPSInputOwnerIsolationAndChangedPayloadConflict(t *testing.T) {
	store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
	firstRequest := newOPSAcceptanceTestRequest("ops-request-isolated", "ren", "first exact input")
	first, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, firstRequest.RequestID, firstRequest.OwnerID), firstRequest)
	if err != nil {
		t.Fatalf("first accept: %v", err)
	}

	changed := firstRequest
	changed.RawMessage = "changed exact input"
	if _, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, changed.RequestID, changed.OwnerID), changed); !errors.Is(err, domconv.ErrAcceptedOPSInputConflict) {
		t.Fatalf("changed payload error = %v, want conflict", err)
	}

	otherOwner := newOPSAcceptanceTestRequest(firstRequest.RequestID, "other-user", firstRequest.RawMessage)
	second, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, otherOwner.RequestID, otherOwner.OwnerID), otherOwner)
	if err != nil {
		t.Fatalf("same request ID for separate owner: %v", err)
	}
	if first.OwnerID == second.OwnerID || first.AcceptanceSequence == second.AcceptanceSequence {
		t.Fatalf("owner isolation failed: first=%+v second=%+v", first, second)
	}
	assertOPSAcceptanceRowCounts(t, store, 2, 2, 2, 2, 2)
}

func TestAcceptOPSInputRequiresExactAuthenticatedUserScope(t *testing.T) {
	store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
	request := newOPSAcceptanceTestRequest("ops-request-scope", "ren", "scope protected")

	wrongRequestScope := opsAcceptanceUserContext(t, "different-request", request.OwnerID)
	if _, err := store.AcceptOPSInput(wrongRequestScope, request); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("wrong request scope error = %v, want forbidden", err)
	}
	wrongOwnerScope := opsAcceptanceUserContext(t, request.RequestID, "other-user")
	if _, err := store.AcceptOPSInput(wrongOwnerScope, request); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("wrong owner scope error = %v, want forbidden", err)
	}
	wrongActor := request
	wrongActor.ActorID = "other-actor"
	if _, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), wrongActor); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("wrong actor identity error = %v, want forbidden", err)
	}
	derivedShiro, err := domaintool.DeriveAgentToolExecutionScope(
		opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), request.RequestID, "shiro", "worker", "ops", true,
	)
	if err != nil {
		t.Fatalf("derive Shiro scope: %v", err)
	}
	if _, err := store.AcceptOPSInput(derivedShiro, request); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("Shiro acceptance error = %v, want forbidden", err)
	}
	assertOPSAcceptanceRowCounts(t, store, 0, 0, 0, 0, 0)
}

func TestReadAcceptedOPSInputRejectsWrongOwnerAndUntrustedShiroScope(t *testing.T) {
	store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
	request := newOPSAcceptanceTestRequest("ops-request-read-scope", "ren", "private source")
	userCtx := opsAcceptanceUserContext(t, request.RequestID, request.OwnerID)
	if _, err := store.AcceptOPSInput(userCtx, request); err != nil {
		t.Fatalf("accept: %v", err)
	}
	if _, err := store.ReadAcceptedOPSInput(opsAcceptanceUserContext(t, request.RequestID, "other-user"), domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: request.OwnerID,
	}); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("wrong owner read error = %v, want forbidden", err)
	}
	wrongRequestScope := opsAcceptanceUserContext(t, "different-request", request.OwnerID)
	if _, err := store.ReadAcceptedOPSInput(wrongRequestScope, domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: request.OwnerID,
	}); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("wrong request read error = %v, want forbidden", err)
	}
	publicScope, err := domaintool.NewToolExecutionScope(
		request.RequestID, domaintool.ActorKindAgent, "mio", "", []string{domaintool.DataScopePublic},
		domaintool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("public agent scope: %v", err)
	}
	if _, err := store.ReadAcceptedOPSInput(domaintool.WithToolExecutionScope(context.Background(), publicScope), domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: request.OwnerID,
	}); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("public agent read error = %v, want forbidden", err)
	}
	publicUserScope, err := domaintool.NewToolExecutionScope(
		request.RequestID, domaintool.ActorKindUser, request.OwnerID, request.OwnerID,
		[]string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("public-only user scope: %v", err)
	}
	if _, err := store.ReadAcceptedOPSInput(domaintool.WithToolExecutionScope(context.Background(), publicUserScope), domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: request.OwnerID,
	}); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("public-only user read error = %v, want forbidden", err)
	}
	badShiro, err := domaintool.DeriveAgentToolExecutionScope(userCtx, request.RequestID, "shiro", "agent", "chat", false)
	if err != nil {
		t.Fatalf("derive non-OPS Shiro scope: %v", err)
	}
	if _, err := store.ReadAcceptedOPSInput(badShiro, domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: request.OwnerID,
	}); !errors.Is(err, domconv.ErrAcceptedOPSInputForbidden) {
		t.Fatalf("non-OPS Shiro read error = %v, want forbidden", err)
	}
}

func TestAcceptOPSInputConcurrentIndependentStoresConvergeAndReplayAfterReopen(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conversation-l1.db")
	storeA := newOPSAcceptanceTestStore(t, dbPath)
	storeB := newOPSAcceptanceTestStore(t, dbPath)
	firstRequest := newOPSAcceptanceTestRequest("ops-request-concurrent", "ren", "one durable input")
	secondRequest := firstRequest
	secondRequest.SessionID = modulecore.NewSessionID()
	secondRequest.FirstThreadID = modulecore.NewThreadID()
	secondRequest.TaskID = modulecore.NewTaskID()
	secondRequest.TurnID = modulecore.NewTurnID()
	secondRequest.TraceID = modulecore.NewTraceID()
	secondRequest.UserMessageID = modulecore.NewMessageID()
	secondRequest.AgentMessageID = modulecore.NewMessageID()
	stores := []*L1SQLiteStore{storeA, storeB}
	requests := []domconv.AcceptedOPSInputRequest{firstRequest, secondRequest}
	contexts := []context.Context{
		opsAcceptanceUserContext(t, firstRequest.RequestID, firstRequest.OwnerID),
		opsAcceptanceUserContext(t, secondRequest.RequestID, secondRequest.OwnerID),
	}
	start := make(chan struct{})
	results := make([]domconv.AcceptedOPSInputReceipt, 2)
	errorsFound := make([]error, 2)
	var wait sync.WaitGroup
	for index := range stores {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsFound[index] = stores[index].AcceptOPSInput(contexts[index], requests[index])
		}(index)
	}
	close(start)
	wait.Wait()
	for index, err := range errorsFound {
		if err != nil {
			t.Fatalf("concurrent request %d: %v", index, err)
		}
	}
	if !sameAcceptedOPSReceiptIdentity(results[0], results[1]) ||
		results[0].IdempotentReplay == results[1].IdempotentReplay {
		t.Fatalf("concurrent equal requests did not converge to one winner: %+v", results)
	}
	assertOPSAcceptanceRowCounts(t, storeA, 1, 1, 1, 1, 1)
	if err := storeA.Close(); err != nil {
		t.Fatalf("close first store before reopen: %v", err)
	}
	if err := storeB.Close(); err != nil {
		t.Fatalf("close second store before reopen: %v", err)
	}
	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen after concurrent acceptance: %v", err)
	}
	defer reopened.Close()
	retry := firstRequest
	retry.SessionID = modulecore.NewSessionID()
	retry.FirstThreadID = modulecore.NewThreadID()
	retry.TaskID = modulecore.NewTaskID()
	retry.TurnID = modulecore.NewTurnID()
	retry.TraceID = modulecore.NewTraceID()
	retry.UserMessageID = modulecore.NewMessageID()
	retry.AgentMessageID = modulecore.NewMessageID()
	replayed, err := reopened.AcceptOPSInput(contexts[0], retry)
	if err != nil || !replayed.IdempotentReplay || !sameAcceptedOPSReceiptIdentity(replayed, results[0]) {
		t.Fatalf("reopen Accept replay = %+v err=%v; original=%+v", replayed, err, results[0])
	}
	assertOPSAcceptanceRowCounts(t, reopened, 1, 1, 1, 1, 1)
}

func TestAcceptOPSInputConcurrentIndependentStoresMixedPayloadOnlyConflictsLoser(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conversation-l1.db")
	storeA := newOPSAcceptanceTestStore(t, dbPath)
	storeB := newOPSAcceptanceTestStore(t, dbPath)
	firstRequest := newOPSAcceptanceTestRequest("ops-request-mixed-race", "ren", "candidate A")
	secondRequest := firstRequest
	secondRequest.RawMessage = "candidate B"
	secondRequest.SessionID = modulecore.NewSessionID()
	secondRequest.FirstThreadID = modulecore.NewThreadID()
	secondRequest.TaskID = modulecore.NewTaskID()
	secondRequest.TurnID = modulecore.NewTurnID()
	secondRequest.TraceID = modulecore.NewTraceID()
	secondRequest.UserMessageID = modulecore.NewMessageID()
	secondRequest.AgentMessageID = modulecore.NewMessageID()
	stores := []*L1SQLiteStore{storeA, storeB}
	requests := []domconv.AcceptedOPSInputRequest{firstRequest, secondRequest}
	contexts := []context.Context{
		opsAcceptanceUserContext(t, firstRequest.RequestID, firstRequest.OwnerID),
		opsAcceptanceUserContext(t, secondRequest.RequestID, secondRequest.OwnerID),
	}
	start := make(chan struct{})
	results := make([]domconv.AcceptedOPSInputReceipt, 2)
	errorsFound := make([]error, 2)
	var wait sync.WaitGroup
	for index := range stores {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsFound[index] = stores[index].AcceptOPSInput(contexts[index], requests[index])
		}(index)
	}
	close(start)
	wait.Wait()
	winner := -1
	for index, err := range errorsFound {
		if err == nil {
			if winner != -1 {
				t.Fatalf("both changed payloads were accepted: %+v", results)
			}
			winner = index
			continue
		}
		if !errors.Is(err, domconv.ErrAcceptedOPSInputConflict) {
			t.Fatalf("mixed payload request %d error = %v, want conflict", index, err)
		}
	}
	if winner == -1 {
		t.Fatalf("no mixed-payload request won: errors=%v", errorsFound)
	}
	assertOPSAcceptanceRowCounts(t, storeA, 1, 1, 1, 1, 1)
	winnerRequest := requests[winner]
	replay := winnerRequest
	replay.SessionID = modulecore.NewSessionID()
	replay.FirstThreadID = modulecore.NewThreadID()
	replay.TaskID = modulecore.NewTaskID()
	replay.TurnID = modulecore.NewTurnID()
	replay.TraceID = modulecore.NewTraceID()
	replay.UserMessageID = modulecore.NewMessageID()
	replay.AgentMessageID = modulecore.NewMessageID()
	replayed, err := stores[winner].AcceptOPSInput(contexts[winner], replay)
	if err != nil || !replayed.IdempotentReplay || !sameAcceptedOPSReceiptIdentity(replayed, results[winner]) {
		t.Fatalf("mixed-payload winner retry = %+v err=%v; original=%+v", replayed, err, results[winner])
	}
	if err := storeA.Close(); err != nil {
		t.Fatalf("close first store before mixed-payload reopen: %v", err)
	}
	if err := storeB.Close(); err != nil {
		t.Fatalf("close second store before mixed-payload reopen: %v", err)
	}
	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen after mixed-payload race: %v", err)
	}
	defer reopened.Close()
	postReopenReplay, err := reopened.AcceptOPSInput(contexts[winner], replay)
	if err != nil || !postReopenReplay.IdempotentReplay || !sameAcceptedOPSReceiptIdentity(postReopenReplay, results[winner]) {
		t.Fatalf("mixed-payload reopen replay = %+v err=%v; original=%+v", postReopenReplay, err, results[winner])
	}
}

func TestReadAcceptedOPSInputSnapshotDoesNotMixConcurrentStateMutation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conversation-l1.db")
	reader := newOPSAcceptanceTestStore(t, dbPath)
	writer := newOPSAcceptanceTestStore(t, dbPath)
	request := newOPSAcceptanceTestRequest("ops-request-snapshot", "ren", "snapshot exact bytes")
	requestContext := opsAcceptanceUserContext(t, request.RequestID, request.OwnerID)
	receipt, err := reader.AcceptOPSInput(requestContext, request)
	if err != nil {
		t.Fatalf("accept before snapshot test: %v", err)
	}
	loadedReceipt := make(chan struct{})
	continueRead := make(chan struct{})
	type readResult struct {
		input domconv.AcceptedOPSInput
		found bool
		err   error
	}
	result := make(chan readResult, 1)
	go func() {
		input, found, err := readAcceptedOPSInputSnapshot(
			context.Background(), reader.readDB, request.OwnerID, request.RequestID,
			func() {
				close(loadedReceipt)
				<-continueRead
			},
		)
		result <- readResult{input: input, found: found, err: err}
	}()
	select {
	case <-loadedReceipt:
	case <-time.After(5 * time.Second):
		close(continueRead)
		t.Fatal("reader did not load its acceptance receipt")
	}
	_, mutationErr := writer.db.ExecContext(context.Background(), `
INSERT INTO l1_raw_state_event (
	state_event_id, raw_record_id, manifest_id, event_type, event_hash, owner_id, scope,
	request_id, actor_id, reason_code, payload_json, created_at
) VALUES (?, ?, ?, 'forget', ?, ?, ?, ?, ?, 'concurrent-restriction', '{}', ?)`,
		"ops-snapshot-forget", receipt.RawRecordID, receipt.ManifestID, strings.Repeat("f", 64),
		receipt.OwnerID, "user:"+receipt.OwnerID, receipt.RequestID, receipt.ActorID, receipt.AcceptedAt)
	close(continueRead)
	read := <-result
	if mutationErr != nil {
		t.Fatalf("commit concurrent restricted state: %v", mutationErr)
	}
	if read.err != nil || !read.found || read.input.RawMessage != request.RawMessage {
		t.Fatalf("snapshot read = %+v found=%t err=%v", read.input, read.found, read.err)
	}
	if _, err := reader.ReadAcceptedOPSInput(requestContext, domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: request.OwnerID,
	}); !errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
		t.Fatalf("read after committed restricted state = %v, want unavailable", err)
	}
}

func TestAcceptOPSInputCommitFailureRollsBackAndLeavesConnectionUsable(t *testing.T) {
	store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
	request := newOPSAcceptanceTestRequest("ops-request-commit-failure", "ren", "commit boundary rollback")
	requestContext, cancel := context.WithCancel(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID))
	defer cancel()
	// The owner seam cancels the request context and attempts the real COMMIT on
	// the reserved connection; cleanup must use its independent bounded context.
	store.opsAcceptanceCommitHook = func(commitContext context.Context, conn *sql.Conn) error {
		cancel()
		_, err := conn.ExecContext(commitContext, `COMMIT`)
		return err
	}
	if _, err := store.AcceptOPSInput(requestContext, request); !errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
		t.Fatalf("injected Commit failure = %v, want unavailable", err)
	}
	store.opsAcceptanceCommitHook = nil
	assertOPSAcceptanceRowCounts(t, store, 0, 0, 0, 0, 0)
	if err := store.db.PingContext(context.Background()); err != nil {
		t.Fatalf("connection pool unusable after Commit failure: %v", err)
	}
	retry := request
	retry.RequestID = "ops-request-commit-recovery"
	accepted, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, retry.RequestID, retry.OwnerID), retry)
	if err != nil || accepted.AcceptanceSequence != 1 {
		t.Fatalf("accept after Commit failure = %+v err=%v", accepted, err)
	}
	assertOPSAcceptanceRowCounts(t, store, 1, 1, 1, 1, 1)
}

func TestAcceptOPSInputRollbackFailureDiscardsConnection(t *testing.T) {
	store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
	request := newOPSAcceptanceTestRequest("ops-request-cleanup-failure", "ren", "rollback cleanup")
	if _, err := store.db.ExecContext(context.Background(), `
CREATE TRIGGER abort_ops_acceptance_for_cleanup_test
BEFORE INSERT ON conversation_ops_input_acceptance
WHEN NEW.request_id = 'ops-request-cleanup-failure'
BEGIN SELECT RAISE(ABORT, 'injected acceptance write failure'); END`); err != nil {
		t.Fatalf("install write failure trigger: %v", err)
	}
	rollbackCalls := 0
	store.opsAcceptanceRollbackHook = func(context.Context, *sql.Conn) error {
		rollbackCalls++
		return errors.New("injected rollback failure")
	}
	if _, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), request); !errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
		t.Fatalf("accept with failed rollback = %v, want unavailable", err)
	}
	if rollbackCalls != 1 {
		t.Fatalf("rollback cleanup calls = %d, want 1", rollbackCalls)
	}
	if got := store.db.Stats().OpenConnections; got != 0 {
		t.Fatalf("open connection count after unconfirmed rollback = %d, want discarded connection", got)
	}
	if err := store.db.PingContext(context.Background()); err != nil {
		t.Fatalf("reopen connection after discard: %v", err)
	}
	assertOPSAcceptanceRowCounts(t, store, 0, 0, 0, 0, 0)
	recovery := newOPSAcceptanceTestRequest("ops-request-cleanup-recovery", "ren", "after rollback failure")
	store.opsAcceptanceRollbackHook = nil
	if _, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, recovery.RequestID, recovery.OwnerID), recovery); err != nil {
		t.Fatalf("accept after discarded connection: %v", err)
	}
}

func TestAcceptOPSInputPanicRollsBackAndReleasesConnection(t *testing.T) {
	store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
	request := newOPSAcceptanceTestRequest("ops-request-panic-cleanup", "ren", "panic after writes")
	store.opsAcceptanceCommitHook = func(context.Context, *sql.Conn) error {
		panic("injected OPS transaction panic")
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = store.AcceptOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), request)
	}()
	store.opsAcceptanceCommitHook = nil
	if recovered != "injected OPS transaction panic" {
		t.Fatalf("transaction panic = %v, want injected panic", recovered)
	}
	assertOPSAcceptanceRowCounts(t, store, 0, 0, 0, 0, 0)
	if err := store.db.PingContext(context.Background()); err != nil {
		t.Fatalf("connection pool unusable after panic cleanup: %v", err)
	}
	recovery := newOPSAcceptanceTestRequest("ops-request-panic-recovery", "ren", "after panic cleanup")
	if accepted, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, recovery.RequestID, recovery.OwnerID), recovery); err != nil || accepted.AcceptanceSequence != 1 {
		t.Fatalf("accept after panic cleanup = %+v err=%v", accepted, err)
	}
}

func TestAcceptOPSInputUniqueIdentityCollisionIsConflictAndRollsBack(t *testing.T) {
	store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
	firstRequest := newOPSAcceptanceTestRequest("ops-request-unique-first", "ren", "first identity")
	first, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, firstRequest.RequestID, firstRequest.OwnerID), firstRequest)
	if err != nil {
		t.Fatalf("accept first identity: %v", err)
	}
	secondRequest := newOPSAcceptanceTestRequest("ops-request-unique-second", firstRequest.OwnerID, "second identity")
	secondRequest.AgentMessageID = first.AgentMessageID
	if _, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, secondRequest.RequestID, secondRequest.OwnerID), secondRequest); !errors.Is(err, domconv.ErrAcceptedOPSInputConflict) {
		t.Fatalf("duplicate identity error = %v, want conflict", err)
	}
	assertOPSAcceptanceRowCounts(t, store, 1, 1, 1, 1, 1)
}

func TestAcceptOPSInputLockedWriterIsUnavailableAndCanRecover(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conversation-l1.db")
	writer := newOPSAcceptanceTestStore(t, dbPath)
	store := newOPSAcceptanceTestStore(t, dbPath)
	if _, err := store.db.ExecContext(context.Background(), `PRAGMA busy_timeout = 25`); err != nil {
		t.Fatalf("set bounded test busy timeout: %v", err)
	}
	writerTx, err := writer.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin held writer: %v", err)
	}
	if _, err := writerTx.ExecContext(context.Background(), `UPDATE l1_schema_migrations SET version = version WHERE migration_name = ?`, opsAcceptanceMigrationName); err != nil {
		_ = writerTx.Rollback()
		t.Fatalf("hold SQLite write lock: %v", err)
	}
	request := newOPSAcceptanceTestRequest("ops-request-locked", "ren", "locked writer")
	if _, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), request); !errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
		_ = writerTx.Rollback()
		t.Fatalf("locked writer error = %v, want unavailable", err)
	}
	assertOPSAcceptanceRowCounts(t, store, 0, 0, 0, 0, 0)
	if err := writerTx.Rollback(); err != nil {
		t.Fatalf("release held writer: %v", err)
	}
	if _, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), request); err != nil {
		t.Fatalf("accept after lock release: %v", err)
	}
}

func TestAcceptOPSInputRollsBackEachOwnerWriteOnFailure(t *testing.T) {
	tests := []struct {
		name        string
		table       string
		triggerName string
	}{
		{name: "thread binding", table: "conversation_active_thread", triggerName: "abort_ops_thread"},
		{name: "raw manifest", table: "l1_raw_source_manifest", triggerName: "abort_ops_manifest"},
		{name: "raw record", table: "l1_raw_record", triggerName: "abort_ops_record"},
		{name: "raw state", table: "l1_raw_state_event", triggerName: "abort_ops_state"},
		{name: "acceptance receipt", table: "conversation_ops_input_acceptance", triggerName: "abort_ops_receipt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
			if _, err := store.db.ExecContext(context.Background(), fmt.Sprintf(
				`CREATE TRIGGER %s BEFORE INSERT ON %s BEGIN SELECT RAISE(ABORT, 'test rollback'); END`,
				test.triggerName, test.table,
			)); err != nil {
				t.Fatalf("install write failure trigger: %v", err)
			}
			request := newOPSAcceptanceTestRequest("ops-rollback-"+test.triggerName, "ren", "atomic write")
			if _, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), request); !errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
				t.Fatalf("failing write trigger error = %v, want unavailable", err)
			}
			assertOPSAcceptanceRowCounts(t, store, 0, 0, 0, 0, 0)
		})
	}
}

func TestReadAcceptedOPSInputFailsClosedOnReceiptRawMetadataHashOrStateCorruption(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *L1SQLiteStore, domconv.AcceptedOPSInputReceipt)
	}{
		{
			name: "receipt raw hash",
			corrupt: func(t *testing.T, store *L1SQLiteStore, receipt domconv.AcceptedOPSInputReceipt) {
				t.Helper()
				if _, err := store.db.ExecContext(context.Background(), `DROP TRIGGER trg_ops_input_acceptance_immutable_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(context.Background(), `UPDATE conversation_ops_input_acceptance SET raw_sha256 = ? WHERE owner_id = ? AND request_id = ?`, strings.Repeat("0", 64), receipt.OwnerID, receipt.RequestID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "receipt missing raw reference",
			corrupt: func(t *testing.T, store *L1SQLiteStore, receipt domconv.AcceptedOPSInputReceipt) {
				t.Helper()
				if _, err := store.db.ExecContext(context.Background(), `DROP TRIGGER trg_ops_input_acceptance_immutable_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(context.Background(), `UPDATE conversation_ops_input_acceptance SET raw_record_id = 'missing-raw-record' WHERE owner_id = ? AND request_id = ?`, receipt.OwnerID, receipt.RequestID); err != nil {
					t.Fatalf("set missing raw reference with current connection FK policy: %v", err)
				}
			},
		},
		{
			name: "receipt missing manifest reference",
			corrupt: func(t *testing.T, store *L1SQLiteStore, receipt domconv.AcceptedOPSInputReceipt) {
				t.Helper()
				if _, err := store.db.ExecContext(context.Background(), `DROP TRIGGER trg_ops_input_acceptance_immutable_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(context.Background(), `UPDATE conversation_ops_input_acceptance SET manifest_id = 'missing-manifest' WHERE owner_id = ? AND request_id = ?`, receipt.OwnerID, receipt.RequestID); err != nil {
					t.Fatalf("set missing manifest reference with current connection FK policy: %v", err)
				}
			},
		},
		{
			name: "raw metadata",
			corrupt: func(t *testing.T, store *L1SQLiteStore, receipt domconv.AcceptedOPSInputReceipt) {
				t.Helper()
				if _, err := store.db.ExecContext(context.Background(), `DROP TRIGGER trg_l1_raw_record_immutable_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(context.Background(), `UPDATE l1_raw_record SET source_type = 'changed' WHERE raw_record_id = ?`, receipt.RawRecordID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "raw owner and scope",
			corrupt: func(t *testing.T, store *L1SQLiteStore, receipt domconv.AcceptedOPSInputReceipt) {
				t.Helper()
				if _, err := store.db.ExecContext(context.Background(), `DROP TRIGGER trg_l1_raw_record_immutable_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(context.Background(), `UPDATE l1_raw_record SET owner_id = 'other-user', scope = 'user:other-user' WHERE raw_record_id = ?`, receipt.RawRecordID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "raw bytes hash",
			corrupt: func(t *testing.T, store *L1SQLiteStore, receipt domconv.AcceptedOPSInputReceipt) {
				t.Helper()
				if _, err := store.db.ExecContext(context.Background(), `DROP TRIGGER trg_l1_raw_record_immutable_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(context.Background(), `UPDATE l1_raw_record SET inline_payload = ? WHERE raw_record_id = ?`, []byte("changed"), receipt.RawRecordID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "manifest hash",
			corrupt: func(t *testing.T, store *L1SQLiteStore, receipt domconv.AcceptedOPSInputReceipt) {
				t.Helper()
				if _, err := store.db.ExecContext(context.Background(), `DROP TRIGGER trg_l1_raw_manifest_immutable_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := store.db.ExecContext(context.Background(), `UPDATE l1_raw_source_manifest SET manifest_sha256 = ? WHERE manifest_id = ?`, strings.Repeat("0", 64), receipt.ManifestID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unsupported later forget state event",
			corrupt: func(t *testing.T, store *L1SQLiteStore, receipt domconv.AcceptedOPSInputReceipt) {
				t.Helper()
				if _, err := store.db.ExecContext(context.Background(), `
INSERT INTO l1_raw_state_event (
	state_event_id, raw_record_id, manifest_id, event_type, event_hash, owner_id, scope,
	request_id, actor_id, reason_code, payload_json, created_at
) VALUES (?, ?, ?, 'forget', ?, ?, ?, ?, ?, 'test', '{}', ?)`,
					"tampered-state", receipt.RawRecordID, receipt.ManifestID, strings.Repeat("a", 64),
					receipt.OwnerID, "user:"+receipt.OwnerID, receipt.RequestID, receipt.ActorID, receipt.AcceptedAt); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newOPSAcceptanceTestStore(t, filepath.Join(t.TempDir(), "conversation-l1.db"))
			request := newOPSAcceptanceTestRequest("ops-corrupt-"+strings.ReplaceAll(test.name, " ", "-"), "ren", "corruption guarded")
			receipt, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), request)
			if err != nil {
				t.Fatalf("accept: %v", err)
			}
			test.corrupt(t, store, receipt)
			if _, err := store.ReadAcceptedOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), domconv.AcceptedOPSInputReadRequest{
				RequestID: request.RequestID, OwnerID: request.OwnerID,
			}); !errors.Is(err, domconv.ErrAcceptedOPSInputUnavailable) {
				t.Fatalf("corrupt read error = %v, want unavailable", err)
			}
		})
	}
}

func TestOPSInputAcceptanceSchemaMigrationPreservesRawAndRejectsUnknownVersion(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conversation-l1.db")
	store := newOPSAcceptanceTestStore(t, dbPath)
	var foreignKeysEnabled int
	if err := store.db.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeysEnabled); err != nil {
		t.Fatalf("read SQLite foreign-key policy: %v", err)
	}
	if foreignKeysEnabled != 0 {
		t.Fatalf("L1 SQLite foreign_keys = %d, want default-off policy to remain explicit", foreignKeysEnabled)
	}
	request := newOPSAcceptanceTestRequest("ops-reopen", "ren", "preserve through schema reopen")
	receipt, err := store.AcceptOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), request)
	if err != nil {
		t.Fatalf("accept before reopen: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}
	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	read, err := reopened.ReadAcceptedOPSInput(opsAcceptanceUserContext(t, request.RequestID, request.OwnerID), domconv.AcceptedOPSInputReadRequest{
		RequestID: request.RequestID, OwnerID: request.OwnerID,
	})
	if err != nil || read.RawMessage != request.RawMessage || read.Receipt.AcceptanceSequence != receipt.AcceptanceSequence {
		t.Fatalf("reopen read = %+v err=%v", read, err)
	}
	assertOPSAcceptanceRowCounts(t, reopened, 1, 1, 1, 1, 1)
	if _, err := reopened.db.ExecContext(context.Background(), `UPDATE l1_schema_migrations SET version = 99 WHERE migration_name = ?`, opsAcceptanceMigrationName); err != nil {
		t.Fatalf("set unknown migration version: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close before unknown version check: %v", err)
	}
	if _, err := NewL1SQLiteStore(dbPath); err == nil || !strings.Contains(err.Error(), "schema version") {
		t.Fatalf("unknown schema version error = %v, want fail-closed version error", err)
	}
}

func TestOPSInputAcceptanceSchemaMigrationAddsOnlyReceiptToExistingCommonRawData(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "conversation-l1.db")
	store := newOPSAcceptanceTestStore(t, dbPath)
	commonInput := commonRawTestInput([]domainmemory.CommonRawRecord{commonRawTestRecord("existing-record", []byte("existing CommonRaw data"))}, nil)
	if _, err := store.IntakeCommonRaw(commonRawTestContext(t, "existing-common-raw"), "existing-common-raw", commonRawTestOwner, commonRawTestOwner, commonInput); err != nil {
		t.Fatalf("existing CommonRaw intake: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `DROP TRIGGER trg_ops_input_acceptance_immutable_delete`); err != nil {
		t.Fatalf("drop acceptance delete trigger: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `DROP TRIGGER trg_ops_input_acceptance_immutable_update`); err != nil {
		t.Fatalf("drop acceptance update trigger: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `DROP TABLE conversation_ops_input_acceptance`); err != nil {
		t.Fatalf("drop new acceptance table to simulate pre-migration store: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `DELETE FROM l1_schema_migrations WHERE migration_name = ?`, opsAcceptanceMigrationName); err != nil {
		t.Fatalf("remove new migration marker: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close pre-migration store: %v", err)
	}
	reopened, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("apply additive migration: %v", err)
	}
	defer reopened.Close()
	var manifests, records, states int
	for query, target := range map[string]*int{
		`SELECT count(*) FROM l1_raw_source_manifest`: &manifests,
		`SELECT count(*) FROM l1_raw_record`:          &records,
		`SELECT count(*) FROM l1_raw_state_event`:     &states,
	} {
		if err := reopened.db.QueryRowContext(context.Background(), query).Scan(target); err != nil {
			t.Fatalf("preserved CommonRaw count %q: %v", query, err)
		}
	}
	if manifests != 1 || records != 1 || states != 1 {
		t.Fatalf("migration changed existing CommonRaw rows: manifests=%d records=%d states=%d", manifests, records, states)
	}
	assertOPSAcceptanceRowCounts(t, reopened, 0, 1, 1, 1, 0)
}

func newOPSAcceptanceTestStore(t *testing.T, dbPath string) *L1SQLiteStore {
	t.Helper()
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}

func newOPSAcceptanceTestRequest(requestID, ownerID, rawMessage string) domconv.AcceptedOPSInputRequest {
	return domconv.AcceptedOPSInputRequest{
		RequestID: requestID, OwnerID: ownerID, ActorID: ownerID,
		SessionID: modulecore.NewSessionID(), FirstThreadID: modulecore.NewThreadID(),
		TaskID: modulecore.NewTaskID(), TurnID: modulecore.NewTurnID(), TraceID: modulecore.NewTraceID(),
		UserMessageID: modulecore.NewMessageID(), AgentMessageID: modulecore.NewMessageID(),
		RawMessage: rawMessage,
	}
}

func sameAcceptedOPSReceiptIdentity(left, right domconv.AcceptedOPSInputReceipt) bool {
	return left.AcceptanceSequence == right.AcceptanceSequence &&
		left.RequestID == right.RequestID && left.OwnerID == right.OwnerID && left.ActorID == right.ActorID &&
		left.SessionID == right.SessionID && left.ThreadID == right.ThreadID && left.ThreadSeq == right.ThreadSeq &&
		left.ThreadKind == right.ThreadKind && left.TaskID == right.TaskID && left.TurnID == right.TurnID &&
		left.TraceID == right.TraceID && left.UserMessageID == right.UserMessageID && left.AgentMessageID == right.AgentMessageID &&
		left.DeclaredOrigin == right.DeclaredOrigin && left.PayloadSHA256 == right.PayloadSHA256 &&
		left.RawRecordID == right.RawRecordID && left.ManifestID == right.ManifestID &&
		left.RawSHA256 == right.RawSHA256 && left.ManifestSHA256 == right.ManifestSHA256 &&
		left.AcceptedAt.Equal(right.AcceptedAt)
}

func opsAcceptanceUserContext(t *testing.T, requestID, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		requestID, domaintool.ActorKindUser, ownerID, ownerID,
		[]string{domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func assertOPSAcceptanceRowCounts(t *testing.T, store *L1SQLiteStore, wantAcceptance, wantManifest, wantRecord, wantState, wantThread int) {
	t.Helper()
	counts := map[string]int{
		"acceptance": 0,
		"manifest":   0,
		"record":     0,
		"state":      0,
		"thread":     0,
	}
	for _, table := range []struct {
		name  string
		table string
	}{
		{name: "acceptance", table: "conversation_ops_input_acceptance"},
		{name: "manifest", table: "l1_raw_source_manifest"},
		{name: "record", table: "l1_raw_record"},
		{name: "state", table: "l1_raw_state_event"},
		{name: "thread", table: "conversation_active_thread"},
	} {
		var count int
		if err := store.db.QueryRowContext(context.Background(), "SELECT count(*) FROM "+table.table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table.table, err)
		}
		counts[table.name] = count
	}
	want := map[string]int{"acceptance": wantAcceptance, "manifest": wantManifest, "record": wantRecord, "state": wantState, "thread": wantThread}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("owner transaction rows = %v, want %v", counts, want)
	}
}

func jsonMarshal(value any) ([]byte, error) {
	return json.Marshal(value)
}
