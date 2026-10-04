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
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const testToken = "unit-test-token-value"

type handlerFixture struct {
	server    *httptest.Server
	client    *Client
	calls     *sync.Map
	journalDi string
}

func newFixture(t *testing.T, mutate func(*Handler)) *handlerFixture {
	t.Helper()
	root := t.TempDir()
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	var calls sync.Map
	h.Register("test", "count", true, func(ctx context.Context, payload json.RawMessage) (any, error) {
		var in struct {
			Key string `json:"key"`
		}
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "payload schema rejected")
		}
		n, _ := calls.LoadOrStore(in.Key, new(int))
		counter := n.(*int)
		*counter++
		return map[string]int{"count": *counter}, nil
	})
	h.Register("test", "echo", false, func(ctx context.Context, payload json.RawMessage) (any, error) {
		var in map[string]any
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "payload schema rejected")
		}
		return in, nil
	})
	h.Register("test", "boom", true, func(ctx context.Context, payload json.RawMessage) (any, error) {
		return nil, ownerRolledBack(NewError(ErrorCodeStoreUnavailable, "store is not served in this build"))
	})
	if mutate != nil {
		mutate(h)
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
	return &handlerFixture{server: srv, client: c, calls: &calls, journalDi: root}
}

func TestHandshakeReportsContractAndGeneration(t *testing.T) {
	f := newFixture(t, nil)
	info, err := f.client.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	if info.ContractVersion != ContractVersion {
		t.Fatalf("contract=%q, want %q", info.ContractVersion, ContractVersion)
	}
	if info.Generation == 0 {
		t.Fatalf("generation must be non-zero")
	}
}

func TestAuthRejectsMissingAndWrongToken(t *testing.T) {
	f := newFixture(t, nil)
	for _, tc := range []struct{ name, header string }{
		{"missing", ""},
		{"wrong", "Bearer nope"},
		{"wrong scheme", testToken},
		{"duplicated", "Bearer " + testToken + ", Bearer " + testToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, f.server.URL+RPCPath, strings.NewReader(`{"contract":"`+ContractVersion+`","op_id":"op-1","group":"test","op":"echo","payload":{}}`))
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status=%d, want 401", resp.StatusCode)
			}
			var out Response
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Error == nil || out.Error.Code != ErrorCodeUnauthorized {
				t.Fatalf("error=%+v, want unauthorized", out.Error)
			}
		})
	}
}

