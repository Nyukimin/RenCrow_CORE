package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// L1ConversationManager provides the always-available conversation path backed
// only by CORE-owned SQLite. Advanced Redis, archive, and vector recall remain
// optional and are still controlled by conversation.enabled.
type L1ConversationManager struct {
	store             l1StoreIface
	mu                sync.Mutex
	sessionThreadRefs map[string]sessionThreadReference
	agentStatuses     map[string]*domconv.AgentStatus
}

type sessionThreadReference struct {
	ID   modulecore.ThreadID
	Seq  modulecore.ThreadSeq
	Kind modulecore.ThreadKind
}

func NewL1ConversationManager(store l1StoreIface) *L1ConversationManager {
	return &L1ConversationManager{
		store:             store,
		sessionThreadRefs: map[string]sessionThreadReference{},
		agentStatuses:     map[string]*domconv.AgentStatus{},
	}
}

func (m *L1ConversationManager) Recall(ctx context.Context, sessionID string, _ string, topK int) ([]domconv.Message, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("L1 conversation store is not configured")
	}
	limit := topK * 4
	if limit < 12 {
		limit = 12
	}
	events, err := m.store.RecentBySession(ctx, sessionID, limit)
	if err != nil {
		return nil, err
	}
	return l1EventsToMessages(events), nil
}

