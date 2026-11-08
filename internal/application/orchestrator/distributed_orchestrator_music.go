package orchestrator

import (
	"context"
	"regexp"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type musicWorkflowIntent uint8

const (
	musicWorkflowIntentNone musicWorkflowIntent = iota
	musicWorkflowIntentGeneration
	musicWorkflowIntentRemix
	musicWorkflowIntentControl
	musicWorkflowIntentAdoption
)

type musicWorkflowRouting struct {
	actorID             string
	generationReady     bool
	remixReady          bool
	controlReady        bool
	workerToolLoopReady bool
}

func newMusicWorkflowRouting(actorID string, generationReady, remixReady, controlReady, workerToolLoopReady bool) musicWorkflowRouting {
	return musicWorkflowRouting{
		actorID: strings.TrimSpace(actorID), generationReady: generationReady,
		remixReady: remixReady, controlReady: controlReady, workerToolLoopReady: workerToolLoopReady,
	}
}

func (r musicWorkflowRouting) candidate(ctx context.Context, req ProcessMessageRequest) bool {
	if r.actorID == "" || !r.workerToolLoopReady {
		return false
	}
	if _, direct := directViewerRecipientForChat(req); direct {
		return false
	}
	message := req.originalUserMessage
	if message == "" {
		message = req.UserMessage
	}
	if strings.HasPrefix(strings.TrimSpace(message), "/") {
		return false
	}
	proof, verified := domaintool.AuthenticatedHTTPUserScopeFromContext(ctx)
	if !verified || proof.ActorKind != domaintool.ActorKindUser || proof.ActorID != r.actorID ||
		proof.AuthenticatedUserID != r.actorID || !proof.Allows(domaintool.DataScopeUser) {
		return false
	}
	current, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || current.Validate() != nil || current.AuthenticatedUserID != r.actorID || !current.Allows(domaintool.DataScopeUser) {
		return false
	}
	switch current.ActorKind {
	case domaintool.ActorKindUser:
		if current.AuthenticationSource != domaintool.AuthenticationSourceHTTP || current.ActorID != r.actorID {
			return false
		}
	case domaintool.ActorKindAgent:
		if current.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator {
			return false
		}
	default:
		return false
	}
	intent := musicWorkflowIntentFromText(message)
	switch intent {
	case musicWorkflowIntentGeneration:
		return r.generationReady
	case musicWorkflowIntentRemix:
		return r.remixReady
	case musicWorkflowIntentControl, musicWorkflowIntentAdoption:
		return r.controlReady
	default:
		return false
	}
}

func (r musicWorkflowRouting) apply(ctx context.Context, req ProcessMessageRequest, decision routing.Decision) routing.Decision {
	if decision.Route == routing.RouteOPS || hasExplicitRouteEvidence(decision) || !r.candidate(ctx, req) {
		return decision
	}
	decision.Route = routing.RouteOPS
	decision.Confidence = 1
	if decision.Reason == "" {
		decision.Reason = "authenticated Music workflow request routed to the ready Worker"
	} else {
		decision.Reason += "; authenticated Music workflow request routed to the ready Worker"
	}
	decision.Evidence = append(decision.Evidence, routing.DecisionEvidence{
		Source: "music_workflow_intent", Matched: true, Route: routing.RouteOPS, Confidence: 1,
		Reason: "authenticated Music generation or remix request",
	})
	return decision
}

// SetMusicWorkflowRouting exposes natural-language routing only when the
// authenticated Music capability and Shiro's actual Worker tool loop exist.
func (o *DistributedOrchestrator) SetMusicWorkflowRouting(actorID string, generationReady, remixReady, controlReady, workerToolLoopReady bool) {
	if o == nil {
		return
	}
	o.musicRouting = newMusicWorkflowRouting(actorID, generationReady, remixReady, controlReady, workerToolLoopReady)
}

func (o *MessageOrchestrator) SetMusicWorkflowRouting(actorID string, generationReady, remixReady, controlReady, workerToolLoopReady bool) {
	if o == nil || o.routeDecisions == nil {
		return
	}
	o.routeDecisions.SetMusicWorkflowRouting(actorID, generationReady, remixReady, controlReady, workerToolLoopReady)
}

func (o *DistributedOrchestrator) applyMusicWorkflowIntentRoute(ctx context.Context, req ProcessMessageRequest, decision routing.Decision) routing.Decision {
	if o == nil {
		return decision
	}
	return o.musicRouting.apply(ctx, req, decision)
}

func hasExplicitRouteEvidence(decision routing.Decision) bool {
	for _, evidence := range decision.Evidence {
		if evidence.Source == routing.EvidenceSourceExplicitCommand && evidence.Matched {
			return true
		}
	}
	return false
}

func musicWorkflowIntentFromText(message string) musicWorkflowIntent {
	text := strings.ToLower(strings.TrimSpace(message))
	if intent := musicControlIntentFromText(message); intent != musicWorkflowIntentNone {
		return intent
	}
	if containsMusicIntentPhrase(text, "can you", "could you", "is it possible") {
		return musicWorkflowIntentNone
	}
	if text == "" || containsMusicIntentPhrase(text,
		"しないで", "作らない", "作成しない", "生成しない", "作らなくていい", "生成不要", "作成不要", "やめて", "禁止",
		"don't", "do not", "not generate", "without creating") {
		return musicWorkflowIntentNone
	}
	remixAction := containsMusicIntentPhrase(text,
		"リミックスして", "リミックスしてください", "リミックスしてほしい", "remix it", "remix this", "remix the")
	editAction := containsMusicIntentPhrase(text, "フェードをつけて", "フェードして", "音量を調整して", "音量を上げて", "音量を下げて", "切り取って", "トリミングして", "つないで", "編集して", "fade", "trim", "edit", "apply")
	if remixAction || editAction && containsMusicIntentPhrase(text, "曲", "楽曲", "音源", "artifact", "audio", "song", "track") {
		return musicWorkflowIntentRemix
	}
	if containsMusicIntentPhrase(text, "制作計画", "制作プラン", "production plan", "計画を立て") &&
		!containsMusicIntentPhrase(text, "実際に曲を生成", "曲を生成して", "楽曲を生成して", "音源を生成して", "曲を作って", "楽曲を作って", "音源を作って", "音源化して", "曲にして", "generate a song", "create a song", "compose a song") {
		return musicWorkflowIntentNone
	}
	generationAction := containsMusicIntentPhrase(text,
		"生成して", "生成してください", "生成したい", "生成をお願いします", "作って", "作ってください", "作ってほしい", "作成して", "作成してください", "作曲して", "作りたい", "音源化して", "曲にして", "compose", "generate", "create")
	if generationAction && containsMusicIntentPhrase(text, "曲", "楽曲", "音源", "歌", "bgm", "インスト", "song", "track", "music") {
		return musicWorkflowIntentGeneration
	}
	if containsMusicIntentPhrase(text, "教えて", "説明して", "解説して", "相談したい", "作り方", "作る方法", "生成方法", "やり方", "どうやって", "アイデア", "案を", "できますか", "できる?", "できる？", "可能ですか", "can you", "how to", "tutorial", "explain") {
		return musicWorkflowIntentNone
	}
	return musicWorkflowIntentNone
}

var musicProjectReferencePattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-])(prj_[A-Za-z0-9_-]{1,252})(?:$|[^A-Za-z0-9_-])`)
var musicArtifactReferencePattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_-])(art_[A-Za-z0-9_-]{1,252})(?:$|[^A-Za-z0-9_-])`)
var musicModeReferencePattern = regexp.MustCompile(`(?i)(?:^|[^A-Za-z0-9_])(DIRECT|AUTO)(?:$|[^A-Za-z0-9_])`)

