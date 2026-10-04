package storagehost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	persistverification "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/verification"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const verificationReportGroupTestToken = "verification-report-group-test-token"

func TestVerificationReportGroupHasExactCanonicalOperationsAndTypedReads(t *testing.T) {
	root := t.TempDir()
	handler, server, rpc, store, _ := openVerificationReportHost(t, root, filepath.Join(root, "reports.jsonl"))
	defer closeVerificationReportHost(t, handler, server)
	contract, err := rpc.Contract(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"save": true, "list_recent": false, "get_by_task_id": false, "summary": false}
	got := map[string]bool{}
	for _, operation := range contract.Operations {
		if operation.Group == GroupVerificationReport {
			got[operation.Op] = operation.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations=%v want=%v", got, want)
	}

	report := verificationReportRPCFixture(t, modulecore.NewTaskID(), domainverification.StatusConflict, time.Now().UTC())
	if err := store.Save(context.Background(), report); err != nil {
		t.Fatalf("Save: %v", err)
	}
	list, err := store.ListRecent(context.Background(), 1)
	if err != nil || len(list) != 1 || !reflect.DeepEqual(list[0], report) {
		t.Fatalf("ListRecent=%+v err=%v", list, err)
	}
	gotReport, err := store.GetByTaskID(context.Background(), report.TaskID)
	if err != nil || !reflect.DeepEqual(gotReport, report) {
		t.Fatalf("GetByTaskID=%+v err=%v", gotReport, err)
	}
	summary, err := store.Summary(context.Background())
	if err != nil || summary["status"][string(domainverification.StatusConflict)] != 1 || summary["trigger_level"][string(report.TriggerLevel)] != 1 {
		t.Fatalf("Summary=%+v err=%v", summary, err)
	}
	if _, err := store.GetByTaskID(context.Background(), modulecore.NewTaskID()); !errors.Is(err, persistverification.ErrVerificationReportNotFound) {
		t.Fatalf("missing report err=%v", err)
	}
}

func TestVerificationReportGroupOwnerReopenRecoversExactSaveWithoutDuplicate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "reports.jsonl")
	h1, server1, rpc1, store1, _ := openVerificationReportHost(t, root, path)
	report := verificationReportRPCFixture(t, modulecore.NewTaskID(), domainverification.StatusVerified, time.Now().UTC())
	const opID = "verification-report-reopen-op"
	originalGeneration := rpc1.Generation()
	rpc1.opID = opID
	h1.crashAfterCommitFor = opID
	expectVerificationReportWireError(t, store1.Save(context.Background(), report), ErrorCodeOutcomeUnknown)
	closeVerificationReportHost(t, h1, server1)

	h2, server2, rpc2, store2, _ := openVerificationReportHost(t, root, path)
	defer closeVerificationReportHost(t, h2, server2)
	if rpc2.Generation() <= originalGeneration {
		t.Fatalf("reopened generation=%d want > %d", rpc2.Generation(), originalGeneration)
	}
	rpc2.opID = opID
	if err := store2.Save(context.Background(), report); err != nil {
		t.Fatalf("recover Save: %v", err)
	}
	rows := readVerificationReportLines(t, path)
	if len(rows) != 1 {
		t.Fatalf("report rows=%d want 1", len(rows))
	}
	proofLines := readVerificationReportLines(t, path+".storagehost-operations.jsonl")
	var begin struct {
		Kind     string `json:"kind"`
		Identity struct {
			WriterGeneration int64 `json:"WriterGeneration"`
		} `json:"identity"`
	}
	if err := json.Unmarshal([]byte(proofLines[0]), &begin); err != nil {
		t.Fatal(err)
	}
	// StorageHostOperationIdentity uses default field names in the owner proof.
	if begin.Identity.WriterGeneration != originalGeneration {
		t.Fatalf("proof writer generation=%d want original %d", begin.Identity.WriterGeneration, originalGeneration)
	}

	changed := verificationReportRPCFixture(t, modulecore.NewTaskID(), domainverification.StatusUnsupported, report.CreatedAt.Add(time.Second))
	rpc2.opID = opID
	expectVerificationReportWireError(t, store2.Save(context.Background(), changed), ErrorCodeDuplicateConflict)
	if got := len(readVerificationReportLines(t, path)); got != 1 {
		t.Fatalf("changed replay appended rows=%d", got)
	}
}

