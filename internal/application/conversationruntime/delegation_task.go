package conversationruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	StartDelegatedTaskWorkKind = "start_delegated_task"
	delegatedTaskLeaseDuration = 2 * time.Minute
	maxDelegatedTaskContext    = 64 * 1024
)

var (
	ErrDelegationTaskAdmissionUnavailable = errors.New("delegation Task admission is unavailable")
	ErrDelegationTaskAdmissionInvalid     = errors.New("delegation Task admission source is invalid")
)

// DelegatedTaskLease is the durable EventStore lease used for one admission
// attempt. Assignee is supplied by canonical stored handoff state.
type DelegatedTaskLease struct {
	WorkID        string
	DedupeKey     string
	Kind          string
	SourceEventID modulecore.EventID
	SessionID     modulecore.SessionID
	Priority      int
	Attempt       int
	LeaseToken    string
	LeaseExpires  time.Time
	Version       int64
	Assignee      string
}

type DelegatedTaskClaimRequest struct {
	Kind          string
	Assignee      string
	Limit         int
	LeaseDuration time.Duration
	Now           time.Time
}

type DelegatedTaskContextMessage struct {
	MessageID modulecore.MessageID
	Role      string
	Text      string
}

type DelegatedTaskSource struct {
	OwnerID  string
	Handoff  conversation.HandoffEnvelope
	Readback conversation.ReadbackEnvelope
	Context  []DelegatedTaskContextMessage
}

type DelegatedTaskBindRequest struct {
	Lease           DelegatedTaskLease
	DelegationID    modulecore.RequestID
	Revision        uint64
	TaskID          modulecore.TaskID
	TaskOperationID string
	Now             time.Time
}

type DelegatedTaskAdmissionStore interface {
	ClaimDelegatedTaskWork(context.Context, DelegatedTaskClaimRequest) ([]DelegatedTaskLease, error)
	LoadDelegationTask(context.Context, DelegatedTaskLease, string, time.Time) (DelegatedTaskSource, error)
	BindDelegationTask(context.Context, DelegatedTaskBindRequest) error
	RetryDelegatedTaskWork(context.Context, DelegatedTaskLease, time.Time) error
}

type DelegatedTaskOwner interface {
	EnsureTaskForDelegation(context.Context, modulecore.RequestID, uint64, domaintask.Task, domaintask.SharedRoleContext, taskmanager.DelegationAdmissionCheck) (domaintask.Task, domaintask.Run, error)
}

type DelegationTaskAdmissionConfig struct {
	ActorID string
	Now     func() time.Time
}

type DelegationTaskAdmissionDependencies struct {
	Store  DelegatedTaskAdmissionStore
	Tasks  DelegatedTaskOwner
	Config DelegationTaskAdmissionConfig
}

type DelegationTaskAdmissionResult struct {
	Claimed      bool
	TaskEnsured  bool
	Bound        bool
	WorkID       string
	DelegationID modulecore.RequestID
	Revision     uint64
	TaskID       modulecore.TaskID
	RunID        modulecore.RunID
}

// DelegationTaskAdmission bridges accepted readbacks into the Task owner. The
// EventStore remains the source-state owner, and TaskManager remains the Task,
// Run, SharedRoleContext, and idempotency-receipt owner.
type DelegationTaskAdmission struct {
	store   DelegatedTaskAdmissionStore
	tasks   DelegatedTaskOwner
	actorID string
	now     func() time.Time
}

func NewDelegationTaskAdmission(dependencies DelegationTaskAdmissionDependencies) (*DelegationTaskAdmission, error) {
	if dependencies.Store == nil || dependencies.Tasks == nil {
		return nil, errors.New("delegation Task admission store and Task owner are required")
	}
	actorID := strings.TrimSpace(dependencies.Config.ActorID)
	if actorID != dependencies.Config.ActorID || superagent.ValidateActorID(actorID) != nil || actorID == "mio" {
		return nil, errors.New("delegation Task admission actor_id must be a supported non-Mio CORE Actor")
	}
	now := dependencies.Config.Now
	if now == nil {
		now = time.Now
	}
	return &DelegationTaskAdmission{store: dependencies.Store, tasks: dependencies.Tasks, actorID: actorID, now: now}, nil
}

