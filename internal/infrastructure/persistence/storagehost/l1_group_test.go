package storagehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const l1GroupTestToken = "l1-group-unit-test-token"

type l1GroupFixture struct {
	store  *l1sqlite.L1SQLiteStore
	host   *Handler
	client *Client
	remote *L1StoreClient
}

func newL1GroupFixture(t *testing.T, wrap func(L1GroupOwner) L1GroupOwner) *l1GroupFixture {
	t.Helper()
	dir := t.TempDir()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(dir, "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := NewHandler(HandlerConfig{Token: l1GroupTestToken, JournalDir: filepath.Join(dir, "journal")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	owner := L1GroupOwner(store)
	if wrap != nil {
		owner = wrap(owner)
	}
	if err := RegisterL1Group(host, owner, ""); err != nil {
		t.Fatalf("RegisterL1Group: %v", err)
	}
	server := httptest.NewServer(host)
	t.Cleanup(server.Close)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: l1GroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	return &l1GroupFixture{store: store, host: host, client: client, remote: NewL1StoreClient(client)}
}

func TestL1GroupSaveMessageRecentSessionAndLatestThreadFidelity(t *testing.T) {
	fixture := newL1GroupFixture(t, nil)
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	threadID := modulecore.NewThreadID()
	createdAt := time.Date(2026, 10, 3, 12, 30, 0, 0, time.UTC)
	message := domconv.Message{
		Speaker:   domconv.SpeakerUser,
		Msg:       "conversation RPC message",
		Timestamp: createdAt,
		Meta:      map[string]interface{}{"origin": "l1-group-test", "ordinal": 3},
	}
	if err := fixture.remote.SaveMessage(ctx, string(sessionID), threadID, 1, modulecore.ThreadKindUserConversation, "conv:"+string(threadID), message, l1sqlite.MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	events, err := fixture.remote.RecentBySession(ctx, string(sessionID), 10)
	if err != nil {
		t.Fatalf("RecentBySession: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("RecentBySession returned %d events, want one", len(events))
	}
	got := events[0]
	if got.SessionID != string(sessionID) || got.ThreadID != threadID || got.ThreadSeq != 1 || got.ThreadKind != modulecore.ThreadKindUserConversation || got.Message != message.Msg || got.Speaker != message.Speaker {
		t.Fatalf("saved message fields lost across RPC: %+v", got)
	}
	if got.Meta["origin"] != "l1-group-test" || got.Meta["ordinal"] != float64(3) {
		t.Fatalf("saved message metadata mismatch: %#v", got.Meta)
	}
	latestID, latestSeq, latestKind, found, err := fixture.remote.LatestConversationThreadReference(ctx, string(sessionID))
	if err != nil || !found || latestID != threadID || latestSeq != 1 || latestKind != modulecore.ThreadKindUserConversation {
		t.Fatalf("LatestConversationThreadReference=(%q,%d,%q,%v,%v), want saved tuple", latestID, latestSeq, latestKind, found, err)
	}
	if err := fixture.remote.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	empty, err := fixture.remote.RecentBySession(ctx, string(modulecore.NewSessionID()), 10)
	if err != nil || empty != nil {
		t.Fatalf("missing session history=(%#v,%v), want nil without error", empty, err)
	}
}

func TestL1GroupAppendEventAndRecentEvents(t *testing.T) {
	fixture := newL1GroupFixture(t, nil)
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	threadID := modulecore.NewThreadID()
	payload := map[string]interface{}{"nested": map[string]interface{}{"ok": true}, "labels": []interface{}{"a", "b"}}
	appended, err := fixture.remote.AppendEvent(ctx, "test.rpc_roundtrip", "conv:"+string(threadID), string(sessionID), threadID, 2, modulecore.ThreadKindUserConversation, payload, "test")
	if err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if appended == nil || appended.EventType != "test.rpc_roundtrip" || appended.ThreadID != threadID {
		t.Fatalf("AppendEvent result mismatch: %+v", appended)
	}
	events, err := fixture.remote.RecentEvents(ctx, "conv:"+string(threadID), 10)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(events) != 1 || events[0].ID != appended.ID || events[0].SessionID != string(sessionID) || events[0].ThreadSeq != 2 {
		t.Fatalf("RecentEvents mismatch: %+v", events)
	}
	if nested, ok := events[0].Payload["nested"].(map[string]interface{}); !ok || nested["ok"] != true {
		t.Fatalf("event payload did not roundtrip: %#v", events[0].Payload)
	}
}

func TestL1GroupAppendEventNumericPayloadRoundTrip(t *testing.T) {
	fixture := newL1GroupFixture(t, nil)
	entry, err := fixture.remote.AppendEvent(context.Background(), "test.numeric_payload", "kb:numeric-payload", "", "", 0, "", map[string]interface{}{"ordinal": 3}, "test")
	if err != nil {
		t.Fatalf("AppendEvent with ordinary integer payload: %v", err)
	}
	if entry == nil || entry.Payload["ordinal"] != float64(3) {
		t.Fatalf("AppendEvent numeric payload = %+v, want JSON number 3", entry)
	}
}

func TestL1GroupSearchCacheSaveFreshSimilarAndInvalidate(t *testing.T) {
	fixture := newL1GroupFixture(t, nil)
	ctx := context.Background()
	entry, err := fixture.remote.SaveSearchCache(ctx, "web", "RenCrow storage host", `{"items":[{"title":"host"}]}`, []string{"https://example.test/source"}, time.Hour)
	if err != nil {
		t.Fatalf("SaveSearchCache: %v", err)
	}
	if entry == nil || entry.Provider != "web" || entry.ResultsJSON != `{"items":[{"title":"host"}]}` {
		t.Fatalf("SaveSearchCache result mismatch: %+v", entry)
	}
	now := time.Now().UTC().Add(time.Minute)
	fresh, err := fixture.remote.GetFreshSearchCache(ctx, "web", "RenCrow storage host", now)
	if err != nil || fresh == nil || fresh.QueryHash != entry.QueryHash {
		t.Fatalf("GetFreshSearchCache=(%+v,%v), want saved cache", fresh, err)
	}
	similar, err := fixture.remote.GetSimilarFreshSearchCache(ctx, "web", "RenCrow storage host", now, 0.9)
	if err != nil || similar == nil || similar.QueryHash != entry.QueryHash {
		t.Fatalf("GetSimilarFreshSearchCache=(%+v,%v), want saved cache", similar, err)
	}
	removed, err := fixture.remote.InvalidateSearchCache(ctx, "web", "RenCrow storage host")
	if err != nil || removed != 1 {
		t.Fatalf("InvalidateSearchCache=(%d,%v), want 1 removal", removed, err)
	}
	missing, err := fixture.remote.GetFreshSearchCache(ctx, "web", "RenCrow storage host", now)
	if err != nil || missing != nil {
		t.Fatalf("missing cache=(%+v,%v), want nil without error", missing, err)
	}
}

func TestL1GroupKnowledgeAndWikiReads(t *testing.T) {
	fixture := newL1GroupFixture(t, nil)
	ctx := context.Background()
	knowledge, err := fixture.remote.SearchKnowledgeItemsFTS(ctx, "technology", "no matching item", 10)
	if err != nil || knowledge != nil {
		t.Fatalf("empty SearchKnowledgeItemsFTS=(%+v,%v), want no matches", knowledge, err)
	}
	seed := l1sqlite.WikiPageIndexItem{
		PageID:          "concept:l1-group-fixture",
		Path:            "docs/wiki/concepts/l1-group-fixture.md",
		Title:           "Storage host L1 group fixture",
		Type:            "concept",
		Status:          l1sqlite.WikiPageStatusActive,
		Owner:           "core",
		CanonicalSource: "internal/infrastructure/persistence/storagehost/l1_group.go",
		SourcePaths:     []string{"internal/infrastructure/persistence/storagehost/l1_group.go"},
		Summary:         "A seeded owner record for the L1 RPC search contract.",
	}
	if _, err := fixture.store.SaveWikiPageIndex(ctx, seed); err != nil {
		t.Fatalf("owner seed SaveWikiPageIndex: %v", err)
	}
	wiki, err := fixture.remote.SearchWikiPageIndex(ctx, "Storage host L1 group fixture", 10)
	if err != nil || len(wiki) != 1 || wiki[0].PageID != seed.PageID || wiki[0].Title != seed.Title {
		t.Fatalf("SearchWikiPageIndex=(%+v,%v), want seeded owner record", wiki, err)
	}
}

func TestL1GroupPromotionAndMemoryStateFlow(t *testing.T) {
	fixture := newL1GroupFixture(t, nil)
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	threadID := modulecore.NewThreadID()
	if err := fixture.remote.SaveMessage(ctx, string(sessionID), threadID, 1, modulecore.ThreadKindUserConversation, "conv:"+string(threadID), domconv.NewMessage(domconv.SpeakerUser, "candidate memory", nil), l1sqlite.MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	items, err := fixture.remote.RecentBySession(ctx, string(sessionID), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("RecentBySession=(%d,%v), want source event", len(items), err)
	}
	sourceID := items[0].ID
	if err := fixture.remote.UpdateMemoryState(ctx, sourceID, l1sqlite.MemoryStateCandidate); err != nil {
		t.Fatalf("UpdateMemoryState: %v", err)
	}
	candidates, err := fixture.remote.RecentByState(ctx, l1sqlite.MemoryStateCandidate, 10)
	if err != nil || len(candidates) != 1 || candidates[0].ID != sourceID {
		t.Fatalf("RecentByState=(%+v,%v), want updated source", candidates, err)
	}
	promoted, err := fixture.remote.PromoteMemoryToNamespace(ctx, sourceID, "user:l1-group-test", "test-owner")
	if err != nil || promoted == nil || promoted.Namespace != "user:l1-group-test" || promoted.MemoryState != l1sqlite.MemoryStateConfirmed {
		t.Fatalf("PromoteMemoryToNamespace=(%+v,%v), want confirmed target event", promoted, err)
	}
	userMemory, err := fixture.remote.RecentByNamespace(ctx, "user:l1-group-test", 10)
	if err != nil || len(userMemory) != 1 || userMemory[0].ID != promoted.ID {
		t.Fatalf("RecentByNamespace=(%+v,%v), want promoted event", userMemory, err)
	}
}

func TestL1GroupRecallTraceRoundTrip(t *testing.T) {
	fixture := newL1GroupFixture(t, nil)
	ctx := context.Background()
	sessionID := modulecore.NewSessionID()
	createdAt := time.Date(2026, 10, 3, 13, 0, 0, 0, time.UTC)
	trace := domconv.RecallTrace{
		TraceID:    modulecore.NewTraceID(),
		TurnID:     modulecore.NewTurnID(),
		RootTaskID: modulecore.NewTaskID(),
		SessionID:  string(sessionID),
		OwnerID:    "l1-group-test",
		Role:       "mio",
		Items: []domconv.RecallTraceItem{{
			Layer: "L1", Kind: "knowledge", MemoryID: "memory-1", SourceID: "source-1", SourceType: "test",
			Summary: "recall fixture", Query: "fixture query", Provider: "local", SourceURLs: []string{"https://example.test/source"},
			RetrievedAt: createdAt, Score: 0.75, Decision: "included", Status: "injected", Reason: "matched",
			MemoryState: l1sqlite.MemoryStateConfirmed, Sensitivity: "normal", PromptSection: "memory", TokenCount: 12, PromptIndex: 0,
		}},
		CreatedAt: createdAt,
	}
	if err := fixture.remote.SaveRecallTrace(ctx, trace); err != nil {
		t.Fatalf("SaveRecallTrace: %v", err)
	}
	traces, err := fixture.remote.RecentRecallTraces(ctx, string(sessionID), 10)
	if err != nil || len(traces) != 1 {
		t.Fatalf("RecentRecallTraces=(%+v,%v), want one trace", traces, err)
	}
	got := traces[0]
	if got.TraceID != trace.TraceID || got.TurnID != trace.TurnID || got.RootTaskID != trace.RootTaskID || got.SessionID != trace.SessionID || got.Role != trace.Role || !got.CreatedAt.Equal(trace.CreatedAt) {
		t.Fatalf("recall trace identity mismatch: %+v", got)
	}
	if len(got.Items) != 1 || got.Items[0].MemoryID != "memory-1" || got.Items[0].Summary != "recall fixture" || got.Items[0].Status != "injected" || got.Items[0].TokenCount != 12 {
		t.Fatalf("recall trace item mismatch: %+v", got.Items)
	}
}

type l1AppendCountingOwner struct {
	L1RecoverableGroupOwner
	appendCalls int
}

func (o *l1AppendCountingOwner) AppendEvent(ctx context.Context, eventType, namespace, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, payload map[string]interface{}, source string) (*l1sqlite.L1EventLogEntry, error) {
	o.appendCalls++
	return o.L1RecoverableGroupOwner.AppendEvent(ctx, eventType, namespace, sessionID, threadID, threadSeq, threadKind, payload, source)
}

func (o *l1AppendCountingOwner) AppendEventForOperation(ctx context.Context, identity l1sqlite.ConversationL1OperationIdentity, eventType, namespace, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, payload map[string]interface{}, source string) (*l1sqlite.L1EventLogEntry, error) {
	o.appendCalls++
	return o.L1RecoverableGroupOwner.AppendEventForOperation(ctx, identity, eventType, namespace, sessionID, threadID, threadSeq, threadKind, payload, source)
}

type l1CommitThenErrorOwner struct {
	L1RecoverableGroupOwner
	appendCalls int
}

func (o *l1CommitThenErrorOwner) AppendEvent(ctx context.Context, eventType, namespace, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, payload map[string]interface{}, source string) (*l1sqlite.L1EventLogEntry, error) {
	o.appendCalls++
	entry, err := o.L1RecoverableGroupOwner.AppendEvent(ctx, eventType, namespace, sessionID, threadID, threadSeq, threadKind, payload, source)
	if err != nil {
		return entry, err
	}
	return nil, errors.New("owner failed after append committed")
}

func (o *l1CommitThenErrorOwner) AppendEventForOperation(ctx context.Context, identity l1sqlite.ConversationL1OperationIdentity, eventType, namespace, sessionID string, threadID modulecore.ThreadID, threadSeq modulecore.ThreadSeq, threadKind modulecore.ThreadKind, payload map[string]interface{}, source string) (*l1sqlite.L1EventLogEntry, error) {
	o.appendCalls++
	entry, err := o.L1RecoverableGroupOwner.AppendEventForOperation(ctx, identity, eventType, namespace, sessionID, threadID, threadSeq, threadKind, payload, source)
	if err != nil {
		return entry, err
	}
	return nil, errors.New("owner failed after append committed")
}

func TestL1GroupRejectsMalformedAndUnknownPayloadBeforeOwner(t *testing.T) {
	var counted *l1AppendCountingOwner
	fixture := newL1GroupFixture(t, func(owner L1GroupOwner) L1GroupOwner {
		counted = &l1AppendCountingOwner{L1RecoverableGroupOwner: owner.(L1RecoverableGroupOwner)}
		return counted
	})
	ctx := context.Background()
	unknown := json.RawMessage(`{"event_type":"test.strict","namespace":"kb:fixture","session_id":"","thread_id":"","thread_seq":0,"thread_kind":"","payload":{},"source":"test","extra":true}`)
	if err := fixture.client.Call(ctx, GroupL1, "append_event", unknown, nil); errorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("unknown-field request error=%v, want schema_rejected", err)
	}
	if counted.appendCalls != 0 {
		t.Fatalf("owner AppendEvent called %d times for unknown-field request", counted.appendCalls)
	}
	malformedBody := fmt.Sprintf(`{"contract":%q,"op_id":"op-l1-malformed","group":"l1","op":"append_event","generation":%d,"payload":{"event_type":`, ContractVersion, fixture.client.Generation())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fixture.client.endpoint, strings.NewReader(malformedBody))
	if err != nil {
		t.Fatalf("malformed request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+l1GroupTestToken)
	response, err := fixture.client.httpClient.Do(request)
	if err != nil {
		t.Fatalf("send malformed request: %v", err)
	}
	defer response.Body.Close()
	var wire Response
	if err := json.NewDecoder(response.Body).Decode(&wire); err != nil {
		t.Fatalf("decode malformed response: %v", err)
	}
	if response.StatusCode != http.StatusBadRequest || wire.Error == nil || wire.Error.Code != ErrorCodeSchemaRejected {
		t.Fatalf("malformed request response=(%d,%+v), want schema_rejected", response.StatusCode, wire.Error)
	}
	if counted.appendCalls != 0 {
		t.Fatalf("owner AppendEvent called %d times for malformed request", counted.appendCalls)
	}
	badValue := map[string]interface{}{"not_finite": math.NaN()}
	if _, err := fixture.remote.AppendEvent(ctx, "test.invalid_value", "kb:fixture", "", "", 0, "", badValue, "test"); errorCode(err) != ErrorCodeSchemaRejected {
		t.Fatalf("non-finite map value error=%v, want schema_rejected", err)
	}
	if counted.appendCalls != 0 {
		t.Fatalf("owner AppendEvent called %d times for non-finite value", counted.appendCalls)
	}
}

func TestL1GroupOperationSetIsClosed(t *testing.T) {
	fixture := newL1GroupFixture(t, nil)
	contract, err := fixture.client.Contract(context.Background())
	if err != nil {
		t.Fatalf("Contract: %v", err)
	}
	want := map[string]bool{
		"save_message": true, "save_search_cache": true, "get_fresh_search_cache": true,
		"get_similar_fresh_search_cache": true, "invalidate_search_cache": true,
		"search_knowledge_items_fts": true, "search_wiki_page_index": true,
		"append_event": true, "recent_events": true, "update_memory_state": true,
		"promote_memory_to_namespace": true, "recent_by_namespace": true, "recent_by_state": true,
		"recent_by_session": true, "latest_conversation_thread_reference": true,
		"save_recall_trace": true, "recent_recall_traces": true,
	}
	got := map[string]bool{}
	for _, spec := range contract.Operations {
		if spec.Group == GroupL1 {
			got[spec.Op] = true
		}
	}
	if len(got) != len(want) {
		t.Fatalf("l1 operation count=%d, want %d: %v", len(got), len(want), got)
	}
	for op := range want {
		if !got[op] {
			t.Errorf("l1 operation %q is missing", op)
		}
	}
	if err := fixture.client.Call(context.Background(), GroupL1, "execute_sql", map[string]interface{}{"query": "select 1"}, nil); errorCode(err) != ErrorCodeOperationUnsupported {
		t.Fatalf("arbitrary operation error=%v, want operation_unsupported", err)
	}
}

func TestL1GroupCommitThenErrorIsOutcomeUnknownAndNotRepeated(t *testing.T) {
	var owner *l1CommitThenErrorOwner
	fixture := newL1GroupFixture(t, func(base L1GroupOwner) L1GroupOwner {
		owner = &l1CommitThenErrorOwner{L1RecoverableGroupOwner: base.(L1RecoverableGroupOwner)}
		return owner
	})
	ctx := context.Background()
	eventType := "test.commit_then_error"
	call := func() error {
		_, err := fixture.remote.AppendEvent(ctx, eventType, "kb:fixture", "", "", 0, "", map[string]interface{}{"committed": true}, "test")
		return err
	}
	if err := call(); errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("first append error=%v, want outcome_unknown", err)
	}
	if err := call(); err != nil {
		t.Fatalf("repeated append error=%v, want original committed result recovery", err)
	}
	if owner.appendCalls != 1 {
		t.Fatalf("owner AppendEvent called %d times, want one", owner.appendCalls)
	}
	committed, err := fixture.store.RecentEvents(ctx, "kb:fixture", 10)
	if err != nil {
		t.Fatalf("owner RecentEvents: %v", err)
	}
	if len(committed) != 1 || committed[0].EventType != eventType {
		t.Fatalf("committed owner events=%+v, want one persisted event", committed)
	}
}

func TestL1GroupMutationResultReconstructionFailureStaysUnknown(t *testing.T) {
	dir := t.TempDir()
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(dir, "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	host, err := NewHandler(HandlerConfig{Token: l1GroupTestToken, JournalDir: filepath.Join(dir, "journal")})
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	t.Cleanup(func() { _ = host.Close() })
	owner := &l1AppendCountingOwner{L1RecoverableGroupOwner: store}
	if err := RegisterL1Group(host, owner, ""); err != nil {
		t.Fatalf("RegisterL1Group: %v", err)
	}
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
		host.ServeHTTP(captured, request)
		if incoming.Group == GroupL1 && incoming.Op == l1OpAppendEvent && captured.Code == http.StatusOK {
			var response Response
			if err := json.Unmarshal(captured.Body.Bytes(), &response); err != nil {
				http.Error(w, "response decode failed", http.StatusInternalServerError)
				return
			}
			response.Result = json.RawMessage(`{"event":{"extra":true}}`)
			encoded, err := json.Marshal(response)
			if err != nil {
				http.Error(w, "response encode failed", http.StatusInternalServerError)
				return
			}
			captured.Body.Reset()
			_, _ = captured.Body.Write(encoded)
		}
		for name, values := range captured.Header() {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		w.WriteHeader(captured.Code)
		_, _ = io.Copy(w, captured.Body)
	}))
	t.Cleanup(server.Close)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: l1GroupTestToken, HTTPClient: server.Client()})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	remote := NewL1StoreClient(client)
	call := func() error {
		_, err := remote.AppendEvent(context.Background(), "test.bad_mutation_result", "kb:fixture", "", "", 0, "", map[string]interface{}{"committed": true}, "test")
		return err
	}
	if err := call(); errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("first append error=%v, want outcome_unknown", err)
	}
	if err := call(); errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("repeated append error=%v, want outcome_unknown", err)
	}
	if owner.appendCalls != 1 {
		t.Fatalf("owner AppendEvent called %d times, want one", owner.appendCalls)
	}
	committed, err := store.RecentEvents(context.Background(), "kb:fixture", 10)
	if err != nil || len(committed) != 1 || committed[0].EventType != "test.bad_mutation_result" {
		t.Fatalf("owner events=(%+v,%v), want one committed event", committed, err)
	}
}

