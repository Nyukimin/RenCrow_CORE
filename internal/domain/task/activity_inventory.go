package task

import (
	"context"
	"strings"
	"time"
	"unicode"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// TaskActivityInventoryFilter selects a bounded, keyset-paginated page of
// safe Task summaries for the authenticated owner viewer.
type TaskActivityInventoryFilter struct {
	BeforeTaskID  modulecore.TaskID
	Status        Status
	ExcludeStatus Status
	OpenOnly      bool
	Assignee      string
	Category      ActivityKind
	Limit         int
}

// TaskActivityInventoryRow contains only the fields needed to render the
// owner task inventory. It never carries Task context, prompts, or results.
type TaskActivityInventoryRow struct {
	TaskID     modulecore.TaskID              `json:"task_id"`
	Title      string                         `json:"title"`
	Status     Status                         `json:"status"`
	Assignee   string                         `json:"assignee,omitempty"`
	CreatedAt  time.Time                      `json:"created_at"`
	UpdatedAt  time.Time                      `json:"updated_at"`
	FinishedAt *time.Time                     `json:"finished_at,omitempty"`
	Activity   *TaskActivityInventoryActivity `json:"activity,omitempty"`
	Scope      string                         `json:"scope"`
}

// CurrentActivitySummary is the authorized, content-minimal projection of a
// Task referenced by the canonical current-activity binding.
type CurrentActivitySummary struct {
	TaskActivityInventoryRow
	Revision uint64 `json:"revision"`
}

type TaskActivityInventoryActivity struct {
	Kind     ActivityKind `json:"kind"`
	Label    string       `json:"label"`
	Phase    string       `json:"phase,omitempty"`
	Revision uint64       `json:"revision"`
}

type TaskActivityInventoryPage struct {
	Items      []TaskActivityInventoryRow `json:"items"`
	NextCursor modulecore.TaskID          `json:"next_cursor,omitempty"`
}

// TaskActivityInventoryReader is the narrow owner-authorized summary port.
// Authorization runs before any summary fields leave the persistence layer.
type TaskActivityInventoryReader interface {
	ListTaskActivityInventory(context.Context, TaskActivityInventoryFilter, TaskReadAuthorizationCheck) (TaskActivityInventoryPage, error)
}

// TaskActivityInventorySummary projects only fields allowed in the owner task
// inventory. It deliberately omits Task context and typed execution content.
func TaskActivityInventorySummary(task Task) TaskActivityInventoryRow {
	scope := "shared"
	if task.Activity == nil || len(task.Activity.Participants) < 2 {
		if task.Assignee != "" || task.Activity != nil {
			scope = "personal"
		}
	}
	row := TaskActivityInventoryRow{
		TaskID: task.TaskID, Title: safeTaskActivityTitle(task.Title), Status: task.Status, Assignee: safeTaskActivityAssignee(task.Assignee),
		CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, FinishedAt: task.FinishedAt, Scope: scope,
	}
	if task.Activity != nil {
		label := task.Activity.Label
		if task.Activity.Game != nil {
			label = task.Activity.Game.GameID
		}
		row.Activity = &TaskActivityInventoryActivity{
			Kind: task.Activity.Kind, Label: label, Phase: task.Activity.Phase, Revision: task.Activity.Revision,
		}
	}
	return row
}

func safeTaskActivityAssignee(raw string) string {
	var output strings.Builder
	runes := 0
	truncated := false
	for _, char := range raw {
		if unicode.IsControl(char) {
			char = ' '
		}
		if runes == 120 {
			truncated = true
			break
		}
		output.WriteRune(char)
		runes++
	}
	assignee := strings.TrimSpace(output.String())
	if truncated {
		assignee += "…"
	}
	return assignee
}

func safeTaskActivityTitle(raw string) string {
	var output strings.Builder
	runes := 0
	truncated := false
	for _, char := range raw {
		if unicode.IsControl(char) {
			char = ' '
		}
		if runes == 240 {
			truncated = true
			break
		}
		output.WriteRune(char)
		runes++
	}
	title := strings.TrimSpace(output.String())
	if title == "" {
		return "タイトル未設定"
	}
	if truncated {
		title += "…"
	}
	return title
}
