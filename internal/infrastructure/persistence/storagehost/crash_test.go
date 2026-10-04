package storagehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// crashStore commits to the owner store and can be made to lose the process
// right after the commit, before the idempotency outcome is recorded.
type crashStore struct {
	mu        sync.Mutex
	committed map[string]int
	failNext  bool
}

func (s *crashStore) op(ctx context.Context, payload json.RawMessage) (any, error) {
	var in struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, NewError(ErrorCodeSchemaRejected, "payload schema rejected")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.committed[in.Key]++
	return map[string]int{"count": s.committed[in.Key]}, nil
}

func (s *crashStore) executions(key string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.committed[key]
}

// TestCrashAfterCommitBeforeOutcomeJournalIsNotRetried is the failure the
// separate-journal design must survive: the store commit landed, the storage
// host died before recording the outcome. A retry with the same op_id must not
// append twice; it must report outcome_unknown so the owner resolves the
// commit through its own read-your-writes path.
func TestCrashAfterCommitBeforeOutcomeJournalIsNotRetried(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	store := &crashStore{committed: map[string]int{}}
	h.Register("task", "append", true, store.op)
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	h.crashAfterCommitFor = "crash-op"
	c.opID = "crash-op"
	var out map[string]int
	err = c.Call(context.Background(), "task", "append", map[string]any{"key": "run-1"}, &out)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("first call error=%v, want outcome_unknown after commit-side crash", err)
	}
	if store.executions("run-1") != 1 {
		t.Fatalf("executions=%d, want the commit to have landed once", store.executions("run-1"))
	}
	var replay map[string]int
	err = c.Call(context.Background(), "task", "append", map[string]any{"key": "run-1"}, &replay)
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry error=%v, want outcome_unknown, never a silent re-append", err)
	}
	if store.executions("run-1") != 1 {
		t.Fatalf("executions=%d, want 1: a begun outcome must never be re-executed blindly", store.executions("run-1"))
	}
}

// TestBegunOutcomeSurvivesStorageHostRestart covers the same crash across an
// actual storage host restart: the begun entry is durable, so the restarted
// host still refuses to re-execute the op_id.
func TestBegunOutcomeSurvivesStorageHostRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	h1, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	store := &crashStore{committed: map[string]int{}}
	h1.Register("task", "append", true, store.op)
	srv1 := httptest.NewServer(h1)
	c1, err := NewClient(ClientConfig{Endpoint: srv1.URL, Token: testToken, HTTPClient: srv1.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c1.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	h1.crashAfterCommitFor = "restart-crash-op"
	c1.opID = "restart-crash-op"
	var out map[string]int
	if err := c1.Call(context.Background(), "task", "append", map[string]any{"key": "run-2"}, &out); err == nil {
		srv1.Close()
		t.Fatalf("first call: want outcome_unknown after commit-side crash, got %v", out)
	}
	srv1.Close()
	h1.Close()

	h2, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler restart: %v", err)
	}
	h2.Register("task", "append", true, store.op)
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	c2, err := NewClient(ClientConfig{Endpoint: srv2.URL, Token: testToken, HTTPClient: srv2.Client()})
	if err != nil {
		t.Fatalf("NewClient restart: %v", err)
	}
	if err := c2.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake restart: %v", err)
	}
	if c2.Generation() <= c1.Generation() {
		t.Fatalf("writer generation must advance across restart: before=%d after=%d", c1.Generation(), c2.Generation())
	}
	c2.opID = "restart-crash-op"
	var replay map[string]int
	err = c2.Call(context.Background(), "task", "append", map[string]any{"key": "run-2"}, &replay)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry after restart error=%v, want outcome_unknown", err)
	}
	if store.executions("run-2") != 1 {
		t.Fatalf("executions=%d, want 1 after restart", store.executions("run-2"))
	}
}

// TestStaleGenerationAfterRestartIsRejected proves the writer epoch is the
// storage host's own monotonic value: a CORE that still holds the previous
// epoch cannot write across a storage host restart.
func TestStaleGenerationAfterRestartIsRejected(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	h1, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	store := &crashStore{committed: map[string]int{}}
	h1.Register("task", "append", true, store.op)
	srv1 := httptest.NewServer(h1)
	defer srv1.Close()
	c1, err := NewClient(ClientConfig{Endpoint: srv1.URL, Token: testToken, HTTPClient: srv1.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c1.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	srv1.Close()
	h1.Close()

	h2, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler restart: %v", err)
	}
	h2.Register("task", "append", true, store.op)
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	c1.httpClient = srv2.Client()
	c1.endpoint = srv2.URL + RPCPath
	var out Response
	req := Request{Contract: ContractVersion, OpID: "stale-gen-op", Group: "task", Op: "append", Generation: c1.Generation(), Payload: json.RawMessage(`{"key":"run-3"}`)}
	if err := c1.send(context.Background(), req, &out); err != nil {
		t.Fatalf("send: %v", err)
	}
	if out.Error == nil || out.Error.Code != ErrorCodeGenerationStale {
		t.Fatalf("error=%+v, want writer_generation_stale for the previous epoch", out.Error)
	}
	if store.executions("run-3") != 0 {
		t.Fatalf("executions=%d, want 0: a stale epoch may not write", store.executions("run-3"))
	}
}
