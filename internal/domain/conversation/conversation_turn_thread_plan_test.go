package conversation

import (
	"errors"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func conversationTurnThreadPlanIdentity(seed string) modulecore.ThreadID {
	id, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "conversation_turn_thread_plan_test", "thread_id", seed)
	if err != nil {
		panic(err)
	}
	return modulecore.ThreadID(id)
}

func TestPlanConversationTurnThreadCreatesFirstThread(t *testing.T) {
	newID := conversationTurnThreadPlanIdentity("new")
	input := ConversationTurnThreadPlanInput{
		Domain:      "writing",
		NewThreadID: newID,
	}

	plan, err := PlanConversationTurnThread(input)
	if err != nil {
		t.Fatalf("PlanConversationTurnThread: %v", err)
	}
	want := ConversationTurnThreadPlan{
		Thread: ConversationTurnThreadIdentity{
			ID:   newID,
			Seq:  1,
			Kind: ThreadKindUserConversation,
		},
		MessageCount: 2,
		Domain:       "writing",
	}
	if plan != want {
		t.Fatalf("plan=%+v, want %+v", plan, want)
	}
}

func TestPlanConversationTurnThreadAddsPairUpToTwelveMessages(t *testing.T) {
	active := ConversationTurnActiveThreadState{
		Thread: ConversationTurnThreadIdentity{
			ID:   conversationTurnThreadPlanIdentity("active"),
			Seq:  4,
			Kind: ThreadKindAgentDiscussion,
		},
		Domain:       "old-domain",
		MessageCount: 10,
	}
	input := ConversationTurnThreadPlanInput{
		Active:      &active,
		Domain:      "new-domain",
		NewThreadID: conversationTurnThreadPlanIdentity("unused"),
	}

	plan, err := PlanConversationTurnThread(input)
	if err != nil {
		t.Fatalf("PlanConversationTurnThread: %v", err)
	}
	want := ConversationTurnThreadPlan{
		Thread:       active.Thread,
		MessageCount: 12,
		Domain:       "new-domain",
	}
	if plan != want {
		t.Fatalf("plan=%+v, want %+v", plan, want)
	}
}

func TestPlanConversationTurnThreadRollsOverForBoundaryOrCapacity(t *testing.T) {
	active := ConversationTurnActiveThreadState{
		Thread: ConversationTurnThreadIdentity{
			ID:   conversationTurnThreadPlanIdentity("active"),
			Seq:  4,
			Kind: ThreadKindAgentDiscussion,
		},
		Domain:       "current-domain",
		MessageCount: 12,
	}
	newID := conversationTurnThreadPlanIdentity("next")
	input := ConversationTurnThreadPlanInput{
		Active:      &active,
		NewThreadID: newID,
	}

	plan, err := PlanConversationTurnThread(input)
	if err != nil {
		t.Fatalf("capacity rollover: %v", err)
	}
	want := ConversationTurnThreadPlan{
		Thread: ConversationTurnThreadIdentity{
			ID:   newID,
			Seq:  5,
			Kind: active.Thread.Kind,
		},
		ClosedThread: active.Thread,
		MessageCount: 2,
		Domain:       active.Domain,
	}
	if plan != want {
		t.Fatalf("capacity rollover plan=%+v, want %+v", plan, want)
	}

	active.MessageCount = 2
	input.Boundary = true
	input.Domain = "explicit-domain"
	boundaryPlan, err := PlanConversationTurnThread(input)
	if err != nil {
		t.Fatalf("explicit boundary: %v", err)
	}
	if boundaryPlan.Thread.ID != newID || boundaryPlan.Thread.Seq != 5 || boundaryPlan.Thread.Kind != active.Thread.Kind ||
		boundaryPlan.ClosedThread != active.Thread || boundaryPlan.MessageCount != 2 || boundaryPlan.Domain != "explicit-domain" {
		t.Fatalf("explicit boundary plan=%+v", boundaryPlan)
	}
}

func TestPlanConversationTurnThreadRejectsInvalidActiveStateAndSequenceOverflow(t *testing.T) {
	valid := ConversationTurnActiveThreadState{
		Thread: ConversationTurnThreadIdentity{
			ID:   conversationTurnThreadPlanIdentity("active"),
			Seq:  1,
			Kind: ThreadKindUserConversation,
		},
		Domain:       "general",
		MessageCount: 2,
	}
	invalidStates := []struct {
		name   string
		change func(*ConversationTurnActiveThreadState)
	}{
		{name: "thread id", change: func(state *ConversationTurnActiveThreadState) { state.Thread.ID = "invalid" }},
		{name: "sequence", change: func(state *ConversationTurnActiveThreadState) { state.Thread.Seq = 0 }},
		{name: "kind", change: func(state *ConversationTurnActiveThreadState) { state.Thread.Kind = "invalid" }},
		{name: "negative message count", change: func(state *ConversationTurnActiveThreadState) { state.MessageCount = -1 }},
		{name: "message count above limit", change: func(state *ConversationTurnActiveThreadState) { state.MessageCount = 13 }},
	}
	for _, test := range invalidStates {
		t.Run(test.name, func(t *testing.T) {
			active := valid
			test.change(&active)
			_, err := PlanConversationTurnThread(ConversationTurnThreadPlanInput{
				Active:      &active,
				Domain:      "general",
				NewThreadID: conversationTurnThreadPlanIdentity("next-" + test.name),
			})
			if !errors.Is(err, ErrConversationTurnInvalid) {
				t.Fatalf("error=%v, want ErrConversationTurnInvalid", err)
			}
		})
	}

	overflow := valid
	overflow.Thread.Seq = modulecore.ThreadSeq(1<<63 - 1)
	_, err := PlanConversationTurnThread(ConversationTurnThreadPlanInput{
		Active:      &overflow,
		Boundary:    true,
		Domain:      "general",
		NewThreadID: conversationTurnThreadPlanIdentity("overflow-next"),
	})
	if !errors.Is(err, ErrConversationTurnInvalid) {
		t.Fatalf("sequence overflow error=%v, want ErrConversationTurnInvalid", err)
	}
}
