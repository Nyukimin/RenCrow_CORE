package agent

import (
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type mioTurnStreamTestGuard struct {
	inUse       bool
	useCalls    int
	retainCalls int
	err         error
	afterErr    error
}

func (g *mioTurnStreamTestGuard) OwnerID() string                                   { return "ren" }
func (g *mioTurnStreamTestGuard) SourceVersion() string                             { return "v1" }
func (g *mioTurnStreamTestGuard) BindingGeneration() conversation.BindingGeneration { return 1 }
func (g *mioTurnStreamTestGuard) MatchesPreparation(*conversation.ConversationTurnPreparation) bool {
	return true
}
func (g *mioTurnStreamTestGuard) Validate() error { return nil }
func (g *mioTurnStreamTestGuard) Use(enqueue func() error) error {
	g.useCalls++
	if g.err != nil {
		return g.err
	}
	g.inUse = true
	defer func() { g.inUse = false }()
	if err := enqueue(); err != nil {
		return err
	}
	return g.afterErr
}
func (g *mioTurnStreamTestGuard) RetainPublication() (conversation.PreparedChatTurnPublicationGuard, error) {
	if !g.inUse {
		return nil, errors.New("publication qualification retained outside Use")
	}
	g.retainCalls++
	return mioTurnPublicationGuardTestStub{}, nil
}
func (g *mioTurnStreamTestGuard) Release() error { return nil }

type mioTurnPublicationGuardTestStub struct{}

func (mioTurnPublicationGuardTestStub) Use(claim func() error) error { return claim() }
func (mioTurnPublicationGuardTestStub) Retain() conversation.PreparedChatTurnPublicationGuard {
	return mioTurnPublicationGuardTestStub{}
}
func (mioTurnPublicationGuardTestStub) Release() {}

func TestMioLogicalAnswerDeltaCallbackRunsInsidePreparedTurnGuard(t *testing.T) {
	guard := &mioTurnStreamTestGuard{}
	handlerCalled := false
	var guardUse MioTurnStreamGuardUse
	ctx := ContextWithMioTurnStreamEventHandler(t.Context(), func(event llm.LogicalOutcomeEvent, authority MioTurnStreamGuardUse) (func(bool), error) {
		if event.Kind != llm.LogicalOutcomeAnswerDelta || !guard.inUse {
			t.Fatalf("event handler ran outside the prepared-turn guard: kind=%q inUse=%t", event.Kind, guard.inUse)
		}
		guardUse = authority
		if authority.Retain == nil {
			t.Fatal("answer_delta did not receive a retained publication qualifier")
		}
		if _, err := authority.Retain(); err != nil {
			t.Fatalf("retain writer publication qualifier inside guard: %v", err)
		}
		handlerCalled = true
		return func(publish bool) {
			if guard.inUse {
				t.Fatal("admission ticket was completed before the prepared-turn guard was released")
			}
			if !publish {
				t.Fatal("valid admission ticket was discarded")
			}
		}, nil
	})
	request := withMioTurnLogicalOutcomeStream(ctx, llm.GenerateRequest{}, guard)
	if request.OnLogicalOutcomeEvent == nil {
		t.Fatal("logical outcome callback was not installed")
	}
	if err := request.OnLogicalOutcomeEvent(llm.LogicalOutcomeEvent{Kind: llm.LogicalOutcomeAnswerDelta, Text: "prefix"}); err != nil {
		t.Fatalf("answer_delta callback: %v", err)
	}
	if guardUse.Use == nil || guardUse.Retain == nil || guardUse.OwnerID != "ren" || guardUse.SourceVersion == "" || guardUse.BindingGeneration != 1 {
		t.Fatal("answer_delta did not receive its prepared-turn revalidation closure")
	}
	revalidated := false
	if err := guardUse.Use(func() error { revalidated = true; return nil }); err != nil {
		t.Fatalf("post-admission prepared-turn revalidation: %v", err)
	}
	if !handlerCalled || !revalidated || guard.useCalls != 2 || guard.retainCalls != 1 || guard.inUse {
		t.Fatalf("handlerCalled=%t guardUseCalls=%d guardStillHeld=%t", handlerCalled, guard.useCalls, guard.inUse)
	}
}

func TestMioLogicalAnswerDeltaFailsClosedWithoutPreparedGuard(t *testing.T) {
	handlerCalls := 0
	ctx := ContextWithMioTurnStreamEventHandler(t.Context(), func(llm.LogicalOutcomeEvent, MioTurnStreamGuardUse) (func(bool), error) {
		handlerCalls++
		return nil, nil
	})
	request := withMioTurnLogicalOutcomeStream(ctx, llm.GenerateRequest{}, nil)
	if request.OnLogicalOutcomeEvent == nil {
		t.Fatal("logical outcome callback was not installed")
	}
	if err := request.OnLogicalOutcomeEvent(llm.LogicalOutcomeEvent{Kind: llm.LogicalOutcomeAnswerDelta, Text: "must stay private"}); err == nil {
		t.Fatal("answer_delta without a prepared-turn guard was accepted")
	}
	if handlerCalls != 0 {
		t.Fatalf("unguarded answer_delta reached the transport callback %d times", handlerCalls)
	}
}

func TestMioLogicalAnswerDeltaPropagatesGuardRejection(t *testing.T) {
	guardErr := errors.New("prepared recall was revoked")
	guard := &mioTurnStreamTestGuard{err: guardErr}
	handlerCalls := 0
	ctx := ContextWithMioTurnStreamEventHandler(t.Context(), func(llm.LogicalOutcomeEvent, MioTurnStreamGuardUse) (func(bool), error) {
		handlerCalls++
		return nil, nil
	})
	request := withMioTurnLogicalOutcomeStream(ctx, llm.GenerateRequest{}, guard)
	if err := request.OnLogicalOutcomeEvent(llm.LogicalOutcomeEvent{Kind: llm.LogicalOutcomeAnswerDelta, Text: "revoked"}); !errors.Is(err, guardErr) {
		t.Fatalf("answer_delta callback error=%v, want guard rejection", err)
	}
	if handlerCalls != 0 {
		t.Fatalf("guard-rejected delta reached the transport callback %d times", handlerCalls)
	}
}

func TestMioLogicalAnswerDeltaDiscardsTicketWhenGuardFailsAfterAdmission(t *testing.T) {
	guardErr := errors.New("prepared turn invalidated after queue admission")
	guard := &mioTurnStreamTestGuard{afterErr: guardErr}
	finishCalled := false
	finishResult := true
	ctx := ContextWithMioTurnStreamEventHandler(t.Context(), func(llm.LogicalOutcomeEvent, MioTurnStreamGuardUse) (func(bool), error) {
		return func(publish bool) { finishCalled = true; finishResult = publish }, nil
	})
	request := withMioTurnLogicalOutcomeStream(ctx, llm.GenerateRequest{}, guard)
	if err := request.OnLogicalOutcomeEvent(llm.LogicalOutcomeEvent{Kind: llm.LogicalOutcomeAnswerDelta, Text: "discard me"}); !errors.Is(err, guardErr) {
		t.Fatalf("answer_delta callback error=%v, want post-admission guard rejection", err)
	}
	if !finishCalled || finishResult {
		t.Fatalf("admission ticket finish=%t publish=%t, want a discarded ticket", finishCalled, finishResult)
	}
}
