package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeJournal seeds a journal directory with a raw journal.jsonl body.
func writeJournal(t *testing.T, dir, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "journal.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write journal: %v", err)
	}
	return path
}

func journalLine(opID, status string) string {
	return `{"op_id":"` + opID + `","group":"task","op":"append","payload_hash":"h","scope":"s","generation":1,"status":"` + status + `"}`
}

// TestJournalTornTrailingLineIsRepairedThenKept: a crash can leave a partial
// trailing record. Recovery must validate the boundary, repair the file, and
// keep the completed records across a later restart instead of appending onto
// the broken tail.
func TestJournalTornTrailingLineIsRepairedThenKept(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	good := journalLine("op-1", "done")
	path := writeJournal(t, dir, good+"\n"+`{"op_id":"op-torn","group":"task","status":"b`)

	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	if string(body) != good+"\n" {
		t.Fatalf("journal tail was not repaired: %q", string(body))
	}
	if _, found := h.journal.lookup("op-1"); !found {
		t.Fatalf("completed record lost during repair")
	}

	store := &crashStore{committed: map[string]int{}}
	if err := h.Register("task", "append", true, store.op); err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	var out map[string]int
	if err := c.Call(context.Background(), "task", "append", map[string]any{"key": "run-torn"}, &out); err != nil {
		t.Fatalf("append after repair: %v", err)
	}
	h.Close()
	srv.Close()

	h2, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler restart: %v", err)
	}
	defer h2.Close()
	if _, found := h2.journal.lookup("op-1"); !found {
		t.Fatalf("completed record lost after append and restart")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read journal: %v", err)
	}
	lines := strings.Split(string(after), "\n")
	if len(lines) == 0 || lines[len(lines)-1] != "" {
		t.Fatalf("journal does not end on a complete record boundary: %q", string(after))
	}
	for _, line := range lines[:len(lines)-1] {
		var rec JournalEntry
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("journal line is not a complete record: %q: %v", line, err)
		}
	}
}

// TestJournalMidStreamCorruptionFailsClosed: corruption in the middle of the
// journal is not a crash tail. Ignoring it silently drops the dedup identity of
// a committed operation.
func TestJournalMidStreamCorruptionFailsClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	writeJournal(t, dir, journalLine("op-1", "done")+"\nnot json at all\n"+journalLine("op-2", "done")+"\n")

	if _, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir}); err == nil {
		t.Fatalf("NewHandler accepted a corrupt mid-stream journal record")
	} else if !errors.Is(err, ErrJournalCorrupt) {
		t.Fatalf("error=%v, want ErrJournalCorrupt", err)
	}
}

// TestJournalDirRejectsSecondWriter: the storage host is the single writer for
// its durable directory. A second handler over the same directory must be
// refused before the generation file is read or the journal recovered.
func TestJournalDirRejectsSecondWriter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	h1, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h1.Close()

	h2, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err == nil {
		h2.Close()
		t.Fatalf("second handler opened the same journal directory")
	}
}

// committedThenErrorStore commits to the owner store and then fails, which a
// plain error cannot prove was rolled back.
type committedThenErrorStore struct {
	mu       sync.Mutex
	executed int
}

func (s *committedThenErrorStore) op(ctx context.Context, payload json.RawMessage) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executed++
	return nil, errors.New("owner operation failed after the commit")
}

func (s *committedThenErrorStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executed
}

// rolledBackError proves the owner undid its own work, so the op_id may be
// released and re-executed.
type rolledBackError struct{}

func (rolledBackError) Error() string { return "owner rolled back" }

func (rolledBackError) RolledBack() bool { return true }

type rollbackStore struct {
	mu       sync.Mutex
	executed int
}

func (s *rollbackStore) op(ctx context.Context, payload json.RawMessage) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executed++
	return nil, rolledBackError{}
}

func (s *rollbackStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executed
}

type countingStore interface {
	op(ctx context.Context, payload json.RawMessage) (any, error)
	count() int
}

