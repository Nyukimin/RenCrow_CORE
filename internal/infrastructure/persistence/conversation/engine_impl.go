package conversation

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

// RealConversationEngine は ConversationEngine の実装
// 既存の RealConversationManager をラップし、RecallPack 生成を追加
type RealConversationEngine struct {
	manager                  domconv.ConversationManager
	persona                  domconv.PersonaState
	detector                 domconv.ThreadBoundaryDetector // nil の場合もtrusted Mio proposalだけは利用できる
	knowledgeRelationEnabled bool
	knowledgeRelationMaxHops int
	userMemoryStore          conversationEngineUserMemoryStore
	userID                   string
	categoryRecallRegistry   domconv.CategoryRecallRegistry
	categoryRecallScope      string
	preparedRecallRuntime    *PreparedRecallRuntime
}

type conversationEngineExternalRecall interface {
	GetFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time) (*l1sqlite.L1SearchCacheEntry, error)
	SearchKnowledgeItemsFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error)
	SearchWikiPageIndex(ctx context.Context, query string, limit int) ([]l1sqlite.WikiPageIndexItem, error)
	SearchKB(ctx context.Context, domain string, query string, topK int) ([]*domconv.Document, error)
	RelatedKnowledgeItems(ctx context.Context, itemID string, maxHop int, limit int) ([]l1sqlite.L1KnowledgeRelationHit, error)
}

// conversationTurnCommitter is intentionally narrower than the legacy
// ConversationManager interface. Only managers that own the canonical L1
// EndTurn transaction may satisfy it.
type conversationTurnCommitter interface {
	CommitConversationTurn(context.Context, domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error)
}

type conversationTurnTargetProvider interface {
	ConversationTurnTargets() []domconv.ConversationTurnTarget
}

type conversationActiveThreadProvider interface {
	LoadActiveConversationThread(context.Context, string) (*domconv.Thread, error)
}

type conversationTurnPreparer interface {
	PrepareConversationTurn(context.Context, modulecore.TurnID, string, string) (*domconv.ConversationTurnPreparation, error)
	ReleaseConversationTurnPreparation(context.Context, *domconv.ConversationTurnPreparation) error
}

type conversationTurnPreparationValidator interface {
	ValidateConversationTurnPreparation(context.Context, *domconv.ConversationTurnPreparation) error
}

type conversationTurnPublicationEpochRetainer interface {
	retainConversationTurnPublicationEpoch(*domconv.ConversationTurnPreparation) (conversationTurnPublicationEpochGuard, error)
}

type conversationEngineUserMemoryStore interface {
	ListUserMemories(ctx context.Context, userID string, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error)
}

// PreparedRecallReadyOption is a turn-scoped capability bundle supplied by
// the trusted runtime. ExpectedSourceVersion must come from the source owner's
// current RAM binding; the engine verifies it against the immutable projection.
type PreparedRecallReadyOption struct {
	ProjectionOwner       *PreparedRecallProjectionOwner
	ExpectedSourceVersion string
	Preparation           *domconv.ConversationTurnPreparation
}

// preparedChatTurnGuard is the infrastructure implementation of the opaque
// domain guard. A private recall lease remains pinned until the caller releases
// the prepared turn; public turns carry only the canonical binding generation.
type preparedChatTurnGuard struct {
	mu              sync.Mutex
	activeUses      uint64
	binding         domconv.SessionBinding
	preparation     *domconv.ConversationTurnPreparation
	ownerID         string
	sourceVersion   string
	userScope       string
	selectInput     domconv.PreparedRecallSelectInput
	projectionLease *PreparedRecallProjectionReadLease
	headEpoch       conversationTurnPublicationEpochGuard
	preparer        conversationTurnPreparer
	validator       conversationTurnPreparationValidator
	released        bool
}

const preparedChatTurnPublicationMaxLifetime = 2 * time.Minute

type preparedChatTurnPublicationState struct {
	mu            sync.Mutex
	manager       *PreparedRecallProjectionOwner
	generation    *preparedRecallProjectionGeneration
	sourceVersion string
	headEpoch     conversationTurnPublicationEpochGuard
	expiresAt     time.Time
	refs          uint64
	activeUses    uint64
	expired       bool
	freed         bool
	timer         *time.Timer
}

type preparedChatTurnPublicationGuard struct {
	mu      sync.Mutex
	state   *preparedChatTurnPublicationState
	release bool
}

func newPreparedChatTurnPublicationGuard(manager *PreparedRecallProjectionOwner, generation *preparedRecallProjectionGeneration, sourceVersion string, headEpoch conversationTurnPublicationEpochGuard) domconv.PreparedChatTurnPublicationGuard {
	state := &preparedChatTurnPublicationState{
		manager: manager, generation: generation, sourceVersion: sourceVersion, headEpoch: headEpoch, refs: 1,
		expiresAt: time.Now().Add(preparedChatTurnPublicationMaxLifetime),
	}
	state.timer = time.AfterFunc(preparedChatTurnPublicationMaxLifetime, state.expire)
	return &preparedChatTurnPublicationGuard{state: state}
}

func (g *preparedChatTurnPublicationGuard) Use(claim func() error) error {
	if g == nil || claim == nil {
		return ErrPreparedRecallProjectionReleased
	}
	g.mu.Lock()
	if g.release || g.state == nil {
		g.mu.Unlock()
		return ErrPreparedRecallProjectionReleased
	}
	state := g.state
	state.mu.Lock()
	if state.expired || state.freed || state.refs == 0 || !time.Now().Before(state.expiresAt) {
		state.mu.Unlock()
		g.mu.Unlock()
		return ErrPreparedRecallProjectionStale
	}
	state.activeUses++
	state.mu.Unlock()
	g.mu.Unlock()

	state.mu.Lock()
	withinLifetime := !state.expired && time.Now().Before(state.expiresAt)
	state.mu.Unlock()
	var err error
	if !withinLifetime || state.headEpoch == nil {
		err = ErrPreparedRecallProjectionStale
	} else if state.manager == nil && state.generation == nil {
		err = state.headEpoch.Use(claim)
	} else if state.manager == nil || state.generation == nil {
		err = ErrPreparedRecallProjectionStale
	} else {
		state.manager.mu.Lock()
		generation := state.generation
		generation.mu.RLock()
		valid := state.manager.current == generation && state.manager.revokeEpoch == generation.revokeEpoch &&
			!generation.revoked && generation.sourceVersion == state.sourceVersion
		if valid {
			err = state.headEpoch.Use(claim)
		} else {
			err = ErrPreparedRecallProjectionStale
		}
		generation.mu.RUnlock()
		state.manager.mu.Unlock()
	}
	state.endUse()
	return err
}

func (g *preparedChatTurnPublicationGuard) Retain() domconv.PreparedChatTurnPublicationGuard {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.release || g.state == nil {
		return nil
	}
	state := g.state
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.expired || state.freed || state.refs == 0 || !time.Now().Before(state.expiresAt) {
		return nil
	}
	state.refs++
	return &preparedChatTurnPublicationGuard{state: state}
}

func (g *preparedChatTurnPublicationGuard) Release() {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.release || g.state == nil {
		g.mu.Unlock()
		return
	}
	g.release = true
	state := g.state
	g.mu.Unlock()
	state.releaseRef()
}

func (s *preparedChatTurnPublicationState) releaseRef() {
	s.mu.Lock()
	if s.refs > 0 {
		s.refs--
	}
	free := s.refs == 0 && s.activeUses == 0 && !s.freed
	if free {
		s.freed = true
	}
	timer := s.timer
	s.mu.Unlock()
	if free {
		if timer != nil {
			timer.Stop()
		}
		s.releaseQualifications()
	}
}

func (s *preparedChatTurnPublicationState) expire() {
	s.mu.Lock()
	s.expired = true
	s.refs = 0
	free := s.activeUses == 0 && !s.freed
	if free {
		s.freed = true
	}
	s.mu.Unlock()
	if free {
		s.releaseQualifications()
	}
}

func (s *preparedChatTurnPublicationState) endUse() {
	s.mu.Lock()
	if s.activeUses > 0 {
		s.activeUses--
	}
	free := (s.refs == 0 || s.expired) && s.activeUses == 0 && !s.freed
	if free {
		s.freed = true
	}
	timer := s.timer
	s.mu.Unlock()
	if free {
		if timer != nil {
			timer.Stop()
		}
		s.releaseQualifications()
	}
}

