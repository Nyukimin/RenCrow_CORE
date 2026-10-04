package storagehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const eventGroupTestToken = "storagehost-event-test-token"

// newEventFixture wires the real canonical event store behind the storage
// host protocol and returns a client-side EventStore handle.
func newEventFixture(t *testing.T) (*Handler, *EventStoreClient) {
	t.Helper()
	dir := t.TempDir()
	store, err := eventstore.NewSQLiteStore(filepath.Join(dir, "events.db"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	h, err := NewHandler(HandlerConfig{Token: eventGroupTestToken, JournalDir: filepath.Join(dir, "journal")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if err := RegisterEventGroup(h, store); err != nil {
		t.Fatalf("RegisterEventGroup: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: eventGroupTestToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	return h, NewEventStoreClient(c)
}

func TestEventStoreClientAppendsAndReadsAcrossProtocol(t *testing.T) {
	_, es := newEventFixture(t)
	ctx := context.Background()

	env := modulecore.NewRootEventEnvelope("core.conversation", "test.fact", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), map[string]any{"k": "v"})
	persisted, err := es.AppendSequenced(ctx, env)
	if err != nil {
		t.Fatalf("AppendSequenced: %v", err)
	}
	if persisted.EventSeq <= 0 {
		t.Fatalf("persisted=%+v, want a storage-assigned sequence", persisted)
	}

	got, found, err := es.GetByID(ctx, env.EventID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if !found {
		t.Fatalf("GetByID found=false")
	}
	if got.EventID != env.EventID || got.EventSeq != persisted.EventSeq || got.EventType != env.EventType {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", got, persisted)
	}
	if payload, _ := got.Payload["k"].(string); payload != "v" {
		t.Fatalf("payload lost across the wire: %+v", got.Payload)
	}

	list, err := es.ListByComponent(ctx, "core.conversation", 10)
	if err != nil {
		t.Fatalf("ListByComponent: %v", err)
	}
	if len(list) != 1 || list[0].EventID != env.EventID {
		t.Fatalf("list=%+v, want the single appended event", list)
	}
}

func TestEventStoreClientAppendPlainImplementsEventAppender(t *testing.T) {
	_, es := newEventFixture(t)
	var appender modulecore.EventAppender = es
	var reader modulecore.EventReader = es
	ctx := context.Background()
	env := modulecore.NewRootEventEnvelope("core.task", "test.plain", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), nil)
	if err := appender.Append(ctx, env); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, found, err := reader.GetByID(ctx, env.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID: found=%v err=%v", found, err)
	}
	if got.EventSeq <= 0 {
		t.Fatalf("plain append must receive a storage-assigned sequence: %+v", got)
	}
}

func TestEventStoreClientMutatingAppendIsIdempotentPerOpID(t *testing.T) {
	_, es := newEventFixture(t)
	ctx := context.Background()
	env := modulecore.NewRootEventEnvelope("core.task", "test.once", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), nil)

	c := es.client
	op := c.NewOperation("event", "append_sequenced", map[string]any{"event": env})
	var first struct {
		Event modulecore.EventEnvelope `json:"event"`
	}
	if err := c.Do(ctx, op, &first); err != nil {
		t.Fatalf("first Do: %v", err)
	}
	c.sendHook = func() error {
		c.sendHook = nil
		return errConnectionResetAfterSend
	}
	// The response is lost; the same operation resolves the recorded outcome
	// instead of appending a second row.
	if err := c.Do(ctx, op, nil); err == nil {
		t.Fatalf("first lost response: want an error")
	}
	var replay struct {
		Event modulecore.EventEnvelope `json:"event"`
	}
	if err := c.Do(ctx, op, &replay); err != nil {
		t.Fatalf("replay Do: %v", err)
	}
	if replay.Event.EventID != env.EventID || replay.Event.EventSeq != first.Event.EventSeq {
		t.Fatalf("replay=%+v, want the recorded outcome of the first append", replay)
	}
	list, err := es.ListByComponent(ctx, "core.task", 10)
	if err != nil {
		t.Fatalf("ListByComponent: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("events=%d, want 1: a lost response may not append twice", len(list))
	}
}

func TestEventStoreClientSurfacesStoreErrorsTyped(t *testing.T) {
	_, es := newEventFixture(t)
	ctx := context.Background()

	bad := modulecore.EventEnvelope{SchemaVersion: "wrong/v0"}
	err := es.Append(ctx, bad)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeSchemaRejected {
		t.Fatalf("err=%v, want schema_rejected from the owner validation", err)
	}

	_, found, err := es.GetByID(ctx, modulecore.NewEventID())
	if err != nil || found {
		t.Fatalf("missing event: found=%v err=%v, want found=false without an error", found, err)
	}
}

func TestEventStoreClientCloseDoesNotCloseSharedRPCClient(t *testing.T) {
	_, es := newEventFixture(t)
	env := modulecore.NewRootEventEnvelope("core.task", "test.close_noop", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), nil)
	if err := es.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := es.AppendSequenced(context.Background(), env); err != nil {
		t.Fatalf("AppendSequenced after Close: %v", err)
	}
}

type eventOwnerMutationCounts struct {
	mu              sync.Mutex
	append          int
	appendSequenced int
}

func (c *eventOwnerMutationCounts) snapshot() (appendCount, sequencedCount int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.append, c.appendSequenced
}

type eventRecoveryOwner struct {
	store                *eventstore.SQLiteStore
	counts               *eventOwnerMutationCounts
	appendAfterCommit    error
	sequencedAfterCommit error
	getByIDErr           error
	corruptGetByID       bool
}

func (o *eventRecoveryOwner) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	o.counts.mu.Lock()
	o.counts.append++
	o.counts.mu.Unlock()
	if err := o.store.Append(ctx, event); err != nil {
		return err
	}
	return o.appendAfterCommit
}

func (o *eventRecoveryOwner) AppendSequenced(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	o.counts.mu.Lock()
	o.counts.appendSequenced++
	o.counts.mu.Unlock()
	persisted, err := o.store.AppendSequenced(ctx, event)
	if err != nil {
		return modulecore.EventEnvelope{}, err
	}
	if o.sequencedAfterCommit != nil {
		return modulecore.EventEnvelope{}, o.sequencedAfterCommit
	}
	return persisted, nil
}

func (o *eventRecoveryOwner) GetByID(ctx context.Context, id modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	if o.getByIDErr != nil {
		return modulecore.EventEnvelope{}, false, o.getByIDErr
	}
	if o.corruptGetByID {
		return modulecore.EventEnvelope{EventID: id}, true, nil
	}
	return o.store.GetByID(ctx, id)
}

func (o *eventRecoveryOwner) ListByComponent(ctx context.Context, componentID string, limit int) ([]modulecore.EventEnvelope, error) {
	return o.store.ListByComponent(ctx, componentID, limit)
}

type eventRecoveryOwnerConfig struct {
	appendAfterCommit    error
	sequencedAfterCommit error
	getByIDErr           error
	corruptGetByID       bool
}

func newEventRecoveryHost(t *testing.T, journalDir, databasePath string, counts *eventOwnerMutationCounts, config eventRecoveryOwnerConfig) (*Handler, *httptest.Server, *eventstore.SQLiteStore, *Client, *EventStoreClient, *eventRecoveryOwner) {
	t.Helper()
	store, err := eventstore.NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	owner := &eventRecoveryOwner{
		store:                store,
		counts:               counts,
		appendAfterCommit:    config.appendAfterCommit,
		sequencedAfterCommit: config.sequencedAfterCommit,
		getByIDErr:           config.getByIDErr,
		corruptGetByID:       config.corruptGetByID,
	}
	h, err := NewHandler(HandlerConfig{Token: eventGroupTestToken, JournalDir: journalDir})
	if err != nil {
		_ = store.Close()
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterEventGroup(h, owner); err != nil {
		_ = h.Close()
		_ = store.Close()
		t.Fatalf("RegisterEventGroup: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		srv.Close()
		_ = h.Close()
		_ = store.Close()
	})
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: eventGroupTestToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	return h, srv, store, c, NewEventStoreClient(c), owner
}

func expectEventStorageError(t *testing.T, err error, code string) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) || got.Code != code {
		t.Fatalf("error=%v, want storagehost error code %q", err, code)
	}
}

func TestEventOwnerReconcilesSQLiteCommitGapAfterRestart(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "events.db")
	journalDir := filepath.Join(root, "journal")
	counts := &eventOwnerMutationCounts{}
	h1, srv1, store1, c1, client1, _ := newEventRecoveryHost(t, journalDir, databasePath, counts, eventRecoveryOwnerConfig{})
	const opID = "event-commit-gap-recovery"
	c1.opID = opID
	originalGeneration := c1.Generation()
	h1.crashAfterCommitFor = opID
	env := modulecore.NewRootEventEnvelope("core.task", "test.recovered", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), map[string]any{"key": "value"})
	_, err := client1.AppendSequenced(context.Background(), env)
	expectEventStorageError(t, err, ErrorCodeOutcomeUnknown)
	appendCount, sequencedCount := counts.snapshot()
	if appendCount != 0 || sequencedCount != 1 {
		t.Fatalf("after crash: append calls=%d sequenced calls=%d, want 0 and 1", appendCount, sequencedCount)
	}

	srv1.Close()
	if err := h1.Close(); err != nil {
		t.Fatalf("Close first storage host: %v", err)
	}
	if err := store1.Close(); err != nil {
		t.Fatalf("Close first event store: %v", err)
	}

	h2, _, store2, c2, client2, _ := newEventRecoveryHost(t, journalDir, databasePath, counts, eventRecoveryOwnerConfig{})
	if c2.Generation() <= c1.Generation() {
		t.Fatalf("current generation=%d, want > first generation %d", c2.Generation(), c1.Generation())
	}
	c2.opID = opID
	recovered, err := client2.AppendSequenced(context.Background(), env)
	if err != nil {
		t.Fatalf("AppendSequenced after current-generation handshake: %v", err)
	}
	if recovered.EventID != env.EventID || recovered.EventSeq <= 0 {
		t.Fatalf("recovered envelope=%+v, want original event id and positive sequence", recovered)
	}
	stored, found, err := store2.GetByID(context.Background(), env.EventID)
	if err != nil || !found {
		t.Fatalf("GetByID after recovery: found=%v err=%v", found, err)
	}
	if stored.EventSeq != recovered.EventSeq || stored.EventID != recovered.EventID {
		t.Fatalf("recovered=%+v stored=%+v, want exact persisted sequence", recovered, stored)
	}
	appendCount, sequencedCount = counts.snapshot()
	if appendCount != 0 || sequencedCount != 1 {
		t.Fatalf("after retry: append calls=%d sequenced calls=%d, want 0 and 1", appendCount, sequencedCount)
	}
	entry, found := h2.journal.lookup(opID)
	if !found || entry.Status != journalStatusDone || entry.OpID != opID || entry.Generation != originalGeneration {
		t.Fatalf("journal entry=%+v found=%v, want same op_id completed with original generation %d", entry, found, originalGeneration)
	}
	var recorded eventResultPayload
	if err := json.Unmarshal(entry.Result, &recorded); err != nil {
		t.Fatalf("decode recovered journal result: %v", err)
	}
	if recorded.Event.EventID != recovered.EventID || recorded.Event.EventSeq != recovered.EventSeq {
		t.Fatalf("journal result=%+v recovered=%+v, want the exact recovered envelope", recorded.Event, recovered)
	}
}

