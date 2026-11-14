package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentroutine"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var errRoutineWorkFactScope = errors.New("routine work fact scope is invalid")

const routineWorkTaskPageSize = 128

func collectRoutineWorkFacts(
	ctx context.Context,
	tasks *taskmanager.Manager,
	journal any,
	actorID, ownerID, date string,
) ([]agentroutine.Activity, error) {
	if ctx == nil || tasks == nil || !routineFactActorID(actorID) || ownerID == "" || strings.TrimSpace(ownerID) != ownerID {
		return nil, errRoutineWorkFactScope
	}
	dayStart, dayEnd, err := routineFactDay(date)
	if err != nil {
		return nil, errRoutineWorkFactScope
	}
	facts := make([]agentroutine.Activity, 0)

	// The readers return only owner-scoped canonical metadata, never message
	// bodies. Legacy conversation history remains out of scope.
	seenConversationEvents := make(map[string]struct{})
	if reader, ok := journal.(interface {
		ListRoutinePublicResponseFacts(context.Context, string, string, time.Time, time.Time) ([]eventstore.RoutinePublicResponseFact, error)
	}); ok {
		responses, err := reader.ListRoutinePublicResponseFacts(ctx, actorID, ownerID, dayStart, dayEnd)
		if err != nil {
			return nil, fmt.Errorf("list canonical public routine conversation facts: %w", err)
		}
		for _, response := range responses {
			if response.ActorID != actorID || response.EventID == "" || response.OccurredAt.Before(dayStart) || !response.OccurredAt.Before(dayEnd) {
				return nil, errors.New("canonical routine conversation fact violates its scope")
			}
			if _, duplicate := seenConversationEvents[response.EventID]; duplicate {
				continue
			}
			seenConversationEvents[response.EventID] = struct{}{}
			facts = append(facts, agentroutine.Activity{
				ActorID: actorID, Date: date, Key: "conversation:" + response.EventID,
				Kind: agentroutine.ActivityConversation, At: response.OccurredAt.UTC(),
				Source: "canonical_public_conversation", SourceID: response.EventID,
				Confirmed: true, Summary: "会話に応答した",
			})
		}
	}
	if reader, ok := journal.(interface {
		ListRoutineBoardPublicationFacts(context.Context, string, string, time.Time, time.Time) ([]eventstore.RoutineBoardPublicationFact, error)
	}); ok {
		responses, err := reader.ListRoutineBoardPublicationFacts(ctx, actorID, ownerID, dayStart, dayEnd)
		if err != nil {
			return nil, fmt.Errorf("list canonical BOARD routine conversation facts: %w", err)
		}
		seenBoardMessages := make(map[string]struct{})
		for _, response := range responses {
			if response.ActorID != actorID || response.BoardMessageID == "" || response.StoredAt.Before(dayStart) || !response.StoredAt.Before(dayEnd) {
				return nil, errors.New("canonical BOARD routine conversation fact violates its scope")
			}
			if response.EventID != "" {
				if _, duplicate := seenConversationEvents[response.EventID]; duplicate {
					continue
				}
				seenConversationEvents[response.EventID] = struct{}{}
			}
			if _, duplicate := seenBoardMessages[response.BoardMessageID]; duplicate {
				continue
			}
			seenBoardMessages[response.BoardMessageID] = struct{}{}
			facts = append(facts, agentroutine.Activity{
				ActorID: actorID, Date: date, Key: "conversation:board_publication:" + response.BoardMessageID,
				Kind: agentroutine.ActivityConversation, At: response.StoredAt.UTC(),
				Source: "canonical_board_publication", SourceID: response.BoardMessageID,
				Confirmed: true, Summary: "BOARDで会話に応答した",
			})
		}
	}

	var taskCursor modulecore.TaskID
	for {
		pageCursor := taskCursor
		canonicalTasks, err := tasks.List(ctx, domaintask.Filter{
			Assignee: actorID, Limit: routineWorkTaskPageSize, TaskIDAfter: &pageCursor,
		})
		if err != nil {
			return nil, fmt.Errorf("list canonical routine work task page: %w", err)
		}
		if len(canonicalTasks) == 0 {
			break
		}
		for _, task := range canonicalTasks {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if task.Assignee != actorID || task.OwnerID != ownerID || task.ModuleID == "agent_routine" {
				continue
			}
			if !taskMayHaveWorkOnRoutineDate(task, dayStart, dayEnd) {
				continue
			}

			runs, err := tasks.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
			if err != nil {
				return nil, fmt.Errorf("list canonical routine work runs: %w", err)
			}
			for _, run := range runs {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if run.TaskID != task.TaskID || run.Assignee != actorID {
					continue
				}
				if !domaintask.ValidRunStatus(run.Status) {
					return nil, errors.New("canonical routine work run has a noncanonical status")
				}
				facts = append(facts, runWorkActivities(run, actorID, date, dayStart, dayEnd)...)
			}
			if len(runs) == 0 && task.StartedAt != nil && domaintask.ValidStatus(task.Status) {
				facts = append(facts, taskWorkActivities(task, actorID, date, dayStart, dayEnd)...)
			}
		}
		nextCursor := canonicalTasks[len(canonicalTasks)-1].TaskID
		if nextCursor <= taskCursor {
			return nil, errors.New("canonical routine work task page cursor did not advance")
		}
		taskCursor = nextCursor
		if len(canonicalTasks) < routineWorkTaskPageSize {
			break
		}
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].At.IsZero() != facts[j].At.IsZero() {
			return !facts[i].At.IsZero()
		}
		if !facts[i].At.Equal(facts[j].At) {
			return facts[i].At.Before(facts[j].At)
		}
		return facts[i].Key < facts[j].Key
	})
	return facts, nil
}

