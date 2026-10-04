package storagehost

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	filememory "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/memory"
)

const operationMemoryTestToken = "operation-memory-test-token"

type operationMemoryTestOwner struct {
	*filememory.FileStore
	mu                  sync.Mutex
	applyCalls          int
	applyErrorAfterSave error
	lookupOverride      func(string, string, string) (filememory.StorageHostMemoryLookup, error)
}

func (o *operationMemoryTestOwner) ApplyStorageHostMemoryOperation(operation filememory.StorageHostMemoryOperation) (filememory.StorageHostMemoryReceipt, error) {
	o.mu.Lock()
	o.applyCalls++
	o.mu.Unlock()
	receipt, err := o.FileStore.ApplyStorageHostMemoryOperation(operation)
	if err == nil && o.applyErrorAfterSave != nil {
		return filememory.StorageHostMemoryReceipt{}, o.applyErrorAfterSave
	}
	return receipt, err
}

func (o *operationMemoryTestOwner) LookupStorageHostMemoryOperation(opID, operation, payloadHash string) (filememory.StorageHostMemoryLookup, error) {
	if o.lookupOverride != nil {
		return o.lookupOverride(opID, operation, payloadHash)
	}
	return o.FileStore.LookupStorageHostMemoryOperation(opID, operation, payloadHash)
}

func (o *operationMemoryTestOwner) applicationCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.applyCalls
}

type operationMemoryTestHost struct {
	handler *Handler
	server  *httptest.Server
	client  *Client
	owner   *operationMemoryTestOwner
	once    sync.Once
}

func newOperationMemoryTestHost(t *testing.T, memoryDir, journalDir string, now time.Time) *operationMemoryTestHost {
	t.Helper()
	store, err := filememory.OpenRecoverableFileStoreAt(memoryDir)
	if err != nil {
		t.Fatalf("OpenRecoverableFileStoreAt: %v", err)
	}
	store.WithClock(func() time.Time { return now })
	owner := &operationMemoryTestOwner{FileStore: store}
	h, err := NewHandler(HandlerConfig{Token: operationMemoryTestToken, JournalDir: journalDir})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterOperationMemoryGroup(h, owner); err != nil {
		_ = h.Close()
		t.Fatalf("RegisterOperationMemoryGroup: %v", err)
	}
	srv := httptest.NewServer(h)
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: operationMemoryTestToken, HTTPClient: srv.Client()})
	if err != nil {
		srv.Close()
		_ = h.Close()
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		srv.Close()
		_ = h.Close()
		t.Fatalf("Handshake: %v", err)
	}
	fixture := &operationMemoryTestHost{handler: h, server: srv, client: c, owner: owner}
	t.Cleanup(fixture.close)
	return fixture
}

func (h *operationMemoryTestHost) close() {
	h.once.Do(func() {
		h.server.Close()
		_ = h.handler.Close()
	})
}

func operationMemoryJST() *time.Location {
	location, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		return time.FixedZone("JST", 9*60*60)
	}
	return location
}

func expectOperationMemoryError(t *testing.T, err error, code string) {
	t.Helper()
	var got *Error
	if !errors.As(err, &got) || got.Code != code {
		t.Fatalf("error=%v, want storage host code %q", err, code)
	}
}

func operationMemoryErrorCode(err error) string {
	var typed *Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}

