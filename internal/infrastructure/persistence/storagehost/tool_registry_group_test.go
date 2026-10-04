package storagehost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/toolregistry"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const toolRegistryGroupTestToken = "tool-registry-group-test-token"

func TestToolRegistryGroupRegistersClosedOperations(t *testing.T) {
	fixture := newToolRegistryGroupFixture(t, t.TempDir())
	client := fixture.client
	contract, err := client.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	want := map[string]bool{
		"register": true, "register_with_receipt": true, "list_for_platform": false,
		"get": false, "find_action_receipt": false,
	}
	got := make(map[string]bool)
	for _, operation := range contract.Operations {
		if operation.Group == GroupToolRegistry {
			got[operation.Op] = operation.Mutating
		}
	}
	if len(got) != len(want) {
		t.Fatalf("tool registry operations=%v, want exactly %v", got, want)
	}
	for operation, mutating := range want {
		if got[operation] != mutating {
			t.Fatalf("tool registry operation %q mutating=%v, want %v", operation, got[operation], mutating)
		}
	}
}

func TestToolRegistryGroupRoundTripsConcreteCapabilityAndReceiptDTOs(t *testing.T) {
	fixture := newToolRegistryGroupFixture(t, t.TempDir())
	ctx := context.Background()
	var registry capability.ToolRegistry = NewToolRegistryClient(fixture.client)
	entry := toolRegistryEntry("round_trip")
	if err := registry.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := registry.Get(ctx, entry.Name)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != entry.Name || got.SchemaJSON != entry.SchemaJSON || got.Source != entry.Source || len(got.Platforms) != 1 || got.Platforms[0] != "linux" {
		t.Fatalf("Get()=%+v, want exact capability entry", got)
	}
	listed, err := registry.ListForPlatform(ctx, "linux")
	if err != nil || len(listed) != 1 || listed[0].Name != entry.Name {
		t.Fatalf("ListForPlatform()=%+v err=%v, want exact entry", listed, err)
	}
	var receiptOwner capability.ToolRegistryReceiptOwner = NewToolRegistryClient(fixture.client)
	actionID := modulecore.NewActionID()
	registered, err := receiptOwner.RegisterWithReceipt(ctx, toolRegistryEntry("receipt_round_trip"), actionID, "agent:mio", strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("RegisterWithReceipt: %v", err)
	}
	if registered.Receipt.ActionID != actionID || registered.Receipt.ActorID != "agent:mio" || registered.Receipt.PayloadHash != strings.Repeat("a", 64) || registered.Receipt.ToolName != "receipt_round_trip" {
		t.Fatalf("receipt result=%+v, want exact typed receipt", registered)
	}
	found, ok, err := receiptOwner.FindActionReceipt(ctx, actionID)
	if err != nil || !ok || found != registered.Receipt {
		t.Fatalf("FindActionReceipt()=%+v found=%v err=%v, want exact receipt DTO", found, ok, err)
	}
	if err := registry.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := registry.Get(ctx, entry.Name); err != nil {
		t.Fatalf("Close must not close shared RPC client: %v", err)
	}
}

func TestToolRegistryMutationsReconcileAfterHandlerAndOwnerReopen(t *testing.T) {
	receiptActionID := modulecore.NewActionID()
	for _, tc := range []struct {
		name string
		call func(*ToolRegistryClient, context.Context, capability.ToolEntry) error
	}{
		{name: "legacy_natural_row", call: func(c *ToolRegistryClient, ctx context.Context, entry capability.ToolEntry) error {
			return c.Register(ctx, entry)
		}},
		{name: "atomic_action_receipt", call: func(c *ToolRegistryClient, ctx context.Context, entry capability.ToolEntry) error {
			_, err := c.RegisterWithReceipt(ctx, entry, receiptActionID, "agent:mio", strings.Repeat("b", 64))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			fixture := newToolRegistryGroupFixture(t, root)
			fixture.client.opID = "tool-recovery-" + tc.name
			fixture.handler.crashAfterCommitFor = fixture.client.opID
			entry := toolRegistryEntry("recover_" + strings.ReplaceAll(tc.name, "_", ""))
			err := tc.call(NewToolRegistryClient(fixture.client), context.Background(), entry)
			expectToolRegistryError(t, err, ErrorCodeOutcomeUnknown)
			fixture.close()
			reopened := newToolRegistryGroupFixture(t, root)
			reopened.client.opID = fixture.client.opID
			if err := tc.call(NewToolRegistryClient(reopened.client), context.Background(), entry); err != nil {
				t.Fatalf("retry after owner/handler reopen: %v", err)
			}
			stored, err := reopened.owner.Get(context.Background(), entry.Name)
			if err != nil || stored.Name != entry.Name {
				t.Fatalf("stored=%+v err=%v, want committed entry", stored, err)
			}
		})
	}
}

