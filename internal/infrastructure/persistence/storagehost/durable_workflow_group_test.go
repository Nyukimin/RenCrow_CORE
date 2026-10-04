package storagehost

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	appdurable "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	domaindurable "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	persistdurable "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/durablestore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const durableWorkflowGroupTestToken = "durable-workflow-group-test-token"

func TestDurableStoreWorkflowGroupRegistersOnlyStoreOperations(t *testing.T) {
	fixture := newDurableWorkflowGroupFixture(t, t.TempDir())
	contract, err := fixture.rpc.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	want := map[string]bool{
		"find_by_dedupe_key":     false,
		"find_by_action_id":      false,
		"find_by_requirement_id": false,
		"save_with_receipt":      true,
	}
	got := map[string]bool{}
	for _, operation := range contract.Operations {
		if operation.Group == GroupDurableStoreWorkflow {
			got[operation.Op] = operation.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("durable workflow operations=%v, want exactly %v", got, want)
	}
}

func TestDurableStoreWorkflowClientRoundTripsTypedResultsAndDistinctCallerScopes(t *testing.T) {
	fixture := newDurableWorkflowGroupFixture(t, t.TempDir())
	ctx := context.Background()
	var store appdurable.Store = fixture.store
	authenticatedUserID := "AuthenticatedUser-CaseSensitive"
	channel, chatID := "Channel", "ChatID"
	tests := []struct {
		name             string
		suffix           string
		actionID         modulecore.ActionID
		semanticActionID modulecore.ActionID
		userScope        string
	}{
		{name: "authenticated user scope", suffix: "user", actionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000031"), semanticActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000041"), userScope: authenticatedUserID},
		{name: "chat channel scope", suffix: "chat", actionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000032"), semanticActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000042"), userScope: channel + ":" + chatID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, receipt := durableWorkflowRPCFixture(test.suffix, test.actionID, test.userScope)
			if err := store.SaveWithReceipt(ctx, &result, receipt); err != nil {
				t.Fatalf("SaveWithReceipt: %v", err)
			}
			byDedupe, err := store.FindByDedupeKey(ctx, result.Requirement.DedupeKey)
			if err != nil || !reflect.DeepEqual(byDedupe, &result) {
				t.Fatalf("FindByDedupeKey=%+v err=%v, want exact %+v", byDedupe, err, result)
			}
			byRequirement, err := store.FindByRequirementID(ctx, result.Requirement.RequirementID)
			if err != nil || !reflect.DeepEqual(byRequirement, &result) {
				t.Fatalf("FindByRequirementID=%+v err=%v, want exact %+v", byRequirement, err, result)
			}
			byAction, err := store.FindByActionID(ctx, receipt.ActionID)
			if err != nil || !reflect.DeepEqual(byAction, &receipt) {
				t.Fatalf("FindByActionID=%+v err=%v, want exact %+v", byAction, err, receipt)
			}
			if byAction.UserScope != test.userScope || byRequirement.Requirement.UserScope != test.userScope {
				t.Fatalf("scope was normalized: receipt=%q workflow=%q want=%q", byAction.UserScope, byRequirement.Requirement.UserScope, test.userScope)
			}
			semanticRequirement := result.Requirement
			semanticRequirement.ActionID = test.semanticActionID
			semanticReceipt := receipt
			semanticReceipt.ActionID = test.semanticActionID
			semanticReceipt.PayloadHash = domaindurable.HashStorageRequirement(semanticRequirement)
			semanticReceipt.CreatedAt = semanticReceipt.CreatedAt.Add(time.Second)
			if err := store.SaveWithReceipt(ctx, nil, semanticReceipt); err != nil {
				t.Fatalf("SaveWithReceipt receipt-only dedupe: %v", err)
			}
			gotSemantic, err := store.FindByActionID(ctx, semanticReceipt.ActionID)
			if err != nil || !reflect.DeepEqual(gotSemantic, &semanticReceipt) {
				t.Fatalf("semantic receipt=%+v err=%v, want %+v", gotSemantic, err, semanticReceipt)
			}
		})
	}
	if got, err := store.FindByRequirementID(ctx, "sr-missing"); err != nil || got != nil {
		t.Fatalf("missing requirement=%+v err=%v, want nil", got, err)
	}
}

