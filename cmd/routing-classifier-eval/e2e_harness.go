package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	conversationinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	infrarouting "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var errE2EExternalEffectDenied = errors.New("routing classifier E2E external effect denied")

// e2eTrialObservation is test-harness output only. It explicitly rejects JSON
// marshaling so fake-provider measurements cannot be mistaken for experiment
// evidence or model-adoption results.
type e2eTrialObservation struct {
	CaseID             string
	Variant            string
	Origin             string
	LabelStatus        string
	ExpectedRoutes     []string
	NeedsContext       bool
	FinalRoute         string
	DecisionSource     string
	ChatCalled         bool
	Committed          bool
	FinalPublished     bool
	TTFTMS             *float64
	FinalMS            *float64
	ExternalEffectCall int
	Spies              e2eSpyCounts
	WorkerConstructed  bool
	CoderConstructed   bool
	HarnessKind        string
	AdoptionEligible   bool
	trialDir           string
}

func (e2eTrialObservation) MarshalJSON() ([]byte, error) {
	return nil, errors.New("test-only E2E observations cannot be serialized as experiment evidence")
}

type e2eSpyCounts struct {
	ToolCalls        int
	MCPCalls         int
	KBCalls          int
	SearchCacheCalls int
	UserMemoryWrites int
	PersonaWrites    int
	LocalMemoryReads int
}

func (c e2eSpyCounts) externalCalls() int {
	return c.ToolCalls + c.MCPCalls + c.KBCalls + c.SearchCacheCalls + c.UserMemoryWrites + c.PersonaWrites
}