func newStoreFixture(t *testing.T, dir string, store countingStore) (*Handler, *Client) {
	t.Helper()
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if err := h.Register("task", "append", true, store.op); err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	return h, c
}

// TestOwnerErrorAfterCommitKeepsBegun: aborting the journal entry on every
// callback error re-opens the operation whenever the owner committed and then
// failed. Without a proven rollback the entry must stay begun and the retry
// must report outcome_unknown instead of executing twice.
func TestOwnerErrorAfterCommitKeepsBegun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &committedThenErrorStore{}
	h, c := newStoreFixture(t, dir, store)
	defer h.Close()

	var out map[string]int
	err := c.Call(context.Background(), "task", "append", map[string]any{"key": "run-x"}, &out)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("first call error=%v, want outcome_unknown for an unproven rollback", err)
	}
	if entry, found := h.journal.lookup(c.lastOpID); !found || entry.Status != journalStatusBegun {
		t.Fatalf("journal entry after unproven rollback: %+v found=%v", entry, found)
	}

	err = c.Call(context.Background(), "task", "append", map[string]any{"key": "run-x"}, &out)
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry error=%v, want outcome_unknown", err)
	}
	if store.count() != 1 {
		t.Fatalf("executions=%d, want 1: the operation may not run twice", store.count())
	}
}

// TestProvenRollbackReleasesOpID: only an owner error that proves the rollback
// releases the op_id for re-execution.
func TestProvenRollbackReleasesOpID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &rollbackStore{}
	h, c := newStoreFixture(t, dir, store)
	defer h.Close()

	var out map[string]int
	err := c.Call(context.Background(), "task", "append", map[string]any{"key": "run-y"}, &out)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeStoreUnavailable {
		t.Fatalf("first call error=%v, want the typed store error after a proven rollback", err)
	}
	if _, found := h.journal.lookup(c.lastOpID); found {
		t.Fatalf("proven rollback left a journal entry")
	}
	err = c.Call(context.Background(), "task", "append", map[string]any{"key": "run-y"}, &out)
	if !errors.As(err, &e) || e.Code != ErrorCodeStoreUnavailable {
		t.Fatalf("retry error=%v, want the typed store error", err)
	}
	if store.count() != 2 {
		t.Fatalf("executions=%d, want 2: a proven rollback may be re-executed", store.count())
	}
}

// corruptOnce wraps the handler and answers the first mutating request with
// a body that cannot be decoded, after the operation itself has run.
func corruptOnce(h *Handler) http.Handler {
	var mu sync.Mutex
	corrupted := false
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "could not read test request", http.StatusBadRequest)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		mu.Lock()
		isMutating := bytes.Contains(body, []byte(`"group":"task"`)) && !bytes.Contains(body, []byte(`"group":"host"`))
		shouldCorrupt := isMutating && !corrupted
		if shouldCorrupt {
			corrupted = true
		}
		mu.Unlock()
		if !shouldCorrupt {
			h.ServeHTTP(w, r)
			return
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "8")
		w.WriteHeader(rec.Code)
		_, _ = io.WriteString(w, "{corrupt")
	})
}

// TestCorruptResponseAfterCommitResolvesOutcome: a response that cannot be
// decoded is not proof that the operation never ran. The client must resolve
// the outcome through the journal rather than report a plain failure.
func TestCorruptResponseAfterCommitResolvesOutcome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &crashStore{committed: map[string]int{}}
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()
	if err := h.Register("task", "append", true, store.op); err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(corruptOnce(h))
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	var out map[string]int
	if err := c.Call(context.Background(), "task", "append", map[string]any{"key": "run-c"}, &out); err != nil {
		t.Fatalf("call with corrupt response: %v", err)
	}
	if out["count"] != 1 {
		t.Fatalf("result=%v, want the committed result resolved from the journal", out)
	}
	if store.executions("run-c") != 1 {
		t.Fatalf("executions=%d, want 1", store.executions("run-c"))
	}
}