func (s *preparedChatTurnPublicationState) releaseQualifications() {
	if s == nil {
		return
	}
	if s.manager != nil && s.generation != nil {
		s.manager.releaseGeneration(s.generation)
	}
	if s.headEpoch != nil {
		s.headEpoch.Release()
	}
}

func (g *preparedChatTurnGuard) OwnerID() string {
	if g == nil {
		return ""
	}
	return g.ownerID
}

func (g *preparedChatTurnGuard) SourceVersion() string {
	if g == nil {
		return ""
	}
	return g.sourceVersion
}

func (g *preparedChatTurnGuard) BindingGeneration() domconv.BindingGeneration {
	if g == nil {
		return domconv.BindingGenerationUnavailable
	}
	return g.binding.BindingGeneration()
}

func (g *preparedChatTurnGuard) MatchesPreparation(preparation *domconv.ConversationTurnPreparation) bool {
	if g == nil || preparation == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.released && sameConversationTurnPreparation(
		cloneConversationTurnPreparation(g.preparation),
		cloneConversationTurnPreparation(preparation),
	)
}

// Validate checks the retained head lease and the projection generation using
// RAM-only owner state. The enclosing request must separately perform its
// canonical Session-generation check at publication time.
func (g *preparedChatTurnGuard) Validate() error {
	if g == nil {
		return preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.released || g.binding.Validate() != nil || g.binding.BindingGeneration() == domconv.BindingGenerationUnavailable ||
		g.preparation == nil || g.preparation.SessionID != string(g.binding.SessionID()) {
		return preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if g.validator != nil {
		if err := g.validator.ValidateConversationTurnPreparation(context.Background(), cloneConversationTurnPreparation(g.preparation)); err != nil {
			return preparedRecallReadyError(PreparedRecallReadyErrorConflict, err)
		}
	}
	if g.binding.Audience() == domconv.SessionAudiencePublic {
		if g.ownerID != "" || g.sourceVersion != "" || g.projectionLease != nil || g.preparation.OwnerID != "public:"+string(g.binding.SessionID()) {
			return preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
		}
		return nil
	}
	if g.binding.Audience() != domconv.SessionAudiencePrivate || g.preparation.OwnerID != g.binding.OwnerID() || g.ownerID != g.binding.OwnerID() ||
		g.userScope != "user:"+g.binding.OwnerID() || g.sourceVersion == "" || g.projectionLease == nil {
		return preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, ErrPreparedRecallProjectionDenied)
	}
	input := g.selectInput
	input.Now = timeNowUTC()
	selection, err := g.projectionLease.Select(input)
	if err != nil {
		return preparedRecallReadyProjectionError(err)
	}
	if selection.SourceVersion != g.sourceVersion || selection.PreparationStatus != domconv.PreparedRecallPreparationReady {
		return preparedRecallReadySelectionError(selection.PreparationStatus)
	}
	return nil
}

// Use serializes a bounded publication enqueue against projection and head
// lifecycle revocation. The caller composes this with SessionRepo.UseBinding;
// all guards protect only the RAM callback and are released before socket I/O.
func (g *preparedChatTurnGuard) Use(enqueue func() error) error {
	if g == nil || enqueue == nil {
		return preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	g.mu.Lock()
	if g.released || g.binding.Validate() != nil || g.binding.BindingGeneration() == domconv.BindingGenerationUnavailable || g.headEpoch == nil {
		g.mu.Unlock()
		return preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	lease := g.projectionLease
	headEpoch := g.headEpoch
	binding := g.binding
	ownerID := g.ownerID
	sourceVersion := g.sourceVersion
	preparation := cloneConversationTurnPreparation(g.preparation)
	if preparation == nil || preparation.SessionID != string(binding.SessionID()) {
		g.mu.Unlock()
		return preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
	}
	if binding.Audience() == domconv.SessionAudiencePublic {
		if lease != nil || ownerID != "" || sourceVersion != "" || preparation.OwnerID != "public:"+string(binding.SessionID()) {
			g.mu.Unlock()
			return preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
		}
	} else if binding.Audience() != domconv.SessionAudiencePrivate || preparation.OwnerID != binding.OwnerID() ||
		lease == nil || ownerID != binding.OwnerID() || sourceVersion == "" {
		g.mu.Unlock()
		return preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, ErrPreparedRecallProjectionDenied)
	}
	g.activeUses++
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		if g.activeUses > 0 {
			g.activeUses--
		}
		g.mu.Unlock()
	}()

	if binding.Audience() == domconv.SessionAudiencePublic {
		return headEpoch.Use(enqueue)
	}

	lease.mu.RLock()
	defer lease.mu.RUnlock()
	if lease.released || lease.generation == nil {
		return preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, ErrPreparedRecallProjectionReleased)
	}
	generation := lease.generation
	generation.mu.RLock()
	defer generation.mu.RUnlock()
	if generation.revoked || generation.sourceVersion != sourceVersion {
		return preparedRecallReadyError(PreparedRecallReadyErrorProjectionStale, ErrPreparedRecallProjectionStale)
	}
	return headEpoch.Use(enqueue)
}

// RetainPublication pins the exact projection generation and reset epoch while
// a Use callback is active. It intentionally does not retain the conversation
// head lease, which is released when ProcessMessage completes.
func (g *preparedChatTurnGuard) RetainPublication() (domconv.PreparedChatTurnPublicationGuard, error) {
	if g == nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	g.mu.Lock()
	if g.released || g.activeUses == 0 || g.headEpoch == nil {
		g.mu.Unlock()
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, ErrPreparedRecallProjectionReleased)
	}
	if g.binding.Audience() == domconv.SessionAudiencePublic {
		if g.ownerID != "" || g.sourceVersion != "" || g.projectionLease != nil || g.preparation == nil ||
			g.preparation.OwnerID != "public:"+string(g.binding.SessionID()) {
			g.mu.Unlock()
			return nil, preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
		}
		headEpoch := g.headEpoch
		g.mu.Unlock()
		retainedHead := headEpoch.Retain()
		if retainedHead == nil {
			return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, ErrPreparedRecallProjectionReleased)
		}
		return newPreparedChatTurnPublicationGuard(nil, nil, "", retainedHead), nil
	}
	if g.binding.Audience() != domconv.SessionAudiencePrivate || g.ownerID == "" || g.userScope != "user:"+g.ownerID ||
		g.sourceVersion == "" || g.binding.OwnerID() != g.ownerID || g.projectionLease == nil || g.projectionLease.generation == nil {
		g.mu.Unlock()
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, ErrPreparedRecallProjectionDenied)
	}
	lease := g.projectionLease
	generation := lease.generation
	ownerID := g.ownerID
	scope := g.userScope
	sourceVersion := g.sourceVersion
	headEpoch := g.headEpoch
	g.mu.Unlock()

	// Use holds this generation's read lock around the caller that requests the
	// retained ticket. The original lease remains pinned until its callback
	// returns, so this atomic reader increment cannot race generation removal.
	if generation.revoked || generation.sourceVersion != sourceVersion || lease.manager == nil ||
		lease.manager.ownerID != ownerID || lease.manager.scope != scope {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorProjectionStale, ErrPreparedRecallProjectionStale)
	}
	retainedHead := headEpoch.Retain()
	if retainedHead == nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, ErrPreparedRecallProjectionReleased)
	}
	generation.readers.Add(1)
	return newPreparedChatTurnPublicationGuard(lease.manager, generation, sourceVersion, retainedHead), nil
}

// Release idempotently frees both the recall snapshot pin and the manager's
// head lease. The manager treats a head already consumed by EndTurn as done.
func (g *preparedChatTurnGuard) Release() error {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	if g.released {
		g.mu.Unlock()
		return nil
	}
	g.released = true
	lease := g.projectionLease
	g.projectionLease = nil
	headEpoch := g.headEpoch
	g.headEpoch = nil
	preparer := g.preparer
	preparation := cloneConversationTurnPreparation(g.preparation)
	g.mu.Unlock()

	if lease != nil {
		lease.Release()
	}
	if headEpoch != nil {
		headEpoch.Release()
	}
	if preparer == nil || preparation == nil {
		return nil
	}
	return preparer.ReleaseConversationTurnPreparation(context.Background(), preparation)
}

