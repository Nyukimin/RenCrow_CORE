package main

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type runtimeDailySource struct {
	descriptor taskcenter.DailySourceDescriptor
	snapshot   func(context.Context, tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error)
}

func newRuntimeDailySource(
	descriptor taskcenter.DailySourceDescriptor,
	snapshot func(context.Context, tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error),
) *runtimeDailySource {
	return &runtimeDailySource{descriptor: cloneRuntimeDailySourceDescriptor(descriptor), snapshot: snapshot}
}

func (s *runtimeDailySource) Descriptor() taskcenter.DailySourceDescriptor {
	if s == nil {
		return taskcenter.DailySourceDescriptor{}
	}
	return cloneRuntimeDailySourceDescriptor(s.descriptor)
}

func (s *runtimeDailySource) Snapshot(ctx context.Context, scope tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
	if s == nil || s.snapshot == nil {
		return taskcenter.DailySourceSnapshot{}, errors.New("daily source reader is unavailable")
	}
	if err := scope.Validate(); err != nil {
		return taskcenter.DailySourceSnapshot{}, err
	}
	return s.snapshot(ctx, scope)
}

func cloneRuntimeDailySourceDescriptor(value taskcenter.DailySourceDescriptor) taskcenter.DailySourceDescriptor {
	value.Definitions = append([]taskcenter.DailyDefinitionDescriptor(nil), value.Definitions...)
	for index := range value.Definitions {
		value.Definitions[index].SourceRefs = append([]taskcenter.DailyResourceRef(nil), value.Definitions[index].SourceRefs...)
	}
	return value
}

func runtimeDailyAxis(state taskcenter.DailyAxisState, sourceRef, reasonCode string) taskcenter.DailyAxis {
	return taskcenter.DailyAxis{State: state, SourceRef: sourceRef, ReasonCode: reasonCode}
}

func runtimeDailyKnown[T any](value T, sourceRef string) taskcenter.DailyValue[T] {
	return taskcenter.DailyValue[T]{State: taskcenter.DailyValueKnown, SourceRef: sourceRef, Value: &value}
}

func runtimeDailyNone[T any](sourceRef, reasonCode string) taskcenter.DailyValue[T] {
	return taskcenter.DailyValue[T]{State: taskcenter.DailyValueNone, SourceRef: sourceRef, ReasonCode: reasonCode}
}

func runtimeDailyUnknown[T any](sourceRef, reasonCode string) taskcenter.DailyValue[T] {
	return taskcenter.DailyValue[T]{State: taskcenter.DailyValueUnknown, SourceRef: sourceRef, ReasonCode: reasonCode}
}

func runtimeDailyUnsupported[T any](sourceRef, reasonCode string) taskcenter.DailyValue[T] {
	return taskcenter.DailyValue[T]{State: taskcenter.DailyValueUnsupported, SourceRef: sourceRef, ReasonCode: reasonCode}
}

func runtimeDailyTimingUnknown(sourceRef, reasonCode string) taskcenter.DailyTimingProjection {
	return taskcenter.DailyTimingProjection{
		Timezone:                 runtimeDailyUnknown[string](sourceRef, reasonCode),
		ScheduledStartAt:         runtimeDailyUnknown[time.Time](sourceRef, reasonCode),
		NotBefore:                runtimeDailyUnknown[time.Time](sourceRef, reasonCode),
		AllowedWindows:           runtimeDailyUnknown[[]modulecore.TaskTimeWindow](sourceRef, reasonCode),
		DeadlineAt:               runtimeDailyUnknown[time.Time](sourceRef, reasonCode),
		Recurrence:               runtimeDailyUnknown[modulecore.TaskRecurrence](sourceRef, reasonCode),
		EstimatedDurationSeconds: runtimeDailyUnknown[int64](sourceRef, reasonCode),
	}
}

func runtimeDailyTimingUnsupported(sourceRef, reasonCode string) taskcenter.DailyTimingProjection {
	return taskcenter.DailyTimingProjection{
		Timezone:                 runtimeDailyUnsupported[string](sourceRef, reasonCode),
		ScheduledStartAt:         runtimeDailyUnsupported[time.Time](sourceRef, reasonCode),
		NotBefore:                runtimeDailyUnsupported[time.Time](sourceRef, reasonCode),
		AllowedWindows:           runtimeDailyUnsupported[[]modulecore.TaskTimeWindow](sourceRef, reasonCode),
		DeadlineAt:               runtimeDailyUnsupported[time.Time](sourceRef, reasonCode),
		Recurrence:               runtimeDailyUnsupported[modulecore.TaskRecurrence](sourceRef, reasonCode),
		EstimatedDurationSeconds: runtimeDailyUnsupported[int64](sourceRef, reasonCode),
	}
}

func runtimeDailyUnknownEvidence(sourceRef, reasonCode string) taskcenter.DailyEvidence {
	return taskcenter.DailyEvidence{
		State:         taskcenter.DailyValueUnknown,
		LastAttemptAt: runtimeDailyUnknown[time.Time](sourceRef, reasonCode),
		LastSuccessAt: runtimeDailyUnknown[time.Time](sourceRef, reasonCode),
		LastResult:    runtimeDailyUnknown[taskcenter.DailyOutcome](sourceRef, reasonCode),
		Refs:          []taskcenter.DailyResourceRef{},
	}
}

func runtimeDailyResourceRef(sourceID, ownerRef, resourceType, reference, nativeID, dataScope, displayName string) taskcenter.DailyResourceRef {
	return taskcenter.DailyResourceRef{
		SourceID: sourceID, OwnerRef: ownerRef, ResourceType: resourceType, Reference: reference,
		NativeID: nativeID, DataScope: dataScope, DisplayName: displayName,
	}
}

func registerTaskCenterDailySource(deps *Dependencies, source taskcenter.DailySource) {
	if deps == nil || source == nil {
		return
	}
	deps.taskCenterDailySources = append(deps.taskCenterDailySources, source)
}

func finalizeTaskCenterDaily(deps *Dependencies) {
	if deps == nil {
		return
	}
	center, err := taskcenter.NewCenter(deps.taskCenter, deps.taskCenterDailySources, time.Now)
	if err != nil {
		deps.taskCenterCenter = nil
		log.Printf("[TaskCenter] daily read facade unavailable: invalid owner source registration")
		return
	}
	deps.taskCenterCenter = center
}