func TestToolRegistryReceiptRecoveryPreservesOriginalResultDTO(t *testing.T) {
	for _, tc := range []struct {
		name           string
		semanticDedupe bool
	}{
		{name: "new_insert"},
		{name: "semantic_dedupe", semanticDedupe: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			first := newToolRegistryGroupFixture(t, root)
			const opID = "tool-receipt-result-recovery"
			actionID := modulecore.NewActionID()
			entry := toolRegistryEntry("receipt_result_recovery")
			if tc.semanticDedupe {
				if err := first.owner.Register(context.Background(), entry); err != nil {
					t.Fatalf("preseed identical natural-key entry: %v", err)
				}
			}
			first.client.opID = opID
			first.handler.crashAfterCommitFor = opID
			_, err := NewToolRegistryClient(first.client).RegisterWithReceipt(context.Background(), entry, actionID, "agent:mio", strings.Repeat("d", 64))
			expectToolRegistryError(t, err, ErrorCodeOutcomeUnknown)
			originalReceipt, found, err := first.owner.FindActionReceipt(context.Background(), actionID)
			if err != nil || !found {
				t.Fatalf("committed action receipt=%+v found=%v err=%v", originalReceipt, found, err)
			}
			first.close()

			reopened := newToolRegistryGroupFixture(t, root)
			reopened.client.opID = opID
			registry := NewToolRegistryClient(reopened.client)
			result, err := registry.RegisterWithReceipt(context.Background(), entry, actionID, "agent:mio", strings.Repeat("d", 64))
			if err != nil {
				t.Fatalf("RegisterWithReceipt after reopen: %v", err)
			}
			receiptMatches := result.Receipt.ActionID == originalReceipt.ActionID && result.Receipt.ActorID == originalReceipt.ActorID &&
				result.Receipt.PayloadHash == originalReceipt.PayloadHash && result.Receipt.ToolName == originalReceipt.ToolName &&
				result.Receipt.CreatedAt.Equal(originalReceipt.CreatedAt)
			if result.RequestReplay || result.SemanticDedupe != tc.semanticDedupe || !receiptMatches {
				t.Fatalf("recovered typed result=%+v, want original receipt %+v and replay=false semantic_dedupe=%v", result, originalReceipt, tc.semanticDedupe)
			}
			entries, err := reopened.owner.ListForPlatform(context.Background(), "linux")
			if err != nil || len(entries) != 1 || entries[0].Name != entry.Name {
				t.Fatalf("stored entries=%+v err=%v, want exactly one row", entries, err)
			}
			changed := entry
			changed.Description = "changed payload"
			_, err = registry.RegisterWithReceipt(context.Background(), changed, actionID, "agent:mio", strings.Repeat("d", 64))
			expectToolRegistryError(t, err, ErrorCodeDuplicateConflict)
		})
	}
}

