package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const tasksCreateClientTimeout = 15 * time.Second

func isTasksOnlineGameCreateSubcommand(args []string) bool {
	args = removeTaskFlag(args, "--compact")
	if len(args) == 0 || !strings.EqualFold(strings.TrimSpace(args[0]), "create") {
		return false
	}
	for index := 1; index < len(args); index++ {
		argument := strings.ToLower(strings.TrimSpace(args[index]))
		if argument == "--game-id" || argument == "--game-personas" || argument == "--game-turns" || argument == "--game-mode" {
			return true
		}
		if argument == "--route" && index+1 < len(args) && strings.EqualFold(strings.TrimSpace(args[index+1]), string(domaintask.RouteGAME)) {
			return true
		}
	}
	return false
}

func cmdTasksCreateGame() {
	args := removeTaskFlag(os.Args[2:], "--compact")
	requestID, args, err := parseTaskCreateRequestID(args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cliExitUsage)
	}
	draft, shared, jsonOut, err := parseTasksCreateArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cliExitUsage)
	}
	if draft.Route != domaintask.RouteGAME || shared.GameSpec == nil {
		fmt.Fprintln(os.Stderr, "online Task creation requires a typed GAME Task")
		os.Exit(cliExitUsage)
	}
	if requestID == "" {
		requestID = modulecore.NewRequestID()
	}
	// Print before sending so an interrupted request can be retried with this
	// exact key instead of silently generating a second Task.
	fmt.Fprintf(os.Stderr, "GAME Task create request_id=%s; reuse with --request-id %s if the response is interrupted\n", requestID, requestID)
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	if !cfg.LocalAgentOps.Enabled {
		fmt.Fprintln(os.Stderr, "failed to create GAME Task: local_agent_ops is disabled")
		os.Exit(1)
	}
	token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read local Agent OPS token: %v\n", err)
		os.Exit(1)
	}
	request := taskCreateRequestFromCLI(requestID, draft, shared)
	receipt, err := runTaskCreateRequest(context.Background(), cfg, token, &http.Client{Timeout: tasksCreateClientTimeout}, request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create GAME Task (request_id=%s): %v\n", requestID, err)
		os.Exit(1)
	}
	if jsonOut {
		writeJSONCLI(os.Stdout, map[string]any{"request_id": receipt.RequestID, "task_id": receipt.TaskID, "task": receipt.Task}, !hasFlag(os.Args[2:], "--compact"))
		return
	}
	fmt.Fprintf(os.Stdout, "created %s\n", receipt.TaskID)
}

func parseTaskCreateRequestID(args []string) (modulecore.RequestID, []string, error) {
	var requestID modulecore.RequestID
	filtered := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		if !strings.EqualFold(strings.TrimSpace(args[index]), "--request-id") {
			filtered = append(filtered, args[index])
			continue
		}
		if requestID != "" || index+1 >= len(args) {
			return "", nil, errors.New("--request-id must be provided once with a value")
		}
		requestID = modulecore.RequestID(strings.TrimSpace(args[index+1]))
		if err := requestID.Validate(); err != nil {
			return "", nil, fmt.Errorf("--request-id is invalid: %w", err)
		}
		index++
	}
	return requestID, filtered, nil
}

func taskCreateRequestFromCLI(requestID modulecore.RequestID, draft domaintask.Task, shared domaintask.SharedRoleContext) taskCreateRequest {
	return taskCreateRequest{
		RequestID: requestID, Title: draft.Title, Route: draft.Route, OwnerID: draft.OwnerID, Assignee: draft.Assignee,
		Priority: draft.Priority, Plan: shared.CurrentPlan, ModuleID: draft.ModuleID, ModuleRoot: draft.ModuleRoot,
		ReadOnly: draft.ReadOnly, ParentTaskID: draft.ParentTaskID, DependencyTaskIDs: append([]modulecore.TaskID(nil), draft.DependencyTaskIDs...),
		OriginSessionID: draft.OriginSessionID, OriginThreadID: draft.OriginThreadID, OriginTurnID: draft.OriginTurnID,
		OriginMessageID: draft.OriginMessageID, WorkstreamID: draft.WorkstreamID, GoalID: draft.GoalID,
		SupersedesTaskID: draft.SupersedesTaskID, GameSpec: shared.GameSpec,
	}
}

func runTaskCreateRequest(ctx context.Context, cfg *config.Config, token []byte, client *http.Client, request taskCreateRequest) (taskCreateReceipt, error) {
	var empty taskCreateReceipt
	if err := request.RequestID.Validate(); err != nil {
		return empty, fmt.Errorf("request_id is invalid: %w", err)
	}
	if ctx == nil || cfg == nil || !cfg.LocalAgentOps.Enabled || len(token) == 0 {
		return empty, errors.New("local authenticated Task create ingress is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return empty, errors.New("configured CORE port is invalid")
	}
	if client == nil {
		client = &http.Client{Timeout: tasksCreateClientTimeout}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	encoded, err := json.Marshal(request)
	if err != nil {
		return empty, fmt.Errorf("encode GAME Task create request: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, tasksCreateClientTimeout)
	defer cancel()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Server.Port, tasksCreatePath)
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return empty, fmt.Errorf("create CORE Task request: %w", err)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+string(token))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	response, err := clientCopy.Do(httpRequest)
	if err != nil {
		return empty, fmt.Errorf("reach CORE Task create ingress on 127.0.0.1:%d: %w", cfg.Server.Port, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var apiError agentOpsErrorResponse
		_ = json.NewDecoder(io.LimitReader(response.Body, 8<<10)).Decode(&apiError)
		if apiError.Error != "" {
			return empty, fmt.Errorf("CORE Task create failed (HTTP %d): %s", response.StatusCode, apiError.Error)
		}
		return empty, fmt.Errorf("CORE Task create failed (HTTP %d)", response.StatusCode)
	}
	var receipt taskCreateReceipt
	if err := decodeTaskCreateResponse(response.Body, &receipt); err != nil {
		return empty, fmt.Errorf("CORE Task create returned an invalid receipt: %w", err)
	}
	if receipt.RequestID != request.RequestID || receipt.TaskID.Validate() != nil || receipt.Task.TaskID != receipt.TaskID ||
		receipt.Task.Route != domaintask.RouteGAME || receipt.Task.OwnerID != strings.TrimSpace(cfg.LocalAgentOps.UserID) ||
		receipt.Task.Title != strings.TrimSpace(request.Title) || receipt.Task.Assignee != normalizedGameTaskAssignee(request.Assignee) {
		return empty, errors.New("CORE Task create returned a mismatched receipt")
	}
	return receipt, nil
}

func normalizedGameTaskAssignee(value string) string {
	if assignee := strings.TrimSpace(value); assignee != "" {
		return assignee
	}
	return "mio"
}

func decodeTaskCreateResponse(reader io.Reader, receipt *taskCreateReceipt) error {
	if receipt == nil {
		return errors.New("receipt target is nil")
	}
	encoded, err := io.ReadAll(io.LimitReader(reader, tasksCreateResponseBytes+1))
	if err != nil {
		return err
	}
	if len(encoded) > tasksCreateResponseBytes {
		return errors.New("Task create response exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(receipt); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON response values")
		}
		return err
	}
	return nil
}
