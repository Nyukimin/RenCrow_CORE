package storagehost

import (
	"context"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	_ TurnGroupOwner = (*l1sqlite.L1SQLiteStore)(nil)
	_ TurnGroupOwner = (*TurnStoreClient)(nil)
)

type turnGroupFixture struct {
	store  *l1sqlite.L1SQLiteStore
	host   *Handler
	client *Client
}

type blockingTurnClaimOwner struct {
	*l1sqlite.L1SQLiteStore
	started chan struct{}
}

func (o *blockingTurnClaimOwner) ClaimNextConversationTurnOutboxTargetExcludingForOperation(ctx context.Context, _ l1sqlite.ConversationTurnOperationIdentity, _ string, _ time.Time, _ time.Duration, _ map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	close(o.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func newTurnGroupFixture(t *testing.T, owner func(*l1sqlite.L1SQLiteStore) TurnGroupOwner) turnGroupFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(dir, "conversation.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	host, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: filepath.Join(dir, "journal")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	selectedOwner := TurnGroupOwner(store)
	if owner != nil {
		selectedOwner = owner(store)
	}
	if err := RegisterTurnGroup(host, selectedOwner); err != nil {
		t.Fatalf("RegisterTurnGroup: %v", err)
	}

	server := httptest.NewServer(host)
	t.Cleanup(server.Close)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: testToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	return turnGroupFixture{store: store, host: host, client: client}
}

func newTurnGroupRequest(targets ...domconv.ConversationTurnTarget) domconv.ConversationTurnRequest {
	return domconv.ConversationTurnRequest{
		TurnID:         modulecore.NewTurnID(),
		TraceID:        modulecore.NewTraceID(),
		RootTaskID:     modulecore.NewTaskID(),
		UserMessageID:  modulecore.NewMessageID(),
		AgentMessageID: modulecore.NewMessageID(),
		SessionID:      string(modulecore.NewSessionID()),
		OwnerID:        "turn-group-test-owner",
		Domain:         "general",
		UserMessage:    "hello from the user",
		AgentMessage:   "hello from the agent",
		AgentSpeaker:   domconv.SpeakerMio,
		Targets:        targets,
	}
}

func TestTurnGroupRegistersOnlyClosedTypedOperations(t *testing.T) {
	fixture := newTurnGroupFixture(t, nil)
	info, err := fixture.client.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	want := map[string]bool{
		"commit": true, "receipt": false, "outbox_claim": true, "outbox_claim_target": true, "outbox_claim_next": true,
		"complete": true, "fail": true, "projection": false, "active_projection": false,
		"response_published": true, "response_publication_status": false, "pending_thread_followers": false,
		"unpublished_thread_followers": false, "session_has_turns": false,
	}
	got := make(map[string]bool)
	for _, operation := range info.Operations {
		if operation.Group != GroupTurn {
			continue
		}
		got[operation.Op] = operation.Mutating
	}
	if len(got) != len(want) {
		t.Fatalf("turn operations=%v, want exactly %v", got, want)
	}
	for operation, mutating := range want {
		if got[operation] != mutating {
			t.Fatalf("turn operation %q mutating=%v, want %v (all=%v)", operation, got[operation], mutating, got)
		}
		if mutating {
			if _, ok := fixture.host.recoverable[opKey(GroupTurn, operation)]; !ok {
				t.Errorf("mutating turn operation %q is not registered with owner reconciliation", operation)
			}
		}
	}
}

func TestTurnStoreClientCancelsRemoteTargetClaimWithRequestContext(t *testing.T) {
	var owner *blockingTurnClaimOwner
	fixture := newTurnGroupFixture(t, func(store *l1sqlite.L1SQLiteStore) TurnGroupOwner {
		owner = &blockingTurnClaimOwner{L1SQLiteStore: store, started: make(chan struct{})}
		return owner
	})
	store := NewTurnStoreClient(fixture.client)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := store.ClaimNextConversationTurnOutboxTargetExcluding(ctx, string(domconv.ConversationTurnTargetRedisProjection), time.Now().UTC(), time.Minute, nil)
		done <- err
	}()
	select {
	case <-owner.started:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("remote owner did not enter the target claim")
	}
	cancel()
	select {
	case err := <-done:
		assertTurnErrorCode(t, err, ErrorCodeOutcomeUnknown)
	case <-time.After(time.Second):
		t.Fatal("remote target claim did not return after context cancellation")
	}
}

func TestTurnStoreClientResponsePublicationMarkerGatesPendingThreadFollower(t *testing.T) {
	fixture := newTurnGroupFixture(t, nil)
	ctx := context.Background()
	store := NewTurnStoreClient(fixture.client)
	var boundary domconv.ConversationTurnRequest
	sessionID := string(modulecore.NewSessionID())
	for index := 0; index < 6; index++ {
		request := newTurnGroupRequest()
		request.SessionID = sessionID
		if _, err := store.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("seed %d commit: %v", index, err)
		}
		if index == 5 {
			boundary = newTurnGroupRequest(domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers)
			boundary.SessionID = request.SessionID
			boundary.Boundary = true
			boundary.BoundaryReason = "new topic"
		}
	}
	committed, err := store.CommitConversationTurn(ctx, boundary)
	if err != nil || committed.ClosedThreadID == "" {
		t.Fatalf("boundary=%+v err=%v", committed, err)
	}
	redisClaim, err := store.ClaimConversationTurnOutboxTarget(ctx, string(boundary.TurnID), string(domconv.ConversationTurnTargetRedisProjection), time.Now().UTC(), time.Minute)
	if err != nil || redisClaim == nil || redisClaim.Target != string(domconv.ConversationTurnTargetRedisProjection) {
		t.Fatalf("target-scoped Redis claim=%+v err=%v", redisClaim, err)
	}
	if _, err := store.CompleteConversationTurnOutbox(ctx, string(boundary.TurnID), redisClaim.Target, redisClaim.LeaseToken, time.Now().UTC().Add(time.Second)); err != nil {
		t.Fatalf("complete target-scoped Redis claim: %v", err)
	}
	if claim, err := store.ClaimNextConversationTurnOutbox(ctx, time.Now().UTC().Add(2*time.Second), time.Minute); err != nil || claim != nil {
		t.Fatalf("unpublished thread follower claim=%+v err=%v", claim, err)
	}
	candidates, err := store.ListPendingConversationTurnThreadFollowers(ctx, boundary.SessionID, 100)
	if err != nil || len(candidates) != 1 || candidates[0].ResponsePublished {
		t.Fatalf("pending candidates=%+v err=%v", candidates, err)
	}
	unpublished, err := store.ListUnpublishedConversationTurnThreadFollowersAfter(ctx, "", 100)
	if err != nil || len(unpublished) != 1 || unpublished[0].TurnID != boundary.TurnID {
		t.Fatalf("unpublished reconciliation candidates=%+v err=%v", unpublished, err)
	}
	if err := store.MarkConversationTurnResponsePublished(ctx, boundary.TurnID, boundary.TraceID, modulecore.SessionID(boundary.SessionID), time.Now().UTC()); err != nil {
		t.Fatalf("mark response published: %v", err)
	}
	unpublished, err = store.ListUnpublishedConversationTurnThreadFollowersAfter(ctx, "", 100)
	if err != nil || len(unpublished) != 0 {
		t.Fatalf("published candidate remained in reconciliation page=%+v err=%v", unpublished, err)
	}
	redisClaim, err = store.ClaimConversationTurnOutboxTarget(ctx, string(boundary.TurnID), string(domconv.ConversationTurnTargetRedisProjection), time.Now().UTC().Add(3*time.Second), time.Minute)
	if err != nil || redisClaim != nil {
		t.Fatalf("target-scoped Redis replay claim=%+v err=%v, want no claim while thread follower is eligible", redisClaim, err)
	}
	candidates, err = store.ListPendingConversationTurnThreadFollowers(ctx, boundary.SessionID, 100)
	if err != nil || len(candidates) != 1 || !candidates[0].ResponsePublished {
		t.Fatalf("published candidates=%+v err=%v", candidates, err)
	}
	claim, err := store.ClaimNextConversationTurnOutbox(ctx, time.Now().UTC().Add(4*time.Second), time.Minute)
	if err != nil || claim == nil || claim.TurnID != boundary.TurnID || claim.Target != string(domconv.ConversationTurnTargetThreadFollowers) {
		t.Fatalf("published follower claim=%+v err=%v", claim, err)
	}
}

