package storagehost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
	persistpersona "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/persona"
)

func TestPersonaGroupRegistersEveryExistingOwnerOperation(t *testing.T) {
	owner, err := persistpersona.NewSQLiteStore(filepath.Join(t.TempDir(), "persona.sqlite"))
	if err != nil {
		t.Fatalf("open Persona owner: %v", err)
	}
	defer owner.Close()
	host, err := NewHandler(HandlerConfig{Token: "persona-contract-token", JournalDir: filepath.Join(t.TempDir(), "journal")})
	if err != nil {
		t.Fatalf("open storage host: %v", err)
	}
	defer host.Close()
	if err := RegisterPersonaGroup(host, owner); err != nil {
		t.Fatalf("register Persona group: %v", err)
	}

	want := map[string]bool{
		"list_discomfort_logs":         false,
		"list_trigger_logs":            false,
		"list_canonical_response_logs": false,
		"list_observation_logs":        false,
		"list_meta_profile_updates":    false,
		"list_interface_sessions":      false,
		"find_observation_log_by_id":   false,
		"save_discomfort_log":          true,
		"save_trigger_log":             true,
		"save_canonical_response_log":  true,
		"save_observation_log":         true,
		"save_meta_profile_update":     true,
		"save_interface_session":       true,
	}
	got := make(map[string]bool)
	for _, spec := range host.specs {
		if spec.Group == GroupPersonaArchitecture {
			got[spec.Op] = spec.Mutating
			if spec.Mutating {
				if _, ok := host.recoverable[opKey(GroupPersonaArchitecture, spec.Op)]; !ok {
					t.Errorf("Persona mutation %q is not registered as recoverable", spec.Op)
				}
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Persona operation contract=%v, want %v", got, want)
	}
}

func TestPersonaGroupRejectsOverLimitListRequest(t *testing.T) {
	f := newPersonaStorageHostFixture(t, t.TempDir())
	var items []domainpersona.DiscomfortLog
	err := f.rpc.Call(context.Background(), GroupPersonaArchitecture, personaOpListDiscomfortLogs, map[string]any{"limit": personaListMaxLimit + 1}, &items)
	if personaStorageHostErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("over-limit request err=%v, want schema rejection", err)
	}
	if f.counted.lastDiscomfortLimit != 0 {
		t.Fatalf("over-limit request reached owner with limit=%d", f.counted.lastDiscomfortLimit)
	}
	items, err = f.client.ListDiscomfortLogs(context.Background(), personaListMaxLimit+1)
	if personaStorageHostErrorCode(err) != ErrorCodeSchemaRejected || items != nil {
		t.Fatalf("typed client over-limit result=%#v err=%v, want local schema rejection", items, err)
	}
	if f.counted.lastDiscomfortLimit != 0 {
		t.Fatalf("typed over-limit request reached owner with limit=%d", f.counted.lastDiscomfortLimit)
	}
}

func TestPersonaGroupUsesDefaultAndMaximumCompatibleListLimits(t *testing.T) {
	f := newPersonaStorageHostFixture(t, t.TempDir())
	if _, err := f.client.ListDiscomfortLogs(context.Background(), 0); err != nil {
		t.Fatalf("default Persona list: %v", err)
	}
	if f.counted.lastDiscomfortLimit != personaListDefaultLimit {
		t.Fatalf("default owner limit=%d; want %d", f.counted.lastDiscomfortLimit, personaListDefaultLimit)
	}
	if _, err := f.client.ListDiscomfortLogs(context.Background(), personaListMaxLimit); err != nil {
		t.Fatalf("maximum compatible Persona list: %v", err)
	}
	if f.counted.lastDiscomfortLimit != personaListMaxLimit {
		t.Fatalf("maximum owner limit=%d; want %d", f.counted.lastDiscomfortLimit, personaListMaxLimit)
	}
}

func TestPersonaGroupRejectsOwnerListOverReturn(t *testing.T) {
	f := newPersonaStorageHostFixture(t, t.TempDir())
	f.counted.overreturnDiscomfort = true
	items, err := f.client.ListDiscomfortLogs(context.Background(), 2)
	if personaStorageHostErrorCode(err) != ErrorCodeStoreUnavailable || items != nil {
		t.Fatalf("over-return result=%#v err=%v, want fail-closed store_unavailable", items, err)
	}
}

func TestPersonaClientRejectsListBeyondExplicitAndDefaultLimit(t *testing.T) {
	tests := []struct {
		name     string
		limit    int
		rowCount int
	}{
		{name: "explicit", limit: 2, rowCount: 3},
		{name: "default", limit: 0, rowCount: 51},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows := make([]domainpersona.DiscomfortLog, test.rowCount)
			encodedRows, err := json.Marshal(rows)
			if err != nil {
				t.Fatalf("marshal rows: %v", err)
			}
			response, err := json.Marshal(Response{Result: encodedRows})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			client, err := NewClient(ClientConfig{
				Endpoint: "http://persona-list-fixture.invalid", Token: "persona-list-token",
				HTTPClient: &http.Client{Transport: staticResponseRoundTripper(response)},
			})
			if err != nil {
				t.Fatalf("NewClient(): %v", err)
			}
			client.generation = 1
			client.specs[opKey(GroupPersonaArchitecture, personaOpListDiscomfortLogs)] = false
			items, err := NewPersonaClient(client).ListDiscomfortLogs(context.Background(), test.limit)
			if err == nil || items != nil {
				t.Fatalf("client accepted %d rows for limit %d: items=%d err=%v", test.rowCount, test.limit, len(items), err)
			}
		})
	}
}

