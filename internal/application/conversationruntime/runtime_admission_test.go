package conversationruntime

import (
	"context"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type admissionRecord struct {
	actor    string
	priority domaintask.Priority
}

type actorAdmissionRecorder struct {
	order    *[]string
	records  []admissionRecord
	releases int
}

func (r *actorAdmissionRecorder) Acquire(_ context.Context, actor string, priority domaintask.Priority) (func(), error) {
	r.records = append(r.records, admissionRecord{actor: actor, priority: priority})
	if r.order != nil {
		*r.order = append(*r.order, "acquire:"+actor+":"+string(priority))
	}
	return func() {
		r.releases++
		if r.order != nil {
			*r.order = append(*r.order, "release")
		}
	}, nil
}

type orderedEmissionGenerator struct {
	inner EmissionGenerator
	order *[]string
}

func (g orderedEmissionGenerator) GenerateEmission(ctx context.Context, scope conversation.ConversationScope, snapshot agent.PromptSnapshot, allowlist conversation.EmissionAllowlist) (conversation.AgentEmission, agent.InvocationReceipt, error) {
	*g.order = append(*g.order, "generate")
	return g.inner.GenerateEmission(ctx, scope, snapshot, allowlist)
}

func installActorAdmission(t *testing.T, target any, admission *actorAdmissionRecorder) {
	t.Helper()
	setter := reflect.ValueOf(target).MethodByName("SetActorAdmission")
	if !setter.IsValid() {
		t.Fatal("runtime does not expose SetActorAdmission for the shared actor reservation")
	}
	if setter.Type().NumIn() != 1 || !reflect.TypeOf(admission).Implements(setter.Type().In(0)) {
		t.Fatalf("SetActorAdmission parameter %v does not accept the scheduler admission port", setter.Type())
	}
	setter.Call([]reflect.Value{reflect.ValueOf(admission)})
}

func TestRuntimeAcquiresMioHighAdmissionBeforeGeneration(t *testing.T) {
	fixture := newRuntimeFixture(t, replyEmission("admitted response"))
	var order []string
	admission := &actorAdmissionRecorder{order: &order}
	installActorAdmission(t, fixture.runtime, admission)
	fixture.runtime.generator = orderedEmissionGenerator{inner: fixture.runtime.generator, order: &order}

	result := fixture.runtime.RunAcceptedTurn(context.Background(), fixture.input)
	if result.Status != TurnStatusDelivered || !result.Committed {
		t.Fatalf("RunAcceptedTurn() = %+v, want delivered turn", result)
	}
	if len(admission.records) != 1 || admission.records[0] != (admissionRecord{actor: "mio", priority: domaintask.PriorityHigh}) || admission.releases != 1 {
		t.Fatalf("admission records/releases = %+v/%d, want Mio high priority acquired and released once", admission.records, admission.releases)
	}
	if want := []string{"acquire:mio:high", "generate", "release"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("generation/admission order = %v, want %v", order, want)
	}
}

func TestExternalRuntimeUsesBoundResponderNormalAdmission(t *testing.T) {
	request, _, _ := externalPeerReadViewFixture(t)
	request = externalPeerRequestWithResponderActor(t, request, "shiro")
	view := ReadView{SessionID: request.Session.SessionID, Messages: []PromptMessage{{
		Role: "user", Text: payloadString(request.Accepted.InputEvent.Payload, "text"),
		MessageID: string(request.Accepted.InputMessageID), TurnID: request.Accepted.TurnID, EventSeq: request.Accepted.AdmissionSeq,
	}}}
	provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"A Shiro-attributed response."}}`}
	runtime, err := NewExternalRuntime(RuntimeDependencies{
		ReadViews: externalReadViewBuilder{view: view}, Composer: identityPromptComposer{},
		Generator: agent.NewMioEmissionGenerator(provider), ExternalJournal: &fakeExternalTurnJournal{},
		ExternalResponderActorID: "shiro", Config: RuntimeConfig{PolicyVersion: "policy-shiro-test-v1"},
	})
	if err != nil {
		t.Fatalf("NewExternalRuntime(Shiro): %v", err)
	}
	var order []string
	admission := &actorAdmissionRecorder{order: &order}
	installActorAdmission(t, runtime, admission)
	runtime.generator = orderedEmissionGenerator{inner: runtime.generator, order: &order}

	result := runtime.RunAcceptedTurn(context.Background(), AcceptedTurnInput{ReadViewRequest: request})
	if result.Status != TurnStatusPublicationPending || !result.Committed {
		t.Fatalf("RunAcceptedTurn() = %+v, want committed external response", result)
	}
	if len(admission.records) != 1 || admission.records[0] != (admissionRecord{actor: "shiro", priority: domaintask.PriorityNormal}) || admission.releases != 1 {
		t.Fatalf("admission records/releases = %+v/%d, want bound Shiro normal priority acquired and released once", admission.records, admission.releases)
	}
	if want := []string{"acquire:shiro:normal", "generate", "release"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("generation/admission order = %v, want %v", order, want)
	}
}

func TestBoardOwnerRequestAcquiresMioHighAdmissionBeforeGeneration(t *testing.T) {
	fixture := newBoardOwnerRequestFixture()
	var order []string
	journal := &boardOwnerRequestJournalFake{order: &order}
	provider := &fakeConversationProvider{response: `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"external_peer","text":"A composed owner request."}}`}
	engine := &boardOwnerRequestConversationEngineFake{}
	recentCalls, runtimeCalls := 0, 0
	runtime := newBoardOwnerRequestRuntimeForTest(t, ownerRequestMioComposer(engine, &recentCalls, &runtimeCalls), orderedEmissionGenerator{
		inner: agent.NewMioEmissionGenerator(provider), order: &order,
	}, journal)
	admission := &actorAdmissionRecorder{order: &order}
	installActorAdmission(t, runtime, admission)

	result := runtime.RunAcceptedBoardOwnerRequest(context.Background(), fixture.input)
	if result.Status != BoardOwnerRequestStatusCommitted || result.Cause != nil {
		t.Fatalf("RunAcceptedBoardOwnerRequest() = %+v, want committed request", result)
	}
	if len(admission.records) != 1 || admission.records[0] != (admissionRecord{actor: "mio", priority: domaintask.PriorityHigh}) || admission.releases != 1 {
		t.Fatalf("admission records/releases = %+v/%d, want Mio high priority acquired and released once", admission.records, admission.releases)
	}
	if want := []string{"load", "acquire:mio:high", "generate", "commit", "release"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("owner generation/admission order = %v, want %v", order, want)
	}
}

var _ EmissionGenerator = orderedEmissionGenerator{}
