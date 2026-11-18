package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleTaskCenterPageServesDedicatedWorkbench(t *testing.T) {
	recorder := httptest.NewRecorder()
	HandleTaskCenterPage(recorder, httptest.NewRequest(http.MethodGet, "/viewer/task-center", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	body := recorder.Body.String()
	for _, expected := range []string{
		`<title>タスク管理 | RenCrow</title>`,
		`id="taskCenterConnectForm"`,
		`id="taskCenterWorkspace"`,
		`data-task-view="remaining"`,
		`data-task-view="claimable"`,
		`data-task-view="daily"`,
		`data-task-view="history"`,
		`id="taskCenterAddForm"`,
		`/viewer/assets/css/tabs/task-center.css`,
		`/viewer/assets/js/tabs/task-center.js`,
		`/viewer/assets/images/mio/mio_basic.png`,
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("task center page missing %q", expected)
		}
	}
}

func TestHandleTaskCenterPageRejectsNonGETMethods(t *testing.T) {
	recorder := httptest.NewRecorder()
	HandleTaskCenterPage(recorder, httptest.NewRequest(http.MethodPost, "/viewer/task-center", nil))

	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
	}
	if got := recorder.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow = %q, want %q", got, http.MethodGet)
	}
}