// runIsolatedMioTrial composes the real Mio routing and CHAT entry points over
// a private, per-trial L1 database. All integrations capable of external work
// are denial spies; only the fake LLM provider and the trial-local SQLite store
// can produce effects. The function is deliberately not called by the CLI.
func runIsolatedMioTrial(ctx context.Context, item classifierlab.ReplayCase, variant string, classifier domainagent.Classifier, provider domainllm.LLMProvider) (e2eTrialObservation, error) {
	if ctx == nil || strings.TrimSpace(item.CaseID) == "" || strings.TrimSpace(variant) == "" {
		return e2eTrialObservation{}, errors.New("isolated Mio trial requires context, case ID, and variant")
	}
	turn, err := replayTurnInput(item)
	if err != nil {
		return e2eTrialObservation{}, fmt.Errorf("construct isolated trial input: %w", err)
	}
	if turn.SessionID() == "" {
		turn = turn.WithSessionID(string(modulecore.NewSessionID()))
	} else if err := modulecore.SessionID(turn.SessionID()).Validate(); err != nil {
		return e2eTrialObservation{}, errors.New("turn_fixture.session_id is not a canonical CORE session ID")
	}

	trialDir, err := os.MkdirTemp("", "rencrow-routing-e2e-")
	if err != nil {
		return e2eTrialObservation{}, fmt.Errorf("create private E2E trial directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(trialDir) }()
	if err := os.Chmod(trialDir, 0o700); err != nil {
		return e2eTrialObservation{}, fmt.Errorf("secure private E2E trial directory: %w", err)
	}
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(trialDir, "l1.sqlite"))
	if err != nil {
		return e2eTrialObservation{}, fmt.Errorf("open private E2E L1 store: %w", err)
	}
	storeClosed := false
	closeStore := func() error {
		if storeClosed {
			return nil
		}
		storeClosed = true
		return store.Close()
	}
	defer func() { _ = closeStore() }()

	spies := &e2eDenialSpies{}
	manager := conversationinfra.NewL1ConversationManager(store)
	engine := conversationinfra.NewRealConversationEngine(manager, domainconversation.NewMioPersona("Classifier routing E2E test persona"))
	engine.WithUserMemoryStore(spies, "routing-e2e-user")
	mio := domainagent.NewMioAgent(provider, classifier, infrarouting.NewRuleDictionary(), e2eToolSpy{spies}, e2eMCPSpy{spies}, engine).
		WithKBManager(e2eKBSpy{spies}).
		WithSearchCacheManager(e2eSearchCacheSpy{spies}).
		WithUserMemoryManager(spies).
		WithPersonaEditor(spies)

	started := time.Now()
	decision, err := mio.DecideAction(ctx, turn)
	if err != nil {
		return e2eTrialObservation{}, errors.New("Mio.DecideAction failed in isolated trial")
	}
	if decision.Route == "" {
		return e2eTrialObservation{}, errors.New("Mio.DecideAction returned an empty route")
	}
	observation := e2eTrialObservation{
		CaseID: item.CaseID, Variant: variant, Origin: item.Origin, LabelStatus: item.LabelStatus,
		ExpectedRoutes: append([]string(nil), item.ExpectedRoutes...), NeedsContext: item.NeedsContext,
		FinalRoute: decision.Route.String(), DecisionSource: e2eDecisionSource(decision),
		Spies: spies.snapshot(), WorkerConstructed: false, CoderConstructed: false,
		HarnessKind: "test_only_fake_provider", AdoptionEligible: false, trialDir: trialDir,
	}
	if decision.Route != domainrouting.RouteCHAT {
		observation.ExternalEffectCall = observation.Spies.externalCalls()
		if err := closeStore(); err != nil {
			return e2eTrialObservation{}, errors.New("close isolated L1 store after route-only trial")
		}
		return observation, nil
	}
	if provider == nil {
		return e2eTrialObservation{}, errors.New("CHAT route requires an injected test provider")
	}

	observation.ChatCalled = true
	sink := &e2eTestSink{}
	chatCtx := domainllm.ContextWithStreamCallback(ctx, sink.OnToken)
	response, err := mio.Chat(chatCtx, turn.WithRoute(decision.Route))
	if err != nil {
		return e2eTrialObservation{}, errors.New("Mio.Chat failed before test sink publication")
	}
	// Mio.Chat returns only after the typed EndTurn commit. Require the durable
	// receipt before publishing the final answer into this in-memory test sink.
	receipt, err := store.GetConversationTurnReceipt(ctx, string(turn.TurnID()))
	if err != nil || receipt.Status != domainconversation.ConversationTurnCompleted {
		return e2eTrialObservation{}, errors.New("isolated L1 commit receipt is missing or incomplete")
	}
	observation.Committed = true
	if err := sink.PublishFinal(response, observation.Committed); err != nil {
		return e2eTrialObservation{}, err
	}
	observation.FinalPublished = true
	if first, ok := sink.firstDisplayableAt(); ok {
		ms := float64(first.Sub(started)) / float64(time.Millisecond)
		observation.TTFTMS = &ms
	}
	if published, ok := sink.finalPublishedAt(); ok {
		ms := float64(published.Sub(started)) / float64(time.Millisecond)
		observation.FinalMS = &ms
	}
	observation.Spies = spies.snapshot()
	observation.ExternalEffectCall = observation.Spies.externalCalls()
	if err := closeStore(); err != nil {
		return e2eTrialObservation{}, errors.New("close isolated L1 store after CHAT trial")
	}
	return observation, nil
}

func e2eDecisionSource(decision domainrouting.Decision) string {
	for i := len(decision.Evidence) - 1; i >= 0; i-- {
		if decision.Evidence[i].Matched && decision.Evidence[i].Source != "" {
			return decision.Evidence[i].Source
		}
	}
	return "unknown"
}

type e2eTestSink struct {
	mu        sync.Mutex
	firstAt   time.Time
	finalAt   time.Time
	committed bool
	finalText string
}

func (s *e2eTestSink) OnToken(token string) {
	if strings.TrimSpace(token) == "" {
		return
	}
	s.mu.Lock()
	if s.firstAt.IsZero() {
		s.firstAt = time.Now()
	}
	s.mu.Unlock()
}

func (s *e2eTestSink) PublishFinal(text string, committed bool) error {
	if !committed {
		return errors.New("test sink refuses publication before L1 commit")
	}
	s.mu.Lock()
	s.committed = true
	s.finalText = text
	s.finalAt = time.Now()
	s.mu.Unlock()
	return nil
}

func (s *e2eTestSink) firstDisplayableAt() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.firstAt, !s.firstAt.IsZero()
}

func (s *e2eTestSink) finalPublishedAt() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalAt, s.committed && !s.finalAt.IsZero()
}

// e2ePairedTTFT only compares the same verified, single-label CHAT case when
// both variants actually finished on CHAT. A wrong route can never be counted
// as a latency improvement.
type e2ePairedTTFT struct {
	EligibleCases      int
	PairedCases        int
	WrongRouteExcluded int
	LegacyMeanMS       *float64
	CandidateMeanMS    *float64
}

