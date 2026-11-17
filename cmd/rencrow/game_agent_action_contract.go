package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

type gameActionContractSet struct {
	actions map[string]gameActionContract
}

type gameActionContract struct {
	target *gameActionFieldContract
	args   map[string]gameActionFieldContract
}

type gameActionFieldContract struct {
	typeName string
	required bool
	enum     []string
	enumSet  bool
	bounds   *gameCoordinateBounds
	min      *int64
	max      *int64
}

type gameCoordinateBounds struct {
	xMin int64
	xMax int64
	yMin int64
	yMax int64
}

// parseGameActionContracts distinguishes legacy requests (both metadata fields
// absent) from malformed or incomplete structured-action metadata.
func parseGameActionContracts(observation map[string]any) (*gameActionContractSet, bool, error) {
	rawContracts, hasContracts := observation["action_contracts"]
	rawLegalIDs, hasLegalIDs := observation["legal_ids"]
	if !hasContracts && !hasLegalIDs {
		return nil, false, nil
	}
	if !hasContracts || !hasLegalIDs {
		return nil, false, fmt.Errorf("action_contracts and legal_ids must be provided together")
	}
	contracts, ok := rawContracts.(map[string]any)
	if !ok || contracts == nil {
		return nil, false, fmt.Errorf("action_contracts must be an object")
	}
	legalIDs, ok := rawLegalIDs.(map[string]any)
	if !ok || legalIDs == nil {
		return nil, false, fmt.Errorf("legal_ids must be an object")
	}

	parsed := make(map[string]gameActionContract, len(contracts))
	for action, rawDescriptor := range contracts {
		contract, err := parseGameActionContract(action, rawDescriptor, legalIDs)
		if err != nil {
			return nil, false, err
		}
		parsed[action] = contract
	}
	return &gameActionContractSet{actions: parsed}, true, nil
}

func parseGameActionContract(action string, rawDescriptor any, legalIDs map[string]any) (gameActionContract, error) {
	descriptor, ok := rawDescriptor.(map[string]any)
	if !ok || descriptor == nil {
		return gameActionContract{}, fmt.Errorf("action_contracts.%s must be an object", action)
	}
	for name := range descriptor {
		if name != "target" && name != "args" {
			return gameActionContract{}, fmt.Errorf("action_contracts.%s has unsupported field %q", action, name)
		}
	}
	contract := gameActionContract{args: map[string]gameActionFieldContract{}}
	if rawTarget, exists := descriptor["target"]; exists {
		field, err := parseGameActionFieldContract(rawTarget, "action_contracts."+action+".target", legalIDs)
		if err != nil {
			return gameActionContract{}, err
		}
		if field.typeName != "string" {
			return gameActionContract{}, fmt.Errorf("action_contracts.%s.target must be a string because GameActionStep.Target is a string", action)
		}
		contract.target = &field
	}
	if rawArgs, exists := descriptor["args"]; exists {
		args, ok := rawArgs.(map[string]any)
		if !ok || args == nil {
			return gameActionContract{}, fmt.Errorf("action_contracts.%s.args must be an object", action)
		}
		for name, rawField := range args {
			field, err := parseGameActionFieldContract(rawField, "action_contracts."+action+".args."+name, legalIDs)
			if err != nil {
				return gameActionContract{}, err
			}
			contract.args[name] = field
		}
	}
	return contract, nil
}