func TestTurnStoreClientUsesRealL1CommitReceiptProjectionAndOutboxLifecycle(t *testing.T) {
	fixture := newTurnGroupFixture(t, nil)
	ctx := context.Background()
	store := NewTurnStoreClient(fixture.client)
	request := newTurnGroupRequest(domconv.ConversationTurnTargetRedisProjection)

	committed, err := store.CommitConversationTurn(ctx, request)
	if err != nil {
		t.Fatalf("CommitConversationTurn: %v", err)
	}
	if hasTurns, err := store.HasConversationTurnReceiptsForSession(ctx, request.SessionID); err != nil || !hasTurns {
		t.Fatalf("remote session typed-turn presence=(%v,%v), want true after commit", hasTurns, err)
	}
	if hasTurns, err := store.HasConversationTurnReceiptsForSession(ctx, string(modulecore.NewSessionID())); err != nil || hasTurns {
		t.Fatalf("remote empty session typed-turn presence=(%v,%v), want false", hasTurns, err)
	}
	if committed.Status != domconv.ConversationTurnPartial {
		t.Fatalf("commit status=%q, want partial while outbox is pending", committed.Status)
	}
	if committed.TurnID != request.TurnID || committed.TraceID != request.TraceID || committed.RootTaskID != request.RootTaskID ||
		committed.UserMessageID != request.UserMessageID || committed.AgentMessageID != request.AgentMessageID {
		t.Fatalf("commit identity=%+v does not match request", committed)
	}

	receipt, err := store.GetConversationTurnReceipt(ctx, string(request.TurnID))
	if err != nil {
		t.Fatalf("GetConversationTurnReceipt: %v", err)
	}
	if receipt.TurnID != committed.TurnID || receipt.ThreadID != committed.ThreadID || receipt.ThreadSeq != committed.ThreadSeq {
		t.Fatalf("receipt=%+v does not preserve committed identity %+v", receipt, committed)
	}

	projection, err := store.LoadConversationThreadProjection(ctx, request.SessionID, committed.ThreadID)
	if err != nil {
		t.Fatalf("LoadConversationThreadProjection: %v", err)
	}
	if len(projection) != 2 || projection[0].ID != string(request.UserMessageID) || projection[1].ID != string(request.AgentMessageID) {
		t.Fatalf("projection=%+v, want the committed message pair", projection)
	}
	active, err := store.LoadActiveConversationThreadProjection(ctx, request.SessionID)
	if err != nil {
		t.Fatalf("LoadActiveConversationThreadProjection: %v", err)
	}
	if len(active) != 2 || active[0].ID != projection[0].ID || active[1].ID != projection[1].ID {
		t.Fatalf("active projection=%+v, want the same committed message pair", active)
	}

	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	claimed, err := store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now, time.Minute)
	if err != nil {
		t.Fatalf("ClaimConversationTurnOutbox: %v", err)
	}
	if claimed == nil || claimed.Status != domconv.ConversationTurnOutboxRunning || claimed.LeaseToken == "" {
		t.Fatalf("claim=%+v, want running outbox with its lease token", claimed)
	}

	finished, err := store.CompleteConversationTurnOutbox(ctx, string(request.TurnID), claimed.Target, claimed.LeaseToken, now.Add(time.Second))
	if err != nil {
		t.Fatalf("CompleteConversationTurnOutbox: %v", err)
	}
	if finished.Status != domconv.ConversationTurnCompleted || len(finished.CompletedTargets) != 1 || len(finished.PendingTargets) != 0 {
		t.Fatalf("completed result=%+v, want terminal completed receipt", finished)
	}
	receipt, err = store.GetConversationTurnReceipt(ctx, string(request.TurnID))
	if err != nil || receipt.Status != domconv.ConversationTurnCompleted {
		t.Fatalf("final receipt=%+v err=%v, want completed", receipt, err)
	}
}

