package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type stubDCITraceLister struct {
	items []domaindci.SearchTrace
	limit int
}

func (s *stubDCITraceLister) ListRecent(limit int) ([]domaindci.SearchTrace, error) {
	s.limit = limit
	return s.items, nil
}

func TestHandleDCIRecent(t *testing.T) {
	traceID := modulecore.NewTraceID()
	actionID := modulecore.NewActionID()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubDCITraceLister{items: []domaindci.SearchTrace{{
		TraceID:            traceID,
		ActionID:           actionID,
		StartedAt:          now,
		EndedAt:            now.Add(time.Second),
		ActorAttribution:   domaindci.ActorAttributionAuthenticated,
		ActorKind:          "agent",
		ActorID:            "shiro",
		Mode:               "dci",
		UserQuery:          "DCI",
		Status:             "completed",
		FinalEvidenceCount: 0,
	}}}
	req := httptest.NewRequest(http.MethodGet, "/viewer/dci/recent?limit=7", nil)
	rec := httptest.NewRecorder()

	HandleDCIRecent(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if store.limit != 7 {
		t.Fatalf("limit = %d", store.limit)
	}
	var body struct {
		Items []domaindci.SearchTrace `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].TraceID != traceID || body.Items[0].ActionID != actionID {
		t.Fatalf("items = %#v", body.Items)
	}
	if strings.Contains(rec.Body.String(), `"event_id"`) || strings.Contains(rec.Body.String(), `"actor"`) {
		t.Fatalf("recent response contains retired search-level fields: %s", rec.Body.String())
	}
}

func TestHandleDCIRecentInvalidLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/dci/recent?limit=bad", nil)
	rec := httptest.NewRecorder()

	HandleDCIRecent(&stubDCITraceLister{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

type stubDCISearcher struct {
	calls       int
	query       string
	traceID     modulecore.TraceID
	actionID    modulecore.ActionID
	actorKind   string
	actorID     string
	idempotency string
	result      domaindci.SearchResult
	err         error
}

func (s *stubDCISearcher) SearchWithIdentity(_ context.Context, query string, traceID modulecore.TraceID, actionID modulecore.ActionID, actorKind, actorID, idempotencyKey string) (domaindci.SearchResult, error) {
	s.calls++
	s.query = query
	s.traceID = traceID
	s.actionID = actionID
	s.actorKind = actorKind
	s.actorID = actorID
	s.idempotency = idempotencyKey
	if s.err != nil {
		return domaindci.SearchResult{}, s.err
	}
	if s.result.Trace.TraceID == "" {
		s.result = validDCIViewerSearchResult(query, traceID, actionID, actorKind, actorID, idempotencyKey)
	}
	return s.result, nil
}

func validDCIViewerSearchResult(query string, traceID modulecore.TraceID, actionID modulecore.ActionID, actorKind, actorID, idempotencyKey string) domaindci.SearchResult {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	fileReadEventID := modulecore.NewEventID()
	evidenceEventID := modulecore.NewEventID()
	evidenceID := modulecore.NewEvidenceID()
	return domaindci.SearchResult{
		Pack: domaindci.EvidencePack{
			ActionID: actionID,
			Query:    query,
			Evidence: []domaindci.Evidence{{
				EvidenceID:       evidenceID,
				CreatedByEventID: evidenceEventID,
				FilePath:         "docs/spec.md",
				LineStart:        1,
				LineEnd:          1,
				Snippet:          "DCI evidence",
			}},
		},
		Trace: domaindci.SearchTrace{
			TraceID:          traceID,
			ActionID:         actionID,
			StartedAt:        now,
			EndedAt:          now.Add(time.Second),
			ActorAttribution: domaindci.ActorAttributionAuthenticated,
			ActorKind:        actorKind,
			ActorID:          actorID,
			IdempotencyKey:   idempotencyKey,
			Mode:             "dci",
			UserQuery:        query,
			Steps: []domaindci.SearchStep{{
				StepNo:    1,
				EventID:   fileReadEventID,
				EventType: "dci.file.read",
				Tool:      "file_read",
				FilePath:  "docs/spec.md",
				Status:    "ok",
				CreatedAt: now,
			}},
			FinalEvidenceCount: 1,
			Status:             "completed",
		},
	}
}

func ownerDCIRequest(body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/viewer/dci/search", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer owner-token")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	return req
}

func TestNewDCISearchHandlerAuthenticatesAndUsesCanonicalIdentity(t *testing.T) {
	searcher := &stubDCISearcher{}
	req := ownerDCIRequest(`{"query":" DCI "}`)
	rec := httptest.NewRecorder()

	NewDCISearchHandler(searcher, "ren", []byte("owner-token")).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if searcher.calls != 1 || searcher.query != "DCI" || searcher.actorKind != "user" || searcher.actorID != "ren" || searcher.idempotency == "" {
		t.Fatalf("search call = %#v", searcher)
	}
	if err := searcher.traceID.Validate(); err != nil {
		t.Fatalf("trace id: %v", err)
	}
	if err := searcher.actionID.Validate(); err != nil {
		t.Fatalf("action id: %v", err)
	}
	if searcher.idempotency == string(searcher.traceID) || searcher.idempotency == string(searcher.actionID) {
		t.Fatal("idempotency key must be separate from canonical IDs")
	}
	var result domaindci.SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if err := domaindci.ValidateSearchResult(result); err != nil {
		t.Fatalf("result validation: %v", err)
	}
	if result.Trace.TraceID != searcher.traceID || result.Trace.ActionID != searcher.actionID || result.Pack.ActionID != searcher.actionID || result.Trace.ActorKind != "user" || result.Trace.ActorID != "ren" {
		t.Fatalf("result identity = %#v", result)
	}
	if strings.Contains(rec.Body.String(), `"EventID"`) || strings.Contains(rec.Body.String(), `"Actor"`) {
		t.Fatalf("response contains legacy PascalCase DCI fields: %s", rec.Body.String())
	}
}

func TestNewDCISearchHandlerRejectsOwnerBoundaryViolations(t *testing.T) {
	tests := []struct {
		name    string
		request func() *http.Request
		userID  string
		token   []byte
		want    int
	}{
		{name: "remote hidden", request: func() *http.Request {
			req := ownerDCIRequest(`{"query":"DCI"}`)
			req.RemoteAddr = "192.0.2.10:1234"
			return req
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusNotFound},
		{name: "remote get hidden", request: func() *http.Request {
			req := ownerDCIRequest(`{"query":"DCI"}`)
			req.Method = http.MethodGet
			req.RemoteAddr = "192.0.2.10:1234"
			return req
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusNotFound},
		{name: "proxied loopback hidden", request: func() *http.Request {
			req := ownerDCIRequest(`{"query":"DCI"}`)
			req.Header.Set("X-Forwarded-For", "192.0.2.10")
			return req
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusNotFound},
		{name: "local get method not allowed", request: func() *http.Request {
			req := ownerDCIRequest(`{"query":"DCI"}`)
			req.Method = http.MethodGet
			return req
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusMethodNotAllowed},
		{name: "query parameter rejected", request: func() *http.Request {
			req := ownerDCIRequest(`{"query":"DCI"}`)
			req.URL.RawQuery = "unexpected=1"
			return req
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusBadRequest},
		{name: "missing principal", request: func() *http.Request {
			return ownerDCIRequest(`{"query":"DCI"}`)
		}, userID: "", token: nil, want: http.StatusServiceUnavailable},
		{name: "bad bearer", request: func() *http.Request {
			req := ownerDCIRequest(`{"query":"DCI"}`)
			req.Header.Set("Authorization", "Bearer wrong")
			return req
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusUnauthorized},
		{name: "wrong profile", request: func() *http.Request {
			req := ownerDCIRequest(`{"query":"DCI"}`)
			req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-diagnostics")
			return req
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusForbidden},
		{name: "unknown field", request: func() *http.Request {
			return ownerDCIRequest(`{"query":"DCI","extra":true}`)
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusBadRequest},
		{name: "duplicate field", request: func() *http.Request {
			return ownerDCIRequest(`{"query":"DCI","query":"other"}`)
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusBadRequest},
		{name: "trailing value", request: func() *http.Request {
			return ownerDCIRequest(`{"query":"DCI"}{}`)
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusBadRequest},
		{name: "wrong content type", request: func() *http.Request {
			req := ownerDCIRequest(`{"query":"DCI"}`)
			req.Header.Set("Content-Type", "text/plain")
			return req
		}, userID: "ren", token: []byte("owner-token"), want: http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			searcher := &stubDCISearcher{}
			rec := httptest.NewRecorder()
			NewDCISearchHandler(searcher, tt.userID, tt.token).ServeHTTP(rec, tt.request())
			if rec.Code != tt.want {
				t.Fatalf("status = %d body=%s, want %d", rec.Code, rec.Body.String(), tt.want)
			}
			if tt.name == "local get method not allowed" && rec.Header().Get("Allow") != http.MethodPost {
				t.Fatalf("Allow = %q, want %q", rec.Header().Get("Allow"), http.MethodPost)
			}
			if searcher.calls != 0 {
				t.Fatalf("search calls = %d", searcher.calls)
			}
		})
	}
}

func TestNewDCISearchHandlerRejectsMismatchedCanonicalResult(t *testing.T) {
	searcher := &stubDCISearcher{result: domaindci.SearchResult{Trace: domaindci.SearchTrace{
		TraceID:  modulecore.NewTraceID(),
		ActionID: modulecore.NewActionID(),
	}}}
	req := ownerDCIRequest(`{"query":"DCI"}`)
	rec := httptest.NewRecorder()

	NewDCISearchHandler(searcher, "ren", []byte("owner-token")).ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError || searcher.calls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", rec.Code, searcher.calls, rec.Body.String())
	}
}