func compareIsolatedE2ETTFT(observations []e2eTrialObservation) e2ePairedTTFT {
	byCaseVariant := make(map[string]map[string]e2eTrialObservation)
	caseLabels := make(map[string]classifierlab.ReplayCase)
	for _, observation := range observations {
		if byCaseVariant[observation.CaseID] == nil {
			byCaseVariant[observation.CaseID] = make(map[string]e2eTrialObservation)
		}
		byCaseVariant[observation.CaseID][observation.Variant] = observation
		caseLabels[observation.CaseID] = classifierlab.ReplayCase{
			CaseID: observation.CaseID, Origin: observation.Origin, LabelStatus: observation.LabelStatus,
			ExpectedRoutes: observation.ExpectedRoutes, NeedsContext: observation.NeedsContext,
		}
	}
	result := e2ePairedTTFT{}
	var legacyTotal, candidateTotal float64
	caseIDs := make([]string, 0, len(caseLabels))
	for caseID := range caseLabels {
		caseIDs = append(caseIDs, caseID)
	}
	sort.Strings(caseIDs)
	for _, caseID := range caseIDs {
		item := caseLabels[caseID]
		if item.Origin != "real_conversation" || item.LabelStatus != "verified" || item.NeedsContext || len(item.ExpectedRoutes) != 1 || item.ExpectedRoutes[0] != "CHAT" {
			continue
		}
		variants := byCaseVariant[caseID]
		legacy, legacyOK := variants["legacy"]
		candidate, candidateOK := variants["candidate"]
		if !legacyOK || !candidateOK {
			continue
		}
		result.EligibleCases++
		if legacy.FinalRoute != "CHAT" || candidate.FinalRoute != "CHAT" {
			result.WrongRouteExcluded++
			continue
		}
		if !legacy.ChatCalled || !candidate.ChatCalled || !legacy.Committed || !candidate.Committed ||
			!legacy.FinalPublished || !candidate.FinalPublished || legacy.TTFTMS == nil || candidate.TTFTMS == nil {
			continue
		}
		legacyTotal += *legacy.TTFTMS
		candidateTotal += *candidate.TTFTMS
		result.PairedCases++
	}
	if result.PairedCases > 0 {
		legacyMean := legacyTotal / float64(result.PairedCases)
		candidateMean := candidateTotal / float64(result.PairedCases)
		result.LegacyMeanMS = &legacyMean
		result.CandidateMeanMS = &candidateMean
	}
	return result
}

type e2eDenialSpies struct {
	mu     sync.Mutex
	counts e2eSpyCounts
}

func (s *e2eDenialSpies) increment(update func(*e2eSpyCounts)) {
	s.mu.Lock()
	update(&s.counts)
	s.mu.Unlock()
}

func (s *e2eDenialSpies) snapshot() e2eSpyCounts {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts
}

type e2eToolSpy struct{ counts *e2eDenialSpies }
type e2eMCPSpy struct{ counts *e2eDenialSpies }
type e2eKBSpy struct{ counts *e2eDenialSpies }
type e2eSearchCacheSpy struct{ counts *e2eDenialSpies }

func (s e2eToolSpy) ExecuteV2(context.Context, string, map[string]any) (*domaintool.ToolResponse, error) {
	s.counts.increment(func(c *e2eSpyCounts) { c.ToolCalls++ })
	return nil, errE2EExternalEffectDenied
}
func (s e2eToolSpy) ListTools(context.Context) ([]domaintool.ToolMetadata, error) {
	s.counts.increment(func(c *e2eSpyCounts) { c.ToolCalls++ })
	return nil, errE2EExternalEffectDenied
}
func (s e2eMCPSpy) CallTool(context.Context, string, string, map[string]interface{}) (string, error) {
	s.counts.increment(func(c *e2eSpyCounts) { c.MCPCalls++ })
	return "", errE2EExternalEffectDenied
}
func (s e2eMCPSpy) ListTools(context.Context, string) ([]string, error) {
	s.counts.increment(func(c *e2eSpyCounts) { c.MCPCalls++ })
	return nil, errE2EExternalEffectDenied
}
func (s e2eKBSpy) SearchKB(context.Context, string, string, int) ([]*domainconversation.Document, error) {
	s.counts.increment(func(c *e2eSpyCounts) { c.KBCalls++ })
	return nil, errE2EExternalEffectDenied
}
func (s e2eKBSpy) SaveWebSearchToKB(context.Context, string, string, []domainagent.WebSearchResult) error {
	s.counts.increment(func(c *e2eSpyCounts) { c.KBCalls++ })
	return errE2EExternalEffectDenied
}
func (s e2eSearchCacheSpy) GetFreshWebSearchCache(context.Context, string) ([]domainagent.WebSearchResult, bool, error) {
	s.counts.increment(func(c *e2eSpyCounts) { c.SearchCacheCalls++ })
	return nil, false, errE2EExternalEffectDenied
}
func (s e2eSearchCacheSpy) SaveWebSearchCache(context.Context, string, []domainagent.WebSearchResult, time.Duration) error {
	s.counts.increment(func(c *e2eSpyCounts) { c.SearchCacheCalls++ })
	return errE2EExternalEffectDenied
}
func (s *e2eDenialSpies) CreateUserMemory(context.Context, domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error) {
	s.increment(func(c *e2eSpyCounts) { c.UserMemoryWrites++ })
	return nil, errE2EExternalEffectDenied
}
func (s *e2eDenialSpies) ListUserMemories(context.Context, string, string, bool, int) ([]domainmemory.UserMemory, error) {
	s.increment(func(c *e2eSpyCounts) { c.LocalMemoryReads++ })
	return nil, nil
}
func (s *e2eDenialSpies) UpdateUserMemoryState(context.Context, string, string, string) (*domainmemory.UserMemory, error) {
	s.increment(func(c *e2eSpyCounts) { c.UserMemoryWrites++ })
	return nil, errE2EExternalEffectDenied
}
func (s *e2eDenialSpies) ForgetUserMemory(context.Context, string, string) (*domainmemory.UserMemory, error) {
	s.increment(func(c *e2eSpyCounts) { c.UserMemoryWrites++ })
	return nil, errE2EExternalEffectDenied
}
func (s *e2eDenialSpies) SupersedeUserMemory(context.Context, string, string, string) (*domainmemory.UserMemory, error) {
	s.increment(func(c *e2eSpyCounts) { c.UserMemoryWrites++ })
	return nil, errE2EExternalEffectDenied
}
func (s *e2eDenialSpies) ReadPersona() (string, error) { return "test persona", nil }
func (s *e2eDenialSpies) WritePersona(string) error {
	s.increment(func(c *e2eSpyCounts) { c.PersonaWrites++ })
	return errE2EExternalEffectDenied
}