func musicControlIntentFromText(message string) musicWorkflowIntent {
	text := strings.ToLower(strings.TrimSpace(message))
	if text == "" || !containsMusicIntentPhrase(text, "music", "音楽") || containsMusicIntentPhrase(text, "しないで", "しません", "やめて", "禁止", "don't", "do not", "not adopt", "not set") {
		return musicWorkflowIntentNone
	}
	projectIDs := musicProjectReferencePattern.FindAllStringSubmatch(message, -1)
	if len(projectIDs) != 1 {
		return musicWorkflowIntentNone
	}
	if containsMusicIntentPhrase(text, "採用して", "採用してください", "を採用", "adopt ", "select version") {
		artifactIDs := musicArtifactReferencePattern.FindAllStringSubmatch(message, -1)
		if len(artifactIDs) == 1 {
			return musicWorkflowIntentAdoption
		}
		return musicWorkflowIntentNone
	}
	modeAction := containsMusicIntentPhrase(text, "にしてください", "にして", "modeを", "モードを", "set mode", "change mode")
	if !modeAction {
		return musicWorkflowIntentNone
	}
	modes := musicModeReferencePattern.FindAllStringSubmatch(message, -1)
	if len(modes) != 1 {
		return musicWorkflowIntentNone
	}
	return musicWorkflowIntentControl
}

func containsMusicIntentPhrase(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}
