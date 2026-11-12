package main

import (
	"context"
	"reflect"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type channelAdmissionRecorder struct {
	order    *[]string
	actor    string
	priority domaintask.Priority
	releases int
}

func (a *channelAdmissionRecorder) Acquire(_ context.Context, actor string, priority domaintask.Priority) (func(), error) {
	a.actor, a.priority = actor, priority
	*a.order = append(*a.order, "acquire:"+actor+":"+string(priority))
	return func() {
		a.releases++
		*a.order = append(*a.order, "release")
	}, nil
}

type channelAdmissionProcessorFake struct{ order *[]string }

func (p channelAdmissionProcessorFake) ProcessMessage(_ context.Context, _ orchestrator.ProcessMessageRequest) (orchestrator.ProcessMessageResponse, error) {
	*p.order = append(*p.order, "process")
	return orchestrator.ProcessMessageResponse{Response: "ok"}, nil
}

func TestDirectChannelMessagesAcquireHighAdmissionBeforeOrchestration(t *testing.T) {
	for _, test := range []struct {
		name string
		req  orchestrator.ProcessMessageRequest
		want string
	}{
		{name: "default Mio route", req: orchestrator.ProcessMessageRequest{Channel: "telegram"}, want: "mio"},
		{name: "explicit responder route", req: orchestrator.ProcessMessageRequest{Channel: "discord", To: "shiro"}, want: "shiro"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var order []string
			admission := &channelAdmissionRecorder{order: &order}
			processor := channelActorAdmissionProcessor{next: channelAdmissionProcessorFake{order: &order}, admission: func() conversationruntime.ActorAdmission { return admission }}
			if _, err := processor.ProcessMessage(context.Background(), test.req); err != nil {
				t.Fatalf("ProcessMessage(): %v", err)
			}
			if admission.actor != test.want || admission.priority != domaintask.PriorityHigh || admission.releases != 1 {
				t.Fatalf("admission actor=%q priority=%q releases=%d, want %q/high/1", admission.actor, admission.priority, admission.releases, test.want)
			}
			if want := []string{"acquire:" + test.want + ":high", "process", "release"}; !reflect.DeepEqual(order, want) {
				t.Fatalf("channel admission order = %v, want %v", order, want)
			}
		})
	}
}

func TestNonDirectChannelDoesNotAcquireActorAdmission(t *testing.T) {
	var order []string
	admission := &channelAdmissionRecorder{order: &order}
	processor := channelActorAdmissionProcessor{next: channelAdmissionProcessorFake{order: &order}, admission: func() conversationruntime.ActorAdmission { return admission }}
	if _, err := processor.ProcessMessage(context.Background(), orchestrator.ProcessMessageRequest{Channel: "background"}); err != nil {
		t.Fatalf("ProcessMessage(): %v", err)
	}
	if admission.actor != "" || admission.releases != 0 || !reflect.DeepEqual(order, []string{"process"}) {
		t.Fatalf("non-direct channel admission actor=%q releases=%d order=%v, want no admission and one process", admission.actor, admission.releases, order)
	}
}

func TestChannelProcessorResolvesAdmissionAfterConstruction(t *testing.T) {
	var order []string
	var active conversationruntime.ActorAdmission
	processor := channelActorAdmissionProcessor{
		next:      channelAdmissionProcessorFake{order: &order},
		admission: func() conversationruntime.ActorAdmission { return active },
	}
	active = &channelAdmissionRecorder{order: &order}
	if _, err := processor.ProcessMessage(context.Background(), orchestrator.ProcessMessageRequest{Channel: "line"}); err != nil {
		t.Fatalf("ProcessMessage(): %v", err)
	}
	if want := []string{"acquire:mio:high", "process", "release"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("late-bound channel admission order = %v, want %v", order, want)
	}
}