// TestOperationLifetimeReusesOpID: a caller must be able to retry the very same
// operation after outcome_unknown. A fresh op_id per call would append twice.
func TestOperationLifetimeReusesOpID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &crashStore{committed: map[string]int{}}
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()
	if err := h.Register("task", "append", true, store.op); err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	op := c.NewOperation("task", "append", map[string]any{"key": "run-life"})
	var out map[string]int
	c.sendHook = func() error {
		c.sendHook = nil
		return errConnectionResetAfterSend
	}
	if err := c.Do(context.Background(), op, &out); err == nil {
		t.Fatalf("first call: want an error when the response is lost")
	}
	if entry, found := h.journal.lookup(op.OpID); !found {
		t.Fatalf("the caller's op_id was not the journaled operation: %+v found=%v", entry, found)
	}

	// The result query is down: the outcome stays unknown, and the caller keeps
	// the same operation instead of minting a new op_id.
	c.resultLookupBroken = true
	err = c.Do(context.Background(), op, &out)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("error=%v, want outcome_unknown while the result query is down", err)
	}
	c.resultLookupBroken = false

	if err := c.Do(context.Background(), op, &out); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if out["count"] != 1 {
		t.Fatalf("result=%v, want the single committed result", out)
	}
	if store.executions("run-life") != 1 {
		t.Fatalf("executions=%d, want 1 across the unknown window", store.executions("run-life"))
	}
}

func TestOperationMalformedDoneResultStaysUnknown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	var mu sync.Mutex
	executions := 0
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()
	if err := h.Register("task", "append", true, func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		executions++
		return "not-an-object", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	op := c.NewOperation("task", "append", map[string]any{"key": "operation-malformed"})
	opID := op.OpID
	var out map[string]int
	err = c.Do(context.Background(), op, &out)
	if !isOutcomeUnknown(err) {
		t.Fatalf("direct DONE decode error=%v, want outcome_unknown", err)
	}
	if op.OpID != opID {
		t.Fatalf("operation op_id after direct decode failure=%q, want %q", op.OpID, opID)
	}
	err = c.Do(context.Background(), op, &out)
	if !isOutcomeUnknown(err) {
		t.Fatalf("resolved DONE decode error=%v, want outcome_unknown", err)
	}
	if op.OpID != opID {
		t.Fatalf("operation op_id after result lookup=%q, want %q", op.OpID, opID)
	}
	mu.Lock()
	defer mu.Unlock()
	if executions != 1 {
		t.Fatalf("owner executions=%d, want one", executions)
	}
}

// typedErrorAfterCommitStore commits to the owner store and then fails with a
// typed protocol error. A typed code alone does not prove the commit was
// undone: the owner may commit first and surface the failure afterwards.
type typedErrorAfterCommitStore struct {
	mu       sync.Mutex
	executed int
}

func (s *typedErrorAfterCommitStore) op(ctx context.Context, payload json.RawMessage) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executed++
	return nil, NewError(ErrorCodeStoreUnavailable, "owner failed after the commit")
}

func (s *typedErrorAfterCommitStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.executed
}

// TestArbitraryTypedErrorAfterCommitKeepsBegun: an owner error carrying a
// typed code (even store_unavailable) is not rollback proof. Only an explicit
// RolledBack()==true may release the op_id; otherwise the begun entry must
// survive and the retry must be outcome_unknown without a second execution.
func TestArbitraryTypedErrorAfterCommitKeepsBegun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &typedErrorAfterCommitStore{}
	h, c := newStoreFixture(t, dir, store)
	defer h.Close()

	var out map[string]int
	err := c.Call(context.Background(), "task", "append", map[string]any{"key": "typed"}, &out)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("first call error=%v, want outcome_unknown: a typed callback error is not rollback proof", err)
	}
	if entry, found := h.journal.lookup(c.lastOpID); !found || entry.Status != journalStatusBegun {
		t.Fatalf("journal entry after unproven typed error: %+v found=%v", entry, found)
	}

	err = c.Call(context.Background(), "task", "append", map[string]any{"key": "typed"}, &out)
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry error=%v, want outcome_unknown", err)
	}
	if store.count() != 1 {
		t.Fatalf("executions=%d, want 1: the operation may not run twice", store.count())
	}
}