func TestVerificationReportGroupCorruptOwnerRowsFailClosedForEveryRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "reports.jsonl")
	handler, server, _, store, _ := openVerificationReportHost(t, root, path)
	defer closeVerificationReportHost(t, handler, server)
	if err := os.WriteFile(path, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := store.ListRecent(context.Background(), 10)
	expectVerificationReportWireError(t, err, ErrorCodeStoreUnavailable)
	_, err = store.GetByTaskID(context.Background(), modulecore.NewTaskID())
	expectVerificationReportWireError(t, err, ErrorCodeStoreUnavailable)
	_, err = store.Summary(context.Background())
	expectVerificationReportWireError(t, err, ErrorCodeStoreUnavailable)
}

func TestVerificationReportGroupMalformedOrSubstitutedOwnerProofStaysUnknown(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "malformed", mutate: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("{\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "legacy", mutate: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte(`{"version":0}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "substituted result", mutate: func(t *testing.T, path string) {
			lines := readVerificationReportLines(t, path)
			var commit map[string]any
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &commit); err != nil {
				t.Fatal(err)
			}
			commit["result_json"] = `{"substituted":true}`
			hash := sha256.Sum256([]byte(commit["result_json"].(string)))
			commit["result_sha256"] = hex.EncodeToString(hash[:])
			encoded, err := json.Marshal(commit)
			if err != nil {
				t.Fatal(err)
			}
			lines[len(lines)-1] = string(encoded)
			if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "reports.jsonl")
			h1, server1, rpc1, store1, _ := openVerificationReportHost(t, root, path)
			report := verificationReportRPCFixture(t, modulecore.NewTaskID(), domainverification.StatusVerified, time.Now().UTC())
			opID := "verification-report-proof-" + strings.ReplaceAll(test.name, " ", "-")
			rpc1.opID = opID
			h1.crashAfterCommitFor = opID
			expectVerificationReportWireError(t, store1.Save(context.Background(), report), ErrorCodeOutcomeUnknown)
			closeVerificationReportHost(t, h1, server1)
			test.mutate(t, path+".storagehost-operations.jsonl")

			h2, server2, rpc2, store2, _ := openVerificationReportHost(t, root, path)
			defer closeVerificationReportHost(t, h2, server2)
			rpc2.opID = opID
			expectVerificationReportWireError(t, store2.Save(context.Background(), report), ErrorCodeOutcomeUnknown)
			if got := len(readVerificationReportLines(t, path)); got != 1 {
				t.Fatalf("unknown proof reran mutation, rows=%d", got)
			}
		})
	}
}