const (
	PreparedRecallReadyErrorContextNotReady   = "context_not_ready"
	PreparedRecallReadyErrorConflict          = "conflict"
	PreparedRecallReadyErrorScopeDenied       = "scope_denied"
	PreparedRecallReadyErrorProjectionDenied  = "projection_denied"
	PreparedRecallReadyErrorProjectionStale   = "projection_stale"
	PreparedRecallReadyErrorProjectionMissing = "projection_unavailable"
)

// PreparedRecallReadyError is a typed fail-closed outcome from the opt-in
// ready path. Code is safe for runtime mapping; Cause is internal diagnostic
// detail and is never used to authorize a fallback.
type PreparedRecallReadyError struct {
	Code  string
	Cause error
}

func (e *PreparedRecallReadyError) Error() string {
	if e == nil {
		return "prepared recall ready path failed"
	}
	return "prepared recall ready path: " + e.Code
}

func (e *PreparedRecallReadyError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func PreparedRecallReadyErrorCodeOf(err error) string {
	var readyErr *PreparedRecallReadyError
	if errors.As(err, &readyErr) && readyErr != nil {
		return readyErr.Code
	}
	return ""
}

func preparedRecallReadyError(code string, cause error) error {
	return &PreparedRecallReadyError{Code: code, Cause: cause}
}

// NewRealConversationEngine は新しい ConversationEngine を作成
func NewRealConversationEngine(
	manager domconv.ConversationManager,
	persona domconv.PersonaState,
) *RealConversationEngine {
	return &RealConversationEngine{
		manager: manager,
		persona: persona,
	}
}

// WithDetector はスレッド境界検出器を設定する（オプション）
func (e *RealConversationEngine) WithDetector(d domconv.ThreadBoundaryDetector) *RealConversationEngine {
	e.detector = d
	return e
}

func (e *RealConversationEngine) WithUserMemoryStore(store conversationEngineUserMemoryStore, userID string) *RealConversationEngine {
	e.userMemoryStore = store
	e.userID = strings.TrimSpace(userID)
	return e
}

// SupportsConversationTurnPreparation reports whether the wrapped manager can
// acquire and validate the session-scoped RAM head lease required before
// combined route-and-reply generation.
func (e *RealConversationEngine) SupportsConversationTurnPreparation() bool {
	if e == nil || e.manager == nil {
		return false
	}
	_, canPrepare := e.manager.(conversationTurnPreparer)
	_, canValidate := e.manager.(conversationTurnPreparationValidator)
	return canPrepare && canValidate
}

// WithCategoryRecallRegistry wires deterministic category source selection into
// every normal conversation turn. It does not write recall results to history.
func (e *RealConversationEngine) WithCategoryRecallRegistry(registry domconv.CategoryRecallRegistry) *RealConversationEngine {
	e.categoryRecallRegistry = registry
	return e
}

func (e *RealConversationEngine) WithCategoryRecallScope(scope string) *RealConversationEngine {
	e.categoryRecallScope = strings.TrimSpace(scope)
	return e
}

// WithPreparedRecallRuntime attaches the current RAM-only immutable private
// recall view owner used by PrepareChatTurn. The runtime is never warmed from
// the request path.
func (e *RealConversationEngine) WithPreparedRecallRuntime(runtime *PreparedRecallRuntime) *RealConversationEngine {
	if e != nil {
		e.preparedRecallRuntime = runtime
	}
	return e
}

func (e *RealConversationEngine) WithKnowledgeRelationRecall(maxHops int) *RealConversationEngine {
	if maxHops < 1 {
		maxHops = 1
	}
	if maxHops > 2 {
		maxHops = 2
	}
	e.knowledgeRelationEnabled = true
	e.knowledgeRelationMaxHops = maxHops
	return e
}

// BeginTurn はターン開始時に Recall + RecallPack 構築を実行
func (e *RealConversationEngine) BeginTurn(ctx context.Context, sessionID string, userMessage string) (*domconv.RecallPack, error) {
	pack := &domconv.RecallPack{
		Persona:     e.persona,
		Constraints: domconv.DefaultConstraints(),
	}
	userMemoryErr := e.loadSharedUserMemory(ctx, userMessage, pack)
	if userMemoryErr != nil {
		log.Printf("[ConversationEngine] WARN: UserMemory recall failed: %v", userMemoryErr)
	}

	// Recall（想起）
	turntiming.Mark(ctx, turntiming.PointRecallStarted)
	var recallMessages []domconv.Message
	var err error
	if provider, ok := e.manager.(conversationTurnOwnerReadProvider); ok && strings.TrimSpace(e.userID) != "" {
		recallMessages, err = provider.recallForOwner(ctx, sessionID, e.userID, userMessage, 3)
	} else {
		recallMessages, err = e.manager.Recall(ctx, sessionID, userMessage, 3)
	}
	turntiming.Mark(ctx, turntiming.PointRecallEnded)
	if err != nil {
		if _, ownerScoped := e.manager.(conversationTurnOwnerReadProvider); ownerScoped && strings.TrimSpace(e.userID) != "" {
			return nil, err
		}
		log.Printf("[ConversationEngine] WARN: Recall failed: %v", err)
		pack.RejectedTraceItems = append(pack.RejectedTraceItems, domconv.RecallTraceItem{
			Layer: "L0", Kind: "conversation_recall_source_failure", Status: domconv.TraceStatusSourceFailure,
			Decision: "rejected", PromptSection: domconv.PromptSectionConversation,
			Reason: "conversation recall source unavailable", PromptIndex: -1,
		})
	} else {
		// Recall 結果を RecallPack に分類
		for _, msg := range recallMessages {
			if speaker, ok := domconv.CanonicalChatAgentSpeaker(msg.Speaker); ok {
				msg.Speaker = speaker
				pack.ShortContext = append(pack.ShortContext, msg)
				continue
			}
			switch {
			case msg.Speaker == domconv.SpeakerUser:
				// 短期記憶（Thread.Turns）: そのまま ShortContext に
				pack.ShortContext = append(pack.ShortContext, msg)

			case msg.Speaker == domconv.SpeakerSystem && strings.HasPrefix(msg.Msg, "[Summary]"):
				// 中期記憶（SQLite archive ThreadSummary）: MidSummaries に変換
				summary := strings.TrimPrefix(msg.Msg, "[Summary] ")
				pack.MidSummaries = append(pack.MidSummaries, domconv.ThreadSummary{
					Summary: summary,
				})

			case msg.Speaker == domconv.SpeakerSystem && strings.HasPrefix(msg.Msg, "[LongTermMemory]"):
				// 長期記憶（VectorDB）: LongFacts に変換
				fact := strings.TrimPrefix(msg.Msg, "[LongTermMemory] ")
				pack.LongFacts = append(pack.LongFacts, fact)

			default:
				// その他のシステムメッセージは LongFacts に
				if msg.Msg != "" {
					pack.LongFacts = append(pack.LongFacts, msg.Msg)
				}
			}
		}
	}

	activeDomain := ""
	turntiming.Mark(ctx, turntiming.PointActiveDomainStarted)
	var activeThread *domconv.Thread
	var activeThreadErr error
	if provider, ok := e.manager.(conversationTurnOwnerReadProvider); ok && strings.TrimSpace(e.userID) != "" {
		activeThread, activeThreadErr = provider.loadActiveConversationThreadForOwner(ctx, sessionID, e.userID)
	} else {
		activeThread, activeThreadErr = e.manager.GetActiveThread(ctx, sessionID)
	}
	if activeThreadErr != nil {
		if errors.Is(activeThreadErr, domconv.ErrThreadNotFound) {
			activeThreadErr = nil
		} else if _, ownerScoped := e.manager.(conversationTurnOwnerReadProvider); ownerScoped && strings.TrimSpace(e.userID) != "" {
			turntiming.Mark(ctx, turntiming.PointActiveDomainEnded)
			return nil, activeThreadErr
		}
	} else if activeThread != nil {
		activeDomain = strings.TrimSpace(activeThread.Domain)
	}
	turntiming.Mark(ctx, turntiming.PointActiveDomainEnded)
	nativeRecallProvenanceIntent := domconv.HasNativeRecallProvenanceIntent(ctx)
	categoryL1KnowledgeRecordIDs := map[string]struct{}{}
	categoryL1KnowledgeSummaries := map[string]struct{}{}
	failedCategoryL1KnowledgeRecordIDs := map[string]struct{}{}
	if e.categoryRecallRegistry != nil {
		turntiming.Mark(ctx, turntiming.PointCategoryStarted)
		scope := strings.TrimSpace(e.categoryRecallScope)
		if scope == "" {
			scope = strings.TrimSpace(e.userID)
		}
		if scope == "" {
			scope = "public"
		}
		if nativeRecallProvenanceIntent {
			scope = trustedCategoryRecallUserScope(ctx)
		}
		categoryResult, categoryErr := e.categoryRecallRegistry.Recall(ctx, domconv.CategoryRecallQuery{
			Message: userMessage, ActiveDomain: activeDomain, UserScope: scope, Time: timeNowUTC(), Limit: 3,
		})
		if categoryErr != nil {
			log.Printf("[ConversationEngine] WARN: Category recall failed: %v", categoryErr)
			pack.CategoryFailures = append(pack.CategoryFailures, domconv.CategoryRecallFailure{
				SourceID:   "category_registry",
				Code:       domconv.CategoryRecallFailureSourceUnavailable,
				State:      "unavailable",
				Reason:     categoryErr.Error(),
				Retryable:  true,
				ObservedAt: timeNowUTC(),
			})
		} else {
			for _, record := range categoryResult.Records {
				snippet := domconv.CategorySnippetFromRecord(record)
				pack.CategorySnippets = append(pack.CategorySnippets, snippet)
				if strings.EqualFold(strings.TrimSpace(snippet.SourceID), "knowledge_l1") {
					if recordID := strings.TrimSpace(snippet.RecordID); recordID != "" {
						categoryL1KnowledgeRecordIDs[recordID] = struct{}{}
					} else if summary := strings.TrimSpace(snippet.Summary); summary != "" {
						categoryL1KnowledgeSummaries[summary] = struct{}{}
					}
				}
			}
			pack.CategoryFailures = append(pack.CategoryFailures, categoryResult.Failures...)
			if nativeRecallProvenanceIntent {
				for _, failure := range categoryResult.Failures {
					if !strings.EqualFold(strings.TrimSpace(failure.SourceID), "knowledge_l1") {
						continue
					}
					if recordID := strings.TrimSpace(failure.RecordID); recordID != "" {
						failedCategoryL1KnowledgeRecordIDs[recordID] = struct{}{}
					}
				}
			}
		}
		turntiming.Mark(ctx, turntiming.PointCategoryEnded)
	}

	// Knowledge Base / SearchCache は、外部情報要求が明確な発話だけに使う。
	if externalRecall, ok := e.manager.(conversationEngineExternalRecall); ok && shouldUseExternalRecallForUserMessage(userMessage) {
		cacheEntry, err := externalRecall.GetFreshSearchCache(ctx, "web", userMessage, timeNowUTC())
		if err != nil {
			log.Printf("[ConversationEngine] WARN: SearchCache lookup failed: %v", err)
		} else if cacheEntry != nil {
			pack.SearchCacheSnippets = append(pack.SearchCacheSnippets, domconv.SearchCacheSnippet{
				Query:       cacheEntry.RawQuery,
				Provider:    cacheEntry.Provider,
				ResultsJSON: cacheEntry.ResultsJSON,
				SourceURLs:  cacheEntry.SourceURLs,
				RetrievedAt: cacheEntry.RetrievedAt,
				Roles:       []string{"chat", "worker", "coder"},
			})
		}

		// 現在のドメインを取得（CategoryRecallと共有）
		domain := activeDomain
		if domain == "" {
			domain = "general"
		}

		items, err := externalRecall.SearchKnowledgeItemsFTS(ctx, domain, userMessage, 3)
		if err != nil {
			log.Printf("[ConversationEngine] WARN: L1 Knowledge FTS failed: %v", err)
		} else {
			for _, item := range items {
				if nativeRecallProvenanceIntent {
					if _, rejected := failedCategoryL1KnowledgeRecordIDs[strings.TrimSpace(item.ID)]; rejected {
						continue
					}
				}
				snippet := strings.TrimSpace(item.SummaryDraft)
				if snippet == "" {
					snippet = strings.TrimSpace(item.RawText)
				}
				if snippet != "" && !isAdoptedCategoryL1Knowledge(item, categoryL1KnowledgeRecordIDs, categoryL1KnowledgeSummaries) {
					pack.KBSnippets = append(pack.KBSnippets, "[L1KB] "+snippet)
				}
			}
		}
		if e.knowledgeRelationEnabled && len(items) > 0 {
			relationSeeds := items
			if nativeRecallProvenanceIntent && len(failedCategoryL1KnowledgeRecordIDs) > 0 {
				relationSeeds = make([]l1sqlite.L1KnowledgeItem, 0, len(items))
				for _, item := range items {
					if _, rejected := failedCategoryL1KnowledgeRecordIDs[strings.TrimSpace(item.ID)]; rejected {
						continue
					}
					relationSeeds = append(relationSeeds, item)
				}
			}
			e.expandKnowledgeRelations(ctx, externalRecall, relationSeeds, pack, failedCategoryL1KnowledgeRecordIDs)
		}

		wikiItems, err := externalRecall.SearchWikiPageIndex(ctx, userMessage, 3)
		if err != nil {
			log.Printf("[ConversationEngine] WARN: WikiPageIndex search failed: %v", err)
		} else {
			for _, item := range wikiItems {
				summary := strings.TrimSpace(item.Summary)
				if summary == "" {
					summary = strings.TrimSpace(item.Title)
				}
				if summary != "" {
					pack.WikiSnippets = append(pack.WikiSnippets, domconv.WikiSnippet{
						PageID:      item.PageID,
						Title:       item.Title,
						Path:        item.Path,
						Summary:     summary,
						SourcePaths: append([]string(nil), item.SourcePaths...),
						Related:     append([]string(nil), item.Related...),
						UpdatedAt:   item.UpdatedAt,
						Roles:       []string{"chat", "worker", "coder"},
					})
				}
			}
		}

		// KB検索を実行
		kbDocs, err := externalRecall.SearchKB(ctx, domain, userMessage, 3)
		if err != nil {
			log.Printf("[ConversationEngine] WARN: SearchKB failed: %v", err)
		} else if len(kbDocs) > 0 {
			for _, doc := range kbDocs {
				pack.KBSnippets = append(pack.KBSnippets, "[VectorKB] "+doc.Content)
			}
		}
	}

	applyL0RollingSummary(pack, 6)
	budgeted := pack.ApplyRecallBudget(pack.Constraints.MaxTotalTokens, pack.Constraints.RecallBudgetRatio)
	return &budgeted, nil
}

// BeginTurnReady is an opt-in path for callers that have already prepared a
// RAM conversation head and a source-versioned immutable recall projection.
// It never falls back to the legacy database, registry, or external recall
// path when either preparation is missing or stale.
func (e *RealConversationEngine) BeginTurnReady(ctx context.Context, sessionID, userMessage string, option PreparedRecallReadyOption) (*domconv.RecallPack, error) {
	if e == nil || e.manager == nil || ctx == nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, err)
	}
	binding, ownerID, err := resolveBoundConversationOwner(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if binding.Audience() != domconv.SessionAudiencePrivate {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, domconv.ErrConversationTurnInvalid)
	}
	userScope := "user:" + ownerID
	now := timeNowUTC()
	if option.ProjectionOwner == nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorProjectionMissing, ErrPreparedRecallProjectionUnavailable)
	}

	preparation := option.Preparation
	if preparation == nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if preparation.SessionID != sessionID || preparation.TurnID.Validate() != nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
	}
	if strings.TrimSpace(preparation.OwnerID) != ownerID {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, domconv.ErrConversationTurnConflict)
	}
	if preparation.ActiveThread != nil && (preparation.ActiveThread.SessionID != sessionID || preparation.ActiveThread.Status != domconv.ThreadActive) {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
	}
	validator, ok := e.manager.(conversationTurnPreparationValidator)
	if !ok {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if err := validator.ValidateConversationTurnPreparation(ctx, preparation); err != nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorConflict, err)
	}

	lease, _, err := option.ProjectionOwner.Acquire(ownerID, userScope, option.ExpectedSourceVersion)
	if err != nil {
		return nil, preparedRecallReadyProjectionError(err)
	}
	defer lease.Release()
	selection, err := lease.Select(domconv.PreparedRecallSelectInput{
		AuthenticatedOwnerID:  ownerID,
		UserScope:             userScope,
		ExpectedSourceVersion: option.ExpectedSourceVersion,
		Message:               userMessage,
		Persona:               e.persona.Name,
		Role:                  "chat",
		Now:                   now,
		UserMemoryLimit:       domainmemory.UserMemoryRecallDefaultLimit,
		KnowledgeLimit:        domconv.PreparedRecallDefaultKnowledgeLimit,
	})
	if err != nil {
		return nil, preparedRecallReadyProjectionError(err)
	}
	if selection.PreparationStatus != domconv.PreparedRecallPreparationReady {
		return nil, preparedRecallReadySelectionError(selection.PreparationStatus)
	}

	pack := &domconv.RecallPack{
		Persona:     e.persona,
		Constraints: domconv.DefaultConstraints(),
	}
	applyPreparedRecallSelection(pack, selection, ownerID, now)
	if activeThread := cloneThreadForStore(preparation.ActiveThread); activeThread != nil {
		appendPreparedConversationShortContext(pack, activeThread)
	}
	applyL0RollingSummary(pack, 6)
	pack.RejectedTraceItems = append(pack.RejectedTraceItems, preparedRecallCoverageTrace(selection)...)
	budgeted := pack.ApplyRecallBudget(pack.Constraints.MaxTotalTokens, pack.Constraints.RecallBudgetRatio)
	return &budgeted, nil
}

