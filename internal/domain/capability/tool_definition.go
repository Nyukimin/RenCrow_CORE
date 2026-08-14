package capability

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// ParseToolDefinition validates a stored ToolEntry schema and returns the
// complete definition consumed by Agent tool injection and runtime metadata.
// Legacy entries containing only a parameter-schema object are wrapped with
// the trusted name and description from the entry.
func ParseToolDefinition(entry ToolEntry) (llm.ToolDefinition, error) {
	name := entry.Name
	description := entry.Description
	if name == "" || strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
		return llm.ToolDefinition{}, fmt.Errorf("trusted tool name is empty")
	}
	if description == "" || strings.TrimSpace(description) == "" || description != strings.TrimSpace(description) {
		return llm.ToolDefinition{}, fmt.Errorf("trusted tool description is empty")
	}

	var object map[string]json.RawMessage
	if err := json.Unmarshal([]byte(entry.SchemaJSON), &object); err != nil {
		return llm.ToolDefinition{}, err
	}
	if object == nil {
		return llm.ToolDefinition{}, fmt.Errorf("schema is not a JSON object")
	}
	typeJSON, hasType := toolSchemaField(object, "type")
	_, hasFunction := toolSchemaField(object, "function")
	var typeName string
	typeIsFunction := hasType && json.Unmarshal(typeJSON, &typeName) == nil && strings.EqualFold(typeName, "function")
	if hasFunction || typeIsFunction {
		if !hasType || !hasFunction {
			return llm.ToolDefinition{}, fmt.Errorf("partial tool definition")
		}
		var definition llm.ToolDefinition
		if err := json.Unmarshal([]byte(entry.SchemaJSON), &definition); err != nil {
			return llm.ToolDefinition{}, err
		}
		if definition.Type != "function" {
			return llm.ToolDefinition{}, fmt.Errorf("tool definition type is not function")
		}
		if definition.Function.Name != name {
			return llm.ToolDefinition{}, fmt.Errorf("tool definition name does not match registry name")
		}
		if strings.TrimSpace(definition.Function.Description) == "" || definition.Function.Description != description {
			return llm.ToolDefinition{}, fmt.Errorf("tool definition description is empty")
		}
		if definition.Function.Parameters == nil {
			return llm.ToolDefinition{}, fmt.Errorf("tool definition parameters are missing")
		}
		return definition, nil
	}

	var parameters map[string]any
	if err := json.Unmarshal([]byte(entry.SchemaJSON), &parameters); err != nil || parameters == nil {
		if err != nil {
			return llm.ToolDefinition{}, err
		}
		return llm.ToolDefinition{}, fmt.Errorf("legacy parameter schema is not an object")
	}
	return llm.ToolDefinition{
		Type: "function",
		Function: llm.ToolFunctionDef{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}, nil
}

func toolSchemaField(object map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	for key, value := range object {
		if strings.EqualFold(key, name) {
			return value, true
		}
	}
	return nil, false
}