func parseGameActionFieldContract(raw any, path string, legalIDs map[string]any) (gameActionFieldContract, error) {
	declaration, ok := raw.(map[string]any)
	if !ok || declaration == nil {
		return gameActionFieldContract{}, fmt.Errorf("%s must be an object", path)
	}
	allowed := map[string]bool{
		"type": true, "required": true, "enum_ref": true, "enum": true,
		"bounds_ref": true, "min": true, "max": true,
		"x_min": true, "x_max": true, "y_min": true, "y_max": true,
	}
	for name := range declaration {
		if !allowed[name] {
			return gameActionFieldContract{}, fmt.Errorf("%s has unsupported contract field %q", path, name)
		}
	}
	typeName, ok := declaration["type"].(string)
	if !ok || (typeName != "string" && typeName != "coordinate" && typeName != "integer") {
		return gameActionFieldContract{}, fmt.Errorf("%s.type must be string, coordinate, or integer", path)
	}
	required, ok := declaration["required"].(bool)
	if !ok {
		return gameActionFieldContract{}, fmt.Errorf("%s.required must be a boolean", path)
	}
	field := gameActionFieldContract{typeName: typeName, required: required}

	if rawRef, exists := declaration["enum_ref"]; exists {
		if typeName != "string" {
			return gameActionFieldContract{}, fmt.Errorf("%s.enum_ref is only valid for string fields", path)
		}
		ref, ok := rawRef.(string)
		if !ok || strings.TrimSpace(ref) == "" {
			return gameActionFieldContract{}, fmt.Errorf("%s.enum_ref must be a non-empty string", path)
		}
		values, exists := legalIDs[ref]
		if !exists {
			return gameActionFieldContract{}, fmt.Errorf("%s.enum_ref references missing legal_ids.%s", path, ref)
		}
		field.enum, ok = stringValues(values)
		if !ok {
			return gameActionFieldContract{}, fmt.Errorf("legal_ids.%s must be an array of strings", ref)
		}
		field.enumSet = true
	}
	if rawEnum, exists := declaration["enum"]; exists {
		if typeName != "string" || field.enumSet {
			return gameActionFieldContract{}, fmt.Errorf("%s.enum is only valid for a string field without enum_ref", path)
		}
		field.enum, ok = stringValues(rawEnum)
		if !ok {
			return gameActionFieldContract{}, fmt.Errorf("%s.enum must be an array of strings", path)
		}
		field.enumSet = true
	}

	if rawRef, exists := declaration["bounds_ref"]; exists {
		if typeName != "coordinate" {
			return gameActionFieldContract{}, fmt.Errorf("%s.bounds_ref is only valid for coordinate fields", path)
		}
		if hasAny(declaration, "x_min", "x_max", "y_min", "y_max") {
			return gameActionFieldContract{}, fmt.Errorf("%s cannot combine bounds_ref with inline coordinate bounds", path)
		}
		ref, ok := rawRef.(string)
		if !ok || strings.TrimSpace(ref) == "" {
			return gameActionFieldContract{}, fmt.Errorf("%s.bounds_ref must be a non-empty string", path)
		}
		bounds, exists := legalIDs[ref]
		if !exists {
			return gameActionFieldContract{}, fmt.Errorf("%s.bounds_ref references missing legal_ids.%s", path, ref)
		}
		field.bounds, ok = parseCoordinateBounds(bounds)
		if !ok {
			return gameActionFieldContract{}, fmt.Errorf("legal_ids.%s must define integer coordinate bounds", ref)
		}
	} else if hasAny(declaration, "x_min", "x_max", "y_min", "y_max") {
		if typeName != "coordinate" || !hasAll(declaration, "x_min", "x_max", "y_min", "y_max") {
			return gameActionFieldContract{}, fmt.Errorf("%s must declare all coordinate bounds on a coordinate field", path)
		}
		bounds, ok := parseCoordinateBounds(declaration)
		if !ok {
			return gameActionFieldContract{}, fmt.Errorf("%s has invalid coordinate bounds", path)
		}
		field.bounds = bounds
	}

	if typeName == "coordinate" && field.bounds == nil {
		return gameActionFieldContract{}, fmt.Errorf("%s must reference coordinate bounds", path)
	}
	if typeName != "integer" && hasAny(declaration, "min", "max") {
		return gameActionFieldContract{}, fmt.Errorf("%s.min/max are only valid for integer fields", path)
	}
	if typeName == "integer" {
		if rawMin, exists := declaration["min"]; exists {
			value, ok := integerValue(rawMin)
			if !ok {
				return gameActionFieldContract{}, fmt.Errorf("%s.min must be an integer", path)
			}
			field.min = &value
		}
		if rawMax, exists := declaration["max"]; exists {
			value, ok := integerValue(rawMax)
			if !ok {
				return gameActionFieldContract{}, fmt.Errorf("%s.max must be an integer", path)
			}
			field.max = &value
		}
		if field.min != nil && field.max != nil && *field.min > *field.max {
			return gameActionFieldContract{}, fmt.Errorf("%s.min must not exceed max", path)
		}
	}
	return field, nil
}