// PrepareChatTurn acquires exactly one canonical conversation-head lease and
// assembles the ready RecallPack from that immutable head plus the current
// private projection, or from the public head alone. It never invokes the
// legacy recall path or warms a source projection on the request path.
func (e *RealConversationEngine) PrepareChatTurn(ctx context.Context, turnID, sessionID, userMessage string) (*domconv.PreparedChatTurn, error) {
	if e == nil || e.manager == nil || ctx == nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, err)
	}
	binding, ownerID, err := resolveBoundConversationOwner(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	canonicalTurnID := modulecore.TurnID(turnID)
	if canonicalTurnID.Validate() != nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnInvalid)
	}

	preparation, err := e.PrepareConversationTurn(ctx, turnID, sessionID)
	if err != nil {
		code := PreparedRecallReadyErrorContextNotReady
		if errors.Is(err, domconv.ErrConversationTurnConflict) {
			code = PreparedRecallReadyErrorConflict
		}
		if preparation != nil {
			err = e.failPreparedChatTurn(ctx, preparation, code, err)
			return nil, err
		}
		return nil, preparedRecallReadyError(code, err)
	}
	if preparation == nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if preparation.TurnID != canonicalTurnID || preparation.SessionID != sessionID || preparation.OwnerID != ownerID {
		return nil, e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
	}
	preparer, canRelease := e.manager.(conversationTurnPreparer)
	validator, canValidate := e.manager.(conversationTurnPreparationValidator)
	if !canRelease || !canValidate {
		return nil, e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	epochRetainer, canRetainEpoch := e.manager.(conversationTurnPublicationEpochRetainer)
	if !canRetainEpoch {
		return nil, e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	headEpoch, err := epochRetainer.retainConversationTurnPublicationEpoch(preparation)
	if err != nil || headEpoch == nil {
		if err == nil {
			err = domconv.ErrConversationTurnUnavailable
		}
		return nil, e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorContextNotReady, err)
	}
	headEpochTransferred := false
	defer func() {
		if !headEpochTransferred {
			headEpoch.Release()
		}
	}()

	var pack *domconv.RecallPack
	var guard *preparedChatTurnGuard
	switch binding.Audience() {
	case domconv.SessionAudiencePrivate:
		runtime := e.preparedRecallRuntime
		if runtime == nil {
			return nil, e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorProjectionMissing, ErrPreparedRecallRuntimeUnavailable)
		}
		capability, available := runtime.CurrentCapability()
		if !available {
			return nil, e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorProjectionMissing, ErrPreparedRecallRuntimeUnavailable)
		}
		if capability.OwnerID() != binding.OwnerID() || capability.UserScope() != "user:"+binding.OwnerID() {
			return nil, e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorScopeDenied, ErrPreparedRecallRuntimeOwner)
		}
		option, ready := capability.ReadyOption(preparation)
		if !ready {
			return nil, e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorProjectionMissing, ErrPreparedRecallRuntimeUnavailable)
		}
		turntiming.Mark(ctx, turntiming.PointRecallProjectionStarted)
		pack, err = e.BeginTurnReady(ctx, sessionID, userMessage, option)
		if err == nil && pack != nil {
			lease, _, acquireErr := option.ProjectionOwner.Acquire(capability.OwnerID(), capability.UserScope(), capability.SourceVersion())
			if acquireErr != nil {
				err = preparedRecallReadyProjectionError(acquireErr)
			} else {
				guard = &preparedChatTurnGuard{
					binding: binding, preparation: cloneConversationTurnPreparation(preparation),
					ownerID: capability.OwnerID(), sourceVersion: capability.SourceVersion(), userScope: capability.UserScope(),
					preparer: preparer, validator: validator,
					selectInput: domconv.PreparedRecallSelectInput{
						AuthenticatedOwnerID: capability.OwnerID(), UserScope: capability.UserScope(),
						ExpectedSourceVersion: capability.SourceVersion(), Message: userMessage,
						Persona: e.persona.Name, Role: "chat", Now: timeNowUTC(),
						UserMemoryLimit: domainmemory.UserMemoryRecallDefaultLimit,
						KnowledgeLimit:  domconv.PreparedRecallDefaultKnowledgeLimit,
					},
					projectionLease: lease, headEpoch: headEpoch,
				}
			}
		}
		turntiming.Mark(ctx, turntiming.PointRecallProjectionEnded)
	case domconv.SessionAudiencePublic:
		pack, err = e.beginPublicTurnReady(ctx, sessionID, userMessage, preparation)
		if err == nil && pack != nil {
			guard = &preparedChatTurnGuard{
				binding: binding, preparation: cloneConversationTurnPreparation(preparation),
				preparer: preparer, validator: validator, headEpoch: headEpoch,
			}
		}
	default:
		err = preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, domconv.ErrConversationTurnInvalid)
	}
	if err != nil {
		code := PreparedRecallReadyErrorCodeOf(err)
		if code == "" {
			code = PreparedRecallReadyErrorContextNotReady
		}
		return nil, e.failPreparedChatTurn(ctx, preparation, code, err)
	}
	if pack == nil {
		return nil, e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if guard == nil {
		_ = e.failPreparedChatTurn(ctx, preparation, PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	headEpochTransferred = true
	return &domconv.PreparedChatTurn{RecallPack: pack, Preparation: preparation, Guard: guard}, nil
}

func (e *RealConversationEngine) beginPublicTurnReady(ctx context.Context, sessionID, userMessage string, preparation *domconv.ConversationTurnPreparation) (*domconv.RecallPack, error) {
	if e == nil || e.manager == nil || ctx == nil || preparation == nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	_, ownerID, err := resolveBoundConversationOwner(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if preparation.SessionID != sessionID || preparation.OwnerID != ownerID || preparation.TurnID.Validate() != nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
	}
	if preparation.ActiveThread != nil && (preparation.ActiveThread.SessionID != sessionID || preparation.ActiveThread.Status != domconv.ThreadActive) {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
	}
	validator, ok := e.manager.(conversationTurnPreparationValidator)
	if !ok {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if err := validator.ValidateConversationTurnPreparation(ctx, preparation); err != nil {
		return nil, preparedRecallReadyError(PreparedRecallReadyErrorConflict, err)
	}
	pack := &domconv.RecallPack{Persona: e.persona, Constraints: domconv.DefaultConstraints()}
	if preparation.ActiveThread != nil {
		appendPreparedConversationShortContext(pack, cloneThreadForStore(preparation.ActiveThread))
	}
	applyL0RollingSummary(pack, 6)
	budgeted := pack.ApplyRecallBudget(pack.Constraints.MaxTotalTokens, pack.Constraints.RecallBudgetRatio)
	return &budgeted, nil
}

func (e *RealConversationEngine) failPreparedChatTurn(ctx context.Context, preparation *domconv.ConversationTurnPreparation, code string, cause error) error {
	if preparation != nil {
		if releaseErr := e.ReleaseConversationTurnPreparation(context.Background(), preparation); releaseErr != nil {
			cause = errors.Join(cause, releaseErr)
		}
	}
	return preparedRecallReadyError(code, cause)
}

func resolveBoundConversationOwner(ctx context.Context, sessionID string) (domconv.SessionBinding, string, error) {
	binding, found := domconv.SessionBindingFromContext(ctx)
	if !found {
		return domconv.SessionBinding{}, "", preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if err := binding.Validate(); err != nil {
		return domconv.SessionBinding{}, "", preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, err)
	}
	if binding.BindingGeneration() == domconv.BindingGenerationUnavailable {
		return domconv.SessionBinding{}, "", preparedRecallReadyError(PreparedRecallReadyErrorContextNotReady, domconv.ErrConversationTurnUnavailable)
	}
	if modulecore.SessionID(sessionID).Validate() != nil || string(binding.SessionID()) != sessionID {
		return domconv.SessionBinding{}, "", preparedRecallReadyError(PreparedRecallReadyErrorConflict, domconv.ErrConversationTurnConflict)
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if found && scope.Validate() != nil {
		return domconv.SessionBinding{}, "", preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, domconv.ErrConversationTurnInvalid)
	}
	switch binding.Audience() {
	case domconv.SessionAudiencePublic:
		if found && !scope.Allows(domaintool.DataScopePublic) {
			return domconv.SessionBinding{}, "", preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, domconv.ErrConversationTurnInvalid)
		}
		return binding, "public:" + sessionID, nil
	case domconv.SessionAudiencePrivate:
		if !found {
			return domconv.SessionBinding{}, "", preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, domconv.ErrConversationTurnInvalid)
		}
		proof, hasProof := domaintool.AuthenticatedHTTPUserScopeFromContext(ctx)
		ownerID := binding.OwnerID()
		if !scope.Allows(domaintool.DataScopeUser) || strings.TrimSpace(scope.AuthenticatedUserID) != ownerID ||
			!hasProof || proof.AuthenticatedUserID != ownerID {
			return domconv.SessionBinding{}, "", preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, domconv.ErrConversationTurnInvalid)
		}
		return binding, ownerID, nil
	default:
		return domconv.SessionBinding{}, "", preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, domconv.ErrConversationTurnInvalid)
	}
}

