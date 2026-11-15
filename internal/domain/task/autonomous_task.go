package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var autonomousActors = map[string]struct{}{
	"mio": {}, "shiro": {}, "aka": {}, "ao": {},
	"kin": {}, "gin": {}, "kuro": {}, "midori": {},
}

// AutonomousActor reports whether actorID is one of the canonical resident
// CORE actors admitted to autonomous self-task creation.
func AutonomousActor(actorID string) bool {
	_, ok := autonomousActors[actorID]
	return ok
}

// AutonomousModuleID groups self-tasks by their owning actor without granting
// a module root or additional execution capability.
func AutonomousModuleID(actorID string) (string, error) {
	if !AutonomousActor(actorID) {
		return "", errors.New("autonomous actor_id is invalid")
	}
	return "autonomous-" + actorID, nil
}

// AutonomousPlanTaskID derives a canonical UUIDv5 Task ID from its owner,
// actor, UTC calendar date, and source Task. An empty source identifies the
// root plan for that owner/actor/date.
func AutonomousPlanTaskID(ownerID, actorID, date string, sourceTaskID modulecore.TaskID) (modulecore.TaskID, error) {
	if err := validateAutonomousOwnerID(ownerID); err != nil {
		return "", err
	}
	if !AutonomousActor(actorID) {
		return "", errors.New("autonomous actor_id is invalid")
	}
	if err := validateAutonomousDate(date); err != nil {
		return "", err
	}
	if sourceTaskID != "" {
		if err := sourceTaskID.Validate(); err != nil {
			return "", fmt.Errorf("autonomous source_task_id is invalid: %w", err)
		}
	}
	value := strings.Join([]string{AutonomousPolicyVersion, ownerID, actorID, date, string(sourceTaskID)}, "\x00")
	id, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "autonomous_task", "plan/"+AutonomousPolicyVersion, value)
	if err != nil {
		return "", fmt.Errorf("derive autonomous plan task_id: %w", err)
	}
	return modulecore.TaskID(id), nil
}

// AutonomousWorkTaskID derives the canonical UUIDv5 Task ID authorized by a
// plan Task. The Task ID already namespaces owner, actor, and date.
func AutonomousWorkTaskID(planTaskID modulecore.TaskID) (modulecore.TaskID, error) {
	if err := planTaskID.Validate(); err != nil {
		return "", fmt.Errorf("autonomous plan task_id is invalid: %w", err)
	}
	id, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "autonomous_task", "work/"+AutonomousPolicyVersion,
		AutonomousPolicyVersion+"\x00"+string(planTaskID))
	if err != nil {
		return "", fmt.Errorf("derive autonomous work task_id: %w", err)
	}
	return modulecore.TaskID(id), nil
}

func validateAutonomousDate(value string) error {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil || parsed.Format("2006-01-02") != value {
		return errors.New("autonomous date must be YYYY-MM-DD")
	}
	return nil
}

