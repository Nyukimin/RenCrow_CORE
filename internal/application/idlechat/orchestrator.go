package idlechat

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

const (
	idleCheckInterval              = 30 * time.Second
	maxTurnsPerTopic               = 12
	idleChatResponseMaxTokens      = 512
	idleChatRetryMaxTokens         = 512
	idleChatShiroResponseMaxTokens = 1024
	idleChatShiroRetryMaxTokens    = 1024
	idleChatShiroSummaryMaxTokens  = 1024
	idleChatQualityReviewMaxTokens = 900
	speakerBreak                   = 500 * time.Millisecond  // 話者交代ブレイク（TTS完了後）
	topicBreak                     = 1000 * time.Millisecond // 次IdleChat session/ドメイン交代ブレイク（TTS完了後）
)

var idleChatTTSWaitTimeout = 60 * time.Second

var idleChatTTSSessionDrainTimeout = 60 * time.Second

var idleChatLLMGenerateTimeout = 45 * time.Second

var jst = time.FixedZone("JST", 9*60*60)

var randSeedOnce sync.Once

var errIdleInvalidResponse = errors.New("idlechat invalid response")

var errIdleGenerationFailed = errors.New("idlechat generation failed")

var promptLeakLineRe = regexp.MustCompile(`(?i)(^|[。．\n])[^。．\n]{0,30}(発言として受け|要件[:：]|発言帰属ガード)[^。．\n]*`)

type SessionSummary struct {
	SessionID         string        `json:"session_id"`
	Title             string        `json:"title"`
	Topic             string        `json:"topic"`
	Category          TopicCategory `json:"category,omitempty"`
	Strategy          TopicStrategy `json:"strategy"` // 生成戦略（旧 Category）
	Summary           string        `json:"summary"`
	QualityReview     string        `json:"quality_review,omitempty"`
	PromptGuidance    string        `json:"prompt_guidance,omitempty"`
	SourceTitle       string        `json:"source_title,omitempty"`
	RewriteStyle      string        `json:"rewrite_style,omitempty"`
	StoryTitle        string        `json:"story_title,omitempty"`
	StoryText         string        `json:"story_text,omitempty"`
	StoryDraftText    string        `json:"story_draft_text,omitempty"`
	StoryRevisionNote string        `json:"story_revision_note,omitempty"`
	StoryEndingFlavor string        `json:"story_ending_flavor,omitempty"`
	StartedAt         string        `json:"started_at"`
	EndedAt           string        `json:"ended_at"`
	Turns             int           `json:"turns"`
	LoopRestarted     bool          `json:"loop_restarted"`
	LoopReason        string        `json:"loop_reason,omitempty"`
	TopicProvider     string        `json:"topic_provider"`
	SummaryProvider   string        `json:"summary_provider"`
	Transcript        []string      `json:"transcript,omitempty"`
}

type ActiveTranscriptEntry struct {
	Type      string `json:"type"`
	From      string `json:"from"`
	To        string `json:"to"`
	Content   string `json:"content"`
	SessionID string `json:"session_id"`
	MessageID string `json:"message_id"`
	TurnIndex int    `json:"turn_index"`
	Timestamp string `json:"timestamp"`
}

type TimelineEvent struct {
	Type       string
	From       string
	To         string
	Content    string
	RawContent string
	SessionID  string
	MessageID  string
	TurnIndex  int
	Category   TopicCategory
	Strategy   TopicStrategy
}

type TTSPrefetchEvent struct {
	SessionID string
	MessageID string
	From      string
	To        string
	TurnIndex int
	Token     string
}

type PersonaRuntimeRecorder interface {
	SaveTriggerLog(ctx context.Context, item domainpersona.TriggerLog) error
	SaveCanonicalResponseLog(ctx context.Context, item domainpersona.CanonicalResponseLog) error
	ListCanonicalResponseLogs(ctx context.Context, limit int) ([]domainpersona.CanonicalResponseLog, error)
	SaveObservationLog(ctx context.Context, item domainpersona.ObservationLog) error
	SaveMetaProfileUpdate(ctx context.Context, item domainpersona.MetaProfileUpdate) error
	SaveInterfaceSession(ctx context.Context, item domainpersona.InterfaceSession) error
}

