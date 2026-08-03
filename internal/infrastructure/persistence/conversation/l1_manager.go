package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

// L1ConversationManager provides the always-available conversation path backed
// only by CORE-owned SQLite. Advanced Redis, archive, and vector recall remain
// optional and are still controlled by conversation.enabled.
type L1ConversationManager struct {
	store            l1StoreIface
	mu               sync.Mutex
	sessionThreadIDs map[string]int64
	agentStatuses    map[string]*domconv.AgentStatus
}

func NewL1ConversationManager(store l1StoreIface) *L1ConversationManager {
	return &L1ConversationManager{
		store:            store,
		sessionThreadIDs: map[string]int64{},
		agentStatuses:    map[string]*domconv.AgentStatus{},
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

	threadID := m.sessionThreadIDs[sessionID]
	if threadID == 0 {
		events, err := m.store.RecentBySession(ctx, sessionID, 1)
		if err != nil {
			return fmt.Errorf("load latest L1 conversation thread: %w", err)
		}
		if len(events) > 0 && events[0].ThreadID > 0 {
			threadID = events[0].ThreadID
		} else {
			threadID = time.Now().UTC().UnixNano()
		}
		m.sessionThreadIDs[sessionID] = threadID
	}

	return m.store.SaveMessage(
		ctx,
		sessionID,
		threadID,
		fmt.Sprintf("conv:%d", threadID),
		msg,
		l1sqlite.MemoryStateObserved,
	)
}

func (m *L1ConversationManager) SaveRecallTrace(ctx context.Context, trace domconv.RecallTrace) error {
	if m == nil || m.store == nil {
		return errors.New("L1 conversation store is not configured")
	}
	return m.store.SaveRecallTrace(ctx, trace)
}

func (m *L1ConversationManager) FlushThread(context.Context, int64) (*domconv.ThreadSummary, error) {
	return nil, errors.New("thread flush requires the advanced conversation runtime")
}

func (m *L1ConversationManager) IsNovelInformation(context.Context, domconv.Message) (bool, float32, error) {
	return false, 0, nil
}

func (m *L1ConversationManager) GetActiveThread(context.Context, string) (*domconv.Thread, error) {
	return nil, domconv.ErrThreadNotFound
}

func (m *L1ConversationManager) CreateThread(_ context.Context, sessionID string, domain string) (*domconv.Thread, error) {
	return domconv.NewThread(sessionID, domain), nil
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