func TestPersonaGroupRejectsUnknownDTOFields(t *testing.T) {
	f := newPersonaStorageHostFixture(t, t.TempDir())
	var listed []domainpersona.DiscomfortLog
	err := f.rpc.Call(context.Background(), GroupPersonaArchitecture, personaOpListDiscomfortLogs, map[string]any{"limit": 1, "unexpected": true}, &listed)
	if personaStorageHostErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unknown list field err=%v, want schema rejection", err)
	}
}

func TestPersonaGroupRejectsUnknownRecordDTOFields(t *testing.T) {
	f := newPersonaStorageHostFixture(t, t.TempDir())
	badRecord := json.RawMessage(`{"record":{"event_id":"evt-extra","character_id":"mio","discomfort":"too light","status":"candidate","created_at":"2026-10-04T12:00:00Z","unexpected":true}}`)
	operation := f.rpc.NewOperation(GroupPersonaArchitecture, personaOpSaveDiscomfortLog, badRecord)
	var saved domainpersona.DiscomfortLog
	err := f.rpc.Do(context.Background(), operation, &saved)
	if personaStorageHostErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unknown record field err=%v, want schema rejection", err)
	}
	items, err := f.owner.ListDiscomfortLogs(context.Background(), 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("owner rows after rejected DTO=%#v err=%v; want no mutation", items, err)
	}
}

