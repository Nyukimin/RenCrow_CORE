package task

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskActivityInventoryKeysetSummaryAndAuthorization(t *testing.T) {
	for _, indexed := range []bool{false, true} {
		name := "without index"
		if indexed {
			name = "with index"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, err := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{Index: indexed})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close: %v", err)
				}
			})
			now := time.Date(2026, 11, 21, 1, 2, 3, 0, time.UTC)
			public := indexedTestTask(1, now)
			public.Title = strings.Repeat("界", 250) + "\nsecret"
			public.Assignee = "mio"
			if err := store.SaveTask(ctx, public); err != nil {
				t.Fatalf("save public Task: %v", err)
			}
			private := indexedTestTask(2, now.Add(time.Second))
			private.Title = "private title"
			private.WorkstreamID = modulecore.NewWorkstreamID()
			private.GoalID = modulecore.NewGoalID()
			private.PrivateAccess = &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "user-a"}
			if err := store.SaveTaskAuthorized(ctx, private, func(context.Context, domaintask.TaskWriteAuthorizationInput) error { return nil }); err != nil {
				t.Fatalf("save private Task: %v", err)
			}
			shared := indexedTestTask(3, now.Add(2*time.Second))
			shared.Assignee = ""
			if err := store.SaveTask(ctx, shared); err != nil {
				t.Fatalf("save shared Task: %v", err)
			}

			authorize := func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
				if input.Scope == domaintask.TaskAccessScopeUser {
					return domaintask.ErrPrivateTaskAccessForbidden
				}
				return nil
			}
			want := []domaintask.Task{public, shared}
			sort.Slice(want, func(i, j int) bool { return want[i].TaskID > want[j].TaskID })
			page, err := store.ListTaskActivityInventory(ctx, domaintask.TaskActivityInventoryFilter{Limit: 1}, authorize)
			if err != nil {
				t.Fatalf("first inventory page: %v", err)
			}
			if len(page.Items) != 1 || page.Items[0].TaskID != want[0].TaskID || page.NextCursor == "" {
				t.Fatalf("first page = %+v, want newest authorized Task and continuation", page)
			}
			first := page.Items[0]
			page, err = store.ListTaskActivityInventory(ctx, domaintask.TaskActivityInventoryFilter{BeforeTaskID: page.NextCursor, Limit: 2}, authorize)
			if err != nil {
				t.Fatalf("second inventory page: %v", err)
			}
			if len(page.Items) != 1 || page.Items[0].TaskID != want[1].TaskID || page.NextCursor != "" {
				t.Fatalf("second page = %+v, want remaining authorized Task and no continuation", page)
			}
			second := page.Items[0]
			for _, summary := range []domaintask.TaskActivityInventoryRow{first, second} {
				wantScope := "shared"
				if summary.TaskID == public.TaskID {
					wantScope = "personal"
				}
				if summary.Scope != wantScope {
					t.Errorf("Task %s scope = %q, want %q", summary.TaskID, summary.Scope, wantScope)
				}
				if summary.TaskID == public.TaskID {
					if got := len([]rune(summary.Title)); got != 241 || strings.ContainsAny(summary.Title, "\r\n\t") {
						t.Fatalf("summary title rune count/control = %d/%q", got, summary.Title)
					}
				}
			}
			if strings.Contains(fmt.Sprint(page), "private title") {
				t.Fatal("unauthorized private Task title appeared in inventory")
			}
			_, err = store.ListTaskActivityInventory(ctx, domaintask.TaskActivityInventoryFilter{Limit: 2}, func(context.Context, domaintask.TaskReadAuthorizationInput) error {
				return errors.New("authorization backend failed")
			})
			if err == nil {
				t.Fatal("authorization backend error was ignored")
			}
		})
	}
}

func TestTaskActivityInventoryAuthorizationReceivesPublicationProof(t *testing.T) {
	ctx := context.Background()
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	item := indexedTestTask(1, time.Now().UTC())
	item.OwnerID = "mio" // Task ownership is an actor identity, not a User ID.
	item.OriginSessionID = modulecore.NewSessionID()
	item.OriginPublication = &domaintask.OriginPublicationProvenance{
		PublicationIncarnation: strings.Repeat("a", 32), Channel: "viewer", ChannelAddress: "cmd:owner-user",
		Audience: domainconversation.SessionAudiencePrivate, OwnerID: "owner-user",
	}
	if err := store.SaveTask(ctx, item); err != nil {
		t.Fatalf("save publication-bound Task: %v", err)
	}
	checks := 0
	page, err := store.ListTaskActivityInventory(ctx, domaintask.TaskActivityInventoryFilter{Limit: 10}, func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
		checks++
		if input.TaskID != item.TaskID || input.OriginSessionID != item.OriginSessionID || input.OriginPublication == nil || *input.OriginPublication != *item.OriginPublication {
			t.Fatalf("authorization input omitted origin publication proof: %#v", input)
		}
		return nil
	})
	if err != nil || checks < 2 || len(page.Items) != 1 || page.Items[0].TaskID != item.TaskID {
		t.Fatalf("inventory page=%#v checks=%d err=%v", page, checks, err)
	}
}

func TestTaskActivityInventoryExcludedStatusPreservesFilteredPagination(t *testing.T) {
	ctx := context.Background()
	store, err := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{Index: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	now := time.Date(2026, 11, 21, 1, 2, 3, 0, time.UTC)
	statuses := []domaintask.Status{domaintask.StatusQueued, domaintask.StatusSucceeded, domaintask.StatusFailed, domaintask.StatusCancelled}
	statusByID := make(map[modulecore.TaskID]domaintask.Status, len(statuses))
	var want []modulecore.TaskID
	for n, status := range statuses {
		item := indexedTestTask(n+1, now.Add(time.Duration(n)*time.Second))
		item.Status = status
		if err := store.SaveTask(ctx, item); err != nil {
			t.Fatalf("save status %s: %v", status, err)
		}
		statusByID[item.TaskID] = status
		if status != domaintask.StatusSucceeded {
			want = append(want, item.TaskID)
		}
	}
	sort.Slice(want, func(i, j int) bool { return want[i] > want[j] })
	filter := domaintask.TaskActivityInventoryFilter{ExcludeStatus: domaintask.StatusSucceeded, Limit: 2}
	page, err := store.ListTaskActivityInventory(ctx, filter, func(context.Context, domaintask.TaskReadAuthorizationInput) error { return nil })
	if err != nil || len(page.Items) != 2 || page.Items[0].TaskID != want[0] || page.Items[1].TaskID != want[1] || page.NextCursor == "" {
		t.Fatalf("first unfinished page = %+v, err=%v", page, err)
	}
	for _, item := range page.Items {
		if statusByID[item.TaskID] == domaintask.StatusSucceeded {
			t.Fatalf("succeeded Task was included in unfinished inventory: %+v", item)
		}
	}
	filter.BeforeTaskID = page.NextCursor
	page, err = store.ListTaskActivityInventory(ctx, filter, func(context.Context, domaintask.TaskReadAuthorizationInput) error { return nil })
	if err != nil || len(page.Items) != 1 || page.Items[0].TaskID != want[2] || page.NextCursor != "" {
		t.Fatalf("second unfinished page = %+v, err=%v", page, err)
	}
}
