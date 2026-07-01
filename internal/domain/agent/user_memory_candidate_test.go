package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	domainmemory "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/memory"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

func TestExtractUserMemoryCandidatePreferenceAndConstraints(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantType  string
		wantStmt  string
		wantEmpty bool
	}{
		{name: "self preference ga", input: "私はGoが好き", wantType: domainmemory.UserMemoryTypePreference, wantStmt: "Goが好き"},
		{name: "self preference wa", input: "僕は短い説明は好き", wantType: domainmemory.UserMemoryTypePreference, wantStmt: "短い説明が好き"},
		{name: "dislike", input: "長い前置きが嫌い", wantType: domainmemory.UserMemoryTypePreference, wantStmt: "長い前置きが嫌い"},
		{name: "constraint", input: "今後は結論から話して", wantType: domainmemory.UserMemoryTypeConstraint, wantStmt: "結論から話して"},
		{name: "question ignored", input: "Goが好き？", wantEmpty: true},
		{name: "explicit command ignored", input: "覚えて Goが好き", wantEmpty: true},
		{name: "route command ignored", input: "/status", wantEmpty: true},
		{name: "too long ignored", input: strings.Repeat("あ", 81), wantEmpty: true},
		{name: "long constraint ignored", input: "今後は" + strings.Repeat("あ", 61), wantEmpty: true},
		{name: "long subject ignored", input: strings.Repeat("あ", 41) + "が好き", wantEmpty: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractUserMemoryCandidate(tc.input)
			if tc.wantEmpty {
				if got.statement != "" {
					t.Fatalf("candidate=%#v, want empty", got)
				}
				return
			}
			if got.memoryType != tc.wantType || got.statement != tc.wantStmt || got.confidence <= 0 {
				t.Fatalf("candidate=%#v, want type=%q stmt=%q", got, tc.wantType, tc.wantStmt)
			}
		})
	}
}

func TestUserMemoryCandidateExistsAndCapture(t *testing.T) {
	mem := &mockUserMemoryManager{
		listItems: []domainmemory.UserMemory{{
			Statement: "Goが好き",
			State:     domainmemory.MemoryStateConfirmed,
			Active:    true,
		}},
	}
	m := (&MioAgent{}).WithUserMemoryManager(mem)
	exists, err := m.userMemoryCandidateExists(context.Background(), " Goが好き ")
	if err != nil {
		t.Fatalf("exists error: %v", err)
	}
	if !exists {
		t.Fatal("expected normalized duplicate to exist")
	}

	err = m.captureUserMemoryCandidate(context.Background(), task.NewTask(task.NewJobID(), "Goが好き", "viewer", "chat-1"))
	if err != nil {
		t.Fatalf("capture duplicate should not fail: %v", err)
	}
	if len(mem.createInputs) != 0 {
		t.Fatalf("duplicate should not be created: %#v", mem.createInputs)
	}

	mem.listItems = nil
	err = m.captureUserMemoryCandidate(context.Background(), task.NewTask(task.JobIDFromString("job-1"), "今後は結論から話して", "viewer", ""))
	if err != nil {
		t.Fatalf("capture failed: %v", err)
	}
	if len(mem.createInputs) != 1 {
		t.Fatalf("created=%#v", mem.createInputs)
	}
	input := mem.createInputs[0]
	if input.Type != domainmemory.UserMemoryTypeConstraint || input.Statement != "結論から話して" || !strings.Contains(input.EvidenceEventIDs[0], "unknown_session:job-1") {
		t.Fatalf("input=%#v", input)
	}
}

func TestCaptureUserMemoryCandidateListError(t *testing.T) {
	mem := &mockUserMemoryManager{listErr: errors.New("list failed")}
	m := (&MioAgent{}).WithUserMemoryManager(mem)
	err := m.captureUserMemoryCandidate(context.Background(), task.NewTask(task.NewJobID(), "Goが好き", "viewer", "chat-1"))
	if err == nil || !strings.Contains(err.Error(), "list failed") {
		t.Fatalf("err=%v, want list failed", err)
	}
}
