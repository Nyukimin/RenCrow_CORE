package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type turnTestArchiveStore struct {
	*mockArchiveSQLiteStore
	byID      map[modulecore.ThreadID]*domconv.ThreadSummary
	getCalls  int
	saveCalls int
	saveErr   error
}

type admittedTurnCommitGateStore struct {
	*l1sqlite.L1SQLiteStore
	mu       sync.Mutex
	calls    int
	started  chan domconv.ConversationTurnRequest
	release  chan struct{}
	release1 sync.Once
	fail     error
}

type orderedTurnCommitGateStore struct {
	*l1sqlite.L1SQLiteStore
	mu            sync.Mutex
	calls         int
	started       chan int
	releaseFirst  chan struct{}
	releaseSecond chan struct{}
	firstOnce     sync.Once
	secondOnce    sync.Once
	readCalls     int
}

type blockingActiveProjectionStore struct {
	*l1sqlite.L1SQLiteStore
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	loads   int
	commits int
	failOn  int
}

func (s *blockingActiveProjectionStore) LoadActiveConversationThreadProjection(ctx context.Context, sessionID string) ([]l1sqlite.L1MemoryEvent, error) {
	s.mu.Lock()
	s.loads++
	loads := s.loads
	s.mu.Unlock()
	select {
	case s.started <- struct{}{}:
	default:
	}
	if s.release != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if loads == s.failOn {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	return s.L1SQLiteStore.LoadActiveConversationThreadProjection(ctx, sessionID)
}

func (s *blockingActiveProjectionStore) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	s.mu.Lock()
	s.commits++
	s.mu.Unlock()
	return s.L1SQLiteStore.CommitConversationTurn(ctx, request)
}

func (s *blockingActiveProjectionStore) loadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loads
}

func (s *blockingActiveProjectionStore) commitCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commits
}

type conversationTurnPreparationManager interface {
	PrepareConversationTurn(context.Context, modulecore.TurnID, string, string) (*domconv.ConversationTurnPreparation, error)
	ReleaseConversationTurnPreparation(context.Context, *domconv.ConversationTurnPreparation) error
}

func prepareManagerTurnForTest(t *testing.T, ctx context.Context, manager *RealConversationManager, request *domconv.ConversationTurnRequest) {
	t.Helper()
	preparation, err := manager.PrepareConversationTurn(ctx, request.TurnID, request.SessionID, request.OwnerID)
	if err != nil {
		t.Fatalf("PrepareConversationTurn(%s): %v", request.TurnID, err)
	}
	request.Preparation = preparation
}

func (s *admittedTurnCommitGateStore) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	s.started <- request
	if call == 1 && request.ThreadPlan != nil {
		select {
		case <-s.release:
		case <-ctx.Done():
			return domconv.ConversationTurnResult{TurnID: request.TurnID, Status: domconv.ConversationTurnFailed}, ctx.Err()
		}
		if s.fail != nil {
			return domconv.ConversationTurnResult{TurnID: request.TurnID, Status: domconv.ConversationTurnFailed}, s.fail
		}
	}
	return s.L1SQLiteStore.CommitConversationTurn(ctx, request)
}

func (s *admittedTurnCommitGateStore) unblock() {
	s.release1.Do(func() { close(s.release) })
}

func (s *orderedTurnCommitGateStore) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()
	s.started <- call
	var release <-chan struct{}
	if call == 1 {
		release = s.releaseFirst
	} else if call == 2 {
		release = s.releaseSecond
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return domconv.ConversationTurnResult{TurnID: request.TurnID, Status: domconv.ConversationTurnFailed}, ctx.Err()
		}
	}
	return s.L1SQLiteStore.CommitConversationTurn(ctx, request)
}

func (s *orderedTurnCommitGateStore) LoadActiveConversationThreadProjection(ctx context.Context, sessionID string) ([]l1sqlite.L1MemoryEvent, error) {
	s.mu.Lock()
	s.readCalls++
	s.mu.Unlock()
	return s.L1SQLiteStore.LoadActiveConversationThreadProjection(ctx, sessionID)
}

func (s *orderedTurnCommitGateStore) unblock() {
	s.firstOnce.Do(func() { close(s.releaseFirst) })
	s.secondOnce.Do(func() { close(s.releaseSecond) })
}

func (s *orderedTurnCommitGateStore) activeReadCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readCalls
}

func newConversationTurnAdmission(t *testing.T, commandCount int) (*persistencequeue.Queue[persistencequeue.PersistenceCommand], *persistencequeue.Admission, context.Context) {
	t.Helper()
	queue, err := persistencequeue.NewPersistenceQueue(8, 4<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue: %v", err)
	}
	admission, err := persistencequeue.Admit(queue, commandCount, 4<<20)
	if err != nil {
		t.Fatalf("Admit: %v", err)
	}
	t.Cleanup(func() {
		_ = admission.Release()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = queue.Shutdown(ctx)
	})
	return queue, admission, persistencequeue.WithAdmission(context.Background(), admission)
}

func TestRealConversationManagerPreparesColdHeadOutsideGlobalOverlayLockAndAcceptsWithoutSecondRead(t *testing.T) {
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "prepared-head.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	sessionID := string(modulecore.NewSessionID())
	seed := managerTurnTestRequest("prepared-head-seed", sessionID)
	seedResult, err := l1.CommitConversationTurn(context.Background(), seed)
	if err != nil {
		t.Fatalf("seed active thread: %v", err)
	}
	store := &blockingActiveProjectionStore{L1SQLiteStore: l1, started: make(chan struct{}, 2), release: make(chan struct{})}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: store}
	preparer, ok := interface{}(manager).(conversationTurnPreparationManager)
	if !ok {
		t.Fatal("RealConversationManager does not implement conversation turn preparation")
	}

	request := managerTurnTestRequest("prepared-head-accepted", sessionID)
	request.Targets = manager.ConversationTurnTargets()
	type preparationResult struct {
		value *domconv.ConversationTurnPreparation
		err   error
	}
	prepared := make(chan preparationResult, 1)
	go func() {
		value, err := preparer.PrepareConversationTurn(context.Background(), request.TurnID, request.SessionID, request.OwnerID)
		prepared <- preparationResult{value: value, err: err}
	}()
	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("cold active-head read did not start")
	}

	lockAcquired := make(chan struct{})
	go func() {
		manager.turnOverlayMu.Lock()
		manager.turnOverlayMu.Unlock()
		close(lockAcquired)
	}()
	select {
	case <-lockAcquired:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("cold L1 read held the global turn overlay lock")
	}

	other := managerTurnTestRequest("prepared-head-competing", sessionID)
	if _, err := preparer.PrepareConversationTurn(context.Background(), other.TurnID, other.SessionID, other.OwnerID); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("competing preparation error=%v, want session conflict", err)
	}
	if _, err := manager.CommitConversationTurn(context.Background(), other); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("synchronous route competing with preparation error=%v, want session conflict", err)
	}

	close(store.release)
	result := <-prepared
	if result.err != nil || result.value == nil || result.value.ActiveThread == nil {
		t.Fatalf("prepared=%+v err=%v, want cold active head", result.value, result.err)
	}
	if result.value.ActiveThread.ID != seedResult.ThreadID || result.value.Revision != 0 {
		t.Fatalf("prepared head=%+v revision=%d, want seed thread %s at revision zero", result.value.ActiveThread, result.value.Revision, seedResult.ThreadID)
	}

	_, _, admittedCtx := newConversationTurnAdmission(t, 1)
	recorder := turntiming.New(time.Now())
	admittedCtx = turntiming.WithRecorder(admittedCtx, recorder)
	request.Preparation = result.value
	accepted, err := manager.CommitConversationTurn(admittedCtx, request)
	if err != nil || accepted.Status != domconv.ConversationTurnAccepted || accepted.ThreadID != seedResult.ThreadID {
		t.Fatalf("accepted=%+v err=%v, want accepted on prepared RAM head", accepted, err)
	}
	if loads := store.loadCount(); loads != 1 {
		t.Fatalf("active projection loads=%d, want exactly the single pre-generation read", loads)
	}
	points := recorder.Snapshot().PointsMS
	for _, point := range []turntiming.Point{turntiming.PointConversationRAMAcceptStarted, turntiming.PointConversationRAMAcceptEnded} {
		if _, ok := points[string(point)]; !ok {
			t.Errorf("missing timing point %q: %#v", point, points)
		}
	}
}

func TestRealConversationManagerPreparationReadFailureReleasesSessionReservation(t *testing.T) {
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "prepared-read-failure.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	manager := &RealConversationManager{
		redisStore: newMockRedisStore(),
		l1Store:    &failingRecallProjectionStore{L1SQLiteStore: l1},
	}
	preparer, ok := interface{}(manager).(conversationTurnPreparationManager)
	if !ok {
		t.Fatal("RealConversationManager does not implement conversation turn preparation")
	}
	request := managerTurnTestRequest("prepared-read-failure", string(modulecore.NewSessionID()))
	if _, err := preparer.PrepareConversationTurn(context.Background(), request.TurnID, request.SessionID, request.OwnerID); err == nil {
		t.Fatal("PrepareConversationTurn() error=nil, want cold L1 read failure")
	}
	manager.turnOverlayMu.RLock()
	_, reserved := manager.turnOverlays[request.SessionID]
	manager.turnOverlayMu.RUnlock()
	if reserved {
		t.Fatal("failed cold read left a session reservation behind")
	}
}

func TestRealConversationManagerAdmittedCommitWithoutPreparationDoesNotReadL1(t *testing.T) {
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "missing-preparation.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	store := &blockingActiveProjectionStore{L1SQLiteStore: l1, started: make(chan struct{}, 1), release: make(chan struct{})}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: store}
	_, _, admittedCtx := newConversationTurnAdmission(t, 1)
	commitCtx, cancel := context.WithTimeout(admittedCtx, 100*time.Millisecond)
	defer cancel()
	request := managerTurnTestRequest("admitted-missing-preparation", string(modulecore.NewSessionID()))
	request.Targets = manager.ConversationTurnTargets()

	result, err := manager.CommitConversationTurn(commitCtx, request)
	if !errors.Is(err, domconv.ErrConversationTurnUnavailable) || result.ErrorCode != domconv.ConversationTurnErrorUnavailable {
		t.Fatalf("result=%+v err=%v, want immediate unavailable without a preparation lease", result, err)
	}
	if loads := store.loadCount(); loads != 0 {
		t.Fatalf("active projection loads=%d, want no EndTurn L1 read", loads)
	}
}

func TestRealConversationManagerWorkerCompletionKeepsPreparedRAMHead(t *testing.T) {
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "prepared-worker-completion.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	gate := &admittedTurnCommitGateStore{L1SQLiteStore: l1, started: make(chan domconv.ConversationTurnRequest, 2), release: make(chan struct{})}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: gate}
	_, admission, ctx := newConversationTurnAdmission(t, 2)
	defer gate.unblock()

	firstRequest := managerTurnTestRequest("prepared-worker-first", string(modulecore.NewSessionID()))
	firstRequest.Targets = manager.ConversationTurnTargets()
	prepareManagerTurnForTest(t, ctx, manager, &firstRequest)
	first, err := manager.CommitConversationTurn(ctx, firstRequest)
	if err != nil || first.Status != domconv.ConversationTurnAccepted {
		t.Fatalf("first=%+v err=%v, want accepted", first, err)
	}
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("persistence worker did not reach the blocked writer")
	}

	preparer := conversationTurnPreparationManager(manager)
	secondRequest := managerTurnTestRequest("prepared-worker-second", firstRequest.SessionID)
	secondRequest.Targets = manager.ConversationTurnTargets()
	preparation, err := preparer.PrepareConversationTurn(ctx, secondRequest.TurnID, secondRequest.SessionID, secondRequest.OwnerID)
	if err != nil || preparation == nil || preparation.ActiveThread == nil || len(preparation.ActiveThread.Turns) != 2 {
		t.Fatalf("preparation=%+v err=%v, want first accepted RAM head", preparation, err)
	}

	gate.unblock()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("first admission barrier: %v", err)
	}
	manager.turnOverlayMu.RLock()
	overlay := manager.turnOverlays[firstRequest.SessionID]
	kept := overlay != nil && overlay.preparation != nil && sameConversationTurnPreparation(overlay.preparation, preparation) &&
		overlay.head != nil && len(overlay.head.Turns) == 2 && overlay.head.Turns[1].Msg == firstRequest.AgentMessage
	manager.turnOverlayMu.RUnlock()
	if !kept {
		t.Fatal("worker completion removed or replaced the RAM head held by the preparation")
	}

	secondRequest.Preparation = preparation
	second, err := manager.CommitConversationTurn(ctx, secondRequest)
	if err != nil || second.Status != domconv.ConversationTurnAccepted || second.ThreadID != first.ThreadID {
		t.Fatalf("second=%+v err=%v, want accepted against the prepared thread", second, err)
	}
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("second admission barrier: %v", err)
	}
}