func errorCode(err error) string {
	var wire *Error
	if errors.As(err, &wire) {
		return wire.Code
	}
	return ""
}

type l1RestartHost struct {
	store  *l1sqlite.L1SQLiteStore
	host   *Handler
	server *httptest.Server
	client *Client
	remote *L1StoreClient
}

func openL1RestartHost(t *testing.T, dbPath, journalDir string) *l1RestartHost {
	return openL1RestartHostWithArchive(t, dbPath, journalDir, nil)
}

func openL1RestartHostWithArchive(t *testing.T, dbPath, journalDir string, archive l1sqlite.L1ArchiveStore) *l1RestartHost {
	t.Helper()
	store, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	if archive != nil {
		store.WithArchiveStore(archive)
	}
	host, err := NewHandler(HandlerConfig{Token: l1GroupTestToken, JournalDir: journalDir})
	if err != nil {
		_ = store.Close()
		t.Fatalf("NewHandler: %v", err)
	}
	if err := RegisterL1Group(host, store, ""); err != nil {
		_ = host.Close()
		_ = store.Close()
		t.Fatalf("RegisterL1Group: %v", err)
	}
	server := httptest.NewServer(host)
	client, err := NewClient(ClientConfig{Endpoint: server.URL, Token: l1GroupTestToken, HTTPClient: server.Client()})
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
	return &l1RestartHost{store: store, host: host, server: server, client: client, remote: NewL1StoreClient(client)}
}