func TestTurnStoreClientClaimNextAndFailLifecycle(t *testing.T) {
	fixture := newTurnGroupFixture(t, nil)
	ctx := context.Background()
	store := NewTurnStoreClient(fixture.client)
	request := newTurnGroupRequest(domconv.ConversationTurnTargetRedisProjection)
	if _, err := store.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("CommitConversationTurn: %v", err)
	}
	claimed, err := store.ClaimNextConversationTurnOutbox(ctx, time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), time.Minute)
	if err != nil {
		t.Fatalf("ClaimNextConversationTurnOutbox: %v", err)
	}
	if claimed == nil || claimed.TurnID != request.TurnID {
		t.Fatalf("claim_next=%+v, want turn %s", claimed, request.TurnID)
	}
	failed, err := store.FailConversationTurnOutbox(ctx, string(request.TurnID), claimed.Target, claimed.LeaseToken,
		domconv.ConversationTurnErrorUnavailable, time.Date(2026, 10, 3, 12, 0, 1, 0, time.UTC))
	if err != nil {
		t.Fatalf("FailConversationTurnOutbox: %v", err)
	}
	if failed.Status != domconv.ConversationTurnPartial {
		t.Fatalf("failed result status=%q, want partial while retry budget remains", failed.Status)
	}
}