func TestClientSurfacesUnauthorized(t *testing.T) {
	root := t.TempDir()
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: "wrong", HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	err = c.Handshake(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeUnauthorized {
		t.Fatalf("error=%v, want unauthorized", err)
	}
}

func TestContractVersionMismatchCloses(t *testing.T) {
	f := newFixture(t, nil)
	body := `{"contract":"rencrow-storage-rpc/v0","op_id":"op-cv","group":"test","op":"echo","payload":{}}`
	var out Response
	if err := f.client.postRaw(context.Background(), []byte(body), &out); err != nil {
		t.Fatalf("postRaw: %v", err)
	}
	if out.Error == nil || out.Error.Code != ErrorCodeContractMismatch {
		t.Fatalf("error=%+v, want contract_version_mismatch", out.Error)
	}
}

func TestUnknownOperationAndGroupRejected(t *testing.T) {
	f := newFixture(t, nil)
	for _, tc := range []struct{ group, op string }{{"test", "nope"}, {"nosuchgroup", "echo"}, {"", ""}} {
		err := f.client.Call(context.Background(), tc.group, tc.op, map[string]any{}, nil)
		var e *Error
		if !errors.As(err, &e) || e.Code != ErrorCodeOperationUnsupported {
			t.Fatalf("group=%q op=%q error=%v, want operation_unsupported", tc.group, tc.op, err)
		}
	}
}

func TestOnlyRPCPathIsServed(t *testing.T) {
	f := newFixture(t, nil)
	for _, p := range []string{"/", "/v1/rpc/extra", "/files/etc/passwd", "/v1/rpc?op=echo"} {
		resp, err := http.Post(f.server.URL+p, "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatalf("post %s: %v", p, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("path=%q status=%d, want 404", p, resp.StatusCode)
		}
	}
}

func TestMalformedSchemaRejected(t *testing.T) {
	f := newFixture(t, nil)
	cases := []string{
		`not json`,
		`{"contract":"` + ContractVersion + `","group":"test","op":"echo"}`,
		`{"contract":"` + ContractVersion + `","op_id":"  ","group":"test","op":"echo"}`,
		`{"contract":"` + ContractVersion + `","op_id":"op-ms","group":"test","op":"echo","payload":{},"unknown_field":1}`,
		`{"contract":"` + ContractVersion + `","op_id":"op-ms2","group":"test","op":"echo","payload":{}} trailing`,
	}
	for _, body := range cases {
		var out Response
		if err := f.client.postRaw(context.Background(), []byte(body), &out); err != nil {
			t.Fatalf("postRaw: %v", err)
		}
		if out.Error == nil || out.Error.Code != ErrorCodeSchemaRejected {
			t.Fatalf("body=%s error=%+v, want schema_rejected", body, out.Error)
		}
	}
}

func TestMutatingOperationIsIdempotentPerOpID(t *testing.T) {
	f := newFixture(t, nil)
	payload := map[string]any{"key": "task-1"}
	var first map[string]int
	if err := f.client.Call(context.Background(), "test", "count", payload, &first); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if first["count"] != 1 {
		t.Fatalf("first=%v, want count 1", first)
	}
	f.client.opID = "fixed-op-id"
	var out1, out2 map[string]int
	if err := f.client.Call(context.Background(), "test", "count", payload, &out1); err != nil {
		t.Fatalf("replay send 1: %v", err)
	}
	if err := f.client.Call(context.Background(), "test", "count", payload, &out2); err != nil {
		t.Fatalf("replay send 2: %v", err)
	}
	if out1["count"] != 2 || out2["count"] != 2 {
		t.Fatalf("replays=%v %v, want both count 2 (executed once)", out1, out2)
	}
}

func TestSameOpIDDifferentPayloadIsConflict(t *testing.T) {
	f := newFixture(t, nil)
	f.client.opID = "conflict-op"
	if err := f.client.Call(context.Background(), "test", "count", map[string]any{"key": "a"}, nil); err != nil {
		t.Fatalf("first: %v", err)
	}
	err := f.client.Call(context.Background(), "test", "count", map[string]any{"key": "b"}, nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeDuplicateConflict {
		t.Fatalf("error=%v, want duplicate_op_conflict", err)
	}
}

func TestStaleWriterGenerationRejected(t *testing.T) {
	f := newFixture(t, nil)
	req := Request{Contract: ContractVersion, OpID: "gen-op", Group: "test", Op: "count", Generation: f.client.generation + 5, Payload: json.RawMessage(`{"key":"g"}`)}
	var out Response
	if err := f.client.send(context.Background(), req, &out); err != nil {
		t.Fatalf("send: %v", err)
	}
	if out.Error == nil || out.Error.Code != ErrorCodeGenerationStale {
		t.Fatalf("error=%+v, want writer_generation_stale", out.Error)
	}
}

func TestStoreLevelTypedErrorPropagates(t *testing.T) {
	f := newFixture(t, nil)
	err := f.client.Call(context.Background(), "test", "boom", map[string]any{}, nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeStoreUnavailable {
		t.Fatalf("error=%v, want store_unavailable", err)
	}
}

func TestReadOperationsAreNotJournaled(t *testing.T) {
	f := newFixture(t, nil)
	for i := 0; i < 3; i++ {
		var out map[string]any
		if err := f.client.Call(context.Background(), "test", "echo", map[string]any{"v": i}, &out); err != nil {
			t.Fatalf("read call %d: %v", i, err)
		}
	}
	entries, err := f.client.journalEntries(context.Background())
	if err != nil {
		t.Fatalf("journalEntries: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("journal entries=%d, want 0 for read-only traffic", len(entries))
	}
}

func TestOutcomeUnknownResolvesThroughResultQuery(t *testing.T) {
	root := t.TempDir()
	h, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	var mu sync.Mutex
	seen := map[string]int{}
	h.Register("test", "count", true, func(ctx context.Context, payload json.RawMessage) (any, error) {
		var in struct {
			Key string `json:"key"`
		}
		_ = json.Unmarshal(payload, &in)
		mu.Lock()
		seen[in.Key]++
		n := seen[in.Key]
		mu.Unlock()
		return map[string]int{"count": n}, nil
	})
	srv := httptest.NewServer(h)
	defer srv.Close()
	c, err := NewClient(ClientConfig{Endpoint: srv.URL, Token: testToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.Handshake(context.Background()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	// 送信後にconnectionを切るtransportでoutcome_unknown経路を作る。
	// op_idは送信前に確定しているため、responseを失ってもjournalで解決できる。
	c.opID = "lost"
	c.sendHook = func() error { return errConnectionResetAfterSend }
	var first map[string]int
	err = c.Call(context.Background(), "test", "count", map[string]any{"key": "lost"}, &first)
	if err != nil {
		t.Fatalf("first call with resolution: %v", err)
	}
	if first["count"] != 1 {
		t.Fatalf("first=%v, want count 1", first)
	}
	mu.Lock()
	executions := seen["lost"]
	mu.Unlock()
	if executions != 1 {
		t.Fatalf("store executions=%d, want 1 (no duplicate append)", executions)
	}
	c.sendHook = nil
	var replay map[string]int
	if err := c.Call(context.Background(), "test", "count", map[string]any{"key": "lost"}, &replay); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay["count"] != 1 {
		t.Fatalf("replay=%v, want recorded outcome count 1", replay)
	}
}

func TestUnreachableEndpointIsTypedNotFallback(t *testing.T) {
	c, err := NewClient(ClientConfig{Endpoint: "http://127.0.0.1:1", Token: testToken})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	err = c.Handshake(context.Background())
	var e *Error
	if !errors.As(err, &e) || e.Code != ErrorCodeUnreachable {
		t.Fatalf("error=%v, want storage_host_unreachable", err)
	}
	if strings.Contains(err.Error(), "fallback") {
		t.Fatalf("error must not report a local fallback: %v", err)
	}
}

func TestJournalSurvivesHandlerRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "journal")
	h1, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	h1.Register("test", "count", true, func(ctx context.Context, payload json.RawMessage) (any, error) {
		return map[string]int{"count": 1}, nil
	})
	srv1 := httptest.NewServer(h1)
	c1, err := NewClient(ClientConfig{Endpoint: srv1.URL, Token: testToken, HTTPClient: srv1.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c1.Handshake(context.Background()); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	c1.opID = "restart-op"
	var out map[string]int
	if err := c1.Call(context.Background(), "test", "count", map[string]any{"key": "k"}, &out); err != nil {
		t.Fatalf("call: %v", err)
	}
	srv1.Close()
	h1.Close()

	h2, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: root})
	if err != nil {
		t.Fatalf("NewHandler restart: %v", err)
	}
	var executions int
	h2.Register("test", "count", true, func(ctx context.Context, payload json.RawMessage) (any, error) {
		executions++
		return map[string]int{"count": 99}, nil
	})
	srv2 := httptest.NewServer(h2)
	defer srv2.Close()
	c2, err := NewClient(ClientConfig{Endpoint: srv2.URL, Token: testToken, HTTPClient: srv2.Client()})
	if err != nil {
		t.Fatalf("NewClient restart: %v", err)
	}
	if err := c2.Handshake(context.Background()); err != nil {
		t.Fatalf("handshake restart: %v", err)
	}
	c2.opID = "restart-op"
	var replay map[string]int
	if err := c2.Call(context.Background(), "test", "count", map[string]any{"key": "k"}, &replay); err != nil {
		t.Fatalf("replay after restart: %v", err)
	}
	if replay["count"] != 1 {
		t.Fatalf("replay=%v, want recorded outcome count 1", replay)
	}
	if executions != 0 {
		t.Fatalf("executions=%d, want 0 after restart", executions)
	}
}

func TestAuthenticatedAdmissionBoundsNonImportReads(t *testing.T) {
	handler, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	tests := []struct {
		name   string
		prefix string
		suffix string
	}{
		{
			name:   "common-raw-status",
			prefix: `{"contract":"` + ContractVersion + `","op_id":"bounded-status-op","group":"common_raw","op":"get_chatgpt_import_status","payload":{"padding":"`,
			suffix: `"}}`,
		},
		{
			name:   "common-raw-unsupported",
			prefix: `{"contract":"` + ContractVersion + `","op_id":"bounded-unsupported-op","group":"common_raw","op":"not_served","payload":{"padding":"`,
			suffix: `"}}`,
		},
		{
			name:   "reordered-chatgpt-large-envelope",
			prefix: `{"payload":{"padding":"`,
			suffix: `"},"op":"import_chatgpt_raw_batch","group":"common_raw","op_id":"bounded-reordered-op","contract":"` + ContractVersion + `","generation":1}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			counted := &countingReader{reader: io.MultiReader(strings.NewReader(test.prefix), &repeatedByteReader{remaining: maxRequestBytes + 512, value: 'x'}, strings.NewReader(test.suffix))}
			req := httptest.NewRequest(http.MethodPost, RPCPath, nil)
			req.Body = io.NopCloser(counted)
			req.Header.Set("Authorization", "Bearer "+testToken)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			if counted.count > maxRequestBytes+1 {
				t.Fatalf("authenticated %s envelope consumed %d bytes before bounded rejection; maximum is %d", test.name, counted.count, maxRequestBytes+1)
			}
		})
	}

	counted := &countingReader{reader: io.MultiReader(strings.NewReader(tests[0].prefix), &repeatedByteReader{remaining: maxRequestBytes + 512, value: 'x'}, strings.NewReader(tests[0].suffix))}
	unauthorized := httptest.NewRequest(http.MethodPost, RPCPath, nil)
	unauthorized.Body = io.NopCloser(counted)
	unauthorized.Header.Set("Authorization", "Bearer wrong-token")
	handler.ServeHTTP(httptest.NewRecorder(), unauthorized)
	if counted.count != 0 {
		t.Fatalf("unauthorized request consumed %d body bytes before authentication", counted.count)
	}
}

func TestAuthenticatedAdmissionHonorsExactBodyLimits(t *testing.T) {
	handler, err := NewHandler(HandlerConfig{Token: testToken, JournalDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.Close()
	if err := handler.Register("test", "echo", false, func(context.Context, json.RawMessage) (any, error) {
		return map[string]bool{"accepted": true}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := handler.Register(GroupCommonRaw, commonRawChatGPTImportOp, false, func(context.Context, json.RawMessage) (any, error) {
		return map[string]bool{"accepted": true}, nil
	}); err != nil {
		t.Fatal(err)
	}

	ordinaryPrefix := `{"contract":"` + ContractVersion + `","op_id":"ordinary-at-cap","group":"test","op":"echo","generation":1,"payload":{"padding":"`
	ordinarySuffix := `"}}`
	if err := serveCountedEnvelope(handler, ordinaryPrefix, ordinarySuffix, int64(maxRequestBytes), testToken, http.StatusOK, ""); err != nil {
		t.Fatalf("valid ordinary envelope at 32 MiB: %v", err)
	}
	if err := serveCountedEnvelope(handler, ordinaryPrefix, ordinarySuffix, int64(maxRequestBytes+1), testToken, http.StatusBadRequest, ErrorCodeSchemaRejected); err != nil {
		t.Fatalf("ordinary envelope at 32 MiB + 1: %v", err)
	}

	largeImportPrefix := `{"contract":"` + ContractVersion + `","op_id":"canonical-import-at-cap","group":"common_raw","op":"` + commonRawChatGPTImportOp + `","generation":1,"payload":{"padding":"`
	if err := serveCountedEnvelope(handler, largeImportPrefix, ordinarySuffix, int64(maxCommonRawRequestBytes), testToken, http.StatusOK, ""); err != nil {
		t.Fatalf("valid canonical import envelope at 96 MiB: %v", err)
	}
}

func serveCountedEnvelope(handler *Handler, prefix, suffix string, totalBytes int64, token string, wantStatus int, wantError string) error {
	remaining := totalBytes - int64(len(prefix)+len(suffix))
	if remaining < 0 {
		return errors.New("envelope fixture size is smaller than its JSON framing")
	}
	counted := &countingReader{reader: io.MultiReader(strings.NewReader(prefix), &repeatedByteReader{remaining: remaining, value: 'x'}, strings.NewReader(suffix))}
	request := httptest.NewRequest(http.MethodPost, RPCPath, nil)
	request.Body = io.NopCloser(counted)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if counted.count != totalBytes {
		return errors.New("handler consumed a different number of bytes than the fixture length")
	}
	if response.Code != wantStatus {
		return errors.New("handler returned an unexpected status for the bounded envelope")
	}
	var body Response
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		return err
	}
	if wantError == "" && body.Error != nil {
		return body.Error
	}
	if wantError != "" && (body.Error == nil || body.Error.Code != wantError) {
		return errors.New("handler returned an unexpected typed error for the bounded envelope")
	}
	return nil
}

func TestClientRejectsCrossOperationPagedMutationResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch {
		case req.Group == GroupHost && req.Op == "contract":
			writeResponse(w, http.StatusOK, Response{Result: mustJSON(ContractInfo{
				ContractVersion: ContractVersion,
				Generation:      3,
				Operations:      []OperationSpec{{Group: GroupCommonRaw, Op: commonRawIntakeOp, Mutating: true}},
			})})
		case req.Group == GroupCommonRaw && req.Op == commonRawIntakeOp:
			pageSize := int64(8 << 20)
			resultBytes := int64(maxRequestBytes + 1)
			pageCount := int((resultBytes + pageSize - 1) / pageSize)
			body, err := json.Marshal(map[string]any{
				"result_page_set": map[string]any{
					"contract_version":   ContractVersion,
					"op_id":              req.OpID,
					"group":              "different_group",
					"op":                 commonRawIntakeOp,
					"journal_generation": int64(2),
					"result_bytes":       resultBytes,
					"result_sha256":      strings.Repeat("a", 64),
					"page_size":          pageSize,
					"page_count":         pageCount,
				},
			})
			if err != nil {
				t.Errorf("marshal fake response: %v", err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected request route: %s/%s", req.Group, req.Op)
			writeResponse(w, http.StatusNotFound, Response{Error: NewError(ErrorCodeOperationUnsupported, "unexpected fake route")})
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: testToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Receipt map[string]any `json:"receipt"`
	}
	err = client.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, map[string]any{"request_id": "paged-unbound"}, &result)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeOutcomeUnknown {
		t.Fatalf("cross-operation result page set must remain unresolved: result=%+v err=%v", result, err)
	}
}

func TestClientLostResponseRecoveryRejectsCrossOperationPages(t *testing.T) {
	validResult := bytes.Repeat([]byte{' '}, maxRequestBytes+1)
	copy(validResult, []byte("{}"))
	pageCount := (len(validResult) + resultPageBytes - 1) / resultPageBytes
	pageHashes := make([]string, pageCount)
	for pageIndex := range pageHashes {
		start := pageIndex * resultPageBytes
		end := start + resultPageBytes
		if end > len(validResult) {
			end = len(validResult)
		}
		pageHashes[pageIndex] = domainmemory.SHA256Hex(validResult[start:end])
	}
	resultHash := domainmemory.SHA256Hex(validResult)

	for _, mode := range []string{"Call", "Do"} {
		t.Run(mode, func(t *testing.T) {
			const group = "test_non_raw_mutation"
			const operation = "write"
			var mutex sync.Mutex
			var mutationOpIDs []string
			var resultQueryIDs []string
			var pageLookups atomic.Int32
			resultQueries := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				switch {
				case req.Group == GroupHost && req.Op == "contract":
					writeResponse(w, http.StatusOK, Response{Result: mustJSON(ContractInfo{
						ContractVersion: ContractVersion, Generation: 7,
						Operations: []OperationSpec{{Group: group, Op: operation, Mutating: true}},
					})})
				case req.Group == group && req.Op == operation:
					mutex.Lock()
					mutationOpIDs = append(mutationOpIDs, req.OpID)
					mutex.Unlock()
					writeResponse(w, http.StatusOK, Response{Result: json.RawMessage(`{}`)})
				case req.Group == GroupHost && req.Op == "result":
					var pageQuery resultQueryRequest
					if err := json.Unmarshal(req.Payload, &pageQuery); err == nil && pageQuery.Page != nil {
						pageLookups.Add(1)
						if pageQuery.OpID != pageQuery.Page.PageSet.OpID {
							t.Errorf("page query op_id=%q, page set op_id=%q", pageQuery.OpID, pageQuery.Page.PageSet.OpID)
						}
						pageIndex := pageQuery.Page.PageIndex
						start := pageIndex * resultPageBytes
						end := start + resultPageBytes
						if end > len(validResult) {
							end = len(validResult)
						}
						page := ResultPage{PageSet: pageQuery.Page.PageSet, PageIndex: pageIndex, PageSHA256: pageHashes[pageIndex], Data: validResult[start:end]}
						returnedPageSet := pageQuery.Page.PageSet
						writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{Found: true, Status: journalStatusDone, PageSet: &returnedPageSet, Page: &page})})
						return
					}
					var query struct {
						OpID string `json:"op_id"`
					}
					if err := json.Unmarshal(req.Payload, &query); err != nil || !opIDPattern.MatchString(query.OpID) {
						t.Errorf("decode result query: %+v err=%v", query, err)
						writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "invalid result query")})
						return
					}
					mutex.Lock()
					resultQueries++
					queryIndex := resultQueries
					resultQueryIDs = append(resultQueryIDs, query.OpID)
					mutex.Unlock()
					if mode == "Do" && queryIndex > 1 {
						writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{Found: true, Status: journalStatusDone, Result: json.RawMessage(`{}`)})})
						return
					}
					pageSet := ResultPageSet{
						ContractVersion: ContractVersion, OpID: query.OpID, Group: GroupCommonRaw, Op: commonRawIntakeOp,
						JournalGeneration: 5, ResultBytes: int64(len(validResult)), ResultSHA256: resultHash,
						PageSize: resultPageBytes, PageCount: pageCount,
					}
					writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{Found: true, Status: journalStatusDone, PageSet: &pageSet})})
				default:
					t.Errorf("unexpected route %s/%s", req.Group, req.Op)
					writeResponse(w, http.StatusNotFound, Response{Error: NewError(ErrorCodeOperationUnsupported, "unexpected fake route")})
				}
			}))
			client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: testToken, HTTPClient: server.Client()})
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			if err := client.Handshake(context.Background()); err != nil {
				server.Close()
				t.Fatal(err)
			}
			var lost atomic.Bool
			client.sendHook = func() error {
				if lost.CompareAndSwap(false, true) {
					return errConnectionResetAfterSend
				}
				return nil
			}

			var firstErr error
			var fixedOpID string
			payload := map[string]any{"request_id": "non-raw-operation"}
			if mode == "Call" {
				firstErr = client.Call(context.Background(), group, operation, payload, nil)
			} else {
				handle := client.NewOperation(group, operation, payload)
				fixedOpID = handle.OpID
				firstErr = client.Do(context.Background(), handle, nil)
				if !isOutcomeUnknown(firstErr) {
					server.Close()
					t.Fatalf("initial lost response must stay outcome_unknown: %v", firstErr)
				}
				firstErr = client.Do(context.Background(), handle, nil)
				if !isOutcomeUnknown(firstErr) || !handle.pending || handle.OpID != fixedOpID {
					server.Close()
					t.Fatalf("cross-operation page set must preserve pending handle identity: err=%v pending=%v op_id=%q", firstErr, handle.pending, handle.OpID)
				}
				if err := client.Do(context.Background(), handle, nil); err != nil || handle.pending || handle.OpID != fixedOpID {
					server.Close()
					t.Fatalf("same handle must resolve its original result after rejected page set: err=%v pending=%v op_id=%q", err, handle.pending, handle.OpID)
				}
			}
			if mode == "Call" {
				if !isOutcomeUnknown(firstErr) {
					server.Close()
					t.Fatalf("lost non-Raw Call must reject cross-operation page set as outcome_unknown, got %v", firstErr)
				}
				if err := client.Call(context.Background(), group, operation, payload, nil); err != nil {
					server.Close()
					t.Fatalf("same convenience call should retry under retained identity: %v", err)
				}
			}
			mutex.Lock()
			gotMutationIDs := append([]string(nil), mutationOpIDs...)
			gotResultIDs := append([]string(nil), resultQueryIDs...)
			mutex.Unlock()
			if len(gotMutationIDs) == 0 {
				server.Close()
				t.Fatal("test did not send its mutation")
			}
			if mode == "Call" && (len(gotMutationIDs) != 2 || gotMutationIDs[0] != gotMutationIDs[1]) {
				server.Close()
				t.Fatalf("convenience retry changed mutation op_id: %v", gotMutationIDs)
			}
			if mode == "Do" && len(gotMutationIDs) != 1 {
				server.Close()
				t.Fatalf("Do result recovery reran the owner mutation: %v", gotMutationIDs)
			}
			for _, resultID := range gotResultIDs {
				if resultID != gotMutationIDs[0] {
					server.Close()
					t.Fatalf("result recovery used op_id %q, want original %q", resultID, gotMutationIDs[0])
				}
			}
			if pageLookups.Load() != 0 {
				server.Close()
				t.Fatalf("non-Raw recovery fetched %d Common Raw pages before rejecting operation mismatch", pageLookups.Load())
			}
			server.Close()
		})
	}
}