func TestPersonaClientRoundTripsEverySaveListAndFindOperation(t *testing.T) {
	f := newPersonaStorageHostFixture(t, t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	discomfort := domainpersona.DiscomfortLog{
		EventID: "persona-discomfort-1", CharacterID: "mio", Discomfort: "too light", Status: "candidate", CreatedAt: now,
	}
	trigger := domainpersona.TriggerLog{
		EventID: "persona-trigger-1", CharacterID: "kuro", TriggerID: "danger", Activated: true, Confidence: 0.7, CreatedAt: now,
	}
	canonical := domainpersona.CanonicalResponseLog{
		EventID: "persona-canonical-1", CharacterID: "kuro", ResponseKey: "block_destructive", Used: true, CreatedAt: now,
	}
	observation := domainpersona.ObservationLog{
		EventID: "persona-observation-1", ObserverID: "mio", TargetID: "ren", ObservationType: "daily",
		Summary: "observation candidate", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: now,
	}
	binding := persistpersona.PersonaObservationStorageHostBinding{ActorObserverID: "mio", AuthenticatedTargetUserID: "ren"}
	meta := domainpersona.MetaProfileUpdate{
		UpdateID: "persona-meta-1", ObserverID: "mio", TargetID: "ren", Section: "Risk Signs",
		ProposedContent: "avoid rushed decisions while tired", Sensitivity: "health", ReviewStatus: "pending", CreatedAt: now,
	}
	session := domainpersona.InterfaceSession{
		SessionID: "persona-session-1", CharacterID: "mio", InterfaceType: "web", SessionKey: "web:viewer", CreatedAt: now,
	}

	assertPersonaSaveAndList(t, ctx, f.client.SaveDiscomfortLog, f.client.ListDiscomfortLogs, "discomfort", discomfort)
	assertPersonaSaveAndList(t, ctx, f.client.SaveTriggerLog, f.client.ListTriggerLogs, "trigger", trigger)
	assertPersonaSaveAndList(t, ctx, f.client.SaveCanonicalResponseLog, f.client.ListCanonicalResponseLogs, "canonical", canonical)
	assertPersonaSaveAndList(t, ctx, f.client.SaveMetaProfileUpdate, f.client.ListMetaProfileUpdates, "meta profile update", meta)
	assertPersonaSaveAndList(t, ctx, f.client.SaveInterfaceSession, f.client.ListInterfaceSessions, "interface session", session)
	storedObservation, err := f.client.SaveObservationLog(ctx, "persona-roundtrip-observation", binding, observation)
	if err != nil || !reflect.DeepEqual(storedObservation, observation) {
		t.Fatalf("save observation result=%#v err=%v; want exact %#v", storedObservation, err, observation)
	}
	observations, err := f.client.ListObservationLogs(ctx, 10)
	if err != nil || len(observations) != 1 || !reflect.DeepEqual(observations[0], observation) {
		t.Fatalf("list observations=%#v err=%v; want exact saved record", observations, err)
	}
	foundObservation, found, err := f.client.FindObservationLogByID(ctx, observation.EventID)
	if err != nil || !found || !reflect.DeepEqual(foundObservation, observation) {
		t.Fatalf("FindObservationLogByID()=%#v found=%v err=%v", foundObservation, found, err)
	}
	if _, found, err := f.client.FindObservationLogByID(ctx, "missing-observation"); err != nil || found {
		t.Fatalf("missing observation found=%v err=%v", found, err)
	}
}

func TestPersonaObservationRequiresActorAndAuthenticatedTargetBinding(t *testing.T) {
	f := newPersonaStorageHostFixture(t, t.TempDir())
	item := domainpersona.ObservationLog{
		EventID: "persona-bound-observation", ObserverID: "mio", TargetID: "ren", ObservationType: "daily",
		Summary: "observation candidate", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		name    string
		binding persistpersona.PersonaObservationStorageHostBinding
	}{
		{name: "wrong actor", binding: persistpersona.PersonaObservationStorageHostBinding{ActorObserverID: "shiro", AuthenticatedTargetUserID: "ren"}},
		{name: "wrong authenticated target", binding: persistpersona.PersonaObservationStorageHostBinding{ActorObserverID: "mio", AuthenticatedTargetUserID: "another-user"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := f.client.NewSaveObservationLogOperation("persona-binding-"+strings.ReplaceAll(test.name, " ", "-"), test.binding, item)
			var saved domainpersona.ObservationLog
			err := f.rpc.Do(context.Background(), operation, &saved)
			if personaStorageHostErrorCode(err) != ErrorCodeSchemaRejected {
				t.Fatalf("mismatched binding err=%v; want schema rejection", err)
			}
			observations, err := f.owner.ListObservationLogs(context.Background(), 10)
			if err != nil || len(observations) != 0 {
				t.Fatalf("observations after mismatched binding=%#v err=%v; want no mutation", observations, err)
			}
		})
	}
}

func TestPersonaLostResponseReopensOriginalReceiptWithoutRerun(t *testing.T) {
	f := newPersonaStorageHostFixture(t, t.TempDir())
	ctx := context.Background()
	item := domainpersona.ObservationLog{
		EventID: "persona-lost-response", ObserverID: "mio", TargetID: "ren", ObservationType: "daily",
		Summary: "original observation", Sensitivity: "normal", ReviewStatus: "pending", CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
	binding := persistpersona.PersonaObservationStorageHostBinding{ActorObserverID: "mio", AuthenticatedTargetUserID: "ren"}
	const opID = "persona-lost-response-reopen"
	f.host.crashAfterCommitFor = opID
	operation := f.client.NewSaveObservationLogOperation(opID, binding, item)
	var first domainpersona.ObservationLog
	if err := f.rpc.Do(ctx, operation, &first); personaStorageHostErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("first save result=%#v err=%v; want outcome_unknown after committed lost response", first, err)
	}
	if got := f.observationSaveCalls.Load(); got != 1 {
		t.Fatalf("owner save count before reopen=%d, want 1", got)
	}

	updated := item
	updated.Summary = "later valid canonical update"
	if err := f.owner.SaveObservationLog(ctx, updated); err != nil {
		t.Fatalf("later canonical update failed: %v", err)
	}
	identity, err := personaOperationIdentity(MutationMetadata{
		OpID: opID, Payload: operation.payload, JournalGeneration: f.host.Generation(), RequestGeneration: f.host.Generation(),
	}, personaOpSaveObservationLog)
	if err != nil {
		t.Fatalf("derive owner operation identity: %v", err)
	}
	if _, found, err := f.owner.LookupPersonaObservationLogStorageHostReceipt(ctx, identity, binding, item); err != nil || !found {
		t.Fatalf("original owner receipt before reopen found=%v err=%v", found, err)
	}

	f.close()
	f.open(t, true)
	retry := f.client.NewSaveObservationLogOperation(opID, binding, item)
	var recovered domainpersona.ObservationLog
	if err := f.rpc.Do(ctx, retry, &recovered); err != nil {
		t.Fatalf("reopened exact operation failed: %v", err)
	}
	if !f.counted.lastObservationLookupFound || f.counted.lastObservationLookupError != nil {
		t.Fatalf("reopened reconciliation found=%v err=%v", f.counted.lastObservationLookupFound, f.counted.lastObservationLookupError)
	}
	if !reflect.DeepEqual(recovered, item) {
		t.Fatalf("recovered result=%#v; want original result %#v", recovered, item)
	}
	if got := f.observationSaveCalls.Load(); got != 1 {
		t.Fatalf("owner save count after reopen=%d, want no rerun (1 total)", got)
	}
	current, found, err := f.owner.FindObservationLogByID(ctx, item.EventID)
	if err != nil || !found || current.Summary != updated.Summary {
		t.Fatalf("current observation=%#v found=%v err=%v; want later canonical update", current, found, err)
	}
}

func assertPersonaSaveAndList[T any](
	t *testing.T,
	ctx context.Context,
	save func(context.Context, string, T) (T, error),
	list func(context.Context, int) ([]T, error),
	name string,
	item T,
) {
	t.Helper()
	got, err := save(ctx, "persona-roundtrip-"+strings.ReplaceAll(name, " ", "_"), item)
	if err != nil || !reflect.DeepEqual(got, item) {
		t.Fatalf("save %s result=%#v err=%v; want exact %#v", name, got, err, item)
	}
	items, err := list(ctx, 10)
	if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], item) {
		t.Fatalf("list %s items=%#v err=%v; want exact saved record", name, items, err)
	}
}