func TestEventOwnerCommitThenTypedErrorStaysRecoverable(t *testing.T) {
	root := t.TempDir()
	counts := &eventOwnerMutationCounts{}
	ownerErr := NewError(ErrorCodeStoreUnavailable, "simulated ambiguous SQLite commit result")
	h, _, store, c, client, _ := newEventRecoveryHost(t, filepath.Join(root, "journal"), filepath.Join(root, "events.db"), counts, eventRecoveryOwnerConfig{appendAfterCommit: ownerErr})
	const opID = "event-commit-then-error"
	c.opID = opID
	env := modulecore.NewRootEventEnvelope("core.task", "test.commit_error", time.Date(2026, 10, 3, 12, 1, 0, 0, time.UTC), nil)
	err := client.Append(context.Background(), env)
	expectEventStorageError(t, err, ErrorCodeOutcomeUnknown)
	entry, found := h.journal.lookup(opID)
	if !found || entry.Status != journalStatusBegun {
		t.Fatalf("journal entry=%+v found=%v, want begun after owner error", entry, found)
	}
	appendCount, sequencedCount := counts.snapshot()
	if appendCount != 1 || sequencedCount != 0 {
		t.Fatalf("after ambiguous commit: append calls=%d sequenced calls=%d, want 1 and 0", appendCount, sequencedCount)
	}

	if err := client.Append(context.Background(), env); err != nil {
		t.Fatalf("retry after owner read reconciliation: %v", err)
	}
	appendCount, sequencedCount = counts.snapshot()
	if appendCount != 1 || sequencedCount != 0 {
		t.Fatalf("after retry: append calls=%d sequenced calls=%d, want 1 and 0", appendCount, sequencedCount)
	}
	stored, found, err := store.GetByID(context.Background(), env.EventID)
	if err != nil || !found || stored.EventSeq <= 0 {
		t.Fatalf("stored event=%+v found=%v err=%v, want positive assigned sequence", stored, found, err)
	}
	entry, found = h.journal.lookup(opID)
	if !found || entry.Status != journalStatusDone || string(entry.Result) != "null" {
		t.Fatalf("journal entry=%+v found=%v, want DONE with the original nil append result", entry, found)
	}
}

