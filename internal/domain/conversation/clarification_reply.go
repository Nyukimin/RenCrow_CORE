package conversation

import (
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const clarificationReplyInstructionSchemaVersion = "mio.clarification_reply.v1"

// ClarificationReplyGenerationContext binds Mio's instruction rewrite to the
// current private question and the handoff that owns it. The accepted public
// answer remains the final canonical user message in the prompt snapshot.
type ClarificationReplyGenerationContext struct {
	Handoff           HandoffEnvelope
	QuestionMessageID core.MessageID
	QuestionSender    string
	QuestionText      string
}

func (input ClarificationReplyGenerationContext) Validate(scope ConversationScope) error {
	if err := scope.Validate(); err != nil || scope.Purpose != PurposeClarificationReply {
		return errors.New("clarification reply generation scope is invalid")
	}
	if err := ValidateHandoffEnvelope(input.Handoff); err != nil {
		return errors.New("clarification reply handoff is invalid")
	}
	if input.Handoff.SessionID != scope.SessionID || input.Handoff.OriginTurnID == scope.TurnID ||
		input.Handoff.ToActor != input.QuestionSender || input.QuestionMessageID.Validate() != nil ||
		input.QuestionMessageID == scope.InputID || !utf8.ValidString(input.QuestionText) ||
		strings.TrimSpace(input.QuestionText) == "" || len([]byte(input.QuestionText)) > MaxReadbackTextBytes {
		return errors.New("clarification reply question does not match its stored handoff")
	}
	return nil
}

// ClarificationReplyInstruction is Mio's only permitted output for an
// explicit answer to a clarification prompt.
type ClarificationReplyInstruction struct {
	SchemaVersion string `json:"schema_version"`
	Instruction   string `json:"instruction"`
}

// ParseClarificationReplyInstruction accepts one strict instruction object.
func ParseClarificationReplyInstruction(raw []byte, scope ConversationScope) (ClarificationReplyInstruction, error) {
	if err := scope.Validate(); err != nil || scope.Purpose != PurposeClarificationReply ||
		len(raw) == 0 || len(raw) > MaxEnvelopeBytes || !utf8.Valid(raw) {
		return ClarificationReplyInstruction{}, errors.New("invalid clarification reply instruction input")
	}
	if err := validateSingleJSONEnvelope(raw); err != nil {
		return ClarificationReplyInstruction{}, errors.New("clarification reply instruction must be one JSON object")
	}
	fields, err := strictObjectFields(raw, []string{"schema_version", "instruction"}, "schema_version", "instruction")
	if err != nil {
		return ClarificationReplyInstruction{}, errors.New("clarification reply instruction fields are invalid")
	}
	version, err := jsonString(fields["schema_version"])
	if err != nil || version != clarificationReplyInstructionSchemaVersion {
		return ClarificationReplyInstruction{}, errors.New("clarification reply instruction schema_version is invalid")
	}
	instruction, err := jsonString(fields["instruction"])
	if err != nil || strings.TrimSpace(instruction) == "" || len([]byte(instruction)) > MaxInstructionBytes {
		return ClarificationReplyInstruction{}, errors.New("clarification reply instruction is empty or oversized")
	}
	var result ClarificationReplyInstruction
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return ClarificationReplyInstruction{}, errors.New("clarification reply instruction JSON is invalid")
	}
	return result, nil
}
