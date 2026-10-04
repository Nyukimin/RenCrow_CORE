package storagehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	_ domainsession.SessionRepository = (*SessionRepositoryClient)(nil)
	_ interface {
		LoadOrCreateCanonical(context.Context, string, conversation.ChannelAddress, time.Time) (*domainsession.Session, error)
	} = (*SessionRepositoryClient)(nil)
)

type sessionGroupFixture struct {
	handler *Handler
	client  *SessionRepositoryClient
}

func newSessionGroupFixture(t *testing.T, owner SessionGroupOwner) sessionGroupFixture {
	t.Helper()
	dir := t.TempDir()
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: filepath.Join(dir, "journal")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	if err := RegisterSessionGroup(h, owner); err != nil {
		t.Fatalf("RegisterSessionGroup: %v", err)
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
	return sessionGroupFixture{handler: h, client: NewSessionRepositoryClient(c)}
}

type countedSessionOwner struct {
	*session.JSONSessionRepository
	saves int
	err   error
}

func (o *countedSessionOwner) Save(ctx context.Context, value *domainsession.Session) error {
	o.saves++
	if o.err != nil {
		return o.err
	}
	return o.JSONSessionRepository.Save(ctx, value)
}

type ownerRollbackClaimError struct{}

func (ownerRollbackClaimError) Error() string    { return "owner claims rollback" }
func (ownerRollbackClaimError) RolledBack() bool { return true }

type countedCanonicalSessionOwner struct {
	*session.JSONSessionRepository
	lookups   int
	createdID string
}

func (o *countedCanonicalSessionOwner) LoadOrCreateCanonical(ctx context.Context, logicalDate string, address conversation.ChannelAddress, createdAt time.Time) (*domainsession.Session, error) {
	o.lookups++
	value, err := o.JSONSessionRepository.LoadOrCreateCanonical(ctx, logicalDate, address, createdAt)
	if err == nil {
		o.createdID = value.ID()
	}
	return value, err
}

func newSessionGroupOwner(t *testing.T) *session.JSONSessionRepository {
	t.Helper()
	return session.NewJSONSessionRepository(t.TempDir())
}

func newStorageHostSession(t *testing.T) *domainsession.Session {
	t.Helper()
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 3, 4, 5, 6, 0, time.FixedZone("JST", 9*60*60))
	updatedAt := createdAt.Add(3 * time.Minute)
	id := modulecore.NewSessionID()
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "hello over storage host", address)
	if err != nil {
		t.Fatal(err)
	}
	input = input.WithSessionID(string(id)).
		WithAttachments([]attachment.Attachment{{
			ID:                  "attachment-1",
			Kind:                attachment.KindDocument,
			Filename:            "notes.txt",
			ContentType:         "text/plain",
			SizeBytes:           5,
			Path:                "attachments/attachment-1",
			SHA256:              "sha256-notes",
			ExtractedText:       "hello",
			ExtractionError:     "partial extraction",
			ExtractionTruncated: true,
			SecurityWarnings:    []string{"reviewed"},
			Data:                []byte("transient bytes are not persisted"),
		}}).
		WithViewerRecipient("shiro").
		WithForcedRoute(routing.RouteCODE3).
		WithRoute(routing.RouteCHAT)
	secondInput, err := conversation.NewTurnInput(modulecore.NewTaskID(), "a second stored turn", address)
	if err != nil {
		t.Fatal(err)
	}
	secondInput = secondInput.WithSessionID(string(id)).WithRoute(routing.RouteOPS)
	value, err := domainsession.ReconstructCanonicalSession(
		id,
		"2026-10-03",
		address,
		[]conversation.TurnInput{input, secondInput},
		map[string]interface{}{
			"string": "value",
			"number": float64(42),
			"bool":   true,
			"object": map[string]interface{}{"enabled": true},
		},
		createdAt,
		updatedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSessionGroupRoundTripsCanonicalSessionFidelity(t *testing.T) {
	owner := newSessionGroupOwner(t)
	fixture := newSessionGroupFixture(t, owner)
	want := newStorageHostSession(t)
	if err := fixture.client.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := fixture.client.Load(context.Background(), want.ID())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ID() != want.ID() || got.LogicalDate() != want.LogicalDate() || got.ChannelAddress() != want.ChannelAddress() {
		t.Fatalf("canonical identity changed: id=%q date=%q address=%#v", got.ID(), got.LogicalDate(), got.ChannelAddress())
	}
	if !got.CreatedAt().Equal(want.CreatedAt()) || !got.UpdatedAt().Equal(want.UpdatedAt()) {
		t.Fatalf("timestamps changed: got %s/%s want %s/%s", got.CreatedAt(), got.UpdatedAt(), want.CreatedAt(), want.UpdatedAt())
	}
	if got.HistoryCount() != 2 {
		t.Fatalf("history count=%d, want 2", got.HistoryCount())
	}
	gotHistory, wantHistory := got.GetHistory(), want.GetHistory()
	for index, gotInput := range gotHistory {
		wantInput := wantHistory[index]
		if gotInput.RootTaskID() != wantInput.RootTaskID() || gotInput.TurnID() != wantInput.TurnID() || gotInput.TraceID() != wantInput.TraceID() || gotInput.UserMessageID() != wantInput.UserMessageID() || gotInput.AgentMessageID() != wantInput.AgentMessageID() {
			t.Fatalf("history[%d] turn identities changed: got=%#v want=%#v", index, gotInput, wantInput)
		}
		if gotInput.MessageText() != wantInput.MessageText() || gotInput.SessionID() != want.ID() || gotInput.ChannelAddress() != wantInput.ChannelAddress() || gotInput.ViewerRecipient() != wantInput.ViewerRecipient() || gotInput.ForcedRoute() != wantInput.ForcedRoute() || gotInput.Route() != wantInput.Route() {
			t.Fatalf("history[%d] turn metadata changed: got=%#v want=%#v", index, gotInput, wantInput)
		}
		wantAttachments := wantInput.Attachments()
		for attachmentIndex := range wantAttachments {
			wantAttachments[attachmentIndex].Data = nil
		}
		if !reflect.DeepEqual(gotInput.Attachments(), wantAttachments) {
			t.Fatalf("history[%d] attachments changed: got=%#v want=%#v", index, gotInput.Attachments(), wantAttachments)
		}
	}
	if !reflect.DeepEqual(got.GetAllMemory(), want.GetAllMemory()) {
		t.Fatalf("memory changed: got=%#v want=%#v", got.GetAllMemory(), want.GetAllMemory())
	}
}

func TestSessionGroupLoadOrCreateCanonicalReusesLookupIdentity(t *testing.T) {
	owner := newSessionGroupOwner(t)
	fixture := newSessionGroupFixture(t, owner)
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	first, err := fixture.client.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, createdAt)
	if err != nil {
		t.Fatalf("first LoadOrCreateCanonical: %v", err)
	}
	second, err := fixture.client.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, createdAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second LoadOrCreateCanonical: %v", err)
	}
	if first.ID() == "" || first.ID() != second.ID() {
		t.Fatalf("same canonical lookup returned IDs %q and %q", first.ID(), second.ID())
	}
}

