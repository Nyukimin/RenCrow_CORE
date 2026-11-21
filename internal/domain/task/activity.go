package task

import (
	"context"
	"errors"
	"fmt"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ActivityKind string

const (
	ActivityKindWork       ActivityKind = "work"
	ActivityKindChat       ActivityKind = "chat"
	ActivityKindHobby      ActivityKind = "hobby"
	ActivityKindRest       ActivityKind = "rest"
	ActivityKindSleep      ActivityKind = "sleep"
	ActivityKindMoving     ActivityKind = "moving"
	ActivityKindGame       ActivityKind = "game"
	ActivityKindEvaluating ActivityKind = "evaluating"
	ActivityKindRoutine    ActivityKind = "routine"
)

var (
	ErrCurrentActivityNotFound             = errors.New("current activity Task not found")
	ErrCurrentActivityConflict             = errors.New("current activity Task conflicts with expected assignment")
	ErrCurrentActivityInvalid              = errors.New("current activity Task is invalid")
	ErrActivityTaskRequiresOwner           = errors.New("Task activity metadata requires its registered owner")
	ErrActivityTaskWriterUnsupported       = errors.New("Task store does not support activity owner writes")
	ErrCurrentActivityReaderUnsupported    = errors.New("Task store does not support indexed current activity reads")
	ErrActiveExternalGameActivitiesTooMany = errors.New("active external Game Task count exceeds bounded read limit")
	ErrActivityRootCandidateSearchLimit    = errors.New("active root activity candidate search exceeds bounded read limit")
)

const MaxActivityRootCandidateScan = 256

// ActivityTaskWriter is the narrow TaskStore path for atomically changing
// activity assignment metadata while preserving all other Task fields.
type ActivityTaskWriter interface {
	GetTaskActivity(context.Context, modulecore.TaskID) (Task, error)
	SaveTaskActivity(context.Context, Task) error
	SaveNewPassiveActivityTask(context.Context, Task) error
}

// CurrentActivityTaskReader returns only Tasks carrying authoritative current
// activity markers for the requested actors from one committed snapshot.
type CurrentActivityTaskReader interface {
	CurrentActivityTasks(context.Context, []string) (map[string]Task, error)
}

// ActiveExternalGameActivityReader reads the bounded set of live, runless,
// passive external Game Tasks, including Tasks no longer current for an actor.
type ActiveExternalGameActivityReader interface {
	ListActiveExternalGameActivities(context.Context) ([]Task, error)
}

// ActivityRootTaskCandidate contains only the owner facts needed to restore
// an actor's current activity to another still-running root Task.
type ActivityRootTaskCandidate struct {
	TaskID          modulecore.TaskID
	Kind            ActivityKind
	Label           string
	Phase           string
	ParticipantRole string
}

// ActivityRootTaskCandidateReader is an internal trusted CORE owner port, not
// a public read or authorization surface. It selects one deterministic active
// root without returning Task titles, context, summaries, or private payloads.
type ActivityRootTaskCandidateReader interface {
	FindRunningRootActivityCandidate(context.Context, string, modulecore.TaskID) (ActivityRootTaskCandidate, bool, error)
}

const MaxActiveExternalGameActivities = 64

// ActivityTaskTransaction is the narrow multi-Task transaction used for
// current-activity handoffs. Implementations serialize the requested Task IDs
// with execution fences and commit all activity-only Task updates together.
type ActivityTaskTransaction interface {
	ActivityTransaction(context.Context, []modulecore.TaskID, func(Store) error) error
}

// ActivityMetadata records the activity a durable Task represents. A shared
// Task may be current for several participants, such as a multiplayer game.
type ActivityMetadata struct {
	Kind         ActivityKind           `json:"kind"`
	Label        string                 `json:"label"`
	Phase        string                 `json:"phase"`
	Passive      bool                   `json:"passive,omitempty"`
	Revision     uint64                 `json:"revision"`
	Game         *ActivityGameReference `json:"game,omitempty"`
	Participants []ActivityParticipant  `json:"participants"`
}

type ActivityGameReference struct {
	GameID    string `json:"game_id"`
	SessionID string `json:"session_id"`
}

type ActivityParticipant struct {
	ActorID   string `json:"actor_id"`
	Role      string `json:"role,omitempty"`
	IsCurrent bool   `json:"is_current"`
	Revision  uint64 `json:"revision"`
}

type CurrentActivitySnapshot struct {
	Task        Task                `json:"task"`
	Participant ActivityParticipant `json:"participant"`
}

func (activity ActivityMetadata) Validate(taskStatus Status) error {
	switch activity.Kind {
	case ActivityKindWork, ActivityKindChat, ActivityKindHobby, ActivityKindRest,
		ActivityKindSleep, ActivityKindMoving, ActivityKindGame,
		ActivityKindEvaluating, ActivityKindRoutine:
	default:
		return fmt.Errorf("activity kind %q is invalid", activity.Kind)
	}
	if strings.TrimSpace(activity.Label) == "" || strings.TrimSpace(activity.Label) != activity.Label || len(activity.Label) > 256 || hasActivityControl(activity.Label) {
		return errors.New("activity label is required and must be trimmed")
	}
	if activity.Phase != "" && (strings.TrimSpace(activity.Phase) != activity.Phase || len(activity.Phase) > 64 || hasActivityControl(activity.Phase)) {
		return errors.New("activity phase must be trimmed")
	}
	if len(activity.Participants) == 0 {
		return errors.New("activity requires at least one participant")
	}
	seen := make(map[string]struct{}, len(activity.Participants))
	for _, participant := range activity.Participants {
		if !canonicalTaskMeetingActor(participant.ActorID) {
			return errors.New("activity participant actor_id is invalid")
		}
		if _, exists := seen[participant.ActorID]; exists {
			return fmt.Errorf("activity participant %q is duplicated", participant.ActorID)
		}
		seen[participant.ActorID] = struct{}{}
		if strings.TrimSpace(participant.Role) != participant.Role || len(participant.Role) > 64 || hasActivityControl(participant.Role) {
			return errors.New("activity participant role must be trimmed")
		}
		if participant.IsCurrent && (participant.Revision == 0 || IsTerminal(taskStatus)) {
			return errors.New("current activity participant requires a nonterminal Task and positive revision")
		}
		if !participant.IsCurrent && participant.Revision == 0 {
			return errors.New("activity participant revision must be positive")
		}
	}
	if activity.Revision == 0 {
		return errors.New("activity revision must be positive")
	}
	if activity.Kind == ActivityKindGame {
		if activity.Game == nil || !validActivityGameID(activity.Game.GameID) ||
			(activity.Game.SessionID != "" && !validActivityGameSessionID(activity.Game.SessionID)) {
			return errors.New("game activity requires a safe game_id and optional safe session_id")
		}
	} else if activity.Game != nil {
		return errors.New("game activity reference is only valid for game kind")
	}
	return nil
}

func hasActivityControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func validActivityGameID(gameID string) bool {
	switch gameID {
	case "nethack", "territory_commander", "survival_garden", "herzog_zwei":
		return true
	default:
		return false
	}
}

func validActivityGameSessionID(sessionID string) bool {
	if sessionID == "" || len(sessionID) > 256 || strings.TrimSpace(sessionID) != sessionID {
		return false
	}
	return !hasActivityControl(sessionID)
}

func cloneActivityMetadata(value *ActivityMetadata) *ActivityMetadata {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Participants = append([]ActivityParticipant(nil), value.Participants...)
	if value.Game != nil {
		game := *value.Game
		cloned.Game = &game
	}
	return &cloned
}
