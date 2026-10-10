package action

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

func TestNativeDelegationValidatesMutationHashAndOrdering(t *testing.T) {
	t.Parallel()
	open := preparedNativeMutation("core.attempt.open", []byte(`{"idempotency_key":"core.attempt.open"}`))
	if err := (NativeDelegation{Mode: NativeDelegationModeOpenStart, Open: &open}).Validate(); err != nil {
		t.Fatalf("prepared open: %v", err)
	}
	start := preparedNativeMutation("core.attempt.start", []byte(`{"idempotency_key":"core.attempt.start"}`))
	if err := (NativeDelegation{Mode: NativeDelegationModeOpenStart, Start: &start}).Validate(); err == nil {
		t.Fatal("start without an accepted open result must be rejected")
	}

	open.Status = NativeMutationStatusAccepted
	open.Result = []byte(`{"thread_id":"thread-1"}`)
	delegation := NativeDelegation{Mode: NativeDelegationModeOpenStart, Open: &open, Start: &start}
	if err := delegation.Validate(); err != nil {
		t.Fatalf("ordered open and start: %v", err)
	}
	delegation.Open.Payload[0] ^= 0x01
	if err := delegation.Validate(); err == nil {
		t.Fatal("payload bytes whose hash changed must be rejected")
	}
}

func TestNativeDelegationLegacyModeResolvesOnlyToOpenStart(t *testing.T) {
	legacy := NativeDelegation{}
	if got := legacy.EffectiveMode(); got != NativeDelegationModeOpenStart {
		t.Fatalf("mode-less historical state resolves to %q, want open/start", got)
	}
	if err := legacy.Validate(); err != nil {
		t.Fatalf("mode-less historical open/start state should remain readable: %v", err)
	}

	resume := acceptedNativeMutation("resume-key", []byte(`{"idempotency_key":"resume-key"}`), []byte(`{"accepted":true}`))
	source := NativeDelegationResumeSource{
		ActionID: "act_018f1c2d-3e4f-7a5b-8c6d-7e8f9a0b0001", AttemptID: "att_018f1c2d-3e4f-7a5b-8c6d-7e8f9a0b0002",
		HarnessTaskID: "tsk_018f1c2d-3e4f-7a5b-8c6d-7e8f9a0b0003", ThreadID: "thr_018f1c2d-3e4f-7a5b-8c6d-7e8f9a0b0004",
		RunID: "run_018f1c2d-3e4f-7a5b-8c6d-7e8f9a0b0005", ReceiptID: "rcp_018f1c2d-3e4f-7a5b-8c6d-7e8f9a0b0006",
		RunResultSHA256: testSHA256("source-result"),
	}
	modeLessResume := NativeDelegation{ResumeSource: &source, Resume: &resume}
	if got := modeLessResume.EffectiveMode(); got != "" {
		t.Fatalf("mode-less Resume state resolves to %q, want invalid", got)
	}
	if err := modeLessResume.Validate(); err == nil {
		t.Fatal("mode-less Resume state must not be reinterpreted as historical open/start")
	}
}

func TestNativeDelegationCloneCopiesNestedBytesAndReferences(t *testing.T) {
	t.Parallel()
	reference := conversation.AcceptedOPSInputReference{OwnerID: "owner", RequestID: "request", PayloadSHA256: testSHA256("input")}
	open := acceptedNativeMutation("open-key", []byte(`{"idempotency_key":"open-key"}`), []byte(`{"session":"one"}`))
	delegation := NativeDelegation{
		Mode:            NativeDelegationModeOpenStart,
		InputReference:  &reference,
		Open:            &open,
		RunResult:       []byte(`{"status":"completed"}`),
		LastObservation: &NativeObservation{Kind: "run.event", Code: "running", ObservedAt: time.Now().UTC()},
	}
	cloned := delegation.Clone()
	cloned.InputReference.OwnerID = "changed"
	cloned.Open.Payload[0] ^= 0x01
	cloned.Open.Result[0] ^= 0x01
	cloned.RunResult[0] ^= 0x01
	cloned.LastObservation.Code = "changed"
	if delegation.InputReference.OwnerID != "owner" || delegation.Open.Payload[0] != '{' || delegation.Open.Result[0] != '{' || delegation.RunResult[0] != '{' || delegation.LastObservation.Code != "running" {
		t.Fatal("Clone() returned aliased nested native state")
	}
}

func preparedNativeMutation(key string, payload []byte) NativeMutation {
	return NativeMutation{Key: key, Payload: payload, PayloadSHA256: testSHA256(string(payload)), Status: NativeMutationStatusPrepared}
}

func acceptedNativeMutation(key string, payload, result []byte) NativeMutation {
	mutation := preparedNativeMutation(key, payload)
	mutation.Status = NativeMutationStatusAccepted
	mutation.Result = result
	return mutation
}

func testSHA256(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}
