package task

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestListTasksTaskIDCursorPagesPreserveFilters(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		name := "fallback"
		if indexed {
			name = "indexed"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{Index: indexed})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close task store: %v", err)
				}
			})

			now := time.Date(2026, time.November, 13, 10, 0, 0, 0, time.UTC)
			var expected []string
			for index := 0; index < 18; index++ {
				task := indexedTestTask(index, now)
				task.ModuleID = "indexed"
				task.Assignee = "ren"
				if index%2 == 0 {
					task.Assignee = "Mio"
				}
				if index == 4 {
					task.Status = domaintask.StatusRunning
				}
				if index == 6 {
					task.ModuleID = "other"
				}
				if index == 8 {
					task.Route = domaintask.RouteCode
				}
				if err := store.SaveTask(ctx, task); err != nil {
					t.Fatalf("SaveTask(%d): %v", index, err)
				}
				if index%2 == 0 && index != 4 && index != 6 && index != 8 {
					expected = append(expected, string(task.TaskID))
				}
			}
			sort.Strings(expected)

			var got []string
			var after modulecore.TaskID
			for {
				page, err := store.ListTasks(ctx, domaintask.Filter{
					Status: domaintask.StatusQueued, ModuleID: "indexed", Assignee: "mio",
					Route: domaintask.RouteGeneral, Limit: 2, TaskIDAfter: &after,
				})
				if err != nil {
					t.Fatalf("ListTasks after %q: %v", after, err)
				}
				if len(page) > 2 {
					t.Fatalf("page size = %d, want at most 2", len(page))
				}
				for _, task := range page {
					if task.Status != domaintask.StatusQueued || task.ModuleID != "indexed" || task.Route != domaintask.RouteGeneral || task.Assignee != "Mio" {
						t.Errorf("page returned Task outside filter: %+v", task)
					}
					got = append(got, string(task.TaskID))
				}
				if len(page) == 0 {
					break
				}
				if len(page) < 2 {
					break
				}
				after = page[len(page)-1].TaskID
			}
			if fmt.Sprint(got) != fmt.Sprint(expected) {
				t.Fatalf("paged TaskIDs = %v, want all matching IDs in ascending order %v", got, expected)
			}
		})
	}
}
