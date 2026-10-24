package l1sqlite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type userMemoryNoticeCapture struct {
	notices   []UserMemoryMutationNotice
	completed int
}

func (capture *userMemoryNoticeCapture) callback(notice UserMemoryMutationNotice) {
	capture.notices = append(capture.notices, notice)
	capture.completed++
}

func assertUserMemoryNotice(t *testing.T, capture *userMemoryNoticeCapture, index int, userID string, memoryIDs ...string) UserMemoryMutationNotice {
	t.Helper()
	if capture.completed != index+1 || len(capture.notices) != index+1 {
		t.Fatalf("callback completed=%d notices=%+v; want callback %d complete before return", capture.completed, capture.notices, index+1)
	}
	notice := capture.notices[index]
	if notice.UserID != userID || notice.ChangeID == "" || len(notice.MemoryIDs) != len(memoryIDs) {
		t.Fatalf("notice=%+v, want user=%q IDs=%v and opaque change identity", notice, userID, memoryIDs)
	}
	for i := range memoryIDs {
		if notice.MemoryIDs[i] != memoryIDs[i] {
			t.Fatalf("notice IDs=%v, want exact affected IDs %v", notice.MemoryIDs, memoryIDs)
		}
	}
	return notice
}

func userMemoryCallbackTestStore(t *testing.T, filename string) *L1SQLiteStore {
	t.Helper()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), filename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func userMemoryCallbackTestInput(userID, statement string) domainmemory.CreateUserMemoryInput {
	return domainmemory.CreateUserMemoryInput{
		UserID: userID, Type: domainmemory.UserMemoryTypePreference, Statement: statement,
		State: MemoryStateCandidate, EvidenceEventIDs: []string{"callback-evidence-" + statement},
		Confidence: .8, Sensitivity: "normal", Scope: "all_personas",
	}
}

func userMemoryCallbackTestMeta(userID, statement string) map[string]interface{} {
	return map[string]interface{}{
		"type": domainmemory.UserMemoryTypePreference, "user_id": userID, "statement": statement,
		"evidence_event_ids": []string{"callback-evidence-" + statement}, "confidence": .8,
		"sensitivity": "normal", "scope": "all_personas", "active": true,
	}
}