// TestOrdinaryCallRetainsOpIDWhenResolutionUnknown: an ordinary Call (no
// pinned op_id, no explicit Operation) that commits at the owner, loses the
// response, and cannot resolve the outcome must retain the SAME op_id for the
// next Call. A fresh op_id per Call would execute the operation twice.
func TestOrdinaryCallRetainsOpIDWhenResolutionUnknown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &crashStore{committed: map[string]int{}}
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()
	if err := h.Register("task", "append", true, store.op); err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	// The commit lands at the owner, the response is lost, and the result
	// query is down: the outcome stays unknown.
	c.resultLookupBroken = true
	c.sendHook = func() error {
		c.sendHook = nil
		return errConnectionResetAfterSend
	}
	var out map[string]int
	err = c.Call(context.Background(), "task", "append", map[string]any{"key": "retain"}, &out)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("first call error=%v, want outcome_unknown while the result query is down", err)
	}
	firstOpID := c.lastOpID
	if firstOpID == "" {
		t.Fatalf("no op_id recorded for the unknown outcome")
	}

	// The very next ordinary Call must reuse that op_id: the host replays the
	// recorded outcome instead of executing a second time.
	err = c.Call(context.Background(), "task", "append", map[string]any{"key": "retain"}, &out)
	if err != nil {
		t.Fatalf("retry after lost response: %v", err)
	}
	if out["count"] != 1 {
		t.Fatalf("retry result=%v, want the single committed outcome", out)
	}
	if c.lastOpID != firstOpID {
		t.Fatalf("retry minted a fresh op_id %q instead of retaining %q", c.lastOpID, firstOpID)
	}
	if store.executions("retain") != 1 {
		t.Fatalf("executions=%d, want at most one commit across the unknown window", store.executions("retain"))
	}
}

// TestMalformedDoneResultLookupStaysOutcomeUnknown: a host.result lookup that
// finds the op_id done but whose stored payload cannot be decoded must report
// explicit uncertainty (outcome_unknown), never a generic schema_rejected that
// consumers may treat as a safe retry.
func TestMalformedDoneResultLookupStaysOutcomeUnknown(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := func(ctx context.Context, payload json.RawMessage) (any, error) {
		return "not-an-object", nil
	}
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()
	if err := h.Register("task", "append", true, store); err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}

	// Commit once with a pinned op_id; the stored result is a JSON string
	// while the caller decodes a map, so the journal holds a done entry whose
	// payload this consumer cannot decode.
	c.opID = "mal-op"
	var ignored map[string]int
	if err := c.Call(context.Background(), "task", "append", map[string]any{"key": "mal"}, &ignored); err == nil {
		t.Fatalf("first call unexpectedly decoded")
	}
	c.opID = ""

	// The response is lost on the next round trip; resolution finds the done
	// record but its payload is malformed for this consumer.
	c.sendHook = func() error {
		c.sendHook = nil
		return errConnectionResetAfterSend
	}
	var out map[string]int
	err = c.Call(context.Background(), "task", "append", map[string]any{"key": "mal"}, &out)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("resolution error=%v, want outcome_unknown: a malformed done result is not a generic schema rejection", err)
	}
}

func newCrashCallFixture(t *testing.T, dir string, store *crashStore) (*Handler, *httptest.Server, *Client) {
	t.Helper()
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if err := h.Register("task", "append", true, store.op); err != nil {
		h.Close()
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(h)
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		srv.Close()
		h.Close()
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		srv.Close()
		h.Close()
		t.Fatalf("Handshake: %v", err)
	}
	return h, srv, c
}

func loseNextResponse(c *Client) {
	c.sendHook = func() error {
		c.sendHook = nil
		return errConnectionResetAfterSend
	}
}

