package conversation

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	MaxEnvelopeBytes      = 64 * 1024
	MaxUserMessageBytes   = 16 * 1024
	MaxInstructionBytes   = 16 * 1024
	MaxSourceMessageIDs   = 32
	emissionSchemaVersion = "mio.turn.v1"
)

var (
	errInvalidEmission = errors.New("invalid agent emission")
	errInvalidScope    = errors.New("invalid conversation scope")
)

type ConversationPurpose string

const (
	PurposeMioTurn              ConversationPurpose = "mio_turn"
	PurposeReadback             ConversationPurpose = "readback"
	PurposeReportReply          ConversationPurpose = "report_reply"
	PurposeClarificationControl ConversationPurpose = "clarification_control"
	PurposeClarificationReply   ConversationPurpose = "clarification_reply"
)

type ConversationScope struct {
	TurnID        core.TurnID
	SessionID     core.SessionID
	TraceID       core.TraceID
	ActorID       string
	InputID       core.MessageID
	GenerationID  string
	Purpose       ConversationPurpose
	PolicyVersion string
}

type EmissionKind string

const (
	EmissionKindReply    EmissionKind = "reply"
	EmissionKindDelegate EmissionKind = "delegate"
)

type AgentEmission struct {
	SchemaVersion string           `json:"schema_version"`
	Kind          EmissionKind     `json:"kind"`
	UserMessage   AgentUserMessage `json:"user_message"`
	Delegation    *AgentDelegation `json:"delegation,omitempty"`
}

type AgentUserMessage struct {
	To   string `json:"to"`
	Text string `json:"text"`
}

type AgentDelegation struct {
	To               string            `json:"to"`
	Goal             string            `json:"goal"`
	Instruction      string            `json:"instruction"`
	Constraints      []AgentConstraint `json:"constraints"`
	ExpectedOutput   string            `json:"expected_output"`
	SourceMessageIDs []core.MessageID  `json:"source_message_ids"`
}

type AgentConstraint struct {
	Key              string           `json:"key"`
	Text             string           `json:"text"`
	SourceMessageIDs []core.MessageID `json:"source_message_ids"`
}

type EmissionAllowlist struct {
	AllowedAssignees        []string
	AllowedSourceMessageIDs []core.MessageID
}

func (scope ConversationScope) Validate() error {
	if scope.TurnID.Validate() != nil || scope.SessionID.Validate() != nil || scope.TraceID.Validate() != nil || scope.InputID.Validate() != nil {
		return errInvalidScope
	}
	if strings.TrimSpace(scope.ActorID) == "" || strings.TrimSpace(scope.GenerationID) == "" || strings.TrimSpace(scope.PolicyVersion) == "" {
		return errInvalidScope
	}
	switch scope.Purpose {
	case PurposeMioTurn, PurposeReadback, PurposeReportReply, PurposeClarificationControl, PurposeClarificationReply:
		return nil
	default:
		return errInvalidScope
	}
}

func ParseAgentEmission(raw []byte, scope ConversationScope, allowlist EmissionAllowlist) (AgentEmission, error) {
	if len(raw) == 0 || len(raw) > MaxEnvelopeBytes || !utf8.Valid(raw) {
		return AgentEmission{}, errInvalidEmission
	}
	if err := scope.Validate(); err != nil {
		return AgentEmission{}, err
	}
	if scope.Purpose == PurposeClarificationReply {
		return AgentEmission{}, errInvalidEmission
	}
	if err := validateSingleJSONEnvelope(raw); err != nil {
		return AgentEmission{}, err
	}
	if err := validateRequiredFields(raw); err != nil {
		return AgentEmission{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var emission AgentEmission
	if err := decoder.Decode(&emission); err != nil {
		return AgentEmission{}, errInvalidEmission
	}
	if err := validateEmission(emission, scope.Purpose, allowlist); err != nil {
		return AgentEmission{}, err
	}
	return emission, nil
}

func validateSingleJSONEnvelope(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := scanJSONValue(decoder)
	if err != nil {
		return errInvalidEmission
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return errInvalidEmission
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errInvalidEmission
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) (json.Token, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return token, nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errInvalidEmission
			}
			if _, exists := keys[key]; exists {
				return nil, errInvalidEmission
			}
			keys[key] = struct{}{}
			if _, err := scanJSONValue(decoder); err != nil {
				return nil, err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, errInvalidEmission
		}
	case '[':
		for decoder.More() {
			if _, err := scanJSONValue(decoder); err != nil {
				return nil, err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return nil, errInvalidEmission
		}
	default:
		return nil, errInvalidEmission
	}
	return token, nil
}

func validateRequiredFields(raw []byte) error {
	root, err := strictObjectFields(raw, []string{"schema_version", "kind", "user_message"}, "schema_version", "kind", "user_message", "delegation")
	if err != nil {
		return errInvalidEmission
	}
	var kind EmissionKind
	if err := json.Unmarshal(root["kind"], &kind); err != nil {
		return errInvalidEmission
	}
	if kind == EmissionKindReply {
		if _, exists := root["delegation"]; exists {
			return errInvalidEmission
		}
	} else if kind == EmissionKindDelegate {
		delegationJSON, exists := root["delegation"]
		if !exists {
			return errInvalidEmission
		}
		delegation, err := strictObjectFields(delegationJSON,
			[]string{"to", "goal", "instruction", "constraints", "expected_output", "source_message_ids"},
			"to", "goal", "instruction", "constraints", "expected_output", "source_message_ids")
		if err != nil || !isJSONArray(delegation["constraints"]) || !isJSONArray(delegation["source_message_ids"]) {
			return errInvalidEmission
		}
		var constraints []json.RawMessage
		if err := json.Unmarshal(delegation["constraints"], &constraints); err != nil {
			return errInvalidEmission
		}
		for _, constraintJSON := range constraints {
			constraint, err := strictObjectFields(constraintJSON,
				[]string{"key", "text", "source_message_ids"}, "key", "text", "source_message_ids")
			if err != nil || !isJSONArray(constraint["source_message_ids"]) {
				return errInvalidEmission
			}
		}
	} else {
		return errInvalidEmission
	}
	userMessage, err := strictObjectFields(root["user_message"], []string{"to", "text"}, "to", "text")
	if err != nil {
		return errInvalidEmission
	}
	if _, err := jsonString(userMessage["to"]); err != nil {
		return errInvalidEmission
	}
	if _, err := jsonString(userMessage["text"]); err != nil {
		return errInvalidEmission
	}
	return nil
}

func strictObjectFields(raw json.RawMessage, required []string, allowed ...string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, errInvalidEmission
	}
	allowedFields := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedFields[key] = struct{}{}
	}
	for key := range fields {
		if _, exists := allowedFields[key]; !exists {
			return nil, errInvalidEmission
		}
	}
	for _, key := range required {
		if _, exists := fields[key]; !exists {
			return nil, errInvalidEmission
		}
	}
	return fields, nil
}

func isJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}

func jsonString(raw json.RawMessage) (string, error) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errInvalidEmission
	}
	return value, nil
}

func validateEmission(emission AgentEmission, purpose ConversationPurpose, allowlist EmissionAllowlist) error {
	if emission.SchemaVersion != emissionSchemaVersion || emission.UserMessage.To != "user" || strings.TrimSpace(emission.UserMessage.Text) == "" || len([]byte(emission.UserMessage.Text)) > MaxUserMessageBytes {
		return errInvalidEmission
	}
	if err := validateAllowlist(allowlist); err != nil {
		return err
	}
	switch emission.Kind {
	case EmissionKindReply:
		if emission.Delegation != nil {
			return errInvalidEmission
		}
		return nil
	case EmissionKindDelegate:
		if purpose == PurposeReportReply {
			return errInvalidEmission
		}
		if emission.Delegation == nil {
			return errInvalidEmission
		}
		return validateDelegation(*emission.Delegation, allowlist)
	default:
		return errInvalidEmission
	}
}

func validateAllowlist(allowlist EmissionAllowlist) error {
	for _, assignee := range allowlist.AllowedAssignees {
		if strings.TrimSpace(assignee) == "" {
			return errInvalidEmission
		}
	}
	for _, messageID := range allowlist.AllowedSourceMessageIDs {
		if messageID.Validate() != nil {
			return errInvalidEmission
		}
	}
	return nil
}

func validateDelegation(delegation AgentDelegation, allowlist EmissionAllowlist) error {
	assigneeAllowed := false
	for _, assignee := range allowlist.AllowedAssignees {
		if delegation.To == assignee {
			assigneeAllowed = true
			break
		}
	}
	if !assigneeAllowed || strings.TrimSpace(delegation.Goal) == "" || strings.TrimSpace(delegation.Instruction) == "" || strings.TrimSpace(delegation.ExpectedOutput) == "" || len([]byte(delegation.Instruction)) > MaxInstructionBytes {
		return errInvalidEmission
	}

	allowedSources := make(map[core.MessageID]struct{}, len(allowlist.AllowedSourceMessageIDs))
	for _, messageID := range allowlist.AllowedSourceMessageIDs {
		allowedSources[messageID] = struct{}{}
	}
	seenConstraintKeys := make(map[string]struct{}, len(delegation.Constraints))
	count := 0
	for _, messageID := range delegation.SourceMessageIDs {
		if !validAllowedSource(messageID, allowedSources) {
			return errInvalidEmission
		}
		count++
	}
	for _, constraint := range delegation.Constraints {
		if strings.TrimSpace(constraint.Key) == "" || strings.TrimSpace(constraint.Text) == "" {
			return errInvalidEmission
		}
		if _, exists := seenConstraintKeys[constraint.Key]; exists {
			return errInvalidEmission
		}
		seenConstraintKeys[constraint.Key] = struct{}{}
		for _, messageID := range constraint.SourceMessageIDs {
			if !validAllowedSource(messageID, allowedSources) {
				return errInvalidEmission
			}
			count++
		}
	}
	if count > MaxSourceMessageIDs {
		return errInvalidEmission
	}
	return nil
}

func validAllowedSource(messageID core.MessageID, allowed map[core.MessageID]struct{}) bool {
	if messageID.Validate() != nil {
		return false
	}
	_, exists := allowed[messageID]
	return exists
}
