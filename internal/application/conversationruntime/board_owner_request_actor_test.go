package conversationruntime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func boardOwnerActorResponse(t *testing.T) (*boardOwnerRequestProviderFake, *boardOwnerRequestEmissionGeneratorSpy) {
	t.Helper()
	responseJSON, err := json.Marshal(conversation.AgentEmission{
		SchemaVersion: "mio.turn.v1", Kind: conversation.EmissionKindReply,
		UserMessage: conversation.AgentUserMessage{To: "external_peer", Text: boardOwnerGeneratedText},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &boardOwnerRequestProviderFake{response: llm.GenerateResponse{
		Content: string(responseJSON), RequestID: modulecore.NewRequestID(), ResponseID: modulecore.NewResponseID(),
	}}
	return provider, &boardOwnerRequestEmissionGeneratorSpy{inner: agent.NewMioEmissionGenerator(provider)}
}

func setBoardOwnerRequestTargetActor(fixture *boardOwnerRequestFixture, actorID string) {
	binding := fixture.input.ReadViewRequest.Accepted.InputEvent.Payload["board_owner_request_binding"].(map[string]any)
	binding["target_actor_id"] = actorID
	fixture.source.TargetActorID = actorID
}

func TestBoardOwnerRequestExplicitTargetActorBindsRuntimeAndCanonicalMention(t *testing.T) {
	fixture := newBoardOwnerRequestFixture()
	setBoardOwnerRequestTargetActor(&fixture, "shiro")
	journal := &boardOwnerRequestJournalFake{}
	provider, generator := boardOwnerActorResponse(t)
	runtime, err := NewBoardOwnerRequestRuntime(BoardOwnerRequestRuntimeDependencies{
		Composer:  agent.NewMioAgent(nil, nil, nil, nil, nil, nil).WithSystemPrompt("Shiro request persona"),
		Generator: generator, Journal: journal, PolicyVersion: "board-owner-request-v1", TargetActorID: "shiro",
	})
	if err != nil {
		t.Fatalf("NewBoardOwnerRequestRuntime(Shiro): %v", err)
	}
	result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
	if result.Status != BoardOwnerRequestStatusCommitted || result.Cause != nil || len(journal.commits) != 1 {
		t.Fatalf("RunAcceptedBoardOwnerRequest(Shiro) = %+v, commits=%d; want one committed Shiro response", result, len(journal.commits))
	}
	commit := journal.commits[0]
	if generator.scope.ActorID != "shiro" || provider.scope.ActorID != "shiro" || commit.ExpectedLookup.Source.TargetActorID != "shiro" {
		t.Fatalf("runtime actor scope = generator:%q provider:%q source:%q; want all Shiro", generator.scope.ActorID, provider.scope.ActorID, commit.ExpectedLookup.Source.TargetActorID)
	}
	if commit.Event.ActorKind != "agent" || commit.Event.ActorID != "shiro" ||
		commit.Event.Payload["from"] != "shiro" || commit.Event.Payload["source_actor_id"] != "shiro" ||
		!reflect.DeepEqual(commit.Event.Payload["mentions"], []any{fixture.source.PeerPrincipalID}) ||
		commit.MessageIndex.FromActor != "shiro" || commit.MessageIndex.ToActor != fixture.source.PeerPrincipalID {
		t.Fatalf("canonical Shiro response did not preserve the authorized peer mention: event=%#v index=%+v", commit.Event, commit.MessageIndex)
	}
}

func TestBoardOwnerRequestRuntimeRejectsDifferentExplicitTargetActor(t *testing.T) {
	fixture := newBoardOwnerRequestFixture()
	setBoardOwnerRequestTargetActor(&fixture, "shiro")
	journal := &boardOwnerRequestJournalFake{}
	provider, generator := boardOwnerActorResponse(t)
	runtime, err := NewBoardOwnerRequestRuntime(BoardOwnerRequestRuntimeDependencies{
		Composer: agent.NewMioAgent(nil, nil, nil, nil, nil, nil), Generator: generator,
		Journal: journal, PolicyVersion: "board-owner-request-v1", TargetActorID: "kuro",
	})
	if err != nil {
		t.Fatalf("NewBoardOwnerRequestRuntime(Kuro): %v", err)
	}
	result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
	if result.Status == BoardOwnerRequestStatusCommitted || result.Cause == nil || len(journal.loads) != 0 ||
		len(journal.commits) != 0 || provider.calls != 0 {
		t.Fatalf("Kuro runtime consumed a Shiro request: result=%+v loads=%d commits=%d provider_calls=%d", result, len(journal.loads), len(journal.commits), provider.calls)
	}
}

func TestMioBoundBoardOwnerRequestRuntimeKeepsLegacySourceAndHash(t *testing.T) {
	fixture := newBoardOwnerRequestFixture()
	provider, generator := boardOwnerActorResponse(t)
	journal := &boardOwnerRequestJournalFake{}
	runtime, err := NewBoardOwnerRequestRuntime(BoardOwnerRequestRuntimeDependencies{
		Composer: agent.NewMioAgent(nil, nil, nil, nil, nil, nil).WithSystemPrompt("Mio owner request persona"), Generator: generator,
		Journal: journal, PolicyVersion: "board-owner-request-v1", TargetActorID: "mio",
	})
	if err != nil {
		t.Fatalf("NewBoardOwnerRequestRuntime(Mio): %v", err)
	}
	result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
	if result.Status != BoardOwnerRequestStatusCommitted || result.Cause != nil || len(journal.commits) != 1 {
		t.Fatalf("RunAcceptedBoardOwnerRequest(legacy Mio) = %+v commits=%d; want legacy-compatible commit", result, len(journal.commits))
	}
	commit := journal.commits[0]
	if generator.scope.ActorID != boardOwnerRequestActorID || provider.scope.ActorID != boardOwnerRequestActorID ||
		commit.Event.ActorID != "mio" || commit.Event.Payload["from"] != "mio" || commit.Event.Payload["mentions"] != nil {
		t.Fatalf("legacy Mio semantics changed: scope=%q event_actor=%q payload=%#v", generator.scope.ActorID, commit.Event.ActorID, commit.Event.Payload)
	}
	// This is the legacy source/body fingerprint from the pre-actor baseline.
	if got, want := commit.ExpectedLookup.SourceFingerprint, "7ce49e33a59d0461ffe2470fe736ace504d5b0f08ce71a605fbc7c995acfd1c7"; got != want {
		t.Fatalf("legacy source fingerprint = %s, want unchanged %s", got, want)
	}
}

func TestBoardOwnerRequestRejectsMalformedExplicitTargetActorBeforeJournal(t *testing.T) {
	for _, actorID := range []string{"", "unknown", "Shiro", " shiro"} {
		t.Run(actorID, func(t *testing.T) {
			fixture := newBoardOwnerRequestFixture()
			setBoardOwnerRequestTargetActor(&fixture, actorID)
			journal := &boardOwnerRequestJournalFake{}
			provider, generator := boardOwnerActorResponse(t)
			runtime := newBoardOwnerRequestRuntimeForTest(t, agent.NewMioAgent(nil, nil, nil, nil, nil, nil), generator, journal)
			result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
			if result.Status == BoardOwnerRequestStatusCommitted || result.Cause == nil || len(journal.loads) != 0 || provider.calls != 0 {
				t.Fatalf("malformed explicit target actor %q reached runtime work: result=%+v loads=%d provider_calls=%d", actorID, result, len(journal.loads), provider.calls)
			}
		})
	}
}
