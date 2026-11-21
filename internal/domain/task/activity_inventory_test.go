package task

import (
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskActivityInventorySummaryIsBoundedAndUsesActivitySharing(t *testing.T) {
	now := time.Date(2026, 11, 21, 1, 2, 3, 0, time.UTC)
	base := Task{TaskID: modulecore.NewTaskID(), Title: strings.Repeat("界", 250) + "\ncontext", Status: StatusRunning,
		CreatedAt: now, UpdatedAt: now, Activity: &ActivityMetadata{Kind: ActivityKindGame, Label: "nethack", Phase: "playing", Revision: 4,
			Participants: []ActivityParticipant{{ActorID: "mio", Revision: 3, IsCurrent: true}, {ActorID: "shiro", Revision: 2}}}}
	row := TaskActivityInventorySummary(base)
	if row.Scope != "shared" || row.Activity == nil || row.Activity.Kind != ActivityKindGame || row.Activity.Revision != 4 {
		t.Fatalf("shared activity summary = %+v", row)
	}
	if got := len([]rune(row.Title)); got != 241 || strings.ContainsAny(row.Title, "\r\n\t") {
		t.Fatalf("summary title rune/control = %d/%q", got, row.Title)
	}
	if row.TaskID != base.TaskID || row.Status != base.Status || row.Assignee != base.Assignee || row.CreatedAt != base.CreatedAt {
		t.Fatalf("summary fields changed = %+v", row)
	}
	base.Activity.Participants = base.Activity.Participants[:1]
	if got := TaskActivityInventorySummary(base); got.Scope != "personal" {
		t.Fatalf("single-participant scope = %q, want personal", got.Scope)
	}
}

func TestTaskActivityInventorySummaryBoundsAndCleansFreeFormAssignee(t *testing.T) {
	row := TaskActivityInventorySummary(Task{Assignee: "worker-1\n" + strings.Repeat("界", 125)})
	if strings.ContainsAny(row.Assignee, "\r\n\t") || len([]rune(row.Assignee)) != 121 || !strings.HasSuffix(row.Assignee, "…") {
		t.Fatalf("assignee summary is not safely bounded: %q (%d runes)", row.Assignee, len([]rune(row.Assignee)))
	}
	if got := TaskActivityInventorySummary(Task{Assignee: "Mio"}).Assignee; got != "Mio" {
		t.Fatalf("free-form assignee changed: %q", got)
	}
}

func TestTaskActivityInventorySummaryUsesCanonicalGameIDWithoutSession(t *testing.T) {
	task := Task{Activity: &ActivityMetadata{
		Kind: ActivityKindGame, Label: "Playing nethack", Phase: "playing", Revision: 4,
		Game: &ActivityGameReference{GameID: "nethack", SessionID: "private-session"},
	}}
	row := TaskActivityInventorySummary(task)
	if row.Activity == nil || row.Activity.Label != "nethack" || strings.Contains(row.Activity.Label, "private-session") {
		t.Fatalf("game activity summary is not canonical and session-free: %+v", row.Activity)
	}
}

func TestTaskActivityInventorySummaryUsesFallbackForControlOnlyTitle(t *testing.T) {
	const canonical = "\x00\n\t"
	row := TaskActivityInventorySummary(Task{Title: canonical})
	if row.Title != "タイトル未設定" || canonical != "\x00\n\t" {
		t.Fatalf("safe title projection = %q; canonical title changed = %q", row.Title, canonical)
	}
}