// IdleChatOrchestrator はアイドル時のAgent間雑談を管理

type IdleChatOrchestrator struct {
	llmProvider           llm.LLMProvider
	speakerLLMs           map[string]llm.LLMProvider
	forecastProvider      llm.LLMProvider // 未来展望セッションの思考用（明示選択済み provider）
	forecastProviderLabel string
	sessionContext        string // 現在のセッション固有コンテキスト（既出テーマ等）
	memory                *session.CentralMemory
	participants          []string
	intervalMin           int
	interval              time.Duration
	maxTurns              int
	temperature           float64
	personalities         map[string]string
	speakerOptions        map[string]map[string]any
	topicGenerationConfig TopicGenerationConfig
	dialogueConfig        DialogueInterestingnessConfig
	currentTopicResult    *TopicGenerationResult
	currentDialoguePlan   *DialogueArcPlan
	currentDialogueState  *DialogueArcState
	lastDialogueQuality   DialogueQualityResult

	lastActivity              time.Time
	chatActive                bool
	chatBusy                  bool
	workerBusy                bool
	disabled                  bool
	externalLLMBusy           func() bool
	manualMode                bool
	sessionMode               string
	currentTopic              string
	promptGuides              []string
	autoStep                  int
	forecastStep              int
	nextTopicAt               time.Time
	history                   []SessionSummary
	emitEvent                 func(TimelineEvent) <-chan struct{}
	emitTTSPrefetch           func(TTSPrefetchEvent)
	reportTTSTimeout          func(TTSTimeoutEvent)
	topicStore                *TopicStore
	topicStockBuf             *forecastTopicStock // 未来展望お題ストック
	forecastTopicGenerator    func(ForecastDomain) (string, []string, *forecastTopicFailure)
	recentTopics              func(context.Context, int) ([]string, error)
	personaRuntime            PersonaRuntimeRecorder
	personaTriggers           []domainpersona.TriggerDefinition
	personaCanonicalResponses []domainpersona.CanonicalResponseDefinition

	ctx                 context.Context
	cancel              context.CancelFunc
	runCtx              context.Context
	runCancel           context.CancelFunc
	activeSessionID     string
	activeGeneration    uint64
	interruptedSessions map[string]struct{}
	watchdogStage       string
	watchdogDetail      string
	watchdogFrom        string
	watchdogTo          string
	watchdogMessageID   string
	watchdogTurnIndex   int
	watchdogUpdatedAt   time.Time
	mu                  sync.Mutex
	wg                  sync.WaitGroup
}

type TTSTimeoutEvent struct {
	Kind           string
	SessionID      string
	MessageID      string
	TurnIndex      int
	RemainingIndex int
	RemainingCount int
}

type idleSessionPlan struct {
	mode     string
	strategy TopicStrategy
	domain   *ForecastDomain
}

func (o *IdleChatOrchestrator) SetPersonaRuntimeRecorder(recorder PersonaRuntimeRecorder, triggers []domainpersona.TriggerDefinition) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.personaRuntime = recorder
	o.personaTriggers = append([]domainpersona.TriggerDefinition(nil), triggers...)
}

func (o *IdleChatOrchestrator) SetPersonaCanonicalResponses(definitions []domainpersona.CanonicalResponseDefinition) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.personaCanonicalResponses = append([]domainpersona.CanonicalResponseDefinition(nil), definitions...)
}