func TestClientRejectsPageSetBeyondReceiptContractBeforeLookup(t *testing.T) {
	var pageLookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		switch {
		case req.Group == GroupHost && req.Op == "contract":
			writeResponse(w, http.StatusOK, Response{Result: mustJSON(ContractInfo{
				ContractVersion: ContractVersion,
				Generation:      3,
				Operations:      []OperationSpec{{Group: GroupCommonRaw, Op: commonRawIntakeOp, Mutating: true}},
			})})
		case req.Group == GroupCommonRaw && req.Op == commonRawIntakeOp:
			resultBytes := maxCommonRawIntakeResultBytes + 1
			pageCount := int((resultBytes + int64(resultPageBytes) - 1) / int64(resultPageBytes))
			pageSet := ResultPageSet{ContractVersion: ContractVersion, OpID: req.OpID, Group: GroupCommonRaw, Op: commonRawIntakeOp, JournalGeneration: 2, ResultBytes: resultBytes, ResultSHA256: strings.Repeat("a", 64), PageSize: resultPageBytes, PageCount: pageCount}
			writeResponse(w, http.StatusOK, Response{ResultPageSet: &pageSet})
		case req.Group == GroupHost && req.Op == "result":
			pageLookups.Add(1)
			var query resultQueryRequest
			if err := json.Unmarshal(req.Payload, &query); err != nil || query.Page == nil {
				t.Errorf("expected a page lookup after oversized descriptor, got %+v err=%v", query, err)
			}
			writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{Found: true, Status: journalStatusDone})})
		default:
			t.Errorf("unexpected request route: %s/%s", req.Group, req.Op)
			writeResponse(w, http.StatusNotFound, Response{Error: NewError(ErrorCodeOperationUnsupported, "unexpected fake route")})
		}
	}))
	defer server.Close()
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: testToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Receipt map[string]any `json:"receipt"`
	}
	err = client.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, map[string]any{"request_id": "paged-over-bound"}, &result)
	var hostErr *Error
	if !errors.As(err, &hostErr) || hostErr.Code != ErrorCodeOutcomeUnknown || pageLookups.Load() != 0 {
		t.Fatalf("over-bound page descriptor must fail before allocation or network loop: err=%v page_lookups=%d max_records=%d max_assets=%d max_metadata=%d", err, pageLookups.Load(), domainmemory.CommonRawMaxRecords, domainmemory.CommonRawMaxAssets, domainmemory.CommonRawMaxMetadataSize)
	}
}