func TestEventOwnerReconciliationNotFoundAuthorizesOneRetry(t *testing.T) {
	root := t.TempDir()
	counts := &eventOwnerMutationCounts{}
	h, _, _, c, _, _ := newEventRecoveryHost(t, filepath.Join(root, "journal"), filepath.Join(root, "events.db"), counts, eventRecoveryOwnerConfig{})
	const opID = "event-not-found-recovery"
	env := modulecore.NewRootEventEnvelope("core.task", "test.not_found", time.Date(2026, 10, 3, 12, 2, 0, 0, time.UTC), nil)
	mutation := eventEnvelopePayload{Event: env}
	payload, err := encodePayload(mutation)
	if err != nil {
		t.Fatalf("encode event mutation: %v", err)
	}
	if err := h.journal.begin(opID, GroupEvent, "append_sequenced", payloadHash(GroupEvent, "append_sequenced", payload), h.scope, h.Generation()); err != nil {
		t.Fatalf("write begun entry before owner execution: %v", err)
	}
	c.opID = opID
	var result eventResultPayload
	if err := c.Call(context.Background(), GroupEvent, "append_sequenced", mutation, &result); err != nil {
		t.Fatalf("Call after confirmed event absence: %v", err)
	}
	if result.Event.EventID != env.EventID || result.Event.EventSeq <= 0 {
		t.Fatalf("result=%+v, want event with assigned sequence", result.Event)
	}
	appendCount, sequencedCount := counts.snapshot()
	if appendCount != 0 || sequencedCount != 1 {
		t.Fatalf("mutation calls append=%d sequenced=%d, want 0 and 1", appendCount, sequencedCount)
	}
}