func (h *l1RestartHost) close() {
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

func TestL1GroupOwnerReceiptPostCommitRecoveryReturnsOriginalResult(t *testing.T) {
	ctx := context.Background()
	type scenario struct {
		name         string
		setup        func(*testing.T, *l1sqlite.L1SQLiteStore)
		call         func(*L1StoreClient) (any, error)
		assertEffect func(*testing.T, *sql.DB)
	}

	sessionID := string(modulecore.NewSessionID())
	threadID := modulecore.NewThreadID()
	message := domconv.Message{
		Speaker:   domconv.SpeakerUser,
		Msg:       "owner receipt recovery message",
		Timestamp: time.Date(2026, 10, 3, 14, 0, 0, 0, time.UTC),
		Meta:      map[string]interface{}{"source": "owner-receipt-test"},
	}
	const cacheQuery = "owner receipt recovery cache"
	const appendEventType = "test.owner_receipt_recovery"
	const appendNamespace = "kb:owner-receipt-recovery"
	const stateMessage = "owner receipt recovery state source"
	stateSession := string(modulecore.NewSessionID())
	stateThread := modulecore.NewThreadID()
	var stateSourceID string
	const promotedMessage = "owner receipt recovery promotion source"
	promotionSession := string(modulecore.NewSessionID())
	promotionThread := modulecore.NewThreadID()
	const promotionNamespace = "user:owner-receipt-recovery"
	var promotionSourceID string
	traceSession := string(modulecore.NewSessionID())
	trace := domconv.RecallTrace{
		TraceID: modulecore.NewTraceID(), TurnID: modulecore.NewTurnID(), RootTaskID: modulecore.NewTaskID(),
		SessionID: traceSession, OwnerID: "owner-receipt-test", Role: "mio",
		CreatedAt: time.Date(2026, 10, 3, 14, 5, 0, 0, time.UTC),
		Items: []domconv.RecallTraceItem{{
			Layer: "L1", Kind: "memory", MemoryID: "memory:owner-receipt", SourceID: "source:owner-receipt",
			SourceType: "test", Summary: "receipt recovery item", Query: "receipt recovery query", Provider: "local",
			RetrievedAt: time.Date(2026, 10, 3, 14, 4, 0, 0, time.UTC), Score: 0.8,
			Decision: "included", Status: "injected", Reason: "matched", MemoryState: l1sqlite.MemoryStateConfirmed,
			Sensitivity: "normal", PromptSection: "memory", TokenCount: 9, PromptIndex: 0,
		}},
	}

	scenarios := []scenario{
		{
			name: "save_message",
			call: func(remote *L1StoreClient) (any, error) {
				return nil, remote.SaveMessage(ctx, sessionID, threadID, 1, modulecore.ThreadKindUserConversation, "conv:"+string(threadID), message, l1sqlite.MemoryStateObserved)
			},
			assertEffect: func(t *testing.T, db *sql.DB) {
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_memory_event WHERE session_id = ? AND message = ?`, sessionID, message.Msg, 1)
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.message_saved' AND session_id = ?`, sessionID, 1)
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_profile_promotion_job WHERE session_id = ?`, sessionID, 1)
			},
		},
		{
			name: "save_search_cache",
			call: func(remote *L1StoreClient) (any, error) {
				return remote.SaveSearchCache(ctx, "web", cacheQuery, `{"items":[{"title":"cached"}]}`, []string{"https://example.test/cached"}, time.Hour)
			},
			assertEffect: func(t *testing.T, db *sql.DB) {
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_search_cache WHERE provider = 'web' AND raw_query = ?`, cacheQuery, 1)
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_event_log WHERE event_type = 'search.cache_saved' AND json_extract(payload_json, '$.raw_query') = ?`, cacheQuery, 1)
			},
		},
		{
			name: "invalidate_search_cache",
			setup: func(t *testing.T, store *l1sqlite.L1SQLiteStore) {
				if _, err := store.SaveSearchCache(ctx, "web", cacheQuery, `{"seed":true}`, nil, time.Hour); err != nil {
					t.Fatalf("seed SaveSearchCache: %v", err)
				}
			},
			call: func(remote *L1StoreClient) (any, error) {
				return remote.InvalidateSearchCache(ctx, "web", cacheQuery)
			},
			assertEffect: func(t *testing.T, db *sql.DB) {
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_search_cache WHERE provider = 'web' AND raw_query = ?`, cacheQuery, 0)
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_event_log WHERE event_type = 'search.cache_invalidated' AND json_extract(payload_json, '$.raw_query') = ?`, cacheQuery, 1)
			},
		},
		{
			name: "append_event",
			call: func(remote *L1StoreClient) (any, error) {
				return remote.AppendEvent(ctx, appendEventType, appendNamespace, "", "", 0, "", map[string]interface{}{"commit": "once"}, "test")
			},
			assertEffect: func(t *testing.T, db *sql.DB) {
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_event_log WHERE event_type = ? AND namespace = ?`, appendEventType, appendNamespace, 1)
			},
		},
		{
			name: "update_memory_state",
			setup: func(t *testing.T, store *l1sqlite.L1SQLiteStore) {
				if err := store.SaveMessage(ctx, stateSession, stateThread, 1, modulecore.ThreadKindUserConversation, "conv:"+string(stateThread), domconv.NewMessage(domconv.SpeakerMio, stateMessage, nil), l1sqlite.MemoryStateObserved); err != nil {
					t.Fatalf("seed SaveMessage: %v", err)
				}
				items, err := store.RecentBySession(ctx, stateSession, 10)
				if err != nil || len(items) != 1 {
					t.Fatalf("find state source: items=%d err=%v", len(items), err)
				}
				stateSourceID = items[0].ID
			},
			call: func(remote *L1StoreClient) (any, error) {
				return nil, remote.UpdateMemoryState(ctx, stateSourceID, l1sqlite.MemoryStateCandidate)
			},
			assertEffect: func(t *testing.T, db *sql.DB) {
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_memory_event WHERE session_id = ? AND message = ? AND memory_state = 'candidate'`, stateSession, stateMessage, 1)
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.state_updated' AND session_id = ?`, stateSession, 1)
			},
		},
		{
			name: "promote_memory_to_namespace",
			setup: func(t *testing.T, store *l1sqlite.L1SQLiteStore) {
				if err := store.SaveMessage(ctx, promotionSession, promotionThread, 1, modulecore.ThreadKindUserConversation, "conv:"+string(promotionThread), domconv.NewMessage(domconv.SpeakerUser, promotedMessage, nil), l1sqlite.MemoryStateCandidate); err != nil {
					t.Fatalf("seed SaveMessage: %v", err)
				}
				items, err := store.RecentBySession(ctx, promotionSession, 10)
				if err != nil || len(items) != 1 {
					t.Fatalf("find promotion source: items=%d err=%v", len(items), err)
				}
				promotionSourceID = items[0].ID
			},
			call: func(remote *L1StoreClient) (any, error) {
				return remote.PromoteMemoryToNamespace(ctx, promotionSourceID, promotionNamespace, "receipt-test")
			},
			assertEffect: func(t *testing.T, db *sql.DB) {
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_memory_event WHERE namespace = ? AND message = ? AND memory_state = 'confirmed'`, promotionNamespace, promotedMessage, 1)
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.promoted' AND namespace = ?`, promotionNamespace, 1)
			},
		},
		{
			name: "save_recall_trace",
			call: func(remote *L1StoreClient) (any, error) { return nil, remote.SaveRecallTrace(ctx, trace) },
			assertEffect: func(t *testing.T, db *sql.DB) {
				assertL1SQLCount(t, db, `SELECT count(*) FROM recall_trace WHERE trace_id = ? AND status = 'completed'`, trace.TraceID, 1)
				assertL1SQLCount(t, db, `SELECT count(*) FROM recall_trace_item WHERE trace_id = ?`, trace.TraceID, 1)
				assertL1SQLCount(t, db, `SELECT count(*) FROM prompt_injection_event WHERE trace_id = ?`, trace.TraceID, 1)
				assertL1SQLCount(t, db, `SELECT count(*) FROM l1_event_log WHERE event_type = 'recall.trace' AND session_id = ?`, traceSession, 1)
			},
		},
	}

	for _, tt := range scenarios {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "l1.db")
			journalDir := filepath.Join(dir, "journal")
			opID := "l1-owner-recovery-" + tt.name
			first := openL1RestartHost(t, dbPath, journalDir)
			t.Cleanup(first.close)
			if tt.setup != nil {
				tt.setup(t, first.store)
			}
			first.client.opID = opID
			first.host.crashAfterCommitFor = opID
			if result, err := tt.call(first.remote); errorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("post-commit response loss result=%#v err=%v, want outcome_unknown", result, err)
			}

			first.close()
			second := openL1RestartHost(t, dbPath, journalDir)
			t.Cleanup(second.close)
			first.client.endpoint = second.server.URL + RPCPath
			first.client.httpClient = second.server.Client()
			if err := first.client.Handshake(ctx); err != nil {
				t.Fatalf("Handshake after restart: %v", err)
			}
			result, err := tt.call(first.remote)
			if err != nil {
				t.Fatalf("retry after SQLite owner restart: %v", err)
			}

			assertL1OwnerReceiptMatches(t, dbPath, opID, result)
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatalf("open SQLite effects: %v", err)
			}
			defer db.Close()
			tt.assertEffect(t, db)
		})
	}
}