type countingTurnGroupOwner struct {
	TurnGroupRecoveryOwner
	commitCalls  atomic.Int32
	receiptCalls atomic.Int32
}

func (o *countingTurnGroupOwner) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	o.commitCalls.Add(1)
	return o.TurnGroupRecoveryOwner.CommitConversationTurn(ctx, request)
}

func (o *countingTurnGroupOwner) GetConversationTurnReceipt(ctx context.Context, turnID string) (domconv.ConversationTurnResult, error) {
	o.receiptCalls.Add(1)
	return o.TurnGroupRecoveryOwner.GetConversationTurnReceipt(ctx, turnID)
}

func TestTurnGroupRejectsMalformedIDsAndUnknownSchemaBeforeOwner(t *testing.T) {
	var owner *countingTurnGroupOwner
	fixture := newTurnGroupFixture(t, func(store *l1sqlite.L1SQLiteStore) TurnGroupOwner {
		owner = &countingTurnGroupOwner{TurnGroupRecoveryOwner: store}
		return owner
	})
	ctx := context.Background()
	badCommit := turnCommitPayload{Request: turnRequestToDTO(newTurnGroupRequest())}
	badCommit.Request.TurnID = "not-a-canonical-turn-id"
	err := fixture.client.Call(ctx, GroupTurn, "commit", badCommit, nil)
	assertTurnErrorCode(t, err, ErrorCodeSchemaRejected)
	if owner.commitCalls.Load() != 0 {
		t.Fatalf("owner commit calls=%d after malformed ID, want 0", owner.commitCalls.Load())
	}
	if entry, found := fixture.host.journal.lookup(fixture.client.lastOpID); found {
		t.Fatalf("pre-owner rejection left a journal entry: %+v", entry)
	}

	var result domconv.ConversationTurnResult
	err = fixture.client.Call(ctx, GroupTurn, "receipt", map[string]any{"turn_id": "not-a-canonical-turn-id"}, &result)
	assertTurnErrorCode(t, err, ErrorCodeSchemaRejected)
	if owner.receiptCalls.Load() != 0 {
		t.Fatalf("owner receipt calls=%d after malformed ID, want 0", owner.receiptCalls.Load())
	}

	err = fixture.client.Call(ctx, GroupTurn, "receipt", map[string]any{"turn_id": string(modulecore.NewTurnID()), "sql": "ignored"}, &result)
	assertTurnErrorCode(t, err, ErrorCodeSchemaRejected)
	if owner.receiptCalls.Load() != 0 {
		t.Fatalf("owner receipt calls=%d after unknown field, want 0", owner.receiptCalls.Load())
	}
}

type turnCommitThenErrorOwner struct {
	TurnGroupRecoveryOwner
	calls        atomic.Int32
	receiptCalls atomic.Int32
}

func (o *turnCommitThenErrorOwner) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	o.calls.Add(1)
	result, err := o.TurnGroupRecoveryOwner.CommitConversationTurn(ctx, request)
	if err != nil {
		return result, err
	}
	return result, errors.New("synthetic response failure after the real SQLite commit")
}

func (o *turnCommitThenErrorOwner) GetConversationTurnReceipt(ctx context.Context, turnID string) (domconv.ConversationTurnResult, error) {
	o.receiptCalls.Add(1)
	return o.TurnGroupRecoveryOwner.GetConversationTurnReceipt(ctx, turnID)
}

type turnMutationThenErrorOwner struct {
	TurnGroupRecoveryOwner
	failOn string
	calls  atomic.Int32
}

func (o *turnMutationThenErrorOwner) afterCommit(operation string, err error) error {
	if err == nil && o.failOn == operation {
		o.calls.Add(1)
		return errors.New("synthetic lost result after the real SQLite mutation receipt")
	}
	return err
}

func (o *turnMutationThenErrorOwner) ClaimConversationTurnOutboxForOperation(ctx context.Context, identity l1sqlite.ConversationTurnOperationIdentity, turnID string, now time.Time, lease time.Duration) (*domconv.ConversationTurnOutbox, error) {
	result, err := o.TurnGroupRecoveryOwner.ClaimConversationTurnOutboxForOperation(ctx, identity, turnID, now, lease)
	return result, o.afterCommit("outbox_claim", err)
}

