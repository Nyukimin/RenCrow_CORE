package storagehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

const userMemoryGroupTestToken = "user-memory-group-unit-test-token"

type userMemoryGroupHost struct {
	store  *l1sqlite.L1SQLiteStore
	host   *Handler
	server *httptest.Server
	client *Client
	remote *UserMemoryStoreClient
}

func openUserMemoryGroupHost(t *testing.T, dbPath, journalPath string) *userMemoryGroupHost {
	t.Helper()
	store, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	host, err := NewHandler(HandlerConfig{Token: userMemoryGroupTestToken, JournalDir: journalPath})
	if err != nil {
		_ = store.Close()
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterUserMemoryGroup(host, store); err != nil {
		_ = host.Close()
		_ = store.Close()
		t.Fatalf("RegisterUserMemoryGroup: %v", err)
	}
	server := httptest.NewServer(host)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: userMemoryGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = host.Close()
		_ = store.Close()
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		server.Close()
		_ = host.Close()
		_ = store.Close()
		t.Fatalf("Handshake: %v", err)
	}
	return &userMemoryGroupHost{store: store, host: host, server: server, client: client, remote: NewUserMemoryStoreClient(client)}
}

func (h *userMemoryGroupHost) close() {
	if h == nil {
		return
	}
	if h.server != nil {
		h.server.Close()
		h.server = nil
	}
	if h.host != nil {
		_ = h.host.Close()
		h.host = nil
	}
	if h.store != nil {
		_ = h.store.Close()
		h.store = nil
	}
}

