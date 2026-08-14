package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const (
	dataWriteToolName                                = "data.write"
	dataWriteUnavailableErrorCode     tool.ErrorCode = "UNAVAILABLE"
	dataWriteMaxPayloadBytes                         = 64 * 1024
	dataWriteReceiptRecallInstruction                = "follow-up data.recall queryにはaudit_refだけを使い、request_id/idempotency_keyは内部相関用でモデルから参照しない"
)

var errDataWriteInvalidRequest = errors.New("data write request is invalid")

var dataWriteForbiddenPayloadKeys = map[string]struct{}{
	"actor":                 {},
	"actor_id":              {},
	"agent":                 {},
	"agent_id":              {},
	"user":                  {},
	"user_id":               {},
	"authenticated_user_id": {},
	"role":                  {},
	"purpose":               {},
	"scope":                 {},
	"data_scope":            {},
	"request_id":            {},
	"path":                  {},
	"db":                    {},
	"database":              {},
	"sql":                   {},
	"table":                 {},
	"column":                {},
}

// DataWriteRequest is the complete model-controlled request sent to an
// operational write provider. Authentication, actor and data scope are
// carried only by the trusted ToolExecutionScope context.
type DataWriteRequest struct {
	Store     string
	Operation string
	Payload   map[string]any
}

func (r *ToolRunner) registerDataWriteTool() {
	r.toolsV2[dataWriteToolName] = r.executeDataWriteV2
}

func dataWriteMetadata() tool.ToolMetadata {
	return tool.ToolMetadata{
		ToolID:      dataWriteToolName,
		Version:     "1.0.0",
		Category:    "mutation",
		Origin:      tool.OriginCoreRuntime,
		Description: "Workerが認証済み実行scope内の名前付き運用データを書き込む。" + dataWriteReceiptRecallInstruction + "。",
		Invariants: []string{
			"trusted ToolExecutionScope is required and must identify an Agent",
			"tool arguments never select actor, user, scope, request, path, database, or SQL",
			"the provider owns per-store schema, migration, validation, audit, idempotency and operation policy",
			dataWriteReceiptRecallInstruction,
		},
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"store":     map[string]any{"type": "string", "minLength": 1},
				"operation": map[string]any{"type": "string", "minLength": 1},
				"payload":   map[string]any{"type": "object"},
			},
			"required": []any{"store", "operation", "payload"},
		},
	}
}

func (r *ToolRunner) executeDataWriteV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found {
		return tool.NewError(tool.ErrValidationFailed, "trusted Tool execution scope is missing", nil), nil
	}
	if err := scope.Validate(); err != nil || scope.ActorKind != tool.ActorKindAgent {
		return tool.NewError(tool.ErrValidationFailed, "trusted Tool execution scope is invalid", nil), nil
	}
	if r.config.OperationalDataWrite == nil {
		return tool.NewError(dataWriteUnavailableErrorCode, "operational data write is unavailable", nil), nil
	}

	request, err := normalizeDataWriteArguments(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, "data.write request is invalid", nil), nil
	}
	result, err := r.config.OperationalDataWrite.Write(ctx, request)
	if err != nil {
		return tool.NewError(dataWriteUnavailableErrorCode, "operational data write is unavailable", nil), nil
	}
	return tool.NewSuccess(result), nil
}

func normalizeDataWriteArguments(args map[string]any) (DataWriteRequest, error) {
	for key := range args {
		switch key {
		case "store", "operation", "payload":
		default:
			return DataWriteRequest{}, errDataWriteInvalidRequest
		}
	}
	store, ok := dataWriteStringArgument(args, "store")
	if !ok || store == "" {
		return DataWriteRequest{}, errDataWriteInvalidRequest
	}
	operation, ok := dataWriteStringArgument(args, "operation")
	if !ok || operation == "" {
		return DataWriteRequest{}, errDataWriteInvalidRequest
	}
	rawPayload, exists := args["payload"]
	if !exists {
		return DataWriteRequest{}, errDataWriteInvalidRequest
	}
	payload, ok := rawPayload.(map[string]any)
	if !ok || payload == nil {
		return DataWriteRequest{}, errDataWriteInvalidRequest
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil || len(payloadJSON) > dataWriteMaxPayloadBytes {
		return DataWriteRequest{}, errDataWriteInvalidRequest
	}
	if err := ValidateDataWritePayload(payload); err != nil {
		return DataWriteRequest{}, errDataWriteInvalidRequest
	}
	return DataWriteRequest{Store: store, Operation: operation, Payload: payload}, nil
}

func dataWriteStringArgument(args map[string]any, key string) (string, bool) {
	raw, exists := args[key]
	if !exists {
		return "", false
	}
	value, ok := raw.(string)
	return strings.TrimSpace(value), ok
}

// ValidateDataWritePayload rejects identity, scope, raw-store and path keys
// at every object depth. Values are not inspected for guessed sensitive text;
// owner routes validate their own domain payload schema.
func ValidateDataWritePayload(payload any) error {
	value := reflect.ValueOf(payload)
	if !value.IsValid() || value.Kind() != reflect.Map || value.IsNil() || value.Type().Key().Kind() != reflect.String {
		return errDataWriteInvalidRequest
	}
	return validateDataWritePayloadValue(value)
}

func validateDataWritePayloadValue(value reflect.Value) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return validateDataWritePayloadValue(value.Elem())
	}
	switch value.Kind() {
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return nil
		}
		for _, key := range value.MapKeys() {
			if dataWritePayloadKeyForbidden(key.String()) {
				return errDataWriteInvalidRequest
			}
			if err := validateDataWritePayloadValue(value.MapIndex(key)); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for index := 0; index < value.Len(); index++ {
			if err := validateDataWritePayloadValue(value.Index(index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func dataWritePayloadKeyForbidden(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	if _, forbidden := dataWriteForbiddenPayloadKeys[normalized]; forbidden {
		return true
	}
	return strings.HasSuffix(normalized, "_path")
}
