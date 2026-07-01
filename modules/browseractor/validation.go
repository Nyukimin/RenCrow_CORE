package browseractor

import (
	"fmt"
	"regexp"
	"strings"
)

var runIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]+$`)

func NormalizeRunRequest(req RunRequest) RunRequest {
	if strings.TrimSpace(req.SchemaVersion) == "" {
		req.SchemaVersion = SchemaVersion
	}
	req.RunID = strings.TrimSpace(req.RunID)
	req.StartURL = strings.TrimSpace(req.StartURL)
	req.ProfileID = strings.TrimSpace(req.ProfileID)
	req.StorageStatePath = strings.TrimSpace(req.StorageStatePath)
	req.ArtifactDir = strings.TrimSpace(req.ArtifactDir)
	if req.Viewport.Width == 0 {
		req.Viewport.Width = 1366
	}
	if req.Viewport.Height == 0 {
		req.Viewport.Height = 900
	}
	if req.TimeoutMS == 0 {
		req.TimeoutMS = 30000
	}
	if req.MaxActions == 0 {
		req.MaxActions = 30
	}
	for i := range req.AllowedOrigins {
		req.AllowedOrigins[i] = strings.TrimSpace(req.AllowedOrigins[i])
	}
	return req
}

func ValidateRunRequest(req RunRequest) error {
	if strings.TrimSpace(req.SchemaVersion) != SchemaVersion {
		return fmt.Errorf("unsupported schema_version: %s", req.SchemaVersion)
	}
	if strings.TrimSpace(req.RunID) != "" && !runIDPattern.MatchString(req.RunID) {
		return fmt.Errorf("run_id must match ^[a-zA-Z0-9_.-]+$")
	}
	if strings.TrimSpace(req.StartURL) == "" {
		return fmt.Errorf("start_url is required")
	}
	if len(req.Actions) == 0 {
		return fmt.Errorf("actions is required")
	}
	if req.MaxActions < 1 || req.MaxActions > 100 {
		return fmt.Errorf("max_actions must be 1..100")
	}
	if len(req.Actions) > req.MaxActions {
		return fmt.Errorf("actions exceeds max_actions")
	}
	if req.TimeoutMS < 1 {
		return fmt.Errorf("timeout_ms must be positive")
	}
	for i, action := range req.Actions {
		if err := ValidateAction(action); err != nil {
			return fmt.Errorf("action %d: %w", i, err)
		}
	}
	return nil
}

func ValidateAction(action Action) error {
	switch strings.TrimSpace(action.Type) {
	case "open", "snapshot", "close":
		return nil
	case "wait_for_selector", "click", "extract_text":
		if strings.TrimSpace(action.Selector) == "" {
			return fmt.Errorf("selector is required")
		}
	case "fill":
		if strings.TrimSpace(action.Selector) == "" {
			return fmt.Errorf("selector is required")
		}
	case "press":
		if strings.TrimSpace(action.Key) == "" {
			return fmt.Errorf("key is required")
		}
	case "screenshot":
		name := strings.TrimSpace(action.Name)
		if name == "" {
			return nil
		}
		if !runIDPattern.MatchString(name) {
			return fmt.Errorf("screenshot name must be path-safe")
		}
	default:
		return fmt.Errorf("unsupported action: %s", action.Type)
	}
	if len(action.Selector) > 1000 {
		return fmt.Errorf("selector too long")
	}
	if len(action.Value) > 10000 {
		return fmt.Errorf("value too long")
	}
	return nil
}