func TestRealConversationManagerCombinesCommittedAndPendingHeadsWithoutRewind(t *testing.T) {
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "committed-head-projection.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	sessionID := string(modulecore.NewSessionID())
	seed := managerTurnTestRequest("committed-head-projection-seed", sessionID)
	seedResult, err := l1.CommitConversationTurn(context.Background(), seed)
	if err != nil {
		t.Fatalf("seed active thread: %v", err)
	}
	store := &orderedTurnCommitGateStore{
		L1SQLiteStore: l1, started: make(chan int, 3),
		releaseFirst: make(chan struct{}), releaseSecond: make(chan struct{}),
	}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: store}
	_, admission, ctx := newConversationTurnAdmission(t, 3)
	defer store.unblock()

	firstRequest := managerTurnTestRequest("committed-head-projection-first", sessionID)
	firstRequest.Targets = manager.ConversationTurnTargets()
	prepareManagerTurnForTest(t, ctx, manager, &firstRequest)
	if loads := store.activeReadCount(); loads != 1 {
		t.Fatalf("initial active-head reads=%d, want one cold read", loads)
	}
	first, err := manager.CommitConversationTurn(ctx, firstRequest)
	if err != nil || first.Status != domconv.ConversationTurnAccepted || first.ThreadID != seedResult.ThreadID {
		t.Fatalf("first=%+v err=%v, want accepted on seed thread", first, err)
	}
	select {
	case call := <-store.started:
		if call != 1 {
			t.Fatalf("first writer call=%d, want 1", call)
		}
	case <-time.After(time.Second):
		t.Fatal("first persistence call did not reach its gate")
	}

	secondRequest := managerTurnTestRequest("committed-head-projection-second", sessionID)
	secondRequest.Targets = manager.ConversationTurnTargets()
	readsBeforeSecondPreparation := store.activeReadCount()
	secondPreparation, err := manager.PrepareConversationTurn(ctx, secondRequest.TurnID, secondRequest.SessionID, secondRequest.OwnerID)
	if err != nil || secondPreparation == nil || secondPreparation.ActiveThread == nil || len(secondPreparation.ActiveThread.Turns) != 4 ||
		secondPreparation.ActiveThread.Turns[3].Msg != firstRequest.AgentMessage {
		t.Fatalf("second preparation=%+v err=%v, want first accepted turn in RAM head", secondPreparation, err)
	}
	if loads := store.activeReadCount(); loads != readsBeforeSecondPreparation {
		t.Fatalf("second preparation changed active-head reads from %d to %d", readsBeforeSecondPreparation, loads)
	}
	secondRequest.Preparation = secondPreparation
	second, err := manager.CommitConversationTurn(ctx, secondRequest)
	if err != nil || second.Status != domconv.ConversationTurnAccepted || second.ThreadID != first.ThreadID {
		t.Fatalf("second=%+v err=%v, want accepted on first turn thread", second, err)
	}

	store.firstOnce.Do(func() { close(store.releaseFirst) })
	select {
	case call := <-store.started:
		if call != 2 {
			t.Fatalf("second writer call=%d, want 2", call)
		}
	case <-time.After(time.Second):
		t.Fatal("second persistence call did not reach its gate after first completed")
	}

	thirdRequest := managerTurnTestRequest("committed-head-projection-third", sessionID)
	thirdRequest.Targets = manager.ConversationTurnTargets()
	readsBeforeThirdPreparation := store.activeReadCount()
	thirdPreparation, err := manager.PrepareConversationTurn(ctx, thirdRequest.TurnID, thirdRequest.SessionID, thirdRequest.OwnerID)
	if err != nil || thirdPreparation == nil || thirdPreparation.ActiveThread == nil || len(thirdPreparation.ActiveThread.Turns) != 6 ||
		thirdPreparation.ActiveThread.Turns[5].Msg != secondRequest.AgentMessage {
		t.Fatalf("third preparation=%+v err=%v, want second pending turn retained after first durable completion", thirdPreparation, err)
	}
	if loads := store.activeReadCount(); loads != readsBeforeThirdPreparation {
		t.Fatalf("third preparation changed active-head reads from %d to %d", readsBeforeThirdPreparation, loads)
	}
	thirdRequest.Preparation = thirdPreparation
	third, err := manager.CommitConversationTurn(ctx, thirdRequest)
	if err != nil || third.Status != domconv.ConversationTurnAccepted || third.ThreadID != first.ThreadID {
		t.Fatalf("third=%+v err=%v, want accepted on same thread", third, err)
	}

	store.secondOnce.Do(func() { close(store.releaseSecond) })
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("admission barrier: %v", err)
	}

	fourthRequest := managerTurnTestRequest("committed-head-projection-fourth", sessionID)
	readsBeforeFourthPreparation := store.activeReadCount()
	fourthPreparation, err := manager.PrepareConversationTurn(ctx, fourthRequest.TurnID, fourthRequest.SessionID, fourthRequest.OwnerID)
	if err != nil || fourthPreparation == nil || fourthPreparation.ActiveThread == nil || len(fourthPreparation.ActiveThread.Turns) != 8 ||
		fourthPreparation.ActiveThread.Turns[7].Msg != thirdRequest.AgentMessage {
		t.Fatalf("fourth preparation=%+v err=%v, want all durable turns in committed head", fourthPreparation, err)
	}
	if loads := store.activeReadCount(); loads != readsBeforeFourthPreparation {
		t.Fatalf("fourth preparation changed active-head reads from %d to %d after all turns became durable", readsBeforeFourthPreparation, loads)
	}
}

func TestRealConversationManagerSynchronousCommitProjectsCommittedHeadWithoutPostCommitRead(t *testing.T) {
	for _, prepared := range []bool{false, true} {
		name := "cold"
		if prepared {
			name = "prepared"
		}
		t.Run(name, func(t *testing.T) {
			l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "synchronous-committed-head.db"))
			if err != nil {
				t.Fatalf("NewL1SQLiteStore: %v", err)
			}
			defer l1.Close()
			store := &blockingActiveProjectionStore{L1SQLiteStore: l1, started: make(chan struct{}, 4), failOn: 3}
			manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: store}

			request := managerTurnTestRequest("synchronous-committed-head", string(modulecore.NewSessionID()))
			request.Targets = manager.ConversationTurnTargets()
			if prepared {
				prepareManagerTurnForTest(t, context.Background(), manager, &request)
			}
			committed, err := manager.CommitConversationTurn(context.Background(), request)
			if err != nil || committed.Status != domconv.ConversationTurnCompleted {
				t.Fatalf("committed=%+v err=%v, want completed durable turn without a post-commit read error", committed, err)
			}
			if reads := store.loadCount(); reads != 2 {
				t.Fatalf("active-head reads=%d, want cold/prepared load plus required Redis projection only", reads)
			}
			if commits := store.commitCount(); commits != 1 {
				t.Fatalf("durable commit calls=%d, want one write", commits)
			}

			manager.turnOverlayMu.RLock()
			overlay := manager.turnOverlays[request.SessionID]
			var head *domconv.Thread
			if overlay != nil {
				head = cloneThreadForStore(overlay.head)
			}
			manager.turnOverlayMu.RUnlock()
			if head == nil || head.ID != committed.ThreadID || len(head.Turns) != 2 || head.Turns[1].Msg != request.AgentMessage {
				t.Fatalf("committed RAM head=%+v, want the durable thread %s", head, committed.ThreadID)
			}

			next := managerTurnTestRequest("synchronous-committed-head-next", request.SessionID)
			preparation, err := manager.PrepareConversationTurn(context.Background(), next.TurnID, next.SessionID, next.OwnerID)
			if err != nil || preparation == nil || preparation.ActiveThread == nil || preparation.ActiveThread.ID != committed.ThreadID || len(preparation.ActiveThread.Turns) != 2 {
				t.Fatalf("next preparation=%+v err=%v, want retained durable head", preparation, err)
			}
			if reads := store.loadCount(); reads != 2 {
				t.Fatalf("next preparation changed active-head reads to %d, want no extra read", reads)
			}
		})
	}
}

func TestRealConversationManagerColdExistingSessionWithoutActiveHeadFailsClosed(t *testing.T) {
	for _, existing := range []string{"typed turn receipt", "legacy L1 thread reference"} {
		t.Run(existing, func(t *testing.T) {
			l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "existing-session.db"))
			if err != nil {
				t.Fatalf("NewL1SQLiteStore: %v", err)
			}
			defer l1.Close()
			sessionID := string(modulecore.NewSessionID())
			if existing == "typed turn receipt" {
				if _, err := l1.CommitConversationTurn(context.Background(), managerTurnTestRequest("existing-head-receipt", sessionID)); err != nil {
					t.Fatalf("seed existing typed session: %v", err)
				}
			} else {
				threadID := modulecore.NewThreadID()
				message := domconv.NewMessage(domconv.SpeakerUser, "existing legacy message", nil)
				if err := l1.SaveMessage(context.Background(), sessionID, threadID, 1, modulecore.ThreadKindUserConversation, "conv:"+string(threadID), message, l1sqlite.MemoryStateObserved); err != nil {
					t.Fatalf("seed existing legacy thread: %v", err)
				}
			}

			manager := &RealConversationManager{
				redisStore: newMockRedisStore(),
				l1Store:    &missingActiveRecallProjectionStore{L1SQLiteStore: l1},
			}
			nextTurn := managerTurnTestRequest("existing-session-next-turn", sessionID)
			preparation, err := manager.PrepareConversationTurn(context.Background(), nextTurn.TurnID, sessionID, "owner-manager")
			if preparation != nil || !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
				t.Fatalf("preparation=%+v err=%v, want unavailable instead of an empty new head", preparation, err)
			}
			manager.turnOverlayMu.RLock()
			_, reserved := manager.turnOverlays[sessionID]
			manager.turnOverlayMu.RUnlock()
			if reserved {
				t.Fatal("failed activation left an empty head reservation behind")
			}

			unprepared := managerTurnTestRequest("existing-session-unprepared-turn", sessionID)
			unprepared.Targets = manager.ConversationTurnTargets()
			result, err := manager.CommitConversationTurn(context.Background(), unprepared)
			if result.Status != domconv.ConversationTurnFailed || !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
				t.Fatalf("unprepared commit=%+v err=%v, want fail-closed for existing session without active head", result, err)
			}
		})
	}
}

func TestRealConversationEngineBeginTurnReadsOwnedRAMHeadWithoutQueueAdmission(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "owner-scoped-head.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()

	sessionID := string(modulecore.NewSessionID())
	ownerID := "owner-manager"
	seed := managerTurnTestRequest("owner-scoped-head-seed", sessionID)
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed durable head: %v", err)
	}
	gate := &admittedTurnCommitGateStore{
		L1SQLiteStore: l1,
		started:       make(chan domconv.ConversationTurnRequest, 1),
		release:       make(chan struct{}),
	}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: gate}
	if err := manager.redisStore.SaveSession(ctx, domconv.NewSessionConversation(sessionID, ownerID)); err != nil {
		t.Fatalf("save persisted session owner: %v", err)
	}
	_, _, admittedCtx := newConversationTurnAdmission(t, 1)
	defer gate.unblock()

	request := managerTurnTestRequest("owner-scoped-head-pending", sessionID)
	request.Targets = manager.ConversationTurnTargets()
	prepareManagerTurnForTest(t, admittedCtx, manager, &request)
	accepted, err := manager.CommitConversationTurn(admittedCtx, request)
	if err != nil || accepted.Status != domconv.ConversationTurnAccepted {
		t.Fatalf("accepted=%+v err=%v, want admitted pending turn", accepted, err)
	}
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("persistence writer did not reach the blocked commit")
	}

	engine := NewRealConversationEngine(manager, domconv.PersonaState{}).WithUserMemoryStore(nil, ownerID)
	pack, err := engine.BeginTurn(context.Background(), sessionID, "continue")
	if err != nil {
		t.Fatalf("BeginTurn without queue admission: %v", err)
	}
	if !hasRecallMessage(pack.ShortContext, request.UserMessage) || !hasRecallMessage(pack.ShortContext, request.AgentMessage) {
		t.Fatalf("owner CHAT recall omitted the retained pending head: %#v", pack.ShortContext)
	}
	if _, err := manager.recallForOwner(context.Background(), sessionID, "different-owner", "continue", 3); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("wrong-owner recall error=%v, want owner conflict", err)
	}

	// Session-only reads remain durable-only. The owner-scoped API is the
	// capability that allows ordinary authenticated CHAT to read pending RAM.
	durable, err := manager.LoadActiveConversationThread(context.Background(), sessionID)
	if err != nil || durable == nil || hasThreadMessage(durable, request.AgentMessage) {
		t.Fatalf("unscoped active head=%+v err=%v, want durable-only view", durable, err)
	}
}