type requestBarrierTransport struct {
	base    http.RoundTripper
	entered chan string
	release <-chan struct{}
}

func (t *requestBarrierTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	if req.Group == "task" && req.Op == "append" {
		t.entered <- req.OpID
		<-t.release
	}
	return t.base.RoundTrip(r)
}

type mixedOutcomeTransport struct {
	base    http.RoundTripper
	entered chan string
	release <-chan struct{}

	mu       sync.Mutex
	requests int
}

func (t *mixedOutcomeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req Request
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	if req.Group != "task" || req.Op != "append" {
		return t.base.RoundTrip(r)
	}
	t.mu.Lock()
	t.requests++
	requestNumber := t.requests
	t.mu.Unlock()
	t.entered <- req.OpID
	<-t.release
	resp, err := t.base.RoundTrip(r)
	if requestNumber == 2 && err == nil {
		resp.Body.Close()
		return nil, errors.New("storagehost test: drop one response after owner handling")
	}
	return resp, err
}

func TestInterleavedUnknownCallsKeepIndependentIDs(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &crashStore{committed: map[string]int{}}
	h, srv, c := newCrashCallFixture(t, dir, store)
	defer h.Close()
	defer srv.Close()

	c.resultLookupBroken = true
	loseNextResponse(c)
	var outA, outB map[string]int
	if err := c.Call(context.Background(), "task", "append", map[string]any{"key": "unknown-a"}, &outA); !isOutcomeUnknown(err) {
		t.Fatalf("first A call error=%v, want outcome_unknown", err)
	}
	idA := c.lastOpID
	loseNextResponse(c)
	if err := c.Call(context.Background(), "task", "append", map[string]any{"key": "unknown-b"}, &outB); !isOutcomeUnknown(err) {
		t.Fatalf("first B call error=%v, want outcome_unknown", err)
	}
	idB := c.lastOpID
	if idA == idB {
		t.Fatalf("distinct calls shared op_id %q", idA)
	}

	c.resultLookupBroken = false
	if err := c.Call(context.Background(), "task", "append", map[string]any{"key": "unknown-a"}, &outA); err != nil {
		t.Fatalf("retry A: %v", err)
	}
	if c.lastOpID != idA {
		t.Fatalf("retry A used op_id %q, want retained %q", c.lastOpID, idA)
	}
	if err := c.Call(context.Background(), "task", "append", map[string]any{"key": "unknown-b"}, &outB); err != nil {
		t.Fatalf("retry B: %v", err)
	}
	if c.lastOpID != idB {
		t.Fatalf("retry B used op_id %q, want retained %q", c.lastOpID, idB)
	}
	if store.executions("unknown-a") != 1 || store.executions("unknown-b") != 1 {
		t.Fatalf("executions A=%d B=%d, want one each", store.executions("unknown-a"), store.executions("unknown-b"))
	}
}

func TestConcurrentRetryOfUnknownCallKeepsOneID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &crashStore{committed: map[string]int{}}
	h, srv, c := newCrashCallFixture(t, dir, store)
	defer h.Close()
	defer srv.Close()

	c.resultLookupBroken = true
	loseNextResponse(c)
	var first map[string]int
	if err := c.Call(context.Background(), "task", "append", map[string]any{"key": "concurrent"}, &first); !isOutcomeUnknown(err) {
		t.Fatalf("initial call error=%v, want outcome_unknown", err)
	}
	firstID := c.lastOpID
	c.resultLookupBroken = false

	baseClient := c.httpClient
	release := make(chan struct{})
	barrier := &requestBarrierTransport{base: baseClient.Transport, entered: make(chan string, 2), release: release}
	c.httpClient = &http.Client{Transport: barrier, Timeout: baseClient.Timeout}

	type result struct {
		out map[string]int
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			var out map[string]int
			err := c.Call(context.Background(), "task", "append", map[string]any{"key": "concurrent"}, &out)
			results <- result{out: out, err: err}
		}()
	}
	var ids [2]string
	for i := range ids {
		select {
		case ids[i] = <-barrier.entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatalf("only %d concurrent retry request(s) reached transport", i)
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		if got := <-results; got.err != nil {
			t.Fatalf("concurrent retry %d: %v", i, got.err)
		}
	}
	if ids[0] != firstID || ids[1] != firstID {
		t.Fatalf("concurrent retry IDs=%v, want both to reuse %q", ids, firstID)
	}
	if store.executions("concurrent") != 1 {
		t.Fatalf("executions=%d, want one owner commit across concurrent retries", store.executions("concurrent"))
	}
}

