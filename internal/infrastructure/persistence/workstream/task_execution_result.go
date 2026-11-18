package workstream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

const TaskCenterExecutionResultArtifactType = "task_execution_result"

const taskCenterExecutionResultArtifactSchemaVersion = 1
const taskCenterExecutionResultMaxBytes = 48 << 10

type TaskCenterExecutionResultArtifact struct {
	SchemaVersion int                                  `json:"schema_version"`
	Reference     domaintask.TaskCenterResultReference `json:"reference"`
	Result        json.RawMessage                      `json:"result"`
}

type TaskCenterExecutionResultAuthorizationCheck = domaintask.TaskCenterExecutionResultAuthorizationCheck

// SaveTaskCenterExecutionResult writes the canonical typed result into the
// existing Workstream artifact table. The callback must verify the current
// linked Goal and successful Task/Run proof before the write is exposed.
func (s *SQLiteStore) SaveTaskCenterExecutionResult(ctx context.Context, reference domaintask.TaskCenterResultReference, result json.RawMessage, authorize TaskCenterExecutionResultAuthorizationCheck) error {
	if s == nil || s.db == nil || ctx == nil || authorize == nil {
		return domainworkstream.ErrTaskPlanForbidden
	}
	if err := validateTaskCenterResult(reference, result); err != nil {
		return err
	}
	if err := authorize(ctx, reference); err != nil {
		return err
	}
	artifact, err := makeTaskCenterResultArtifact(reference, result)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(artifact)
	if err != nil {
		return err
	}
	if len(encoded) > taskCenterExecutionResultMaxBytes+(4<<10) {
		return errors.New("TaskCenter result artifact exceeds its storage limit")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	if err := validateTaskCenterResultSource(ctx, tx, reference); err != nil {
		return err
	}
	row := domainworkstream.Artifact{
		ArtifactID: reference.ArtifactID, WorkstreamID: string(reference.WorkstreamID), Type: TaskCenterExecutionResultArtifactType,
		Title: "Task execution result", Status: "stored", Payload: encoded, CreatedAt: time.Now().UTC(),
	}
	if err := domainworkstream.ValidateArtifact(row); err != nil {
		return fmt.Errorf("validate TaskCenter result artifact: %w", err)
	}
	rowJSON, err := json.Marshal(row)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO artifact (artifact_id, workstream_id, created_at, payload) VALUES (?, ?, ?, ?)`, row.ArtifactID, row.WorkstreamID, row.CreatedAt.Format(timeFormatRFC3339Nano), string(rowJSON)); err != nil {
		return classifyTaskPlanStorageError(err)
	}
	var existing string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM artifact WHERE artifact_id = ?`, reference.ArtifactID).Scan(&existing); err != nil {
		return classifyTaskPlanStorageError(err)
	}
	var stored domainworkstream.Artifact
	if json.Unmarshal([]byte(existing), &stored) != nil || stored.Type != TaskCenterExecutionResultArtifactType || stored.WorkstreamID != string(reference.WorkstreamID) {
		return domainworkstream.ErrTaskPlanConflict
	}
	var storedPayload TaskCenterExecutionResultArtifact
	if json.Unmarshal(stored.Payload, &storedPayload) != nil || !sameTaskCenterExecutionResult(storedPayload, artifact) {
		return domainworkstream.ErrTaskPlanConflict
	}
	return tx.Commit()
}

// GetTaskCenterExecutionResult returns a result only after the caller has
// proved authorization against the exact reference stored in the artifact.
func (s *SQLiteStore) GetTaskCenterExecutionResult(ctx context.Context, expected domaintask.TaskCenterResultReference, authorize TaskCenterExecutionResultAuthorizationCheck) (domaintask.TaskCenterResultReference, json.RawMessage, error) {
	if s == nil || s.db == nil || ctx == nil || authorize == nil {
		return domaintask.TaskCenterResultReference{}, nil, domainworkstream.ErrTaskPlanForbidden
	}
	if err := validateTaskCenterResultReference(expected, true); err != nil {
		return domaintask.TaskCenterResultReference{}, nil, err
	}
	if err := authorize(ctx, expected); err != nil {
		return domaintask.TaskCenterResultReference{}, nil, err
	}
	var payload string
	if err := s.db.QueryRowContext(ctx, `SELECT payload FROM artifact WHERE artifact_id = ?`, expected.ArtifactID).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domaintask.TaskCenterResultReference{}, nil, domainworkstream.ErrTaskPlanNotFound
		}
		return domaintask.TaskCenterResultReference{}, nil, classifyTaskPlanStorageError(err)
	}
	var artifact domainworkstream.Artifact
	if json.Unmarshal([]byte(payload), &artifact) != nil || artifact.Type != TaskCenterExecutionResultArtifactType || artifact.ArtifactID != expected.ArtifactID {
		return domaintask.TaskCenterResultReference{}, nil, domainworkstream.ErrTaskPlanUnavailable
	}
	var result TaskCenterExecutionResultArtifact
	if json.Unmarshal(artifact.Payload, &result) != nil || result.SchemaVersion != taskCenterExecutionResultArtifactSchemaVersion {
		return domaintask.TaskCenterResultReference{}, nil, domainworkstream.ErrTaskPlanUnavailable
	}
	if err := validateTaskCenterResult(result.Reference, result.Result); err != nil || !taskCenterResultRefMatchesExpected(expected, result.Reference) || artifact.WorkstreamID != string(result.Reference.WorkstreamID) {
		return domaintask.TaskCenterResultReference{}, nil, domainworkstream.ErrTaskPlanUnavailable
	}
	if err := authorize(ctx, result.Reference); err != nil {
		return domaintask.TaskCenterResultReference{}, nil, err
	}
	return result.Reference, append(json.RawMessage(nil), result.Result...), nil
}