func (o *turnMutationThenErrorOwner) ClaimConversationTurnOutboxTargetForOperation(ctx context.Context, identity l1sqlite.ConversationTurnOperationIdentity, turnID, target string, now time.Time, lease time.Duration) (*domconv.ConversationTurnOutbox, error) {
	result, err := o.TurnGroupRecoveryOwner.ClaimConversationTurnOutboxTargetForOperation(ctx, identity, turnID, target, now, lease)
	return result, o.afterCommit("outbox_claim_target", err)
}

func (o *turnMutationThenErrorOwner) ClaimNextConversationTurnOutboxForOperation(ctx context.Context, identity l1sqlite.ConversationTurnOperationIdentity, now time.Time, lease time.Duration) (*domconv.ConversationTurnOutbox, error) {
	result, err := o.TurnGroupRecoveryOwner.ClaimNextConversationTurnOutboxForOperation(ctx, identity, now, lease)
	return result, o.afterCommit("outbox_claim_next", err)
}

func (o *turnMutationThenErrorOwner) ClaimNextConversationTurnOutboxTargetExcludingForOperation(ctx context.Context, identity l1sqlite.ConversationTurnOperationIdentity, target string, now time.Time, lease time.Duration, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	result, err := o.TurnGroupRecoveryOwner.ClaimNextConversationTurnOutboxTargetExcludingForOperation(ctx, identity, target, now, lease, excluded)
	return result, o.afterCommit("outbox_claim_next", err)
}

func (o *turnMutationThenErrorOwner) CompleteConversationTurnOutboxForOperation(ctx context.Context, identity l1sqlite.ConversationTurnOperationIdentity, turnID, target, leaseToken string, now time.Time) (domconv.ConversationTurnResult, error) {
	result, err := o.TurnGroupRecoveryOwner.CompleteConversationTurnOutboxForOperation(ctx, identity, turnID, target, leaseToken, now)
	return result, o.afterCommit("complete", err)
}

func (o *turnMutationThenErrorOwner) FailConversationTurnOutboxForOperation(ctx context.Context, identity l1sqlite.ConversationTurnOperationIdentity, turnID, target, leaseToken string, code domconv.ConversationTurnErrorCode, now time.Time) (domconv.ConversationTurnResult, error) {
	result, err := o.TurnGroupRecoveryOwner.FailConversationTurnOutboxForOperation(ctx, identity, turnID, target, leaseToken, code, now)
	return result, o.afterCommit("fail", err)
}

func TestTurnGroupOwnerErrorAfterCommitReconcilesRealSQLiteReceipt(t *testing.T) {
	var owner *turnCommitThenErrorOwner
	fixture := newTurnGroupFixture(t, func(store *l1sqlite.L1SQLiteStore) TurnGroupOwner {
		owner = &turnCommitThenErrorOwner{TurnGroupRecoveryOwner: store}
		return owner
	})
	ctx := context.Background()
	client := NewTurnStoreClient(fixture.client)
	request := newTurnGroupRequest()

	_, err := client.CommitConversationTurn(ctx, request)
	assertTurnErrorCode(t, err, ErrorCodeOutcomeUnknown)
	opID := fixture.client.lastOpID
	result, err := client.CommitConversationTurn(ctx, request)
	if err != nil {
		t.Fatalf("retry after the real SQLite commit: %v", err)
	}
	if owner.calls.Load() != 1 {
		t.Fatalf("owner commit calls=%d, want exactly 1 after retry", owner.calls.Load())
	}
	if owner.receiptCalls.Load() != 1 {
		t.Fatalf("owner receipt calls=%d, want one exact owner reconciliation", owner.receiptCalls.Load())
	}
	sha, err := request.PayloadSHA256()
	if err != nil {
		t.Fatalf("request payload SHA256: %v", err)
	}
	if result.TurnID != request.TurnID || result.TraceID != request.TraceID || result.RootTaskID != request.RootTaskID ||
		result.UserMessageID != request.UserMessageID || result.AgentMessageID != request.AgentMessageID || result.PayloadSHA256 != sha {
		t.Fatalf("reconciled result=%+v, want exact request identities and PayloadSHA256 %s", result, sha)
	}
	if fixture.client.lastOpID != opID {
		t.Fatalf("retry op_id=%q, want the original %q", fixture.client.lastOpID, opID)
	}
	entry, found := fixture.host.journal.lookup(fixture.client.lastOpID)
	if !found || entry.Status != journalStatusDone {
		t.Fatalf("journal entry=%+v found=%v, want reconciled done receipt", entry, found)
	}
}

