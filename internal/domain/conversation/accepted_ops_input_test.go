package conversation

import (
	"errors"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func acceptedOPSInputDomainFixture() AcceptedOPSInputRequest {
	return AcceptedOPSInputRequest{
		RequestID:      "ops-request-1",
		OwnerID:        "ren",
		ActorID:        "ren",
		SessionID:      modulecore.NewSessionID(),
		FirstThreadID:  modulecore.NewThreadID(),
		TaskID:         modulecore.NewTaskID(),
		TurnID:         modulecore.NewTurnID(),
		TraceID:        modulecore.NewTraceID(),
		UserMessageID:  modulecore.NewMessageID(),
		AgentMessageID: modulecore.NewMessageID(),
		RawMessage:     "  雪だるま ☃️\n\n",
	}
}

func TestAcceptedOPSInputCanonicalHashPreservesMessageAndOriginOnly(t *testing.T) {
	request := acceptedOPSInputDomainFixture()
	canonical, err := CanonicalAcceptedOPSInputPayload(request)
	if err != nil {
		t.Fatalf("CanonicalAcceptedOPSInputPayload: %v", err)
	}
	if got, want := string(canonical), `{"version":"rencrow.accepted_ops_input.v1","raw_message":"  雪だるま ☃️\n\n"}`; got != want {
		t.Fatalf("automation canonical bytes = %q want %q", got, want)
	}
	first, err := AcceptedOPSInputPayloadSHA256(request)
	if err != nil {
		t.Fatalf("AcceptedOPSInputPayloadSHA256: %v", err)
	}
	retry := request
	retry.SessionID = modulecore.NewSessionID()
	retry.FirstThreadID = modulecore.NewThreadID()
	retry.TaskID = modulecore.NewTaskID()
	retry.TurnID = modulecore.NewTurnID()
	retry.TraceID = modulecore.NewTraceID()
	retry.UserMessageID = modulecore.NewMessageID()
	retry.AgentMessageID = modulecore.NewMessageID()
	retry.DeclaredOrigin = AcceptedOPSInputOriginAutomation
	second, err := AcceptedOPSInputPayloadSHA256(retry)
	if err != nil {
		t.Fatalf("retry AcceptedOPSInputPayloadSHA256: %v", err)
	}
	if first != second {
		t.Fatalf("generated identities changed canonical payload hash: %s != %s", first, second)
	}

	retry.RawMessage = " 雪だるま ☃️\n\n"
	changedBytes, err := AcceptedOPSInputPayloadSHA256(retry)
	if err != nil {
		t.Fatalf("changed message hash: %v", err)
	}
	if changedBytes == first {
		t.Fatal("leading whitespace change did not change the canonical hash")
	}
}

func TestAcceptedOPSInputDefaultsOriginAndAcceptsHuman(t *testing.T) {
	request := acceptedOPSInputDomainFixture()
	normalized, err := NormalizeAcceptedOPSInputRequest(request)
	if err != nil {
		t.Fatalf("normalize default origin: %v", err)
	}
	if normalized.DeclaredOrigin != AcceptedOPSInputOriginAutomation {
		t.Fatalf("default origin = %q, want automation", normalized.DeclaredOrigin)
	}

	automationHash, err := AcceptedOPSInputPayloadSHA256(request)
	if err != nil {
		t.Fatalf("automation payload hash: %v", err)
	}
	request.DeclaredOrigin = AcceptedOPSInputOrigin("human")
	human, err := NormalizeAcceptedOPSInputRequest(request)
	if err != nil || human.DeclaredOrigin != AcceptedOPSInputOrigin("human") {
		t.Fatalf("normalize Human origin = %+v err=%v", human, err)
	}
	humanHash, err := AcceptedOPSInputPayloadSHA256(human)
	if err != nil {
		t.Fatalf("human payload hash: %v", err)
	}
	humanCanonical, err := CanonicalAcceptedOPSInputPayload(human)
	if err != nil {
		t.Fatalf("human canonical payload: %v", err)
	}
	if got, want := string(humanCanonical), `{"version":"rencrow.accepted_ops_input.v1","declared_origin":"human","raw_message":"  雪だるま ☃️\n\n"}`; got != want {
		t.Fatalf("human canonical bytes = %q want %q", got, want)
	}
	if humanHash == automationHash {
		t.Fatal("changing declared origin did not change the canonical payload hash")
	}

	receipt := AcceptedOPSInputReceipt{
		AcceptanceSequence: 1,
		RequestID:          human.RequestID,
		OwnerID:            human.OwnerID,
		ActorID:            human.ActorID,
		SessionID:          human.SessionID,
		ThreadID:           human.FirstThreadID,
		ThreadSeq:          1,
		ThreadKind:         modulecore.ThreadKindUserConversation,
		TaskID:             human.TaskID,
		TurnID:             human.TurnID,
		TraceID:            human.TraceID,
		UserMessageID:      human.UserMessageID,
		AgentMessageID:     human.AgentMessageID,
		DeclaredOrigin:     human.DeclaredOrigin,
		PayloadSHA256:      humanHash,
		RawRecordID:        "raw-record",
		ManifestID:         "manifest",
		RawSHA256:          strings.Repeat("a", 64),
		ManifestSHA256:     strings.Repeat("b", 64),
		AcceptedAt:         time.Now().UTC(),
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("Human receipt validation: %v", err)
	}
}

func TestAcceptedOPSInputBoundsExactUTF8AndIdentity(t *testing.T) {
	base := acceptedOPSInputDomainFixture()
	tests := []struct {
		name   string
		mutate func(*AcceptedOPSInputRequest)
	}{
		{name: "request id byte bound", mutate: func(request *AcceptedOPSInputRequest) {
			request.RequestID = strings.Repeat("r", AcceptedOPSInputMaxRequestIDBytes+1)
		}},
		{name: "owner id byte bound", mutate: func(request *AcceptedOPSInputRequest) {
			request.OwnerID = strings.Repeat("o", AcceptedOPSInputMaxOwnerIDBytes+1)
		}},
		{name: "message byte bound", mutate: func(request *AcceptedOPSInputRequest) {
			request.RawMessage = strings.Repeat("x", AcceptedOPSInputMaxMessageBytes+1)
		}},
		{name: "invalid utf8", mutate: func(request *AcceptedOPSInputRequest) { request.RawMessage = string([]byte{0xff}) }},
		{name: "NUL", mutate: func(request *AcceptedOPSInputRequest) { request.RawMessage = "a\x00b" }},
		{name: "noncanonical owner", mutate: func(request *AcceptedOPSInputRequest) { request.OwnerID = " ren " }},
		{name: "same message ids", mutate: func(request *AcceptedOPSInputRequest) { request.AgentMessageID = request.UserMessageID }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			if _, err := NormalizeAcceptedOPSInputRequest(request); !errors.Is(err, ErrAcceptedOPSInputInvalid) {
				t.Fatalf("NormalizeAcceptedOPSInputRequest error = %v, want invalid", err)
			}
		})
	}
}
