package taskcenter

import (
	"context"
	"fmt"
	"reflect"
	"time"

	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Center composes the existing native Goal service with independent,
// read-only daily owner sources. Daily reads remain available when the
// managed Goal owner is not configured.
type Center struct {
	managed *Service
	sources []dailySourceEntry
	byID    map[string]dailyDefinitionEntry
	now     func() time.Time
}

type dailySourceEntry struct {
	source     DailySource
	descriptor DailySourceDescriptor
}

type dailyDefinitionEntry struct {
	sourceID   string
	descriptor DailyDefinitionDescriptor
}

// NewCenter snapshots source metadata once. Every ListDaily request still
// obtains a fresh, scope-specific snapshot from each relevant owner source.
func NewCenter(managed *Service, sources []DailySource, clock func() time.Time) (*Center, error) {
	if clock == nil {
		clock = time.Now
	}
	center := &Center{
		managed: managed,
		byID:    make(map[string]dailyDefinitionEntry),
		now:     clock,
	}
	sourceIDs := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if dailySourceIsNil(source) {
			return nil, fmt.Errorf("%w: invalid daily owner metadata", ErrInvalid)
		}
		descriptor := cloneDailySourceDescriptor(source.Descriptor())
		for index := range descriptor.Definitions {
			if descriptor.Definitions[index].SourceID != "" && descriptor.Definitions[index].SourceID != descriptor.SourceID {
				return nil, fmt.Errorf("%w: invalid daily owner metadata", ErrInvalid)
			}
			descriptor.Definitions[index].SourceID = descriptor.SourceID
		}
		if validateDailySourceDescriptor(descriptor) != nil {
			return nil, fmt.Errorf("%w: invalid daily owner metadata", ErrInvalid)
		}
		if _, exists := sourceIDs[descriptor.SourceID]; exists {
			return nil, fmt.Errorf("%w: duplicate daily source metadata", ErrInvalid)
		}
		sourceIDs[descriptor.SourceID] = struct{}{}
		for _, definition := range descriptor.Definitions {
			if _, exists := center.byID[definition.DefinitionID]; exists {
				return nil, fmt.Errorf("%w: duplicate daily definition metadata", ErrInvalid)
			}
			center.byID[definition.DefinitionID] = dailyDefinitionEntry{
				sourceID: descriptor.SourceID, descriptor: definition,
			}
		}
		center.sources = append(center.sources, dailySourceEntry{source: source, descriptor: descriptor})
	}
	return center, nil
}