type personaStorageHostFixture struct {
	root                 string
	database             string
	owner                *persistpersona.SQLiteStore
	counted              *countedPersonaGroupOwner
	host                 *Handler
	server               *httptest.Server
	rpc                  *Client
	client               *PersonaClient
	observationSaveCalls *atomic.Int32
}

type countedPersonaGroupOwner struct {
	PersonaGroupOwner
	store                         *persistpersona.SQLiteStore
	observationSaveCalls          *atomic.Int32
	lastDiscomfortLimit           int
	overreturnDiscomfort          bool
	lastObservationLookupIdentity persistpersona.PersonaStorageHostOperationIdentity
	lastObservationLookupFound    bool
	lastObservationLookupError    error
}

func (owner *countedPersonaGroupOwner) ListDiscomfortLogs(ctx context.Context, limit int) ([]domainpersona.DiscomfortLog, error) {
	owner.lastDiscomfortLimit = limit
	if owner.overreturnDiscomfort {
		return make([]domainpersona.DiscomfortLog, limit+1), nil
	}
	return owner.PersonaGroupOwner.ListDiscomfortLogs(ctx, limit)
}

func (owner *countedPersonaGroupOwner) SaveObservationLogForStorageHostOperation(ctx context.Context, identity persistpersona.PersonaStorageHostOperationIdentity, binding persistpersona.PersonaObservationStorageHostBinding, item domainpersona.ObservationLog) (domainpersona.ObservationLog, error) {
	owner.observationSaveCalls.Add(1)
	return owner.store.SaveObservationLogForStorageHostOperation(ctx, identity, binding, item)
}

