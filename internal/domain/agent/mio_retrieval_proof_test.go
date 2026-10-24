package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type mioRetrievalTestQualification struct {
	ownerID       string
	userScope     string
	sourceVersion string
}

func (q mioRetrievalTestQualification) OwnerID() string       { return q.ownerID }
func (q mioRetrievalTestQualification) UserScope() string     { return q.userScope }
func (q mioRetrievalTestQualification) SourceVersion() string { return q.sourceVersion }

func validMioUserMemoryResultForTest(input conversation.TurnInput, ownerID, sourceVersion, text string) MioRetrievalResult {
	updatedAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	itemID := "memory-test-1"
	evidenceID := "evt-test-1"
	return MioRetrievalResult{
		SourceKind: MioRetrievalSourceUserMemory,
		Status:     MioRetrievalStatusFound,
		Items:      []MioRetrievalItem{{Text: text}},
		Qualification: mioRetrievalTestQualification{
			ownerID: ownerID, userScope: "user:" + ownerID, sourceVersion: sourceVersion,
		},
		Provenance: []MioRetrievalProvenance{{
			Reference: domainmemory.UserMemoryChatRecallSourceReference{
				OwnerID: ownerID, SessionID: modulecore.SessionID(input.SessionID()), MemoryID: itemID,
				Namespace: "user:" + ownerID, SourceType: "user_memory", Source: "owner confirmed memory",
				EvidenceEventIDs: []string{evidenceID}, PersonaScope: "mio", State: domainmemory.MemoryStateConfirmed,
				Sensitivity: "normal", Active: true, UpdatedAt: updatedAt,
				OwnerVersion: updatedAt.UTC().Format(time.RFC3339Nano),
			},
			TurnID: string(input.TurnID()), TraceID: string(input.TraceID()), RootTaskID: string(input.RootTaskID()),
			StatementSHA256: sha256.Sum256([]byte(text)),
		}},
	}
}

func TestValidateMioRetrievalResultRejectsEvidenceFreePositiveUserMemory(t *testing.T) {
	result := MioRetrievalResult{
		SourceKind: MioRetrievalSourceUserMemory,
		Status:     MioRetrievalStatusFound,
		Items:      []MioRetrievalItem{{Text: "Ren prefers blue tea"}},
	}
	if err := validateMioRetrievalResult(result); err == nil {
		t.Fatal("positive UserMemory result without owner provenance was accepted")
	}
}

func TestValidateMioRetrievalResultKeepsUserMemoryProvenanceOffPromptJSON(t *testing.T) {
	address, err := conversation.NewChannelAddress("viewer", "user")
	if err != nil {
		t.Fatalf("NewChannelAddress: %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "Do you remember my tea preference?", address)
	if err != nil {
		t.Fatalf("NewTurnInput: %v", err)
	}
	input = input.WithSessionID(string(modulecore.NewSessionID()))
	result := validMioUserMemoryResultForTest(input, "owner", "source-v1", "Ren prefers blue tea")
	if err := validateMioRetrievalResult(result); err != nil {
		t.Fatalf("validateMioRetrievalResult(valid evidence): %v", err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	for _, secret := range []string{"memory-test-1", "evt-test-1", "owner confirmed memory", "source-v1", "owner"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("serialized Mio retrieval result exposed off-prompt provenance %q: %s", secret, encoded)
		}
	}
}

func TestMioContinuationRejectsUserMemoryFromAnotherPreparedSourceVersion(t *testing.T) {
	input := newAgentTurnInput(t, "何時に出発するんだっけ？", "viewer", "user").WithSessionID(string(modulecore.NewSessionID()))
	preparation := &conversation.ConversationTurnPreparation{TurnID: input.TurnID(), SessionID: input.SessionID(), OwnerID: "owner"}
	guard := &preparedChatGuardStub{}
	engine := &preparedChatEngineStub{
		mockConversationEngine: &mockConversationEngine{},
		prepared: &conversation.PreparedChatTurn{
			RecallPack: &conversation.RecallPack{}, Preparation: preparation, Guard: guard,
		},
	}
	classifier := &preparedChatClassifierStub{classifications: []MioTurnClassification{
		{
			Decision: routing.NewDecision(routing.RouteCHAT, 0.95, "needs owner memory"),
			Outcome: MioTurnOutcome{Kind: MioTurnOutcomeRetrieve, Requests: []MioRetrievalRequest{{
				SourceKind: MioRetrievalSourceUserMemory, Query: "departure", InformationNeeded: "departure time",
			}}},
		},
		{
			Decision: routing.NewDecision(routing.RouteCHAT, 0.95, "answer from owner evidence"),
			Outcome:  MioTurnOutcome{Kind: MioTurnOutcomeAnswer, Text: "10時だよ。"},
		},
	}}
	mio := NewMioAgent(&mockLLMProvider{}, classifier, &mockRuleDictionary{}, nil, nil, engine)
	if _, draft, err := mio.DecideActionWithChatDraft(context.Background(), input, true); err != nil || draft == nil {
		t.Fatalf("DecideActionWithChatDraft: draft=%v err=%v", draft != nil, err)
	} else {
		wrongVersion := validMioUserMemoryResultForTest(input, "owner", "revoked-source-version", "Ren leaves at 10")
		if _, err := mio.ContinueChatDraft(context.Background(), input, draft, []MioRetrievalResult{wrongVersion}); !errors.Is(err, ErrInvalidMioRetrievalResult) {
			t.Fatalf("ContinueChatDraft(wrong source version) error=%v, want invalid result", err)
		}
		if len(classifier.requests) != 1 {
			t.Fatalf("mismatched source version reached LLM continuation: requests=%d, want only initial generation", len(classifier.requests))
		}
		valid := validMioUserMemoryResultForTest(input, "owner", "source-v1", "Ren leaves at 10")
		if _, err := mio.ContinueChatDraft(context.Background(), input, draft, []MioRetrievalResult{valid}); err != nil {
			t.Fatalf("ContinueChatDraft(matching source version): %v", err)
		}
		mio.ReleaseChatDraft(context.Background(), draft)
	}
}