func TestUserMemoryGroupRecoversCreateAfterOwnerAndHandlerReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "l1.db")
	journalPath := filepath.Join(dir, "journal")
	first := openUserMemoryGroupHost(t, dbPath, journalPath)
	opID := "user-memory-create-reopen"
	first.client.opID = opID
	first.host.crashAfterCommitFor = opID
	input := domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Prefers concise explanations", State: domainmemory.MemoryStateCandidate, EvidenceEventIDs: []string{"event-1"}, Confidence: .8, Sensitivity: "normal", Scope: "all_personas", Source: "user_explicit"}
	if _, err := first.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("lost response error=%v, want outcome_unknown", err)
	}
	original, err := first.store.ListUserMemories(ctx, "ren", "", true, 10)
	if err != nil || len(original) != 1 {
		t.Fatalf("committed owner effect items=%d err=%v, want one", len(original), err)
	}
	first.close()

	second := openUserMemoryGroupHost(t, dbPath, journalPath)
	defer second.close()
	second.client.opID = opID
	replayed, err := second.remote.CreateUserMemory(ctx, input)
	if err != nil {
		t.Fatalf("retry after owner and handler reopen: %v", err)
	}
	wantJSON, _ := json.Marshal(original[0])
	gotJSON, _ := json.Marshal(replayed)
	if replayed == nil || !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("recovered result=%+v, want exact original %+v", replayed, original[0])
	}
	items, err := second.store.ListUserMemories(ctx, "ren", "", true, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("owner effects after retry=%d err=%v, want exactly one", len(items), err)
	}
	var auditCount int
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type='memory.user_created' AND namespace='user:ren'`).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("creation audit count=%d err=%v, want one", auditCount, err)
	}
}

func TestUserMemoryGroupRecoversMutationReceiptsAfterReopen(t *testing.T) {
	ctx := context.Background()
	for _, operation := range []string{"candidate", "state", "forget", "supersede"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			dbPath, journalPath := filepath.Join(dir, "l1.db"), filepath.Join(dir, "journal")
			first := openUserMemoryGroupHost(t, dbPath, journalPath)
			opID := "user-memory-" + operation + "-reopen"
			first.client.opID = opID
			first.host.crashAfterCommitFor = opID
			input := domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Stable result for " + operation, EvidenceEventIDs: []string{"event-1"}, Confidence: .8, Sensitivity: "normal", Scope: "all_personas"}
			var primaryID, secondaryID, requestID string
			if operation == "state" || operation == "forget" || operation == "supersede" {
				item, err := first.store.CreateUserMemory(ctx, input)
				if err != nil {
					t.Fatalf("seed memory: %v", err)
				}
				primaryID = item.ID
			}
			if operation == "supersede" {
				input.Statement = "Replacement memory"
				item, err := first.store.CreateUserMemory(ctx, input)
				if err != nil {
					t.Fatalf("seed replacement memory: %v", err)
				}
				secondaryID = item.ID
			}
			var firstResult any
			var firstErr error
			switch operation {
			case "candidate":
				requestID = "request-" + operation
				firstResult, _, firstErr = first.remote.CreateUserMemoryCandidateWithRequest(ctx, requestID, "shiro", input)
			case "state":
				firstResult, firstErr = first.remote.UpdateUserMemoryState(ctx, primaryID, domainmemory.MemoryStateConfirmed, "user explicitly confirmed")
			case "forget":
				firstResult, firstErr = first.remote.ForgetUserMemory(ctx, primaryID, "user requested forgetting")
			case "supersede":
				firstResult, firstErr = first.remote.SupersedeUserMemory(ctx, primaryID, secondaryID, "newer memory")
			}
			if userMemoryGroupErrorCode(firstErr) != ErrorCodeOutcomeUnknown {
				t.Fatalf("post-commit response loss result=%#v err=%v, want outcome_unknown", firstResult, firstErr)
			}
			var want any
			switch operation {
			case "candidate":
				items, err := first.store.ListUserMemories(ctx, "ren", "", true, 20)
				if err != nil || len(items) != 1 {
					t.Fatalf("candidate effect count=%d err=%v", len(items), err)
				}
				want = userMemoryCandidateResult{Memory: &items[0], IdempotentReplay: false}
			case "state", "forget", "supersede":
				item, found, err := first.store.FindUserMemoryByID(ctx, primaryID)
				if err != nil || !found {
					t.Fatalf("mutated memory lookup found=%v err=%v", found, err)
				}
				want = userMemoryItemResult{Memory: &item}
			}
			first.close()
			second := openUserMemoryGroupHost(t, dbPath, journalPath)
			defer second.close()
			second.client.opID = opID
			var got any
			switch operation {
			case "candidate":
				item, replay, err := second.remote.CreateUserMemoryCandidateWithRequest(ctx, requestID, "shiro", input)
				if err != nil {
					t.Fatalf("candidate retry: %v", err)
				}
				got = userMemoryCandidateResult{Memory: item, IdempotentReplay: replay}
			case "state":
				item, err := second.remote.UpdateUserMemoryState(ctx, primaryID, domainmemory.MemoryStateConfirmed, "user explicitly confirmed")
				if err != nil {
					t.Fatalf("state retry: %v", err)
				}
				got = userMemoryItemResult{Memory: item}
			case "forget":
				item, err := second.remote.ForgetUserMemory(ctx, primaryID, "user requested forgetting")
				if err != nil {
					t.Fatalf("forget retry: %v", err)
				}
				got = userMemoryItemResult{Memory: item}
			case "supersede":
				item, err := second.remote.SupersedeUserMemory(ctx, primaryID, secondaryID, "newer memory")
				if err != nil {
					t.Fatalf("supersede retry: %v", err)
				}
				got = userMemoryItemResult{Memory: item}
			}
			wantJSON, _ := json.Marshal(want)
			gotJSON, _ := json.Marshal(got)
			if !bytes.Equal(gotJSON, wantJSON) {
				t.Fatalf("recovered exact result=%s, want %s", gotJSON, wantJSON)
			}
			var audits int
			eventType := map[string]string{"candidate": "memory.user_created", "state": "memory.state_updated", "forget": "memory.user_forgotten", "supersede": "memory.user_superseded"}[operation]
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type = ?`, eventType).Scan(&audits); err != nil || audits != 1 {
				t.Fatalf("audit events=%d err=%v, want exactly one %s", audits, err, eventType)
			}
		})
	}
}

