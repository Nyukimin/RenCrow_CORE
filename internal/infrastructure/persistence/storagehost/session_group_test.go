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
		LoadCanonical(context.Context, string, conversation.ChannelAddress) (*domainsession.Session, bool, error)
		LoadOrCreateCanonical(context.Context, string, conversation.ChannelAddress, time.Time) (*domainsession.Session, bool, error)
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
	created   bool
}

func (o *countedCanonicalSessionOwner) LoadOrCreateCanonical(ctx context.Context, logicalDate string, address conversation.ChannelAddress, createdAt time.Time) (*domainsession.Session, bool, error) {
	o.lookups++
	value, created, err := o.JSONSessionRepository.LoadOrCreateCanonical(ctx, logicalDate, address, createdAt)
	if err == nil {
		o.createdID = value.ID()
		o.created = created
	}
	return value, created, err
}

type canonicalReadSessionOwner struct {
	*session.JSONSessionRepository
	lookups int
	value   *domainsession.Session
	found   bool
	err     error
}

type countedReservationSessionOwner struct {
	*session.JSONSessionRepository
	reservations int
	reads        int
}

func (o *countedReservationSessionOwner) ReserveCanonicalV2(ctx context.Context, logicalDate string, address conversation.ChannelAddress, ownerID string, createdAt time.Time) (*domainsession.Session, bool, error) {
	o.reservations++
	return o.JSONSessionRepository.ReserveCanonicalV2(ctx, logicalDate, address, ownerID, createdAt)
}

func (o *countedReservationSessionOwner) LoadCanonicalWithCreationIntent(ctx context.Context, logicalDate string, address conversation.ChannelAddress) (*domainsession.Session, *domainsession.CreationIntent, bool, error) {
	o.reads++
	return o.JSONSessionRepository.LoadCanonicalWithCreationIntent(ctx, logicalDate, address)
}

func (o *canonicalReadSessionOwner) LoadCanonical(ctx context.Context, logicalDate string, address conversation.ChannelAddress) (*domainsession.Session, bool, error) {
	o.lookups++
	if o.err != nil || o.value != nil || o.found {
		return o.value, o.found, o.err
	}
	return o.JSONSessionRepository.LoadCanonical(ctx, logicalDate, address)
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
	first, firstCreated, err := fixture.client.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, createdAt)
	if err != nil {
		t.Fatalf("first LoadOrCreateCanonical: %v", err)
	}
	if !firstCreated {
		t.Fatal("first LoadOrCreateCanonical created=false, want true")
	}
	second, secondCreated, err := fixture.client.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, createdAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second LoadOrCreateCanonical: %v", err)
	}
	if secondCreated {
		t.Fatal("second LoadOrCreateCanonical created=true, want false")
	}
	if first.ID() == "" || first.ID() != second.ID() {
		t.Fatalf("same canonical lookup returned IDs %q and %q", first.ID(), second.ID())
	}
}

func TestSessionGroupLoadCanonicalReportsAbsentAndPresent(t *testing.T) {
	owner := newSessionGroupOwner(t)
	fixture := newSessionGroupFixture(t, owner)
	address, err := conversation.NewChannelAddress("line", "U123")
	if err != nil {
		t.Fatal(err)
	}

	missing, found, err := fixture.client.LoadCanonical(context.Background(), "2026-10-03", address)
	if err != nil || found || missing != nil {
		t.Fatalf("LoadCanonical before creation=(%v,%t,%v), want (nil,false,nil)", missing, found, err)
	}
	created, wasCreated, err := fixture.client.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	if err != nil || !wasCreated {
		t.Fatalf("LoadOrCreateCanonical=(%v,%t,%v), want newly-created session", created, wasCreated, err)
	}
	got, found, err := fixture.client.LoadCanonical(context.Background(), "2026-10-03", address)
	if err != nil || !found || got == nil || got.ID() != created.ID() {
		t.Fatalf("LoadCanonical after creation=(%v,%t,%v), want session %q", got, found, err, created.ID())
	}
}