func TestClientRejectsMalformedResultPages(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "missing"},
		{name: "reordered"},
		{name: "duplicate"},
		{name: "cross-operation"},
		{name: "tampered-digest"},
		{name: "oversized"},
		{name: "trailing-result-bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var pageLookups atomic.Int32
			var mutationOpIDs []string
			var mutationOpIDsMu sync.Mutex
			var validTrailingBody []byte
			if test.name == "trailing-result-bytes" {
				validTrailingBody = bytes.Repeat([]byte{' '}, maxRequestBytes+1)
				copy(validTrailingBody, []byte("{}!"))
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				switch {
				case req.Group == GroupHost && req.Op == "contract":
					writeResponse(w, http.StatusOK, Response{Result: mustJSON(ContractInfo{
						ContractVersion: ContractVersion,
						Generation:      3,
						Operations:      []OperationSpec{{Group: GroupCommonRaw, Op: commonRawIntakeOp, Mutating: true}},
					})})
				case req.Group == GroupCommonRaw && req.Op == commonRawIntakeOp:
					mutationOpIDsMu.Lock()
					mutationOpIDs = append(mutationOpIDs, req.OpID)
					mutationOpIDsMu.Unlock()
					resultBytes := int64(maxRequestBytes + 1)
					resultSHA := strings.Repeat("a", 64)
					if validTrailingBody != nil {
						resultBytes = int64(len(validTrailingBody))
						resultSHA = domainmemory.SHA256Hex(validTrailingBody)
					}
					pageCount := int((resultBytes + int64(resultPageBytes) - 1) / int64(resultPageBytes))
					set := ResultPageSet{ContractVersion: ContractVersion, OpID: req.OpID, Group: GroupCommonRaw, Op: commonRawIntakeOp, JournalGeneration: 2, ResultBytes: resultBytes, ResultSHA256: resultSHA, PageSize: resultPageBytes, PageCount: pageCount}
					writeResponse(w, http.StatusOK, Response{ResultPageSet: &set})
				case req.Group == GroupHost && req.Op == "result":
					pageLookups.Add(1)
					var query resultQueryRequest
					if err := json.Unmarshal(req.Payload, &query); err != nil || query.Page == nil {
						t.Errorf("expected fixed-index page query, got %+v err=%v", query, err)
						writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "missing page query")})
						return
					}
					set := query.Page.PageSet
					pageIndex := query.Page.PageIndex
					data := bytes.Repeat([]byte{'x'}, resultPageBytes)
					pageSet := set
					pageSHA := domainmemory.SHA256Hex(data)
					switch test.name {
					case "missing":
						writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{Found: true, Status: journalStatusDone, PageSet: &set})})
						return
					case "reordered":
						pageIndex = 1
					case "duplicate":
						if pageIndex > 0 {
							pageIndex = 0
						}
					case "cross-operation":
						pageSet.Op = "other"
					case "tampered-digest":
						pageSHA = strings.Repeat("0", 64)
					case "oversized":
						data = append(data, 'x')
						pageSHA = domainmemory.SHA256Hex(data)
					case "trailing-result-bytes":
						start := query.Page.PageIndex * resultPageBytes
						end := start + resultPageBytes
						if end > len(validTrailingBody) {
							end = len(validTrailingBody)
						}
						data = append([]byte(nil), validTrailingBody[start:end]...)
						pageSHA = domainmemory.SHA256Hex(data)
					}
					page := ResultPage{PageSet: pageSet, PageIndex: pageIndex, PageSHA256: pageSHA, Data: data}
					writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{Found: true, Status: journalStatusDone, PageSet: &set, Page: &page})})
				default:
					t.Errorf("unexpected request route: %s/%s", req.Group, req.Op)
					writeResponse(w, http.StatusNotFound, Response{Error: NewError(ErrorCodeOperationUnsupported, "unexpected fake route")})
				}
			}))
			client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: testToken, HTTPClient: server.Client()})
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			if err := client.Handshake(context.Background()); err != nil {
				server.Close()
				t.Fatal(err)
			}
			var result struct {
				Receipt map[string]any `json:"receipt"`
			}
			callErr := client.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, map[string]any{"request_id": "malformed-page"}, &result)
			var hostErr *Error
			if !errors.As(callErr, &hostErr) || hostErr.Code != ErrorCodeOutcomeUnknown {
				server.Close()
				t.Fatalf("%s page anomaly must remain outcome_unknown: result=%+v err=%v", test.name, result, callErr)
			}
			mutationOpIDsMu.Lock()
			firstMutationOpIDs := append([]string(nil), mutationOpIDs...)
			mutationOpIDsMu.Unlock()
			if len(firstMutationOpIDs) != 1 || !strings.Contains(callErr.Error(), firstMutationOpIDs[0]) {
				server.Close()
				t.Fatalf("page failure did not preserve its unresolved op_id: requests=%v err=%v", firstMutationOpIDs, callErr)
			}
			retryErr := client.Call(context.Background(), GroupCommonRaw, commonRawIntakeOp, map[string]any{"request_id": "malformed-page"}, &result)
			if !errors.As(retryErr, &hostErr) || hostErr.Code != ErrorCodeOutcomeUnknown {
				server.Close()
				t.Fatalf("retry after %s page anomaly must remain outcome_unknown: err=%v", test.name, retryErr)
			}
			mutationOpIDsMu.Lock()
			retriedMutationOpIDs := append([]string(nil), mutationOpIDs...)
			mutationOpIDsMu.Unlock()
			if len(retriedMutationOpIDs) != 2 || retriedMutationOpIDs[1] != firstMutationOpIDs[0] {
				server.Close()
				t.Fatalf("retry changed unresolved op_id: requests=%v", retriedMutationOpIDs)
			}
			if test.name == "missing" && pageLookups.Load() != 2 {
				server.Close()
				t.Fatalf("missing page lookups=%d, want one per same-op retry", pageLookups.Load())
			}
			server.Close()
		})
	}
}