func TestSessionGroupMutatingSaveRetryUsesRecordedOperation(t *testing.T) {
	owner := &countedSessionOwner{JSONSessionRepository: newSessionGroupOwner(t)}
	fixture := newSessionGroupFixture(t, owner)
	want := newStorageHostSession(t)
	fixture.client.client.resultLookupBroken = true
	fixture.client.client.sendHook = func() error { return errConnectionResetAfterSend }
	firstErr := fixture.client.Save(context.Background(), want)
	var storageErr *Error
	if !errors.As(firstErr, &storageErr) || storageErr.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("first Save error=%v, want outcome_unknown after result lookup is unavailable", firstErr)
	}
	fixture.client.client.resultLookupBroken = false
	if err := fixture.client.Save(context.Background(), want); err != nil {
		t.Fatalf("retry Save: %v", err)
	}
	if owner.saves != 1 {
		t.Fatalf("owner Save calls=%d, want 1 across the op retry", owner.saves)
	}
}

func TestSessionGroupDoesNotTrustOwnerRollbackClaims(t *testing.T) {
	owner := &countedSessionOwner{JSONSessionRepository: newSessionGroupOwner(t), err: ownerRollbackClaimError{}}
	fixture := newSessionGroupFixture(t, owner)
	want := newStorageHostSession(t)
	for attempt := 1; attempt <= 2; attempt++ {
		err := fixture.client.Save(context.Background(), want)
		var storageErr *Error
		if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeOutcomeUnknown {
			t.Fatalf("Save attempt %d error=%v, want outcome_unknown", attempt, err)
		}
	}
	if owner.saves != 1 {
		t.Fatalf("owner Save calls=%d, want 1 after an unproven owner error", owner.saves)
	}
}