func (contracts *gameActionContractSet) validate(step viewer.GameActionStep) error {
	contract, exists := contracts.actions[step.Action]
	if !exists {
		return fmt.Errorf("action_contracts.%s is missing", step.Action)
	}
	if contract.target != nil {
		if contract.target.required && step.Target == "" {
			return fmt.Errorf("action_contracts.%s.target is required", step.Action)
		}
		if step.Target != "" {
			if err := contract.target.validate(step.Target, "target"); err != nil {
				return fmt.Errorf("action_contracts.%s: %w", step.Action, err)
			}
		}
	}
	for name, field := range contract.args {
		value, exists := step.Args[name]
		if !exists {
			if field.required {
				return fmt.Errorf("action_contracts.%s.args.%s is required", step.Action, name)
			}
			continue
		}
		if err := field.validate(value, "args."+name); err != nil {
			return fmt.Errorf("action_contracts.%s: %w", step.Action, err)
		}
	}
	return nil
}

func (field gameActionFieldContract) validate(value any, path string) error {
	switch field.typeName {
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("%s must be a string", path)
		}
		if field.enumSet && !containsGameActionContractValue(field.enum, text) {
			return fmt.Errorf("%s must be one of the values in its declared enum", path)
		}
	case "integer":
		integer, ok := integerValue(value)
		if !ok {
			return fmt.Errorf("%s must be an integer", path)
		}
		if field.min != nil && integer < *field.min {
			return fmt.Errorf("%s must be at least %d", path, *field.min)
		}
		if field.max != nil && integer > *field.max {
			return fmt.Errorf("%s must be at most %d", path, *field.max)
		}
	case "coordinate":
		coordinate, ok := value.(map[string]any)
		if !ok || coordinate == nil {
			return fmt.Errorf("%s must be a coordinate object", path)
		}
		x, xOK := integerValue(coordinate["x"])
		y, yOK := integerValue(coordinate["y"])
		if !xOK || !yOK {
			return fmt.Errorf("%s.x and %s.y must be integers", path, path)
		}
		if x < field.bounds.xMin || x > field.bounds.xMax || y < field.bounds.yMin || y > field.bounds.yMax {
			return fmt.Errorf("%s must be within its declared coordinate bounds", path)
		}
	}
	return nil
}

func parseCoordinateBounds(raw any) (*gameCoordinateBounds, bool) {
	values, ok := raw.(map[string]any)
	if !ok || values == nil {
		return nil, false
	}
	xMin, xMinOK := integerValue(values["x_min"])
	xMax, xMaxOK := integerValue(values["x_max"])
	yMin, yMinOK := integerValue(values["y_min"])
	yMax, yMaxOK := integerValue(values["y_max"])
	if !xMinOK || !xMaxOK || !yMinOK || !yMaxOK || xMin > xMax || yMin > yMax {
		return nil, false
	}
	return &gameCoordinateBounds{xMin: xMin, xMax: xMax, yMin: yMin, yMax: yMax}, true
}

func stringValues(raw any) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		out := make([]string, len(values))
		for i, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil, false
			}
			out[i] = text
		}
		return out, true
	default:
		return nil, false
	}
}

func integerValue(raw any) (int64, bool) {
	switch value := raw.(type) {
	case json.Number:
		parsed, err := value.Int64()
		return parsed, err == nil
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value < float64(math.MinInt64) || value >= float64(math.MaxInt64) {
			return 0, false
		}
		return int64(value), true
	default:
		return 0, false
	}
}

func containsGameActionContractValue(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func hasAny(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, exists := values[key]; exists {
			return true
		}
	}
	return false
}

func hasAll(values map[string]any, keys ...string) bool {
	for _, key := range keys {
		if _, exists := values[key]; !exists {
			return false
		}
	}
	return true
}