func TestDurableStoreWorkflowClientPreservesRequestConflict(t *testing.T) {
	fixture := newDurableWorkflowGroupFixture(t, t.TempDir())
	ctx := context.Background()
	result, receipt := durableWorkflowRPCFixture("conflict", modulecore.ActionID("act_00000000-0000-5000-8000-000000000033"), "User-One")
	if err := fixture.store.SaveWithReceipt(ctx, &result, receipt); err != nil {
		t.Fatalf("initial SaveWithReceipt: %v", err)
	}
	changed := result
	changed.Requirement.UserScope = "Channel:ChatID"
	changed.Requirement.DedupeKey = "dedupe-conflict-changed"
	changed.Requirement.RequirementID = "sr-conflict-changed"
	changedReceipt := receipt
	changedReceipt.UserScope = changed.Requirement.UserScope
	changedReceipt.RequirementID = changed.Requirement.RequirementID
	changedReceipt.PayloadHash = domaindurable.HashStorageRequirement(changed.Requirement)
	if err := fixture.store.SaveWithReceipt(ctx, &changed, changedReceipt); !errors.Is(err, appdurable.ErrRequestConflict) {
		t.Fatalf("changed action error=%v, want ErrRequestConflict", err)
	}
	if got, err := fixture.store.FindByRequirementID(ctx, changed.Requirement.RequirementID); err != nil || got != nil {
		t.Fatalf("conflicting write persisted=%+v err=%v", got, err)
	}
}

func TestDurableStoreWorkflowGroupRejectsUnboundedAndUnknownPayloads(t *testing.T) {
	fixture := newDurableWorkflowGroupFixture(t, t.TempDir())
	var result any
	err := fixture.rpc.Call(context.Background(), GroupDurableStoreWorkflow, "find_by_requirement_id", map[string]any{"requirement_id": "sr-1", "sql": "SELECT *"}, &result)
	expectDurableWorkflowStorageError(t, err, ErrorCodeSchemaRejected)
	err = fixture.rpc.Call(context.Background(), GroupDurableStoreWorkflow, "find_by_dedupe_key", map[string]any{"dedupe_key": string(make([]byte, (2<<20)+1))}, &result)
	expectDurableWorkflowStorageError(t, err, ErrorCodeSchemaRejected)
}

func TestDurableStoreWorkflowOwnerRecoversSQLiteCommitGapAfterRestart(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "workflow.db")
	journalDir := filepath.Join(root, "journal")
	h1, srv1, owner1, rpc1, client1 := openDurableWorkflowRecoveryHost(t, journalDir, databasePath)
	result, receipt := durableWorkflowRPCFixture("recover", modulecore.ActionID("act_00000000-0000-5000-8000-000000000034"), "AuthenticatedUser")
	const opID = "durable-workflow-commit-gap"
	rpc1.opID = opID
	originalJournalGeneration := rpc1.Generation()
	h1.crashAfterCommitFor = opID
	err := client1.SaveWithReceipt(context.Background(), &result, receipt)
	expectDurableWorkflowStorageError(t, err, ErrorCodeOutcomeUnknown)
	closeDurableWorkflowRecoveryHost(t, h1, srv1, owner1)

	h2, srv2, owner2, rpc2, client2 := openDurableWorkflowRecoveryHost(t, journalDir, databasePath)
	defer closeDurableWorkflowRecoveryHost(t, h2, srv2, owner2)
	if rpc2.Generation() <= rpc1.Generation() {
		t.Fatalf("reopened generation=%d, want greater than %d", rpc2.Generation(), rpc1.Generation())
	}
	rpc2.opID = opID
	if err := client2.SaveWithReceipt(context.Background(), &result, receipt); err != nil {
		t.Fatalf("recover committed save: %v", err)
	}
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open recovered owner receipt: %v", err)
	}
	var storedWriterGeneration int64
	if err := db.QueryRow(`SELECT writer_generation FROM durable_store_workflow_storagehost_receipt WHERE op_id = ?`, opID).Scan(&storedWriterGeneration); err != nil {
		_ = db.Close()
		t.Fatalf("read recovered writer generation: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close recovered owner receipt: %v", err)
	}
	if storedWriterGeneration != originalJournalGeneration || storedWriterGeneration == rpc2.Generation() {
		t.Fatalf("stored writer generation=%d, want original journal generation %d and not current request generation %d", storedWriterGeneration, originalJournalGeneration, rpc2.Generation())
	}
	assertDurableWorkflowRPCRowCount(t, databasePath, "durable_store_workflow", 1)
	assertDurableWorkflowRPCRowCount(t, databasePath, "durable_store_workflow_receipt", 1)
	assertDurableWorkflowRPCRowCount(t, databasePath, "durable_store_workflow_storagehost_receipt", 1)
}