func TestTurnStoreClientFilteredClaimsPreserveRemoteRetryBackoff(t *testing.T) {
	fixture := newTurnGroupFixture(t, nil)
	ctx := context.Background()
	first := newTurnGroupRequest(domconv.ConversationTurnTargetRedisProjection)
	second := newTurnGroupRequest(domconv.ConversationTurnTargetRedisProjection)
	if first.TurnID > second.TurnID {
		first, second = second, first
	}
	for _, request := range []domconv.ConversationTurnRequest{first, second} {
		if _, err := fixture.store.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("commit %s: %v", request.TurnID, err)
		}
	}
	client := NewTurnStoreClient(fixture.client)
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	claim, err := client.ClaimNextConversationTurnOutboxTargetExcluding(ctx, string(domconv.ConversationTurnTargetRedisProjection), now, time.Minute, nil)
	if err != nil || claim == nil || claim.TurnID != first.TurnID {
		t.Fatalf("remote first claim=%+v err=%v", claim, err)
	}
	if _, err := client.FailConversationTurnOutbox(ctx, string(claim.TurnID), claim.Target, claim.LeaseToken, domconv.ConversationTurnErrorUnavailable, now); err != nil {
		t.Fatalf("remote fail: %v", err)
	}
	excluded := map[string]struct{}{string(claim.TurnID) + "\x00" + claim.Target: {}}
	next, err := client.ClaimNextConversationTurnOutboxTargetExcluding(ctx, claim.Target, now.Add(time.Second), time.Minute, excluded)
	if err != nil || next == nil || next.TurnID != second.TurnID || next.Attempts != 1 {
		t.Fatalf("remote filtered next claim=%+v err=%v, want second row attempt 1", next, err)
	}
	retry, err := fixture.store.ClaimConversationTurnOutbox(ctx, string(first.TurnID), now.Add(2*time.Second), time.Minute)
	if err != nil || retry == nil || retry.Attempts != 2 {
		t.Fatalf("remote failed-row attempts=%+v err=%v, want attempt 2 only on next pass", retry, err)
	}
}