func (owner *countedPersonaGroupOwner) LookupPersonaObservationLogStorageHostReceipt(ctx context.Context, identity persistpersona.PersonaStorageHostOperationIdentity, binding persistpersona.PersonaObservationStorageHostBinding, expected domainpersona.ObservationLog) (json.RawMessage, bool, error) {
	result, found, err := owner.store.LookupPersonaObservationLogStorageHostReceipt(ctx, identity, binding, expected)
	owner.lastObservationLookupIdentity = identity
	owner.lastObservationLookupFound = found
	owner.lastObservationLookupError = err
	return result, found, err
}

func newPersonaStorageHostFixture(t *testing.T, root string) *personaStorageHostFixture {
	t.Helper()
	f := &personaStorageHostFixture{root: root, database: filepath.Join(root, "persona.sqlite")}
	f.observationSaveCalls = &atomic.Int32{}
	f.open(t, false)
	t.Cleanup(f.close)
	return f
}

func (f *personaStorageHostFixture) open(t *testing.T, reuseClient bool) {
	t.Helper()
	owner, err := persistpersona.NewSQLiteStore(f.database)
	if err != nil {
		t.Fatalf("open Persona owner: %v", err)
	}
	f.owner = owner
	f.counted = &countedPersonaGroupOwner{PersonaGroupOwner: owner, store: owner, observationSaveCalls: f.observationSaveCalls}
	f.host, err = NewHandler(HandlerConfig{Token: "persona-test-token", JournalDir: filepath.Join(f.root, "journal")})
	if err != nil {
		_ = owner.Close()
		t.Fatalf("open Persona storage host: %v", err)
	}
	if err := RegisterPersonaGroup(f.host, f.counted); err != nil {
		_ = f.host.Close()
		_ = owner.Close()
		t.Fatalf("register Persona group: %v", err)
	}
	f.server = httptest.NewServer(f.host)
	if reuseClient {
		if f.rpc == nil {
			f.close()
			t.Fatal("cannot reuse an uninitialized Persona storage host client")
		}
		f.rpc.endpoint = f.server.URL + RPCPath
		f.rpc.httpClient = f.server.Client()
	} else {
		f.rpc, err = NewClient(ClientConfig{Endpoint: f.server.URL, Token: "persona-test-token", HTTPClient: f.server.Client()})
		if err != nil {
			f.close()
			t.Fatalf("create Persona storage host client: %v", err)
		}
	}
	if err := f.rpc.Handshake(context.Background()); err != nil {
		f.close()
		t.Fatalf("handshake Persona storage host client: %v", err)
	}
	f.client = NewPersonaClient(f.rpc)
}

func (f *personaStorageHostFixture) close() {
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.host != nil {
		_ = f.host.Close()
		f.host = nil
	}
	if f.owner != nil {
		_ = f.owner.Close()
		f.owner = nil
	}
}

func personaStorageHostErrorCode(err error) string {
	var storageErr *Error
	if errors.As(err, &storageErr) {
		return storageErr.Code
	}
	return ""
}