func TestUserMemoryGroupPostCommitTypedOwnerErrorReconcilesWithoutRerun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(dir, "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	owner := &userMemoryPostCommitErrorOwner{UserMemoryGroupRecoveryOwner: store}
	host, err := NewHandler(HandlerConfig{Token: userMemoryGroupTestToken, JournalDir: filepath.Join(dir, "journal")})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := RegisterUserMemoryGroup(host, owner); err != nil {
		_ = host.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(host)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: userMemoryGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = host.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	if err := client.Handshake(ctx); err != nil {
		server.Close()
		_ = host.Close()
		_ = store.Close()
		t.Fatal(err)
	}
	f := &userMemoryGroupHost{store: store, host: host, server: server, client: client, remote: NewUserMemoryStoreClient(client)}
	defer f.close()
	client.opID = "user-memory-post-commit-typed-error"
	input := domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Post-commit typed error", EvidenceEventIDs: []string{"event-1"}, Confidence: .7, Sensitivity: "normal", Scope: "all_personas"}
	if _, err := f.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("post-commit owner error=%v, want outcome_unknown", err)
	}
	replayed, err := f.remote.CreateUserMemory(ctx, input)
	if err != nil {
		t.Fatalf("receipt reconciliation: %v", err)
	}
	if owner.createCalls != 1 {
		t.Fatalf("owner execution count=%d, want no rerun", owner.createCalls)
	}
	items, err := store.ListUserMemories(ctx, "ren", "", true, 10)
	if err != nil || len(items) != 1 || replayed == nil {
		t.Fatalf("recovered=%+v effect count=%d err=%v", replayed, len(items), err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var audits int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.user_created'`).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("post-commit typed error audit count=%d err=%v", audits, err)
	}
}

func TestUserMemoryGroupCandidateReplayFlagSurvivesOwnerAndHandlerReopen(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, journalPath := filepath.Join(dir, "l1.db"), filepath.Join(dir, "journal")
	first := openUserMemoryGroupHost(t, dbPath, journalPath)
	input := domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Candidate replay flag", EvidenceEventIDs: []string{"event-1"}, Confidence: .8, Sensitivity: "normal", Scope: "all_personas"}
	first.client.opID = "user-memory-candidate-insert"
	created, replay, err := first.remote.CreateUserMemoryCandidateWithRequest(ctx, "candidate-replay-request", "shiro", input)
	if err != nil || created == nil || replay {
		t.Fatalf("first candidate=%+v replay=%v err=%v", created, replay, err)
	}
	const replayOpID = "user-memory-candidate-replay-after-reopen"
	first.client.opID = replayOpID
	first.host.crashAfterCommitFor = replayOpID
	if _, _, err := first.remote.CreateUserMemoryCandidateWithRequest(ctx, "candidate-replay-request", "shiro", input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("replay response loss=%v, want outcome_unknown", err)
	}
	first.close()
	second := openUserMemoryGroupHost(t, dbPath, journalPath)
	defer second.close()
	second.client.opID = replayOpID
	recovered, replay, err := second.remote.CreateUserMemoryCandidateWithRequest(ctx, "candidate-replay-request", "shiro", input)
	if err != nil || recovered == nil || !replay {
		t.Fatalf("recovered candidate=%+v replay=%v err=%v, want replay=true", recovered, replay, err)
	}
	createdJSON, _ := json.Marshal(created)
	recoveredJSON, _ := json.Marshal(recovered)
	if !bytes.Equal(createdJSON, recoveredJSON) {
		t.Fatalf("recovered memory=%s, want %s", recoveredJSON, createdJSON)
	}
	var candidateEvents, resultProofs, receipts int
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type='memory.user_created' AND json_extract(payload_json, '$.request_id')='candidate-replay-request'`).Scan(&candidateEvents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type='memory.user_storagehost_operation_result'`).Scan(&resultProofs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM user_memory_storagehost_operation_receipt`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if candidateEvents != 1 || resultProofs != 2 || receipts != 2 {
		t.Fatalf("candidate events=%d proofs=%d receipts=%d, want 1/2/2", candidateEvents, resultProofs, receipts)
	}
}

func TestUserMemoryGroupMissingReceiptRemainsNonrepeatableOutcomeUnknown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, journalPath := filepath.Join(dir, "l1.db"), filepath.Join(dir, "journal")
	first := openUserMemoryGroupHost(t, dbPath, journalPath)
	const opID = "user-memory-missing-receipt"
	first.client.opID = opID
	first.host.crashAfterCommitFor = opID
	input := domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Receipt deletion is uncertain", EvidenceEventIDs: []string{"event-1"}, Confidence: .7, Sensitivity: "normal", Scope: "all_personas"}
	if _, err := first.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("post-commit response loss=%v, want outcome_unknown", err)
	}
	first.close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM user_memory_storagehost_operation_receipt WHERE op_id = ?`, opID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	second := openUserMemoryGroupHost(t, dbPath, journalPath)
	defer second.close()
	second.client.opID = opID
	if _, err := second.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry without owner receipt=%v, want nonrepeatable outcome_unknown", err)
	}
	items, err := second.store.ListUserMemories(ctx, "ren", "", true, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("re-executed effects=%d err=%v, want one", len(items), err)
	}
}