func TestSessionGroupCanonicalReservationReadAndOwnerCheck(t *testing.T) {
	owner := newSessionGroupOwner(t)
	fixture := newSessionGroupFixture(t, owner)
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	value, intent, found, err := fixture.client.LoadCanonicalWithCreationIntent(context.Background(), "2026-10-03", address)
	if err != nil || found || value != nil || intent != nil {
		t.Fatalf("reservation lookup before creation=(%v,%+v,%t,%v), want absent", value, intent, found, err)
	}

	created, wasCreated, err := fixture.client.ReserveCanonicalV2(context.Background(), "2026-10-03", address, "ren", time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	if err != nil || !wasCreated {
		t.Fatalf("ReserveCanonicalV2=(%v,%t,%v), want newly created reservation", created, wasCreated, err)
	}
	loaded, intent, found, err := fixture.client.LoadCanonicalWithCreationIntent(context.Background(), "2026-10-03", address)
	if err != nil || !found || loaded == nil || loaded.ID() != created.ID() || intent == nil || intent.Runtime != "v2" || intent.OwnerID != "ren" {
		t.Fatalf("reservation lookup after creation=(%v,%+v,%t,%v), want session and matching intent", loaded, intent, found, err)
	}
	if reserved, err := fixture.client.IsCanonicalV2Reserved(context.Background(), created.ID(), "ren"); err != nil || !reserved {
		t.Fatalf("IsCanonicalV2Reserved(same owner)=(%t,%v), want true", reserved, err)
	}
	if reserved, err := fixture.client.IsCanonicalV2Reserved(context.Background(), created.ID(), "other"); err != nil || reserved {
		t.Fatalf("IsCanonicalV2Reserved(other owner)=(%t,%v), want false", reserved, err)
	}
}

func TestSessionGroupCanonicalReservationReadRejectsInvalidAndMismatchedOwnerResults(t *testing.T) {
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	owner := newSessionGroupOwner(t)
	fixture := newSessionGroupFixture(t, owner)
	if _, _, _, err := fixture.client.LoadCanonicalWithCreationIntent(context.Background(), "not-a-date", address); err == nil {
		t.Fatal("reservation read accepted an invalid logical date")
	} else {
		var storageErr *Error
		if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeSchemaRejected {
			t.Fatalf("invalid reservation lookup error=%v, want schema_rejected", err)
		}
	}

	badOwner := &canonicalReservationReplyOwner{
		JSONSessionRepository: owner,
		value:                 newStorageHostSession(t),
		intent:                &domainsession.CreationIntent{Runtime: "legacy", OwnerID: "ren"},
		found:                 true,
	}
	fixture = newSessionGroupFixture(t, badOwner)
	if _, _, _, err := fixture.client.LoadCanonicalWithCreationIntent(context.Background(), "2026-10-03", address); err == nil {
		t.Fatal("reservation read accepted an invalid intent and mismatched identity")
	}
}

type canonicalReservationReplyOwner struct {
	*session.JSONSessionRepository
	value  *domainsession.Session
	intent *domainsession.CreationIntent
	found  bool
}

func (o *canonicalReservationReplyOwner) LoadCanonicalWithCreationIntent(context.Context, string, conversation.ChannelAddress) (*domainsession.Session, *domainsession.CreationIntent, bool, error) {
	return o.value, o.intent, o.found, nil
}

func TestSessionGroupReserveCanonicalV2RecoversLostResponseFromSameOwnerReservation(t *testing.T) {
	owner := &countedReservationSessionOwner{JSONSessionRepository: newSessionGroupOwner(t)}
	fixture := newSessionGroupFixture(t, owner)
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	committedOpID := ""
	fixture.client.client.sendHook = func() error {
		fixture.client.client.sendHook = nil
		entries := fixture.handler.journal.entries()
		if len(entries) != 1 || entries[0].Status != journalStatusDone || entries[0].Op != "reserve_canonical_v2" {
			t.Errorf("journal at lost reservation response=%+v, want committed reservation", entries)
			return errConnectionResetAfterSend
		}
		committedOpID = entries[0].OpID
		if err := fixture.handler.journal.complete(entries[0].OpID, []byte(`{"found":true,"session":{"id":"bad-result"}}`)); err != nil {
			t.Errorf("replace stored reservation result: %v", err)
		}
		return errConnectionResetAfterSend
	}

	value, created, err := fixture.client.ReserveCanonicalV2(context.Background(), "2026-10-03", address, "ren", createdAt)
	if err != nil || created || value == nil {
		t.Fatalf("ReserveCanonicalV2 after lost response=(%v,%t,%v), want read-confirmed reservation", value, created, err)
	}
	if owner.reservations != 1 || owner.reads != 1 {
		t.Fatalf("owner reserve/read counts=%d/%d, want one mutation and one read-only reconciliation", owner.reservations, owner.reads)
	}
	entries := fixture.handler.journal.entries()
	if len(entries) != 1 || entries[0].OpID != committedOpID {
		t.Fatalf("reservation reconciliation used a different or repeated op_id: %+v", entries)
	}
	if reserved, checkErr := owner.IsCanonicalV2Reserved(context.Background(), value.ID(), "ren"); checkErr != nil || !reserved {
		t.Fatalf("owner reservation evidence=(%t,%v), want exact owner reservation", reserved, checkErr)
	}
}

func TestSessionGroupSaveCannotMutateOrOverwriteV2CreationIntent(t *testing.T) {
	owner := &countedSessionOwner{JSONSessionRepository: newSessionGroupOwner(t)}
	fixture := newSessionGroupFixture(t, owner)
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	reserved, _, err := fixture.client.ReserveCanonicalV2(context.Background(), "2026-10-03", address, "ren", time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("ReserveCanonicalV2: %v", err)
	}
	if err := fixture.client.Save(context.Background(), reserved); err == nil {
		t.Fatal("storagehost Save overwrote a v2-reserved session")
	}
	if owner.saves != 1 {
		t.Fatalf("owner Save calls=%d, want one rejected attempt", owner.saves)
	}

	dto, err := sessionRecordFromDomain(reserved)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := marshalSessionMessage(sessionSavePayload{Session: dto})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	var record map[string]json.RawMessage
	if err := json.Unmarshal(envelope["session"], &record); err != nil {
		t.Fatal(err)
	}
	record["creation_intent"] = json.RawMessage(`{"runtime":"legacy","owner_id":"ren"}`)
	envelope["session"], err = json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	mutatedPayload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	err = fixture.client.client.Call(context.Background(), GroupSession, "save", json.RawMessage(mutatedPayload), nil)
	var storageErr *Error
	if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeSchemaRejected {
		t.Fatalf("Save payload with a forged creation_intent error=%v, want schema_rejected", err)
	}
	if owner.saves != 1 {
		t.Fatalf("owner Save calls after forged payload=%d, want no execution beyond the rejected valid DTO", owner.saves)
	}
	value, intent, found, err := fixture.client.LoadCanonicalWithCreationIntent(context.Background(), "2026-10-03", address)
	if err != nil || !found || value.ID() != reserved.ID() || intent == nil || intent.Runtime != "v2" || intent.OwnerID != "ren" {
		t.Fatalf("reservation after rejected Save=(%v,%+v,%t,%v), want immutable original intent", value, intent, found, err)
	}
}

func TestSessionGroupLoadCanonicalRejectsInvalidLookupAndMismatchedOwnerReply(t *testing.T) {
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatal(err)
	}
	countedOwner := &canonicalReadSessionOwner{JSONSessionRepository: newSessionGroupOwner(t)}
	fixture := newSessionGroupFixture(t, countedOwner)
	if _, _, err := fixture.client.LoadCanonical(context.Background(), "not-a-date", address); err == nil {
		t.Fatal("LoadCanonical accepted an invalid logical date")
	} else {
		var storageErr *Error
		if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeSchemaRejected {
			t.Fatalf("invalid lookup error=%v, want schema_rejected", err)
		}
	}
	if countedOwner.lookups != 0 {
		t.Fatalf("invalid lookup reached owner %d times, want zero", countedOwner.lookups)
	}
	entries := fixture.handler.journal.entries()
	if len(entries) != 0 {
		t.Fatalf("invalid lookup retained operations: %+v", entries)
	}

	countedOwner.value = newStorageHostSession(t)
	countedOwner.found = true
	if _, _, err := fixture.client.LoadCanonical(context.Background(), "2026-10-03", address); err == nil {
		t.Fatal("LoadCanonical accepted a mismatched owner reply")
	} else {
		var storageErr *Error
		if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeStoreUnavailable {
			t.Fatalf("mismatched owner reply error=%v, want store_unavailable", err)
		}
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

	_, _, firstErr := fixture.client.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, createdAt)
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

	_, _, retryErr := fixture.client.LoadOrCreateCanonical(context.Background(), "2026-10-03", address, createdAt)
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

type canonicalOwnerSessionRepository interface {
	LoadCanonicalForOwner(context.Context, string, string, conversation.ChannelAddress) (domainsession.CanonicalSessionOwnerResolution, error)
	LoadOrCreateCanonicalForOwner(context.Context, string, conversation.ChannelAddress, string, time.Time) (domainsession.CanonicalSessionOwnerResolution, error)
}

type canonicalOwnerSessionOwnerStub struct {
	*session.JSONSessionRepository
	readCalls   int
	createCalls int
	read        domainsession.CanonicalSessionOwnerResolution
	create      domainsession.CanonicalSessionOwnerResolution
	readInput   struct {
		id      string
		ownerID string
		address conversation.ChannelAddress
	}
	createInput struct {
		logicalDate string
		ownerID     string
		address     conversation.ChannelAddress
		createdAt   time.Time
	}
}

func (o *canonicalOwnerSessionOwnerStub) LoadCanonicalForOwner(_ context.Context, id, ownerID string, address conversation.ChannelAddress) (domainsession.CanonicalSessionOwnerResolution, error) {
	o.readCalls++
	o.readInput.id, o.readInput.ownerID, o.readInput.address = id, ownerID, address
	return o.read, nil
}

func (o *canonicalOwnerSessionOwnerStub) LoadOrCreateCanonicalForOwner(_ context.Context, logicalDate string, address conversation.ChannelAddress, ownerID string, createdAt time.Time) (domainsession.CanonicalSessionOwnerResolution, error) {
	o.createCalls++
	o.createInput.logicalDate, o.createInput.ownerID, o.createInput.address, o.createInput.createdAt = logicalDate, ownerID, address, createdAt
	return o.create, nil
}

func newOwnerBoundStorageHostSession(t *testing.T, ownerID string) (*domainsession.Session, conversation.ChannelAddress) {
	t.Helper()
	address, err := conversation.NewChannelAddress("line", "U-owner-bound")
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	value, err := domainsession.NewCanonicalSessionWithOwner(modulecore.NewSessionID(), ownerID, "2026-10-24", address, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	return value, address
}

func TestSessionGroupSaveAndLoadPreservesCanonicalOwnerBinding(t *testing.T) {
	owner := newSessionGroupOwner(t)
	fixture := newSessionGroupFixture(t, owner)
	want, _ := newOwnerBoundStorageHostSession(t, "ren")
	if err := fixture.client.Save(context.Background(), want); err != nil {
		t.Fatalf("Save owner-bound Session: %v", err)
	}
	got, err := fixture.client.Load(context.Background(), want.ID())
	if err != nil {
		t.Fatalf("Load owner-bound Session: %v", err)
	}
	if got.OwnerID() != "ren" {
		t.Fatalf("Load OwnerID() = %q, want immutable owner ren", got.OwnerID())
	}
}

func TestSessionGroupCanonicalOwnerProtocolDelegatesTypedResolution(t *testing.T) {
	value, address := newOwnerBoundStorageHostSession(t, "ren")
	owner := &canonicalOwnerSessionOwnerStub{
		JSONSessionRepository: newSessionGroupOwner(t),
		read: domainsession.CanonicalSessionOwnerResolution{
			Session: value, BoundOwnerID: "ren", Status: domainsession.CanonicalSessionOwnerMatched,
		},
		create: domainsession.CanonicalSessionOwnerResolution{
			Session: value, BoundOwnerID: "ren", Status: domainsession.CanonicalSessionOwnerCreated,
		},
	}
	fixture := newSessionGroupFixture(t, owner)
	api, ok := any(fixture.client).(canonicalOwnerSessionRepository)
	if !ok {
		t.Fatal("SessionRepositoryClient does not expose canonical owner operations")
	}

	read, err := api.LoadCanonicalForOwner(context.Background(), value.ID(), "ren", address)
	if err != nil || read.Status != domainsession.CanonicalSessionOwnerMatched || read.Session == nil || read.Session.ID() != value.ID() || read.Session.OwnerID() != "ren" || read.BoundOwnerID != "ren" {
		t.Fatalf("LoadCanonicalForOwner() = %+v, %v; want matched owner-bound Session", read, err)
	}
	if owner.readCalls != 1 || owner.readInput.id != value.ID() || owner.readInput.ownerID != "ren" || owner.readInput.address != address {
		t.Fatalf("owner read delegation = calls:%d input:%+v, want exact request", owner.readCalls, owner.readInput)
	}

	createdAt := value.CreatedAt().Add(time.Hour)
	created, err := api.LoadOrCreateCanonicalForOwner(context.Background(), value.LogicalDate(), address, "ren", createdAt)
	if err != nil || created.Status != domainsession.CanonicalSessionOwnerCreated || created.Session == nil || created.Session.ID() != value.ID() || created.Session.OwnerID() != "ren" || created.BoundOwnerID != "ren" {
		t.Fatalf("LoadOrCreateCanonicalForOwner() = %+v, %v; want created owner-bound Session", created, err)
	}
	if owner.createCalls != 1 || owner.createInput.logicalDate != value.LogicalDate() || owner.createInput.ownerID != "ren" || owner.createInput.address != address || !owner.createInput.createdAt.Equal(createdAt) {
		t.Fatalf("owner create delegation = calls:%d input:%+v, want exact request", owner.createCalls, owner.createInput)
	}
}

func TestSessionGroupCanonicalOwnerResolutionUsesJSONSessionOwner(t *testing.T) {
	owner := newSessionGroupOwner(t)
	fixture := newSessionGroupFixture(t, owner)
	want, address := newOwnerBoundStorageHostSession(t, "ren")
	if err := owner.Save(context.Background(), want); err != nil {
		t.Fatalf("seed owner-bound Session: %v", err)
	}
	api := canonicalOwnerSessionRepository(fixture.client)

	matched, err := api.LoadCanonicalForOwner(context.Background(), want.ID(), "ren", address)
	if err != nil || matched.Status != domainsession.CanonicalSessionOwnerMatched || matched.Session == nil || matched.Session.ID() != want.ID() || matched.BoundOwnerID != "ren" {
		t.Fatalf("LoadCanonicalForOwner(same owner) = %+v, %v; want matched", matched, err)
	}
	conflict, err := api.LoadCanonicalForOwner(context.Background(), want.ID(), "other", address)
	if err != nil || conflict.Status != domainsession.CanonicalSessionOwnerConflict || conflict.Session != nil || conflict.BoundOwnerID != "" {
		t.Fatalf("LoadCanonicalForOwner(other owner) = %+v, %v; want payload-free conflict", conflict, err)
	}
	otherAddress, err := conversation.NewChannelAddress("line", "U-other")
	if err != nil {
		t.Fatal(err)
	}
	conflict, err = api.LoadCanonicalForOwner(context.Background(), want.ID(), "ren", otherAddress)
	if err != nil || conflict.Status != domainsession.CanonicalSessionOwnerConflict || conflict.Session != nil {
		t.Fatalf("LoadCanonicalForOwner(other address) = %+v, %v; want payload-free conflict", conflict, err)
	}
	missing, err := api.LoadCanonicalForOwner(context.Background(), string(modulecore.NewSessionID()), "ren", address)
	if err != nil || missing.Status != domainsession.CanonicalSessionOwnerNotFound || missing.Session != nil {
		t.Fatalf("LoadCanonicalForOwner(missing) = %+v, %v; want payload-free not_found", missing, err)
	}

	legacy := newStorageHostSession(t)
	if err := owner.Save(context.Background(), legacy); err != nil {
		t.Fatalf("seed legacy Session: %v", err)
	}
	legacyResolution, err := api.LoadCanonicalForOwner(context.Background(), legacy.ID(), "ren", legacy.ChannelAddress())
	if err != nil || legacyResolution.Status != domainsession.CanonicalSessionOwnerUnavailable || legacyResolution.Session != nil || legacyResolution.BoundOwnerID != "" {
		t.Fatalf("LoadCanonicalForOwner(legacy) = %+v, %v; want payload-free unavailable", legacyResolution, err)
	}
}

func TestSessionGroupCanonicalOwnerProtocolFailsClosedOnUnboundStatuses(t *testing.T) {
	value, address := newOwnerBoundStorageHostSession(t, "ren")
	for _, status := range []domainsession.CanonicalSessionOwnerStatus{
		domainsession.CanonicalSessionOwnerUnavailable,
		domainsession.CanonicalSessionOwnerConflict,
		domainsession.CanonicalSessionOwnerNotFound,
	} {
		t.Run(string(status), func(t *testing.T) {
			owner := &canonicalOwnerSessionOwnerStub{
				JSONSessionRepository: newSessionGroupOwner(t),
				read:                  domainsession.CanonicalSessionOwnerResolution{Status: status},
			}
			fixture := newSessionGroupFixture(t, owner)
			api, ok := any(fixture.client).(canonicalOwnerSessionRepository)
			if !ok {
				t.Fatal("SessionRepositoryClient does not expose canonical owner operations")
			}
			got, err := api.LoadCanonicalForOwner(context.Background(), value.ID(), "ren", address)
			if err != nil || got.Status != status || got.Session != nil || got.BoundOwnerID != "" {
				t.Fatalf("LoadCanonicalForOwner() = %+v, %v; want payload-free %s", got, err, status)
			}
		})
	}
}

func TestSessionGroupCanonicalOwnerRequestRejectsUserIDAndDuplicateOwnerAliases(t *testing.T) {
	value, address := newOwnerBoundStorageHostSession(t, "ren")
	owner := &canonicalOwnerSessionOwnerStub{JSONSessionRepository: newSessionGroupOwner(t)}
	fixture := newSessionGroupFixture(t, owner)
	addressDTO := sessionAddressFromDomain(address)
	addressJSON, err := json.Marshal(addressDTO)
	if err != nil {
		t.Fatal(err)
	}
	base := `"id":"` + value.ID() + `","channel_address":` + string(addressJSON)
	for name, raw := range map[string]string{
		"caller user_id":      `{"id":"` + value.ID() + `","owner_id":"ren","user_id":"ren","channel_address":` + string(addressJSON) + `}`,
		"duplicate owner_id":  `{` + base + `,"owner_id":"ren","owner_id":"other"}`,
		"owner_id case alias": `{` + base + `,"owner_id":"ren","OWNER_ID":"other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			err := fixture.client.client.Call(context.Background(), GroupSession, "load_canonical_for_owner", json.RawMessage(raw), nil)
			var storageErr *Error
			if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeSchemaRejected {
				t.Fatalf("owner request error = %v, want schema_rejected", err)
			}
			if owner.readCalls != 0 {
				t.Fatalf("owner read calls = %d, want zero for rejected input", owner.readCalls)
			}
		})
	}
}

func TestSessionGroupSaveRejectsDuplicateOrAliasedOwnerProvenance(t *testing.T) {
	owner := &countedSessionOwner{JSONSessionRepository: newSessionGroupOwner(t)}
	fixture := newSessionGroupFixture(t, owner)
	value, _ := newOwnerBoundStorageHostSession(t, "ren")
	dto, err := sessionRecordFromDomain(value)
	if err != nil {
		t.Fatalf("sessionRecordFromDomain: %v", err)
	}
	record, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal session DTO: %v", err)
	}
	baseRecord := string(record[:len(record)-1])
	for name, suffix := range map[string]string{
		"duplicate owner_id":  `,"owner_id":"other"}`,
		"owner_id case alias": `,"OWNER_ID":"other"}`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := json.RawMessage(`{"session":` + baseRecord + suffix + `}`)
			err := fixture.client.client.Call(context.Background(), GroupSession, "save", raw, nil)
			var storageErr *Error
			if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeSchemaRejected {
				t.Fatalf("duplicate owner provenance error = %v, want schema_rejected", err)
			}
			if owner.saves != 0 {
				t.Fatalf("owner Save calls = %d, want zero for rejected DTO", owner.saves)
			}
		})
	}
}

func TestSessionGroupCanonicalOwnerRejectsInvalidOwnerResolution(t *testing.T) {
	value, address := newOwnerBoundStorageHostSession(t, "ren")
	tests := []struct {
		name       string
		resolution domainsession.CanonicalSessionOwnerResolution
	}{
		{
			name: "matched owner mismatch",
			resolution: domainsession.CanonicalSessionOwnerResolution{
				Session: value, BoundOwnerID: "other", Status: domainsession.CanonicalSessionOwnerMatched,
			},
		},
		{
			name: "unavailable leaks session",
			resolution: domainsession.CanonicalSessionOwnerResolution{
				Session: value, Status: domainsession.CanonicalSessionOwnerUnavailable,
			},
		},
		{
			name: "unknown status",
			resolution: domainsession.CanonicalSessionOwnerResolution{
				Status: domainsession.CanonicalSessionOwnerStatus("future_status"),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			owner := &canonicalOwnerSessionOwnerStub{
				JSONSessionRepository: newSessionGroupOwner(t), read: test.resolution,
			}
			fixture := newSessionGroupFixture(t, owner)
			api, ok := any(fixture.client).(canonicalOwnerSessionRepository)
			if !ok {
				t.Fatal("SessionRepositoryClient does not expose canonical owner operations")
			}
			_, err := api.LoadCanonicalForOwner(context.Background(), value.ID(), "ren", address)
			var storageErr *Error
			if !errors.As(err, &storageErr) || storageErr.Code != ErrorCodeStoreUnavailable {
				t.Fatalf("invalid owner result error = %v, want store_unavailable", err)
			}
		})
	}
}

func TestSessionGroupCanonicalOwnerClientValidatesOwnerAndAddress(t *testing.T) {
	value, address := newOwnerBoundStorageHostSession(t, "ren")
	owner := &canonicalOwnerSessionOwnerStub{JSONSessionRepository: newSessionGroupOwner(t)}
	fixture := newSessionGroupFixture(t, owner)
	api, ok := any(fixture.client).(canonicalOwnerSessionRepository)
	if !ok {
		t.Fatal("SessionRepositoryClient does not expose canonical owner operations")
	}
	if _, err := api.LoadCanonicalForOwner(context.Background(), "not-a-session", "ren", address); err == nil {
		t.Fatal("LoadCanonicalForOwner accepted a noncanonical SessionID")
	}
	if _, err := api.LoadCanonicalForOwner(context.Background(), value.ID(), "", address); err == nil {
		t.Fatal("LoadCanonicalForOwner accepted an empty owner")
	}
	if _, err := api.LoadCanonicalForOwner(context.Background(), value.ID(), "ren", conversation.ChannelAddress{}); err == nil {
		t.Fatal("LoadCanonicalForOwner accepted an invalid ChannelAddress")
	}
	if _, err := api.LoadOrCreateCanonicalForOwner(context.Background(), "not-a-date", address, "ren", value.CreatedAt()); err == nil {
		t.Fatal("LoadOrCreateCanonicalForOwner accepted an invalid logical date")
	}
	if _, err := api.LoadOrCreateCanonicalForOwner(context.Background(), value.LogicalDate(), conversation.ChannelAddress{}, "ren", value.CreatedAt()); err == nil {
		t.Fatal("LoadOrCreateCanonicalForOwner accepted an invalid ChannelAddress")
	}
	if _, err := api.LoadOrCreateCanonicalForOwner(context.Background(), value.LogicalDate(), address, "", value.CreatedAt()); err == nil {
		t.Fatal("LoadOrCreateCanonicalForOwner accepted an empty owner")
	}
	if owner.readCalls != 0 || owner.createCalls != 0 || len(fixture.handler.journal.entries()) != 0 {
		t.Fatalf("invalid client requests reached owner or journal: read=%d create=%d journal=%+v", owner.readCalls, owner.createCalls, fixture.handler.journal.entries())
	}
}

func TestSessionGroupOwnerCreationUncertaintyRetainsOperationID(t *testing.T) {
	value, address := newOwnerBoundStorageHostSession(t, "ren")
	owner := &canonicalOwnerSessionOwnerStub{
		JSONSessionRepository: newSessionGroupOwner(t),
		create: domainsession.CanonicalSessionOwnerResolution{
			Session: value, BoundOwnerID: "ren", Status: domainsession.CanonicalSessionOwnerCreated,
		},
	}
	fixture := newSessionGroupFixture(t, owner)
	api, ok := any(fixture.client).(canonicalOwnerSessionRepository)
	if !ok {
		t.Fatal("SessionRepositoryClient does not expose canonical owner operations")
	}
	createdAt := value.CreatedAt()
	committedOpID := ""
	fixture.client.client.sendHook = func() error {
		fixture.client.client.sendHook = nil
		entries := fixture.handler.journal.entries()
		if len(entries) != 1 || entries[0].Status != journalStatusDone || entries[0].Op != "load_or_create_canonical_for_owner" {
			t.Errorf("journal at lost owner-create response = %+v, want committed operation", entries)
			return errConnectionResetAfterSend
		}
		committedOpID = entries[0].OpID
		if err := fixture.handler.journal.complete(entries[0].OpID, []byte(`{"status":"created","bound_owner_id":"ren","session":{"id":"bad-result"}}`)); err != nil {
			t.Errorf("replace stored owner-create result: %v", err)
		}
		return errConnectionResetAfterSend
	}

	_, firstErr := api.LoadOrCreateCanonicalForOwner(context.Background(), value.LogicalDate(), address, "ren", createdAt)
	if !isOutcomeUnknown(firstErr) {
		t.Fatalf("first owner-create error = %v, want outcome_unknown", firstErr)
	}
	_, retryErr := api.LoadOrCreateCanonicalForOwner(context.Background(), value.LogicalDate(), address, "ren", createdAt)
	if !isOutcomeUnknown(retryErr) {
		t.Fatalf("same owner-create retry error = %v, want outcome_unknown", retryErr)
	}
	if owner.createCalls != 1 {
		t.Fatalf("owner create calls = %d, want one after unresolved result", owner.createCalls)
	}
	entries := fixture.handler.journal.entries()
	if len(entries) != 1 || entries[0].OpID != committedOpID || entries[0].Status != journalStatusDone {
		t.Fatalf("journal after unresolved retry = %+v, want original DONE op_id %q", entries, committedOpID)
	}
}

func TestSessionGroupContractIsClosedAndRejectsPathOperations(t *testing.T) {
	fixture := newSessionGroupFixture(t, newSessionGroupOwner(t))
	contract, err := fixture.client.client.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	want := map[string]bool{
		"save":                               true,
		"load":                               false,
		"exists":                             false,
		"delete":                             true,
		"load_canonical":                     false,
		"load_canonical_for_owner":           false,
		"load_canonical_reservation":         false,
		"load_or_create_canonical":           true,
		"load_or_create_canonical_for_owner": true,
		"reserve_canonical_v2":               true,
		"is_v2_reserved":                     false,
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