func TestL1GroupOwnerReceiptCorruptionStaysOutcomeUnknown(t *testing.T) {
	for _, test := range []struct {
		name   string
		update string
		value  string
	}{
		{name: "malformed_result", update: `UPDATE conversation_l1_operation_receipt SET result_json = ? WHERE op_id = ?`, value: "{"},
		{name: "mismatched_payload_hash", update: `UPDATE conversation_l1_operation_receipt SET payload_sha256 = ? WHERE op_id = ?`, value: strings.Repeat("0", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "l1.db")
			journalDir := filepath.Join(dir, "journal")
			first := openL1RestartHost(t, dbPath, journalDir)
			defer first.close()
			opID := "l1-owner-corrupt-" + test.name
			first.client.opID = opID
			first.host.crashAfterCommitFor = opID
			call := func(remote *L1StoreClient) error {
				_, err := remote.AppendEvent(context.Background(), "test.corrupt_receipt", "kb:corrupt-receipt", "", "", 0, "", map[string]interface{}{"single_effect": true}, "test")
				return err
			}
			if err := call(first.remote); errorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("post-commit response loss error=%v, want outcome_unknown", err)
			}
			first.close()
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatalf("open owner SQLite for corruption: %v", err)
			}
			if _, err := db.Exec(test.update, test.value, opID); err != nil {
				_ = db.Close()
				t.Fatalf("corrupt owner receipt: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close corrupt receipt DB: %v", err)
			}
			second := openL1RestartHost(t, dbPath, journalDir)
			defer second.close()
			first.client.endpoint = second.server.URL + RPCPath
			first.client.httpClient = second.server.Client()
			if err := first.client.Handshake(context.Background()); err != nil {
				t.Fatalf("Handshake after owner restart: %v", err)
			}
			if err := call(first.remote); errorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("retry after corrupt owner receipt error=%v, want outcome_unknown", err)
			}
			assertDB, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatalf("open SQLite effects: %v", err)
			}
			defer assertDB.Close()
			assertL1SQLCount(t, assertDB, `SELECT count(*) FROM l1_event_log WHERE event_type = 'test.corrupt_receipt' AND namespace = 'kb:corrupt-receipt'`, 1)
		})
	}
}

