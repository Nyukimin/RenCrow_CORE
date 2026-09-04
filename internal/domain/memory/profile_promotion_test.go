package memory

import (
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestProfilePromotionLogicalRequestBudgets(t *testing.T) {
	if ProfilePromotionEvidenceBlockMax != 3000 {
		t.Fatalf("evidence block max=%d want 3000", ProfilePromotionEvidenceBlockMax)
	}
	if ProfilePromotionExistingContextMax != 800 {
		t.Fatalf("existing context max=%d want 800", ProfilePromotionExistingContextMax)
	}
	if ProfilePromotionMaterialDigestMax != 800 {
		t.Fatalf("material digest max=%d want 800", ProfilePromotionMaterialDigestMax)
	}
	if ProfilePromotionMaxTokens != 1024 {
		t.Fatalf("max tokens=%d want 1024", ProfilePromotionMaxTokens)
	}
	if ProfilePromotionPerGroupCandidateLimit != 4 {
		t.Fatalf("per-group candidate max=%d want 4", ProfilePromotionPerGroupCandidateLimit)
	}
	if ProfilePromotionRepairCandidateLimit != 2 {
		t.Fatalf("repair candidate max=%d want 2", ProfilePromotionRepairCandidateLimit)
	}
	if ProfilePromotionRepairMaxTokens != 512 {
		t.Fatalf("repair max tokens=%d want 512", ProfilePromotionRepairMaxTokens)
	}
	if ProfilePromotionRepairStringMax != 100 {
		t.Fatalf("repair string max=%d want 100", ProfilePromotionRepairStringMax)
	}
	wantInitial := ProfilePromotionEvidenceBlockMax + ProfilePromotionExistingContextMax + ProfilePromotionMaterialDigestMax + ProfilePromotionPromptInstructionMax
	if ProfilePromotionInitialPromptMax != wantInitial {
		t.Fatalf("initial prompt max=%d want derived %d", ProfilePromotionInitialPromptMax, wantInitial)
	}
	wantPrompt := wantInitial + ProfilePromotionRepairInstructionMax
	if ProfilePromotionPromptMax != wantPrompt {
		t.Fatalf("prompt max=%d want derived %d", ProfilePromotionPromptMax, wantPrompt)
	}
}

func TestValidateProfilePromotionProjectionRequiresCanonicalNonEmptyEnums(t *testing.T) {
	base := UserMemory{
		Namespace: "user:ren", UserID: "ren", Type: UserMemoryTypeProfile,
		Statement: "bounded", State: MemoryStateCandidate, Active: true,
		Confidence: 0.7, Sensitivity: "normal", Scope: "all_personas",
	}
	for _, sensitivity := range []string{"", "private"} {
		item := base
		item.Sensitivity = sensitivity
		if err := ValidateProfilePromotionProjection([]UserMemory{item}, "ren"); err == nil {
			t.Fatalf("sensitivity %q was accepted", sensitivity)
		}
	}
	for _, scope := range []string{"", "untrusted"} {
		item := base
		item.Scope = scope
		if err := ValidateProfilePromotionProjection([]UserMemory{item}, "ren"); err == nil {
			t.Fatalf("scope %q was accepted", scope)
		}
	}
}

func profilePromotionEvidenceBatchForTest(t *testing.T) ProfilePromotionBatch {
	t.Helper()
	threadID := modulecore.NewThreadID()
	threadSeq := modulecore.ThreadSeq(1)
	threadKind := modulecore.ThreadKindUserConversation
	return ProfilePromotionBatch{
		LeaseToken: "lease-1",
		SessionID:  "session-1",
		ThreadID:   threadID,
		ThreadSeq:  threadSeq,
		ThreadKind: threadKind,
		Messages: []ProfilePromotionMessage{{
			EventID:    "event-1",
			SessionID:  "session-1",
			ThreadID:   threadID,
			ThreadSeq:  threadSeq,
			ThreadKind: threadKind,
			Text:       "evidence",
		}},
	}
}

func TestValidateProfilePromotionBatchEvidenceRejectsInvalidThreadID(t *testing.T) {
	for _, threadID := range []modulecore.ThreadID{"", "thr_not-a-uuid"} {
		t.Run(string(threadID), func(t *testing.T) {
			batch := profilePromotionEvidenceBatchForTest(t)
			batch.ThreadID = threadID
			batch.Messages[0].ThreadID = threadID
			if err := ValidateProfilePromotionBatchEvidence(batch); err == nil {
				t.Fatalf("thread ID %q was accepted", threadID)
			}
		})
	}
}

func TestValidateProfilePromotionBatchEvidenceRejectsInvalidThreadSequence(t *testing.T) {
	for _, test := range []struct {
		name      string
		threadSeq modulecore.ThreadSeq
	}{
		{name: "zero", threadSeq: 0},
		{name: "negative", threadSeq: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			batch := profilePromotionEvidenceBatchForTest(t)
			batch.ThreadSeq = test.threadSeq
			batch.Messages[0].ThreadSeq = test.threadSeq
			if err := ValidateProfilePromotionBatchEvidence(batch); err == nil {
				t.Fatalf("thread sequence %d was accepted", test.threadSeq)
			}
		})
	}
}

func TestValidateProfilePromotionBatchEvidenceRejectsInvalidThreadKind(t *testing.T) {
	batch := profilePromotionEvidenceBatchForTest(t)
	batch.ThreadKind = modulecore.ThreadKind("invalid")
	batch.Messages[0].ThreadKind = batch.ThreadKind
	if err := ValidateProfilePromotionBatchEvidence(batch); err == nil {
		t.Fatal("invalid thread kind was accepted")
	}
}

func TestValidateProfilePromotionBatchEvidenceRequiresExactMessageThreadBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProfilePromotionBatch)
	}{
		{
			name: "session",
			mutate: func(batch *ProfilePromotionBatch) {
				batch.Messages[0].SessionID = "other-session"
			},
		},
		{
			name: "thread id",
			mutate: func(batch *ProfilePromotionBatch) {
				batch.Messages[0].ThreadID = modulecore.NewThreadID()
			},
		},
		{
			name: "thread sequence",
			mutate: func(batch *ProfilePromotionBatch) {
				batch.Messages[0].ThreadSeq = modulecore.ThreadSeq(2)
			},
		},
		{
			name: "thread kind",
			mutate: func(batch *ProfilePromotionBatch) {
				batch.Messages[0].ThreadKind = modulecore.ThreadKindAgentDiscussion
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			batch := profilePromotionEvidenceBatchForTest(t)
			test.mutate(&batch)
			if err := ValidateProfilePromotionBatchEvidence(batch); err == nil {
				t.Fatalf("message %s mismatch was accepted", test.name)
			}
		})
	}
}
