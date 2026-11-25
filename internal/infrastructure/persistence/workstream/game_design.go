package workstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const gameDesignReceiptType = "game_project_design_registration"

type gameDesignReceipt struct {
	WorkstreamID string `json:"workstream_id"`
	GoalID       string `json:"goal_id"`
	UserID       string `json:"user_id"`
}

// EnsureGameProjectDesign serializes on the existing Workstream row. The
// original project receipt stays immutable; this extension has its own proof.
func (s *SQLiteStore) EnsureGameProjectDesign(ctx context.Context, scope tool.ToolExecutionScope, id string) (domainworkstream.GameProjectDesignResult, error) {
	var empty domainworkstream.GameProjectDesignResult
	if err := validateTaskPlanOwnerCall(ctx, scope); err != nil {
		return empty, err
	}
	if scope.ActorKind != tool.ActorKindUser || scope.ActorID != scope.AuthenticatedUserID || scope.ActorID == "" || !scope.Allows(tool.DataScopePublic) {
		return empty, domainworkstream.ErrTaskPlanForbidden
	}
	if modulecore.WorkstreamID(id).Validate() != nil {
		return empty, domainworkstream.ErrTaskPlanInvalid
	}
	if s == nil || s.db == nil {
		return empty, domainworkstream.ErrTaskPlanUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return empty, classifyTaskPlanStorageError(err)
	}
	defer tx.Rollback()
	lock, err := tx.ExecContext(ctx, `UPDATE workstream SET payload=payload WHERE workstream_id=?`, id)
	if err != nil {
		return empty, classifyTaskPlanStorageError(err)
	}
	n, err := lock.RowsAffected()
	if err != nil {
		return empty, err
	}
	if n != 1 {
		return empty, domainworkstream.ErrTaskPlanNotFound
	}
	var raw string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM workstream WHERE workstream_id=?`, id).Scan(&raw); err != nil {
		return empty, err
	}
	var project domainworkstream.Workstream
	if err := json.Unmarshal([]byte(raw), &project); err != nil {
		return empty, err
	}
	if project.ProjectDomain == nil || *project.ProjectDomain != domainworkstream.ProjectDomainGame || project.TaskCenterScope != nil {
		return empty, domainworkstream.ErrTaskPlanForbidden
	}
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM artifact WHERE workstream_id=? AND json_extract(payload,'$.artifact_type') IN (?,?)`, id, gameProjectPlanReceiptArtifactType, gameDesignReceiptType)
	if err != nil {
		return empty, err
	}
	var original *domainworkstream.ProjectPlanOperationReceipt
	var extension *gameDesignReceipt
	var originalRef, extensionRef string
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			rows.Close()
			return empty, err
		}
		var artifact domainworkstream.Artifact
		if err := json.Unmarshal([]byte(encoded), &artifact); err != nil {
			rows.Close()
			return empty, err
		}
		if artifact.Type == gameProjectPlanReceiptArtifactType {
			if original != nil {
				rows.Close()
				return empty, domainworkstream.ErrTaskPlanConflict
			}
			original = new(domainworkstream.ProjectPlanOperationReceipt)
			if err := decodeTaskPlanStrictJSON(artifact.Payload, original); err != nil {
				rows.Close()
				return empty, err
			}
			if err := validateGameProjectPlanReceipt(artifact, id, *original); err != nil {
				rows.Close()
				return empty, err
			}
			originalRef = artifact.ArtifactID
		} else {
			if extension != nil {
				rows.Close()
				return empty, domainworkstream.ErrTaskPlanConflict
			}
			extension = new(gameDesignReceipt)
			if err := decodeTaskPlanStrictJSON(artifact.Payload, extension); err != nil {
				rows.Close()
				return empty, err
			}
			extensionRef = artifact.ArtifactID
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return empty, err
	}
	rows.Close()
	if original == nil {
		return empty, domainworkstream.ErrTaskPlanNotFound
	}
	if original.AuthenticatedUserID != scope.AuthenticatedUserID {
		return empty, domainworkstream.ErrTaskPlanForbidden
	}
	goalID, receiptRef := "", originalRef
	for i, key := range original.Registration.StageKeys {
		if key == "design" {
			goalID = original.Registration.GoalIDs[i]
		}
	}
	if extension != nil {
		if extension.WorkstreamID != id || extension.UserID != scope.AuthenticatedUserID || modulecore.GoalID(extension.GoalID).Validate() != nil || goalID != "" {
			return empty, domainworkstream.ErrTaskPlanConflict
		}
		goalID, receiptRef = extension.GoalID, extensionRef
	}
	if goalID != "" {
		var encoded string
		if err := tx.QueryRowContext(ctx, `SELECT payload FROM workstream_goal WHERE goal_id=? AND workstream_id=?`, goalID, id).Scan(&encoded); err != nil {
			return empty, err
		}
		var goal domainworkstream.Goal
		if err := json.Unmarshal([]byte(encoded), &goal); err != nil {
			return empty, err
		}
		if goal.GoalID != goalID || goal.WorkstreamID != id {
			return empty, errors.New("design Goal identity mismatch")
		}
		if err := domainworkstream.ValidateGoal(goal); err != nil {
			return empty, err
		}
		return domainworkstream.GameProjectDesignResult{Goal: goal, Replayed: true, ReceiptRef: receiptRef}, nil
	}
	now := time.Now().UTC()
	goal := domainworkstream.Goal{GoalID: string(modulecore.NewGoalID()), WorkstreamID: id, Title: "デザイン", Description: "Museが主体となり、仕様に沿った世界観、画面、視覚表現と素材の方針を定め、試作へ引き継ぐ。", Status: domainworkstream.StatusDraft, CreatedAt: now, SuccessCriteria: []string{"世界観と画面・視覚表現の方針が記録済み", "試作に必要なデザインと素材の出所・未決事項が明確"}, Verification: []string{"Museのデザインを仕様と照合し、試作担当が参照できることを確認する"}}
	if err := domainworkstream.ValidateGoal(goal); err != nil {
		return empty, err
	}
	encoded, err := json.Marshal(goal)
	if err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO workstream_goal(goal_id,workstream_id,created_at,payload) VALUES(?,?,?,?)`, goal.GoalID, id, now.Format(timeFormatRFC3339Nano), string(encoded)); err != nil {
		return empty, err
	}
	receiptRef = string(modulecore.NewArtifactID())
	payload, err := json.Marshal(gameDesignReceipt{WorkstreamID: id, GoalID: goal.GoalID, UserID: scope.AuthenticatedUserID})
	if err != nil {
		return empty, err
	}
	artifact := domainworkstream.Artifact{ArtifactID: receiptRef, WorkstreamID: id, Type: gameDesignReceiptType, Title: "Game design registration", Status: "stored", Payload: payload, CreatedAt: now}
	encoded, err = json.Marshal(artifact)
	if err != nil {
		return empty, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO artifact(artifact_id,workstream_id,created_at,payload) VALUES(?,?,?,?)`, receiptRef, id, now.Format(timeFormatRFC3339Nano), string(encoded)); err != nil {
		return empty, err
	}
	if err := tx.Commit(); err != nil {
		return empty, fmt.Errorf("commit design: %w", err)
	}
	return domainworkstream.GameProjectDesignResult{Goal: goal, ReceiptRef: receiptRef}, nil
}