func preparedRecallReadyProjectionError(err error) error {
	switch {
	case errors.Is(err, ErrPreparedRecallProjectionDenied):
		return preparedRecallReadyError(PreparedRecallReadyErrorScopeDenied, err)
	case errors.Is(err, ErrPreparedRecallProjectionStale):
		return preparedRecallReadyError(PreparedRecallReadyErrorProjectionStale, err)
	default:
		return preparedRecallReadyError(PreparedRecallReadyErrorProjectionMissing, err)
	}
}

func preparedRecallReadySelectionError(status string) error {
	switch status {
	case domconv.PreparedRecallPreparationDenied:
		return preparedRecallReadyError(PreparedRecallReadyErrorProjectionDenied, ErrPreparedRecallProjectionDenied)
	case domconv.PreparedRecallPreparationStale:
		return preparedRecallReadyError(PreparedRecallReadyErrorProjectionStale, ErrPreparedRecallProjectionStale)
	default:
		return preparedRecallReadyError(PreparedRecallReadyErrorProjectionMissing, ErrPreparedRecallProjectionUnavailable)
	}
}

func applyPreparedRecallSelection(pack *domconv.RecallPack, selection domconv.PreparedRecallSelection, ownerID string, now time.Time) {
	if pack == nil {
		return
	}
	profile := domconv.UserProfile{UserID: ownerID, Preferences: map[string]string{}, Facts: []string{}, UpdatedAt: now}
	for _, selected := range selection.UserMemories {
		decision := selected.Decision
		pack.UserMemoryRecallDecisions = append(pack.UserMemoryRecallDecisions, decision)
		if !decision.Selected {
			continue
		}
		statement := strings.TrimSpace(decision.Item.Statement)
		if statement == "" {
			continue
		}
		if decision.Item.State == domainmemory.MemoryStatePinned {
			statement = "[優先] " + statement
		}
		profile.Facts = append(profile.Facts, statement)
		if len(profile.Facts) >= 12 {
			break
		}
	}
	if len(profile.Facts) > 0 {
		pack.UserProfile = profile
	}
	for _, selected := range selection.Knowledge {
		record := selected.Record
		// Owner scope was checked by the authenticated projection selection.
		// RecallPack's existing Scope field is then consumed as the prompt-role
		// policy, so retain only the narrower role grant here.
		record.Scope = "chat"
		pack.CategorySnippets = append(pack.CategorySnippets, domconv.CategorySnippetFromRecord(record))
	}
}