func TestL1GroupOwnerReceiptValidTamperedResultStaysUnknown(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "l1.db")
	journalDir := filepath.Join(dir, "journal")
	first := openL1RestartHost(t, dbPath, journalDir)
	defer first.close()
	const opID = "l1-owner-valid-result-tamper"
	first.client.opID = opID
	first.host.crashAfterCommitFor = opID
	call := func(remote *L1StoreClient) (*l1sqlite.L1EventLogEntry, error) {
		return remote.AppendEvent(context.Background(), "test.valid_result_tamper", "kb:valid-result-tamper", "", "", 0, "", map[string]interface{}{"single_effect": true}, "test")
	}
	if result, err := call(first.remote); result != nil || errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("post-commit response loss result=%+v err=%v, want outcome_unknown", result, err)
	}
	first.close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open owner SQLite to mutate valid result: %v", err)
	}
	var raw string
	if err := db.QueryRow(`SELECT result_json FROM conversation_l1_operation_receipt WHERE op_id = ?`, opID).Scan(&raw); err != nil {
		_ = db.Close()
		t.Fatalf("read owner result: %v", err)
	}
	var receipt l1sqlite.L1EventLogEntry
	if err := json.Unmarshal([]byte(raw), &receipt); err != nil {
		_ = db.Close()
		t.Fatalf("decode owner result: %v", err)
	}
	receipt.Payload["single_effect"] = false
	mutated, err := json.Marshal(receipt)
	if err != nil {
		_ = db.Close()
		t.Fatalf("encode valid request-inconsistent result: %v", err)
	}
	if _, err := db.Exec(`UPDATE conversation_l1_operation_receipt SET result_json = ? WHERE op_id = ?`, string(mutated), opID); err != nil {
		_ = db.Close()
		t.Fatalf("mutate valid owner result: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close mutated owner DB: %v", err)
	}
	second := openL1RestartHost(t, dbPath, journalDir)
	defer second.close()
	first.client.endpoint = second.server.URL + RPCPath
	first.client.httpClient = second.server.Client()
	if err := first.client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake after restart: %v", err)
	}
	if result, err := call(first.remote); result != nil || errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry published corrupted receipt result=%+v err=%v, want outcome_unknown and no result", result, err)
	}
	effects, err := second.store.RecentEvents(context.Background(), "kb:valid-result-tamper", 10)
	if err != nil || len(effects) != 1 || effects[0].Payload["single_effect"] != true {
		t.Fatalf("durable event effects=%+v err=%v, want one original true event", effects, err)
	}
}

func TestL1GroupOwnerReceiptAffectedCountIntegrityStaysUnknown(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "l1.db")
	journalDir := filepath.Join(dir, "journal")
	first := openL1RestartHost(t, dbPath, journalDir)
	defer first.close()
	const cacheQuery = "receipt affected-count integrity"
	if _, err := first.store.SaveSearchCache(context.Background(), "web", cacheQuery, `{"seed":true}`, nil, time.Hour); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	const opID = "l1-owner-affected-count-integrity"
	first.client.opID = opID
	first.host.crashAfterCommitFor = opID
	call := func(remote *L1StoreClient) (int64, error) {
		return remote.InvalidateSearchCache(context.Background(), "web", cacheQuery)
	}
	if affected, err := call(first.remote); affected != 0 || errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("post-commit response loss affected=%d err=%v, want outcome_unknown", affected, err)
	}
	first.close()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open SQLite to alter result: %v", err)
	}
	var receipt l1sqlite.ConversationL1SearchCacheInvalidationResult
	var receiptJSON string
	if err := db.QueryRow(`SELECT result_json FROM conversation_l1_operation_receipt WHERE op_id = ?`, opID).Scan(&receiptJSON); err != nil {
		_ = db.Close()
		t.Fatalf("read invalidation receipt: %v", err)
	}
	if err := json.Unmarshal([]byte(receiptJSON), &receipt); err != nil || receipt.EventID == "" {
		_ = db.Close()
		t.Fatalf("decode invalidation receipt=%+v err=%v", receipt, err)
	}
	receipt.Affected = 0
	updateL1OwnerReceiptResultForTest(t, db, opID, receipt)
	if err := db.Close(); err != nil {
		t.Fatalf("close altered DB: %v", err)
	}
	second := openL1RestartHost(t, dbPath, journalDir)
	defer second.close()
	first.client.endpoint = second.server.URL + RPCPath
	first.client.httpClient = second.server.Client()
	if err := first.client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake after restart: %v", err)
	}
	if affected, err := call(first.remote); affected != 0 || errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("retry returned altered affected=%d err=%v, want outcome_unknown", affected, err)
	}
	if cache, err := second.store.GetFreshSearchCache(context.Background(), "web", cacheQuery, time.Now().UTC()); err != nil || cache != nil {
		t.Fatalf("original invalidation effect cache=%+v err=%v, want absent cache", cache, err)
	}
}

