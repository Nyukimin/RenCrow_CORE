package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	superagentapp "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const (
	gameTaskRequestTimeout = 5 * time.Second
	gameTaskReceiptMaxBody = 1 << 20
	gameTaskPollDelay      = 5 * time.Second
)

var errGameTaskReceiptNotFound = errors.New("GAMES launch receipt is not available yet")

type gameTaskLaunchClient struct {
	baseURL string
	client  *http.Client
}

func newGameTaskLaunchClient(observerBaseURL string) (*gameTaskLaunchClient, error) {
	baseURL, err := parseGameObserverSessionsBaseURL(observerBaseURL)
	if err != nil {
		return nil, fmt.Errorf("GAMES launch URL is invalid: %w", err)
	}
	return &gameTaskLaunchClient{baseURL: baseURL, client: &http.Client{Timeout: gameTaskRequestTimeout}}, nil
}

type gameTaskLaunchRequest struct {
	LaunchKey string   `json:"launch_key"`
	GameID    string   `json:"game_id"`
	Personas  []string `json:"personas"`
	Turns     int      `json:"turns"`
	Mode      string   `json:"mode"`
}

type gameTaskLaunchResponse struct {
	OK        bool   `json:"ok"`
	GameID    string `json:"game_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type gameTaskReceiptResponse struct {
	LaunchKey  string              `json:"launch_key"`
	SessionID  string              `json:"session_id"`
	Status     string              `json:"status"`
	Request    domaintask.GameSpec `json:"request"`
	Result     json.RawMessage     `json:"result"`
	Summary    json.RawMessage     `json:"summary"`
	GameResult json.RawMessage     `json:"game_result"`
	Error      *string             `json:"error"`
	CreatedAt  time.Time           `json:"created_at"`
	UpdatedAt  time.Time           `json:"updated_at"`
}

func (c *gameTaskLaunchClient) launch(ctx context.Context, key string, spec domaintask.GameSpec) error {
	if c == nil || c.client == nil || c.baseURL == "" {
		return errors.New("GAMES launch client is unavailable")
	}
	body, err := json.Marshal(gameTaskLaunchRequest{LaunchKey: key, GameID: spec.GameID, Personas: append([]string(nil), spec.Personas...), Turns: spec.Turns, Mode: spec.Mode})
	if err != nil {
		return fmt.Errorf("encode GAMES launch request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/games/launch", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create GAMES launch request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return errors.New("GAMES launch request failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("GAMES launch returned HTTP %d", response.StatusCode)
	}
	var accepted gameTaskLaunchResponse
	if err := decodeGameTaskJSON(response.Body, gameTaskReceiptMaxBody, &accepted); err != nil {
		return fmt.Errorf("decode GAMES launch response: %w", err)
	}
	if !accepted.OK || accepted.GameID != spec.GameID || accepted.SessionID == "" || strings.TrimSpace(accepted.SessionID) != accepted.SessionID {
		return errors.New("GAMES launch response identity is incomplete")
	}
	return nil
}

func (c *gameTaskLaunchClient) receipt(ctx context.Context, key string) (domaintask.GameLaunchReceipt, error) {
	if c == nil || c.client == nil || c.baseURL == "" || strings.TrimSpace(key) == "" || strings.TrimSpace(key) != key {
		return domaintask.GameLaunchReceipt{}, errors.New("GAMES receipt request identity is invalid")
	}
	requestURL := c.baseURL + "/games/launches/" + url.PathEscape(key)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return domaintask.GameLaunchReceipt{}, fmt.Errorf("create GAMES receipt request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return domaintask.GameLaunchReceipt{}, errors.New("GAMES receipt request failed")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return domaintask.GameLaunchReceipt{}, errGameTaskReceiptNotFound
	}
	if response.StatusCode != http.StatusOK {
		return domaintask.GameLaunchReceipt{}, fmt.Errorf("GAMES receipt returned HTTP %d", response.StatusCode)
	}
	var payload gameTaskReceiptResponse
	if err := decodeGameTaskJSON(response.Body, gameTaskReceiptMaxBody, &payload); err != nil {
		return domaintask.GameLaunchReceipt{}, fmt.Errorf("decode GAMES receipt: %w", err)
	}
	result, err := decodeOptionalGameTaskJSON[domaintask.GameLaunchResult](payload.Result)
	if err != nil {
		return domaintask.GameLaunchReceipt{}, fmt.Errorf("decode GAMES launch result: %w", err)
	}
	summaryJSON, err := canonicalOptionalJSONObject(payload.Summary)
	if err != nil {
		return domaintask.GameLaunchReceipt{}, fmt.Errorf("decode GAMES observer summary: %w", err)
	}
	gameResultJSON, err := canonicalOptionalJSONObject(payload.GameResult)
	if err != nil {
		return domaintask.GameLaunchReceipt{}, fmt.Errorf("decode GAMES title outcome: %w", err)
	}
	var errorText string
	if payload.Error != nil {
		errorText = *payload.Error
	}
	return domaintask.GameLaunchReceipt{
		LaunchKey: payload.LaunchKey, SessionID: payload.SessionID,
		Status: domaintask.GameExecutionStatus(payload.Status), Request: payload.Request,
		Result: result, SummaryJSON: summaryJSON, GameResultJSON: gameResultJSON, Error: errorText,
		CreatedAt: payload.CreatedAt, UpdatedAt: payload.UpdatedAt,
	}, nil
}

func decodeGameTaskJSON(reader io.Reader, limit int64, target any) error {
	body, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return errors.New("JSON response exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeOptionalGameTaskJSON[T any](raw json.RawMessage) (*T, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var result T
	if err := decodeGameTaskJSON(bytes.NewReader(raw), gameTaskReceiptMaxBody, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func canonicalOptionalJSONObject(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return "", errors.New("value must be a JSON object or null")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

type gameTaskRunQueueRunner struct {
	tasks     *taskmanager.Manager
	client    *gameTaskLaunchClient
	admission *gameTaskRuntimeAdmission
}

func newGameTaskRunQueueRunner(tasks *taskmanager.Manager, observerBaseURL string, admission *gameTaskRuntimeAdmission) (*gameTaskRunQueueRunner, error) {
	client, err := newGameTaskLaunchClient(observerBaseURL)
	if err != nil {
		return nil, err
	}
	if tasks == nil {
		return nil, errors.New("GAME Task Manager is unavailable")
	}
	if admission == nil {
		return nil, errors.New("GAME Task admission is unavailable")
	}
	return &gameTaskRunQueueRunner{tasks: tasks, client: client, admission: admission}, nil
}

func (runner *gameTaskRunQueueRunner) process(ctx context.Context, item domainsuperagent.RunQueueItem) (string, error) {
	if runner == nil || runner.tasks == nil || runner.client == nil || !domainsuperagent.IsGameTaskQueueItem(item) {
		return "", errors.New("GAME Task controller is unavailable or its queue intent is invalid")
	}
	state, err := runner.tasks.GetGameTaskState(ctx, item.TaskID)
	if err != nil {
		return "", fmt.Errorf("load GAME Task controller checkpoint: %w", err)
	}
	if state.Task.Status != domaintask.StatusRunning || state.Progress.ControllerRunID != item.RunID || state.Progress.ControllerQueueItemID != item.QueueItemID {
		return "", errors.New("GAME Task controller Run is not bound to its durable queue item")
	}
	if state.Progress.Status == domaintask.GameExecutionPrepared {
		reserved, err := runner.admission.TryReserveTaskGroup(ctx, state.Progress.LaunchKey, state.Spec)
		if err != nil {
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		if !reserved {
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		state, _, err = runner.tasks.BeginGameLaunch(ctx, item.TaskID)
		if err != nil {
			latest, readErr := runner.tasks.GetGameTaskState(ctx, item.TaskID)
			if readErr == nil && latest.Progress.Status == domaintask.GameExecutionPrepared {
				if releaseErr := runner.admission.ReleaseGameGroup(state.Progress.LaunchKey); releaseErr != nil {
					return "", fmt.Errorf("persist GAME launch intent: %v; release unlaunched actor reservation: %w", err, releaseErr)
				}
			}
			// Keep the exact controller intent resumable when the transaction result
			// is uncertain or the store could not be reread. If the fresh read proved
			// it was still prepared, the unused provisional group was released above.
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		// The TaskID is the durable GAMES idempotency key. From this point on, a
		// resumed controller only reads that key's receipt and never re-POSTs.
		_ = runner.client.launch(ctx, state.Progress.LaunchKey, state.Spec)
	} else if state.Progress.Status != domaintask.GameExecutionLaunching && state.Progress.Status != domaintask.GameExecutionRunning && state.Progress.Status != domaintask.GameExecutionUnknown {
		return "", errors.New("GAME Task is not in a launchable or reconcilable state")
	}
	receipt, err := runner.client.receipt(ctx, state.Progress.LaunchKey)
	if err != nil {
		if _, markErr := runner.tasks.MarkGameLaunchUnknown(ctx, item.TaskID, err.Error()); markErr != nil {
			return "", errors.Join(err, markErr)
		}
		return runner.pause(ctx, item, state.Task.Assignee)
	}
	if err := receipt.Validate(state.Spec, state.Progress.LaunchKey); err != nil {
		if _, markErr := runner.tasks.MarkGameLaunchUnknown(ctx, item.TaskID, err.Error()); markErr != nil {
			return "", errors.Join(err, markErr)
		}
		return runner.pause(ctx, item, state.Task.Assignee)
	}
	switch receipt.Status {
	case domaintask.GameExecutionLaunching, domaintask.GameExecutionRunning, domaintask.GameExecutionUnknown:
		if _, err := runner.tasks.RecordGameLaunchReceipt(ctx, item.TaskID, receipt); err != nil {
			if _, markErr := runner.tasks.MarkGameLaunchUnknown(ctx, item.TaskID, err.Error()); markErr != nil {
				return "", errors.Join(err, markErr)
			}
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		if receipt.SessionID != "" {
			if err := runner.admission.BindGameGroup(state.Progress.LaunchKey, receipt.SessionID); err != nil {
				return runner.pause(ctx, item, state.Task.Assignee)
			}
		}
		return runner.pause(ctx, item, state.Task.Assignee)
	case domaintask.GameExecutionCompleted:
		summary := gameTaskTerminalSummary(state.Spec, receipt)
		if err := runner.admission.BindGameGroup(state.Progress.LaunchKey, receipt.SessionID); err != nil {
			if _, markErr := runner.tasks.MarkGameLaunchUnknown(ctx, item.TaskID, err.Error()); markErr != nil {
				return "", errors.Join(err, markErr)
			}
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		if err := runner.admission.MarkGameGroupClosing(state.Progress.LaunchKey, receipt.SessionID); err != nil {
			if _, markErr := runner.tasks.MarkGameLaunchUnknown(ctx, item.TaskID, err.Error()); markErr != nil {
				return "", errors.Join(err, markErr)
			}
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		if _, err := runner.tasks.FinalizeGameTaskRun(ctx, item.TaskID, item.RunID, state.Task.Assignee, receipt, summary); err != nil {
			if _, markErr := runner.tasks.MarkGameLaunchUnknown(ctx, item.TaskID, err.Error()); markErr != nil {
				return "", errors.Join(err, markErr)
			}
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		if err := runner.admission.ReleaseGameGroup(state.Progress.LaunchKey); err != nil {
			return "", fmt.Errorf("release terminal GAME Task actor group: %w", err)
		}
		return summary, nil
	case domaintask.GameExecutionFailed:
		summary := gameTaskTerminalSummary(state.Spec, receipt)
		if receipt.Error != "" {
			summary = boundedGameTaskSummary(summary + "; error: " + receipt.Error)
		}
		if err := runner.admission.BindGameGroup(state.Progress.LaunchKey, receipt.SessionID); err != nil {
			if _, markErr := runner.tasks.MarkGameLaunchUnknown(ctx, item.TaskID, err.Error()); markErr != nil {
				return "", errors.Join(err, markErr)
			}
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		if err := runner.admission.MarkGameGroupClosing(state.Progress.LaunchKey, receipt.SessionID); err != nil {
			if _, markErr := runner.tasks.MarkGameLaunchUnknown(ctx, item.TaskID, err.Error()); markErr != nil {
				return "", errors.Join(err, markErr)
			}
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		if _, err := runner.tasks.FinalizeGameTaskRun(ctx, item.TaskID, item.RunID, state.Task.Assignee, receipt, summary); err != nil {
			if _, markErr := runner.tasks.MarkGameLaunchUnknown(ctx, item.TaskID, err.Error()); markErr != nil {
				return "", errors.Join(err, markErr)
			}
			return runner.pause(ctx, item, state.Task.Assignee)
		}
		if err := runner.admission.ReleaseGameGroup(state.Progress.LaunchKey); err != nil {
			return "", fmt.Errorf("release terminal GAME Task actor group: %w", err)
		}
		return "", errors.New("GAMES reported a failed game session")
	default:
		return "", errors.New("GAMES receipt has an unsupported terminal state")
	}
}

func (runner *gameTaskRunQueueRunner) pause(ctx context.Context, item domainsuperagent.RunQueueItem, actorID string) (string, error) {
	if err := runner.tasks.PauseGameTaskRun(ctx, item.TaskID, item.RunID, actorID); err != nil {
		return "", fmt.Errorf("pause GAME Task controller Run: %w", err)
	}
	return "", superagentapp.RunQueuePauseOutcome{}
}

func gameTaskTerminalSummary(spec domaintask.GameSpec, receipt domaintask.GameLaunchReceipt) string {
	actors := strings.Join(spec.Personas, ",")
	outcome := receipt.GameResultJSON
	if outcome == "" {
		outcome = "{}"
	}
	return boundedGameTaskSummary(fmt.Sprintf("%s session %s %s for [%s]; title outcome %s", spec.GameID, receipt.SessionID, receipt.Status, actors, outcome))
}

func boundedGameTaskSummary(summary string) string {
	const maxBytes = 64 << 10
	if len(summary) <= maxBytes {
		return summary
	}
	return summary[:maxBytes]
}