func TestRealConversationManagerColdOwnerRecallUsesCanonicalL1HeadInsteadOfStaleRedis(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "cold-owner-recall.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()

	sessionID := string(modulecore.NewSessionID())
	ownerID := "owner-cold-recall"
	seed := managerTurnTestRequest("cold-owner-recall-seed", sessionID)
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed canonical L1 head: %v", err)
	}

	redis := newMockRedisStore()
	stale, err := domconv.NewThread(sessionID, "stale-redis", domconv.ThreadKindUserConversation, 1)
	if err != nil {
		t.Fatalf("NewThread stale Redis head: %v", err)
	}
	if err := stale.AddMessage(domconv.NewMessage(domconv.SpeakerUser, "stale Redis user message", nil)); err != nil {
		t.Fatalf("add stale Redis user message: %v", err)
	}
	if err := stale.AddMessage(domconv.NewMessage(domconv.SpeakerMio, "stale Redis agent message", nil)); err != nil {
		t.Fatalf("add stale Redis agent message: %v", err)
	}
	session := domconv.NewSessionConversation(sessionID, ownerID)
	session.LastThreadID = stale.ID
	session.LastThreadSeq = stale.ThreadSeq
	session.LastThreadKind = stale.ThreadKind
	if err := redis.SaveThread(ctx, stale); err != nil {
		t.Fatalf("save stale Redis thread: %v", err)
	}
	if err := redis.SaveSession(ctx, session); err != nil {
		t.Fatalf("save persisted session owner: %v", err)
	}

	manager := &RealConversationManager{redisStore: redis, l1Store: l1}
	engine := NewRealConversationEngine(manager, domconv.PersonaState{}).WithUserMemoryStore(nil, ownerID)
	pack, err := engine.BeginTurn(ctx, sessionID, "continue")
	if err != nil {
		t.Fatalf("BeginTurn cold owner recall: %v", err)
	}
	if !hasRecallMessage(pack.ShortContext, seed.UserMessage) || !hasRecallMessage(pack.ShortContext, seed.AgentMessage) {
		t.Fatalf("cold owner recall omitted canonical L1 head: %#v", pack.ShortContext)
	}
	if hasRecallMessage(pack.ShortContext, "stale Redis user message") || hasRecallMessage(pack.ShortContext, "stale Redis agent message") {
		t.Fatalf("cold owner recall included stale Redis head: %#v", pack.ShortContext)
	}
}

func TestRealConversationManagerColdOwnerPreparationLoadsResetHeadAfterRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "cold-owner-reset-head.db")
	l1, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer func() {
		if l1 != nil {
			_ = l1.Close()
		}
	}()

	sessionID := string(modulecore.NewSessionID())
	ownerID := "owner-reset-restart"
	seed := managerTurnTestRequest("cold-owner-reset-seed", sessionID)
	seed.OwnerID = ownerID
	seedResult, err := l1.CommitConversationTurn(ctx, seed)
	if err != nil {
		t.Fatalf("seed durable head: %v", err)
	}
	reset, err := l1.ResetConversationHead(ctx, domconv.ConversationHeadResetRequest{
		OperationID:       "cold-owner-reset-operation",
		SessionID:         sessionID,
		OwnerID:           ownerID,
		ExpectedThreadID:  seedResult.ThreadID,
		ExpectedThreadSeq: seedResult.ThreadSeq,
	})
	if err != nil {
		t.Fatalf("ResetConversationHead: %v", err)
	}

	redis := newMockRedisStore()
	if err := redis.SaveSession(ctx, domconv.NewSessionConversation(sessionID, ownerID)); err != nil {
		t.Fatalf("save persisted session owner: %v", err)
	}
	if err := l1.Close(); err != nil {
		t.Fatalf("close L1 before manager restart: %v", err)
	}
	l1 = nil
	l1, err = l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen L1: %v", err)
	}

	manager := &RealConversationManager{redisStore: redis, l1Store: l1}
	thread, err := manager.loadActiveConversationThreadForOwner(ctx, sessionID, ownerID)
	if err != nil {
		t.Fatalf("load reset head after manager restart: %v", err)
	}
	if thread == nil || thread.ID != reset.ActiveThread.ID || thread.ThreadSeq != reset.ActiveThread.Seq ||
		thread.ThreadKind != reset.ActiveThread.Kind || thread.Domain != reset.Domain || len(thread.Turns) != 0 {
		t.Fatalf("reset thread=%+v, want typed empty head %+v", thread, reset)
	}

	preparation, err := manager.PrepareConversationTurn(ctx, modulecore.NewTurnID(), sessionID, ownerID)
	if err != nil {
		t.Fatalf("prepare reset head after manager restart: %v", err)
	}
	if preparation == nil || preparation.ActiveThread == nil || preparation.ActiveThread.ID != reset.ActiveThread.ID ||
		preparation.ActiveThread.ThreadSeq != reset.ActiveThread.Seq || len(preparation.ActiveThread.Turns) != 0 {
		t.Fatalf("preparation=%+v, want the durable empty reset head", preparation)
	}
	if err := manager.ReleaseConversationTurnPreparation(ctx, preparation); err != nil {
		t.Fatalf("release reset head preparation: %v", err)
	}
}

func TestRealConversationManagerColdResetHeadRequiresPersistedOwner(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "cold-reset-head-owner.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()

	sessionID := string(modulecore.NewSessionID())
	seed := managerTurnTestRequest("cold-reset-owner-seed", sessionID)
	seedResult, err := l1.CommitConversationTurn(ctx, seed)
	if err != nil {
		t.Fatalf("seed durable head: %v", err)
	}
	if _, err := l1.ResetConversationHead(ctx, domconv.ConversationHeadResetRequest{
		OperationID:       "cold-reset-owner-operation",
		SessionID:         sessionID,
		OwnerID:           seed.OwnerID,
		ExpectedThreadID:  seedResult.ThreadID,
		ExpectedThreadSeq: seedResult.ThreadSeq,
	}); err != nil {
		t.Fatalf("ResetConversationHead: %v", err)
	}

	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: l1}
	preparation, err := manager.PrepareConversationTurn(ctx, modulecore.NewTurnID(), sessionID, seed.OwnerID)
	if preparation != nil || !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("preparation=%+v err=%v, want unavailable without a persisted owner", preparation, err)
	}
	manager.turnOverlayMu.RLock()
	_, reserved := manager.turnOverlays[sessionID]
	manager.turnOverlayMu.RUnlock()
	if reserved {
		t.Fatal("failed owner validation left a cold preparation reservation behind")
	}
}

func TestRealConversationManagerColdOwnerRecallRejectsWrongPersistedSessionOwner(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "cold-owner-mismatch.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()

	sessionID := string(modulecore.NewSessionID())
	seed := managerTurnTestRequest("cold-owner-mismatch-seed", sessionID)
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed canonical L1 head: %v", err)
	}
	redis := newMockRedisStore()
	if err := redis.SaveSession(ctx, domconv.NewSessionConversation(sessionID, "persisted-owner")); err != nil {
		t.Fatalf("save persisted session owner: %v", err)
	}
	manager := &RealConversationManager{redisStore: redis, l1Store: l1}

	if _, err := manager.recallForOwner(ctx, sessionID, "different-owner", "continue", 3); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("wrong persisted session owner error=%v, want owner conflict", err)
	}
}

func TestRealConversationManagerColdOwnerRecallFailsClosedWithoutPersistedSessionOwner(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "cold-owner-missing-owner.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()

	sessionID := string(modulecore.NewSessionID())
	seed := managerTurnTestRequest("cold-owner-missing-owner-seed", sessionID)
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed canonical L1 head: %v", err)
	}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: l1}

	if _, err := manager.recallForOwner(ctx, sessionID, seed.OwnerID, "continue", 3); !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("owner read without persisted session identity error=%v, want unavailable", err)
	}
}

func TestRealConversationManagerCreateThreadInvalidatesOldPreparationAndBlocksStaleColdHead(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "lifecycle-head.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	sessionID := string(modulecore.NewSessionID())
	seed := managerTurnTestRequest("lifecycle-head-seed", sessionID)
	seedResult, err := l1.CommitConversationTurn(ctx, seed)
	if err != nil {
		t.Fatalf("seed durable head: %v", err)
	}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: l1}
	request := managerTurnTestRequest("lifecycle-head-old-lease", sessionID)
	preparation, err := manager.PrepareConversationTurn(ctx, request.TurnID, request.SessionID, request.OwnerID)
	if err != nil || preparation == nil || preparation.ActiveThread == nil || preparation.ActiveThread.ID != seedResult.ThreadID {
		t.Fatalf("preparation=%+v err=%v, want seeded L1 head", preparation, err)
	}
	publication, err := manager.retainConversationTurnPublicationEpoch(preparation)
	if err != nil || publication == nil {
		t.Fatalf("retain pending publication epoch=%v err=%v", publication, err)
	}
	defer publication.Release()

	if _, err := manager.CreateThread(ctx, sessionID, "general"); err != nil {
		t.Fatalf("CreateThread lifecycle: %v", err)
	}
	claimed := false
	if err := publication.Use(func() error { claimed = true; return nil }); !errors.Is(err, domconv.ErrConversationTurnConflict) || claimed {
		t.Fatalf("publication claim after CreateThread reset: claimed=%t err=%v, want stale and unclaimed", claimed, err)
	}
	if err := manager.ValidateConversationTurnPreparation(ctx, preparation); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("old preparation validation error=%v, want lifecycle conflict", err)
	}
	request.Preparation = preparation
	if _, err := manager.CommitConversationTurn(ctx, request); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("old prepared commit error=%v, want lifecycle conflict", err)
	}
	if next, err := manager.PrepareConversationTurn(ctx, modulecore.NewTurnID(), sessionID, request.OwnerID); next != nil || !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("post-lifecycle preparation=%+v err=%v, want fail-closed until L1 lifecycle state is available", next, err)
	}
}

func TestRealConversationManagerCreateThreadAllowsFirstOwnedPreparation(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "new-session-lifecycle.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: l1}
	sessionID := string(modulecore.NewSessionID())
	if _, err := manager.CreateThread(ctx, sessionID, "general"); err != nil {
		t.Fatalf("CreateThread new session: %v", err)
	}
	preparation, err := manager.PrepareConversationTurn(ctx, modulecore.NewTurnID(), sessionID, "new-session-owner")
	if err != nil || preparation == nil || preparation.ActiveThread != nil {
		t.Fatalf("first owned preparation=%+v err=%v, want valid empty head", preparation, err)
	}
	if err := manager.ReleaseConversationTurnPreparation(ctx, preparation); err != nil {
		t.Fatalf("release first preparation: %v", err)
	}
}