func TestToolRegistryReceiptRecoveryRejectsCorruptProofWithoutOwnerRerun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
		value string
	}{
		{name: "malformed_result", query: `UPDATE tool_registry_storagehost_receipts SET result_json = '{}' WHERE op_id = ?`},
		{name: "substituted_effect_proof", query: `UPDATE tool_registry_storagehost_receipts SET effect_sha256 = ? WHERE op_id = ?`, value: strings.Repeat("0", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			var firstOwner *toolRegistryCountingOwner
			first := newToolRegistryGroupFixtureWithOwner(t, root, func(store *toolregistry.SQLiteToolRegistryStore) ToolRegistryGroupOwner {
				firstOwner = &toolRegistryCountingOwner{SQLiteToolRegistryStore: store}
				return firstOwner
			})
			const opID = "tool-corrupt-proof-recovery"
			actionID := modulecore.NewActionID()
			entry := toolRegistryEntry("corrupt_proof_tool")
			first.client.opID = opID
			first.handler.crashAfterCommitFor = opID
			_, err := NewToolRegistryClient(first.client).RegisterWithReceipt(context.Background(), entry, actionID, "agent:mio", strings.Repeat("e", 64))
			expectToolRegistryError(t, err, ErrorCodeOutcomeUnknown)
			if firstOwner.storageHostRegisterCalls != 1 {
				t.Fatalf("first owner calls=%d, want exactly one", firstOwner.storageHostRegisterCalls)
			}
			first.close()

			db, err := sql.Open("sqlite", filepath.Join(root, "tools.db"))
			if err != nil {
				t.Fatalf("open isolated owner database for proof corruption: %v", err)
			}
			var result sql.Result
			if tc.value == "" {
				result, err = db.Exec(tc.query, opID)
			} else {
				result, err = db.Exec(tc.query, tc.value, opID)
			}
			if err != nil {
				_ = db.Close()
				t.Fatalf("corrupt isolated operation proof: %v", err)
			}
			if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
				_ = db.Close()
				t.Fatalf("corrupted proof rows=%d err=%v, want one", rows, rowsErr)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close isolated owner database: %v", err)
			}

			var reopenedOwner *toolRegistryCountingOwner
			reopened := newToolRegistryGroupFixtureWithOwner(t, root, func(store *toolregistry.SQLiteToolRegistryStore) ToolRegistryGroupOwner {
				reopenedOwner = &toolRegistryCountingOwner{SQLiteToolRegistryStore: store}
				return reopenedOwner
			})
			reopened.client.opID = opID
			_, err = NewToolRegistryClient(reopened.client).RegisterWithReceipt(context.Background(), entry, actionID, "agent:mio", strings.Repeat("e", 64))
			expectToolRegistryError(t, err, ErrorCodeOutcomeUnknown)
			if reopenedOwner.storageHostRegisterCalls != 0 {
				t.Fatalf("reopened owner calls=%d, want no rerun after corrupt proof", reopenedOwner.storageHostRegisterCalls)
			}
		})
	}
}

func TestToolRegistryLegacyReceiptWithoutExactResultProofStaysUnknown(t *testing.T) {
	root := t.TempDir()
	var firstOwner *toolRegistryCountingOwner
	first := newToolRegistryGroupFixtureWithOwner(t, root, func(store *toolregistry.SQLiteToolRegistryStore) ToolRegistryGroupOwner {
		firstOwner = &toolRegistryCountingOwner{SQLiteToolRegistryStore: store, failStorageHostBeforeWrite: NewError(ErrorCodeStoreUnavailable, "injected typed owner error before storage-host proof")}
		return firstOwner
	})
	const opID = "tool-legacy-receipt-without-result-proof"
	actionID := modulecore.NewActionID()
	entry := toolRegistryEntry("legacy_result_proof_tool")
	if _, err := first.owner.RegisterWithReceipt(context.Background(), entry, actionID, "agent:mio", strings.Repeat("f", 64)); err != nil {
		t.Fatalf("preseed legacy canonical action receipt: %v", err)
	}
	first.client.opID = opID
	_, err := NewToolRegistryClient(first.client).RegisterWithReceipt(context.Background(), entry, actionID, "agent:mio", strings.Repeat("f", 64))
	expectToolRegistryError(t, err, ErrorCodeOutcomeUnknown)
	if firstOwner.storageHostRegisterCalls != 1 {
		t.Fatalf("first owner calls=%d, want injected single attempt", firstOwner.storageHostRegisterCalls)
	}
	first.close()

	var reopenedOwner *toolRegistryCountingOwner
	reopened := newToolRegistryGroupFixtureWithOwner(t, root, func(store *toolregistry.SQLiteToolRegistryStore) ToolRegistryGroupOwner {
		reopenedOwner = &toolRegistryCountingOwner{SQLiteToolRegistryStore: store}
		return reopenedOwner
	})
	reopened.client.opID = opID
	_, err = NewToolRegistryClient(reopened.client).RegisterWithReceipt(context.Background(), entry, actionID, "agent:mio", strings.Repeat("f", 64))
	expectToolRegistryError(t, err, ErrorCodeOutcomeUnknown)
	if reopenedOwner.storageHostRegisterCalls != 0 {
		t.Fatalf("reopened owner calls=%d, want no rerun without exact result proof", reopenedOwner.storageHostRegisterCalls)
	}
}