func appendPreparedConversationShortContext(pack *domconv.RecallPack, thread *domconv.Thread) {
	if pack == nil || thread == nil {
		return
	}
	turns := thread.Turns
	if len(turns) > 12 {
		turns = turns[len(turns)-12:]
	}
	for _, message := range turns {
		if speaker, ok := domconv.CanonicalChatAgentSpeaker(message.Speaker); ok {
			message.Speaker = speaker
			pack.ShortContext = append(pack.ShortContext, message)
			continue
		}
		if message.Speaker == domconv.SpeakerUser {
			pack.ShortContext = append(pack.ShortContext, message)
		}
	}
}

func preparedRecallCoverageTrace(selection domconv.PreparedRecallSelection) []domconv.RecallTraceItem {
	truncation := make([]string, 0, 5)
	if selection.Truncated.Keys {
		truncation = append(truncation, "keys")
	}
	if selection.Truncated.QueryRunes {
		truncation = append(truncation, "query_runes")
	}
	if selection.Truncated.PostingVisits {
		truncation = append(truncation, "posting_visits")
	}
	if selection.Truncated.UserMemoryCandidates {
		truncation = append(truncation, "user_memory_candidates")
	}
	if selection.Truncated.KnowledgeCandidates {
		truncation = append(truncation, "knowledge_candidates")
	}
	truncationText := "none"
	if len(truncation) > 0 {
		truncationText = strings.Join(truncation, ",")
	}
	return []domconv.RecallTraceItem{
		{
			Layer: "L1", Kind: "prepared_recall_projection", SourceID: selection.SourceVersion,
			SourceType: "user_memory", Decision: "observed", Status: preparedRecallCoverageStatus(selection.Coverage.UserMemory),
			PromptSection: domconv.PromptSectionUserMemory, PromptIndex: -1,
			Reason: fmt.Sprintf("coverage=%s match=%s keys=%d posting_visits=%d candidates=%d truncated=%s", selection.Coverage.UserMemory, selection.MatchStatus, selection.KeysUsed, selection.PostingVisits, selection.UserMemoryCandidateCount, truncationText),
		},
		{
			Layer: "L1", Kind: "prepared_recall_projection", SourceID: selection.SourceVersion,
			SourceType: "knowledge", Decision: "observed", Status: preparedRecallCoverageStatus(selection.Coverage.Knowledge),
			PromptSection: domconv.PromptSectionKnowledge, PromptIndex: -1,
			Reason: fmt.Sprintf("coverage=%s match=%s keys=%d posting_visits=%d candidates=%d truncated=%s", selection.Coverage.Knowledge, selection.MatchStatus, selection.KeysUsed, selection.PostingVisits, selection.KnowledgeCandidateCount, truncationText),
		},
	}
}

func preparedRecallCoverageStatus(coverage string) string {
	if coverage == domconv.PreparedRecallCoverageFull {
		return domconv.TraceStatusRetrieved
	}
	return domconv.CategoryRecallStatusPartial
}

func trustedCategoryRecallUserScope(ctx context.Context) string {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.AuthenticatedUserID == "" || !scope.Allows(domaintool.DataScopeUser) {
		return "public"
	}
	return "user:" + scope.AuthenticatedUserID
}