func TestClientJournalPageFailuresFailClosed(t *testing.T) {
	tests := []struct {
		name string
		mode string
	}{
		{name: "missing-page", mode: "missing"},
		{name: "cross-operation", mode: "cross-operation"},
		{name: "malformed-page", mode: "malformed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var pageLookups atomic.Int32
			pageSet := ResultPageSet{
				ContractVersion: ContractVersion, OpID: "op-journal-pages", Group: GroupCommonRaw, Op: commonRawIntakeOp,
				JournalGeneration: 3, ResultBytes: maxRequestBytes + 1, ResultSHA256: strings.Repeat("a", 64),
				PageSize: resultPageBytes, PageCount: int((maxRequestBytes + int64(resultPageBytes)) / int64(resultPageBytes)),
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req Request
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				switch {
				case req.Group == GroupHost && req.Op == "contract":
					writeResponse(w, http.StatusOK, Response{Result: mustJSON(ContractInfo{
						ContractVersion: ContractVersion, Generation: 4,
						Operations: []OperationSpec{{Group: GroupCommonRaw, Op: commonRawIntakeOp, Mutating: true}},
					})})
				case req.Group == GroupHost && req.Op == "journal":
					writeResponse(w, http.StatusOK, Response{Result: mustJSON(struct {
						Entries []journalEntryView `json:"entries"`
					}{Entries: []journalEntryView{{OpID: pageSet.OpID, Group: pageSet.Group, Op: pageSet.Op, Generation: pageSet.JournalGeneration, Status: journalStatusDone, PageSet: &pageSet}}})})
				case req.Group == GroupHost && req.Op == "result":
					pageLookups.Add(1)
					var query resultQueryRequest
					if err := json.Unmarshal(req.Payload, &query); err != nil || query.Page == nil {
						t.Errorf("expected fixed page query, got %+v err=%v", query, err)
						writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "missing page query")})
						return
					}
					if test.mode == "missing" {
						writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{Found: true, Status: journalStatusDone, PageSet: &pageSet})})
						return
					}
					returnedPageSet := pageSet
					data := bytes.Repeat([]byte{'x'}, resultPageBytes)
					pageSHA := domainmemory.SHA256Hex(data)
					if test.mode == "cross-operation" {
						returnedPageSet.Op = "other"
					} else {
						data = []byte("bad")
						pageSHA = strings.Repeat("0", 64)
					}
					page := ResultPage{PageSet: returnedPageSet, PageIndex: query.Page.PageIndex, PageSHA256: pageSHA, Data: data}
					writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{Found: true, Status: journalStatusDone, PageSet: &pageSet, Page: &page})})
				default:
					t.Errorf("unexpected route %s/%s", req.Group, req.Op)
					writeResponse(w, http.StatusNotFound, Response{Error: NewError(ErrorCodeOperationUnsupported, "unexpected fake route")})
				}
			}))
			client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: testToken, HTTPClient: server.Client()})
			if err != nil {
				server.Close()
				t.Fatal(err)
			}
			if err := client.Handshake(context.Background()); err != nil {
				server.Close()
				t.Fatal(err)
			}
			entries, err := client.journalEntries(context.Background())
			if err == nil || entries != nil {
				server.Close()
				t.Fatalf("journal page anomaly must fail closed without partial entries: entries=%+v err=%v", entries, err)
			}
			if pageLookups.Load() != 1 {
				server.Close()
				t.Fatalf("page anomaly lookups=%d, want one", pageLookups.Load())
			}
			server.Close()
		})
	}
}

type repeatedByteReader struct {
	remaining int64
	value     byte
}

func (r *repeatedByteReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = r.value
	}
	r.remaining -= int64(n)
	return n, nil
}