func TestSessionGroupCanonicalResultDecodeFailureIsNonRepeatable(t *testing.T) {
	owner := &countedCanonicalSessionOwner{JSONSessionRepository: newSessionGroupOwner(t)}
	fixture := newSessionGroupFixture(t, owner)
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	committedOpID := ""
	fixture.client.client.sendHook = func() error {
		fixture.client.client.sendHook = nil
		entries := fixture.handler.journal.entries()
		if len(entries) != 1 || entries[0].Status != journalStatusDone {
			t.Errorf("journal entries at lost response=%+v, want the committed DONE operation", entries)
			return errConnectionResetAfterSend
		}
		committedOpID = entries[0].OpID
		if err := fixture.handler.journal.complete(entries[0].OpID, []byte(`{"found":true,"session":{"id":"malformed-result"}}`)); err != nil {
			t.Errorf("replace stored DONE result: %v", err)
		}
		return errConnectionResetAfterSend
	}

	_, firstErr := fixture.client.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, createdAt)
	var firstStorageErr *Error
	if !errors.As(firstErr, &firstStorageErr) || firstStorageErr.Code != ErrorCodeOutcomeUnknown {
		t.Errorf("first canonical call error=%v, want outcome_unknown", firstErr)
	}
	if owner.lookups != 1 {
		t.Errorf("owner canonical calls after first result failure=%d, want 1", owner.lookups)
	}
	if owner.createdID == "" {
		t.Fatal("owner returned no committed session identity")
	}
	if _, err := owner.JSONSessionRepository.Load(context.Background(), owner.createdID); err != nil {
		t.Fatalf("owner commit evidence: load session %q: %v", owner.createdID, err)
	}
	t.Logf("owner commit confirmed for session %q after typed result failure", owner.createdID)

	_, retryErr := fixture.client.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, createdAt)
	var retryStorageErr *Error
	if !errors.As(retryErr, &retryStorageErr) || retryStorageErr.Code != ErrorCodeOutcomeUnknown {
		t.Errorf("canonical retry error=%v, want outcome_unknown", retryErr)
	}
	if owner.lookups != 1 {
		t.Errorf("owner canonical calls after retry=%d, want 1", owner.lookups)
	}
	entries := fixture.handler.journal.entries()
	if len(entries) != 1 || entries[0].OpID != committedOpID || entries[0].Status != journalStatusDone {
		t.Errorf("journal after retry=%+v, want only the original DONE op_id %q", entries, committedOpID)
	}
}

