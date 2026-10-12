package viewer

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// Failure knowledge: docs/調査/*Run開始後の早期失敗でRunを閉じない_FailureKnowledge.md.
// A DCI search holds the long-research slot, the same one Memory Promotion
// holds, so its Run is closed under the same rule.

func serveDCISearch(t *testing.T, closure *taskmanagertest.Closure, searchErr error) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	NewDCISearchHandler(&stubDCISearcher{err: searchErr}, closure.Tasks, "ren", "shiro", []byte("owner-token")).ServeHTTP(rec, ownerDCIRequest(`{"query":"DCI"}`))
	return rec
}

func TestDCISearchHandlerClosesTheRunItStartedWhenTheSearchFails(t *testing.T) {
	cases := []struct {
		name       string
		searchErr  error
		wantTask   domaintask.Status
		wantRun    domaintask.RunStatus
		wantStatus int
	}{
		{"search error", taskmanagertest.ErrInjected, domaintask.StatusFailed, domaintask.RunStatusFailed, http.StatusInternalServerError},
		{"search cancelled", context.Canceled, domaintask.StatusCancelled, domaintask.RunStatusCancelled, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			closure := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})

			rec := serveDCISearch(t, closure, tc.searchErr)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			closure.AssertRunState(t, tc.wantTask, tc.wantRun)
			closure.AssertTerminalWritesBounded(t, false)
			closure.AssertSlotFree(t)
		})
	}
}

func TestDCISearchHandlerClosesTheRunItStartedWhenTheSearchSucceeds(t *testing.T) {
	closure := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{})

	rec := serveDCISearch(t, closure, nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	closure.AssertRunState(t, domaintask.StatusSucceeded, domaintask.RunStatusSucceeded)
	closure.AssertTerminalWritesBounded(t, false)
	closure.AssertSlotFree(t)
}

// The search failed and the Task store refuses the write that closes its Run.
// The response is the search failure, as before, but the refusal is not dropped:
// it is logged, and the Run is left running, the only state a refused write
// may leave.
func TestDCISearchHandlerReportsARunItCouldNotClose(t *testing.T) {
	closure := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{TaskTerminalErr: taskmanagertest.ErrTerminalRefused})
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })

	rec := serveDCISearch(t, closure, taskmanagertest.ErrInjected)

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "dci_search_failed") {
		t.Fatalf("status = %d body=%s, want the search failure", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logged.String(), "was not closed") || !strings.Contains(logged.String(), taskmanagertest.ErrTerminalRefused.Error()) {
		t.Fatalf("log = %q, want the refused close reported", logged.String())
	}
	closure.AssertRunLeftRunning(t)
}

// The search succeeded and the Task store refuses the write that closes its
// Run: the caller must not be told the search completed.
func TestDCISearchHandlerFailsWhenTheRunCannotBeClosedAfterASuccessfulSearch(t *testing.T) {
	closure := taskmanagertest.NewClosure(t, taskmanagertest.ClosureOptions{TaskTerminalErr: taskmanagertest.ErrTerminalRefused})

	rec := serveDCISearch(t, closure, nil)

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "dci_run_complete_failed") {
		t.Fatalf("status = %d body=%s, want dci_run_complete_failed", rec.Code, rec.Body.String())
	}
	closure.AssertRunLeftRunning(t)
}