func isAdoptedCategoryL1Knowledge(item l1sqlite.L1KnowledgeItem, recordIDs map[string]struct{}, summaries map[string]struct{}) bool {
	// Record IDs are the authoritative deduplication key. Summaries are only a
	// fallback for legacy category records that have no RecordID.
	if _, ok := recordIDs[strings.TrimSpace(item.ID)]; ok && strings.TrimSpace(item.ID) != "" {
		return true
	}
	summary := strings.TrimSpace(item.SummaryDraft)
	if summary == "" {
		summary = strings.TrimSpace(item.RawText)
	}
	if summary == "" {
		return false
	}
	_, ok := summaries[summary]
	return ok
}

func (e *RealConversationEngine) loadSharedUserMemory(ctx context.Context, query string, pack *domconv.RecallPack) error {
	if e.userMemoryStore == nil || pack == nil || e.userID == "" {
		return nil
	}
	turntiming.Mark(ctx, turntiming.PointUserMemoryStarted)
	defer turntiming.Mark(ctx, turntiming.PointUserMemoryEnded)
	// Scan only supply-eligible states. A recency-ordered scan across all
	// states lets a growing candidate backlog crowd every confirmed/pinned
	// memory out of the bounded window, so nothing ever reaches the prompt.
	items := make([]domainmemory.UserMemory, 0, domainmemory.UserMemoryRecallMaxScan)
	for _, state := range []string{domainmemory.MemoryStatePinned, domainmemory.MemoryStateConfirmed} {
		stateItems, err := e.userMemoryStore.ListUserMemories(ctx, e.userID, state, false, domainmemory.UserMemoryRecallMaxScan-len(items))
		if err != nil {
			pack.UserMemoryRecallDecisions = append(pack.UserMemoryRecallDecisions, domainmemory.UserMemoryRecallDecision{
				Status: domainmemory.UserMemoryRecallStatusSourceFailure,
				Reason: "user memory source unavailable",
			})
			return err
		}
		items = append(items, stateItems...)
		if len(items) >= domainmemory.UserMemoryRecallMaxScan {
			break
		}
	}
	decisions := domainmemory.RankUserMemoriesForRecallForPersona(query, items, domainmemory.UserMemoryRecallDefaultLimit, e.persona.Name)
	pack.UserMemoryRecallDecisions = decisions
	profile := domconv.NewUserProfile(e.userID)
	for i := range pack.UserMemoryRecallDecisions {
		decision := &pack.UserMemoryRecallDecisions[i]
		if decision.Selected && !isSharedUserMemoryPromptInjectable(decision.Item) {
			decision.Selected = false
			decision.Status = domainmemory.UserMemoryRecallStatusFilteredScope
			decision.Reason = "memory scope is not available to this persona"
			continue
		}
		if !decision.Selected {
			continue
		}
		item := decision.Item
		statement := strings.TrimSpace(item.Statement)
		if statement == "" {
			continue
		}
		if item.State == domainmemory.MemoryStatePinned {
			statement = "[優先] " + statement
		}
		profile.Facts = append(profile.Facts, statement)
		if len(profile.Facts) >= 12 {
			break
		}
	}
	if len(profile.Facts) > 0 {
		pack.UserProfile = profile
	}
	return nil
}

func isSharedUserMemoryPromptInjectable(item domainmemory.UserMemory) bool {
	for _, persona := range []string{"mio", "shiro", "kuro", "midori"} {
		if domainmemory.IsUserMemoryPromptInjectable(item, persona) {
			return true
		}
	}
	return false
}

func (e *RealConversationEngine) expandKnowledgeRelations(ctx context.Context, recall conversationEngineExternalRecall, seeds []l1sqlite.L1KnowledgeItem, pack *domconv.RecallPack, excludedIDs map[string]struct{}) {
	if pack == nil || recall == nil {
		return
	}
	seen := make(map[string]bool, len(seeds))
	for _, seed := range seeds {
		seen[seed.ID] = true
	}
	for _, seed := range seeds {
		hits, err := recall.RelatedKnowledgeItems(ctx, seed.ID, e.knowledgeRelationMaxHops, 3)
		if err != nil {
			log.Printf("[ConversationEngine] WARN: Knowledge Relation lookup failed for item=%s: %v", seed.ID, err)
			continue
		}
		for _, hit := range hits {
			itemID := strings.TrimSpace(hit.Item.ID)
			if _, rejected := excludedIDs[itemID]; rejected || seen[itemID] {
				continue
			}
			seen[itemID] = true
			summary := strings.TrimSpace(hit.Item.SummaryDraft)
			if summary == "" {
				summary = strings.TrimSpace(hit.Item.RawText)
			}
			if summary == "" {
				continue
			}
			pack.RelationSnippets = append(pack.RelationSnippets, domconv.RelationSnippet{
				ItemID: hit.Item.ID, Title: hit.Item.Title, Summary: summary, SourceType: hit.Item.Domain,
				RelationType: hit.RelationType, Score: hit.Score, Evidence: hit.Evidence, Hop: hit.Hop,
				Roles: []string{"chat", "worker", "coder"},
			})
			if len(pack.RelationSnippets) >= 3 {
				return
			}
		}
	}
}