func (m *L1ConversationManager) Store(ctx context.Context, sessionID string, msg domconv.Message) error {
	if m == nil || m.store == nil {
		return errors.New("L1 conversation store is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("conversation session ID is required")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	thread, ok := m.sessionThreadRefs[sessionID]
	if !ok {
		threadID, threadSeq, threadKind, found, err := m.store.LatestConversationThreadReference(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("load latest L1 conversation thread: %w", err)
		}
		if found {
			thread = sessionThreadReference{ID: threadID, Seq: threadSeq, Kind: threadKind}
		} else {
			thread = sessionThreadReference{ID: modulecore.NewThreadID(), Seq: 1, Kind: modulecore.ThreadKindUserConversation}
		}
	}
	if err := validateSessionThreadReferenceTuple(thread); err != nil {
		return err
	}

	if err := m.store.SaveMessage(
		ctx,
		sessionID,
		thread.ID,
		thread.Seq,
		thread.Kind,
		fmt.Sprintf("conv:%s", thread.ID),
		msg,
		l1sqlite.MemoryStateObserved,
	); err != nil {
		return err
	}
	if !ok {
		if m.sessionThreadRefs == nil {
			m.sessionThreadRefs = make(map[string]sessionThreadReference)
		}
		m.sessionThreadRefs[sessionID] = thread
	}
	return nil
}

// CommitConversationTurn exposes the L1-only EndTurn path. Followers belong
// to RealConversationManager; accepting them here would silently reintroduce
// the legacy Store/flush route and lose the durable outbox contract.
func (m *L1ConversationManager) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	base := failedConversationTurnManagerResult(request, domconv.ConversationTurnErrorUnavailable)
	if m == nil || m.store == nil {
		return base, domconv.ErrConversationTurnUnavailable
	}
	normalized, err := domconv.NormalizeConversationTurnRequest(request)
	if err != nil {
		base.ErrorCode = domconv.ConversationTurnErrorInvalid
		return base, err
	}
	if len(normalized.Targets) > 0 {
		base.ErrorCode = domconv.ConversationTurnErrorInvalid
		return base, domconv.ErrConversationTurnInvalid
	}
	store, ok := m.store.(interface {
		CommitConversationTurn(context.Context, domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error)
	})
	if !ok {
		return base, domconv.ErrConversationTurnUnavailable
	}
	return store.CommitConversationTurn(ctx, normalized)
}

// ConversationTurnTargets intentionally returns no followers for the
// always-available L1 manager. Advanced projections are owned by
// RealConversationManager only.
func (m *L1ConversationManager) ConversationTurnTargets() []domconv.ConversationTurnTarget {
	return nil
}

// LoadActiveConversationThread returns the active thread resolved from the
// L1 active-thread projection, not from the legacy in-memory thread cache.
func (m *L1ConversationManager) LoadActiveConversationThread(ctx context.Context, sessionID string) (*domconv.Thread, error) {
	if m == nil || m.store == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	store, ok := m.store.(conversationTurnL1Store)
	if !ok {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	events, err := store.LoadActiveConversationThreadProjection(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return conversationThreadFromL1Projection(events, domconv.ThreadActive)
}

func (m *L1ConversationManager) SaveRecallTrace(ctx context.Context, trace domconv.RecallTrace) error {
	if m == nil || m.store == nil {
		return errors.New("L1 conversation store is not configured")
	}
	return m.store.SaveRecallTrace(ctx, trace)
}

func (m *L1ConversationManager) FlushThread(context.Context, modulecore.ThreadID) (*domconv.ThreadSummary, error) {
	return nil, errors.New("thread flush requires the advanced conversation runtime")
}

func (m *L1ConversationManager) IsNovelInformation(context.Context, domconv.Message) (bool, float32, error) {
	return false, 0, nil
}

func (m *L1ConversationManager) GetActiveThread(context.Context, string) (*domconv.Thread, error) {
	return nil, domconv.ErrThreadNotFound
}

func (m *L1ConversationManager) CreateThread(ctx context.Context, sessionID string, domain string) (*domconv.Thread, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("L1 conversation manager is not configured")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sessionThreadRefs == nil {
		m.sessionThreadRefs = make(map[string]sessionThreadReference)
	}
	ref, ok := m.sessionThreadRefs[sessionID]
	if !ok {
		threadID, threadSeq, threadKind, found, err := m.store.LatestConversationThreadReference(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("load latest L1 conversation thread: %w", err)
		}
		if found {
			ref = sessionThreadReference{ID: threadID, Seq: threadSeq, Kind: threadKind}
		} else {
			ref = sessionThreadReference{}
		}
	}
	seq := modulecore.ThreadSeq(1)
	if ref.ID != "" {
		if err := validateSessionThreadReferenceTuple(ref); err != nil {
			return nil, err
		}
		if ref.Seq == modulecore.ThreadSeq(1<<63-1) {
			return nil, errors.New("thread sequence overflow")
		}
		seq = ref.Seq + 1
	}
	thread, err := domconv.NewThread(sessionID, domain, domconv.ThreadKindUserConversation, seq)
	if err != nil {
		return nil, err
	}
	m.sessionThreadRefs[sessionID] = sessionThreadReference{ID: thread.ID, Seq: thread.ThreadSeq, Kind: thread.ThreadKind}
	return thread, nil
}

func validateSessionThreadReferenceTuple(ref sessionThreadReference) error {
	if ref.ID == "" {
		if ref.Seq != 0 || ref.Kind != "" {
			return errors.New("session thread reference must contain thread_id, thread_seq, and thread_kind")
		}
		return nil
	}
	if ref.ID.Validate() != nil || ref.Seq.Validate() != nil || ref.Kind.Validate() != nil {
		return errors.New("invalid session thread reference")
	}
	return nil
}

func (m *L1ConversationManager) GetAgentStatus(_ context.Context, agentName string) (*domconv.AgentStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if status := m.agentStatuses[agentName]; status != nil {
		cp := *status
		cp.KPI = copyKPI(status.KPI)
		return &cp, nil
	}
	return domconv.NewAgentStatus(agentName), nil
}

func (m *L1ConversationManager) UpdateAgentStatus(_ context.Context, status *domconv.AgentStatus) error {
	if status == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *status
	cp.KPI = copyKPI(status.KPI)
	m.agentStatuses[status.AgentName] = &cp
	return nil
}

var _ domconv.ConversationManager = (*L1ConversationManager)(nil)