func TestMixedConcurrentOutcomeRetainsUnknownCallIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &crashStore{committed: map[string]int{}}
	h, srv, c := newCrashCallFixture(t, dir, store)
	defer h.Close()
	defer srv.Close()

	payload := map[string]any{"key": "mixed-outcome"}
	c.resultLookupBroken = true
	loseNextResponse(c)
	var initial map[string]int
	if err := c.Call(context.Background(), "task", "append", payload, &initial); !isOutcomeUnknown(err) {
		t.Fatalf("initial call error=%v, want outcome_unknown", err)
	}
	originalID := c.lastOpID

	baseClient := c.httpClient
	release := make(chan struct{})
	transport := &mixedOutcomeTransport{
		base:    baseClient.Transport,
		entered: make(chan string, 3),
		release: release,
	}
	c.httpClient = &http.Client{Transport: transport, Timeout: baseClient.Timeout}

	type result struct {
		err error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			var out map[string]int
			results <- result{err: c.Call(context.Background(), "task", "append", payload, &out)}
		}()
	}
	var concurrentIDs [2]string
	for i := range concurrentIDs {
		select {
		case concurrentIDs[i] = <-transport.entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatalf("only %d concurrent retry request(s) reached transport", i)
		}
	}
	close(release)
	var successes, unknowns int
	for i := 0; i < 2; i++ {
		got := <-results
		switch {
		case got.err == nil:
			successes++
		case isOutcomeUnknown(got.err):
			unknowns++
		default:
			t.Fatalf("concurrent retry error=%v, want one success and one outcome_unknown", got.err)
		}
	}
	if successes != 1 || unknowns != 1 {
		t.Fatalf("concurrent outcomes: successes=%d unknowns=%d, want one each", successes, unknowns)
	}
	if concurrentIDs[0] != originalID || concurrentIDs[1] != originalID {
		t.Fatalf("concurrent retry IDs=%v, want both to reuse %q", concurrentIDs, originalID)
	}

	var retried map[string]int
	if err := c.Call(context.Background(), "task", "append", payload, &retried); err != nil {
		t.Fatalf("retry after mixed outcomes: %v", err)
	}
	retryID := <-transport.entered
	if retryID != originalID {
		t.Fatalf("retry after mixed outcomes used op_id %q, want retained %q", retryID, originalID)
	}
	if store.executions("mixed-outcome") != 1 {
		t.Fatalf("owner executions=%d, want one across the mixed-outcome wave", store.executions("mixed-outcome"))
	}
}

func TestMalformedDoneRetriesStayUnknownWithSameID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	var mu sync.Mutex
	executions := 0
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()
	if err := h.Register("task", "append", true, func(context.Context, json.RawMessage) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		executions++
		return "not-an-object", nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	c.resultLookupBroken = false
	payload := map[string]any{"key": "malformed-done"}
	var out map[string]int
	var opID string
	c.sendHook = func() error {
		opID = c.lastOpID
		c.sendHook = nil
		return errConnectionResetAfterSend
	}
	err = c.Call(context.Background(), "task", "append", payload, &out)
	if !isOutcomeUnknown(err) {
		t.Fatalf("first lost-response resolution error=%v, want outcome_unknown", err)
	}
	if opID == "" {
		t.Fatalf("lost mutating request did not issue an op_id")
	}

	// The cached DONE response itself cannot decode into the caller's map.
	err = c.Call(context.Background(), "task", "append", payload, &out)
	if !isOutcomeUnknown(err) {
		t.Fatalf("direct DONE replay error=%v, want outcome_unknown", err)
	}
	if c.lastOpID != opID {
		t.Fatalf("direct DONE replay used op_id %q, want retained %q", c.lastOpID, opID)
	}

	var observedID string
	c.sendHook = func() error {
		observedID = c.lastOpID
		c.sendHook = nil
		return errConnectionResetAfterSend
	}
	err = c.Call(context.Background(), "task", "append", payload, &out)
	if !isOutcomeUnknown(err) {
		t.Fatalf("repeated result lookup error=%v, want outcome_unknown", err)
	}
	if observedID != opID {
		t.Fatalf("repeated result lookup used op_id %q, want retained %q", observedID, opID)
	}
	mu.Lock()
	defer mu.Unlock()
	if executions != 1 {
		t.Fatalf("owner executions=%d, want one", executions)
	}
}