func TestVerificationReportGroupArbitraryTypedOwnerErrorDoesNotProveRollback(t *testing.T) {
	root := t.TempDir()
	owner := &uncertainVerificationReportOwner{}
	handler, err := NewHandler(HandlerConfig{Token: verificationReportGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterVerificationReportGroup(handler, owner); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer closeVerificationReportHost(t, handler, server)
	rpc, err := NewClient(ClientConfig{Endpoint: server.URL, Token: verificationReportGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := rpc.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	store := NewVerificationReportClient(rpc)
	report := verificationReportRPCFixture(t, modulecore.NewTaskID(), domainverification.StatusVerified, time.Now().UTC())
	const opID = "verification-report-uncertain-owner"
	rpc.opID = opID
	expectVerificationReportWireError(t, store.Save(context.Background(), report), ErrorCodeOutcomeUnknown)
	rpc.opID = opID
	expectVerificationReportWireError(t, store.Save(context.Background(), report), ErrorCodeOutcomeUnknown)
	if owner.saveCalls != 1 || owner.lookupCalls != 1 {
		t.Fatalf("calls save=%d lookup=%d, want 1/1", owner.saveCalls, owner.lookupCalls)
	}
}

type uncertainVerificationReportOwner struct{ saveCalls, lookupCalls int }

func (*uncertainVerificationReportOwner) Save(context.Context, domainverification.VerificationReport) error {
	return nil
}
func (*uncertainVerificationReportOwner) ListRecent(context.Context, int) ([]domainverification.VerificationReport, error) {
	return nil, nil
}
func (*uncertainVerificationReportOwner) GetByTaskID(context.Context, modulecore.TaskID) (domainverification.VerificationReport, error) {
	return domainverification.VerificationReport{}, persistverification.ErrVerificationReportNotFound
}
func (*uncertainVerificationReportOwner) Summary(context.Context) (map[string]map[string]int, error) {
	return map[string]map[string]int{}, nil
}
func (owner *uncertainVerificationReportOwner) SaveForStorageHostOperation(context.Context, persistverification.StorageHostOperationIdentity, domainverification.VerificationReport) error {
	owner.saveCalls++
	return NewError(ErrorCodeSchemaRejected, "typed owner failure")
}
func (owner *uncertainVerificationReportOwner) LookupStorageHostOperationReceipt(context.Context, persistverification.StorageHostOperationIdentity) (json.RawMessage, bool, error) {
	owner.lookupCalls++
	return nil, false, NewError(ErrorCodeSchemaRejected, "typed reconciliation failure")
}

func openVerificationReportHost(t *testing.T, root, path string) (*Handler, *httptest.Server, *Client, *VerificationReportClient, *persistverification.JSONLReportStore) {
	t.Helper()
	owner, err := persistverification.NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewHandler(HandlerConfig{Token: verificationReportGroupTestToken, JournalDir: filepath.Join(root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterVerificationReportGroup(handler, owner); err != nil {
		_ = handler.Close()
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	rpc, err := NewClient(ClientConfig{Endpoint: server.URL, Token: verificationReportGroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		server.Close()
		_ = handler.Close()
		t.Fatal(err)
	}
	if err := rpc.Handshake(context.Background()); err != nil {
		server.Close()
		_ = handler.Close()
		t.Fatal(err)
	}
	return handler, server, rpc, NewVerificationReportClient(rpc), owner
}

func closeVerificationReportHost(t *testing.T, handler *Handler, server *httptest.Server) {
	t.Helper()
	server.Close()
	if err := handler.Close(); err != nil {
		t.Fatalf("close handler: %v", err)
	}
}

func verificationReportRPCFixture(t *testing.T, taskID modulecore.TaskID, status domainverification.VerificationStatus, createdAt time.Time) domainverification.VerificationReport {
	t.Helper()
	report := domainverification.VerificationReport{
		ArtifactID: modulecore.NewArtifactID(), Kind: modulecore.ArtifactKindReport, TaskID: taskID,
		SessionID: "verification-session", Route: "CHAT", Status: status, TriggerLevel: domainverification.TriggerHigh,
		SupersededBy: modulecore.ArtifactID("art_00000000-0000-7000-8000-0000000000a1"), CreatedAt: createdAt,
	}
	digest, err := domainverification.ComputeVerificationReportContentHash(report)
	if err != nil {
		t.Fatal(err)
	}
	report.ContentHash = digest
	return report
}

func readVerificationReportLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func expectVerificationReportWireError(t *testing.T, err error, code string) {
	t.Helper()
	var wire *Error
	if !errors.As(err, &wire) || wire.Code != code {
		t.Fatalf("error=%v want code=%s", err, code)
	}
}
