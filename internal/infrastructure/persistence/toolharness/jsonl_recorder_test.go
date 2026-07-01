package toolharness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/toolharness"
)

func TestJSONLRecorderRecordToolMediationEvent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool_mediation.jsonl")
	recorder, err := NewJSONLRecorder(path)
	if err != nil {
		t.Fatalf("NewJSONLRecorder failed: %v", err)
	}

	event := domain.Event{
		EventID:          "evt_tool_test",
		ToolName:         "file_read",
		RawInputHash:     "sha256:test",
		ValidationStatus: domain.ValidationStatusRepaired,
		Repairs: []domain.Repair{{
			Type:       "empty_placeholder_object_unwrap",
			Path:       []string{"args"},
			BeforeType: "map[string]interface {}",
			AfterType:  "map[string]interface {}",
		}},
		CreatedAt: time.Now().UTC(),
	}
	if err := recorder.RecordToolMediationEvent(event); err != nil {
		t.Fatalf("RecordToolMediationEvent failed: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("expected one event")
	}
	var got domain.Event
	if err := json.Unmarshal(scanner.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if got.EventID != event.EventID || got.ToolName != event.ToolName || got.ValidationStatus != event.ValidationStatus {
		t.Fatalf("unexpected event: %#v", got)
	}
	if scanner.Scan() {
		t.Fatal("expected exactly one event")
	}
}

func TestJSONLRecorderListRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tool_mediation.jsonl")
	recorder, err := NewJSONLRecorder(path)
	if err != nil {
		t.Fatalf("NewJSONLRecorder failed: %v", err)
	}

	for i := 0; i < 3; i++ {
		event := domain.Event{
			EventID:          fmt.Sprintf("evt_%d", i),
			ToolName:         "file_read",
			RawInputHash:     "sha256:test",
			ValidationStatus: domain.ValidationStatusValid,
			CreatedAt:        time.Now().UTC(),
		}
		if err := recorder.RecordToolMediationEvent(event); err != nil {
			t.Fatalf("RecordToolMediationEvent failed: %v", err)
		}
	}

	items, err := recorder.ListRecent(2)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if items[0].EventID != "evt_2" || items[1].EventID != "evt_1" {
		t.Fatalf("unexpected order: %#v", items)
	}
}