func TestRealConversationManagerFlushActiveHeadInvalidatesLeaseAndKeepsL1Context(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "flush-lifecycle-head.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	sessionID := string(modulecore.NewSessionID())
	seed := managerTurnTestRequest("flush-lifecycle-seed", sessionID)
	seedResult, err := l1.CommitConversationTurn(ctx, seed)
	if err != nil {
		t.Fatalf("seed durable head: %v", err)
	}
	redis := newMockRedisStore()
	manager := &RealConversationManager{redisStore: redis, l1Store: l1}
	preparation, err := manager.PrepareConversationTurn(ctx, modulecore.NewTurnID(), sessionID, seed.OwnerID)
	if err != nil || preparation == nil || preparation.ActiveThread == nil || preparation.ActiveThread.ID != seedResult.ThreadID {
		t.Fatalf("preparation=%+v err=%v, want seeded L1 head", preparation, err)
	}
	if err := redis.SaveThread(ctx, preparation.ActiveThread); err != nil {
		t.Fatalf("seed Redis active projection: %v", err)
	}

	if _, err := manager.FlushThread(ctx, preparation.ActiveThread.ID); err != nil {
		t.Fatalf("FlushThread: %v", err)
	}
	if err := manager.ValidateConversationTurnPreparation(ctx, preparation); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("flushed preparation validation=%v, want lifecycle conflict", err)
	}
	next, err := manager.PrepareConversationTurn(ctx, modulecore.NewTurnID(), sessionID, seed.OwnerID)
	if err != nil || next == nil || next.ActiveThread == nil || next.ActiveThread.ID != seedResult.ThreadID || !hasThreadMessage(next.ActiveThread, seed.AgentMessage) {
		t.Fatalf("post-flush preparation=%+v err=%v, want reloaded canonical L1 context", next, err)
	}
	if err := manager.ReleaseConversationTurnPreparation(ctx, next); err != nil {
		t.Fatalf("release post-flush preparation: %v", err)
	}
}

func TestRealConversationManagerBoundsDurableHeadRetentionWithoutEvictingActiveLease(t *testing.T) {
	const retainedHeadLimit = 256
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "bounded-head-retention.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	store := &blockingActiveProjectionStore{L1SQLiteStore: l1}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: store}

	active := managerTurnTestRequest("retention-active-lease", "retention-active-session")
	activeResult, err := l1.CommitConversationTurn(ctx, active)
	if err != nil {
		t.Fatalf("seed active lease head: %v", err)
	}
	activePreparation, err := manager.PrepareConversationTurn(ctx, active.TurnID, active.SessionID, active.OwnerID)
	if err != nil || activePreparation == nil || activePreparation.ActiveThread == nil || activePreparation.ActiveThread.ID != activeResult.ThreadID {
		t.Fatalf("active preparation=%+v err=%v", activePreparation, err)
	}

	requests := make(map[string]domconv.ConversationTurnRequest, retainedHeadLimit+1)
	for index := 0; index <= retainedHeadLimit; index++ {
		request := managerTurnTestRequest(fmt.Sprintf("retention-head-%03d", index), fmt.Sprintf("retention-session-%03d", index))
		result, err := l1.CommitConversationTurn(ctx, request)
		if err != nil {
			t.Fatalf("seed retained head %d: %v", index, err)
		}
		requests[request.SessionID] = request
		preparation, err := manager.PrepareConversationTurn(ctx, request.TurnID, request.SessionID, request.OwnerID)
		if err != nil || preparation == nil || preparation.ActiveThread == nil || preparation.ActiveThread.ID != result.ThreadID {
			t.Fatalf("retained preparation %d=%+v err=%v", index, preparation, err)
		}
		if err := manager.ReleaseConversationTurnPreparation(ctx, preparation); err != nil {
			t.Fatalf("release retained preparation %d: %v", index, err)
		}
	}

	manager.turnOverlayMu.RLock()
	quiescent := 0
	var evictedSession string
	for _, overlay := range manager.turnOverlays {
		if overlay != nil && overlay.preparation == nil && overlay.preparingTurnID == "" && overlay.syncTurnID == "" && len(overlay.pending) == 0 && overlay.headFailure == nil {
			quiescent++
		}
	}
	for sessionID := range requests {
		if _, retained := manager.turnOverlays[sessionID]; !retained {
			evictedSession = sessionID
			break
		}
	}
	activeOverlay := manager.turnOverlays[active.SessionID]
	_, activeStillPresent := manager.turnOverlays[active.SessionID]
	manager.turnOverlayMu.RUnlock()
	if quiescent > retainedHeadLimit {
		t.Fatalf("quiescent durable head count=%d, want <=%d", quiescent, retainedHeadLimit)
	}
	if !activeStillPresent || activeOverlay.preparation == nil || !sameConversationTurnPreparation(activeOverlay.preparation, activePreparation) {
		t.Fatal("retention evicted or changed the active preparation lease")
	}
	if evictedSession == "" {
		t.Fatal("retention did not evict any quiescent durable head after exceeding its bound")
	}

	evictedRequest := requests[evictedSession]
	readsBeforeReload := store.loadCount()
	reloaded, err := manager.PrepareConversationTurn(ctx, evictedRequest.TurnID, evictedSession, evictedRequest.OwnerID)
	if err != nil || reloaded == nil || reloaded.ActiveThread == nil || reloaded.ActiveThread.ID == "" || !hasThreadMessage(reloaded.ActiveThread, evictedRequest.AgentMessage) {
		t.Fatalf("reloaded evicted head=%+v err=%v, want original L1 context", reloaded, err)
	}
	if reads := store.loadCount(); reads != readsBeforeReload+1 {
		t.Fatalf("evicted head reloads=%d after %d, want one cold L1 activation", reads, readsBeforeReload)
	}
	if err := manager.ReleaseConversationTurnPreparation(ctx, reloaded); err != nil {
		t.Fatalf("release reloaded preparation: %v", err)
	}
}

func hasRecallMessage(messages []domconv.Message, text string) bool {
	for _, message := range messages {
		if message.Msg == text {
			return true
		}
	}
	return false
}

func hasThreadMessage(thread *domconv.Thread, text string) bool {
	if thread == nil {
		return false
	}
	return hasRecallMessage(thread.Turns, text)
}

func TestRealConversationManagerAdmittedTurnIsVisibleBeforeBlockedWriter(t *testing.T) {
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "admitted-blocked.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	baselineRequest := managerTurnTestRequest("turn-admitted-baseline", "session-admitted-blocked")
	baseline, err := l1.CommitConversationTurn(context.Background(), baselineRequest)
	if err != nil {
		t.Fatalf("seed stale L1 active thread: %v", err)
	}
	gate := &admittedTurnCommitGateStore{L1SQLiteStore: l1, started: make(chan domconv.ConversationTurnRequest, 2), release: make(chan struct{})}
	redis := newMockRedisStore()
	manager := &RealConversationManager{redisStore: redis, l1Store: gate}
	queue, admission, ctx := newConversationTurnAdmission(t, 2)
	defer gate.unblock()

	firstRequest := managerTurnTestRequest("turn-admitted-first", "session-admitted-blocked")
	firstRequest.Targets = manager.ConversationTurnTargets()
	prepareManagerTurnForTest(t, ctx, manager, &firstRequest)
	staleThread, err := domconv.NewThread(firstRequest.SessionID, "legacy", domconv.ThreadKindUserConversation, 1)
	if err != nil {
		t.Fatalf("NewThread stale Redis fixture: %v", err)
	}
	for _, message := range []domconv.Message{
		domconv.NewMessage(domconv.SpeakerUser, "stale Redis user", nil),
		domconv.NewMessage(domconv.SpeakerMio, "stale Redis agent", nil),
	} {
		if err := staleThread.AddMessage(message); err != nil {
			t.Fatalf("AddMessage stale Redis fixture: %v", err)
		}
	}
	staleSession := domconv.NewSessionConversation(firstRequest.SessionID, "owner-manager")
	staleSession.LastThreadID = staleThread.ID
	staleSession.LastThreadSeq = staleThread.ThreadSeq
	staleSession.LastThreadKind = staleThread.ThreadKind
	if err := redis.SaveThread(context.Background(), staleThread); err != nil {
		t.Fatalf("SaveThread stale Redis fixture: %v", err)
	}
	if err := redis.SaveSession(context.Background(), staleSession); err != nil {
		t.Fatalf("SaveSession stale Redis fixture: %v", err)
	}
	first, err := manager.CommitConversationTurn(ctx, firstRequest)
	if err != nil || string(first.Status) != "accepted" {
		t.Fatalf("first=%+v err=%v, want accepted while writer is blocked", first, err)
	}
	if first.ThreadID != baseline.ThreadID || first.ThreadSeq != baseline.ThreadSeq {
		t.Fatalf("first accepted thread=%s/%d, baseline L1 thread=%s/%d", first.ThreadID, first.ThreadSeq, baseline.ThreadID, baseline.ThreadSeq)
	}
	select {
	case written := <-gate.started:
		if written.ThreadPlan == nil || written.ThreadPlan.Plan.Thread.ID != first.ThreadID {
			t.Fatalf("worker request plan=%+v, want accepted thread %s", written.ThreadPlan, first.ThreadID)
		}
	case <-time.After(time.Second):
		t.Fatal("persistence worker did not reach the blocked writer")
	}
	unadmittedThread, err := manager.GetActiveThread(context.Background(), firstRequest.SessionID)
	if err != nil || unadmittedThread == nil || unadmittedThread.ID != staleThread.ID {
		t.Fatalf("unadmitted Redis thread=%+v err=%v, want scoped stale thread %s", unadmittedThread, err, staleThread.ID)
	}
	unadmittedL1, err := manager.LoadActiveConversationThread(context.Background(), firstRequest.SessionID)
	if err != nil || unadmittedL1 == nil || unadmittedL1.ID != baseline.ThreadID || len(unadmittedL1.Turns) != 2 {
		t.Fatalf("unadmitted L1 thread=%+v err=%v, want only the two durable baseline messages", unadmittedL1, err)
	}

	secondRequest := managerTurnTestRequest("turn-admitted-second", firstRequest.SessionID)
	secondRequest.Targets = manager.ConversationTurnTargets()
	prepareManagerTurnForTest(t, ctx, manager, &secondRequest)
	second, err := manager.CommitConversationTurn(ctx, secondRequest)
	if err != nil || string(second.Status) != "accepted" {
		t.Fatalf("second=%+v err=%v, want accepted", second, err)
	}
	if second.ThreadID != first.ThreadID || second.ThreadSeq != first.ThreadSeq || second.ThreadKind != first.ThreadKind {
		t.Fatalf("second thread=%s/%d/%s, first=%s/%d/%s", second.ThreadID, second.ThreadSeq, second.ThreadKind, first.ThreadID, first.ThreadSeq, first.ThreadKind)
	}

	thread, err := manager.GetActiveThread(ctx, firstRequest.SessionID)
	if err != nil || thread == nil || len(thread.Turns) != 6 || thread.Turns[4].Msg != secondRequest.UserMessage || thread.Turns[5].Msg != secondRequest.AgentMessage {
		t.Fatalf("overlay active thread=%+v err=%v, want stale L1 plus both accepted turns", thread, err)
	}
	active, err := manager.LoadActiveConversationThread(ctx, firstRequest.SessionID)
	if err != nil || active == nil || active.ID != first.ThreadID || len(active.Turns) != 6 {
		t.Fatalf("overlay L1 active thread=%+v err=%v, want six messages", active, err)
	}
	recalled, err := manager.Recall(ctx, firstRequest.SessionID, "", 3)
	if err != nil || len(recalled) != 6 || recalled[4].Msg != secondRequest.UserMessage || recalled[5].Msg != secondRequest.AgentMessage {
		t.Fatalf("overlay recall=%+v err=%v, want stale L1 plus both accepted turns", recalled, err)
	}

	gate.unblock()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("admission barrier: %v", err)
	}
	if status := queue.Status(); status.Failure != nil || status.Pending != 0 {
		t.Fatalf("queue status after successful persistence=%+v", status)
	}
	thread, err = manager.GetActiveThread(context.Background(), firstRequest.SessionID)
	if err != nil || thread == nil || len(thread.Turns) != 6 {
		t.Fatalf("durable active thread=%+v err=%v, want six messages", thread, err)
	}
}

