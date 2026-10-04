package storagehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

const (
	recoveryGroup = "task-recovery-test"
	recoveryOp    = "apply"
	recoveryToken = "storagehost-recovery-test-token"
)

type recoveryOwnerStore struct {
	mu          sync.Mutex
	executions  []MutationMetadata
	reconciles  []MutationMetadata
	results     map[string]map[string]int
	reconcileFn func(MutationMetadata) (ReconcileDecision, error)
}

func newRecoveryOwnerStore() *recoveryOwnerStore {
	return &recoveryOwnerStore{results: make(map[string]map[string]int)}
}

func (s *recoveryOwnerStore) execute(_ context.Context, mutation MutationMetadata) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutation.Payload = append(json.RawMessage(nil), mutation.Payload...)
	s.executions = append(s.executions, mutation)
	result := map[string]int{"count": len(s.executions)}
	s.results[mutation.OpID] = result
	return result, nil
}

func (s *recoveryOwnerStore) reconcile(_ context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	mutation.Payload = append(json.RawMessage(nil), mutation.Payload...)
	s.reconciles = append(s.reconciles, mutation)
	if s.reconcileFn != nil {
		return s.reconcileFn(mutation)
	}
	if result, ok := s.results[mutation.OpID]; ok {
		copyResult := map[string]int{"count": result["count"]}
		return Committed(copyResult), nil
	}
	return UnknownOutcome(), nil
}

func (s *recoveryOwnerStore) snapshot() (executions, reconciles []MutationMetadata) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]MutationMetadata(nil), s.executions...), append([]MutationMetadata(nil), s.reconciles...)
}

func registerRecoveryOperation(t *testing.T, h *Handler, owner *recoveryOwnerStore) {
	t.Helper()
	if err := h.RegisterRecoverable(recoveryGroup, recoveryOp, owner.execute, owner.reconcile); err != nil {
		t.Fatalf("RegisterRecoverable: %v", err)
	}
}

func recoveryClient(t *testing.T, server *httptest.Server, token, opID string) *Client {
	t.Helper()
	c, err := NewClient(ClientConfig{Endpoint: server.URL, Token: token, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	c.opID = opID
	return c
}

func expectStorageError(t *testing.T, err error, code string) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) || got.Code != code {
		t.Fatalf("error=%v, want storagehost error code %q", err, code)
	}
}