// StartOne claims and admits at most one accepted delegated Task. TaskManager's
// global operation receipt makes a retry after Task creation but before the
// EventStore bind return the same Task and first Run.
func (a *DelegationTaskAdmission) StartOne(ctx context.Context) (DelegationTaskAdmissionResult, error) {
	if ctx == nil {
		return DelegationTaskAdmissionResult{}, errors.New("delegation Task admission context is required")
	}
	if err := ctx.Err(); err != nil {
		return DelegationTaskAdmissionResult{}, err
	}
	if a == nil || a.store == nil || a.tasks == nil || a.now == nil {
		return DelegationTaskAdmissionResult{}, ErrDelegationTaskAdmissionUnavailable
	}
	now := a.now().UTC()
	leases, err := a.store.ClaimDelegatedTaskWork(ctx, DelegatedTaskClaimRequest{
		Kind: StartDelegatedTaskWorkKind, Assignee: a.actorID, Limit: 1, LeaseDuration: delegatedTaskLeaseDuration, Now: now,
	})
	if err != nil {
		return DelegationTaskAdmissionResult{}, fmt.Errorf("claim delegated Task start work: %w", err)
	}
	if len(leases) == 0 {
		return DelegationTaskAdmissionResult{}, nil
	}
	if len(leases) != 1 {
		causes := []error{fmt.Errorf("task-start claim returned %d leases, want at most one", len(leases))}
		for _, lease := range leases {
			if retryErr := a.retryLease(ctx, lease); retryErr != nil {
				causes = append(causes, retryErr)
			}
		}
		return DelegationTaskAdmissionResult{Claimed: true}, errors.Join(causes...)
	}
	return a.admitLease(ctx, leases[0])
}

func (a *DelegationTaskAdmission) admitLease(ctx context.Context, lease DelegatedTaskLease) (DelegationTaskAdmissionResult, error) {
	result := DelegationTaskAdmissionResult{Claimed: true, WorkID: lease.WorkID}
	fail := func(cause error) (DelegationTaskAdmissionResult, error) {
		if retryErr := a.retryLease(ctx, lease); retryErr != nil {
			return result, errors.Join(cause, fmt.Errorf("retry delegated Task start lease: %w", retryErr))
		}
		return result, cause
	}
	if err := validateDelegatedTaskLease(lease, a.actorID, a.now()); err != nil {
		return fail(err)
	}
	source, err := a.loadSource(ctx, lease)
	if err != nil {
		return fail(fmt.Errorf("load accepted delegated Task source: %w", err))
	}
	if err := validateDelegatedTaskSource(source, lease, a.actorID); err != nil {
		return fail(err)
	}
	draft, shared, err := delegatedTaskDraft(source, a.actorID)
	if err != nil {
		return fail(err)
	}
	check := func(checkCtx context.Context, delegationID modulecore.RequestID, revision uint64) error {
		if delegationID != source.Handoff.DelegationID || revision != source.Handoff.Revision {
			return ErrDelegationTaskAdmissionInvalid
		}
		current, err := a.loadSource(checkCtx, lease)
		if err != nil {
			return fmt.Errorf("revalidate current delegation before Task commit: %w", err)
		}
		if err := validateDelegatedTaskSource(current, lease, a.actorID); err != nil {
			return err
		}
		if !reflect.DeepEqual(current, source) {
			return fmt.Errorf("%w: accepted handoff, readback, or public context changed", ErrDelegationTaskAdmissionInvalid)
		}
		return nil
	}
	task, run, err := a.tasks.EnsureTaskForDelegation(ctx, source.Handoff.DelegationID, source.Handoff.Revision, draft, shared, check)
	if err != nil {
		return fail(fmt.Errorf("ensure delegated Task through Task owner: %w", err))
	}
	result.DelegationID, result.Revision, result.TaskID, result.RunID = source.Handoff.DelegationID, source.Handoff.Revision, task.TaskID, run.RunID
	result.TaskEnsured = true
	if err := a.store.BindDelegationTask(ctx, DelegatedTaskBindRequest{
		Lease: lease, DelegationID: source.Handoff.DelegationID, Revision: source.Handoff.Revision,
		TaskID: task.TaskID, TaskOperationID: taskmanager.DelegationTaskOperationID(source.Handoff.DelegationID, source.Handoff.Revision),
		Now: a.now().UTC(),
	}); err != nil {
		return fail(fmt.Errorf("bind delegated Task in EventStore: %w", err))
	}
	result.Bound = true
	return result, nil
}

func (a *DelegationTaskAdmission) loadSource(ctx context.Context, lease DelegatedTaskLease) (DelegatedTaskSource, error) {
	return a.store.LoadDelegationTask(ctx, lease, a.actorID, a.now().UTC())
}

func (a *DelegationTaskAdmission) retryLease(ctx context.Context, lease DelegatedTaskLease) error {
	return a.store.RetryDelegatedTaskWork(ctx, lease, a.now().UTC())
}

func validateDelegatedTaskLease(lease DelegatedTaskLease, actorID string, now time.Time) error {
	if lease.WorkID == "" || lease.Kind != StartDelegatedTaskWorkKind || lease.DedupeKey == "" ||
		lease.SessionID == "" || lease.SourceEventID == "" || lease.LeaseToken == "" || lease.Version < 1 ||
		lease.LeaseExpires.IsZero() || !lease.LeaseExpires.After(now) || lease.Assignee != actorID {
		return fmt.Errorf("%w: task-start lease is invalid or belongs to another Actor", ErrDelegationTaskAdmissionInvalid)
	}
	return nil
}