func TestUserMemoryGroupLegacyReceiptWithoutResultEvidenceIsOutcomeUnknown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, journalPath := filepath.Join(dir, "l1.db"), filepath.Join(dir, "journal")
	first := openUserMemoryGroupHost(t, dbPath, journalPath)
	const opID = "user-memory-legacy-receipt"
	first.client.opID = opID
	first.host.crashAfterCommitFor = opID
	input := domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Legacy receipt is unproven", EvidenceEventIDs: []string{"event-1"}, Confidence: .7, Sensitivity: "normal", Scope: "all_personas"}
	if _, err := first.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("post-commit loss=%v", err)
	}
	first.close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE user_memory_storagehost_operation_receipt SET result_event_id = 'legacy-unbound' WHERE op_id = ?`, opID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	second := openUserMemoryGroupHost(t, dbPath, journalPath)
	defer second.close()
	second.client.opID = opID
	if _, err := second.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("legacy receipt retry=%v, want nonrepeatable outcome_unknown", err)
	}
	items, err := second.store.ListUserMemories(ctx, "ren", "", true, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("legacy receipt caused owner rerun: effects=%d err=%v", len(items), err)
	}
}

func TestUserMemoryGroupReceiptSubstitutionCannotChangeRecoveredCreateResult(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath, journalPath := filepath.Join(dir, "l1.db"), filepath.Join(dir, "journal")
	first := openUserMemoryGroupHost(t, dbPath, journalPath)
	const firstOpID, secondOpID = "user-memory-substitute-first", "user-memory-substitute-second"
	input := domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Same semantic request", EvidenceEventIDs: []string{"event-1"}, Confidence: .8, Sensitivity: "normal", Scope: "all_personas"}
	first.client.opID = firstOpID
	first.host.crashAfterCommitFor = firstOpID
	if _, err := first.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("first response loss=%v, want outcome_unknown", err)
	}
	originalItems, err := first.store.ListUserMemories(ctx, "ren", "", true, 10)
	if err != nil || len(originalItems) != 1 {
		t.Fatalf("first committed result count=%d err=%v", len(originalItems), err)
	}
	first.client.opID = secondOpID
	if _, err := first.remote.CreateUserMemory(ctx, input); err != nil {
		t.Fatalf("second equivalent create: %v", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var substituted string
	if err := db.QueryRowContext(ctx, `SELECT result_json FROM user_memory_storagehost_operation_receipt WHERE op_id = ?`, secondOpID).Scan(&substituted); err != nil {
		db.Close()
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(substituted))
	if _, err := db.ExecContext(ctx, `UPDATE user_memory_storagehost_operation_receipt SET result_json = ?, result_sha256 = ? WHERE op_id = ?`, substituted, hex.EncodeToString(hash[:]), firstOpID); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	first.close()
	second := openUserMemoryGroupHost(t, dbPath, journalPath)
	defer second.close()
	second.client.opID = firstOpID
	if _, err := second.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("reconcile substituted receipt=%v, want nonrepeatable outcome_unknown", err)
	}
	items, err := second.store.ListUserMemories(ctx, "ren", "", true, 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("substituted receipt caused another owner execution: effects=%d err=%v", len(items), err)
	}
}

type userMemoryPostCommitErrorOwner struct {
	UserMemoryGroupRecoveryOwner
	createCalls int
}

func (o *userMemoryPostCommitErrorOwner) CreateUserMemoryForStorageHostOperation(ctx context.Context, identity l1sqlite.UserMemoryStorageHostOperationIdentity, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error) {
	o.createCalls++
	if _, err := o.UserMemoryGroupRecoveryOwner.CreateUserMemoryForStorageHostOperation(ctx, identity, input); err != nil {
		return nil, err
	}
	return nil, NewError(ErrorCodeStoreUnavailable, "injected typed error after owner commit")
}

func userMemoryGroupErrorCode(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

func TestUserMemoryGroupExposesSevenTypedOperations(t *testing.T) {
	f := openUserMemoryGroupHost(t, filepath.Join(t.TempDir(), "l1.db"), filepath.Join(t.TempDir(), "journal"))
	defer f.close()
	info, err := f.client.Contract(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{userMemoryOpCreate: true, userMemoryOpList: false, userMemoryOpFind: false, userMemoryOpCandidate: true, userMemoryOpState: true, userMemoryOpForget: true, userMemoryOpSupersede: true}
	got := map[string]bool{}
	for _, spec := range info.Operations {
		if spec.Group == GroupUserMemory {
			got[spec.Op] = spec.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("user memory operation manifest=%v, want %v", got, want)
	}
}

func TestUserMemoryGroupReadPayloadRejectsUnknownField(t *testing.T) {
	f := openUserMemoryGroupHost(t, filepath.Join(t.TempDir(), "l1.db"), filepath.Join(t.TempDir(), "journal"))
	defer f.close()
	var result userMemoryListResult
	err := f.client.Call(context.Background(), GroupUserMemory, userMemoryOpList, map[string]any{"user_id": "ren", "state": "", "include_inactive": false, "limit": 10, "sql": "SELECT 1"}, &result)
	if userMemoryGroupErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unknown field error=%v, want schema_rejected", err)
	}
}

func TestUserMemoryGroupOwnerReceiptResultCorruptionIsOutcomeUnknown(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "l1.db")
	journalPath := filepath.Join(dir, "journal")
	opID := "user-memory-corrupt-result"
	first := openUserMemoryGroupHost(t, dbPath, journalPath)
	first.client.opID = opID
	first.host.crashAfterCommitFor = opID
	input := domainmemory.CreateUserMemoryInput{UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "Corruption fixture", State: domainmemory.MemoryStateCandidate, Sensitivity: "normal", Scope: "all_personas"}
	if _, err := first.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("lost response error=%v, want outcome_unknown", err)
	}
	first.close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE user_memory_storagehost_operation_receipt SET result_json='' WHERE op_id=?`, opID); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	second := openUserMemoryGroupHost(t, dbPath, journalPath)
	defer second.close()
	second.client.opID = opID
	if _, err := second.remote.CreateUserMemory(ctx, input); userMemoryGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry with missing owner result error=%v, want outcome_unknown", err)
	}
	items, err := second.store.ListUserMemories(ctx, "ren", "", true, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("corrupt-receipt retry effects=%d err=%v, want one", len(items), err)
	}
}