func makeTaskCenterResultArtifact(reference domaintask.TaskCenterResultReference, result json.RawMessage) (TaskCenterExecutionResultArtifact, error) {
	compact := bytes.NewBuffer(nil)
	if err := json.Compact(compact, result); err != nil {
		return TaskCenterExecutionResultArtifact{}, err
	}
	return TaskCenterExecutionResultArtifact{SchemaVersion: taskCenterExecutionResultArtifactSchemaVersion, Reference: reference, Result: append(json.RawMessage(nil), compact.Bytes()...)}, nil
}

func sameTaskCenterExecutionResult(a, b TaskCenterExecutionResultArtifact) bool {
	return a.SchemaVersion == b.SchemaVersion && a.Reference == b.Reference && bytes.Equal(a.Result, b.Result)
}

func validateTaskCenterResult(reference domaintask.TaskCenterResultReference, result json.RawMessage) error {
	if err := validateTaskCenterResultReference(reference, false); err != nil {
		return err
	}
	if len(result) == 0 || len(result) > taskCenterExecutionResultMaxBytes || !json.Valid(result) {
		return domainworkstream.ErrTaskPlanInvalid
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, result); err != nil {
		return domainworkstream.ErrTaskPlanInvalid
	}
	digest := sha256.Sum256(compact.Bytes())
	if reference.ResultSHA256 != hex.EncodeToString(digest[:]) {
		return domainworkstream.ErrTaskPlanConflict
	}
	return nil
}

func validateTaskCenterResultReference(reference domaintask.TaskCenterResultReference, allowUnknownDigest bool) error {
	if reference.ArtifactID == "" || reference.SourceGoalID == "" || reference.WorkstreamID == "" || reference.TaskID == "" || reference.RunID == "" || reference.KindID == "" {
		return domainworkstream.ErrTaskPlanInvalid
	}
	if reference.SourceGoalID.Validate() != nil || reference.WorkstreamID.Validate() != nil || reference.TaskID.Validate() != nil || reference.RunID.Validate() != nil {
		return domainworkstream.ErrTaskPlanInvalid
	}
	expectedArtifactID, err := domaintask.TaskCenterResultArtifactID(reference.TaskID, reference.RunID)
	if err != nil || reference.ArtifactID != expectedArtifactID {
		return domainworkstream.ErrTaskPlanInvalid
	}
	if !validTaskCenterSHA256(reference.KindInputSHA256) || (!allowUnknownDigest || reference.ResultSHA256 != "") && !validTaskCenterSHA256(reference.ResultSHA256) {
		return domainworkstream.ErrTaskPlanInvalid
	}
	if reference.KindID != "movie.catalog.lookup" {
		return domainworkstream.ErrTaskPlanInvalid
	}
	switch reference.DataScope {
	case domainworkstream.TaskPlanDataScopePublic:
		if reference.UserID != "" {
			return domainworkstream.ErrTaskPlanInvalid
		}
	case domainworkstream.TaskPlanDataScopeUser:
		if reference.UserID == "" {
			return domainworkstream.ErrTaskPlanInvalid
		}
	default:
		return domainworkstream.ErrTaskPlanInvalid
	}
	return nil
}

func validTaskCenterSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}

func taskCenterResultRefMatchesExpected(expected, actual domaintask.TaskCenterResultReference) bool {
	if expected.ResultSHA256 == "" {
		actual.ResultSHA256 = ""
	}
	return expected == actual
}

func validateTaskCenterResultSource(ctx context.Context, tx *sql.Tx, reference domaintask.TaskCenterResultReference) error {
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM workstream_goal WHERE goal_id = ?`, reference.SourceGoalID).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainworkstream.ErrTaskPlanNotFound
		}
		return classifyTaskPlanStorageError(err)
	}
	var goal domainworkstream.Goal
	if json.Unmarshal([]byte(raw), &goal) != nil || domainworkstream.ValidateGoal(goal) != nil || goal.TaskPlan == nil {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	plan := goal.TaskPlan
	if goal.WorkstreamID != string(reference.WorkstreamID) || plan.DataScope != reference.DataScope || plan.UserID != reference.UserID ||
		plan.HandoffState != domainworkstream.TaskPlanHandoffLinked || plan.TaskID != reference.TaskID || plan.RunID != reference.RunID || plan.KindID != reference.KindID ||
		plan.HandoffInput == nil || !bytes.Equal(plan.KindInput, plan.HandoffInput.KindInput) {
		return domainworkstream.ErrTaskPlanConflict
	}
	digest, err := domaintask.TaskCenterKindInputSHA256(plan.HandoffInput.KindInput)
	if err != nil || digest != reference.KindInputSHA256 {
		return domainworkstream.ErrTaskPlanConflict
	}
	return nil
}