func routineFactActorID(actorID string) bool {
	switch actorID {
	case "mio", "shiro", "midori", "kuro", "aka", "ao", "kin", "gin":
		return true
	default:
		return false
	}
}

func routineFactDay(date string) (time.Time, time.Time, error) {
	location, err := time.LoadLocation(agentroutine.JST)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start, err := time.ParseInLocation("2006-01-02", date, location)
	if err != nil || start.Format("2006-01-02") != date {
		return time.Time{}, time.Time{}, errRoutineWorkFactScope
	}
	return start, start.AddDate(0, 0, 1), nil
}

func taskMayHaveWorkOnRoutineDate(task domaintask.Task, dayStart, dayEnd time.Time) bool {
	if task.StartedAt != nil && !task.StartedAt.IsZero() {
		end := time.Time{}
		if task.FinishedAt != nil {
			end = *task.FinishedAt
		}
		return routineIntervalOverlapsDate(*task.StartedAt, end, dayStart, dayEnd)
	}
	// Older Tasks can lack StartedAt while still having a canonical Run. Their
	// create/update window is only a cheap candidate filter; it never emits a
	// work fact by itself.
	return routineIntervalOverlapsDate(task.CreatedAt, task.UpdatedAt, dayStart, dayEnd)
}

func routineIntervalOverlapsDate(start, end, dayStart, dayEnd time.Time) bool {
	if start.IsZero() || !start.Before(dayEnd) {
		return false
	}
	return end.IsZero() || !end.Before(start) && !end.Before(dayStart)
}

func taskWorkActivities(task domaintask.Task, actorID, date string, dayStart, dayEnd time.Time) []agentroutine.Activity {
	facts := make([]agentroutine.Activity, 0, 2)
	if at := timestampOnRoutineDate(optionalTaskTime(task.StartedAt), dayStart, dayEnd); !at.IsZero() {
		facts = append(facts, routineWorkActivity(actorID, date, "canonical_task", string(task.TaskID), "task:"+string(task.TaskID)+":started", at, "作業を開始した"))
	}
	if task.FinishedAt != nil {
		if at := timestampOnRoutineDate(*task.FinishedAt, dayStart, dayEnd); !at.IsZero() {
			if summary, ok := routineTaskCompletionSummary(task.Status); ok {
				facts = append(facts, routineWorkActivity(actorID, date, "canonical_task", string(task.TaskID), "task:"+string(task.TaskID)+":finished:"+string(task.Status), at, summary))
			}
		}
	}
	return facts
}

func runWorkActivities(run domaintask.Run, actorID, date string, dayStart, dayEnd time.Time) []agentroutine.Activity {
	facts := make([]agentroutine.Activity, 0, 2)
	if at := timestampOnRoutineDate(run.StartedAt, dayStart, dayEnd); !at.IsZero() {
		facts = append(facts, routineWorkActivity(actorID, date, "canonical_run", string(run.RunID), "run:"+string(run.RunID)+":started", at, "作業を開始した"))
	}
	if run.CompletedAt != nil {
		if at := timestampOnRoutineDate(*run.CompletedAt, dayStart, dayEnd); !at.IsZero() {
			facts = append(facts, routineWorkActivity(actorID, date, "canonical_run", string(run.RunID), "run:"+string(run.RunID)+":completed:"+string(run.Status), at, routineRunCompletionSummary(run.Status)))
		}
	}
	return facts
}

func routineWorkActivity(actorID, date, source, sourceID, key string, at time.Time, summary string) agentroutine.Activity {
	return agentroutine.Activity{
		ActorID: actorID, Date: date, Key: key, Kind: agentroutine.ActivityWork, At: at,
		Source: source, SourceID: sourceID, Confirmed: true, Summary: summary,
	}
}

func optionalTaskTime(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func timestampOnRoutineDate(candidate, dayStart, dayEnd time.Time) time.Time {
	if candidate.IsZero() || candidate.Before(dayStart) || !candidate.Before(dayEnd) {
		return time.Time{}
	}
	return candidate.UTC()
}

func routineTaskCompletionSummary(status domaintask.Status) (string, bool) {
	switch status {
	case domaintask.StatusSucceeded:
		return "作業を完了した", true
	case domaintask.StatusFailed:
		return "作業が失敗した", true
	case domaintask.StatusCancelled:
		return "作業を中止した", true
	case domaintask.StatusSuperseded:
		return "作業が置き換わった", true
	case domaintask.StatusBlocked:
		return "作業が停止状態になった", true
	case domaintask.StatusWaiting:
		return "作業が待機状態になった", true
	default:
		return "", false
	}
}

func routineRunCompletionSummary(status domaintask.RunStatus) string {
	switch status {
	case domaintask.RunStatusSucceeded:
		return "作業を完了した"
	case domaintask.RunStatusFailed:
		return "作業が失敗した"
	case domaintask.RunStatusCancelled:
		return "作業を中止した"
	case domaintask.RunStatusWaiting:
		return "作業が待機状態になった"
	case domaintask.RunStatusBlocked:
		return "作業が停止状態になった"
	case domaintask.RunStatusInterrupted:
		return "作業が中断された"
	case domaintask.RunStatusReassigned:
		return "作業の担当が変更された"
	case domaintask.RunStatusSuperseded:
		return "作業が置き換わった"
	default:
		return "作業状態が更新された"
	}
}