func TestL1InvalidationReceiptCannotBorrowAnotherOperationsEvent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "l1.db")
	journalDir := filepath.Join(dir, "journal")
	first := openL1RestartHost(t, dbPath, journalDir)
	defer first.close()
	const query = "invalidation event operation binding"
	if _, err := first.store.SaveSearchCache(context.Background(), "web", query, `{"seed":true}`, nil, time.Hour); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	const op1 = "l1-invalidation-binding-op1"
	const op2 = "l1-invalidation-binding-op2"
	call := func(remote *L1StoreClient) (int64, error) {
		return remote.InvalidateSearchCache(context.Background(), "web", query)
	}
	first.client.opID = op1
	first.host.crashAfterCommitFor = op1
	if affected, err := call(first.remote); affected != 0 || errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("op1 response loss affected=%d err=%v, want outcome_unknown", affected, err)
	}
	first.close()

	second := openL1RestartHost(t, dbPath, journalDir)
	first.client.endpoint = second.server.URL + RPCPath
	first.client.httpClient = second.server.Client()
	if err := first.client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake for op2 after restart: %v", err)
	}
	first.client.opID = op2
	if affected, err := call(first.remote); affected != 0 || err != nil {
		t.Fatalf("op2 repeat invalidation affected=%d err=%v, want zero committed result", affected, err)
	}
	second.close()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open SQLite to substitute op2 evidence: %v", err)
	}
	var op1Receipt, op2Receipt l1sqlite.ConversationL1SearchCacheInvalidationResult
	for _, item := range []struct {
		opID string
		out  *l1sqlite.ConversationL1SearchCacheInvalidationResult
	}{{op1, &op1Receipt}, {op2, &op2Receipt}} {
		var raw string
		if err := db.QueryRow(`SELECT result_json FROM conversation_l1_operation_receipt WHERE op_id = ?`, item.opID).Scan(&raw); err != nil {
			_ = db.Close()
			t.Fatalf("read %s owner result: %v", item.opID, err)
		}
		if err := json.Unmarshal([]byte(raw), item.out); err != nil {
			_ = db.Close()
			t.Fatalf("decode %s owner result: %v", item.opID, err)
		}
	}
	if op1Receipt.Affected != 1 || op2Receipt.Affected != 0 || op1Receipt.EventID == op2Receipt.EventID {
		_ = db.Close()
		t.Fatalf("op1 result=%+v op2 result=%+v, want affected 1/0 and distinct events", op1Receipt, op2Receipt)
	}
	// Preserve op1's receipt row identity but replace its result with op2's
	// count/event evidence, then recompute the existing receipt-result digest.
	updateL1OwnerReceiptResultForTest(t, db, op1, op2Receipt)
	if err := db.Close(); err != nil {
		t.Fatalf("close substituted receipt DB: %v", err)
	}

	third := openL1RestartHost(t, dbPath, journalDir)
	defer third.close()
	first.client.endpoint = third.server.URL + RPCPath
	first.client.httpClient = third.server.Client()
	if err := first.client.Handshake(context.Background()); err != nil {
		t.Fatalf("Handshake after owner and handler restart: %v", err)
	}
	first.client.opID = op1
	if affected, err := call(first.remote); affected != 0 || errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("op1 retry accepted substituted affected=%d err=%v, want outcome_unknown", affected, err)
	}
	checkDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open SQLite to verify no rerun: %v", err)
	}
	defer checkDB.Close()
	assertL1SQLCount(t, checkDB, `SELECT count(*) FROM l1_event_log WHERE event_type = 'search.cache_invalidated' AND json_extract(payload_json, '$.raw_query') = ?`, query, 2)
}

func TestL1GroupLegacyReceiptSchemaMigratesAndFailsClosed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "l1.db")
	legacyDB, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open legacy DB: %v", err)
	}
	_, err = legacyDB.Exec(`
CREATE TABLE conversation_l1_operation_receipt (
	op_id TEXT NOT NULL PRIMARY KEY CHECK(length(op_id) BETWEEN 1 AND 128),
	operation TEXT NOT NULL CHECK(operation IN (
		'save_message', 'save_search_cache', 'invalidate_search_cache', 'append_event',
		'update_memory_state', 'promote_memory_to_namespace', 'save_recall_trace'
	)),
	payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
	writer_generation INTEGER NOT NULL CHECK(writer_generation > 0),
	result_json TEXT NOT NULL CHECK(length(result_json) <= 8388608),
	created_at TIMESTAMP NOT NULL
)`)
	if err != nil {
		_ = legacyDB.Close()
		t.Fatalf("create legacy receipt table: %v", err)
	}
	legacyPayloadHash := sha256.Sum256([]byte(`{"legacy":true}`))
	const legacyOpID = "legacy-receipt-no-result-hash"
	if _, err := legacyDB.Exec(`INSERT INTO conversation_l1_operation_receipt(op_id, operation, payload_sha256, writer_generation, result_json, created_at) VALUES (?, 'append_event', ?, 1, 'null', ?)`, legacyOpID, hex.EncodeToString(legacyPayloadHash[:]), time.Now().UTC()); err != nil {
		_ = legacyDB.Close()
		t.Fatalf("insert legacy receipt: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("close legacy DB: %v", err)
	}
	store, err := l1sqlite.NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("open store and migrate legacy receipt schema: %v", err)
	}
	defer store.Close()
	identity := l1sqlite.ConversationL1OperationIdentity{
		OpID: legacyOpID, Operation: "append_event", PayloadSHA256: hex.EncodeToString(legacyPayloadHash[:]), WriterGeneration: 1,
	}
	if _, found, err := store.LookupConversationL1OperationReceipt(context.Background(), identity); err == nil || found {
		t.Fatalf("legacy receipt lookup=(found=%v, err=%v), want fail-closed unknown", found, err)
	}
	entry, err := store.AppendEvent(context.Background(), "test.legacy_local_api", "kb:legacy-local", "", "", 0, "", map[string]interface{}{"local_api": true}, "test")
	if err != nil || entry == nil || entry.EventType != "test.legacy_local_api" {
		t.Fatalf("ordinary local AppendEvent after migration=(%+v,%v), want success", entry, err)
	}
}

