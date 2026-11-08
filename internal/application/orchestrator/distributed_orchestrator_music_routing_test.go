package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMusicWorkflowIntentRoutesToWorkerOnlyWithReadyCapabilityAndDelegatedOwner(t *testing.T) {
	trusted := trustedMusicRoutingContext(t, "ren")
	trustedUser := trustedMusicHTTPUserContext(t, "ren")
	for _, tc := range []struct {
		name       string
		message    string
		ctx        context.Context
		actorID    string
		generation bool
		remix      bool
		control    bool
		toolLoop   bool
		decision   routing.Decision
		wantRoute  routing.Route
	}{
		{name: "affirmative generation", message: "このプランで歌詞付きの曲を生成して", ctx: trusted, actorID: "ren", generation: true, toolLoop: true, decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary"), wantRoute: routing.RouteOPS},
		{name: "authenticated HTTP user ingress", message: "曲を生成して", ctx: trustedUser, actorID: "ren", generation: true, toolLoop: true, decision: routing.NewDecision(routing.RouteCODE2, 0.9, "rule dictionary"), wantRoute: routing.RouteOPS},
		{name: "affirmative remix", message: "この曲をリミックスして", ctx: trusted, actorID: "ren", remix: true, toolLoop: true, decision: routing.NewDecision(routing.RouteWILD, 0.9, "rule dictionary"), wantRoute: routing.RouteOPS},
		{name: "music intent beats non-explicit fallback route", message: "この音源をリミックスしてください", ctx: trusted, actorID: "ren", remix: true, toolLoop: true, decision: routing.NewDecision(routing.RouteCODE2, 0.9, "rule dictionary"), wantRoute: routing.RouteOPS},
		{name: "advice only", message: "曲の作り方を教えて", ctx: trusted, actorID: "ren", generation: true, toolLoop: true, decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary"), wantRoute: routing.RoutePLAN},
		{name: "negated generation", message: "曲を生成しないでください", ctx: trusted, actorID: "ren", generation: true, toolLoop: true, decision: routing.NewDecision(routing.RouteWILD, 0.9, "rule dictionary"), wantRoute: routing.RouteWILD},
		{name: "user asks capability", message: "曲を作れますか", ctx: trusted, actorID: "ren", generation: true, toolLoop: true, decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary"), wantRoute: routing.RoutePLAN},
		{name: "no authenticated proof", message: "曲を生成して", ctx: context.Background(), actorID: "ren", generation: true, toolLoop: true, decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary"), wantRoute: routing.RoutePLAN},
		{name: "different authenticated user", message: "曲を生成して", ctx: trusted, actorID: "other", generation: true, toolLoop: true, decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary"), wantRoute: routing.RoutePLAN},
		{name: "generation capability unavailable", message: "曲を生成して", ctx: trusted, actorID: "ren", toolLoop: true, decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary"), wantRoute: routing.RoutePLAN},
		{name: "remix capability unavailable", message: "この曲をリミックスして", ctx: trusted, actorID: "ren", toolLoop: true, decision: routing.NewDecision(routing.RouteWILD, 0.9, "rule dictionary"), wantRoute: routing.RouteWILD},
		{name: "worker toolloop unavailable", message: "曲を生成して", ctx: trusted, actorID: "ren", generation: true, decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary"), wantRoute: routing.RoutePLAN},
		{name: "explicit slash command wins", message: "/PLAN 曲を生成して", ctx: trusted, actorID: "ren", generation: true, toolLoop: true, decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary"), wantRoute: routing.RoutePLAN},
		{name: "explicit Music mode with exact project ID", message: "Musicのproject prj_track_01をDIRECTにしてください", ctx: trusted, actorID: "ren", control: true, toolLoop: true, decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary"), wantRoute: routing.RouteOPS},
		{name: "adopt explicit artifact in exact project", message: "Music project prj_track_01の音源art_master_02を採用してください", ctx: trusted, actorID: "ren", control: true, toolLoop: true, decision: routing.NewDecision(routing.RouteCHAT, 0.9, "rule dictionary"), wantRoute: routing.RouteOPS},
		{name: "deictic project without ID fails closed", message: "このMusicプロジェクトの音源art_master_02を採用してください", ctx: trusted, actorID: "ren", control: true, toolLoop: true, decision: routing.NewDecision(routing.RouteCHAT, 0.9, "rule dictionary"), wantRoute: routing.RouteCHAT},
		{name: "artifact without project ID fails closed", message: "Musicの音源art_master_02を採用してください", ctx: trusted, actorID: "ren", control: true, toolLoop: true, decision: routing.NewDecision(routing.RouteCHAT, 0.9, "rule dictionary"), wantRoute: routing.RouteCHAT},
		{name: "ambiguous project IDs fail closed", message: "Music project prj_track_01とprj_track_02をDIRECTにしてください", ctx: trusted, actorID: "ren", control: true, toolLoop: true, decision: routing.NewDecision(routing.RouteCHAT, 0.9, "rule dictionary"), wantRoute: routing.RouteCHAT},
		{name: "control capability unavailable", message: "Music project prj_track_01をDIRECTにしてください", ctx: trusted, actorID: "ren", toolLoop: true, decision: routing.NewDecision(routing.RouteCHAT, 0.9, "rule dictionary"), wantRoute: routing.RouteCHAT},
		{name: "explicit route evidence wins", message: "曲を生成して", ctx: trusted, actorID: "ren", generation: true, toolLoop: true, decision: routing.NewDecisionWithEvidence(routing.RoutePLAN, 1, "explicit PLAN", routing.DecisionEvidence{Source: routing.EvidenceSourceExplicitCommand, Matched: true, Route: routing.RoutePLAN}), wantRoute: routing.RoutePLAN},
		{name: "existing OPS decision remains", message: "曲を生成して", ctx: trusted, actorID: "ren", generation: true, toolLoop: true, decision: routing.NewDecision(routing.RouteOPS, 0.9, "explicit OPS"), wantRoute: routing.RouteOPS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orch := &DistributedOrchestrator{}
			orch.SetMusicWorkflowRouting(tc.actorID, tc.generation, tc.remix, tc.control, tc.toolLoop)
			request := ProcessMessageRequest{Channel: "viewer", To: "mio", UserMessage: tc.message}
			got := orch.applyMusicWorkflowIntentRoute(tc.ctx, request, tc.decision)
			if got.Route != tc.wantRoute {
				t.Fatalf("music intent route = %s, want %s (decision=%+v)", got.Route, tc.wantRoute, got)
			}
			if tc.wantRoute == routing.RouteOPS && tc.decision.Route != routing.RouteOPS && got.Reason == tc.decision.Reason {
				t.Fatal("music Worker override did not record its route reason")
			}
		})
	}
}

func TestMusicControlIntentMatcherRequiresExplicitResolvableIDs(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    musicWorkflowIntent
	}{
		{message: "Music project prj_track_01をDIRECTにしてください", want: musicWorkflowIntentControl},
		{message: "Music project prj_track_01の音源art_master_02を採用してください", want: musicWorkflowIntentAdoption},
		{message: "このMusicプロジェクトの音源art_master_02を採用してください", want: musicWorkflowIntentNone},
		{message: "Music project prj_track_01とprj_track_02をDIRECTにしてください", want: musicWorkflowIntentNone},
		{message: "Music project prj_track_01をAUTOとDIRECTにしてください", want: musicWorkflowIntentNone},
		{message: "Music project prj_track_01をDIRECTにしないで", want: musicWorkflowIntentNone},
	} {
		if got := musicWorkflowIntentFromText(tc.message); got != tc.want {
			t.Errorf("musicWorkflowIntentFromText(%q) = %v, want %v", tc.message, got, tc.want)
		}
	}
}

func TestMusicWorkflowIntentMatcherRejectsAdviceAndNegation(t *testing.T) {
	for _, message := range []string{
		"作曲の考え方を説明して",
		"BGMを生成する方法を教えて",
		"曲を作らないで。作り方だけ相談したい",
		"Can you generate a song?",
		"楽曲制作計画を作って",
		"リミックスとは何か説明して",
	} {
		if got := musicWorkflowIntentFromText(message); got != musicWorkflowIntentNone {
			t.Errorf("musicWorkflowIntentFromText(%q) = %v; want none", message, got)
		}
	}
}

func TestMessageRouteCoordinatorRoutesAuthenticatedMusicIntentBeforeCombinedChat(t *testing.T) {
	mio := &musicIntentRouteMioStub{decision: routing.NewDecision(routing.RoutePLAN, 0.9, "rule dictionary")}
	coordinator := newRouteDecisionCoordinator(mio)
	coordinator.SetMusicWorkflowRouting("ren", true, true, false, true)
	input := newOrchestratorTestTurnInput(t, "このプランで歌詞付きの曲を生成して", "viewer", "cmd:ren")
	decision, err := coordinator.DecideWithChatDraft(
		trustedMusicRoutingContext(t, "ren"), input,
		ProcessMessageRequest{Channel: "viewer", To: "mio", UserMessage: input.MessageText()}, modulecore.NewTaskID(), true,
	)
	if err != nil || decision.Decision.Route != routing.RouteOPS || mio.combinedCalls != 0 || mio.routeCalls != 1 {
		t.Fatalf("music route decision=%+v err=%v combined=%d route=%d", decision, err, mio.combinedCalls, mio.routeCalls)
	}
}

type musicIntentRouteMioStub struct {
	decision      routing.Decision
	combinedCalls int
	routeCalls    int
}

func (s *musicIntentRouteMioStub) DecideAction(context.Context, conversation.TurnInput) (routing.Decision, error) {
	s.routeCalls++
	return s.decision, nil
}
func (*musicIntentRouteMioStub) Chat(context.Context, conversation.TurnInput) (string, error) {
	return "", nil
}
func (*musicIntentRouteMioStub) HandleChatCommand(context.Context, string, string) (agent.ChatCommandResult, error) {
	return agent.ChatCommandResult{}, nil
}
func (s *musicIntentRouteMioStub) DecideActionWithChatDraft(context.Context, conversation.TurnInput, bool) (routing.Decision, *agent.MioChatDraft, error) {
	s.combinedCalls++
	return s.decision, nil, nil
}
func (*musicIntentRouteMioStub) ReleaseChatDraft(context.Context, *agent.MioChatDraft) {}

func trustedMusicRoutingContext(t *testing.T, userID string) context.Context {
	t.Helper()
	userCtx := trustedMusicHTTPUserContext(t, userID)
	ctx, err := domaintool.DeriveAgentToolExecutionScope(userCtx, "music-route-mio-request", "mio", "chat", "", false)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func trustedMusicHTTPUserContext(t *testing.T, userID string) context.Context {
	t.Helper()
	userScope, err := domaintool.NewToolExecutionScope(
		"music-route-http-request", domaintool.ActorKindUser, userID, userID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), userScope)
}