func validateDelegatedTaskSource(source DelegatedTaskSource, lease DelegatedTaskLease, actorID string) error {
	if strings.TrimSpace(source.OwnerID) == "" || source.Handoff.FromActor != "mio" || source.Handoff.ToActor != actorID ||
		source.Handoff.SessionID != lease.SessionID || source.Handoff.Revision == 0 ||
		source.Readback.Status != conversation.ReadbackStatusAccepted || source.Readback.Sender != actorID ||
		source.Readback.DelegationID != source.Handoff.DelegationID || source.Readback.Revision != source.Handoff.Revision ||
		lease.DedupeKey != fmt.Sprintf("delegation:%s:%d:start_task", source.Handoff.DelegationID, source.Handoff.Revision) ||
		len(source.Handoff.AllowedCapabilities) != 0 {
		return fmt.Errorf("%w: handoff is stale, unsupported, not accepted, or grants unsupported capabilities", ErrDelegationTaskAdmissionInvalid)
	}
	if err := superagent.ValidateActorID(source.Handoff.ToActor); err != nil {
		return fmt.Errorf("%w: unsupported remote Task owner", ErrDelegationTaskAdmissionInvalid)
	}
	if err := conversation.ValidateHandoffEnvelope(source.Handoff); err != nil {
		return fmt.Errorf("%w: invalid HandoffEnvelope: %v", ErrDelegationTaskAdmissionInvalid, err)
	}
	if err := conversation.ValidateReadbackEnvelope(source.Readback, source.Handoff, conversation.TrustedReadbackMetadata{
		DelegationID: source.Handoff.DelegationID, Revision: source.Handoff.Revision, Sender: actorID,
	}); err != nil {
		return fmt.Errorf("%w: invalid accepted readback: %v", ErrDelegationTaskAdmissionInvalid, err)
	}
	if len(source.Context) == 0 || len(source.Context) > 256 {
		return fmt.Errorf("%w: authorized public context is empty or exceeds its bound", ErrDelegationTaskAdmissionInvalid)
	}
	seen := make(map[modulecore.MessageID]struct{}, len(source.Context))
	for _, message := range source.Context {
		if err := message.MessageID.Validate(); err != nil || (message.Role != "user" && message.Role != "assistant") || strings.TrimSpace(message.Text) == "" {
			return fmt.Errorf("%w: context message is not a public authorized message", ErrDelegationTaskAdmissionInvalid)
		}
		if _, duplicate := seen[message.MessageID]; duplicate {
			return fmt.Errorf("%w: context contains duplicate message identity", ErrDelegationTaskAdmissionInvalid)
		}
		seen[message.MessageID] = struct{}{}
	}
	if _, found := seen[source.Handoff.OriginUserMessageID]; !found {
		return fmt.Errorf("%w: public context omits the origin user message", ErrDelegationTaskAdmissionInvalid)
	}
	for _, constraint := range source.Handoff.Constraints {
		for _, messageID := range constraint.SourceMessageIDs {
			if _, found := seen[messageID]; !found {
				return fmt.Errorf("%w: a handoff constraint source is outside public context", ErrDelegationTaskAdmissionInvalid)
			}
		}
	}
	return nil
}

func delegatedTaskDraft(source DelegatedTaskSource, actorID string) (domaintask.Task, domaintask.SharedRoleContext, error) {
	contextText := strings.Builder{}
	for _, message := range source.Context {
		label := "Assistant"
		if message.Role == "user" {
			label = "User"
		}
		contextText.WriteString(label)
		contextText.WriteString(": ")
		contextText.WriteString(message.Text)
		contextText.WriteString("\n")
		if contextText.Len() > maxDelegatedTaskContext {
			return domaintask.Task{}, domaintask.SharedRoleContext{}, fmt.Errorf("%w: public context exceeds Task context bound", ErrDelegationTaskAdmissionInvalid)
		}
	}
	constraints := make([]string, 0, len(source.Handoff.Constraints))
	for _, constraint := range source.Handoff.Constraints {
		constraints = append(constraints, constraint.Key+": "+constraint.Text)
	}
	notes := "Accepted readback: " + source.Readback.Text + "\nExpected output: " + source.Handoff.ExpectedOutput + "\nPublic source context:\n" + contextText.String()
	if len(notes) > maxDelegatedTaskContext {
		return domaintask.Task{}, domaintask.SharedRoleContext{}, fmt.Errorf("%w: delegated Task notes exceed context bound", ErrDelegationTaskAdmissionInvalid)
	}
	task := domaintask.Task{
		Title: source.Handoff.Goal, Route: domaintask.RouteGeneral,
		OwnerID: source.OwnerID, Assignee: actorID,
		OriginSessionID: source.Handoff.SessionID, OriginTurnID: source.Handoff.OriginTurnID,
		OriginMessageID: source.Handoff.OriginUserMessageID,
		ReadOnly:        true,
	}
	shared := domaintask.SharedRoleContext{
		UserIntent: source.Handoff.Goal, CurrentPlan: source.Handoff.Instruction,
		Constraints: constraints, LatestStatus: "Accepted readback: " + source.Readback.UnderstoodGoal,
		HandoffNotes: notes,
	}
	return task, shared, nil
}
