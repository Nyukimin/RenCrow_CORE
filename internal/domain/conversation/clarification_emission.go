package conversation

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const clarificationDecisionSchemaVersion = "mio.clarification.v1"

// MaxClarificationRevision bounds same-assignee continuations for a single
// clarification chain. At this revision Mio can still ask the user, but CORE
// will not accept another automatic continuation.
const MaxClarificationRevision uint64 = 4

type ClarificationDecisionKind string

const (
	ClarificationDecisionAskUser            ClarificationDecisionKind = "ask_user"
	ClarificationDecisionContinueDelegation ClarificationDecisionKind = "continue_delegation"
)

// ClarificationTerminalReason records why CORE durably ended a clarification
// control Turn without accepting a Mio decision.
type ClarificationTerminalReason string

const (
	ClarificationTerminalGenerationError ClarificationTerminalReason = "generation_error"
	ClarificationTerminalInvalidOutput   ClarificationTerminalReason = "invalid_output"
	ClarificationTerminalRevisionLimit   ClarificationTerminalReason = "revision_limit"
)

// ClarificationTerminalUserText is the fixed public text for a safe terminal
// error. Provider errors and rejected model output are never exposed.
const ClarificationTerminalUserText = "この依頼を安全に続けられませんでした。必要な情報を含めて、新しいメッセージを送ってください。"

// ClarificationDecision is Mio's typed response to an assignee's private
// clarification question. It deliberately contains no actor, delegation ID,
// revision, constraint, or capability fields; CORE preserves those from the
// stored handoff.
type ClarificationDecision struct {
	SchemaVersion string                    `json:"schema_version"`
	Kind          ClarificationDecisionKind `json:"kind"`
	Question      string                    `json:"question,omitempty"`
	Instruction   string                    `json:"instruction,omitempty"`
}

// ClarificationControlContext binds the private question to the canonical
// handoff and control Turn supplied by EventStore.
type ClarificationControlContext struct {
	Handoff              HandoffEnvelope
	QuestionDelegationID core.RequestID
	QuestionRevision     uint64
	QuestionMessageID    core.MessageID
	QuestionSender       string
	QuestionText         string
}

func (input ClarificationControlContext) Validate(scope ConversationScope) error {
	if err := scope.Validate(); err != nil || scope.Purpose != PurposeClarificationControl {
		return errors.New("clarification control scope is invalid")
	}
	if err := ValidateHandoffEnvelope(input.Handoff); err != nil {
		return fmt.Errorf("clarification control handoff: %w", err)
	}
	if input.QuestionDelegationID != input.Handoff.DelegationID || input.QuestionRevision != input.Handoff.Revision ||
		input.QuestionMessageID.Validate() != nil || input.QuestionMessageID != scope.InputID || scope.TurnID == input.Handoff.OriginTurnID ||
		input.Handoff.SessionID != scope.SessionID || input.Handoff.SourceTraceID != scope.TraceID ||
		input.QuestionSender != input.Handoff.ToActor || strings.TrimSpace(input.QuestionText) == "" ||
		len([]byte(input.QuestionText)) > MaxReadbackTextBytes {
		return errors.New("clarification question does not match its authenticated control context")
	}
	return nil
}

// ParseClarificationDecision accepts exactly one constrained Mio decision.
// The LLM may ask the user or propose revised instructions; it cannot choose
// an assignee or change delegation identity, revision, constraints, or ACL.
func ParseClarificationDecision(raw []byte, scope ConversationScope) (ClarificationDecision, error) {
	if scope.Validate() != nil || scope.Purpose != PurposeClarificationControl ||
		len(raw) == 0 || len(raw) > MaxEnvelopeBytes || !utf8.Valid(raw) {
		return ClarificationDecision{}, errors.New("invalid clarification decision input")
	}
	if err := validateSingleJSONEnvelope(raw); err != nil {
		return ClarificationDecision{}, errors.New("clarification decision must be one JSON object")
	}
	root, err := strictObjectFields(raw, []string{"schema_version", "kind"}, "schema_version", "kind", "question", "instruction")
	if err != nil {
		return ClarificationDecision{}, errors.New("clarification decision fields are invalid")
	}
	version, err := jsonString(root["schema_version"])
	if err != nil || version != clarificationDecisionSchemaVersion {
		return ClarificationDecision{}, errors.New("clarification decision schema_version is invalid")
	}
	kindValue, err := jsonString(root["kind"])
	if err != nil {
		return ClarificationDecision{}, errors.New("clarification decision kind is invalid")
	}
	decision := ClarificationDecision{SchemaVersion: version, Kind: ClarificationDecisionKind(kindValue)}
	switch decision.Kind {
	case ClarificationDecisionAskUser:
		fields, err := strictObjectFields(raw, []string{"schema_version", "kind", "question"}, "schema_version", "kind", "question")
		if err != nil {
			return ClarificationDecision{}, errors.New("ask_user clarification decision fields are invalid")
		}
		decision.Question, err = jsonString(fields["question"])
		if err != nil || strings.TrimSpace(decision.Question) == "" || len([]byte(decision.Question)) > MaxUserMessageBytes {
			return ClarificationDecision{}, errors.New("ask_user clarification question is empty or oversized")
		}
	case ClarificationDecisionContinueDelegation:
		fields, err := strictObjectFields(raw, []string{"schema_version", "kind", "instruction"}, "schema_version", "kind", "instruction")
		if err != nil {
			return ClarificationDecision{}, errors.New("continue_delegation clarification decision fields are invalid")
		}
		decision.Instruction, err = jsonString(fields["instruction"])
		if err != nil || strings.TrimSpace(decision.Instruction) == "" || len([]byte(decision.Instruction)) > MaxInstructionBytes {
			return ClarificationDecision{}, errors.New("continuation instruction is empty or oversized")
		}
	default:
		return ClarificationDecision{}, errors.New("clarification decision kind is invalid")
	}
	return decision, nil
}