func TestSessionGroupDeleteExistsAndNotFound(t *testing.T) {
	fixture := newSessionGroupFixture(t, newSessionGroupOwner(t))
	want := newStorageHostSession(t)
	if err := fixture.client.Save(context.Background(), want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if exists, err := fixture.client.Exists(context.Background(), want.ID()); err != nil || !exists {
		t.Fatalf("Exists before delete=(%v,%v), want (true,nil)", exists, err)
	}
	if err := fixture.client.Delete(context.Background(), want.ID()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if exists, err := fixture.client.Exists(context.Background(), want.ID()); err != nil || exists {
		t.Fatalf("Exists after delete=(%v,%v), want (false,nil)", exists, err)
	}
	if err := fixture.client.Delete(context.Background(), want.ID()); err != nil {
		t.Fatalf("repeated Delete: %v", err)
	}
	if _, err := fixture.client.Load(context.Background(), want.ID()); !errors.Is(err, domainsession.ErrSessionNotFound) {
		t.Fatalf("Load after delete error=%v, want ErrSessionNotFound", err)
	}
}

func TestSessionGroupRejectsMalformedPayloadBeforeOwnerExecution(t *testing.T) {
	owner := &countedSessionOwner{JSONSessionRepository: newSessionGroupOwner(t)}
	fixture := newSessionGroupFixture(t, owner)
	badID := "not-a-canonical-session-id"
	err := fixture.client.client.Call(context.Background(), GroupSession, "save", json.RawMessage(`{"session":{"id":"`+badID+`"}}`), nil)
	var storageErr *Error
	if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeSchemaRejected {
		t.Fatalf("malformed Save error=%v, want schema_rejected", err)
	}
	if owner.saves != 0 {
		t.Fatalf("owner Save calls=%d, want 0 for rejected payload", owner.saves)
	}
	entries := fixture.handler.journal.entries()
	if len(entries) != 0 {
		t.Fatalf("journal entries=%+v, want no retained op_id after pre-owner rejection", entries)
	}
}

func TestSessionGroupContractIsClosedAndRejectsPathOperations(t *testing.T) {
	fixture := newSessionGroupFixture(t, newSessionGroupOwner(t))
	contract, err := fixture.client.client.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	want := map[string]bool{
		"save":                     true,
		"load":                     false,
		"exists":                   false,
		"delete":                   true,
		"load_or_create_canonical": true,
	}
	for _, spec := range contract.Operations {
		if spec.Group != GroupSession {
			t.Fatalf("unexpected group operation: %+v", spec)
		}
		mutating, ok := want[spec.Op]
		if !ok {
			t.Fatalf("unexpected open-ended operation: %+v", spec)
		}
		if spec.Mutating != mutating {
			t.Fatalf("operation %q mutating=%v, want %v", spec.Op, spec.Mutating, mutating)
		}
		delete(want, spec.Op)
	}
	if len(want) != 0 {
		t.Fatalf("missing operations: %v", want)
	}
	dto, err := sessionRecordFromDomain(newStorageHostSession(t))
	if err != nil {
		t.Fatalf("sessionRecordFromDomain: %v", err)
	}
	payload, err := marshalSessionMessage(sessionSavePayload{Session: dto})
	if err != nil {
		t.Fatalf("marshalSessionMessage: %v", err)
	}
	var topLevel map[string]json.RawMessage
	if err := json.Unmarshal(payload, &topLevel); err != nil {
		t.Fatalf("decode save payload: %v", err)
	}
	if len(topLevel) != 1 || len(topLevel["session"]) == 0 {
		t.Fatalf("save payload top-level fields=%v, want only typed session", topLevel)
	}
	for _, op := range []string{"read_file", "write_file", "list_path"} {
		err := fixture.client.client.Call(context.Background(), GroupSession, op, map[string]string{}, nil)
		var storageErr *Error
		if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeOperationUnsupported {
			t.Fatalf("operation %q error=%v, want operation_unsupported", op, err)
		}
	}
	err = fixture.client.client.Call(context.Background(), GroupSession, "exists", map[string]string{"id": string(modulecore.NewSessionID()), "path": "/tmp/session"}, nil)
	var storageErr *Error
	if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeSchemaRejected {
		t.Fatalf("payload with path field error=%v, want schema_rejected", err)
	}
	if strings.Contains(err.Error(), "/tmp/session") {
		t.Fatalf("schema error echoed an arbitrary path: %v", err)
	}
}