func TestL1GroupOwnerReceiptRequestMismatchedResultsStayUnknown(t *testing.T) {
	type scenario struct {
		name   string
		setup  func(*testing.T, *l1sqlite.L1SQLiteStore)
		call   func(*L1StoreClient) (any, error)
		mutate func(*testing.T, *sql.DB, string)
	}
	const cacheQuery = "receipt consistency cache query"
	const eventType = "test.receipt_consistency"
	const targetNamespace = "user:receipt-consistency"
	var promotionSourceID string
	promotionSession := string(modulecore.NewSessionID())
	promotionThread := modulecore.NewThreadID()

	scenarios := []scenario{
		{
			name: "save_search_cache",
			call: func(remote *L1StoreClient) (any, error) {
				return remote.SaveSearchCache(context.Background(), "web", cacheQuery, `{"requested":true}`, []string{"https://example.test/requested"}, time.Hour)
			},
			mutate: func(t *testing.T, db *sql.DB, opID string) {
				var raw string
				if err := db.QueryRow(`SELECT result_json FROM conversation_l1_operation_receipt WHERE op_id = ?`, opID).Scan(&raw); err != nil {
					t.Fatalf("read cache receipt: %v", err)
				}
				var entry l1sqlite.L1SearchCacheEntry
				if err := json.Unmarshal([]byte(raw), &entry); err != nil {
					t.Fatalf("decode cache receipt: %v", err)
				}
				entry.RawQuery = "unrequested query"
				updateL1OwnerReceiptResultForTest(t, db, opID, entry)
			},
		},
		{
			name: "append_event",
			call: func(remote *L1StoreClient) (any, error) {
				return remote.AppendEvent(context.Background(), eventType, "kb:receipt-consistency", "", "", 0, "", map[string]interface{}{"single_effect": true}, "test")
			},
			mutate: func(t *testing.T, db *sql.DB, opID string) {
				var raw string
				if err := db.QueryRow(`SELECT result_json FROM conversation_l1_operation_receipt WHERE op_id = ?`, opID).Scan(&raw); err != nil {
					t.Fatalf("read event receipt: %v", err)
				}
				var entry l1sqlite.L1EventLogEntry
				if err := json.Unmarshal([]byte(raw), &entry); err != nil {
					t.Fatalf("decode event receipt: %v", err)
				}
				entry.Payload["single_effect"] = false
				updateL1OwnerReceiptResultForTest(t, db, opID, entry)
			},
		},
		{
			name: "promote_memory_to_namespace",
			setup: func(t *testing.T, store *l1sqlite.L1SQLiteStore) {
				if err := store.SaveMessage(context.Background(), promotionSession, promotionThread, 1, modulecore.ThreadKindUserConversation, "conv:"+string(promotionThread), domconv.NewMessage(domconv.SpeakerMio, "receipt consistency source", nil), l1sqlite.MemoryStateCandidate); err != nil {
					t.Fatalf("seed promotion source: %v", err)
				}
				items, err := store.RecentBySession(context.Background(), promotionSession, 10)
				if err != nil || len(items) != 1 {
					t.Fatalf("read promotion source: count=%d err=%v", len(items), err)
				}
				promotionSourceID = items[0].ID
			},
			call: func(remote *L1StoreClient) (any, error) {
				return remote.PromoteMemoryToNamespace(context.Background(), promotionSourceID, targetNamespace, "requested-promoter")
			},
			mutate: func(t *testing.T, db *sql.DB, opID string) {
				var raw string
				if err := db.QueryRow(`SELECT result_json FROM conversation_l1_operation_receipt WHERE op_id = ?`, opID).Scan(&raw); err != nil {
					t.Fatalf("read promotion receipt: %v", err)
				}
				var event l1sqlite.L1MemoryEvent
				if err := json.Unmarshal([]byte(raw), &event); err != nil {
					t.Fatalf("decode promotion receipt: %v", err)
				}
				event.Meta["promoted_by"] = "unrequested-promoter"
				updateL1OwnerReceiptResultForTest(t, db, opID, event)
			},
		},
	}

	for _, tt := range scenarios {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			dbPath := filepath.Join(dir, "l1.db")
			journalDir := filepath.Join(dir, "journal")
			first := openL1RestartHost(t, dbPath, journalDir)
			defer first.close()
			if tt.setup != nil {
				tt.setup(t, first.store)
			}
			opID := "l1-owner-result-consistency-" + tt.name
			first.client.opID = opID
			first.host.crashAfterCommitFor = opID
			if result, err := tt.call(first.remote); !l1AnyResultIsNil(result) || errorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("post-commit response loss result=%#v err=%v, want outcome_unknown", result, err)
			}
			first.close()
			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatalf("open SQLite to mutate request-mismatched receipt: %v", err)
			}
			tt.mutate(t, db, opID)
			if err := db.Close(); err != nil {
				t.Fatalf("close mutated receipt DB: %v", err)
			}
			second := openL1RestartHost(t, dbPath, journalDir)
			defer second.close()
			first.client.endpoint = second.server.URL + RPCPath
			first.client.httpClient = second.server.Client()
			if err := first.client.Handshake(context.Background()); err != nil {
				t.Fatalf("Handshake after owner restart: %v", err)
			}
			if result, err := tt.call(first.remote); !l1AnyResultIsNil(result) || errorCode(err) != ErrorCodeOutcomeUnknown {
				t.Fatalf("retry published request-mismatched result=%#v err=%v, want outcome_unknown", result, err)
			}
		})
	}
}

func l1AnyResultIsNil(result any) bool {
	if result == nil {
		return true
	}
	value := reflect.ValueOf(result)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func updateL1OwnerReceiptResultForTest(t *testing.T, db *sql.DB, opID string, result any) {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode altered result: %v", err)
	}
	hash := sha256.Sum256(encoded)
	if _, err := db.Exec(`UPDATE conversation_l1_operation_receipt SET result_json = ?, result_sha256 = ? WHERE op_id = ?`, string(encoded), hex.EncodeToString(hash[:]), opID); err != nil {
		t.Fatalf("update altered result and matching integrity hash: %v", err)
	}
}

type l1PromotionCommitThenErrorArchive struct {
	*archivesqlite.ArchiveSQLiteStore
	fail  bool
	calls int
}

func (archive *l1PromotionCommitThenErrorArchive) ArchiveL1PromotionForOperation(ctx context.Context, identity l1sqlite.ConversationL1OperationIdentity, event l1sqlite.L1MemoryEvent) error {
	archive.calls++
	if err := archive.ArchiveSQLiteStore.ArchiveL1PromotionForOperation(ctx, identity, event); err != nil {
		return err
	}
	if archive.fail {
		archive.fail = false
		return errors.New("simulated lost archive commit response")
	}
	return nil
}

