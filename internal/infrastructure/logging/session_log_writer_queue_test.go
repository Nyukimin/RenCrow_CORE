package logging

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type contextAwareSessionLogWriter interface {
	WriteUserWithIdentityContext(context.Context, string, string, string, string, string) error
	WriteAssistantWithIdentityContext(context.Context, string, string, string, string, string, string, string) error
}

func TestSessionLogWriterContextWithoutAdmissionWritesSynchronously(t *testing.T) {
	writer := NewSessionLogWriter(t.TempDir())
	contextWriter, ok := any(writer).(contextAwareSessionLogWriter)
	if !ok {
		t.Fatal("session log writer does not expose context-aware identity writes")
	}
	traceID := modulecore.NewTraceID()
	if err := contextWriter.WriteUserWithIdentityContext(context.Background(), "session-sync", "cmd", "msg-user", string(traceID), "hello"); err != nil {
		t.Fatalf("write session log without admission: %v", err)
	}

	data, err := os.ReadFile(writer.pathFor("session-sync"))
	if err != nil {
		t.Fatalf("read session log immediately after write: %v", err)
	}
	var entry SessionLogEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("decode session log: %v", err)
	}
	if entry.Role != "user" || entry.MessageID != "msg-user" || entry.TraceID != string(traceID) || entry.Content != "hello" {
		t.Fatalf("synchronous session log entry = %#v", entry)
	}
}

func TestSessionLogWriterContextWithoutAdmissionReturnsWriteFailure(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(baseDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("prepare blocked path: %v", err)
	}
	writer := NewSessionLogWriter(baseDir)
	contextWriter, ok := any(writer).(contextAwareSessionLogWriter)
	if !ok {
		t.Fatal("session log writer does not expose context-aware identity writes")
	}
	if err := contextWriter.WriteUserWithIdentityContext(context.Background(), "session-sync-failure", "cmd", "msg-user", "trace-user", "hello"); err == nil {
		t.Fatal("synchronous session log failure was not returned to the caller")
	}
}

func TestSessionLogWriterContextQueuesRecordsInOrderAndReadsAfterWriterRestart(t *testing.T) {
	baseDir := t.TempDir()
	queue, err := persistencequeue.NewPersistenceQueue(3, 1<<20)
	if err != nil {
		t.Fatalf("create persistence queue: %v", err)
	}
	admission, err := persistencequeue.Admit(queue, 3, 1<<20)
	if err != nil {
		t.Fatalf("admit persistence commands: %v", err)
	}
	releaseWorker := make(chan struct{})
	workerStarted := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWorker) }) }
	t.Cleanup(func() {
		release()
		_ = admission.Release()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = queue.Shutdown(shutdownCtx)
	})

	if _, err := admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerSession,
		Kind:      "test.block",
		SizeBytes: 1,
		Apply: func(context.Context) error {
			close(workerStarted)
			<-releaseWorker
			return nil
		},
	}); err != nil {
		t.Fatalf("enqueue blocking command: %v", err)
	}
	select {
	case <-workerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("queue worker did not start the blocking command")
	}

	writer := NewSessionLogWriter(baseDir)
	contextWriter, ok := any(writer).(contextAwareSessionLogWriter)
	if !ok {
		t.Fatal("session log writer does not expose context-aware identity writes")
	}
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	taskID := modulecore.NewTaskID()
	traceID := modulecore.NewTraceID()
	if err := contextWriter.WriteUserWithIdentityContext(ctx, "session-queue", "cmd", "msg-user", string(traceID), "hello"); err != nil {
		t.Fatalf("enqueue user session log: %v", err)
	}
	if err := contextWriter.WriteAssistantWithIdentityContext(ctx, "session-queue", "cmd", "CHAT", string(taskID), "msg-assistant", string(traceID), "hi"); err != nil {
		t.Fatalf("enqueue assistant session log: %v", err)
	}

	path := writer.pathFor("session-queue")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session log reached disk while the shared writer was blocked, stat err=%v", err)
	}

	release()
	barrierCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("wait for accepted session logs: %v", err)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release admission: %v", err)
	}
	if err := queue.Shutdown(barrierCtx); err != nil {
		t.Fatalf("shutdown persistence queue: %v", err)
	}

	readerAfterRestart := NewSessionLogWriter(baseDir)
	data, err := os.Open(readerAfterRestart.pathFor("session-queue"))
	if err != nil {
		t.Fatalf("open persisted log with a fresh writer: %v", err)
	}
	defer data.Close()
	var entries []SessionLogEntry
	scanner := bufio.NewScanner(data)
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
		t.Fatalf("persisted entries = %d, want 2: %#v", len(entries), entries)
	}
	if entries[0].Role != "user" || entries[0].MessageID != "msg-user" || entries[0].TraceID != string(traceID) {
		t.Fatalf("user session log identity or order changed: %#v", entries[0])
	}
	if entries[1].Role != "assistant" || entries[1].TaskID != taskID || entries[1].MessageID != "msg-assistant" || entries[1].TraceID != string(traceID) {
		t.Fatalf("assistant session log identity or order changed: %#v", entries[1])
	}
}

func TestSessionLogWriterContextReturnsAdmissionErrorWithoutWriting(t *testing.T) {
	baseDir := t.TempDir()
	queue, err := persistencequeue.NewPersistenceQueue(1, 1)
	if err != nil {
		t.Fatalf("create persistence queue: %v", err)
	}
	admission, err := persistencequeue.Admit(queue, 1, 1)
	if err != nil {
		t.Fatalf("admit persistence command: %v", err)
	}
	t.Cleanup(func() {
		_ = admission.Release()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = queue.Shutdown(shutdownCtx)
	})

	writer := NewSessionLogWriter(baseDir)
	contextWriter, ok := any(writer).(contextAwareSessionLogWriter)
	if !ok {
		t.Fatal("session log writer does not expose context-aware identity writes")
	}
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	err = contextWriter.WriteUserWithIdentityContext(ctx, "session-full", "cmd", "msg-user", "trace-user", "hello")
	if !errors.Is(err, persistencequeue.ErrReservationBytes) {
		t.Fatalf("admission error = %v, want %v", err, persistencequeue.ErrReservationBytes)
	}
	if _, err := os.Stat(writer.pathFor("session-full")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected session log was written, stat err=%v", err)
	}
}