func TestDurableStoreWorkflowOwnerRejectsChangedRecoveryPayload(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "workflow.db")
	journalDir := filepath.Join(root, "journal")
	h1, srv1, owner1, rpc1, client1 := openDurableWorkflowRecoveryHost(t, journalDir, databasePath)
	result, receipt := durableWorkflowRPCFixture("changed-recovery", modulecore.ActionID("act_00000000-0000-5000-8000-000000000035"), "AuthenticatedUser")
	const opID = "durable-workflow-changed-recovery"
	rpc1.opID = opID
	h1.crashAfterCommitFor = opID
	expectDurableWorkflowStorageError(t, client1.SaveWithReceipt(context.Background(), &result, receipt), ErrorCodeOutcomeUnknown)
	closeDurableWorkflowRecoveryHost(t, h1, srv1, owner1)

	h2, srv2, owner2, rpc2, client2 := openDurableWorkflowRecoveryHost(t, journalDir, databasePath)
	defer closeDurableWorkflowRecoveryHost(t, h2, srv2, owner2)
	changed := result
	changed.Requirement.UserScope = "Channel:ChatID"
	changed.Requirement.DedupeKey += "-changed"
	changed.Requirement.RequirementID += "-changed"
	changedReceipt := receipt
	changedReceipt.UserScope = changed.Requirement.UserScope
	changedReceipt.RequirementID = changed.Requirement.RequirementID
	changedReceipt.PayloadHash = domaindurable.HashStorageRequirement(changed.Requirement)
	rpc2.opID = opID
	if err := client2.SaveWithReceipt(context.Background(), &changed, changedReceipt); !errors.Is(err, appdurable.ErrRequestConflict) {
		t.Fatalf("changed recovery error=%v, want ErrRequestConflict", err)
	}
	assertDurableWorkflowRPCRowCount(t, databasePath, "durable_store_workflow", 1)
}

func TestDurableStoreWorkflowOwnerCorruptOrSubstitutedReceiptStaysOutcomeUnknown(t *testing.T) {
	tests := []struct {
		name       string
		resultJSON string
		rehash     bool
	}{
		{name: "malformed result", resultJSON: "{"},
		{name: "valid non-null result substitution", resultJSON: `{"substituted":true}`, rehash: true},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			databasePath := filepath.Join(root, "workflow.db")
			journalDir := filepath.Join(root, "journal")
			h1, srv1, owner1, rpc1, client1 := openDurableWorkflowRecoveryHost(t, journalDir, databasePath)
			actionID := modulecore.ActionID([]string{
				"act_00000000-0000-5000-8000-000000000036",
				"act_00000000-0000-5000-8000-000000000037",
			}[index])
			result, receipt := durableWorkflowRPCFixture(fmt.Sprintf("receipt-integrity-%d", index), actionID, "AuthenticatedUser")
			opID := fmt.Sprintf("durable-workflow-receipt-integrity-%d", index)
			rpc1.opID = opID
			h1.crashAfterCommitFor = opID
			expectDurableWorkflowStorageError(t, client1.SaveWithReceipt(context.Background(), &result, receipt), ErrorCodeOutcomeUnknown)
			closeDurableWorkflowRecoveryHost(t, h1, srv1, owner1)

			db, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Fatalf("open receipt database: %v", err)
			}
			if test.rehash {
				hash := sha256.Sum256([]byte(test.resultJSON))
				_, err = db.Exec(`UPDATE durable_store_workflow_storagehost_receipt SET result_json = ?, result_sha256 = ? WHERE op_id = ?`, test.resultJSON, hex.EncodeToString(hash[:]), opID)
			} else {
				_, err = db.Exec(`UPDATE durable_store_workflow_storagehost_receipt SET result_json = ? WHERE op_id = ?`, test.resultJSON, opID)
			}
			if err != nil {
				_ = db.Close()
				t.Fatalf("mutate owner receipt: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close receipt database: %v", err)
			}

			h2, srv2, owner2, rpc2, client2 := openDurableWorkflowRecoveryHost(t, journalDir, databasePath)
			defer closeDurableWorkflowRecoveryHost(t, h2, srv2, owner2)
			rpc2.opID = opID
			expectDurableWorkflowStorageError(t, client2.SaveWithReceipt(context.Background(), &result, receipt), ErrorCodeOutcomeUnknown)
			assertDurableWorkflowRPCRowCount(t, databasePath, "durable_store_workflow", 1)
		})
	}
}