func TestRealConversationManagerAdmittedTurnConflictAndIdempotentReplay(t *testing.T) {
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "admitted-replay.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	gate := &admittedTurnCommitGateStore{L1SQLiteStore: l1, started: make(chan domconv.ConversationTurnRequest, 2), release: make(chan struct{})}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: gate}
	queue, admission, ctx := newConversationTurnAdmission(t, 2)
	defer gate.unblock()

	request := managerTurnTestRequest("turn-admitted-replay", "session-admitted-replay")
	request.Targets = manager.ConversationTurnTargets()
	prepareManagerTurnForTest(t, ctx, manager, &request)
	first, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || string(first.Status) != "accepted" {
		t.Fatalf("first=%+v err=%v, want accepted", first, err)
	}
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("persistence worker did not reach the blocked writer")
	}

	replayRequest := request
	replayRequest.Preparation = nil
	replay, err := manager.CommitConversationTurn(ctx, replayRequest)
	if err != nil || string(replay.Status) != "accepted" || !replay.IdempotentReplay {
		t.Fatalf("replay=%+v err=%v, want accepted idempotent replay", replay, err)
	}
	conflicting := request
	conflicting.Preparation = nil
	conflicting.AgentMessage = "different payload"
	if _, err := manager.CommitConversationTurn(ctx, conflicting); !errors.Is(err, domconv.ErrConversationTurnConflict) {
		t.Fatalf("conflicting accepted replay err=%v, want conflict", err)
	}
	differentOwner := managerTurnTestRequest("turn-admitted-other-owner", request.SessionID)
	differentOwner.OwnerID = "owner-other"
	differentOwner.Targets = manager.ConversationTurnTargets()
	if _, err := manager.CommitConversationTurn(ctx, differentOwner); !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("different owner without a preparation err=%v, want unavailable", err)
	}
	if status := queue.Status(); status.Pending != 1 {
		t.Fatalf("pending commands=%d, want exactly one accepted turn", status.Pending)
	}

	gate.unblock()
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("admission barrier: %v", err)
	}
}

func TestRealConversationManagerAdmittedWriterFailureRetainsReadOverlay(t *testing.T) {
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "admitted-failure.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	writerErr := errors.New("blocked L1 writer failure")
	gate := &admittedTurnCommitGateStore{L1SQLiteStore: l1, started: make(chan domconv.ConversationTurnRequest, 1), release: make(chan struct{}), fail: writerErr}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: gate}
	queue, admission, ctx := newConversationTurnAdmission(t, 1)
	defer gate.unblock()

	request := managerTurnTestRequest("turn-admitted-failure", "session-admitted-failure")
	request.Targets = manager.ConversationTurnTargets()
	prepareManagerTurnForTest(t, ctx, manager, &request)
	accepted, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || string(accepted.Status) != "accepted" {
		t.Fatalf("accepted=%+v err=%v, want accepted", accepted, err)
	}
	select {
	case <-gate.started:
	case <-time.After(time.Second):
		t.Fatal("persistence worker did not reach the blocked writer")
	}
	gate.unblock()
	if err := admission.Barrier(context.Background()); !errors.Is(err, persistencequeue.ErrFailed) || !errors.Is(err, writerErr) {
		t.Fatalf("admission barrier err=%v, want queue failure wrapping writer error", err)
	}
	if status := queue.Status(); status.Failure == nil {
		t.Fatal("queue failure did not preserve its failed head")
	}
	thread, err := manager.GetActiveThread(ctx, request.SessionID)
	if err != nil || thread == nil || len(thread.Turns) != 2 || thread.Turns[0].Msg != request.UserMessage {
		t.Fatalf("failed head overlay=%+v err=%v, want accepted messages retained", thread, err)
	}
}

type countingTurnSummarizer struct {
	calls int
}

type deadlineTurnSummarizer struct{ deadlines chan time.Time }

type duplicateRecallProjectionStore struct {
	*l1sqlite.L1SQLiteStore
	duplicateThreadID modulecore.ThreadID
}

func (s *duplicateRecallProjectionStore) LoadConversationThreadProjection(ctx context.Context, sessionID string, threadID modulecore.ThreadID) ([]l1sqlite.L1MemoryEvent, error) {
	events, err := s.L1SQLiteStore.LoadConversationThreadProjection(ctx, sessionID, threadID)
	if err == nil && threadID == s.duplicateThreadID && len(events) > 0 {
		for index := range events {
			events[index].CreatedAt = time.Unix(1, 0).UTC()
		}
		events = append(events, events[0])
	}
	return events, err
}

func (s *duplicateRecallProjectionStore) LoadActiveConversationThreadProjection(ctx context.Context, sessionID string) ([]l1sqlite.L1MemoryEvent, error) {
	events, err := s.L1SQLiteStore.LoadActiveConversationThreadProjection(ctx, sessionID)
	if err == nil {
		for index := range events {
			events[index].CreatedAt = time.Unix(1, 0).UTC().Add(time.Hour)
		}
	}
	return events, err
}

type failingRecallProjectionStore struct {
	*l1sqlite.L1SQLiteStore
	activeErr error
}

func (s *failingRecallProjectionStore) LoadActiveConversationThreadProjection(context.Context, string) ([]l1sqlite.L1MemoryEvent, error) {
	return nil, s.activeErr
}

type missingActiveRecallProjectionStore struct{ *l1sqlite.L1SQLiteStore }

func (s *missingActiveRecallProjectionStore) LoadActiveConversationThreadProjection(context.Context, string) ([]l1sqlite.L1MemoryEvent, error) {
	return nil, domconv.ErrThreadNotFound
}

type trackingLegacyRecallL1Store struct {
	*l1sqlite.L1SQLiteStore
	typedReadCalls int
}

func (s *trackingLegacyRecallL1Store) LoadActiveConversationThreadProjection(ctx context.Context, sessionID string) ([]l1sqlite.L1MemoryEvent, error) {
	s.typedReadCalls++
	return s.L1SQLiteStore.LoadActiveConversationThreadProjection(ctx, sessionID)
}

func (s *trackingLegacyRecallL1Store) ListPendingConversationTurnThreadFollowers(ctx context.Context, sessionID string, limit int) ([]l1sqlite.PendingConversationTurnThreadFollower, error) {
	s.typedReadCalls++
	return s.L1SQLiteStore.ListPendingConversationTurnThreadFollowers(ctx, sessionID, limit)
}

func (s *trackingLegacyRecallL1Store) HasConversationTurnReceiptsForSession(ctx context.Context, sessionID string) (bool, error) {
	s.typedReadCalls++
	return s.L1SQLiteStore.HasConversationTurnReceiptsForSession(ctx, sessionID)
}

func (s *deadlineTurnSummarizer) Summarize(ctx context.Context, _ *domconv.Thread) (domconv.SummaryResidual, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return domconv.SummaryResidual{}, errors.New("follower work has no deadline")
	}
	s.deadlines <- deadline
	return domconv.SummaryResidual{Summary: "bounded summary", Keywords: []string{"bounded", "summary"}, Provider: "test"}, nil
}

type failingTurnEmbedder struct{}

type failingTurnSummarizer struct{ calls int }

func (s *failingTurnSummarizer) Summarize(context.Context, *domconv.Thread) (domconv.SummaryResidual, error) {
	s.calls++
	return domconv.SummaryResidual{}, errors.New("summary unavailable")
}

type conversationTurnTraceEvents map[modulecore.TraceID][]modulecore.EventEnvelope

func (events conversationTurnTraceEvents) HasPublicConversationFinalResponse(_ context.Context, traceID modulecore.TraceID, turnID modulecore.TurnID, sessionID modulecore.SessionID) (bool, error) {
	return hasPublicConversationFinalResponse(events[traceID], traceID, turnID, sessionID), nil
}

type failingConversationFinalReader struct {
	failTrace modulecore.TraceID
	finals    map[modulecore.TraceID]modulecore.EventEnvelope
	err       error
}

func (r failingConversationFinalReader) HasPublicConversationFinalResponse(_ context.Context, traceID modulecore.TraceID, turnID modulecore.TurnID, sessionID modulecore.SessionID) (bool, error) {
	if traceID == r.failTrace {
		return false, r.err
	}
	event, ok := r.finals[traceID]
	return ok && hasPublicConversationFinalResponse([]modulecore.EventEnvelope{event}, traceID, turnID, sessionID), nil
}

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

func TestL1ProjectionRecallPreservesUserAgentOrderAtEqualTimestamp(t *testing.T) {
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	events := []l1sqlite.L1MemoryEvent{
		{Speaker: domconv.SpeakerUser, Message: "raw user", CreatedAt: at},
		{Speaker: domconv.SpeakerMio, Message: "full agent reply", CreatedAt: at},
	}
	got := l1ProjectionEventsToMessages(events)
	if len(got) != 2 || got[0].Speaker != domconv.SpeakerUser || got[0].Msg != "raw user" || got[1].Speaker != domconv.SpeakerMio || got[1].Msg != "full agent reply" {
		t.Fatalf("ASC L1 projection recall = %+v, want User then Agent at equal timestamps", got)
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

type blockingTurnCommitStore struct {
	*l1sqlite.L1SQLiteStore
	committed chan struct{}
	release   chan struct{}
	once      sync.Once
}

type shortLeaseTurnClaimStore struct {
	*l1sqlite.L1SQLiteStore
	leaseExpiresAt time.Time
}

type deadlineRecordingTurnRedisStore struct {
	*mockRedisStore
	deadlines chan time.Time
}

func (s *deadlineRecordingTurnRedisStore) SaveThread(ctx context.Context, _ *domconv.Thread) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("Redis projection context has no deadline")
	}
	s.deadlines <- deadline
	return errors.New("stop after observing bounded Redis context")
}

func (s *shortLeaseTurnClaimStore) ClaimNextConversationTurnOutboxTargetExcluding(ctx context.Context, target string, now time.Time, leaseDuration time.Duration, excluded map[string]struct{}) (*domconv.ConversationTurnOutbox, error) {
	claim, err := s.L1SQLiteStore.ClaimNextConversationTurnOutboxTargetExcluding(ctx, target, now, leaseDuration, excluded)
	if claim != nil {
		claim.LeaseExpiresAt = s.leaseExpiresAt
	}
	return claim, err
}

func (s *blockingTurnCommitStore) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	result, err := s.L1SQLiteStore.CommitConversationTurn(ctx, request)
	if err != nil {
		return result, err
	}
	s.once.Do(func() { close(s.committed) })
	select {
	case <-s.release:
		return result, nil
	case <-ctx.Done():
		return result, ctx.Err()
	}
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
	if s.saveErr != nil {
		return s.saveErr
	}
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

func TestRealConversationManagerWorkerCannotClaimRedisBetweenL1AndForegroundProjection(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection-claim-race.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	blocking := &blockingTurnCommitStore{L1SQLiteStore: l1, committed: make(chan struct{}), release: make(chan struct{})}
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: blocking}
	request := managerTurnTestRequest("turn-projection-claim-race", "session-projection-claim-race")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	commitResult := make(chan error, 1)
	go func() {
		_, err := manager.CommitConversationTurn(ctx, request)
		commitResult <- err
	}()
	select {
	case <-blocking.committed:
	case <-time.After(time.Second):
		t.Fatal("L1 turn did not commit to the controlled gap")
	}
	drainResult := make(chan error, 1)
	go func() { drainResult <- manager.DrainConversationTurnOutbox(ctx, 1) }()
	time.Sleep(25 * time.Millisecond)
	close(blocking.release)
	if err := <-commitResult; err != nil {
		t.Fatalf("foreground commit lost its Redis claim to the worker: %v", err)
	}
	if err := <-drainResult; err != nil {
		t.Fatalf("worker after foreground Redis projection: %v", err)
	}
	thread, err := manager.GetActiveThread(ctx, request.SessionID)
	if err != nil || thread == nil || len(thread.Turns) != 2 || thread.Turns[1].Msg != request.AgentMessage {
		t.Fatalf("Redis projection after claim race thread=%+v err=%v", thread, err)
	}
}

func TestRealConversationManagerForegroundProjectionLockWaitHonorsContext(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection-lock-cancel.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: l1}
	if err := manager.turnProjectionMu.Lock(ctx); err != nil {
		t.Fatalf("hold projection lock: %v", err)
	}
	defer manager.turnProjectionMu.Unlock()

	request := managerTurnTestRequest("turn-projection-lock-cancel", "session-projection-lock-cancel")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	commitCtx, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = manager.CommitConversationTurn(commitCtx, request)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("commit while projection lock busy err=%v elapsed=%s, want prompt context cancellation", err, time.Since(started))
	}
	if _, err := l1.GetConversationTurnReceipt(ctx, string(request.TurnID)); err == nil {
		t.Fatal("turn committed while foreground was still waiting for projection lock")
	}
}