func TestOperationMemoryMutationsReconcileAfterResponseLossAndOwnerReopen(t *testing.T) {
	tests := []struct {
		name    string
		op      string
		opID    string
		payload any
		target  string
		marker  string
	}{
		{
			name: "write_long_term", op: "write_long_term", opID: "memory-write-after-reopen",
			payload: operationMemoryContentPayload{Content: "long-term-once"}, target: "MEMORY.md", marker: "long-term-once",
		},
		{
			name: "append_today", op: "append_today", opID: "memory-append-after-reopen",
			payload: operationMemoryContentPayload{Content: "daily-append-once"}, target: filepath.Join("202603", "20260305.md"), marker: "daily-append-once",
		},
		{
			name: "save_daily_note_for_date", op: "save_daily_note_for_date", opID: "memory-save-date-after-reopen",
			payload: operationMemoryDatePayload{Date: "2026-02-21", Content: "dated-note-once"}, target: filepath.Join("202602", "20260221.md"), marker: "dated-note-once",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			memoryDir := filepath.Join(root, "operation-memory")
			journalDir := filepath.Join(root, "rpc-journal")
			ownerNow := time.Date(2026, 3, 5, 14, 0, 0, 0, operationMemoryJST())
			first := newOperationMemoryTestHost(t, memoryDir, journalDir, ownerNow)
			first.client.opID = tt.opID
			first.handler.crashAfterCommitFor = tt.opID
			err := first.client.Call(context.Background(), GroupOperationMemory, tt.op, tt.payload, nil)
			expectOperationMemoryError(t, err, ErrorCodeOutcomeUnknown)
			if got := first.owner.applicationCount(); got != 1 {
				t.Fatalf("owner mutations after first request=%d, want 1", got)
			}
			first.close()

			// The reopened owner clock is a day later. append_today must reconcile
			// the recorded target, not select a different daily note.
			secondNow := ownerNow.AddDate(0, 0, 1)
			second := newOperationMemoryTestHost(t, memoryDir, journalDir, secondNow)
			first.client.endpoint = second.server.URL + RPCPath
			first.client.httpClient = second.server.Client()
			if err := first.client.Handshake(context.Background()); err != nil {
				t.Fatalf("Handshake after owner reopen: %v", err)
			}
			if err := first.client.Call(context.Background(), GroupOperationMemory, tt.op, tt.payload, nil); err != nil {
				t.Fatalf("retry after owner reopen: %v", err)
			}
			if got := second.owner.applicationCount(); got != 0 {
				t.Fatalf("reopened owner mutation executions=%d, want 0 after receipt reconciliation", got)
			}
			contents, err := os.ReadFile(filepath.Join(memoryDir, tt.target))
			if err != nil {
				t.Fatalf("read committed target: %v", err)
			}
			if got := strings.Count(string(contents), tt.marker); got != 1 {
				t.Fatalf("target marker count=%d, want exactly once; content=%q", got, contents)
			}
			entry, found := second.handler.journal.lookup(tt.opID)
			if !found || entry.Status != journalStatusDone || string(entry.Result) != "null" {
				t.Fatalf("RPC journal entry=%+v found=%v, want DONE with null result", entry, found)
			}
		})
	}
}

func TestOperationMemoryReadOperationsUseOwnerClockAndClosedContract(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 3, 5, 14, 0, 0, 0, operationMemoryJST())
	host := newOperationMemoryTestHost(t, filepath.Join(root, "memory"), filepath.Join(root, "journal"), now)
	ctx := context.Background()

	operations, err := host.client.Contract(ctx)
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	gotOperations := make(map[string]bool, len(operations.Operations))
	for _, spec := range operations.Operations {
		if spec.Group == GroupOperationMemory {
			gotOperations[spec.Op] = spec.Mutating
		}
	}
	wantOperations := map[string]bool{
		"read_long_term": false, "write_long_term": true, "read_today": false,
		"append_today": true, "recent_daily_notes": false,
		"save_daily_note_for_date": true, "memory_context": false,
	}
	if len(gotOperations) != len(wantOperations) {
		t.Fatalf("operation-memory contract=%v, want exactly %v", gotOperations, wantOperations)
	}
	for name, mutating := range wantOperations {
		if gotOperations[name] != mutating {
			t.Errorf("operation %q mutating=%v, want %v", name, gotOperations[name], mutating)
		}
	}

	if err := host.client.Call(ctx, GroupOperationMemory, "write_long_term", operationMemoryContentPayload{Content: "stable fact"}, nil); err != nil {
		t.Fatalf("write_long_term: %v", err)
	}
	if err := host.client.Call(ctx, GroupOperationMemory, "append_today", operationMemoryContentPayload{Content: "today note"}, nil); err != nil {
		t.Fatalf("append_today: %v", err)
	}
	if err := host.client.Call(ctx, GroupOperationMemory, "save_daily_note_for_date", operationMemoryDatePayload{Date: "2026-03-04", Content: "yesterday note"}, nil); err != nil {
		t.Fatalf("save_daily_note_for_date: %v", err)
	}

	read := func(operation string, payload any) string {
		t.Helper()
		var result operationMemoryContentResult
		if err := host.client.Call(ctx, GroupOperationMemory, operation, payload, &result); err != nil {
			t.Fatalf("%s: %v", operation, err)
		}
		return result.Content
	}
	if got := read("read_long_term", operationMemoryEmptyPayload{}); got != "stable fact" {
		t.Fatalf("read_long_term=%q", got)
	}
	if got := read("read_today", operationMemoryEmptyPayload{}); !strings.Contains(got, "today note") || !strings.Contains(got, "# 2026-03-05") {
		t.Fatalf("read_today=%q, want owner-clock date and append", got)
	}
	if got := read("recent_daily_notes", operationMemoryRecentPayload{Days: 2}); !strings.Contains(got, "today note") || !strings.Contains(got, "yesterday note") {
		t.Fatalf("recent_daily_notes=%q, want both owner-clock days", got)
	}
	if got := read("memory_context", operationMemoryEmptyPayload{}); !strings.Contains(got, "stable fact") || !strings.Contains(got, "today note") {
		t.Fatalf("memory_context=%q, want long-term and recent note context", got)
	}
	if err := host.client.Call(ctx, GroupOperationMemory, "read_today", struct {
		Unknown bool `json:"unknown"`
	}{true}, nil); operationMemoryErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unknown field error=%v, want schema_rejected", err)
	}
	if err := host.client.Call(ctx, GroupOperationMemory, "recent_daily_notes", operationMemoryRecentPayload{Days: maxOperationMemoryRecentDays + 1}, nil); operationMemoryErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unbounded days error=%v, want schema_rejected", err)
	}
	if err := host.client.Call(ctx, GroupOperationMemory, "save_daily_note_for_date", operationMemoryDatePayload{Date: "2026-02-30", Content: "invalid"}, nil); operationMemoryErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("noncanonical date error=%v, want schema_rejected", err)
	}
	if err := host.client.Call(ctx, GroupOperationMemory, "save_daily_note_for_date", operationMemoryDatePayload{Date: "2026-2-04", Content: "invalid"}, nil); operationMemoryErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("noncanonical date format error=%v, want schema_rejected", err)
	}
	if err := host.client.Call(ctx, GroupOperationMemory, "write_long_term", operationMemoryContentPayload{Content: strings.Repeat("x", maxOperationMemoryInputBytes+1)}, nil); operationMemoryErrorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("oversized write error=%v, want schema_rejected", err)
	}
	if _, err := operationMemoryResult(strings.Repeat("x", maxOperationMemoryResultBytes+1)); operationMemoryErrorCode(err) != ErrorCodeStoreUnavailable {
		t.Fatalf("oversized result error=%v, want store_unavailable", err)
	}
}