func TestToolRegistryRejectsChangedOpPayloadAndMalformedCapability(t *testing.T) {
	fixture := newToolRegistryGroupFixture(t, t.TempDir())
	client := NewToolRegistryClient(fixture.client)
	ctx := context.Background()
	fixture.client.opID = "tool-payload-binding"
	entry := toolRegistryEntry("bound_tool")
	if err := client.Register(ctx, entry); err != nil {
		t.Fatalf("Register: %v", err)
	}
	entry.Description = "changed payload"
	expectToolRegistryError(t, client.Register(ctx, entry), ErrorCodeDuplicateConflict)
	fixture.client.opID = ""
	bad := toolRegistryEntry("bad_schema")
	bad.SchemaJSON = `[]`
	expectToolRegistryError(t, client.Register(ctx, bad), ErrorCodeSchemaRejected)
	bad = toolRegistryEntry("bad_platform")
	bad.Platforms = []string{"linux", "linux"}
	expectToolRegistryError(t, client.Register(ctx, bad), ErrorCodeSchemaRejected)
	bad = toolRegistryEntry("oversized_schema")
	bad.SchemaJSON = `{"type":"object","description":"` + strings.Repeat("x", 70<<10) + `"}`
	expectToolRegistryError(t, client.Register(ctx, bad), ErrorCodeSchemaRejected)
	unknownField := map[string]any{"entry": map[string]any{
		"Name": "unknown_field", "Description": "bounded", "SchemaJSON": `{}`, "Platforms": []string{"linux"},
		"Source": "builtin", "CreatedAt": time.Time{}, "CreatedBy": "builtin", "DatabasePath": "not-allowed",
	}}
	expectToolRegistryError(t, fixture.client.Call(ctx, GroupToolRegistry, "register", unknownField, nil), ErrorCodeSchemaRejected)
	var missing capability.ToolRegistry = client
	_, err := missing.Get(ctx, "no_such_tool")
	if !errors.Is(err, capability.ErrToolRegistryEntryNotFound) {
		t.Fatalf("missing Get error=%v, want ErrToolRegistryEntryNotFound", err)
	}
}

