package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	persistdci "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestDCIGroupClosedOperationsAndTypedRoundTrip(t *testing.T) {
	f := newDCIGroupFixture(t, t.TempDir())
	want := map[string]bool{
		dciOpSaveTrace: true, dciOpSaveResult: true,
		dciOpFindTraceAction: false, dciOpFindResultAction: false,
		dciOpFindTraceDedupe: false, dciOpFindResultDedupe: false,
		dciOpListRecent: false,
	}
	got := map[string]bool{}
	for _, spec := range f.host.specs {
		if spec.Group == GroupDCI {
			got[spec.Op] = spec.Mutating
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operations=%v want=%v", got, want)
	}
	for op, mutating := range want {
		if mutating {
			if _, ok := f.host.recoverable[opKey(GroupDCI, op)]; !ok {
				t.Fatalf("%s is not recoverable", op)
			}
		}
	}

	ctx := context.Background()
	result := validDCIGroupResult("typed-result")
	if err := f.client.SaveSearchResult(ctx, result); err != nil {
		t.Fatal(err)
	}
	if got, found, err := f.client.FindSearchResultByActionID(ctx, result.Trace.ActionID); err != nil || !found || !reflect.DeepEqual(got, result) {
		t.Fatalf("result action lookup=%#v found=%v err=%v", got, found, err)
	}
	if got, found, err := f.client.FindSearchResultByIdempotencyKey(ctx, result.Trace.IdempotencyKey); err != nil || !found || !reflect.DeepEqual(got, result) {
		t.Fatalf("result key lookup=%#v found=%v err=%v", got, found, err)
	}
	if got, found, err := f.client.FindSearchTraceByActionID(ctx, result.Trace.ActionID); err != nil || !found || !reflect.DeepEqual(got, result.Trace) {
		t.Fatalf("trace action lookup=%#v found=%v err=%v", got, found, err)
	}
	if got, found, err := f.client.FindSearchTraceByIdempotencyKey(ctx, result.Trace.IdempotencyKey); err != nil || !found || !reflect.DeepEqual(got, result.Trace) {
		t.Fatalf("trace key lookup=%#v found=%v err=%v", got, found, err)
	}
	items, err := f.client.ListRecent(10)
	if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0], result.Trace) {
		t.Fatalf("list=%#v err=%v", items, err)
	}
}

func TestDCIAllMutationsRecoverLostResponseAcrossOwnerAndHandlerReopen(t *testing.T) {
	tests := []struct {
		name string
		save func(context.Context, *DCIClient, domaindci.SearchResult) error
	}{
		{"trace", func(ctx context.Context, c *DCIClient, v domaindci.SearchResult) error {
			return c.SaveSearchTrace(ctx, v.Trace)
		}},
		{"result", func(ctx context.Context, c *DCIClient, v domaindci.SearchResult) error {
			return c.SaveSearchResult(ctx, v)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newDCIGroupFixture(t, t.TempDir())
			item := validDCIGroupResult("recover-" + test.name)
			opID := "dci-recover-" + test.name
			f.rpc.opID = opID
			f.host.crashAfterCommitFor = opID
			if err := test.save(context.Background(), f.client, item); dciGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("lost response error=%v", err)
			}
			f.restart(t)
			f.rpc.opID = opID
			if err := test.save(context.Background(), f.client, item); err != nil {
				t.Fatalf("recovered save: %v", err)
			}
			items, err := f.client.ListRecent(10)
			if err != nil || len(items) != 1 || items[0].ActionID != item.Trace.ActionID {
				t.Fatalf("list=%#v err=%v", items, err)
			}
		})
	}
}

func TestDCIGroupSameOperationChangedPayloadConflicts(t *testing.T) {
	f := newDCIGroupFixture(t, t.TempDir())
	f.rpc.opID = "dci-conflict"
	item := validDCIGroupResult("conflict-a")
	if err := f.client.SaveSearchResult(context.Background(), item); err != nil {
		t.Fatal(err)
	}
	changed := validDCIGroupResult("conflict-b")
	if err := f.client.SaveSearchResult(context.Background(), changed); !errors.Is(err, persistdci.ErrDCIStorageHostConflict) {
		t.Fatalf("changed payload error=%v", err)
	}
}