func TestOperationMemoryReadsRejectSymlinksEscapingOwnerRoot(t *testing.T) {
	tests := []struct {
		name        string
		operation   string
		linkName    string
		outsideFile string
		outsideDir  bool
		outsideName string
	}{
		{name: "long-term-file", operation: "read_long_term", linkName: "MEMORY.md", outsideFile: "outside-memory.md"},
		{name: "today-month-directory", operation: "read_today", linkName: "202603", outsideFile: "outside-month", outsideDir: true, outsideName: "20260305.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			memoryDir := filepath.Join(root, "memory")
			host := newOperationMemoryTestHost(t, memoryDir, filepath.Join(root, "journal"), time.Date(2026, 3, 5, 14, 0, 0, 0, operationMemoryJST()))
			outside := filepath.Join(root, tt.outsideFile)
			linkTarget := outside
			if tt.outsideDir {
				if err := os.MkdirAll(outside, 0o755); err != nil {
					t.Fatalf("create outside directory: %v", err)
				}
				if err := os.WriteFile(filepath.Join(outside, tt.outsideName), []byte("outside owner memory"), 0o644); err != nil {
					t.Fatalf("write outside daily note: %v", err)
				}
				linkTarget = outside
			} else if err := os.WriteFile(outside, []byte("outside owner memory"), 0o644); err != nil {
				t.Fatalf("write outside long-term note: %v", err)
			}
			if err := os.Symlink(linkTarget, filepath.Join(memoryDir, tt.linkName)); err != nil {
				message := strings.ToLower(err.Error())
				if errors.Is(err, os.ErrPermission) || strings.Contains(message, "not supported") || strings.Contains(message, "operation not permitted") {
					t.Skipf("symlinks are unsupported in this environment: %v", err)
				}
				t.Fatalf("create escaping symlink: %v", err)
			}
			if err := host.client.Call(context.Background(), GroupOperationMemory, tt.operation, operationMemoryEmptyPayload{}, nil); operationMemoryErrorCode(err) != ErrorCodeStoreUnavailable {
				t.Fatalf("read through escaping symlink error=%v, want store_unavailable", err)
			}
		})
	}
}

