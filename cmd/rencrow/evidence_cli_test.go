package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type fakeEvidenceStore struct {
	items    []domainexecution.ExecutionReport
	byTaskID map[modulecore.TaskID]domainexecution.ExecutionReport
	summary  map[string]map[string]int
	err      error
}

func (f *fakeEvidenceStore) ListRecent(_ context.Context, _ int) ([]domainexecution.ExecutionReport, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func (f *fakeEvidenceStore) GetByTaskID(_ context.Context, taskID modulecore.TaskID) (domainexecution.ExecutionReport, error) {
	if f.err != nil {
		return domainexecution.ExecutionReport{}, f.err
	}
	if f.byTaskID == nil {
		return domainexecution.ExecutionReport{}, errors.New("not found")
	}
	r, ok := f.byTaskID[taskID]
	if !ok {
		return domainexecution.ExecutionReport{}, errors.New("not found")
	}
	return r, nil
}

func (f *fakeEvidenceStore) Summary(_ context.Context) (map[string]map[string]int, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.summary == nil {
		return map[string]map[string]int{}, nil
	}
	return f.summary, nil
}

func TestRunEvidenceCommand_List(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &fakeEvidenceStore{items: []domainexecution.ExecutionReport{{
		TaskID:     taskID,
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}}}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"list"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), taskID.String()) {
		t.Fatalf("expected output to include %s, got: %s", taskID, out.String())
	}
}

func TestRunEvidenceCommand_ShowMissingArg(t *testing.T) {
	store := &fakeEvidenceStore{}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"show"}, store, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "usage: rencrow evidence show <task_id>") {
		t.Fatalf("unexpected err output: %s", errOut.String())
	}
}

func TestRunEvidenceCommand_ShowRejectsNonCanonicalTaskID(t *testing.T) {
	store := &fakeEvidenceStore{}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"show", "task-not-canonical"}, store, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "invalid task_id") {
		t.Fatalf("unexpected err output: %s", errOut.String())
	}
}

func TestRunEvidenceCommand_Summary(t *testing.T) {
	store := &fakeEvidenceStore{summary: map[string]map[string]int{"status": {"passed": 1}}}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"summary"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "passed") {
		t.Fatalf("expected summary json output, got: %s", out.String())
	}
}

func TestRunEvidenceCommand_Unknown(t *testing.T) {
	store := &fakeEvidenceStore{}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"wat"}, store, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "unknown evidence subcommand") {
		t.Fatalf("unexpected err output: %s", errOut.String())
	}
}

func TestRunEvidenceCommand_ListJSON(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &fakeEvidenceStore{items: []domainexecution.ExecutionReport{{
		TaskID:     taskID,
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}}}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"list", "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}

	var payload struct {
		Items []domainexecution.ExecutionReport `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json output: %v (out=%s)", err, out.String())
	}
	if len(payload.Items) != 1 || payload.Items[0].TaskID != taskID {
		t.Fatalf("unexpected payload: %+v", payload.Items)
	}
}

func TestRunEvidenceCommand_ListJSONWithStatusFilter(t *testing.T) {
	passedTaskID := modulecore.NewTaskID()
	failedTaskID := modulecore.NewTaskID()
	store := &fakeEvidenceStore{items: []domainexecution.ExecutionReport{
		{TaskID: passedTaskID, Status: "passed", Goal: "ok", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
		{TaskID: failedTaskID, Status: "failed", ErrorKind: "verify", Goal: "ng", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
	}}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"list", "--status", "failed", "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	var payload struct {
		Items []domainexecution.ExecutionReport `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].TaskID != failedTaskID {
		t.Fatalf("unexpected filtered payload: %+v", payload.Items)
	}
}