func TestDurableStoreWorkflowOwnerCanonicalEffectCorruptionStaysOutcomeUnknown(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, domaindurable.WorkflowResult, domaindurable.RequestReceipt)
	}{
		{
			name: "missing action receipt",
			mutate: func(t *testing.T, db *sql.DB, _ domaindurable.WorkflowResult, receipt domaindurable.RequestReceipt) {
				t.Helper()
				if _, err := db.Exec(`DELETE FROM durable_store_workflow_receipt WHERE action_id = ?`, string(receipt.ActionID)); err != nil {
					t.Fatalf("delete action receipt: %v", err)
				}
			},
		},
		{
			name: "missing workflow row",
			mutate: func(t *testing.T, db *sql.DB, result domaindurable.WorkflowResult, _ domaindurable.RequestReceipt) {
				t.Helper()
				if _, err := db.Exec(`DELETE FROM durable_store_workflow WHERE requirement_id = ?`, result.Requirement.RequirementID); err != nil {
					t.Fatalf("delete workflow row: %v", err)
				}
			},
		},
		{
			name: "valid workflow payload substitution",
			mutate: func(t *testing.T, db *sql.DB, result domaindurable.WorkflowResult, _ domaindurable.RequestReceipt) {
				t.Helper()
				var payload string
				if err := db.QueryRow(`SELECT payload FROM durable_store_workflow WHERE requirement_id = ?`, result.Requirement.RequirementID).Scan(&payload); err != nil {
					t.Fatalf("read workflow payload: %v", err)
				}
				substituted := strings.Replace(payload, `"reason":"existing owner"`, `"reason":"substituted owner"`, 1)
				if substituted == payload {
					t.Fatal("workflow substitution fixture did not change payload")
				}
				if _, err := db.Exec(`UPDATE durable_store_workflow SET payload = ? WHERE requirement_id = ?`, substituted, result.Requirement.RequirementID); err != nil {
					t.Fatalf("substitute workflow payload: %v", err)
				}
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			databasePath := filepath.Join(root, "workflow.db")
			journalDir := filepath.Join(root, "journal")
			h1, srv1, owner1, rpc1, client1 := openDurableWorkflowRecoveryHost(t, journalDir, databasePath)
			actionID := modulecore.ActionID([]string{
				"act_00000000-0000-5000-8000-000000000051",
				"act_00000000-0000-5000-8000-000000000052",
				"act_00000000-0000-5000-8000-000000000053",
			}[index])
			result, receipt := durableWorkflowRPCFixture(fmt.Sprintf("canonical-effect-%d", index), actionID, "AuthenticatedUser")
			opID := fmt.Sprintf("durable-workflow-canonical-effect-%d", index)
			rpc1.opID = opID
			h1.crashAfterCommitFor = opID
			expectDurableWorkflowStorageError(t, client1.SaveWithReceipt(context.Background(), &result, receipt), ErrorCodeOutcomeUnknown)
			closeDurableWorkflowRecoveryHost(t, h1, srv1, owner1)

			db, err := sql.Open("sqlite", databasePath)
			if err != nil {
				t.Fatalf("open canonical owner database: %v", err)
			}
			test.mutate(t, db, result, receipt)
			if err := db.Close(); err != nil {
				t.Fatalf("close canonical owner database: %v", err)
			}

			h2, srv2, owner2, rpc2, client2 := openDurableWorkflowRecoveryHost(t, journalDir, databasePath)
			defer closeDurableWorkflowRecoveryHost(t, h2, srv2, owner2)
			rpc2.opID = opID
			expectDurableWorkflowStorageError(t, client2.SaveWithReceipt(context.Background(), &result, receipt), ErrorCodeOutcomeUnknown)
		})
	}
}

type durableWorkflowGroupFixture struct {
	owner *persistdurable.SQLiteStore
	rpc   *Client
	store *DurableStoreWorkflowClient
}

