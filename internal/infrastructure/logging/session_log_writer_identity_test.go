package logging

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
)

func TestSessionLogWriterPersistsConversationIdentity(t *testing.T) {
	writer := NewSessionLogWriter(t.TempDir())
	writer.WriteUserWithIdentity("session-1", "viewer", "msg_user", "trace-1", "hello")
	writer.WriteAssistantWithIdentity("session-1", "viewer", "CHAT", "job-1", "msg_assistant", "trace-1", "hi")

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
	if entries[0].MessageID != "msg_user" || entries[0].TraceID != "trace-1" {
		t.Fatalf("user identity = %#v", entries[0])
	}
	if entries[1].MessageID != "msg_assistant" || entries[1].TraceID != "trace-1" {
		t.Fatalf("assistant identity = %#v", entries[1])
	}
}