func TestRealConversationManagerWorkerSkipsBusyRedisLockAndRunsThreadFollower(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection-lock-skip.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	seed := managerTurnTestRequest("turn-projection-lock-skip-seed", "session-projection-lock-skip")
	seed.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	boundary := managerTurnTestRequest("turn-projection-lock-skip-boundary", seed.SessionID)
	boundary.Boundary = true
	boundary.BoundaryReason = "projection lock skip test"
	boundary.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
	if _, err := l1.CommitConversationTurn(ctx, boundary); err != nil {
		t.Fatalf("boundary commit: %v", err)
	}
	if err := l1.MarkConversationTurnResponsePublished(ctx, boundary.TurnID, boundary.TraceID, modulecore.SessionID(boundary.SessionID), time.Now().UTC()); err != nil {
		t.Fatalf("mark final published: %v", err)
	}
	archive := newTurnTestArchiveStore()
	summarizer := &countingTurnSummarizer{}
	redis := newMockRedisStore()
	manager := &RealConversationManager{redisStore: redis, l1Store: l1, archiveStore: archive, vectordbStore: &mockVectorDBStore{}, summarizer: summarizer}
	if err := manager.turnProjectionMu.Lock(ctx); err != nil {
		t.Fatalf("hold projection lock: %v", err)
	}
	defer manager.turnProjectionMu.Unlock()
	if err := manager.DrainConversationTurnOutbox(ctx, 1); err != nil {
		t.Fatalf("drain while foreground owns projection lock: %v", err)
	}
	if summarizer.calls != 1 {
		t.Fatalf("thread follower summary calls=%d, want worker to skip busy Redis and continue", summarizer.calls)
	}
	if len(redis.threads) != 0 || len(redis.sessions) != 0 {
		t.Fatalf("worker touched Redis while foreground owned projection lock: threads=%d sessions=%d", len(redis.threads), len(redis.sessions))
	}
	receipt, err := l1.GetConversationTurnReceipt(ctx, string(boundary.TurnID))
	if err != nil {
		t.Fatalf("boundary receipt: %v", err)
	}
	if len(receipt.PendingTargets) != 1 || receipt.PendingTargets[0] != string(domconv.ConversationTurnTargetRedisProjection) || !containsConversationTurnTarget(receipt.CompletedTargets, string(domconv.ConversationTurnTargetThreadFollowers)) {
		t.Fatalf("boundary receipt after worker pass=%+v, want Redis pending and thread follower complete", receipt)
	}
}

func TestConversationTurnWorkDeadlineRespectsTargetBudgetAndLeaseReserve(t *testing.T) {
	started := time.Date(2026, time.October, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		timeout    time.Duration
		leaseUntil time.Time
		now        time.Time
		want       time.Time
		ok         bool
	}{
		{name: "target timeout", timeout: 5 * time.Second, leaseUntil: started.Add(time.Minute), now: started.Add(time.Second), want: started.Add(5 * time.Second), ok: true},
		{name: "lease reserve", timeout: 45 * time.Second, leaseUntil: started.Add(30 * time.Second), now: started.Add(5 * time.Second), want: started.Add(20 * time.Second), ok: true},
		{name: "lease already consumed", timeout: 45 * time.Second, leaseUntil: started.Add(30 * time.Second), now: started.Add(20 * time.Second), ok: false},
		{name: "insufficient target budget", timeout: 5 * time.Second, leaseUntil: started.Add(time.Minute), now: started.Add(5*time.Second - 50*time.Millisecond), ok: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := conversationTurnWorkDeadline(started, testCase.timeout, testCase.leaseUntil, testCase.now)
			if got != testCase.want || ok != testCase.ok {
				t.Fatalf("deadline=%s ok=%v, want %s ok=%v", got, ok, testCase.want, testCase.ok)
			}
		})
	}
}

func TestRealConversationManagerDoesNotStartRedisWorkWithoutLeaseBudget(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection-insufficient-lease.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	request := managerTurnTestRequest("turn-projection-insufficient-lease", "session-projection-insufficient-lease")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := l1.CommitConversationTurn(ctx, request); err != nil {
		t.Fatalf("commit: %v", err)
	}
	redis := newMockRedisStore()
	manager := &RealConversationManager{
		redisStore: redis,
		l1Store: &shortLeaseTurnClaimStore{
			L1SQLiteStore:  l1,
			leaseExpiresAt: time.Now().UTC().Add(50 * time.Millisecond),
		},
	}
	err = manager.DrainConversationTurnOutbox(ctx, 1)
	if !errors.Is(err, domconv.ErrConversationTurnUnavailable) {
		t.Fatalf("drain err=%v, want unavailable because lease budget cannot cover work and bookkeeping", err)
	}
	if len(redis.threads) != 0 || len(redis.sessions) != 0 {
		t.Fatalf("Redis external I/O started without remaining lease budget: threads=%d sessions=%d", len(redis.threads), len(redis.sessions))
	}
	receipt, err := l1.GetConversationTurnReceipt(ctx, string(request.TurnID))
	if err != nil {
		t.Fatalf("receipt: %v", err)
	}
	if len(receipt.PendingTargets) != 1 || receipt.PendingTargets[0] != string(domconv.ConversationTurnTargetRedisProjection) {
		t.Fatalf("receipt after insufficient-lease failure=%+v, want durable Redis target pending", receipt)
	}
}

func TestRealConversationManagerUsesShortRedisProjectionDeadline(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "projection-short-deadline.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	redis := &deadlineRecordingTurnRedisStore{mockRedisStore: newMockRedisStore(), deadlines: make(chan time.Time, 1)}
	manager := &RealConversationManager{redisStore: redis, l1Store: l1}
	request := managerTurnTestRequest("turn-projection-short-deadline", "session-projection-short-deadline")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	started := time.Now()
	if _, err := manager.CommitConversationTurn(ctx, request); err == nil {
		t.Fatal("commit succeeded after Redis projection failed")
	}
	select {
	case deadline := <-redis.deadlines:
		remaining := time.Until(deadline)
		if remaining <= 4*time.Second || remaining > conversationTurnRedisProjectionTimeout {
			t.Fatalf("Redis projection deadline remaining=%s, want near the %s target budget (elapsed=%s)", remaining, conversationTurnRedisProjectionTimeout, time.Since(started))
		}
	case <-time.After(time.Second):
		t.Fatal("Redis projection was not attempted promptly")
	}
	if time.Since(started) > time.Second {
		t.Fatalf("Redis failure held foreground projection for %s, want bounded completion", time.Since(started))
	}
}

func containsConversationTurnTarget(targets []string, wanted string) bool {
	for _, target := range targets {
		if target == wanted {
			return true
		}
	}
	return false
}

func TestRealConversationManagerBoundsFollowerWorkInsideLease(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "follower-deadline.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	summarizer := &deadlineTurnSummarizer{deadlines: make(chan time.Time, 1)}
	manager := &RealConversationManager{
		redisStore:    newMockRedisStore(),
		l1Store:       l1,
		archiveStore:  newTurnTestArchiveStore(),
		vectordbStore: &mockVectorDBStore{},
		summarizer:    summarizer,
	}
	seed := managerTurnTestRequest("turn-deadline-seed", "session-deadline")
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	request := managerTurnTestRequest("turn-deadline-boundary", seed.SessionID)
	request.Boundary = true
	request.BoundaryReason = "deadline test"
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetThreadFollowers}
	result, err := l1.CommitConversationTurn(ctx, request)
	if err != nil {
		t.Fatalf("boundary commit: %v", err)
	}
	if err := l1.MarkConversationTurnResponsePublished(ctx, request.TurnID, request.TraceID, modulecore.SessionID(request.SessionID), time.Now().UTC()); err != nil {
		t.Fatalf("mark final published: %v", err)
	}
	started := time.Now()
	if err := manager.DrainConversationTurnOutbox(ctx, 1); err != nil {
		t.Fatalf("drain thread follower: %v", err)
	}
	select {
	case deadline := <-summarizer.deadlines:
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > conversationTurnFollowerLease || time.Since(started) > time.Second {
			t.Fatalf("summarizer deadline remaining=%s lease=%s elapsed=%s closed_thread=%s", remaining, conversationTurnFollowerLease, time.Since(started), result.ClosedThreadID)
		}
	default:
		t.Fatal("thread follower summarizer did not run")
	}
}

func TestRealConversationManagerRequiredRedisFailureBlocksTurnSuccess(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "redis-required.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	redis := newMockRedisStore()
	redis.saveThreadErr = errors.New("redis unavailable")
	manager := &RealConversationManager{redisStore: redis, l1Store: l1}
	request := managerTurnTestRequest("turn-required-redis-failure", "session-required-redis-failure")
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	result, err := manager.CommitConversationTurn(ctx, request)
	if err == nil || result.Status != domconv.ConversationTurnPartial {
		t.Fatalf("commit result=%+v err=%v, want durable partial and required Redis error", result, err)
	}
	receipt, receiptErr := l1.GetConversationTurnReceipt(ctx, string(request.TurnID))
	if receiptErr != nil || receipt.Status != domconv.ConversationTurnPartial || len(receipt.PendingTargets) != 1 || receipt.PendingTargets[0] != string(domconv.ConversationTurnTargetRedisProjection) {
		t.Fatalf("receipt=%+v err=%v, want Redis pending after L1 commit", receipt, receiptErr)
	}
}

func TestRealConversationManagerPublicationReconcileContinuesAfterCandidateError(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "reconcile-continue.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	requests := []domconv.ConversationTurnRequest{
		managerTurnTestRequest("turn-reconcile-error-first", "session-reconcile-error-first"),
		managerTurnTestRequest("turn-reconcile-error-second", "session-reconcile-error-second"),
	}
	if requests[0].TurnID > requests[1].TurnID {
		requests[0], requests[1] = requests[1], requests[0]
	}
	for index := range requests {
		seed := requests[index]
		seed.Boundary = false
		seed.Targets = nil
		if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
			t.Fatalf("seed %s: %v", seed.TurnID, err)
		}
		boundary := requests[index]
		boundary.TurnID = modulecore.TurnID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalTurnID, "reconcile-boundary:"+string(requests[index].TurnID)))
		boundary.TraceID = modulecore.TraceID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalTraceID, "reconcile-boundary:"+string(requests[index].TurnID)))
		boundary.RootTaskID = modulecore.TaskID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalTaskID, "reconcile-boundary:"+string(requests[index].TurnID)))
		boundary.UserMessageID = modulecore.MessageID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalMessageID, "reconcile-user:"+string(requests[index].TurnID)))
		boundary.AgentMessageID = modulecore.MessageID(canonicalManagerTurnIdentityForTest(modulecore.CanonicalMessageID, "reconcile-agent:"+string(requests[index].TurnID)))
		boundary.Boundary = true
		boundary.BoundaryReason = "reconcile cursor test"
		boundary.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetThreadFollowers}
		if _, err := l1.CommitConversationTurn(ctx, boundary); err != nil {
			t.Fatalf("boundary %s: %v", boundary.TurnID, err)
		}
		requests[index] = boundary
	}
	readerErr := errors.New("canonical event lookup unavailable")
	final := modulecore.NewEventEnvelope(requests[1].TraceID, "", nil, "orchestrator", "agent.response", time.Now().UTC(), map[string]any{"to": "user"})
	final.TurnID = requests[1].TurnID
	final.SessionID = modulecore.SessionID(requests[1].SessionID)
	manager := &RealConversationManager{l1Store: l1}
	reader := failingConversationFinalReader{failTrace: requests[0].TraceID, finals: map[modulecore.TraceID]modulecore.EventEnvelope{requests[1].TraceID: final}, err: readerErr}
	if err := manager.ReconcileConversationTurnResponsePublications(ctx, reader, 100); !errors.Is(err, readerErr) {
		t.Fatalf("reconcile error=%v, want first lookup error", err)
	}
	firstPublished, err := l1.IsConversationTurnResponsePublished(ctx, requests[0].TurnID, requests[0].TraceID, modulecore.SessionID(requests[0].SessionID))
	if err != nil || firstPublished {
		t.Fatalf("failed candidate marker=%v err=%v, want pending", firstPublished, err)
	}
	secondPublished, err := l1.IsConversationTurnResponsePublished(ctx, requests[1].TurnID, requests[1].TraceID, modulecore.SessionID(requests[1].SessionID))
	if err != nil || !secondPublished {
		t.Fatalf("later candidate marker=%v err=%v, want continued reconciliation", secondPublished, err)
	}
}