func TestDCIListRecentRejectsWireResponseAboveRequestedLimit(t *testing.T) {
	f := newDCIGroupFixture(t, t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(w, "request read failed", http.StatusBadRequest)
			return
		}
		request.Body = io.NopCloser(bytes.NewReader(body))
		var incoming Request
		_ = json.Unmarshal(body, &incoming)
		captured := httptest.NewRecorder()
		f.host.ServeHTTP(captured, request)
		if incoming.Group == GroupDCI && incoming.Op == dciOpListRecent && captured.Code == http.StatusOK {
			var payload dciListPayload
			_ = json.Unmarshal(incoming.Payload, &payload)
			limit := payload.Limit
			if limit == 0 {
				limit = 50
			}
			item := validDCIGroupResult("malicious-over-return").Trace
			items := make([]dciTraceWire, limit+1)
			for i := range items {
				items[i] = dciTraceWire{Trace: item, IdempotencyKey: item.IdempotencyKey}
			}
			captured.Body.Reset()
			_ = json.NewEncoder(captured.Body).Encode(Response{Result: mustJSON(dciListResult{Items: items})})
		}
		for name, values := range captured.Header() {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(captured.Code)
		_, _ = io.Copy(w, captured.Body)
	}))
	defer server.Close()
	f.rpc.endpoint = server.URL + RPCPath
	f.rpc.httpClient = server.Client()
	for _, limit := range []int{1, 0} {
		if _, err := f.client.ListRecent(limit); dciGroupErrorCode(err) != ErrorCodeOutcomeUnknown {
			t.Fatalf("ListRecent(%d) error=%v, want outcome_unknown", limit, err)
		}
	}
}

type dciGroupFixture struct {
	root, databasePath string
	owner              *persistdci.SQLiteStore
	host               *Handler
	server             *httptest.Server
	rpc                *Client
	client             *DCIClient
}

func newDCIGroupFixture(t *testing.T, root string) *dciGroupFixture {
	t.Helper()
	f := &dciGroupFixture{root: root, databasePath: filepath.Join(root, "dci.db")}
	f.open(t, false)
	t.Cleanup(f.close)
	return f
}

func (f *dciGroupFixture) open(t *testing.T, reuse bool) {
	t.Helper()
	var err error
	f.owner, err = persistdci.NewSQLiteStore(f.databasePath)
	if err != nil {
		t.Fatal(err)
	}
	f.host, err = NewHandler(HandlerConfig{Token: "dci-test-token", JournalDir: filepath.Join(f.root, "journal")})
	if err != nil {
		t.Fatal(err)
	}
	if err := RegisterDCIGroup(f.host, f.owner); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(f.host)
	if !reuse {
		f.rpc, err = NewClient(ClientConfig{Endpoint: f.server.URL, Token: "dci-test-token", HTTPClient: f.server.Client()})
		if err != nil {
			t.Fatal(err)
		}
	} else {
		f.rpc.endpoint = f.server.URL + RPCPath
		f.rpc.httpClient = f.server.Client()
	}
	if err := f.rpc.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.client = NewDCIClient(f.rpc)
}

func (f *dciGroupFixture) close() {
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

func (f *dciGroupFixture) restart(t *testing.T) {
	t.Helper()
	f.close()
	f.open(t, true)
}

func validDCIGroupResult(key string) domaindci.SearchResult {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	actionID := modulecore.NewActionID()
	trace := domaindci.SearchTrace{
		TraceID: modulecore.NewTraceID(), ActionID: actionID,
		StartedAt: now, EndedAt: now.Add(time.Second),
		ActorAttribution: domaindci.ActorAttributionAuthenticated,
		ActorKind:        "agent", ActorID: "shiro", IdempotencyKey: key,
		Mode: "dci", UserQuery: "canonical evidence", CorpusScope: []string{"docs/"},
		Status: "completed",
	}
	return domaindci.SearchResult{
		Trace: trace,
		Pack:  domaindci.EvidencePack{ActionID: actionID, Query: trace.UserQuery, CorpusScope: append([]string(nil), trace.CorpusScope...), Confidence: 0.75},
	}
}

func dciGroupErrorCode(err error) string {
	var wire *Error
	if errors.As(err, &wire) {
		return wire.Code
	}
	return ""
}