func TestOperationMemoryReadsRejectInvalidCanonicalFiles(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		dir     bool
	}{
		{name: "oversized", content: []byte(strings.Repeat("x", maxOperationMemoryInputBytes+1))},
		{name: "invalid-utf8", content: []byte{0xff}},
		{name: "non-regular", dir: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			memoryDir := filepath.Join(root, "memory")
			if err := os.MkdirAll(memoryDir, 0o755); err != nil {
				t.Fatalf("create memory root: %v", err)
			}
			target := filepath.Join(memoryDir, "MEMORY.md")
			if tt.dir {
				if err := os.Mkdir(target, 0o755); err != nil {
					t.Fatalf("create non-regular canonical target: %v", err)
				}
			} else if err := os.WriteFile(target, tt.content, 0o644); err != nil {
				t.Fatalf("write invalid canonical target: %v", err)
			}
			host := newOperationMemoryTestHost(t, memoryDir, filepath.Join(root, "journal"), time.Date(2026, 3, 5, 14, 0, 0, 0, operationMemoryJST()))
			if err := host.client.Call(context.Background(), GroupOperationMemory, "read_long_term", operationMemoryEmptyPayload{}, nil); operationMemoryErrorCode(err) != ErrorCodeStoreUnavailable {
				t.Fatalf("read invalid canonical file error=%v, want store_unavailable", err)
			}
		})
	}
}

func TestOperationMemoryTypedOwnerErrorRequiresReceiptReconciliation(t *testing.T) {
	root := t.TempDir()
	memoryDir, journalDir := filepath.Join(root, "memory"), filepath.Join(root, "journal")
	now := time.Date(2026, 3, 5, 14, 0, 0, 0, operationMemoryJST())
	first := newOperationMemoryTestHost(t, memoryDir, journalDir, now)
	first.owner.applyErrorAfterSave = NewError(ErrorCodeStoreUnavailable, "simulated typed error after durable owner commit")
	first.client.opID = "memory-typed-error-after-commit"
	payload := operationMemoryContentPayload{Content: "committed before typed error"}
	expectOperationMemoryError(t, first.client.Call(context.Background(), GroupOperationMemory, "append_today", payload, nil), ErrorCodeOutcomeUnknown)
	if got := first.owner.applicationCount(); got != 1 {
		t.Fatalf("owner mutations=%d, want 1", got)
	}
	first.close()

	second := newOperationMemoryTestHost(t, memoryDir, journalDir, now)
	first.client.endpoint = second.server.URL + RPCPath
	first.client.httpClient = second.server.Client()
	if err := first.client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake after restart: %v", err)
	}
	if err := first.client.Call(context.Background(), GroupOperationMemory, "append_today", payload, nil); err != nil {
		t.Fatalf("retry after typed owner error: %v", err)
	}
	if got := second.owner.applicationCount(); got != 0 {
		t.Fatalf("owner mutations after receipt reconciliation=%d, want 0", got)
	}
	data, err := os.ReadFile(filepath.Join(memoryDir, "202603", "20260305.md"))
	if err != nil {
		t.Fatalf("read daily note: %v", err)
	}
	if strings.Count(string(data), payload.Content) != 1 {
		t.Fatalf("daily note=%q, want exactly one append", data)
	}
}

func TestOperationMemoryMalformedOwnerReceiptStaysOutcomeUnknown(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 3, 5, 14, 0, 0, 0, operationMemoryJST())
	host := newOperationMemoryTestHost(t, filepath.Join(root, "memory"), filepath.Join(root, "journal"), now)
	host.owner.applyErrorAfterSave = NewError(ErrorCodeStoreUnavailable, "simulated response loss after durable commit")
	host.client.opID = "memory-malformed-owner-receipt"
	payload := operationMemoryContentPayload{Content: "one durable append"}
	expectOperationMemoryError(t, host.client.Call(context.Background(), GroupOperationMemory, "append_today", payload, nil), ErrorCodeOutcomeUnknown)
	if got := host.owner.applicationCount(); got != 1 {
		t.Fatalf("owner mutations=%d, want 1", got)
	}

	host.owner.lookupOverride = func(opID, operation, payloadHash string) (filememory.StorageHostMemoryLookup, error) {
		lookup, err := host.owner.FileStore.LookupStorageHostMemoryOperation(opID, operation, payloadHash)
		lookup.Receipt.ResultJSON = []byte(`{"malformed":"not-null"}`)
		return lookup, err
	}
	expectOperationMemoryError(t, host.client.Call(context.Background(), GroupOperationMemory, "append_today", payload, nil), ErrorCodeOutcomeUnknown)
	if got := host.owner.applicationCount(); got != 1 {
		t.Fatalf("owner mutations after malformed receipt=%d, want 1", got)
	}
}
