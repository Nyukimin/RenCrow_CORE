package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSessionLogWriterPersistsConversationIdentity(t *testing.T) {
	writer := NewSessionLogWriter(t.TempDir())
	taskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	writer.WriteUserWithIdentity("session-1", "viewer", "msg_user", string(traceID), "hello")
	writer.WriteAssistantWithIdentity("session-1", "viewer", "CHAT", string(taskID), "msg_assistant", string(traceID), "hi")

	file, err := os.Open(writer.pathFor("session-1"))
	if err != nil {
		t.Fatalf("open session log: %v", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var entries []SessionLogEntry
	for scanner.Scan() {
		var entry SessionLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decode session log: %v", err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan session log: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].MessageID != "msg_user" || entries[0].TraceID != string(traceID) {
		t.Fatalf("user identity = %#v", entries[0])
	}
	if entries[1].TaskID != taskID || entries[1].MessageID != "msg_assistant" || entries[1].TraceID != string(traceID) {
		t.Fatalf("assistant identity = %#v", entries[1])
	}
}

func TestSessionLogWriterLegacyAssistantDoesNotDeriveTraceIDFromTaskID(t *testing.T) {
	writer := NewSessionLogWriter(t.TempDir())
	taskID := modulecore.NewTaskID()
	writer.WriteAssistant("session-1", "viewer", "CHAT", string(taskID), "hi")

	data, err := os.ReadFile(writer.pathFor("session-1"))
	if err != nil {
		t.Fatalf("open session log: %v", err)
	}
	var entry SessionLogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("decode session log: %v", err)
	}
	if entry.TaskID != taskID || entry.TraceID != "" {
		t.Fatalf("legacy assistant identity = %#v, want task only", entry)
	}
	if string(data) == "" || bytes.Contains(data, []byte(`"job_id"`)) {
		t.Fatalf("session log contains retired field: %s", data)
	}
}

func TestSessionLogWriterRejectsMalformedAssistantTaskID(t *testing.T) {
	writer := NewSessionLogWriter(t.TempDir())
	writer.WriteAssistant("session-1", "viewer", "CHAT", "not-a-task-id", "hi")
	if _, err := os.Stat(writer.pathFor("session-1")); !os.IsNotExist(err) {
		t.Fatalf("malformed task_id must not be written, stat err=%v", err)
	}
}