func shouldUseExternalRecallForUserMessage(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" || looksLikePersonalMemoryQuestion(message) {
		return false
	}
	direct := []string{"検索", "調べて", "調査して"}
	for _, marker := range direct {
		if strings.Contains(message, marker) {
			return true
		}
	}
	timely := []string{
		"最新", "ニュース", "今日", "昨日", "今週", "今月", "今年", "最近", "現在", "速報",
		"2024", "2025", "2026", "2027", "天気", "価格", "相場", "株価", "為替",
	}
	for _, marker := range timely {
		if strings.Contains(message, marker) {
			return true
		}
	}
	topic := []string{"について教えて", "について調べて", "について検索", "とは", "仕様", "API", "Wiki", "RecallPack", "Source Registry", "RenCrow_CMD", "rencrow"}
	for _, marker := range topic {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func looksLikePersonalMemoryQuestion(message string) bool {
	selfMarkers := []string{"俺", "私", "僕", "ぼく", "わたし", "自分"}
	recallMarkers := []string{"知ってる", "覚えてる", "覚えていた", "覚えている", "記憶してる", "記憶している"}
	hasSelf := false
	for _, marker := range selfMarkers {
		if strings.Contains(message, marker) {
			hasSelf = true
			break
		}
	}
	if !hasSelf {
		return false
	}
	for _, marker := range recallMarkers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func applyL0RollingSummary(pack *domconv.RecallPack, keepRecent int) {
	if pack == nil || keepRecent <= 0 || len(pack.ShortContext) <= keepRecent {
		return
	}
	cut := len(pack.ShortContext) - keepRecent
	older := pack.ShortContext[:cut]
	pack.ShortContext = append([]domconv.Message(nil), pack.ShortContext[cut:]...)
	var lines []string
	for _, msg := range older {
		text := strings.TrimSpace(msg.Msg)
		if text == "" {
			continue
		}
		lines = append(lines, string(msg.Speaker)+": "+text)
	}
	if len(lines) == 0 {
		return
	}
	summary := strings.Join(lines, " / ")
	if strings.TrimSpace(pack.RollingSummary) != "" {
		pack.RollingSummary = strings.TrimSpace(pack.RollingSummary) + " / " + summary
		return
	}
	pack.RollingSummary = summary
}

var timeNowUTC = func() time.Time {
	return time.Now().UTC()
}

// CommitConversationTurn is the typed atomic EndTurn route. Admitted calls must
// carry the prepared head; synchronous calls resolve the active L1 head here.
// Caller-supplied ownership, targets, domain and boundary decisions are ignored.
func (e *RealConversationEngine) CommitConversationTurn(ctx context.Context, request domconv.ConversationTurnRequest) (domconv.ConversationTurnResult, error) {
	base := failedConversationTurnManagerResult(request, domconv.ConversationTurnErrorUnavailable)
	if e == nil || e.manager == nil {
		return base, domconv.ErrConversationTurnUnavailable
	}
	ownerID := strings.TrimSpace(e.userID)
	_, bindingPresent := domconv.SessionBindingFromContext(ctx)
	if bindingPresent {
		_, boundOwnerID, err := resolveBoundConversationOwner(ctx, request.SessionID)
		if err != nil {
			base.ErrorCode = domconv.ConversationTurnErrorInvalid
			if errors.Is(err, domconv.ErrConversationTurnConflict) {
				base.ErrorCode = domconv.ConversationTurnErrorConflict
			}
			return base, err
		}
		ownerID = boundOwnerID
	}
	if ownerID == "" {
		return base, domconv.ErrConversationTurnUnavailable
	}
	committer, ok := e.manager.(conversationTurnCommitter)
	if !ok {
		return base, domconv.ErrConversationTurnUnavailable
	}
	provider, ok := e.manager.(conversationTurnTargetProvider)
	if !ok {
		return base, domconv.ErrConversationTurnUnavailable
	}
	// Admission means EndTurn must use the already prepared RAM head. Refuse
	// missing preparation before the active-thread provider can perform a cold
	// L1 read; verified synchronous callers without Admission retain the legacy
	// path.
	if request.Preparation == nil {
		if _, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
			base.ErrorCode = domconv.ConversationTurnErrorUnavailable
			return base, domconv.ErrConversationTurnUnavailable
		}
	}
	activeProvider, ok := e.manager.(conversationActiveThreadProvider)
	if !ok {
		return base, domconv.ErrConversationTurnUnavailable
	}

	canonicalSpeaker, ok := domconv.CanonicalChatAgentSpeaker(request.AgentSpeaker)
	if !ok || canonicalSpeaker != request.AgentSpeaker {
		base.ErrorCode = domconv.ConversationTurnErrorInvalid
		return base, domconv.ErrConversationTurnInvalid
	}
	mioBoundary, mioAttested, err := agent.MioThreadBoundaryProposalFromContext(ctx, request, ownerID)
	if err != nil {
		base.ErrorCode = domconv.ConversationTurnErrorCodeOf(err)
		if base.ErrorCode == "" {
			base.ErrorCode = domconv.ConversationTurnErrorConflict
		}
		return base, err
	}

	preparation := cloneConversationTurnPreparation(request.Preparation)
	var activeThread *domconv.Thread
	if preparation != nil {
		if preparation.TurnID != request.TurnID || preparation.SessionID != request.SessionID ||
			preparation.OwnerID != ownerID ||
			(preparation.ActiveThread != nil && (preparation.ActiveThread.SessionID != request.SessionID || preparation.ActiveThread.Status != domconv.ThreadActive)) {
			base.ErrorCode = domconv.ConversationTurnErrorConflict
			return base, domconv.ErrConversationTurnConflict
		}
		validator, ok := e.manager.(conversationTurnPreparationValidator)
		if !ok {
			return base, domconv.ErrConversationTurnUnavailable
		}
		if err := validator.ValidateConversationTurnPreparation(ctx, preparation); err != nil {
			base.ErrorCode = domconv.ConversationTurnErrorCodeOf(err)
			if base.ErrorCode == "" {
				base.ErrorCode = domconv.ConversationTurnErrorConflict
			}
			return base, err
		}
		activeThread = preparation.ActiveThread
	} else {
		activeThread, err = activeProvider.LoadActiveConversationThread(ctx, request.SessionID)
	}
	domain := "general"
	boundary := false
	reason := ""
	if err == nil {
		if activeThread == nil {
			// A missing active head is the normal first-turn case. The manager
			// creates the canonical thread while committing the synchronous turn.
		} else {
			domain = strings.TrimSpace(activeThread.Domain)
			if domain == "" {
				return base, domconv.ErrConversationTurnInvalid
			}
			decision := domconv.ThreadBoundaryResult{Proposal: domconv.ThreadBoundaryUnknown}
			if e.detector != nil {
				turntiming.Mark(ctx, turntiming.PointConversationDetectorStarted)
				decision = e.detector.Detect(activeThread, request.UserMessage)
				turntiming.Mark(ctx, turntiming.PointConversationDetectorEnded)
			}
			switch decision.Proposal {
			case domconv.ThreadBoundaryKeep:
				// Deterministic owner continuation cues outrank Mio's semantic proposal.
			case domconv.ThreadBoundaryNew:
				if decision.Reason == domconv.BoundaryExplicitTopicChange {
					boundary = true
					reason = string(domconv.BoundaryExplicitTopicChange)
				}
			}
			if !boundary && decision.Proposal != domconv.ThreadBoundaryKeep && mioAttested && mioBoundary == domconv.ThreadBoundaryNew {
				boundary = true
				reason = string(domconv.BoundaryMioTopicChange)
			}
		}
	} else if !errors.Is(err, domconv.ErrThreadNotFound) {
		return base, err
	}

	canonical := request
	canonical.OwnerID = ownerID
	canonical.Domain = domain
	canonical.AgentSpeaker = canonicalSpeaker
	canonical.Boundary = boundary
	canonical.BoundaryReason = reason
	canonical.Targets = append([]domconv.ConversationTurnTarget(nil), provider.ConversationTurnTargets()...)
	canonical.Preparation = preparation
	result, err := committer.CommitConversationTurn(ctx, canonical)
	if err != nil {
		return result, err
	}
	return result, nil
}

// PrepareConversationTurn acquires the manager's session-scoped RAM head lease
// before chat generation. The turn ID is also the lease token carried by EndTurn.
func (e *RealConversationEngine) PrepareConversationTurn(ctx context.Context, turnID, sessionID string) (*domconv.ConversationTurnPreparation, error) {
	if e == nil || e.manager == nil {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	ownerID := strings.TrimSpace(e.userID)
	if _, bindingPresent := domconv.SessionBindingFromContext(ctx); bindingPresent {
		_, boundOwnerID, err := resolveBoundConversationOwner(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		ownerID = boundOwnerID
	}
	if ownerID == "" {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	preparer, ok := e.manager.(conversationTurnPreparer)
	if !ok {
		return nil, domconv.ErrConversationTurnUnavailable
	}
	return preparer.PrepareConversationTurn(ctx, modulecore.TurnID(turnID), sessionID, ownerID)
}

// ReleaseConversationTurnPreparation drops a lease when route selection or
// generation does not reach the matching EndTurn.
func (e *RealConversationEngine) ReleaseConversationTurnPreparation(ctx context.Context, preparation *domconv.ConversationTurnPreparation) error {
	if e == nil || e.manager == nil {
		return domconv.ErrConversationTurnUnavailable
	}
	preparer, ok := e.manager.(conversationTurnPreparer)
	if !ok {
		return domconv.ErrConversationTurnUnavailable
	}
	return preparer.ReleaseConversationTurnPreparation(ctx, preparation)
}

// GetPersona は現在のペルソナ設定を返す
func (e *RealConversationEngine) GetPersona() domconv.PersonaState {
	return e.persona
}

// FlushCurrentThread は現在のスレッドを強制フラッシュする
func (e *RealConversationEngine) FlushCurrentThread(ctx context.Context, sessionID string) error {
	thread, err := e.manager.GetActiveThread(ctx, sessionID)
	if err != nil {
		return err
	}
	if _, err := e.manager.FlushThread(ctx, thread.ID); err != nil {
		return err
	}
	_, err = e.manager.CreateThread(ctx, sessionID, thread.Domain)
	return err
}

// GetStatus は会話セッションの現在状態を返す
func (e *RealConversationEngine) GetStatus(ctx context.Context, sessionID string) (*domconv.ConversationStatus, error) {
	thread, err := e.manager.GetActiveThread(ctx, sessionID)
	if err != nil {
		return &domconv.ConversationStatus{
			SessionID: sessionID,
		}, nil
	}
	return &domconv.ConversationStatus{
		SessionID:    sessionID,
		ThreadID:     thread.ID,
		ThreadDomain: thread.Domain,
		TurnCount:    len(thread.Turns),
		ThreadStart:  thread.StartTime,
		ThreadStatus: thread.Status,
	}, nil
}

// ResetSession はセッションをリセットする
func (e *RealConversationEngine) ResetSession(ctx context.Context, sessionID string) error {
	thread, err := e.manager.GetActiveThread(ctx, sessionID)
	if err == nil && thread != nil {
		if _, err := e.manager.FlushThread(ctx, thread.ID); err != nil {
			log.Printf("[ConversationEngine] WARN: FlushThread during reset failed: %v", err)
		}
	}
	_, err = e.manager.CreateThread(ctx, sessionID, "general")
	return err
}
