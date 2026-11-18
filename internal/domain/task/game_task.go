package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	GameTaskWaitingReason = "game session pending"
	MaxGameTaskTurns      = 1_000_000
)

type GameExecutionStatus string

const (
	GameExecutionPrepared  GameExecutionStatus = "prepared"
	GameExecutionLaunching GameExecutionStatus = "launching"
	GameExecutionRunning   GameExecutionStatus = "running"
	GameExecutionUnknown   GameExecutionStatus = "unknown"
	GameExecutionCompleted GameExecutionStatus = "completed"
	GameExecutionFailed    GameExecutionStatus = "failed"
)

// GameSpec is the immutable, normalized request owned by a GAME Task.
type GameSpec struct {
	GameID   string   `json:"game_id"`
	Personas []string `json:"personas"`
	Turns    int      `json:"turns"`
	Mode     string   `json:"mode"`
}

// NormalizeGameSpec applies the canonical Task defaults and validates the
// supported player roster before any Task or game side effect is persisted.
func NormalizeGameSpec(spec GameSpec) (GameSpec, error) {
	spec.GameID = strings.TrimSpace(spec.GameID)
	spec.Mode = strings.TrimSpace(spec.Mode)
	spec.Personas = append([]string(nil), spec.Personas...)
	for i := range spec.Personas {
		spec.Personas[i] = strings.TrimSpace(spec.Personas[i])
	}
	wantPersonas := []string{"mio"}
	defaultTurns := 0
	wantMode := "auto"
	switch spec.GameID {
	case "nethack":
		defaultTurns = 60
	case "territory_commander":
		defaultTurns = 120
	case "survival_garden":
		defaultTurns = 120
		wantPersonas = []string{"aka", "ao", "kin", "gin"}
	case "herzog_zwei":
		defaultTurns = 40
		wantMode = "realtime"
		wantPersonas = []string{"aka", "ao", "kin", "gin"}
	default:
		return GameSpec{}, fmt.Errorf("unsupported game_id %q", spec.GameID)
	}
	if len(spec.Personas) == 0 {
		spec.Personas = append([]string(nil), wantPersonas...)
	}
	if !equalGamePersonas(spec.Personas, wantPersonas) {
		return GameSpec{}, fmt.Errorf("game %q requires ordered personas %q", spec.GameID, strings.Join(wantPersonas, ","))
	}
	if spec.Turns == 0 {
		spec.Turns = defaultTurns
	}
	if spec.Turns < 1 || spec.Turns > MaxGameTaskTurns {
		return GameSpec{}, fmt.Errorf("turns must be between 1 and %d", MaxGameTaskTurns)
	}
	if spec.Mode == "" {
		spec.Mode = wantMode
	}
	if spec.Mode != wantMode {
		return GameSpec{}, fmt.Errorf("game %q requires mode %q", spec.GameID, wantMode)
	}
	return spec, nil
}

func (spec GameSpec) Validate() error {
	normalized, err := NormalizeGameSpec(spec)
	if err != nil {
		return err
	}
	if normalized.GameID != spec.GameID || normalized.Mode != spec.Mode || normalized.Turns != spec.Turns || !equalGamePersonas(normalized.Personas, spec.Personas) {
		return errors.New("game spec is not in canonical normalized form")
	}
	return nil
}