func newDurableWorkflowGroupFixture(t *testing.T, root string) durableWorkflowGroupFixture {
	t.Helper()
	owner, err := persistdurable.NewSQLiteStore(filepath.Join(root, "workflow.db"))
	if err != nil {
		t.Fatalf("open workflow owner: %v", err)
	}
	handler, err := NewHandler(HandlerConfig{Token: durableWorkflowGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		_ = owner.Close()
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterDurableStoreWorkflowGroup(handler, owner); err != nil {
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("RegisterDurableStoreWorkflowGroup: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(func() {
		server.Close()
		_ = handler.Close()
		_ = owner.Close()
	})
	rpc, err := NewClient(ClientConfig{Endpoint: server.URL, Token: durableWorkflowGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := rpc.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	return durableWorkflowGroupFixture{owner: owner, rpc: rpc, store: NewDurableStoreWorkflowClient(rpc)}
}

func openDurableWorkflowRecoveryHost(t *testing.T, journalDir, databasePath string) (*Handler, *httptest.Server, *persistdurable.SQLiteStore, *Client, *DurableStoreWorkflowClient) {
	t.Helper()
	owner, err := persistdurable.NewSQLiteStore(databasePath)
	if err != nil {
		t.Fatalf("open workflow owner: %v", err)
	}
	handler, err := NewHandler(HandlerConfig{Token: durableWorkflowGroupTestToken, JournalDir: journalDir})
	if err != nil {
		_ = owner.Close()
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterDurableStoreWorkflowGroup(handler, owner); err != nil {
		_ = handler.Close()
		_ = owner.Close()
		t.Fatalf("RegisterDurableStoreWorkflowGroup: %v", err)
	}
	server := httptest.NewServer(handler)
	rpc, err := NewClient(ClientConfig{Endpoint: server.URL, Token: durableWorkflowGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		closeDurableWorkflowRecoveryHost(t, handler, server, owner)
		t.Fatalf("NewClient: %v", err)
	}
	if err := rpc.Handshake(context.Background()); err != nil {
		closeDurableWorkflowRecoveryHost(t, handler, server, owner)
		t.Fatalf("Handshake: %v", err)
	}
	return handler, server, owner, rpc, NewDurableStoreWorkflowClient(rpc)
}

func closeDurableWorkflowRecoveryHost(t *testing.T, handler *Handler, server *httptest.Server, owner *persistdurable.SQLiteStore) {
	t.Helper()
	server.Close()
	if err := handler.Close(); err != nil {
		t.Fatalf("close storage host: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatalf("close workflow owner: %v", err)
	}
}

func durableWorkflowRPCFixture(suffix string, actionID modulecore.ActionID, scope string) (domaindurable.WorkflowResult, domaindurable.RequestReceipt) {
	now := time.Date(2026, 10, 3, 11, 12, 13, 14, time.UTC)
	result := domaindurable.WorkflowResult{
		Status: domaindurable.StatusCompleted, Lifecycle: domaindurable.LifecycleValidated,
		Requirement: domaindurable.StorageRequirement{
			RequirementID: "sr-" + suffix, DedupeKey: "dedupe-" + suffix, ActionID: actionID,
			TraceID: "trace-" + suffix, RequestedBy: "shiro", UserScope: scope,
			RequestedOutcome: domaindurable.OutcomeAssess, FactsToStore: []string{"x_bookmark"},
			SourceSystems: []string{"chat"}, ReadPatterns: []string{"lookup"}, WritePatterns: []string{"append"},
			OwnerModule: "RenCrow_CORE", Acceptance: []string{"receipt survives restart"},
		},
		Classification: domaindurable.Classification{Class: domaindurable.ClassExistingStore, OwnerModule: "RenCrow_CORE", Status: domaindurable.StatusCompleted, Reason: "existing owner"},
		Evidence:       domaindurable.ActivationEvidence{MigrationPassed: true},
		Reason:         "existing owner", EvidenceRefs: []string{"receipt:" + suffix}, CreatedAt: now, UpdatedAt: now,
	}
	receipt := domaindurable.RequestReceipt{
		ActionID: actionID, UserScope: scope, PayloadHash: domaindurable.HashStorageRequirement(result.Requirement),
		RequirementID: result.Requirement.RequirementID, CreatedAt: now,
	}
	return result, receipt
}

func expectDurableWorkflowStorageError(t *testing.T, err error, code string) {
	t.Helper()
	var wire *Error
	if !errors.As(err, &wire) || wire.Code != code {
		t.Fatalf("error=%v, want storagehost code %q", err, code)
	}
}

func assertDurableWorkflowRPCRowCount(t *testing.T, databasePath, table string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open database for %s count: %v", table, err)
	}
	defer db.Close()
	var got int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s rows=%d, want %d", table, got, want)
	}
}