func TestRealConversationManagerReplayDoesNotClaimPublishedThreadFollowerForeground(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "foreground-replay.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: l1}
	seed := managerTurnTestRequest("turn-foreground-replay-seed", "session-foreground-replay")
	seed.Targets = manager.ConversationTurnTargets()
	if _, err := manager.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	request := managerTurnTestRequest("turn-foreground-replay-boundary", seed.SessionID)
	request.Boundary = true
	request.BoundaryReason = "new topic"
	request.Targets = manager.ConversationTurnTargets()
	result, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || result.Status != domconv.ConversationTurnPartial {
		t.Fatalf("boundary=%+v err=%v", result, err)
	}
	if err := manager.MarkConversationTurnResponsePublished(ctx, request.TurnID, request.TraceID, modulecore.SessionID(request.SessionID), time.Now().UTC()); err != nil {
		t.Fatalf("mark response: %v", err)
	}
	replay, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || !replay.IdempotentReplay || replay.Status != domconv.ConversationTurnPartial {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	claim, err := l1.ClaimNextConversationTurnOutbox(ctx, time.Now().UTC(), time.Minute)
	if err != nil || claim == nil || claim.Target != string(domconv.ConversationTurnTargetThreadFollowers) || claim.Attempts != 1 {
		t.Fatalf("background claim=%+v err=%v, want first thread-follower attempt after replay", claim, err)
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
	seed.Targets = manager.ConversationTurnTargets()
	if _, err := manager.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	request := managerTurnTestRequest("turn-manager-boundary", seed.SessionID)
	request.Boundary = true
	request.BoundaryReason = "new topic"
	request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
	result, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || result.Status != domconv.ConversationTurnPartial || result.ClosedThreadID == "" {
		t.Fatalf("boundary result=%+v err=%v, want durable partial while closed-thread followers wait for public response", result, err)
	}
	if result.ThreadID == "" || result.ThreadSeq != 2 || result.ThreadKind != domconv.ThreadKindUserConversation || result.ClosedThreadSeq != 1 || result.ClosedThreadKind != domconv.ThreadKindUserConversation {
		t.Fatalf("boundary tuple=%q/%d/%q closed=%q/%d/%q", result.ThreadID, result.ThreadSeq, result.ThreadKind, result.ClosedThreadID, result.ClosedThreadSeq, result.ClosedThreadKind)
	}
	if summarizer.calls != 0 {
		t.Fatalf("summarizer calls=%d, want no summary before final response publication", summarizer.calls)
	}
	if archived := archive.byID[result.ClosedThreadID]; archived != nil {
		t.Fatalf("closed thread was archived before final response publication: %+v", archived)
	}
	if closedRedis := redis.threads[result.ClosedThreadID]; closedRedis == nil {
		t.Fatalf("closed Redis thread was cleaned up before its summary follower became eligible")
	}
	recalled, err := manager.Recall(ctx, seed.SessionID, "remember the complete reply", 12)
	if err != nil {
		t.Fatalf("Recall while summary is pending: %v", err)
	}
	var foundPriorUser, foundPriorReply bool
	for _, message := range recalled {
		foundPriorUser = foundPriorUser || message.Msg == seed.UserMessage
		foundPriorReply = foundPriorReply || message.Msg == seed.AgentMessage
	}
	if !foundPriorUser || !foundPriorReply {
		t.Fatalf("pending-summary Recall lost prior raw/full reply: found_user=%v found_reply=%v recalled=%+v", foundPriorUser, foundPriorReply, recalled)
	}
	replay, err := manager.CommitConversationTurn(ctx, request)
	if err != nil || !replay.IdempotentReplay || replay.Status != domconv.ConversationTurnPartial {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	if summarizer.calls != 0 {
		t.Fatalf("replay summarizer calls=%d, want unchanged", summarizer.calls)
	}
}

func TestRealConversationManagerRecallIncludesNewestClosedTurnAfterBacklog(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "recall-backlog.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: l1}
	sessionID := canonicalManagerTurnSessionIDForTest("session-recall-backlog")
	var latestClosedTurn domconv.ConversationTurnRequest
	for index := 0; index < 102; index++ {
		request := managerTurnTestRequest(fmt.Sprintf("turn-recall-backlog-%03d", index), sessionID)
		request.Boundary = index > 0
		if request.Boundary {
			request.BoundaryReason = "topic changed"
		}
		request.Targets = manager.ConversationTurnTargets()
		if _, err := manager.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("commit backlog turn %d: %v", index, err)
		}
		latestClosedTurn = request
	}
	current := managerTurnTestRequest("turn-recall-backlog-current", sessionID)
	current.Boundary = true
	current.BoundaryReason = "topic changed"
	current.Targets = manager.ConversationTurnTargets()
	if _, err := manager.CommitConversationTurn(ctx, current); err != nil {
		t.Fatalf("commit current turn: %v", err)
	}
	recalled, err := manager.Recall(ctx, sessionID, "latest closed response", 12)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	foundUser, foundReply := false, false
	for _, message := range recalled {
		foundUser = foundUser || message.Msg == latestClosedTurn.UserMessage
		foundReply = foundReply || message.Msg == latestClosedTurn.AgentMessage
	}
	if !foundUser || !foundReply {
		t.Fatalf("bounded Recall omitted newest closed raw turn: user=%v reply=%v messages=%+v", foundUser, foundReply, recalled)
	}
}

func TestRealConversationManagerRecallOnRedisMissUsesActiveAndLatestPendingThread(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		expiredKey bool
	}{
		{name: "missing Redis session"},
		{name: "expired active thread key", expiredKey: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "recall-redis-miss.db"))
			if err != nil {
				t.Fatalf("NewL1SQLiteStore: %v", err)
			}
			defer l1.Close()

			sessionID := canonicalManagerTurnSessionIDForTest("session-recall-redis-miss-" + test.name)
			turns := seedPendingRecallThreads(t, ctx, l1, sessionID)
			latestClosed := turns[1]
			active := turns[2]
			redis := newMockRedisStore()
			if test.expiredKey {
				activeEvents, err := l1.LoadActiveConversationThreadProjection(ctx, sessionID)
				if err != nil || len(activeEvents) == 0 {
					t.Fatalf("load active L1 projection: events=%d err=%v", len(activeEvents), err)
				}
				session := domconv.NewSessionConversation(sessionID, "owner-recall")
				session.LastThreadID = activeEvents[0].ThreadID
				session.LastThreadSeq = activeEvents[0].ThreadSeq
				session.LastThreadKind = activeEvents[0].ThreadKind
				if err := redis.SaveSession(ctx, session); err != nil {
					t.Fatalf("save Redis session with expired thread key: %v", err)
				}
			}

			pending, err := l1.ListPendingConversationTurnThreadFollowers(ctx, sessionID, 1)
			if err != nil || len(pending) != 1 {
				t.Fatalf("load newest pending follower: count=%d err=%v", len(pending), err)
			}
			// The latest closed projection is duplicated by the test store to verify
			// that Recall still deduplicates by canonical message ID.
			store := &duplicateRecallProjectionStore{L1SQLiteStore: l1, duplicateThreadID: pending[0].ClosedThreadID}
			manager := &RealConversationManager{redisStore: redis, l1Store: store}

			got, err := manager.Recall(ctx, sessionID, "continue", 12)
			if err != nil {
				t.Fatalf("Recall after Redis miss: %v", err)
			}
			want := []string{latestClosed.UserMessage, latestClosed.AgentMessage, active.UserMessage, active.AgentMessage}
			if len(got) != len(want) {
				t.Fatalf("Recall returned %d messages, want active plus latest pending closed thread (%d): %+v", len(got), len(want), got)
			}
			seenMessageIDs := make(map[string]struct{}, len(got))
			for index, message := range got {
				if message.Msg != want[index] {
					t.Fatalf("Recall[%d]=%q, want %q; all=%+v", index, message.Msg, want[index], got)
				}
				messageID, ok := message.Meta["message_id"].(string)
				if !ok || messageID == "" {
					t.Fatalf("Recall[%d] missing canonical message ID: %+v", index, message)
				}
				if _, exists := seenMessageIDs[messageID]; exists {
					t.Fatalf("Recall duplicated message ID %q: %+v", messageID, got)
				}
				seenMessageIDs[messageID] = struct{}{}
			}
			for index := 0; index < len(got); index += 2 {
				if !got[index].Timestamp.Equal(got[index+1].Timestamp) || got[index].Speaker != domconv.SpeakerUser || (got[index+1].Speaker != domconv.SpeakerMio && got[index+1].Speaker != domconv.SpeakerShiro) {
					t.Fatalf("same-time User/Agent ordering was not preserved for pair %d: %+v", index/2, got[index:index+2])
				}
			}
		})
	}
}

func TestRealConversationManagerRecallReturnsL1ProjectionReadFailure(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "recall-projection-error.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	sessionID := canonicalManagerTurnSessionIDForTest("session-recall-projection-error")
	seedPendingRecallThreads(t, ctx, l1, sessionID)
	wantErr := errors.New("active conversation projection read failed")
	manager := &RealConversationManager{
		redisStore: newMockRedisStore(),
		l1Store:    &failingRecallProjectionStore{L1SQLiteStore: l1, activeErr: wantErr},
	}
	if messages, err := manager.Recall(ctx, sessionID, "continue", 12); !errors.Is(err, wantErr) {
		t.Fatalf("Recall=(%+v, %v), want projection error instead of empty/fallback success", messages, err)
	}
}

func TestRealConversationManagerRecallDoesNotReplayCompletedTypedThreadsWhenActiveProjectionMissing(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "recall-completed-typed.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	sessionID := canonicalManagerTurnSessionIDForTest("session-recall-completed-typed")
	seed := managerTurnTestRequest("turn-recall-completed-typed-seed", sessionID)
	seed.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection}
	if _, err := l1.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("commit seed: %v", err)
	}
	boundary := managerTurnTestRequest("turn-recall-completed-typed-boundary", sessionID)
	boundary.Boundary = true
	boundary.BoundaryReason = "topic changed"
	boundary.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
	if _, err := l1.CommitConversationTurn(ctx, boundary); err != nil {
		t.Fatalf("commit boundary: %v", err)
	}
	if err := l1.MarkConversationTurnResponsePublished(ctx, boundary.TurnID, boundary.TraceID, modulecore.SessionID(sessionID), time.Now().UTC()); err != nil {
		t.Fatalf("mark final response published: %v", err)
	}
	claim, err := l1.ClaimConversationTurnOutboxTarget(ctx, string(boundary.TurnID), string(domconv.ConversationTurnTargetThreadFollowers), time.Now().UTC(), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("claim thread follower: claim=%+v err=%v", claim, err)
	}
	if _, err := l1.CompleteConversationTurnOutbox(ctx, string(claim.TurnID), claim.Target, claim.LeaseToken, time.Now().UTC()); err != nil {
		t.Fatalf("complete thread follower: %v", err)
	}
	pending, err := l1.ListPendingConversationTurnThreadFollowers(ctx, sessionID, 1)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending followers=%+v err=%v, want all completed", pending, err)
	}

	archive := &mockArchiveSQLiteStore{saved: []*domconv.ThreadSummary{{
		ThreadID: modulecore.NewThreadID(), Domain: "general", Summary: "completed turn summary",
	}}}
	manager := &RealConversationManager{
		redisStore:   newMockRedisStore(),
		l1Store:      &missingActiveRecallProjectionStore{L1SQLiteStore: l1},
		archiveStore: archive,
	}
	got, err := manager.Recall(ctx, sessionID, "continue", 12)
	if err != nil {
		t.Fatalf("Recall with completed typed threads and missing active projection: %v", err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Msg, "[Summary] completed turn summary") {
		t.Fatalf("Recall=(%+v), want archive summary after suppressing completed typed raw L1 fallback", got)
	}
}