func TestEventOwnerUnverifiedMatchesStayUnknown(t *testing.T) {
	for _, mode := range []string{"mismatch", "corrupt", "read_error"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			counts := &eventOwnerMutationCounts{}
			config := eventRecoveryOwnerConfig{}
			if mode == "corrupt" {
				config.corruptGetByID = true
			} else if mode == "read_error" {
				config.getByIDErr = errors.New("event owner read failed")
			}
			h, _, store, c, _, _ := newEventRecoveryHost(t, filepath.Join(root, "journal"), filepath.Join(root, "events.db"), counts, config)
			const opID = "event-unverified-recovery"
			env := modulecore.NewRootEventEnvelope("core.task", "test.unverified", time.Date(2026, 10, 3, 12, 3, 0, 0, time.UTC), map[string]any{"version": "requested"})
			if mode == "mismatch" {
				persisted := env
				persisted.Payload = map[string]any{"version": "persisted"}
				if _, err := store.AppendSequenced(context.Background(), persisted); err != nil {
					t.Fatalf("seed conflicting persisted event: %v", err)
				}
			}
			mutation := eventEnvelopePayload{Event: env}
			payload, err := encodePayload(mutation)
			if err != nil {
				t.Fatalf("encode event mutation: %v", err)
			}
			if err := h.journal.begin(opID, GroupEvent, "append_sequenced", payloadHash(GroupEvent, "append_sequenced", payload), h.scope, h.Generation()); err != nil {
				t.Fatalf("write begun entry: %v", err)
			}
			c.opID = opID
			var result eventResultPayload
			err = c.Call(context.Background(), GroupEvent, "append_sequenced", mutation, &result)
			expectEventStorageError(t, err, ErrorCodeOutcomeUnknown)
			appendCount, sequencedCount := counts.snapshot()
			if appendCount != 0 || sequencedCount != 0 {
				t.Fatalf("unverified recovery executed owner mutation: append=%d sequenced=%d", appendCount, sequencedCount)
			}
			entry, found := h.journal.lookup(opID)
			if !found || entry.Status != journalStatusBegun {
				t.Fatalf("journal entry=%+v found=%v, want begun", entry, found)
			}
		})
	}
}