func assertUserMemoryCallbackBlocksSuccessReturn(t *testing.T, store *L1SQLiteStore, call func() error) UserMemoryMutationNotice {
	t.Helper()
	started := make(chan UserMemoryMutationNotice, 1)
	finished := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseCallback := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseCallback()
	store.WithUserMemoryMutationCallback(func(notice UserMemoryMutationNotice) {
		started <- notice
		<-release
		finished <- struct{}{}
	})
	done := make(chan error, 1)
	go func() { done <- call() }()
	var notice UserMemoryMutationNotice
	select {
	case notice = <-started:
	case err := <-done:
		t.Fatalf("mutation returned before invoking its callback: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("mutation did not invoke its callback")
	}
	select {
	case err := <-done:
		t.Fatalf("mutation returned while its callback was blocked: %v", err)
	default:
	}
	releaseCallback()
	if err := <-done; err != nil {
		t.Fatalf("mutation failed: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("callback did not finish before mutation return")
	}
	return notice
}

func TestUserMemoryMutationCallbackRunsAfterCommitBeforeOwnerReceiptReturn(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "user-memory-change.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	created, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Forget callback",
		State: MemoryStateCandidate, EvidenceEventIDs: []string{"evidence-forget-callback"},
		Confidence: .8, Sensitivity: "normal", Scope: "all_personas",
	})
	if err != nil {
		t.Fatal(err)
	}

	var notices []UserMemoryMutationNotice
	callbackCompleted := false
	callbackStarted := make(chan UserMemoryMutationNotice, 1)
	allowCallbackReturn := make(chan struct{})
	var releaseOnce sync.Once
	releaseCallback := func() { releaseOnce.Do(func() { close(allowCallbackReturn) }) }
	defer releaseCallback()
	store.WithUserMemoryMutationCallback(func(notice UserMemoryMutationNotice) {
		notices = append(notices, notice)
		callbackStarted <- notice
		<-allowCallbackReturn
		callbackCompleted = true
	})
	requestID := "user-memory-forget-callback"
	type transitionResult struct {
		result domainmemory.UserMemoryOwnerResult
		err    error
	}
	returned := make(chan transitionResult, 1)
	go func() {
		result, err := store.OwnerTransitionUserMemory(
			ownerLifecycleContext(requestID, "ren"), requestID, "ren", "ren", created.ID,
			domainmemory.UserMemoryOwnerOperationForget, "", "user requested",
		)
		returned <- transitionResult{result: result, err: err}
	}()
	select {
	case <-callbackStarted:
	case outcome := <-returned:
		t.Fatalf("forget returned before invoking callback: result=%+v err=%v", outcome.result, outcome.err)
	case <-time.After(3 * time.Second):
		t.Fatal("forget did not invoke callback")
	}
	select {
	case outcome := <-returned:
		t.Fatalf("forget returned while callback was blocked: result=%+v err=%v", outcome.result, outcome.err)
	default:
	}
	// The method must not return a successful receipt until the invalidation
	// callback has completed synchronously.
	releaseCallback()
	outcome := <-returned
	if outcome.err != nil {
		t.Fatalf("forget failed: %v", outcome.err)
	}
	result := outcome.result
	if !callbackCompleted || len(notices) != 1 {
		t.Fatalf("callback completion=%v notices=%+v; want one synchronous callback before success returns", callbackCompleted, notices)
	}
	notice := notices[0]
	if notice.UserID != "ren" || len(notice.MemoryIDs) != 1 || notice.MemoryIDs[0] != created.ID || notice.ChangeID == "" {
		t.Fatalf("mutation notice=%+v, want exact owner and forgotten memory identity", notice)
	}
	var eventType string
	if err := store.db.QueryRowContext(ctx, `SELECT event_type FROM l1_event_log WHERE id = ?`, notice.ChangeID).Scan(&eventType); err != nil {
		t.Fatalf("notice change identity is not a committed owner audit event: %v", err)
	}
	if eventType != "memory.user_owner_forget" {
		t.Fatalf("change event type=%q, want memory.user_owner_forget", eventType)
	}
	var receipts int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_owner_receipt WHERE request_id = ?`, requestID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || result.Receipt.IdempotentReplay {
		t.Fatalf("committed owner receipt count=%d result=%+v", receipts, result.Receipt)
	}

	if _, err := store.OwnerTransitionUserMemory(
		ownerLifecycleContext(requestID, "ren"), requestID, "ren", "ren", created.ID,
		domainmemory.UserMemoryOwnerOperationForget, "", "user requested",
	); err != nil {
		t.Fatalf("idempotent replay failed: %v", err)
	}
	if len(notices) != 1 {
		t.Fatalf("idempotent replay invoked callback again: %+v", notices)
	}
}

func TestUserMemoryMutationCallbackDoesNotRunForRolledBackMutation(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "user-memory-change-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	created, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Keep on rollback",
		State: MemoryStateCandidate, EvidenceEventIDs: []string{"evidence-rollback-callback"},
		Confidence: .8, Sensitivity: "normal", Scope: "all_personas",
	})
	if err != nil {
		t.Fatal(err)
	}
	callbackCalls := 0
	store.WithUserMemoryMutationCallback(func(UserMemoryMutationNotice) { callbackCalls++ })
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER fail_user_memory_forget_audit
BEFORE INSERT ON l1_event_log
WHEN NEW.event_type = 'memory.user_forgotten'
BEGIN
	SELECT RAISE(ABORT, 'intentional user memory mutation failure');
END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ForgetUserMemory(ctx, created.ID, "test rollback"); err == nil {
		t.Fatal("forget unexpectedly succeeded despite audit trigger")
	}
	if callbackCalls != 0 {
		t.Fatalf("failed mutation callback calls=%d, want zero", callbackCalls)
	}
	current, found, err := store.FindUserMemoryByID(ctx, created.ID)
	if err != nil || !found || !current.Active {
		t.Fatalf("rolled-back memory=%+v found=%v err=%v; want active", current, found, err)
	}
}

func TestUserMemoryMutationCallbackRunsBeforeCreateAndOwnerProposalReturn(t *testing.T) {
	ctx := context.Background()
	store := userMemoryCallbackTestStore(t, "user-memory-create-callback.db")
	capture := &userMemoryNoticeCapture{}
	store.WithUserMemoryMutationCallback(capture.callback)

	created, err := store.CreateUserMemory(ctx, userMemoryCallbackTestInput("ren", "create callback memory"))
	if err != nil {
		t.Fatal(err)
	}
	assertUserMemoryNotice(t, capture, 0, "ren", created.ID)
	candidate, replay, err := store.CreateUserMemoryCandidateWithRequest(ctx, "callback-candidate-request", "ren", userMemoryCallbackTestInput("ren", "request candidate callback"))
	if err != nil || replay {
		t.Fatalf("candidate=%+v replay=%v err=%v", candidate, replay, err)
	}
	assertUserMemoryNotice(t, capture, 1, "ren", candidate.ID)
	if _, replay, err := store.CreateUserMemoryCandidateWithRequest(ctx, "callback-candidate-request", "ren", userMemoryCallbackTestInput("ren", "request candidate callback")); err != nil || !replay {
		t.Fatalf("candidate replay=%v err=%v", replay, err)
	}
	if capture.completed != 2 {
		t.Fatalf("candidate replay callback count=%d, want 2", capture.completed)
	}

	requestID := "owner-propose-callback"
	proposed, err := store.OwnerProposeUserMemory(ownerLifecycleContext(requestID, "ren"), requestID, "ren", "ren", domainmemory.UserMemoryTypePreference, "owner proposed memory", "explicit user request")
	if err != nil {
		t.Fatal(err)
	}
	assertUserMemoryNotice(t, capture, 2, "ren", proposed.Item.ID)
	if proposed.Receipt.IdempotentReplay {
		t.Fatal("first owner proposal unexpectedly replayed")
	}
}

func TestUserMemoryMutationCallbackRunsBeforeBackgroundLifecycleReturn(t *testing.T) {
	store := newLifecycleREDStore(t)
	defer store.Close()
	owner := "callback-background-lifecycle"
	now := time.Now().UTC()
	insertOwnerLifecycleMemory(t, store, "callback-background-candidate", owner, MemoryStateCandidate, true, "", now.Add(-8*24*time.Hour), now.Add(-8*24*time.Hour), nil)
	capture := &userMemoryNoticeCapture{}
	store.WithUserMemoryMutationCallback(capture.callback)
	updated, err := store.queueUserMemoryCandidateReview(context.Background(), now, now.Add(-7*24*time.Hour), 10)
	if err != nil || updated != 1 {
		t.Fatalf("updated=%d err=%v", updated, err)
	}
	assertUserMemoryNotice(t, capture, 0, owner, "callback-background-candidate")
}

func TestUserMemoryMutationCallbackRunsBeforeStorageHostOwnerLifecycleAndProfileReturns(t *testing.T) {
	t.Run("storagehost create", func(t *testing.T) {
		ctx := context.Background()
		store := userMemoryCallbackTestStore(t, "user-memory-storagehost-callback.db")
		capture := &userMemoryNoticeCapture{}
		store.WithUserMemoryMutationCallback(capture.callback)
		hash := sha256.Sum256([]byte("storagehost-create-callback"))
		identity, err := NewUserMemoryStorageHostOperationIdentity("callback-create", "create_user_memory", hex.EncodeToString(hash[:]), 1)
		if err != nil {
			t.Fatal(err)
		}
		memory, err := store.CreateUserMemoryForStorageHostOperation(ctx, identity, userMemoryCallbackTestInput("ren", "storagehost callback memory"))
		if err != nil {
			t.Fatal(err)
		}
		assertUserMemoryNotice(t, capture, 0, "ren", memory.ID)
		if _, err := store.CreateUserMemoryForStorageHostOperation(ctx, identity, userMemoryCallbackTestInput("ren", "storagehost callback memory")); err != nil {
			t.Fatal(err)
		}
		if capture.completed != 1 {
			t.Fatalf("StorageHost replay callback count=%d, want 1", capture.completed)
		}
	})

	t.Run("storagehost state transition", func(t *testing.T) {
		ctx := context.Background()
		store := userMemoryCallbackTestStore(t, "user-memory-storagehost-state-callback.db")
		memory, err := store.CreateUserMemory(ctx, userMemoryCallbackTestInput("ren", "storagehost state callback"))
		if err != nil {
			t.Fatal(err)
		}
		capture := &userMemoryNoticeCapture{}
		store.WithUserMemoryMutationCallback(capture.callback)
		hash := sha256.Sum256([]byte("storagehost-state-callback"))
		identity := ConversationL1OperationIdentity{OpID: "callback-state", Operation: "update_memory_state", PayloadSHA256: hex.EncodeToString(hash[:]), WriterGeneration: 2}
		if err := store.UpdateMemoryStateForOperation(ctx, identity, memory.ID, MemoryStateConfirmed); err != nil {
			t.Fatal(err)
		}
		assertUserMemoryNotice(t, capture, 0, "ren", memory.ID)
	})

	t.Run("conversation StorageHost UserMemory message", func(t *testing.T) {
		ctx := context.Background()
		store := userMemoryCallbackTestStore(t, "user-memory-storagehost-message-callback.db")
		capture := &userMemoryNoticeCapture{}
		store.WithUserMemoryMutationCallback(capture.callback)
		hash := sha256.Sum256([]byte("storagehost-message-callback"))
		identity := ConversationL1OperationIdentity{OpID: "callback-message", Operation: "save_message", PayloadSHA256: hex.EncodeToString(hash[:]), WriterGeneration: 4}
		thread := l1TestThread("storagehost-message-callback", 1)
		meta := map[string]interface{}{
			"type": domainmemory.UserMemoryTypePreference, "user_id": "ren", "statement": "storagehost message callback",
			"evidence_event_ids": []string{"storagehost-message-evidence"}, "confidence": .8,
			"sensitivity": "normal", "scope": "all_personas", "active": true,
		}
		if err := store.SaveMessageForOperation(ctx, identity, l1TestSessionID("storagehost-message-callback"), thread.ID, thread.Seq, thread.Kind, "user:ren", domconv.NewMessage(domconv.SpeakerMemory, "storagehost message callback", meta), MemoryStateCandidate); err != nil {
			t.Fatal(err)
		}
		var memoryID string
		if err := store.db.QueryRowContext(ctx, `SELECT id FROM l1_memory_event WHERE namespace = ? AND message = ?`, "user:ren", "storagehost message callback").Scan(&memoryID); err != nil {
			t.Fatal(err)
		}
		assertUserMemoryNotice(t, capture, 0, "ren", memoryID)
	})

	t.Run("owner lifecycle", func(t *testing.T) {
		store := newLifecycleREDStore(t)
		defer store.Close()
		owner := "callback-lifecycle-owner"
		evaluationAt := time.Now().UTC()
		insertOwnerLifecycleMemory(t, store, "callback-lifecycle-candidate", owner, MemoryStateCandidate, true, "", evaluationAt.Add(-8*24*time.Hour), evaluationAt.Add(-8*24*time.Hour), nil)
		planRequestID := "callback-lifecycle-plan"
		plan, err := store.OwnerPlanUserMemoryLifecycleAt(ownerLifecycleContext(planRequestID, owner), planRequestID, owner, owner, evaluationAt)
		if err != nil || plan.ActionCount != 1 {
			t.Fatalf("plan=%+v err=%v", plan, err)
		}
		capture := &userMemoryNoticeCapture{}
		store.WithUserMemoryMutationCallback(capture.callback)
		runRequestID := "callback-lifecycle-run"
		run, err := store.OwnerRunUserMemoryLifecycle(ownerLifecycleContext(runRequestID, owner), runRequestID, owner, owner, planRequestID, "apply callback lifecycle", true)
		if err != nil {
			t.Fatal(err)
		}
		if run.ActionCount != 1 {
			t.Fatalf("run=%+v", run)
		}
		assertUserMemoryNotice(t, capture, 0, owner, "callback-lifecycle-candidate")
	})

	t.Run("profile promotion", func(t *testing.T) {
		ctx := context.Background()
		store := userMemoryCallbackTestStore(t, "user-memory-profile-callback.db")
		now := time.Now().UTC()
		if err := l1TestSaveMessageInNamespace(store, ctx, "ren", "callback-profile", 1, "conv:callback-profile", domconv.NewMessage(domconv.SpeakerUser, "I like Go", nil), MemoryStateObserved); err != nil {
			t.Fatal(err)
		}
		batch, err := store.ClaimProfilePromotionBatch(ctx, 1, 5, time.Minute, now)
		if err != nil || batch == nil {
			t.Fatalf("batch=%+v err=%v", batch, err)
		}
		capture := &userMemoryNoticeCapture{}
		store.WithUserMemoryMutationCallback(capture.callback)
		if saved, err := store.CompleteProfilePromotionBatch(ctx, *batch, []domainmemory.ProfileCandidate{{
			Type: domainmemory.UserMemoryTypePreference, Statement: "likes Go", Confidence: .8,
			Sensitivity: "normal", Scope: "all_personas",
		}}, "ren", now); err != nil || saved != 1 {
			t.Fatalf("saved=%d err=%v", saved, err)
		}
		memories, err := store.ListUserMemories(ctx, "ren", MemoryStateCandidate, true, 10)
		if err != nil || len(memories) != 1 {
			t.Fatalf("memories=%+v err=%v", memories, err)
		}
		assertUserMemoryNotice(t, capture, 0, "ren", memories[0].ID)
	})
}

func TestUserMemoryMutationCallbackChatGPTConfirmationsAreSynchronousAndNoReplayNotice(t *testing.T) {
	t.Run("raw evidence confirmation", func(t *testing.T) {
		fixture := newChatGPTConfirmFixture(t, "callback-confirm-owner", "callback-confirm-export", domainmemory.ChatGPTImportStateCompleted, true, domainmemory.ProfilePromotionCompleted)
		capture := &userMemoryNoticeCapture{}
		fixture.store.WithUserMemoryMutationCallback(capture.callback)
		input := confirmFixtureInput(fixture, "callback-confirm-request", true)
		result, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, fixture.owner, fixture.owner), input)
		if err != nil || result.Confirmed != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
		assertUserMemoryNotice(t, capture, 0, fixture.owner, fixture.candidateID)
		replay, err := fixture.store.ConfirmChatGPTImportCandidates(confirmTestContext(t, input.RequestID, fixture.owner, fixture.owner), input)
		if err != nil || !replay.IdempotentReplay {
			t.Fatalf("replay=%+v err=%v", replay, err)
		}
		if capture.completed != 1 {
			t.Fatalf("confirmation replay callback count=%d, want 1", capture.completed)
		}
	})

	t.Run("legacy ChatGPT confirmation", func(t *testing.T) {
		store := userMemoryCallbackTestStore(t, "user-memory-chatgpt-legacy-callback.db")
		evidence := ChatGPTL3ImportRecord{Format: ChatGPTL3ArtifactFormat, ExportID: "callback-export", EvidenceID: "chatgpt_export:callback-conv:user-1", ConversationID: "callback-conv", MessageID: "user-1", Role: "user", Text: "Ren likes movies", OnCurrentBranch: true}
		if _, err := store.ImportChatGPTL3Records(context.Background(), []ChatGPTL3ImportRecord{evidence}, true); err != nil {
			t.Fatal(err)
		}
		candidate, err := store.CreateUserMemory(context.Background(), domainmemory.CreateUserMemoryInput{
			UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Ren likes movies",
			State: MemoryStateCandidate, EvidenceEventIDs: []string{evidence.EvidenceID}, Confidence: .9,
			Sensitivity: "normal", Scope: "all_personas", Source: "profile_extractor",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.Exec(`UPDATE l1_profile_promotion_job SET state = ? WHERE evidence_event_id = ?`, domainmemory.ProfilePromotionCompleted, evidence.EvidenceID); err != nil {
			t.Fatal(err)
		}
		capture := &userMemoryNoticeCapture{}
		store.WithUserMemoryMutationCallback(capture.callback)
		if _, err := store.ConfirmChatGPTL3Candidates(context.Background(), "callback-export", "explicit confirmation", true); err != nil {
			t.Fatal(err)
		}
		assertUserMemoryNotice(t, capture, 0, "ren", candidate.ID)
	})
}

func TestUserMemoryMutationCallbackStorageHostPromotionAndValidatedStagingEligibility(t *testing.T) {
	t.Run("StorageHost promotion", func(t *testing.T) {
		ctx := context.Background()
		store := userMemoryCallbackTestStore(t, "user-memory-storagehost-promotion-callback.db")
		source, err := store.CreateUserMemory(ctx, userMemoryCallbackTestInput("ren", "storagehost promotion source"))
		if err != nil {
			t.Fatal(err)
		}
		capture := &userMemoryNoticeCapture{}
		store.WithUserMemoryMutationCallback(capture.callback)
		hash := sha256.Sum256([]byte("storagehost-promote-callback"))
		identity := ConversationL1OperationIdentity{OpID: "callback-promote", Operation: "promote_memory_to_namespace", PayloadSHA256: hex.EncodeToString(hash[:]), WriterGeneration: 3}
		promoted, err := store.PromoteMemoryToNamespaceForOperation(ctx, identity, source.ID, "user:ren", "owner")
		if err != nil {
			t.Fatal(err)
		}
		assertUserMemoryNotice(t, capture, 0, "ren", promoted.ID)
	})

	t.Run("validated staging user namespace", func(t *testing.T) {
		ctx := context.Background()
		store := userMemoryCallbackTestStore(t, "user-memory-staging-callback.db")
		capture := &userMemoryNoticeCapture{}
		store.WithUserMemoryMutationCallback(capture.callback)
		promote := func(eventID, statement string, meta map[string]interface{}) *L1MemoryEvent {
			t.Helper()
			item, err := store.SaveStagingItem(ctx, L1StagingItem{
				Kind: L1StagingKindMemoryCandidate, Namespace: "conv:staging-callback",
				EventID: eventID, SourceID: "user-provided", RawText: statement, SummaryDraft: statement,
				FetchedAt: time.Now().UTC(), LicenseNote: "user provided", Meta: meta,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ValidateStagingItem(ctx, item.ID, L1StagingValidationPolicy{
				SourceTrustScores: map[string]float64{"user-provided": 1}, MinimumTrustScore: .5, Now: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			promoted, err := store.PromoteValidatedStagingItemToMemory(ctx, item.ID, "user:ren", "validator")
			if err != nil {
				t.Fatal(err)
			}
			return promoted
		}
		validMeta := map[string]interface{}{
			"type": domainmemory.UserMemoryTypePreference, "user_id": "ren", "statement": "Likes Go",
			"evidence_event_ids": []string{"staging-callback-evidence"}, "confidence": .8,
			"sensitivity": "normal", "scope": "all_personas", "active": true,
		}
		promoted := promote("staging-callback-event", "Likes Go", validMeta)
		assertUserMemoryNotice(t, capture, 0, "ren", promoted.ID)

		invalidMeta := map[string]interface{}{
			"type": domainmemory.UserMemoryTypePreference, "statement": "Missing user owner",
			"evidence_event_ids": []string{"staging-invalid-evidence"}, "confidence": .8,
			"sensitivity": "normal", "scope": "all_personas", "active": true,
		}
		_ = promote("staging-invalid-event", "Missing user owner", invalidMeta)
		inactiveMeta := map[string]interface{}{
			"type": domainmemory.UserMemoryTypePreference, "user_id": "ren", "statement": "Inactive preference",
			"evidence_event_ids": []string{"staging-inactive-evidence"}, "confidence": .8,
			"sensitivity": "normal", "scope": "all_personas", "active": false,
		}
		_ = promote("staging-inactive-event", "Inactive preference", inactiveMeta)
		if capture.completed != 1 {
			t.Fatalf("strict-invalid or prompt-ineligible staging rows notified: %+v", capture.notices)
		}
	})
}

func TestUserMemoryMutationCallbackCoversGenericMessageStorePaths(t *testing.T) {
	t.Run("SaveMessage", func(t *testing.T) {
		ctx := context.Background()
		store := userMemoryCallbackTestStore(t, "user-memory-save-message-callback.db")
		thread := l1TestThread("generic-user-memory-save", 1)
		sessionID := l1TestSessionID("generic-user-memory-save")
		statement := "saved strict user memory"
		meta := userMemoryCallbackTestMeta("ren", statement)
		notice := assertUserMemoryCallbackBlocksSuccessReturn(t, store, func() error {
			return store.SaveMessage(ctx, sessionID, thread.ID, thread.Seq, thread.Kind, "user:ren",
				domconv.NewMessage(domconv.SpeakerMemory, statement, meta), MemoryStateCandidate)
		})
		var memoryID string
		if err := store.db.QueryRowContext(ctx, `SELECT id FROM l1_memory_event WHERE namespace = ? AND message = ?`, "user:ren", statement).Scan(&memoryID); err != nil {
			t.Fatal(err)
		}
		assertUserMemoryNotice(t, &userMemoryNoticeCapture{notices: []UserMemoryMutationNotice{notice}, completed: 1}, 0, "ren", memoryID)
		var eventType string
		if err := store.db.QueryRowContext(ctx, `SELECT event_type FROM l1_event_log WHERE id = ?`, notice.ChangeID).Scan(&eventType); err != nil || eventType != "memory.message_saved" {
			t.Fatalf("notice change event=%q err=%v", eventType, err)
		}
	})

	t.Run("UpdateMemoryState", func(t *testing.T) {
		ctx := context.Background()
		store := userMemoryCallbackTestStore(t, "user-memory-update-message-callback.db")
		memory, err := store.CreateUserMemory(ctx, userMemoryCallbackTestInput("ren", "generic update state"))
		if err != nil {
			t.Fatal(err)
		}
		notice := assertUserMemoryCallbackBlocksSuccessReturn(t, store, func() error {
			return store.UpdateMemoryState(ctx, memory.ID, MemoryStateConfirmed)
		})
		assertUserMemoryNotice(t, &userMemoryNoticeCapture{notices: []UserMemoryMutationNotice{notice}, completed: 1}, 0, "ren", memory.ID)
		var eventType string
		if err := store.db.QueryRowContext(ctx, `SELECT event_type FROM l1_event_log WHERE id = ?`, notice.ChangeID).Scan(&eventType); err != nil || eventType != "memory.state_updated" {
			t.Fatalf("notice change event=%q err=%v", eventType, err)
		}
	})

	t.Run("PromoteMemoryToNamespace", func(t *testing.T) {
		ctx := context.Background()
		store := userMemoryCallbackTestStore(t, "user-memory-promote-message-callback.db")
		source, err := store.CreateUserMemory(ctx, userMemoryCallbackTestInput("ren", "generic promote memory"))
		if err != nil {
			t.Fatal(err)
		}
		var promoted *L1MemoryEvent
		notice := assertUserMemoryCallbackBlocksSuccessReturn(t, store, func() error {
			var promoteErr error
			promoted, promoteErr = store.PromoteMemoryToNamespace(ctx, source.ID, "user:ren", "owner")
			return promoteErr
		})
		if promoted == nil {
			t.Fatal("promotion returned no memory")
		}
		assertUserMemoryNotice(t, &userMemoryNoticeCapture{notices: []UserMemoryMutationNotice{notice}, completed: 1}, 0, "ren", promoted.ID)
		var eventType string
		if err := store.db.QueryRowContext(ctx, `SELECT event_type FROM l1_event_log WHERE id = ?`, notice.ChangeID).Scan(&eventType); err != nil || eventType != "memory.promoted" {
			t.Fatalf("notice change event=%q err=%v", eventType, err)
		}
	})
}

func TestUserMemoryMutationCallbackGenericMessageWritesRollbackWithoutNotice(t *testing.T) {
	ctx := context.Background()
	store := userMemoryCallbackTestStore(t, "user-memory-generic-write-rollback.db")
	callbackCalls := 0
	store.WithUserMemoryMutationCallback(func(UserMemoryMutationNotice) { callbackCalls++ })
	if _, err := store.db.ExecContext(ctx, `
CREATE TRIGGER reject_generic_user_memory_audit
BEFORE INSERT ON l1_event_log
WHEN NEW.event_type IN ('memory.message_saved', 'memory.state_updated', 'memory.promoted')
BEGIN
	SELECT RAISE(ABORT, 'intentional generic UserMemory audit failure');
END`); err != nil {
		t.Fatal(err)
	}
	thread := l1TestThread("generic-user-memory-rollback", 1)
	if err := store.SaveMessage(ctx, l1TestSessionID("generic-user-memory-rollback"), thread.ID, thread.Seq, thread.Kind, "user:ren",
		domconv.NewMessage(domconv.SpeakerMemory, "save rollback", userMemoryCallbackTestMeta("ren", "save rollback")), MemoryStateCandidate); err == nil {
		t.Fatal("SaveMessage succeeded despite audit trigger")
	}
	if got := queryInt(t, store, `SELECT count(*) FROM l1_memory_event WHERE namespace = 'user:ren' AND message = 'save rollback'`); got != 0 {
		t.Fatalf("SaveMessage rollback left %d UserMemory rows", got)
	}

	stateMemory, err := store.CreateUserMemory(ctx, userMemoryCallbackTestInput("ren", "state rollback"))
	if err != nil {
		t.Fatal(err)
	}
	callbackCalls = 0
	if err := store.UpdateMemoryState(ctx, stateMemory.ID, MemoryStateConfirmed); err == nil {
		t.Fatal("UpdateMemoryState succeeded despite audit trigger")
	}
	currentState, found, err := store.FindUserMemoryByID(ctx, stateMemory.ID)
	if err != nil || !found || currentState.State != MemoryStateCandidate {
		t.Fatalf("state after failed update=%+v found=%t err=%v; want candidate", currentState, found, err)
	}

	promoteSource, err := store.CreateUserMemory(ctx, userMemoryCallbackTestInput("ren", "promotion rollback source"))
	if err != nil {
		t.Fatal(err)
	}
	callbackCalls = 0
	if _, err := store.PromoteMemoryToNamespace(ctx, promoteSource.ID, "user:ren", "owner"); err == nil {
		t.Fatal("PromoteMemoryToNamespace succeeded despite audit trigger")
	}
	if got := queryInt(t, store, `SELECT count(*) FROM l1_memory_event WHERE namespace = 'user:ren' AND source = 'promoter' AND meta_json LIKE '%promotion rollback source%'`); got != 0 {
		t.Fatalf("promotion rollback left %d promoted rows", got)
	}
	if callbackCalls != 0 {
		t.Fatalf("failed generic write callback calls=%d, want 0", callbackCalls)
	}
}