func (o *IdleChatOrchestrator) recordPersonaTimelineEvent(ev TimelineEvent) {
	if o == nil || !shouldRecordPersonaTimelineEvent(ev) {
		return
	}
	o.mu.Lock()
	recorder := o.personaRuntime
	triggers := append([]domainpersona.TriggerDefinition(nil), o.personaTriggers...)
	o.mu.Unlock()
	if recorder == nil {
		return
	}
	now := time.Now().UTC()
	sessionKey := idlePersonaSessionKey(ev)
	characterID := idlePersonaCharacterID(ev)
	if err := recorder.SaveInterfaceSession(o.ctx, domainpersona.InterfaceSession{
		SessionID:     "persona_session:" + sessionKey,
		CharacterID:   characterID,
		InterfaceType: "idlechat",
		SessionKey:    sessionKey,
		CreatedAt:     now,
		LastUsedAt:    now,
	}); err != nil {
		log.Printf("[IdleChat] persona interface session record failed: %v", err)
		return
	}
	if err := recorder.SaveObservationLog(o.ctx, domainpersona.ObservationLog{
		EventID:         "evt_persona_idlechat_observation_" + formatPersonaEventTime(now),
		ObserverID:      characterID,
		TargetID:        idlePersonaTargetID(ev),
		ObservationType: idlePersonaObservationType(ev),
		Summary:         "IdleChat runtime observed a timeline event; review is required before memory promotion.",
		EvidenceRefs:    idlePersonaEvidenceRefs(ev),
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}); err != nil {
		log.Printf("[IdleChat] persona observation record failed: %v", err)
		return
	}
	if candidate, ok := buildIdlePersonaMetaProfileUpdateCandidate(ev, characterID, now); ok {
		if err := recorder.SaveMetaProfileUpdate(o.ctx, candidate); err != nil {
			log.Printf("[IdleChat] persona meta profile update candidate record failed: %v", err)
		}
	}
	if match, ok := domainpersona.MatchTrigger(ev.Content, triggers); ok {
		if err := recorder.SaveTriggerLog(o.ctx, domainpersona.TriggerLog{
			EventID:         "evt_persona_idlechat_trigger_" + formatPersonaEventTime(now),
			CharacterID:     match.CharacterID,
			TriggerID:       match.TriggerID,
			TriggerCategory: match.Category,
			Activated:       true,
			Confidence:      match.Confidence,
			CreatedAt:       now,
		}); err != nil {
			log.Printf("[IdleChat] persona trigger record failed: %v", err)
		}
	}
}

func buildIdlePersonaMetaProfileUpdateCandidate(ev TimelineEvent, observerID string, now time.Time) (domainpersona.MetaProfileUpdate, bool) {
	if !shouldRecordPersonaTimelineEvent(ev) || !shouldProposeIdlePersonaMetaUpdateFromText(ev.Content) {
		return domainpersona.MetaProfileUpdate{}, false
	}
	return domainpersona.MetaProfileUpdate{
		UpdateID:        "meta_persona_idlechat_" + formatPersonaEventTime(now),
		ObserverID:      observerID,
		TargetID:        idlePersonaTargetID(ev),
		Section:         "flow_observation",
		ProposedContent: "Runtime candidate from IdleChat timeline event. Human review is required before treating this as stable memory.\n\n" + strings.TrimSpace(ev.Content),
		EvidenceRefs:    idlePersonaEvidenceRefs(ev),
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}, true
}

