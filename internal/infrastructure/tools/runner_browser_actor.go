package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
	modulebrowser "github.com/Nyukimin/picoclaw_multiLLM/modules/browseractor"
)

type BrowserActorRunner interface {
	Run(ctx context.Context, req modulebrowser.RunRequest) (modulebrowser.RunResponse, error)
}

func (r *ToolRunner) WithBrowserActorRunner(runner BrowserActorRunner) *ToolRunner {
	r.config.BrowserActorRunner = runner
	r.registerTools()
	return r
}

func (r *ToolRunner) executeBrowserRunV2(ctx context.Context, args map[string]any) (*tool.ToolResponse, error) {
	if r.config.BrowserActorRunner == nil {
		return tool.NewError(tool.ErrInternalError, "browser.run is not configured", nil), nil
	}
	req, err := browserRunRequestFromArgs(args)
	if err != nil {
		return tool.NewError(tool.ErrValidationFailed, err.Error(), nil), nil
	}
	resp, err := r.config.BrowserActorRunner.Run(ctx, req)
	if err != nil {
		return tool.NewError(tool.ErrInternalError, err.Error(), nil), nil
	}
	if resp.Status != modulebrowser.StatusCompleted {
		code := tool.ErrInternalError
		message := "browser run failed"
		details := map[string]any{}
		if resp.Error != nil {
			message = resp.Error.Message
			details = resp.Error.Details
			switch resp.Error.Code {
			case modulebrowser.ErrValidationFailed:
				code = tool.ErrValidationFailed
			case modulebrowser.ErrPermissionDenied:
				code = tool.ErrPermissionDenied
			case modulebrowser.ErrTimeout:
				code = tool.ErrTimeout
			case modulebrowser.ErrNotFound:
				code = tool.ErrNotFound
			}
		}
		if details == nil {
			details = map[string]any{}
		}
		details["run_id"] = resp.RunID
		details["status"] = resp.Status
		details["risk_level"] = resp.RiskLevel
		return tool.NewError(code, message, details), nil
	}
	return tool.NewSuccess(resp), nil
}

func browserRunRequestFromArgs(args map[string]any) (modulebrowser.RunRequest, error) {
	req := modulebrowser.RunRequest{
		SchemaVersion:  modulebrowser.SchemaVersion,
		Headless:       true,
		TimeoutMS:      30000,
		MaxActions:     30,
		SaveTrace:      true,
		SaveScreenshot: true,
		MaskSecrets:    true,
	}
	if value, ok := args["run_id"].(string); ok {
		req.RunID = strings.TrimSpace(value)
	}
	if value, ok := args["goal"].(string); ok {
		req.Goal = strings.TrimSpace(value)
	}
	if value, ok := args["start_url"].(string); ok {
		req.StartURL = strings.TrimSpace(value)
	}
	if value, ok := args["profile_id"].(string); ok {
		req.ProfileID = strings.TrimSpace(value)
	}
	if value, ok := args["storage_state_path"].(string); ok {
		req.StorageStatePath = strings.TrimSpace(value)
	}
	if value, ok := args["headless"].(bool); ok {
		req.Headless = value
	}
	if value, ok := args["artifact_dir"].(string); ok {
		req.ArtifactDir = strings.TrimSpace(value)
	}
	if value, ok := int64Arg(args["timeout_ms"]); ok {
		req.TimeoutMS = int(value)
	}
	if value, ok := int64Arg(args["max_actions"]); ok {
		req.MaxActions = int(value)
	}
	if value, ok := args["save_trace"].(bool); ok {
		req.SaveTrace = value
	}
	if value, ok := args["save_screenshot"].(bool); ok {
		req.SaveScreenshot = value
	}
	if value, ok := args["mask_secrets"].(bool); ok {
		req.MaskSecrets = value
	}
	if raw, ok := args["allowed_origins"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				req.AllowedOrigins = append(req.AllowedOrigins, strings.TrimSpace(s))
			}
		}
	}
	if raw, ok := args["viewport"].(map[string]any); ok {
		if value, ok := int64Arg(raw["width"]); ok {
			req.Viewport.Width = int(value)
		}
		if value, ok := int64Arg(raw["height"]); ok {
			req.Viewport.Height = int(value)
		}
	}
	actions, err := browserActionsFromArg(args["actions"])
	if err != nil {
		return req, err
	}
	req.Actions = actions
	if err := modulebrowser.ValidateRunRequest(modulebrowser.NormalizeRunRequest(req)); err != nil {
		return req, err
	}
	return req, nil
}

func browserActionsFromArg(value any) ([]modulebrowser.Action, error) {
	raw, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("'actions' is required")
	}
	actions := make([]modulebrowser.Action, 0, len(raw))
	for i, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("actions[%d] must be object", i)
		}
		action := modulebrowser.Action{}
		if value, ok := m["type"].(string); ok {
			action.Type = strings.TrimSpace(value)
		}
		if value, ok := m["selector"].(string); ok {
			action.Selector = value
		}
		if value, ok := m["value"].(string); ok {
			action.Value = value
		}
		if value, ok := m["key"].(string); ok {
			action.Key = value
		}
		if value, ok := m["name"].(string); ok {
			action.Name = value
		}
		if value, ok := int64Arg(m["timeout_ms"]); ok {
			action.TimeoutMS = int(value)
		}
		actions = append(actions, action)
	}
	return actions, nil
}