func TestOwnerReconcileCommittedAfterRestartReturnsOriginalResult(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	payload := json.RawMessage(`{"key":"run-1","values":[1,2]}`)
	const opID = "owner-commit-recovery"
	owner := newRecoveryOwnerStore()

	h1, err := NewHandler(HandlerConfig{Token: recoveryToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler first generation: %v", err)
	}
	registerRecoveryOperation(t, h1, owner)
	srv1 := httptest.NewServer(h1)
	c := recoveryClient(t, srv1, recoveryToken, opID)
	oldGeneration := c.Generation()
	h1.crashAfterCommitFor = opID
	var firstResult map[string]int
	err = c.Call(context.Background(), recoveryGroup, recoveryOp, payload, &firstResult)
	expectStorageError(t, err, ErrorCodeOutcomeUnknown)
	executions, reconciles := owner.snapshot()
	if len(executions) != 1 || len(reconciles) != 0 {
		t.Fatalf("after simulated commit crash: executions=%d reconciles=%d, want 1 and 0", len(executions), len(reconciles))
	}
	if !reflect.DeepEqual(executions[0].Payload, payload) || executions[0].OpID != opID || executions[0].RequestGeneration != oldGeneration || executions[0].JournalGeneration != oldGeneration {
		t.Fatalf("execution metadata=%+v, want exact payload, op_id, and generation %d", executions[0], oldGeneration)
	}
	srv1.Close()
	if err := h1.Close(); err != nil {
		t.Fatalf("Close first generation: %v", err)
	}
	h2, err := NewHandler(HandlerConfig{Token: recoveryToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler restarted generation: %v", err)
	}
	defer h2.Close()
	registerRecoveryOperation(t, h2, owner)
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	c.httpClient = srv2.Client()
	c.endpoint = srv2.URL + RPCPath

	// The old generation is rejected before the reconciliation hook can run.
	var stale Response
	if err := c.send(context.Background(), Request{
		Contract: ContractVersion, OpID: opID, Group: recoveryGroup, Op: recoveryOp,
		Generation: oldGeneration, Payload: payload,
	}, &stale); err != nil {
		t.Fatalf("send stale request: %v", err)
	}
	if stale.Error == nil || stale.Error.Code != ErrorCodeGenerationStale {
		t.Fatalf("stale response error=%+v, want %s", stale.Error, ErrorCodeGenerationStale)
	}
	_, reconciles = owner.snapshot()
	if len(reconciles) != 0 {
		t.Fatalf("stale request invoked reconciliation %d times, want 0", len(reconciles))
	}

	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake after restart: %v", err)
	}
	currentGeneration := c.Generation()
	if currentGeneration <= oldGeneration {
		t.Fatalf("restarted generation=%d, want > old generation %d", currentGeneration, oldGeneration)
	}
	var replay map[string]int
	err = c.Call(context.Background(), recoveryGroup, recoveryOp, payload, &replay)
	if err != nil {
		t.Fatalf("matching retry after owner reconciliation: %v", err)
	}
	if !reflect.DeepEqual(replay, map[string]int{"count": 1}) {
		t.Fatalf("recovered result=%v, want original committed result", replay)
	}
	executions, reconciles = owner.snapshot()
	if len(executions) != 1 || len(reconciles) != 1 {
		t.Fatalf("after recovery: executions=%d reconciles=%d, want 1 and 1", len(executions), len(reconciles))
	}
	recovery := reconciles[0]
	if recovery.OpID != opID || !reflect.DeepEqual(recovery.Payload, payload) || recovery.RequestGeneration != currentGeneration || recovery.JournalGeneration != oldGeneration {
		t.Fatalf("recovery metadata=%+v, want op_id/payload, request generation %d, journal generation %d", recovery, currentGeneration, oldGeneration)
	}
	entry, found := h2.journal.lookup(opID)
	if !found || entry.Status != journalStatusDone || !reflect.DeepEqual(entry.Result, json.RawMessage(`{"count":1}`)) {
		t.Fatalf("journal entry=%+v found=%v, want done with original result", entry, found)
	}
}

func TestOwnerReconcileConfirmedNotCommittedReexecutesSameIdentity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	payload := json.RawMessage(`{"key":"not-committed"}`)
	const opID = "owner-not-committed-recovery"
	owner := newRecoveryOwnerStore()
	owner.reconcileFn = func(MutationMetadata) (ReconcileDecision, error) { return ConfirmedNotCommitted(), nil }

	h1, err := NewHandler(HandlerConfig{Token: recoveryToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler first generation: %v", err)
	}
	registerRecoveryOperation(t, h1, owner)
	oldGeneration := h1.Generation()
	if err := h1.journal.begin(opID, recoveryGroup, recoveryOp, payloadHash(recoveryGroup, recoveryOp, payload), h1.scope, oldGeneration); err != nil {
		t.Fatalf("write begun entry: %v", err)
	}
	if err := h1.Close(); err != nil {
		t.Fatalf("Close first generation: %v", err)
	}

	h2, err := NewHandler(HandlerConfig{Token: recoveryToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler restarted generation: %v", err)
	}
	defer h2.Close()
	registerRecoveryOperation(t, h2, owner)
	srv := httptest.NewServer(h2)
	defer srv.Close()
	c := recoveryClient(t, srv, recoveryToken, opID)
	currentGeneration := c.Generation()
	var result map[string]int
	if err := c.Call(context.Background(), recoveryGroup, recoveryOp, payload, &result); err != nil {
		t.Fatalf("call after confirmed non-commit: %v", err)
	}
	if !reflect.DeepEqual(result, map[string]int{"count": 1}) {
		t.Fatalf("result=%v, want one execution", result)
	}
	executions, reconciles := owner.snapshot()
	if len(executions) != 1 || len(reconciles) != 1 {
		t.Fatalf("executions=%d reconciles=%d, want 1 and 1", len(executions), len(reconciles))
	}
	if executions[0].OpID != opID || !reflect.DeepEqual(executions[0].Payload, payload) || executions[0].RequestGeneration != currentGeneration || executions[0].JournalGeneration != currentGeneration {
		t.Fatalf("re-execution metadata=%+v, want same op_id/payload and current generation %d", executions[0], currentGeneration)
	}
	if reconciles[0].JournalGeneration != oldGeneration || reconciles[0].RequestGeneration != currentGeneration {
		t.Fatalf("reconciliation generations=(journal %d, request %d), want (%d, %d)", reconciles[0].JournalGeneration, reconciles[0].RequestGeneration, oldGeneration, currentGeneration)
	}
	entry, found := h2.journal.lookup(opID)
	if !found || entry.Status != journalStatusDone || entry.Generation != currentGeneration {
		t.Fatalf("journal entry=%+v found=%v, want done at current generation %d", entry, found, currentGeneration)
	}
}

func TestOwnerReconcileUnknownOrErrorNeverReexecutes(t *testing.T) {
	tests := []struct {
		name         string
		decision     ReconcileDecision
		reconcileErr error
	}{
		{name: "unknown", decision: UnknownOutcome()},
		{name: "error", decision: Committed(map[string]int{"count": 9}), reconcileErr: errors.New("owner read failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			payload := json.RawMessage(`{"key":"unknown"}`)
			const opID = "owner-unknown-recovery"
			owner := newRecoveryOwnerStore()
			owner.reconcileFn = func(MutationMetadata) (ReconcileDecision, error) { return tt.decision, tt.reconcileErr }
			h, err := NewHandler(HandlerConfig{Token: recoveryToken, JournalDir: root})
			if err != nil {
				t.Fatalf("NewHandler: %v", err)
			}
			defer h.Close()
			registerRecoveryOperation(t, h, owner)
			if err := h.journal.begin(opID, recoveryGroup, recoveryOp, payloadHash(recoveryGroup, recoveryOp, payload), h.scope, h.Generation()); err != nil {
				t.Fatalf("write begun entry: %v", err)
			}
			srv := httptest.NewServer(h)
			defer srv.Close()
			c := recoveryClient(t, srv, recoveryToken, opID)
			var result map[string]int
			err = c.Call(context.Background(), recoveryGroup, recoveryOp, payload, &result)
			expectStorageError(t, err, ErrorCodeOutcomeUnknown)
			executions, reconciles := owner.snapshot()
			if len(executions) != 0 || len(reconciles) != 1 {
				t.Fatalf("executions=%d reconciles=%d, want 0 and 1", len(executions), len(reconciles))
			}
			entry, found := h.journal.lookup(opID)
			if !found || entry.Status != journalStatusBegun {
				t.Fatalf("journal entry=%+v found=%v, want begun", entry, found)
			}
		})
	}
}

func TestOwnerReconcileWithoutHookKeepsLegacyOutcomeUnknown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	payload := json.RawMessage(`{"key":"legacy"}`)
	const opID = "legacy-begun-op"
	h, err := NewHandler(HandlerConfig{Token: recoveryToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()
	executions := 0
	if err := h.Register(recoveryGroup, recoveryOp, true, func(context.Context, json.RawMessage) (any, error) {
		executions++
		return map[string]int{"count": executions}, nil
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := h.journal.begin(opID, recoveryGroup, recoveryOp, payloadHash(recoveryGroup, recoveryOp, payload), h.scope, h.Generation()); err != nil {
		t.Fatalf("write begun entry: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := recoveryClient(t, srv, recoveryToken, opID)
	var result map[string]int
	err = c.Call(context.Background(), recoveryGroup, recoveryOp, payload, &result)
	expectStorageError(t, err, ErrorCodeOutcomeUnknown)
	if executions != 0 {
		t.Fatalf("legacy owner executions=%d, want 0", executions)
	}
}

func TestOwnerReconcileConflictsAndDoneReplayBypassHook(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	owner := newRecoveryOwnerStore()
	payload := json.RawMessage(`{"key":"same"}`)
	h1, err := NewHandler(HandlerConfig{Token: recoveryToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler first generation: %v", err)
	}
	for _, opID := range []string{"hash-conflict-op", "scope-conflict-op"} {
		if err := h1.journal.begin(opID, recoveryGroup, recoveryOp, payloadHash(recoveryGroup, recoveryOp, payload), h1.scope, h1.Generation()); err != nil {
			t.Fatalf("write begun entry %s: %v", opID, err)
		}
	}
	if err := h1.Close(); err != nil {
		t.Fatalf("Close first generation: %v", err)
	}

	h2, err := NewHandler(HandlerConfig{Token: recoveryToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler second generation: %v", err)
	}
	registerRecoveryOperation(t, h2, owner)
	srv2 := httptest.NewServer(h2)
	c2 := recoveryClient(t, srv2, recoveryToken, "hash-conflict-op")
	var result map[string]int
	changedPayload := json.RawMessage(`{"key":"different"}`)
	err = c2.Call(context.Background(), recoveryGroup, recoveryOp, changedPayload, &result)
	expectStorageError(t, err, ErrorCodeDuplicateConflict)
	_, reconciles := owner.snapshot()
	if len(reconciles) != 0 {
		t.Fatalf("hash mismatch invoked reconciliation %d times, want 0", len(reconciles))
	}

	c2.opID = "done-replay-op"
	if err := c2.Call(context.Background(), recoveryGroup, recoveryOp, payload, &result); err != nil {
		t.Fatalf("initial done operation: %v", err)
	}
	if !reflect.DeepEqual(result, map[string]int{"count": 1}) {
		t.Fatalf("initial done result=%v, want count 1", result)
	}
	if err := c2.Call(context.Background(), recoveryGroup, recoveryOp, payload, &result); err != nil {
		t.Fatalf("done replay: %v", err)
	}
	if !reflect.DeepEqual(result, map[string]int{"count": 1}) {
		t.Fatalf("done replay result=%v, want stored count 1", result)
	}
	executions, reconciles := owner.snapshot()
	if len(executions) != 1 || len(reconciles) != 0 {
		t.Fatalf("done replay executions=%d reconciles=%d, want 1 and 0", len(executions), len(reconciles))
	}
	srv2.Close()
	if err := h2.Close(); err != nil {
		t.Fatalf("Close second generation: %v", err)
	}

	h3, err := NewHandler(HandlerConfig{Token: "different-test-token", JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler different scope: %v", err)
	}
	defer h3.Close()
	registerRecoveryOperation(t, h3, owner)
	srv3 := httptest.NewServer(h3)
	defer srv3.Close()
	c3 := recoveryClient(t, srv3, "different-test-token", "scope-conflict-op")
	err = c3.Call(context.Background(), recoveryGroup, recoveryOp, payload, &result)
	expectStorageError(t, err, ErrorCodeDuplicateConflict)
	executions, reconciles = owner.snapshot()
	if len(executions) != 1 || len(reconciles) != 0 {
		t.Fatalf("scope mismatch executions=%d reconciles=%d, want 1 and 0", len(executions), len(reconciles))
	}
}

func TestOwnerReconcileJournalCompletionFailureRemainsUnknown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	payload := json.RawMessage(`{"key":"complete-failure"}`)
	const opID = "owner-complete-failure"
	owner := newRecoveryOwnerStore()
	h, err := NewHandler(HandlerConfig{Token: recoveryToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	defer h.Close()
	registerRecoveryOperation(t, h, owner)
	srv := httptest.NewServer(h)
	defer srv.Close()
	c := recoveryClient(t, srv, recoveryToken, opID)
	h.crashAfterCommitFor = opID
	var result map[string]int
	err = c.Call(context.Background(), recoveryGroup, recoveryOp, payload, &result)
	expectStorageError(t, err, ErrorCodeOutcomeUnknown)

	if err := h.journal.path.Close(); err != nil {
		t.Fatalf("close writable journal: %v", err)
	}
	readOnly, err := os.Open(filepath.Join(root, journalFileName))
	if err != nil {
		t.Fatalf("open journal read-only: %v", err)
	}
	h.journal.path = readOnly
	if err := c.Call(context.Background(), recoveryGroup, recoveryOp, payload, &result); err == nil {
		t.Fatalf("reconciliation succeeded despite journal completion failure: result=%v", result)
	} else {
		expectStorageError(t, err, ErrorCodeOutcomeUnknown)
	}
	executions, reconciles := owner.snapshot()
	if len(executions) != 1 || len(reconciles) != 1 {
		t.Fatalf("executions=%d reconciles=%d, want 1 and 1", len(executions), len(reconciles))
	}
	entry, found := h.journal.lookup(opID)
	if !found || entry.Status != journalStatusBegun {
		t.Fatalf("journal entry=%+v found=%v, want begun after failed completion", entry, found)
	}
}