func TestL1GroupPromotionRecoversArchiveCommitGapAfterOwnerRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "l1.db")
	archivePath := filepath.Join(dir, "archive.db")
	journalDir := filepath.Join(dir, "journal")
	setup := openL1RestartHost(t, dbPath, journalDir)
	sessionID := string(modulecore.NewSessionID())
	threadID := modulecore.NewThreadID()
	if err := setup.store.SaveMessage(context.Background(), sessionID, threadID, 1, modulecore.ThreadKindUserConversation, "conv:"+string(threadID), domconv.NewMessage(domconv.SpeakerMio, "archive promotion source", nil), l1sqlite.MemoryStateCandidate); err != nil {
		setup.close()
		t.Fatalf("seed source memory: %v", err)
	}
	events, err := setup.store.RecentBySession(context.Background(), sessionID, 10)
	if err != nil || len(events) != 1 {
		setup.close()
		t.Fatalf("read source memory: count=%d err=%v", len(events), err)
	}
	sourceID := events[0].ID
	setup.close()

	archive1, err := archivesqlite.NewArchiveSQLiteStore(archivePath)
	if err != nil {
		t.Fatalf("open archive owner: %v", err)
	}
	uncertainArchive := &l1PromotionCommitThenErrorArchive{ArchiveSQLiteStore: archive1, fail: true}
	host1 := openL1RestartHostWithArchive(t, dbPath, journalDir, uncertainArchive)
	const opID = "l1-promotion-archive-commit-gap"
	originalGeneration := host1.client.Generation()
	host1.client.opID = opID
	if promoted, err := host1.remote.PromoteMemoryToNamespace(context.Background(), sourceID, "user:archive-boundary", "test"); errorCode(err) != ErrorCodeOutcomeUnknown || promoted != nil {
		t.Fatalf("lost archive response result=%+v error=%v", promoted, err)
	}
	host1.close()
	if err := archive1.Close(); err != nil {
		t.Fatal(err)
	}

	archive2, err := archivesqlite.NewArchiveSQLiteStore(archivePath)
	if err != nil {
		t.Fatalf("reopen archive owner: %v", err)
	}
	defer archive2.Close()
	host2 := openL1RestartHostWithArchive(t, dbPath, journalDir, archive2)
	defer host2.close()
	host2.client.opID = opID
	promoted, err := host2.remote.PromoteMemoryToNamespace(context.Background(), sourceID, "user:archive-boundary", "test")
	if err != nil || promoted == nil || promoted.Namespace != "user:archive-boundary" {
		t.Fatalf("recovered promotion result=%+v error=%v", promoted, err)
	}
	if uncertainArchive.calls != 1 {
		t.Fatalf("uncertain external archive effect calls=%d want 1", uncertainArchive.calls)
	}
	host2.client.opID = opID
	if _, err := host2.remote.PromoteMemoryToNamespace(context.Background(), sourceID, "user:changed-payload", "test"); errorCode(err) != ErrorCodeDuplicateConflict {
		t.Fatalf("same op changed payload error=%v want duplicate conflict", err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open SQLite after archive-boundary rejection: %v", err)
	}
	defer db.Close()
	assertL1SQLCount(t, db, `SELECT count(*) FROM l1_memory_event WHERE namespace = 'user:archive-boundary'`, 1)
	assertL1SQLCount(t, db, `SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.promoted' AND namespace = 'user:archive-boundary'`, 1)
	assertL1SQLCount(t, db, `SELECT count(*) FROM conversation_l1_operation_receipt WHERE operation = 'promote_memory_to_namespace'`, 1)
	assertL1SQLCount(t, db, `SELECT count(*) FROM conversation_l1_promotion_archive_outbox WHERE status = 'committed'`, 1)
	var outboxGeneration int64
	if err := db.QueryRow(`SELECT writer_generation FROM conversation_l1_promotion_archive_outbox WHERE op_id = ?`, opID).Scan(&outboxGeneration); err != nil {
		t.Fatal(err)
	}
	if outboxGeneration != originalGeneration || outboxGeneration == host2.client.Generation() {
		t.Fatalf("outbox generation=%d want original=%d not reopened=%d", outboxGeneration, originalGeneration, host2.client.Generation())
	}
	archived, found, err := archive2.FindUserMemoryArchive(context.Background(), "archive-boundary", promoted.ID)
	if err != nil || !found || archived.ID != promoted.ID {
		t.Fatalf("archived promotion=%+v found=%v err=%v", archived, found, err)
	}
	archiveDB, err := sql.Open("sqlite", archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveDB.Close()
	assertL1SQLCount(t, archiveDB, `SELECT count(*) FROM l1_memory_event_archive WHERE namespace = 'user:archive-boundary'`, 1)
	assertL1SQLCount(t, archiveDB, `SELECT count(*) FROM conversation_l1_promotion_archive_receipt WHERE op_id = ?`, opID, 1)
	var archiveGeneration int64
	if err := archiveDB.QueryRow(`SELECT writer_generation FROM conversation_l1_promotion_archive_receipt WHERE op_id = ?`, opID).Scan(&archiveGeneration); err != nil {
		t.Fatal(err)
	}
	if archiveGeneration != originalGeneration {
		t.Fatalf("archive receipt generation=%d want original=%d", archiveGeneration, originalGeneration)
	}
}

func TestL1GroupPromotionSubstitutedResultStaysOutcomeUnknownWithoutRerun(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "l1.db")
	archivePath := filepath.Join(dir, "archive.db")
	journalDir := filepath.Join(dir, "journal")
	setup := openL1RestartHost(t, dbPath, journalDir)
	sessionID := string(modulecore.NewSessionID())
	threadID := modulecore.NewThreadID()
	if err := setup.store.SaveMessage(context.Background(), sessionID, threadID, 1, modulecore.ThreadKindUserConversation, "conv:"+string(threadID), domconv.NewMessage(domconv.SpeakerMio, "substitution source", nil), l1sqlite.MemoryStateCandidate); err != nil {
		t.Fatal(err)
	}
	events, err := setup.store.RecentBySession(context.Background(), sessionID, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("source events=%+v err=%v", events, err)
	}
	setup.close()

	archive1, err := archivesqlite.NewArchiveSQLiteStore(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	host1 := openL1RestartHostWithArchive(t, dbPath, journalDir, archive1)
	const opID = "l1-promotion-result-substitution"
	host1.client.opID = opID
	host1.host.crashAfterCommitFor = opID
	if _, err := host1.remote.PromoteMemoryToNamespace(context.Background(), events[0].ID, "user:substitution", "test"); errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("commit-gap error=%v", err)
	}
	host1.close()
	if err := archive1.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var resultJSON string
	if err := db.QueryRow(`SELECT result_json FROM conversation_l1_operation_receipt WHERE op_id = ?`, opID).Scan(&resultJSON); err != nil {
		t.Fatal(err)
	}
	var substituted *l1sqlite.L1MemoryEvent
	if err := json.Unmarshal([]byte(resultJSON), &substituted); err != nil || substituted == nil {
		t.Fatalf("decode canonical result=%+v err=%v", substituted, err)
	}
	substituted.Message = "substituted result"
	updateL1OwnerReceiptResultForTest(t, db, opID, substituted)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	archive2, err := archivesqlite.NewArchiveSQLiteStore(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archive2.Close()
	host2 := openL1RestartHostWithArchive(t, dbPath, journalDir, archive2)
	defer host2.close()
	host2.client.opID = opID
	if _, err := host2.remote.PromoteMemoryToNamespace(context.Background(), events[0].ID, "user:substitution", "test"); errorCode(err) != ErrorCodeOutcomeUnknown {
		t.Fatalf("substituted result recovery error=%v want outcome_unknown", err)
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	assertL1SQLCount(t, db, `SELECT count(*) FROM l1_memory_event WHERE namespace = 'user:substitution'`, 1)
	assertL1SQLCount(t, db, `SELECT count(*) FROM l1_event_log WHERE event_type = 'memory.promoted' AND namespace = 'user:substitution'`, 1)
}

func assertL1SQLCount(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	var want int
	if len(args) == 0 {
		t.Fatalf("expected SQL count query arguments to include expected count")
	}
	want, args = args[len(args)-1].(int), args[:len(args)-1]
	var got int
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("query row count: %v", err)
	}
	if got != want {
		t.Fatalf("SQL count = %d, want %d for %s", got, want, query)
	}
}

func assertL1OwnerReceiptMatches(t *testing.T, dbPath, opID string, got any) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open SQLite owner receipt: %v", err)
	}
	defer db.Close()
	var raw string
	if err := db.QueryRow(`SELECT result_json FROM conversation_l1_operation_receipt WHERE op_id = ?`, opID).Scan(&raw); err != nil {
		t.Fatalf("read owner receipt result: %v", err)
	}
	var want any
	switch got.(type) {
	case nil:
		if strings.TrimSpace(raw) != "null" {
			t.Fatalf("void owner receipt result=%s, want null", raw)
		}
		return
	case *l1sqlite.L1SearchCacheEntry:
		var decoded *l1sqlite.L1SearchCacheEntry
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("decode search cache receipt result: %v", err)
		}
		want = decoded
	case int64:
		var decoded l1sqlite.ConversationL1SearchCacheInvalidationResult
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("decode affected receipt result: %v", err)
		}
		if decoded.EventID == "" {
			t.Fatal("affected receipt is missing its invalidation event binding")
		}
		want = decoded.Affected
	case *l1sqlite.L1EventLogEntry:
		var decoded *l1sqlite.L1EventLogEntry
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("decode event receipt result: %v", err)
		}
		want = decoded
	case *l1sqlite.L1MemoryEvent:
		var decoded *l1sqlite.L1MemoryEvent
		if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
			t.Fatalf("decode memory receipt result: %v", err)
		}
		want = decoded
	default:
		t.Fatalf("unexpected owner result type %T", got)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("recovered result %#v differs from committed owner receipt %#v", got, want)
	}
}