func dailySourceIsNil(source DailySource) bool {
	if source == nil {
		return true
	}
	value := reflect.ValueOf(source)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (c *Center) Submit(ctx context.Context, input SubmitInput) (MutationResult, error) {
	if c == nil || c.managed == nil {
		return MutationResult{}, unavailableManagedOperation(ctx)
	}
	return c.managed.Submit(ctx, input)
}

func (c *Center) SubmitGameProjectPlan(ctx context.Context, input domainworkstream.GameProjectSubmission) (domainworkstream.ProjectPlanMutationResult, error) {
	if c == nil || c.managed == nil {
		return domainworkstream.ProjectPlanMutationResult{}, ErrUnsupported
	}
	return c.managed.SubmitGameProjectPlan(ctx, input)
}

func (c *Center) GameProjectPlanReceipt(ctx context.Context, idempotencyKey string) (domainworkstream.ProjectPlanMutationResult, bool, error) {
	if c == nil || c.managed == nil {
		return domainworkstream.ProjectPlanMutationResult{}, false, ErrUnsupported
	}
	return c.managed.GameProjectPlanReceipt(ctx, idempotencyKey)
}

func (c *Center) Claim(ctx context.Context, input ClaimInput) (MutationResult, error) {
	if c == nil || c.managed == nil {
		return MutationResult{}, unavailableManagedOperation(ctx)
	}
	return c.managed.Claim(ctx, input)
}

func (c *Center) Release(ctx context.Context, input ReleaseInput) (MutationResult, error) {
	if c == nil || c.managed == nil {
		return MutationResult{}, unavailableManagedOperation(ctx)
	}
	return c.managed.Release(ctx, input)
}

func (c *Center) List(ctx context.Context, query ListQuery) (ListResult, error) {
	if query.View == ViewDaily {
		if _, err := dailyScopeFromContext(ctx); err != nil {
			return ListResult{}, err
		}
		return ListResult{}, ErrUnsupported
	}
	if c == nil || c.managed == nil {
		return ListResult{}, unavailableManagedOperation(ctx)
	}
	return c.managed.List(ctx, query)
}

func (c *Center) Capabilities(ctx context.Context) (Capabilities, error) {
	if c == nil {
		scope, err := dailyScopeFromContext(ctx)
		if err != nil {
			return Capabilities{}, err
		}
		return dailyOnlyCapabilities(scope, false), nil
	}
	if c.managed != nil {
		capabilities, err := c.managed.Capabilities(ctx)
		if err != nil {
			return Capabilities{}, err
		}
		capabilities.ManagedAvailable = true
		capabilities.ManagedReasonCode = ""
		dailyScope, dailyScopeErr := dailyScopeFromContext(ctx)
		capabilities.DailyAvailable = dailyScopeErr == nil && c.hasVisibleDailyDefinitions(dailyScope.scope)
		if !capabilities.DailyAvailable {
			capabilities.DailyReasonCode = "daily_sources_unavailable"
		} else {
			capabilities.DailyReasonCode = ""
		}
		return capabilities, nil
	}
	scope, err := dailyScopeFromContext(ctx)
	if err != nil {
		return Capabilities{}, err
	}
	return dailyOnlyCapabilities(scope, c.hasVisibleDailyDefinitions(scope.scope)), nil
}

func (c *Center) InspectUserTaskCancellation(ctx context.Context, taskID modulecore.TaskID) (UserTaskCancellationResult, error) {
	if c == nil || c.managed == nil {
		return UserTaskCancellationResult{}, ErrUnsupported
	}
	return c.managed.InspectUserTaskCancellation(ctx, taskID)
}

func (c *Center) CancelUserOriginTask(ctx context.Context, taskID modulecore.TaskID) (UserTaskCancellationResult, error) {
	if c == nil || c.managed == nil {
		return UserTaskCancellationResult{}, ErrUnsupported
	}
	return c.managed.CancelUserOriginTask(ctx, taskID)
}

func (c *Center) hasVisibleDailyDefinitions(scope tool.ToolExecutionScope) bool {
	if c == nil {
		return false
	}
	for _, definition := range c.byID {
		if dailyDefinitionVisible(scope, definition.descriptor) {
			return true
		}
	}
	return false
}

func dailyOnlyCapabilities(scope dailyAuthorizedScope, dailyAvailable bool) Capabilities {
	capabilities := Capabilities{
		Kinds: []KindCapability{}, Agents: []string{}, OwnerRef: OwnerRefWorkstream, Revision: 1,
		CanClaim: false, ActorKind: string(scope.scope.ActorKind), ManagedAvailable: false,
		DailyAvailable: dailyAvailable, CanSubmit: false, ManagedReasonCode: "managed_owner_unavailable",
	}
	if !dailyAvailable {
		capabilities.DailyReasonCode = "daily_sources_unavailable"
	}
	return capabilities
}

func (c *Center) Get(ctx context.Context, goalID string) (Item, error) {
	if c == nil || c.managed == nil {
		return Item{}, unavailableManagedOperation(ctx)
	}
	return c.managed.Get(ctx, goalID)
}

func (c *Center) Receipt(ctx context.Context, operation, key string) (MutationResult, bool, error) {
	if c == nil || c.managed == nil {
		return MutationResult{}, false, unavailableManagedOperation(ctx)
	}
	return c.managed.Receipt(ctx, operation, key)
}

// ExecuteTaskCenterExecution forwards the managed execution operation without
// adding another authorization or execution decision at the facade layer.
func (c *Center) ExecuteTaskCenterExecution(ctx context.Context, input ExecutionInput) (ExecutionResult, error) {
	if c == nil || c.managed == nil {
		return ExecutionResult{}, unavailableManagedOperation(ctx)
	}
	return c.managed.ExecuteTaskCenterExecution(ctx, input)
}

// ReadResult forwards the scope-authorized canonical result read.
func (c *Center) ReadResult(ctx context.Context, goalID string) (ResultView, error) {
	if c == nil || c.managed == nil {
		return ResultView{}, unavailableManagedOperation(ctx)
	}
	return c.managed.ReadResult(ctx, goalID)
}

// AcceptResult forwards creator-only acceptance to the managed service.
func (c *Center) AcceptResult(ctx context.Context, input AcceptResultInput) (MutationResult, error) {
	if c == nil || c.managed == nil {
		return MutationResult{}, unavailableManagedOperation(ctx)
	}
	return c.managed.AcceptResult(ctx, input)
}

var _ TaskCenterExecutionCoordinator = (*Center)(nil)

func unavailableManagedOperation(ctx context.Context) error {
	if _, err := dailyScopeFromContext(ctx); err != nil {
		return err
	}
	return ErrUnavailable
}