type e2eTestDecisionClassifier struct {
	result domainrouting.Decision
	called int
}

func (c *e2eTestDecisionClassifier) Classify(context.Context, domainconversation.TurnInput) (domainrouting.Decision, error) {
	c.called++
	return c.result, nil
}

// e2eCandidateDecisionAdapter is deliberately test-only. Confidence 1.0 is
// used solely to cross Mio's existing 0.7 dispatch gate; it is not model
// confidence and must never enter experiment metrics.
type e2eCandidateDecisionAdapter struct {
	candidate classifierlab.CandidateClassifier
	policySHA string
	requestID string
}

func (a e2eCandidateDecisionAdapter) Classify(ctx context.Context, turn domainconversation.TurnInput) (domainrouting.Decision, error) {
	if a.candidate == nil || a.policySHA == "" || a.requestID == "" {
		return domainrouting.Decision{}, errors.New("test candidate adapter is incomplete")
	}
	result, err := a.candidate.ClassifyCandidate(ctx, classifierlab.CandidateInput{
		SchemaVersion: classifierlab.SchemaVersion, RequestID: a.requestID,
		PolicySHA256: a.policySHA, Message: turn.MessageText(),
	})
	if err != nil {
		return domainrouting.Decision{}, err
	}
	if result.SchemaVersion != classifierlab.SchemaVersion || result.RequestID != a.requestID || result.PolicySHA256 != a.policySHA {
		return domainrouting.Decision{}, errors.New("test candidate adapter received mismatched response provenance")
	}
	if result.Status != classifierlab.CandidateClassified || result.Route == nil || !e2eValidRoute(*result.Route) {
		return domainrouting.Decision{}, errors.New("test candidate adapter received no valid route")
	}
	return domainrouting.NewDecisionWithEvidence(
		domainrouting.Route(*result.Route), 1.0, "test_adapter_gate_only",
		domainrouting.DecisionEvidence{Source: domainrouting.EvidenceSourceClassifier, Matched: true,
			Route: domainrouting.Route(*result.Route), Confidence: 1.0, Reason: "test_adapter_gate_only"},
	), nil
}

func e2eValidRoute(label classifierlab.RouteLabel) bool {
	for _, name := range classifierlab.ReplayRouteNames() {
		if string(label) == name {
			return true
		}
	}
	return false
}

var _ domainagent.ToolRunner = e2eToolSpy{}
var _ domainagent.MCPClient = e2eMCPSpy{}
var _ domainagent.KBManager = e2eKBSpy{}
var _ domainagent.SearchCacheManager = e2eSearchCacheSpy{}
var _ domainagent.UserMemoryManager = (*e2eDenialSpies)(nil)
var _ domainagent.PersonaEditor = (*e2eDenialSpies)(nil)