func (a AutonomousTask) Validate(task Task) error {
	if a.PolicyVersion != AutonomousPolicyVersion {
		return fmt.Errorf("autonomous policy_version is unsupported: %q", a.PolicyVersion)
	}
	if err := validateAutonomousDate(a.Date); err != nil {
		return err
	}
	if err := validateAutonomousOwnerID(task.OwnerID); err != nil {
		return err
	}
	if !AutonomousActor(task.Assignee) {
		return errors.New("autonomous Task assignee is not a canonical actor")
	}
	if strings.TrimSpace(task.Title) == "" || len(task.Title) > 256 {
		return errors.New("autonomous Task title is required and bounded")
	}
	if err := a.RootTaskID.Validate(); err != nil {
		return fmt.Errorf("autonomous root_task_id is invalid: %w", err)
	}
	if a.Depth < 0 || a.Depth > AutonomousMaxDepth {
		return fmt.Errorf("autonomous depth must be between 0 and %d", AutonomousMaxDepth)
	}
	if task.Route != RouteCHAT || task.Priority != PriorityLow || !task.ReadOnly || task.ModuleRoot != "" || task.ParentTaskID != "" ||
		len(task.DependencyTaskIDs) != 0 || task.OriginSessionID != "" || task.OriginThreadID != "" || task.OriginTurnID != "" ||
		task.OriginMessageID != "" || task.OriginPublication != nil || task.AcceptedOPSClaim != nil || task.ExpectedCriteriaRevision != "" ||
		len(task.NativeResumeClaims) != 0 || task.WorkstreamID != "" || task.GoalID != "" || task.Production != nil ||
		task.RoutingEventID != "" || task.AssignmentEventID != "" || len(task.CoderRoles) != 0 || task.SupersedesTaskID != "" {
		return errors.New("autonomous Task exceeds the private read-only CHAT contract")
	}
	moduleID, err := AutonomousModuleID(task.Assignee)
	if err != nil || task.ModuleID != moduleID {
		return errors.New("autonomous Task module_id does not match its actor")
	}
	switch a.Kind {
	case AutonomousTaskKindPlan:
		wantID, err := AutonomousPlanTaskID(task.OwnerID, task.Assignee, a.Date, a.SourceTaskID)
		if err != nil || task.TaskID != wantID {
			return errors.New("autonomous plan task_id is not deterministic for its source")
		}
		if a.SourceTaskID == "" {
			if a.SourceRunID != "" || a.SourceOutputSHA256 != "" || a.Depth != 0 || a.RootTaskID != task.TaskID {
				return errors.New("autonomous root plan source metadata is invalid")
			}
		} else {
			if err := a.SourceTaskID.Validate(); err != nil {
				return fmt.Errorf("autonomous source_task_id is invalid: %w", err)
			}
			if err := a.SourceRunID.Validate(); err != nil {
				return fmt.Errorf("autonomous source_run_id is invalid: %w", err)
			}
			if !ValidCriteriaRevision(a.SourceOutputSHA256) {
				return errors.New("autonomous source_output_sha256 is invalid")
			}
			if a.RootTaskID == task.TaskID {
				return errors.New("autonomous successor plan cannot be its own root")
			}
		}
	case AutonomousTaskKindWork:
		if a.SourceTaskID == "" {
			return errors.New("autonomous work source_task_id is required")
		}
		if err := a.SourceTaskID.Validate(); err != nil {
			return fmt.Errorf("autonomous source_task_id is invalid: %w", err)
		}
		if err := a.SourceRunID.Validate(); err != nil {
			return fmt.Errorf("autonomous source_run_id is invalid: %w", err)
		}
		if a.SourceOutputSHA256 != "" {
			return errors.New("autonomous work must not include source_output_sha256")
		}
		wantID, err := AutonomousWorkTaskID(a.SourceTaskID)
		if err != nil || task.TaskID != wantID || a.Depth < 1 || a.RootTaskID == task.TaskID {
			return errors.New("autonomous work identity or depth is invalid")
		}
	default:
		return fmt.Errorf("autonomous kind is invalid: %q", a.Kind)
	}
	return nil
}

func validateAutonomousOwnerID(ownerID string) error {
	if ownerID == "" || strings.TrimSpace(ownerID) != ownerID || !utf8.ValidString(ownerID) {
		return errors.New("autonomous owner_id is invalid")
	}
	if err := domainsession.ValidateOwnerID(ownerID); err != nil {
		return fmt.Errorf("autonomous owner_id is invalid: %w", err)
	}
	return nil
}

// Validate checks the bounded decision contract. The source Task ID supplies
// the deterministic candidate identity for continue decisions.
func (d AutonomousDecision) Validate(planTaskID modulecore.TaskID) error {
	if err := planTaskID.Validate(); err != nil {
		return fmt.Errorf("autonomous decision plan_task_id is invalid: %w", err)
	}
	if strings.TrimSpace(d.Reason) == "" || len(d.Reason) > 2048 {
		return errors.New("autonomous decision reason is required and bounded")
	}
	switch d.Decision {
	case AutonomousDecisionStop:
		if d.Title != "" || d.Objective != "" || d.Completion != "" || d.CandidateTaskID != "" {
			return errors.New("autonomous stop decision must not include a candidate")
		}
	case AutonomousDecisionContinue:
		if strings.TrimSpace(d.Title) == "" || len(d.Title) > 256 || strings.TrimSpace(d.Objective) == "" || len(d.Objective) > 4096 ||
			strings.TrimSpace(d.Completion) == "" || len(d.Completion) > 2048 {
			return errors.New("autonomous continue decision requires bounded title, objective, and completion")
		}
		wantID, err := AutonomousWorkTaskID(planTaskID)
		if err != nil || d.CandidateTaskID != wantID {
			return errors.New("autonomous candidate_task_id is not deterministic for its plan")
		}
	default:
		return fmt.Errorf("autonomous decision is invalid: %q", d.Decision)
	}
	return nil
}