func TestRealConversationManagerRecallPreservesLegacyL1SessionReadContract(t *testing.T) {
	tests := []struct {
		name      string
		sessionID string
		bound     bool
		wantError bool
	}{
		{name: "empty unbound legacy row remains readable", sessionID: ""},
		{name: "noncanonical bound row remains rejected", sessionID: "legacy-session-id", bound: true, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			dbPath := filepath.Join(t.TempDir(), "legacy-session-recall.db")
			store, err := l1sqlite.NewL1SQLiteStore(dbPath)
			if err != nil {
				t.Fatalf("NewL1SQLiteStore: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatalf("close initial L1 store: %v", err)
			}

			threadID := ""
			threadSeq := 0
			threadKind := ""
			namespace := "conv:legacy-unbound"
			if test.bound {
				id := modulecore.NewThreadID()
				threadID = string(id)
				threadSeq = 1
				threadKind = string(modulecore.ThreadKindUserConversation)
				namespace = "conv:" + threadID
			}
			db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
			if err != nil {
				t.Fatalf("sql.Open legacy L1 database: %v", err)
			}
			createdAt := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
			_, err = db.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '{}', ?, ?, ?, ?, ?)`,
				"legacy-session-row", namespace, test.sessionID, threadID, threadSeq, threadKind,
				domconv.SpeakerUser, "legacy conversation survived restart", l1sqlite.MemoryStateObserved, l1sqlite.MemoryLayerL1,
				"conversation", createdAt, createdAt)
			closeErr := db.Close()
			if err != nil {
				t.Fatalf("insert legacy L1 row: %v", err)
			}
			if closeErr != nil {
				t.Fatalf("close raw SQLite writer: %v", closeErr)
			}
			store, err = l1sqlite.NewL1SQLiteStore(dbPath)
			if err != nil {
				t.Fatalf("reopen legacy L1 store: %v", err)
			}
			defer store.Close()

			tracked := &trackingLegacyRecallL1Store{L1SQLiteStore: store}
			manager := &RealConversationManager{redisStore: newMockRedisStore(), l1Store: tracked}
			got, err := manager.Recall(ctx, test.sessionID, "continue", 5)
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "invalid l1 memory event thread identity") {
					t.Fatalf("Recall error=%v, want existing L1 identity validation error", err)
				}
			} else if err != nil {
				t.Fatalf("Recall empty-session legacy row: %v", err)
			} else if len(got) != 1 || got[0].Msg != "legacy conversation survived restart" {
				t.Fatalf("Recall=(%+v), want the real unbound legacy L1 row", got)
			}
			if tracked.typedReadCalls != 0 {
				t.Fatalf("typed turn owner was queried %d times for a noncanonical session", tracked.typedReadCalls)
			}
		})
	}
}

func seedPendingRecallThreads(t *testing.T, ctx context.Context, l1 *l1sqlite.L1SQLiteStore, sessionID string) []domconv.ConversationTurnRequest {
	t.Helper()
	turns := make([]domconv.ConversationTurnRequest, 3)
	for index := range turns {
		request := managerTurnTestRequest(fmt.Sprintf("turn-recall-miss-%d-%s", index, sessionID), sessionID)
		request.Targets = []domconv.ConversationTurnTarget{domconv.ConversationTurnTargetRedisProjection, domconv.ConversationTurnTargetThreadFollowers}
		if index > 0 {
			request.Boundary = true
			request.BoundaryReason = "topic changed"
		}
		if _, err := l1.CommitConversationTurn(ctx, request); err != nil {
			t.Fatalf("commit turn %d: %v", index, err)
		}
		turns[index] = request
	}
	return turns
}

func TestRealConversationManagerReconcilesExactPublishedFinalAfterRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "publication-recovery.db")
	l1, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	redis := newMockRedisStore()
	archive := newTurnTestArchiveStore()
	summarizer := &countingTurnSummarizer{}
	manager := &RealConversationManager{redisStore: redis, l1Store: l1, archiveStore: archive, vectordbStore: &mockVectorDBStore{}, summarizer: summarizer}
	seed := managerTurnTestRequest("turn-publication-recovery-seed", "session-publication-recovery")
	seed.Targets = manager.ConversationTurnTargets()
	if _, err := manager.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	boundary := managerTurnTestRequest("turn-publication-recovery-boundary", seed.SessionID)
	boundary.Boundary = true
	boundary.BoundaryReason = "new topic"
	boundary.Targets = manager.ConversationTurnTargets()
	result, err := manager.CommitConversationTurn(ctx, boundary)
	if err != nil || result.Status != domconv.ConversationTurnPartial || result.ClosedThreadID == "" {
		t.Fatalf("boundary result=%+v err=%v", result, err)
	}
	if err := l1.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	l1, err = l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen after simulated crash: %v", err)
	}
	defer l1.Close()
	manager = &RealConversationManager{redisStore: redis, l1Store: l1, archiveStore: archive, vectordbStore: &mockVectorDBStore{}, summarizer: summarizer}
	reader := conversationTurnTraceEvents{}
	if err := manager.ReconcileConversationTurnResponsePublications(ctx, reader, 100); err != nil {
		t.Fatalf("reconcile unpublished response: %v", err)
	}
	if err := manager.DrainConversationTurnOutbox(ctx, 100); err != nil {
		t.Fatalf("drain without final event: %v", err)
	}
	if summarizer.calls != 0 {
		t.Fatalf("summary ran without a canonical final event: calls=%d", summarizer.calls)
	}
	candidates, err := l1.ListPendingConversationTurnThreadFollowers(ctx, boundary.SessionID, 100)
	if err != nil || len(candidates) != 1 || candidates[0].ResponsePublished {
		t.Fatalf("unpublished candidate=%+v err=%v", candidates, err)
	}
	wrong := modulecore.NewEventEnvelope(boundary.TraceID, "", nil, "orchestrator", "agent.response", time.Now().UTC(), map[string]any{"type": "agent.response", "to": "user"})
	wrong.TurnID = modulecore.NewTurnID()
	wrong.SessionID = modulecore.SessionID(boundary.SessionID)
	reader[boundary.TraceID] = []modulecore.EventEnvelope{wrong}
	if err := manager.ReconcileConversationTurnResponsePublications(ctx, reader, 100); err != nil {
		t.Fatalf("reconcile wrong-message event: %v", err)
	}
	candidates, err = l1.ListPendingConversationTurnThreadFollowers(ctx, boundary.SessionID, 100)
	if err != nil || len(candidates) != 1 || candidates[0].ResponsePublished {
		t.Fatalf("wrong-message event opened response gate: %+v err=%v", candidates, err)
	}
	legacyFinal := modulecore.NewEventEnvelope(boundary.TraceID, "", nil, "orchestrator", "agent.response", time.Now().UTC(), map[string]any{
		"type": "agent.response", "to": "user",
	})
	legacyFinal.SessionID = modulecore.SessionID(boundary.SessionID)
	reader[boundary.TraceID] = append(reader[boundary.TraceID], legacyFinal)
	if err := manager.ReconcileConversationTurnResponsePublications(ctx, reader, 100); err != nil {
		t.Fatalf("reconcile legacy final without TurnID: %v", err)
	}
	candidates, err = l1.ListPendingConversationTurnThreadFollowers(ctx, boundary.SessionID, 100)
	if err != nil || len(candidates) != 1 || candidates[0].ResponsePublished {
		t.Fatalf("legacy final without TurnID opened response gate: %+v err=%v", candidates, err)
	}
	if err := manager.ReconcileConversationTurnResponsePublications(ctx, reader, 100); err != nil {
		t.Fatalf("advance reconciliation cursor after legacy event: %v", err)
	}
	final := modulecore.NewEventEnvelope(boundary.TraceID, "", nil, "orchestrator", "agent.response", time.Now().UTC(), map[string]any{
		"type": "agent.response", "to": "user",
	})
	final.TurnID = boundary.TurnID
	final.SessionID = modulecore.SessionID(boundary.SessionID)
	final.TaskID = modulecore.NewTaskID() // Child task IDs may differ from the root task in the receipt.
	reader[boundary.TraceID] = append(reader[boundary.TraceID], final)
	if err := manager.ReconcileConversationTurnResponsePublications(ctx, reader, 100); err != nil {
		t.Fatalf("reconcile exact final event: %v", err)
	}
	candidates, err = l1.ListPendingConversationTurnThreadFollowers(ctx, boundary.SessionID, 100)
	if err != nil || len(candidates) != 1 || !candidates[0].ResponsePublished {
		t.Fatalf("exact published candidate=%+v err=%v", candidates, err)
	}
	if err := manager.DrainConversationTurnOutbox(ctx, 100); err != nil {
		t.Fatalf("drain recovered published final: %v", err)
	}
	if summarizer.calls != 1 || archive.byID[result.ClosedThreadID] == nil {
		t.Fatalf("recovered thread followers summary_calls=%d archived=%v", summarizer.calls, archive.byID[result.ClosedThreadID] != nil)
	}
}

func TestRealConversationManagerTerminalSummaryFailureKeepsReceiptPartialAndRawReply(t *testing.T) {
	ctx := context.Background()
	l1, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "summary-failure.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer l1.Close()
	redis := newMockRedisStore()
	archive := newTurnTestArchiveStore()
	summarizer := &countingTurnSummarizer{}
	archive.saveErr = errors.New("archive unavailable")
	manager := &RealConversationManager{redisStore: redis, l1Store: l1, archiveStore: archive, vectordbStore: &mockVectorDBStore{}, summarizer: summarizer}
	seed := managerTurnTestRequest("turn-summary-failure-seed", "session-summary-failure")
	seed.Targets = manager.ConversationTurnTargets()
	if _, err := manager.CommitConversationTurn(ctx, seed); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	boundary := managerTurnTestRequest("turn-summary-failure-boundary", seed.SessionID)
	boundary.Boundary = true
	boundary.BoundaryReason = "new topic"
	boundary.Targets = manager.ConversationTurnTargets()
	result, err := manager.CommitConversationTurn(ctx, boundary)
	if err != nil {
		t.Fatalf("boundary commit: %v", err)
	}
	if err := l1.MarkConversationTurnResponsePublished(ctx, boundary.TurnID, boundary.TraceID, modulecore.SessionID(boundary.SessionID), time.Now().UTC()); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	for attempt := 0; attempt < domconv.ConversationTurnMaxOutboxAttempts; attempt++ {
		if err := manager.DrainConversationTurnOutbox(ctx, 1); err == nil {
			t.Fatalf("summary attempt %d error=nil, want bounded follower failure", attempt+1)
		}
	}
	receipt, err := l1.GetConversationTurnReceipt(ctx, string(boundary.TurnID))
	if err != nil || receipt.Status != domconv.ConversationTurnPartial || receipt.ErrorCode == "" {
		t.Fatalf("failed summary receipt=%+v err=%v, want durable partial", receipt, err)
	}
	events, err := l1.LoadConversationThreadProjection(ctx, boundary.SessionID, result.ClosedThreadID)
	if err != nil {
		t.Fatalf("raw closed thread after summary failure: %v", err)
	}
	foundReply := false
	for _, event := range events {
		foundReply = foundReply || event.Message == seed.AgentMessage
	}
	if !foundReply || summarizer.calls != domconv.ConversationTurnMaxOutboxAttempts {
		t.Fatalf("summary retries=%d raw_full_reply_retained=%v", summarizer.calls, foundReply)
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
	if receipt.Status != domconv.ConversationTurnPartial || receipt.ErrorCode != domconv.ConversationTurnErrorUnavailable {
		t.Fatalf("terminal receipt=%+v, want partial with bounded follower failure", receipt)
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
	if err := l1.MarkConversationTurnResponsePublished(ctx, request.TurnID, request.TraceID, modulecore.SessionID(request.SessionID), time.Now().UTC()); err != nil {
		t.Fatalf("mark response published: %v", err)
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
	if err != nil || result.Status != domconv.ConversationTurnPartial {
		t.Fatalf("boundary commit result=%+v err=%v, want durable partial", result, err)
	}
	if err := l1.MarkConversationTurnResponsePublished(ctx, request.TurnID, request.TraceID, modulecore.SessionID(request.SessionID), time.Now().UTC()); err != nil {
		t.Fatalf("mark response published: %v", err)
	}
	if err := manager.DrainConversationTurnOutbox(ctx, 1); err == nil {
		t.Fatal("embed failure drain error=nil, want follower failure")
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
