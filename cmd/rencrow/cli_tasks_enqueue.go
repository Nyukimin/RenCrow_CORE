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
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const tasksEnqueueClientTimeout = 15 * time.Second

func isTasksEnqueueSubcommand(args []string) bool {
	clean := removeTaskFlag(args, "--compact")
	return len(clean) > 0 && strings.EqualFold(strings.TrimSpace(clean[0]), "enqueue")
}

func parseTasksEnqueueArgs(args []string) (modulecore.TaskID, bool, error) {
	compact := hasFlag(args, "--compact")
	args = removeTaskFlag(args, "--compact")
	if len(args) != 2 || !strings.EqualFold(strings.TrimSpace(args[0]), "enqueue") {
		return "", compact, errors.New(tasksUsageEnqueue)
	}
	taskID, err := parseTaskIDArg(args, 1, tasksUsageEnqueue)
	return taskID, compact, err
}

func cmdTasksEnqueue() {
	taskID, compact, err := parseTasksEnqueueArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(cliExitUsage)
	}
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	if !cfg.LocalAgentOps.Enabled {
		fmt.Fprintln(os.Stderr, "failed to enqueue Task: local_agent_ops is disabled")
		os.Exit(1)
	}
	token, err := readAgentOpsToken(cfg.LocalAgentOps.AuthTokenFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read local Agent OPS token: %v\n", err)
		os.Exit(1)
	}
	if code := runTasksEnqueueRequest(taskID, compact, cfg, token, &http.Client{Timeout: tasksEnqueueClientTimeout}, os.Stdout, os.Stderr); code != 0 {
		os.Exit(code)
	}
}

func runTasksEnqueueRequest(taskID modulecore.TaskID, compact bool, cfg *config.Config, token []byte, client *http.Client, out, errOut io.Writer) int {
	if err := taskID.Validate(); err != nil {
		fmt.Fprintf(errOut, "invalid task_id: %v\n", err)
		return cliExitUsage
	}
	if cfg == nil || !cfg.LocalAgentOps.Enabled {
		fmt.Fprintln(errOut, "failed to enqueue Task: local_agent_ops is disabled")
		return 1
	}
	if len(token) == 0 {
		fmt.Fprintln(errOut, "failed to enqueue Task: local Agent OPS token is empty")
		return 1
	}
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		fmt.Fprintln(errOut, "failed to enqueue Task: configured CORE port is invalid")
		return 1
	}
	if client == nil {
		client = &http.Client{Timeout: tasksEnqueueClientTimeout}
	}
	clientCopy := *client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	client = &clientCopy
	encoded, err := json.Marshal(taskEnqueueRequest{TaskID: taskID})
	if err != nil {
		fmt.Fprintf(errOut, "failed to encode Task enqueue request: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), tasksEnqueueClientTimeout)
	defer cancel()
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", cfg.Server.Port, tasksEnqueuePath)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		fmt.Fprintf(errOut, "failed to create CORE Task enqueue request: %v\n", err)
		return 1
	}
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := client.Do(request)
	if err != nil {
		fmt.Fprintf(errOut, "failed to reach CORE Task enqueue ingress on 127.0.0.1:%d: %v\n", cfg.Server.Port, err)
		return 1
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		var apiError agentOpsErrorResponse
		_ = json.NewDecoder(io.LimitReader(response.Body, 8<<10)).Decode(&apiError)
		if apiError.Error != "" {
			fmt.Fprintf(errOut, "CORE Task enqueue failed (HTTP %d): %s\n", response.StatusCode, apiError.Error)
		} else {
			fmt.Fprintf(errOut, "CORE Task enqueue failed (HTTP %d)\n", response.StatusCode)
		}
		return 1
	}
	var receipt taskEnqueueReceipt
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		fmt.Fprintf(errOut, "CORE Task enqueue returned an invalid receipt: %v\n", err)
		return 1
	}
	if receipt.TaskID != taskID || receipt.QueueItemID.Validate() != nil || receipt.QueueStatus == "" ||
		(receipt.Status != "enqueued" && receipt.Status != "existing") || receipt.CreatedAt.IsZero() {
		fmt.Fprintln(errOut, "CORE Task enqueue returned an incomplete receipt")
		return 1
	}
	writeJSONCLI(out, receipt, !compact)
	return 0
}