func TestTurnGroupClaimMutationsReconcileAtomicSQLiteReceipts(t *testing.T) {
	t.Run("target claim returns exact committed Redis lease", func(t *testing.T) {
		fixture, owner := newTurnMutationThenErrorFixture(t, "outbox_claim_target")
		ctx := context.Background()
		request := newTurnGroupRequest(domconv.ConversationTurnTargetRedisProjection)
		if _, err := fixture.store.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("seed turn: %v", err)
		}
		client := NewTurnStoreClient(fixture.client)
		now := time.Now().UTC()
		_, err := client.ClaimConversationTurnOutboxTarget(ctx, string(request.TurnID), string(domconv.ConversationTurnTargetRedisProjection), now, time.Minute)
		assertTurnErrorCode(t, err, ErrorCodeOutcomeUnknown)
		opID := fixture.client.lastOpID
		identity := turnOperationIdentityFromJournal(t, fixture, opID, "outbox_claim")
		claimed, err := client.ClaimConversationTurnOutboxTarget(ctx, string(request.TurnID), string(domconv.ConversationTurnTargetRedisProjection), now, time.Minute)
		if err != nil || claimed == nil || claimed.Target != string(domconv.ConversationTurnTargetRedisProjection) {
			t.Fatalf("retry target claim=%+v err=%v", claimed, err)
		}
		recorded, found, err := fixture.store.GetConversationTurnClaimOperationReceipt(ctx, identity)
		if err != nil || !found || recorded == nil || recorded.TurnID != claimed.TurnID || recorded.Target != claimed.Target ||
			recorded.LeaseToken != claimed.LeaseToken || recorded.Attempts != claimed.Attempts {
			t.Fatalf("durable target claim receipt=%+v found=%v err=%v", recorded, found, err)
		}
		if owner.calls.Load() != 1 {
			t.Fatalf("owner target claim calls=%d, want one after lost result", owner.calls.Load())
		}
	})

	t.Run("claim returns the exact committed lease", func(t *testing.T) {
		fixture, owner := newTurnMutationThenErrorFixture(t, "outbox_claim")
		ctx := context.Background()
		request := newTurnGroupRequest(domconv.ConversationTurnTargetRedisProjection)
		if _, err := fixture.store.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("seed turn: %v", err)
		}
		client := NewTurnStoreClient(fixture.client)
		now := time.Now().UTC()
		_, err := client.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now, time.Minute)
		assertTurnErrorCode(t, err, ErrorCodeOutcomeUnknown)
		opID := fixture.client.lastOpID
		identity := turnOperationIdentityFromJournal(t, fixture, opID, "outbox_claim")
		claimed, err := client.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now, time.Minute)
		if err != nil || claimed == nil {
			t.Fatalf("retry claim=%+v err=%v, want committed lease", claimed, err)
		}
		recorded, found, err := fixture.store.GetConversationTurnClaimOperationReceipt(ctx, identity)
		if err != nil || !found || recorded == nil || recorded.TurnID != claimed.TurnID || recorded.Target != claimed.Target ||
			recorded.LeaseToken != claimed.LeaseToken || recorded.Attempts != claimed.Attempts || !recorded.LeaseExpiresAt.Equal(claimed.LeaseExpiresAt) {
			t.Fatalf("durable claim receipt=%+v found=%v err=%v, want exact returned lease", recorded, found, err)
		}
		if owner.calls.Load() != 1 {
			t.Fatalf("owner claim calls=%d, want one after lost result", owner.calls.Load())
		}
	})

	t.Run("claim_next returns the exact committed lease", func(t *testing.T) {
		fixture, owner := newTurnMutationThenErrorFixture(t, "outbox_claim_next")
		ctx := context.Background()
		request := newTurnGroupRequest(domconv.ConversationTurnTargetRedisProjection)
		if _, err := fixture.store.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("seed turn: %v", err)
		}
		client := NewTurnStoreClient(fixture.client)
		now := time.Now().UTC()
		_, err := client.ClaimNextConversationTurnOutbox(ctx, now, time.Minute)
		assertTurnErrorCode(t, err, ErrorCodeOutcomeUnknown)
		opID := fixture.client.lastOpID
		identity := turnOperationIdentityFromJournal(t, fixture, opID, "outbox_claim_next")
		claimed, err := client.ClaimNextConversationTurnOutbox(ctx, now, time.Minute)
		if err != nil || claimed == nil || claimed.TurnID != request.TurnID {
			t.Fatalf("retry claim_next=%+v err=%v, want committed lease for %s", claimed, err, request.TurnID)
		}
		recorded, found, err := fixture.store.GetConversationTurnClaimOperationReceipt(ctx, identity)
		if err != nil || !found || recorded == nil || recorded.TurnID != claimed.TurnID || recorded.Target != claimed.Target || recorded.LeaseToken != claimed.LeaseToken {
			t.Fatalf("durable claim_next receipt=%+v found=%v err=%v, want exact returned lease", recorded, found, err)
		}
		if owner.calls.Load() != 1 {
			t.Fatalf("owner claim_next calls=%d, want one after lost result", owner.calls.Load())
		}
	})

	t.Run("empty claim_next has a durable nil receipt", func(t *testing.T) {
		fixture, owner := newTurnMutationThenErrorFixture(t, "outbox_claim_next")
		ctx := context.Background()
		client := NewTurnStoreClient(fixture.client)
		now := time.Now().UTC()
		_, err := client.ClaimNextConversationTurnOutbox(ctx, now, time.Minute)
		assertTurnErrorCode(t, err, ErrorCodeOutcomeUnknown)
		opID := fixture.client.lastOpID
		identity := turnOperationIdentityFromJournal(t, fixture, opID, "outbox_claim_next")
		claimed, err := client.ClaimNextConversationTurnOutbox(ctx, now, time.Minute)
		if err != nil || claimed != nil {
			t.Fatalf("retry empty claim_next=%+v err=%v, want the original nil result", claimed, err)
		}
		recorded, found, err := fixture.store.GetConversationTurnClaimOperationReceipt(ctx, identity)
		if err != nil || !found || recorded != nil {
			t.Fatalf("durable empty claim_next receipt=%+v found=%v err=%v, want present nil result", recorded, found, err)
		}
		if owner.calls.Load() != 1 {
			t.Fatalf("owner empty claim_next calls=%d, want one after lost result", owner.calls.Load())
		}
	})
}