func TestRunEvidenceCommand_ListTextWithErrorKindFilterNoMatch(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &fakeEvidenceStore{items: []domainexecution.ExecutionReport{
		{TaskID: taskID, Status: "passed", Goal: "ok", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
	}}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"list", "--error-kind", "verify"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	if !strings.Contains(out.String(), "No evidence records") {
		t.Fatalf("expected no records output, got: %s", out.String())
	}
}

func TestRunEvidenceCommand_ShowCompactJSON(t *testing.T) {
	taskID := modulecore.NewTaskID()
	store := &fakeEvidenceStore{
		byTaskID: map[modulecore.TaskID]domainexecution.ExecutionReport{
			taskID: {
				TaskID:     taskID,
				Goal:       "TTS実装して",
				Status:     "passed",
				CreatedAt:  time.Now().UTC(),
				FinishedAt: time.Now().UTC(),
			},
		},
	}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"show", taskID.String(), "--compact"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	got := out.String()
	if strings.Contains(got, "\n  \"") {
		t.Fatalf("expected compact json, got pretty output: %s", got)
	}
	if !strings.Contains(got, "\"task_id\":\""+taskID.String()+"\"") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestRunEvidenceCommand_SummaryCompactJSON(t *testing.T) {
	store := &fakeEvidenceStore{summary: map[string]map[string]int{"status": {"passed": 1}}}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"summary", "--compact"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	got := out.String()
	if strings.Contains(got, "\n  \"") {
		t.Fatalf("expected compact json, got pretty output: %s", got)
	}
	if !strings.Contains(got, "\"summary\"") {
		t.Fatalf("unexpected output: %s", got)
	}
}

func TestRunEvidenceCommand_SummaryWithFilters(t *testing.T) {
	passedTaskID := modulecore.NewTaskID()
	verifyTaskID := modulecore.NewTaskID()
	applyTaskID := modulecore.NewTaskID()
	store := &fakeEvidenceStore{items: []domainexecution.ExecutionReport{
		{TaskID: passedTaskID, Status: "passed", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
		{TaskID: verifyTaskID, Status: "failed", ErrorKind: "verify", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
		{TaskID: applyTaskID, Status: "failed", ErrorKind: "apply", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
	}}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"summary", "--status", "failed", "--error-kind", "verify", "--compact"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "\"failed\":1") {
		t.Fatalf("expected filtered failed=1 summary, got: %s", got)
	}
	if strings.Contains(got, "\"apply\":1") {
		t.Fatalf("did not expect apply count in filtered summary: %s", got)
	}
}

func TestRunEvidenceCommand_ListJSONWithSinceHours(t *testing.T) {
	now := time.Now().UTC()
	recentTaskID := modulecore.NewTaskID()
	oldTaskID := modulecore.NewTaskID()
	store := &fakeEvidenceStore{items: []domainexecution.ExecutionReport{
		{TaskID: recentTaskID, Status: "passed", CreatedAt: now.Add(-30 * time.Minute), FinishedAt: now.Add(-25 * time.Minute)},
		{TaskID: oldTaskID, Status: "failed", CreatedAt: now.Add(-5 * time.Hour), FinishedAt: now.Add(-5 * time.Hour)},
	}}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"list", "--since-hours", "1", "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	var payload struct {
		Items []domainexecution.ExecutionReport `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].TaskID != recentTaskID {
		t.Fatalf("unexpected since-hours filtered payload: %+v", payload.Items)
	}
}

func TestRunEvidenceCommand_SummaryWithSinceHours(t *testing.T) {
	now := time.Now().UTC()
	recentTaskID := modulecore.NewTaskID()
	oldTaskID := modulecore.NewTaskID()
	store := &fakeEvidenceStore{items: []domainexecution.ExecutionReport{
		{TaskID: recentTaskID, Status: "passed", CreatedAt: now.Add(-30 * time.Minute), FinishedAt: now.Add(-25 * time.Minute)},
		{TaskID: oldTaskID, Status: "failed", ErrorKind: "verify", CreatedAt: now.Add(-5 * time.Hour), FinishedAt: now.Add(-5 * time.Hour)},
	}}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"summary", "--since-hours", "1", "--compact"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("expected code 0, got %d (err=%s)", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "\"passed\":1") || strings.Contains(got, "\"failed\":1") {
		t.Fatalf("unexpected since-hours summary: %s", got)
	}
}

func TestRunEvidenceCommand_ListInvalidSinceHours(t *testing.T) {
	store := &fakeEvidenceStore{}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"list", "--since-hours", "bad"}, store, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "invalid --since-hours") {
		t.Fatalf("unexpected err output: %s", errOut.String())
	}
}

func TestRunEvidenceCommand_SummaryInvalidSinceHours(t *testing.T) {
	store := &fakeEvidenceStore{}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"summary", "--since-hours", "0"}, store, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "invalid --since-hours") {
		t.Fatalf("unexpected err output: %s", errOut.String())
	}
}

func TestRunEvidenceCommand_ListInvalidStatus(t *testing.T) {
	store := &fakeEvidenceStore{}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"list", "--status", "weird"}, store, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "invalid --status") {
		t.Fatalf("unexpected err output: %s", errOut.String())
	}
}

func TestRunEvidenceCommand_SummaryInvalidErrorKind(t *testing.T) {
	store := &fakeEvidenceStore{}
	var out, errOut bytes.Buffer

	code := runEvidenceCommand([]string{"summary", "--error-kind", "xxx"}, store, &out, &errOut)
	if code != 1 {
		t.Fatalf("expected code 1, got %d", code)
	}
	if !strings.Contains(errOut.String(), "invalid --error-kind") {
		t.Fatalf("unexpected err output: %s", errOut.String())
	}
}