func TestToolRegistryOwnerReconciliationErrorStaysUnknown(t *testing.T) {
	root := t.TempDir()
	store, err := toolregistry.NewSQLiteToolRegistryStore(filepath.Join(root, "tools.db"))
	if err != nil {
		t.Fatalf("NewSQLiteToolRegistryStore: %v", err)
	}
	owner := &toolRegistryFaultOwner{SQLiteToolRegistryStore: store, getError: fmt.Errorf("injected read failure")}
	handler, err := NewHandler(HandlerConfig{Token: toolRegistryGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterToolRegistryGroup(handler, owner); err != nil {
		t.Fatalf("RegisterToolRegistryGroup: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(func() { server.Close(); _ = handler.Close(); _ = store.Close() })
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: toolRegistryGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	client.opID = "tool-owner-read-error"
	registry := NewToolRegistryClient(client)
	entry := toolRegistryEntry("owner_error")
	owner.registerError = errors.New("ambiguous owner response after commit")
	expectToolRegistryError(t, registry.Register(context.Background(), entry), ErrorCodeOutcomeUnknown)
	expectToolRegistryError(t, registry.Register(context.Background(), entry), ErrorCodeOutcomeUnknown)
}

func TestToolRegistryMismatchedOwnerReceiptStaysUnknown(t *testing.T) {
	root := t.TempDir()
	store, err := toolregistry.NewSQLiteToolRegistryStore(filepath.Join(root, "tools.db"))
	if err != nil {
		t.Fatalf("NewSQLiteToolRegistryStore: %v", err)
	}
	owner := &toolRegistryFaultOwner{SQLiteToolRegistryStore: store}
	handler, err := NewHandler(HandlerConfig{Token: toolRegistryGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterToolRegistryGroup(handler, owner); err != nil {
		t.Fatalf("RegisterToolRegistryGroup: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(func() { server.Close(); _ = handler.Close(); _ = store.Close() })
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: toolRegistryGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	client.opID = "tool-mismatched-receipt"
	entry := toolRegistryEntry("receipt_mismatch")
	actionID := modulecore.NewActionID()
	owner.receiptErrorAfterWrite = errors.New("ambiguous receipt response")
	registry := NewToolRegistryClient(client)
	_, err = registry.RegisterWithReceipt(context.Background(), entry, actionID, "agent:mio", strings.Repeat("c", 64))
	expectToolRegistryError(t, err, ErrorCodeOutcomeUnknown)
	owner.receiptOverride = func(receipt capability.ToolRegistryRequestReceipt, found bool) (capability.ToolRegistryRequestReceipt, bool) {
		if found {
			receipt.ActorID = "agent:other"
		}
		return receipt, found
	}
	_, err = registry.RegisterWithReceipt(context.Background(), entry, actionID, "agent:mio", strings.Repeat("c", 64))
	expectToolRegistryError(t, err, ErrorCodeOutcomeUnknown)
}

type toolRegistryFaultOwner struct {
	*toolregistry.SQLiteToolRegistryStore
	getError               error
	registerError          error
	receiptErrorAfterWrite error
	receiptOverride        func(capability.ToolRegistryRequestReceipt, bool) (capability.ToolRegistryRequestReceipt, bool)
}

func (o *toolRegistryFaultOwner) RegisterWithReceipt(ctx context.Context, entry capability.ToolEntry, actionID modulecore.ActionID, actorID, payloadHash string) (capability.ToolRegistryRegistrationResult, error) {
	result, err := o.SQLiteToolRegistryStore.RegisterWithReceipt(ctx, entry, actionID, actorID, payloadHash)
	if err == nil && o.receiptErrorAfterWrite != nil {
		return capability.ToolRegistryRegistrationResult{}, o.receiptErrorAfterWrite
	}
	return result, err
}

func (o *toolRegistryFaultOwner) RegisterWithReceiptForStorageHost(ctx context.Context, entry capability.ToolEntry, actionID modulecore.ActionID, actorID, payloadHash string, identity toolregistry.ToolRegistryStorageHostOperationIdentity) (capability.ToolRegistryRegistrationResult, error) {
	result, err := o.SQLiteToolRegistryStore.RegisterWithReceiptForStorageHost(ctx, entry, actionID, actorID, payloadHash, identity)
	if err == nil && o.receiptErrorAfterWrite != nil {
		return capability.ToolRegistryRegistrationResult{}, o.receiptErrorAfterWrite
	}
	return result, err
}

func (o *toolRegistryFaultOwner) FindActionReceipt(ctx context.Context, actionID modulecore.ActionID) (capability.ToolRegistryRequestReceipt, bool, error) {
	receipt, found, err := o.SQLiteToolRegistryStore.FindActionReceipt(ctx, actionID)
	if err == nil && o.receiptOverride != nil {
		receipt, found = o.receiptOverride(receipt, found)
	}
	return receipt, found, err
}

func (o *toolRegistryFaultOwner) FindStorageHostRegistrationResult(ctx context.Context, identity toolregistry.ToolRegistryStorageHostOperationIdentity) (capability.ToolRegistryRegistrationResult, bool, error) {
	result, found, err := o.SQLiteToolRegistryStore.FindStorageHostRegistrationResult(ctx, identity)
	if err == nil && found && o.receiptOverride != nil {
		result.Receipt, found = o.receiptOverride(result.Receipt, found)
	}
	return result, found, err
}

func (o *toolRegistryFaultOwner) Register(ctx context.Context, entry capability.ToolEntry) error {
	if err := o.SQLiteToolRegistryStore.Register(ctx, entry); err != nil {
		return err
	}
	return o.registerError
}

func (o *toolRegistryFaultOwner) Get(ctx context.Context, name string) (capability.ToolEntry, error) {
	if o.getError != nil {
		return capability.ToolEntry{}, o.getError
	}
	return o.SQLiteToolRegistryStore.Get(ctx, name)
}

type toolRegistryCountingOwner struct {
	*toolregistry.SQLiteToolRegistryStore
	storageHostRegisterCalls   int
	failStorageHostBeforeWrite error
}

func (o *toolRegistryCountingOwner) RegisterWithReceiptForStorageHost(ctx context.Context, entry capability.ToolEntry, actionID modulecore.ActionID, actorID, payloadHash string, identity toolregistry.ToolRegistryStorageHostOperationIdentity) (capability.ToolRegistryRegistrationResult, error) {
	o.storageHostRegisterCalls++
	if o.failStorageHostBeforeWrite != nil {
		return capability.ToolRegistryRegistrationResult{}, o.failStorageHostBeforeWrite
	}
	return o.SQLiteToolRegistryStore.RegisterWithReceiptForStorageHost(ctx, entry, actionID, actorID, payloadHash, identity)
}

type toolRegistryGroupFixture struct {
	root    string
	owner   *toolregistry.SQLiteToolRegistryStore
	handler *Handler
	server  *httptest.Server
	client  *Client
}

func newToolRegistryGroupFixture(t *testing.T, root string) *toolRegistryGroupFixture {
	return newToolRegistryGroupFixtureWithOwner(t, root, nil)
}

func newToolRegistryGroupFixtureWithOwner(t *testing.T, root string, ownerFactory func(*toolregistry.SQLiteToolRegistryStore) ToolRegistryGroupOwner) *toolRegistryGroupFixture {
	t.Helper()
	owner, err := toolregistry.NewSQLiteToolRegistryStore(filepath.Join(root, "tools.db"))
	if err != nil {
		t.Fatalf("NewSQLiteToolRegistryStore: %v", err)
	}
	var groupOwner ToolRegistryGroupOwner = owner
	if ownerFactory != nil {
		groupOwner = ownerFactory(owner)
	}
	handler, err := NewHandler(HandlerConfig{Token: toolRegistryGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterToolRegistryGroup(handler, groupOwner); err != nil {
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("RegisterToolRegistryGroup: %v", err)
	}
	server := httptest.NewServer(handler)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: toolRegistryGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("Handshake: %v", err)
	}
	fixture := &toolRegistryGroupFixture{root: root, owner: owner, handler: handler, server: server, client: client}
	t.Cleanup(fixture.close)
	return fixture
}

func (f *toolRegistryGroupFixture) close() {
	if f == nil {
		return
	}
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.handler != nil {
		_ = f.handler.Close()
		f.handler = nil
	}
	if f.owner != nil {
		_ = f.owner.Close()
		f.owner = nil
	}
}

func toolRegistryEntry(name string) capability.ToolEntry {
	return capability.ToolEntry{Name: name, Description: "bounded test tool", SchemaJSON: `{"type":"object","properties":{}}`, Platforms: []string{"linux"}, Source: capability.ToolSourceBuiltin, CreatedAt: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC), CreatedBy: "builtin"}
}

func expectToolRegistryError(t *testing.T, err error, code string) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) || got.Code != code {
		t.Fatalf("error=%v, want storagehost code %q", err, code)
	}
}