func TestTurnGroupFinishMutationsReconcileExactLeaseReceipts(t *testing.T) {
	for _, operation := range []string{"complete", "fail"} {
		t.Run(operation, func(t *testing.T) {
			fixture, owner := newTurnMutationThenErrorFixture(t, operation)
			ctx := context.Background()
			request := newTurnGroupRequest(domconv.ConversationTurnTargetRedisProjection)
			if _, err := fixture.store.CommitConversationTurn(ctx, request); err != nil {
				t.Fatalf("seed turn: %v", err)
			}
			now := time.Now().UTC()
			claimed, err := fixture.store.ClaimConversationTurnOutbox(ctx, string(request.TurnID), now, time.Minute)
			if err != nil || claimed == nil {
				t.Fatalf("seed lease=%+v err=%v", claimed, err)
			}
			client := NewTurnStoreClient(fixture.client)
			finishAt := now.Add(time.Second)
			var firstErr error
			if operation == "complete" {
				_, firstErr = client.CompleteConversationTurnOutbox(ctx, string(request.TurnID), claimed.Target, claimed.LeaseToken, finishAt)
			} else {
				_, firstErr = client.FailConversationTurnOutbox(ctx, string(request.TurnID), claimed.Target, claimed.LeaseToken, domconv.ConversationTurnErrorUnavailable, finishAt)
			}
			assertTurnErrorCode(t, firstErr, ErrorCodeOutcomeUnknown)
			opID := fixture.client.lastOpID
			identity := turnOperationIdentityFromJournal(t, fixture, opID, operation)
			var result domconv.ConversationTurnResult
			if operation == "complete" {
				result, err = client.CompleteConversationTurnOutbox(ctx, string(request.TurnID), claimed.Target, claimed.LeaseToken, finishAt)
			} else {
				result, err = client.FailConversationTurnOutbox(ctx, string(request.TurnID), claimed.Target, claimed.LeaseToken, domconv.ConversationTurnErrorUnavailable, finishAt)
			}
			if err != nil || result.TurnID != request.TurnID {
				t.Fatalf("retry %s result=%+v err=%v", operation, result, err)
			}
			recorded, found, err := fixture.store.GetConversationTurnFinishOperationReceipt(ctx, identity, string(request.TurnID), claimed.Target, claimed.LeaseToken)
			if err != nil || !found || recorded.TurnID != result.TurnID || recorded.Status != result.Status || recorded.ErrorCode != result.ErrorCode {
				t.Fatalf("durable %s receipt=%+v found=%v err=%v, want exact returned result", operation, recorded, found, err)
			}
			if owner.calls.Load() != 1 {
				t.Fatalf("owner %s calls=%d, want one after lost result", operation, owner.calls.Load())
			}
		})
	}
}

func newTurnMutationThenErrorFixture(t *testing.T, operation string) (turnGroupFixture, *turnMutationThenErrorOwner) {
	t.Helper()
	var owner *turnMutationThenErrorOwner
	fixture := newTurnGroupFixture(t, func(store *l1sqlite.L1SQLiteStore) TurnGroupOwner {
		owner = &turnMutationThenErrorOwner{TurnGroupRecoveryOwner: store, failOn: operation}
		return owner
	})
	return fixture, owner
}

func turnOperationIdentityFromJournal(t *testing.T, fixture turnGroupFixture, opID, operation string) l1sqlite.ConversationTurnOperationIdentity {
	t.Helper()
	entry, found := fixture.host.journal.lookup(opID)
	if !found {
		t.Fatalf("journal entry for %s not found", opID)
	}
	return l1sqlite.ConversationTurnOperationIdentity{OpID: opID, Operation: operation, PayloadSHA256: entry.PayloadHash}
}

func TestTurnStoreClientPreservesOwnerEmptyAndNotFoundBehavior(t *testing.T) {
	fixture := newTurnGroupFixture(t, nil)
	client := NewTurnStoreClient(fixture.client)
	ctx := context.Background()

	claimed, err := client.ClaimNextConversationTurnOutbox(ctx, time.Time{}, time.Minute)
	if err != nil || claimed != nil {
		t.Fatalf("empty claim_next=%+v err=%v, want nil, nil", claimed, err)
	}
	_, err = client.GetConversationTurnReceipt(ctx, string(modulecore.NewTurnID()))
	if !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("missing receipt error=%v, want ErrConversationTurnUnavailable", err)
	}
	_, err = client.LoadActiveConversationThreadProjection(ctx, string(modulecore.NewSessionID()))
	if !errors.Is(err, domconv.ErrThreadNotFound) {
		t.Fatalf("missing active thread error=%v, want ErrThreadNotFound", err)
	}
}

func assertTurnErrorCode(t *testing.T, err error, want string) {
	t.Helper()
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != want {
		t.Fatalf("error=%v, want storagehost code %q", err, want)
	}
}