func TestEventOwnerValidationFailureIsTheOnlyRollbackProof(t *testing.T) {
	root := t.TempDir()
	counts := &eventOwnerMutationCounts{}
	h, _, _, c, _, _ := newEventRecoveryHost(t, filepath.Join(root, "journal"), filepath.Join(root, "events.db"), counts, eventRecoveryOwnerConfig{})
	const opID = "event-invalid-envelope"
	c.opID = opID
	invalid := modulecore.EventEnvelope{SchemaVersion: "invalid/v0"}
	err := c.Call(context.Background(), GroupEvent, "append", eventEnvelopePayload{Event: invalid}, nil)
	expectEventStorageError(t, err, ErrorCodeSchemaRejected)
	appendCount, sequencedCount := counts.snapshot()
	if appendCount != 0 || sequencedCount != 0 {
		t.Fatalf("invalid envelope reached owner: append calls=%d sequenced calls=%d", appendCount, sequencedCount)
	}
	if _, found := h.journal.lookup(opID); found {
		t.Fatal("invalid envelope retained a journal entry after proven pre-owner rejection")
	}
}

func TestEventPayloadSchemaRejectsUnknownFieldsAndTrailingJSON(t *testing.T) {
	root := t.TempDir()
	counts := &eventOwnerMutationCounts{}
	h, _, _, c, _, _ := newEventRecoveryHost(t, filepath.Join(root, "journal"), filepath.Join(root, "events.db"), counts, eventRecoveryOwnerConfig{})
	env := modulecore.NewRootEventEnvelope("core.task", "test.closed_payload", time.Date(2026, 10, 3, 12, 4, 0, 0, time.UTC), map[string]any{"arbitrary": "event data remains allowed"})
	op := c.NewOperation(GroupEvent, "append", struct {
		Event      modulecore.EventEnvelope `json:"event"`
		Unexpected string                   `json:"unexpected"`
	}{Event: env, Unexpected: "not part of the closed DTO"})
	err := c.Do(context.Background(), op, nil)
	expectEventStorageError(t, err, ErrorCodeSchemaRejected)
	appendCount, sequencedCount := counts.snapshot()
	if appendCount != 0 || sequencedCount != 0 {
		t.Fatalf("unknown outer field reached owner: append calls=%d sequenced calls=%d", appendCount, sequencedCount)
	}
	if _, found := h.journal.lookup(op.OpID); found {
		t.Fatal("schema-rejected payload retained a journal entry after proven pre-owner rejection")
	}

	valid, err := encodePayload(eventEnvelopePayload{Event: env})
	if err != nil {
		t.Fatalf("encode valid event payload: %v", err)
	}
	withTrailing := append(append(json.RawMessage(nil), valid...), []byte(` {}`)...)
	var decoded eventEnvelopePayload
	if err := decodeEventPayload(withTrailing, &decoded); err == nil {
		t.Fatal("event payload decoder accepted trailing JSON")
	}
}