func TestStaleGenerationRetryAfterHandshakeKeepsOriginalID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	store := &crashStore{committed: map[string]int{}}
	h1, srv1, c := newCrashCallFixture(t, dir, store)
	c.resultLookupBroken = true
	loseNextResponse(c)
	payload := map[string]any{"key": "stale-retry"}
	var out map[string]int
	if err := c.Call(context.Background(), "task", "append", payload, &out); !isOutcomeUnknown(err) {
		t.Fatalf("initial call error=%v, want outcome_unknown", err)
	}
	opID := c.lastOpID
	srv1.Close()
	if err := h1.Close(); err != nil {
		t.Fatalf("close first handler: %v", err)
	}

	h2, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler restart: %v", err)
	}
	defer h2.Close()
	if err := h2.Register("task", "append", true, store.op); err != nil {
		t.Fatalf("Register restart: %v", err)
	}
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	c.endpoint = srv2.URL + RPCPath
	c.httpClient = srv2.Client()

	err = c.Call(context.Background(), "task", "append", payload, &out)
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != ErrorCodeGenerationStale {
		t.Fatalf("old-generation retry error=%v, want writer_generation_stale", err)
	}
	if c.lastOpID != opID {
		t.Fatalf("stale retry used op_id %q, want original %q", c.lastOpID, opID)
	}

	c.resultLookupBroken = false
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake after restart: %v", err)
	}
	if err := c.Call(context.Background(), "task", "append", payload, &out); err != nil {
		t.Fatalf("retry after handshake: %v", err)
	}
	if c.lastOpID != opID {
		t.Fatalf("post-handshake retry used op_id %q, want original %q", c.lastOpID, opID)
	}
	if out["count"] != 1 || store.executions("stale-retry") != 1 {
		t.Fatalf("result=%v executions=%d, want cached result and one owner commit", out, store.executions("stale-retry"))
	}
}

// TestJournalCreationFsyncsDirAfterFileCreated: creating journal.jsonl is not
// durable until the parent directory entry is synced. The durability sync for
// the directory must happen after the file exists, before the writer accepts
// owner writes. Process-crash tests are not power-loss proofs; this pins the
// ordering that is actually tested.
func TestJournalCreationFsyncsDirAfterFileCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	var mu sync.Mutex
	var journalExistedAtSync []bool
	orig := syncDir
	syncDir = func(p string) error {
		mu.Lock()
		defer mu.Unlock()
		if filepath.Clean(p) == filepath.Clean(dir) {
			_, err := os.Lstat(filepath.Join(dir, "journal.jsonl"))
			journalExistedAtSync = append(journalExistedAtSync, err == nil)
		}
		return orig(p)
	}
	t.Cleanup(func() { syncDir = orig })

	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: dir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()

	mu.Lock()
	defer mu.Unlock()
	afterCreation := false
	for _, ok := range journalExistedAtSync {
		if ok {
			afterCreation = true
		}
	}
	if !afterCreation {
		t.Fatalf("journal directory was never made durable after journal.jsonl was created: sync events=%v", journalExistedAtSync)
	}
}
