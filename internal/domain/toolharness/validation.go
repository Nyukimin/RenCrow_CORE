package toolharness

import (
	"fmt"
	"strings"
)

func ValidateEvent(event Event) error {
	if strings.TrimSpace(event.EventID) == "" {
		return fmt.Errorf("tool harness event_id is required")
	}
	if strings.TrimSpace(event.ToolName) == "" {
		return fmt.Errorf("tool harness tool_name is required")
	}
	if strings.TrimSpace(event.RawInputHash) == "" {
		return fmt.Errorf("tool harness raw_input_hash is required")
	}
	if event.CreatedAt.IsZero() {
		return fmt.Errorf("tool harness created_at is required")
	}
	switch event.ValidationStatus {
	case ValidationStatusValid:
		if len(event.Repairs) > 0 || len(event.RelationDefaults) > 0 {
			return fmt.Errorf("tool harness valid event must not include repair evidence")
		}
	case ValidationStatusRepaired:
		if len(event.Repairs) == 0 && len(event.RelationDefaults) == 0 {
			return fmt.Errorf("tool harness repaired event requires repair evidence")
		}
	default:
		return fmt.Errorf("tool harness validation_status is invalid")
	}
	for _, repair := range event.Repairs {
		if strings.TrimSpace(repair.Type) == "" {
			return fmt.Errorf("tool harness repair type is required")
		}
	}
	for _, def := range event.RelationDefaults {
		if strings.TrimSpace(def.Field) == "" {
			return fmt.Errorf("tool harness relation default field is required")
		}
	}
	return nil
}