func shouldProposeIdlePersonaMetaUpdateFromText(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	markers := []string{"私は", "私の", "自分は", "自分の", "覚えて", "覚えといて", "記憶して"}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (o *IdleChatOrchestrator) applyPersonaCanonicalResponse(speaker, sessionID, candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if o == nil || candidate == "" {
		return ""
	}
	o.mu.Lock()
	recorder := o.personaRuntime
	triggers := append([]domainpersona.TriggerDefinition(nil), o.personaTriggers...)
	definitions := append([]domainpersona.CanonicalResponseDefinition(nil), o.personaCanonicalResponses...)
	o.mu.Unlock()
	if recorder == nil || len(triggers) == 0 || len(definitions) == 0 {
		return ""
	}
	match, ok := domainpersona.MatchTrigger(candidate, triggers)
	if !ok || !strings.EqualFold(strings.TrimSpace(match.CharacterID), strings.TrimSpace(speaker)) {
		return ""
	}
	def, ok := selectIdlePersonaCanonicalResponse(match, definitions)
	if !ok || strings.TrimSpace(def.Response) == "" {
		return ""
	}
	recent, err := recorder.ListCanonicalResponseLogs(o.ctx, 50)
	if err != nil {
		log.Printf("[IdleChat] persona canonical response list failed: %v", err)
		return ""
	}
	policy := domainpersona.CanonicalResponsePolicy{
		ResponseID:       def.ResponseID,
		CooldownTurns:    def.CooldownTurns,
		MaxPerSession:    def.MaxPerSession,
		RequiredContexts: def.RequiredContexts,
	}
	contexts := []string{match.Category, match.TriggerID, "idlechat"}
	if !domainpersona.CanUseCanonicalResponse(policy, recent, contexts) {
		return ""
	}
	now := time.Now().UTC()
	if err := recorder.SaveCanonicalResponseLog(o.ctx, domainpersona.CanonicalResponseLog{
		EventID:     "evt_persona_idlechat_canonical_" + formatPersonaEventTime(now),
		CharacterID: def.CharacterID,
		ResponseID:  def.ResponseID,
		MessageID:   strings.TrimSpace(sessionID),
		Used:        true,
		Rewritten:   false,
		CreatedAt:   now,
	}); err != nil {
		log.Printf("[IdleChat] persona canonical response record failed: %v", err)
		return ""
	}
	return def.Response
}

func selectIdlePersonaCanonicalResponse(match domainpersona.TriggerMatch, definitions []domainpersona.CanonicalResponseDefinition) (domainpersona.CanonicalResponseDefinition, bool) {
	var selected domainpersona.CanonicalResponseDefinition
	found := false
	for _, def := range definitions {
		if strings.TrimSpace(def.ResponseID) == "" || strings.TrimSpace(def.CharacterID) == "" {
			continue
		}
		if !strings.EqualFold(def.CharacterID, match.CharacterID) {
			continue
		}
		if strings.TrimSpace(def.Category) != "" && def.Category != match.Category {
			continue
		}
		if !found || def.Priority > selected.Priority {
			selected = def
			found = true
		}
	}
	return selected, found
}

func shouldRecordPersonaTimelineEvent(ev TimelineEvent) bool {
	switch strings.TrimSpace(ev.Type) {
	case "idlechat.message", "idlechat.viewer", "idlechat.summary", "idlechat.topic":
		return strings.TrimSpace(ev.Content) != ""
	default:
		return false
	}
}

func idlePersonaSessionKey(ev TimelineEvent) string {
	sessionID := strings.TrimSpace(ev.SessionID)
	if sessionID == "" {
		sessionID = "unknown"
	}
	return "idlechat:" + sessionID
}

func idlePersonaCharacterID(ev TimelineEvent) string {
	from := strings.ToLower(strings.TrimSpace(ev.From))
	switch from {
	case "mio", "shiro", "kuro", "midori", "wild":
		return from
	default:
		return "mio"
	}
}

func idlePersonaTargetID(ev TimelineEvent) string {
	to := strings.ToLower(strings.TrimSpace(ev.To))
	switch to {
	case "mio", "shiro", "kuro", "midori", "wild":
		return to
	case "user":
		return "ren"
	default:
		return "idlechat"
	}
}

func idlePersonaObservationType(ev TimelineEvent) string {
	switch strings.TrimSpace(ev.Type) {
	case "idlechat.summary":
		return "idlechat_summary"
	case "idlechat.topic":
		return "idlechat_topic"
	case "idlechat.viewer":
		return "idlechat_viewer_message"
	default:
		return "idlechat_message"
	}
}

func idlePersonaEvidenceRefs(ev TimelineEvent) []string {
	refs := []string{"session:" + strings.TrimSpace(ev.SessionID), "channel:idlechat"}
	if strings.TrimSpace(ev.Type) != "" {
		refs = append(refs, "event_type:"+strings.TrimSpace(ev.Type))
	}
	if strings.TrimSpace(ev.From) != "" {
		refs = append(refs, "from:"+strings.TrimSpace(ev.From))
	}
	if strings.TrimSpace(ev.To) != "" {
		refs = append(refs, "to:"+strings.TrimSpace(ev.To))
	}
	return refs
}

func formatPersonaEventTime(t time.Time) string {
	return strings.ReplaceAll(t.Format("20060102150405.000000000"), ".", "")
}

// SetEventEmitter sets an optional timeline event emitter used by viewer SSE.
// The callback returns a channel that closes when TTS playback completes (nil = no TTS).