func (spec GameSpec) Digest() string {
	encoded, err := json.Marshal(struct {
		Version  string   `json:"version"`
		GameID   string   `json:"game_id"`
		Personas []string `json:"personas"`
		Turns    int      `json:"turns"`
		Mode     string   `json:"mode"`
	}{"game-task.v1", spec.GameID, spec.Personas, spec.Turns, spec.Mode})
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func equalGamePersonas(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// GameProgress is the Task-owned durable launch/session receipt checkpoint.
// It is changed only through TaskManager game lifecycle methods.
type GameProgress struct {
	LaunchKey             string                 `json:"launch_key"`
	SpecDigest            string                 `json:"spec_digest"`
	ControllerRunID       modulecore.RunID       `json:"controller_run_id,omitempty"`
	ControllerQueueItemID modulecore.QueueItemID `json:"controller_queue_item_id,omitempty"`
	Status                GameExecutionStatus    `json:"status"`
	SessionID             string                 `json:"session_id,omitempty"`
	ResultOK              *bool                  `json:"result_ok,omitempty"`
	ResultGameID          string                 `json:"result_game_id,omitempty"`
	ResultSessionID       string                 `json:"result_session_id,omitempty"`
	ResultStatus          string                 `json:"result_status,omitempty"`
	Summary               string                 `json:"summary,omitempty"`
	GameResultJSON        string                 `json:"game_result_json,omitempty"`
	Error                 string                 `json:"error,omitempty"`
	CreatedAt             time.Time              `json:"created_at"`
	UpdatedAt             time.Time              `json:"updated_at"`
}

type GameLaunchResult struct {
	OK        bool   `json:"ok"`
	GameID    string `json:"game_id"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

// GameLaunchReceipt is the exact normalized receipt returned by the GAMES
// owner. SummaryJSON preserves its nullable summary without copying GAMES
// domain semantics into CORE.
type GameLaunchReceipt struct {
	LaunchKey      string              `json:"launch_key"`
	SessionID      string              `json:"session_id"`
	Status         GameExecutionStatus `json:"status"`
	Request        GameSpec            `json:"request"`
	Result         *GameLaunchResult   `json:"result,omitempty"`
	SummaryJSON    string              `json:"summary_json,omitempty"`
	GameResultJSON string              `json:"game_result_json,omitempty"`
	Error          string              `json:"error,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

func (receipt GameLaunchReceipt) Validate(spec GameSpec, launchKey string) error {
	if launchKey == "" || receipt.LaunchKey != launchKey {
		return errors.New("game launch receipt key does not match")
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("expected game spec is invalid: %w", err)
	}
	if err := receipt.Request.Validate(); err != nil || receipt.Request.Digest() != spec.Digest() {
		return errors.New("game launch receipt request does not match the Task spec")
	}
	switch receipt.Status {
	case GameExecutionLaunching, GameExecutionRunning, GameExecutionUnknown, GameExecutionCompleted, GameExecutionFailed:
	default:
		return fmt.Errorf("game launch receipt status %q is invalid", receipt.Status)
	}
	if receipt.SessionID != "" && (strings.TrimSpace(receipt.SessionID) != receipt.SessionID || len(receipt.SessionID) > 256) {
		return errors.New("game launch receipt session_id is invalid")
	}
	if (receipt.Status == GameExecutionLaunching || receipt.Status == GameExecutionRunning || receipt.Status == GameExecutionCompleted) && receipt.SessionID == "" {
		return errors.New("active or completed game launch receipt requires a session_id")
	}
	if receipt.Result != nil {
		if receipt.Result.GameID != spec.GameID || receipt.Result.SessionID == "" || receipt.Result.SessionID != receipt.SessionID || receipt.Result.Status == "" {
			return errors.New("game launch receipt result identity does not match")
		}
		if (receipt.Status == GameExecutionCompleted && (!receipt.Result.OK || receipt.Result.Status != string(GameExecutionCompleted))) ||
			(receipt.Status == GameExecutionFailed && (receipt.Result.OK || receipt.Result.Status != string(GameExecutionFailed))) {
			return errors.New("game launch receipt result does not match its terminal status")
		}
	}
	if receipt.Status == GameExecutionCompleted && receipt.Result == nil {
		return errors.New("completed game launch receipt requires a result")
	}
	if receipt.Status == GameExecutionFailed && receipt.Result == nil && strings.TrimSpace(receipt.Error) == "" {
		return errors.New("failed game launch receipt requires a result or error")
	}
	if receipt.Status == GameExecutionCompleted && receipt.GameResultJSON == "" {
		return errors.New("completed game launch receipt requires a title outcome")
	}
	if receipt.CreatedAt.IsZero() || receipt.UpdatedAt.IsZero() || receipt.UpdatedAt.Before(receipt.CreatedAt) {
		return errors.New("game launch receipt timestamps are invalid")
	}
	if receipt.GameResultJSON != "" {
		var result map[string]json.RawMessage
		if len(receipt.GameResultJSON) > 64<<10 || json.Unmarshal([]byte(receipt.GameResultJSON), &result) != nil || result == nil {
			return errors.New("game launch receipt game_result is invalid")
		}
	}
	return nil
}

func NewGameProgress(taskID modulecore.TaskID, spec GameSpec, now time.Time) (GameProgress, error) {
	if err := taskID.Validate(); err != nil {
		return GameProgress{}, fmt.Errorf("game task_id is invalid: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return GameProgress{}, fmt.Errorf("game spec is invalid: %w", err)
	}
	if now.IsZero() {
		return GameProgress{}, errors.New("game progress timestamp is required")
	}
	progress := GameProgress{
		LaunchKey: string(taskID), SpecDigest: spec.Digest(), Status: GameExecutionPrepared,
		CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	return progress, progress.Validate(taskID, spec)
}

func (progress GameProgress) Validate(taskID modulecore.TaskID, spec GameSpec) error {
	if err := taskID.Validate(); err != nil {
		return fmt.Errorf("game progress task_id is invalid: %w", err)
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("game progress spec is invalid: %w", err)
	}
	if progress.LaunchKey != string(taskID) || progress.SpecDigest != spec.Digest() || progress.SpecDigest == "" {
		return errors.New("game progress launch identity does not match its Task and spec")
	}
	if (progress.ControllerRunID == "") != (progress.ControllerQueueItemID == "") {
		return errors.New("game controller Run binding is incomplete")
	}
	if progress.ControllerRunID != "" {
		if progress.ControllerRunID.Validate() != nil || progress.ControllerQueueItemID.Validate() != nil {
			return errors.New("game controller Run binding is invalid")
		}
	}
	switch progress.Status {
	case GameExecutionPrepared, GameExecutionLaunching, GameExecutionRunning, GameExecutionUnknown, GameExecutionCompleted, GameExecutionFailed:
	default:
		return fmt.Errorf("game progress status %q is invalid", progress.Status)
	}
	if progress.Status != GameExecutionPrepared && progress.Status != GameExecutionLaunching && progress.SessionID != "" {
		if strings.TrimSpace(progress.SessionID) != progress.SessionID || len(progress.SessionID) > 256 {
			return errors.New("game progress session_id is invalid")
		}
	}
	if (progress.Status == GameExecutionRunning || progress.Status == GameExecutionCompleted) && progress.SessionID == "" {
		return errors.New("running or completed game progress requires a session_id")
	}
	if progress.Status == GameExecutionCompleted && (progress.SessionID == "" || progress.ResultStatus != "completed") {
		return errors.New("completed game progress requires a completed result and session")
	}
	if progress.Status == GameExecutionFailed && progress.ResultStatus != "failed" && strings.TrimSpace(progress.Error) == "" {
		return errors.New("failed game progress requires a failed result")
	}
	if progress.Status == GameExecutionCompleted && progress.GameResultJSON == "" {
		return errors.New("completed game progress requires a title outcome")
	}
	if progress.ResultStatus != "" {
		if progress.ResultOK == nil || progress.ResultGameID != spec.GameID || progress.ResultSessionID != progress.SessionID {
			return errors.New("game progress result identity is incomplete")
		}
	}
	if progress.GameResultJSON != "" {
		var result map[string]json.RawMessage
		if len(progress.GameResultJSON) > 64<<10 || json.Unmarshal([]byte(progress.GameResultJSON), &result) != nil || result == nil {
			return errors.New("game progress result is invalid or too large")
		}
	}
	if progress.CreatedAt.IsZero() || progress.UpdatedAt.IsZero() || progress.UpdatedAt.Before(progress.CreatedAt) {
		return errors.New("game progress timestamps are invalid")
	}
	return nil
}
