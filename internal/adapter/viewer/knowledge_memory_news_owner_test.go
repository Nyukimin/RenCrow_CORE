package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

const knowledgeMemoryNewsOwnerTestToken = "knowledge-owner-token"

func TestKnowledgeMemoryNewsOwnerAccessFiltersPublicAndPrivateRows(t *testing.T) {
	store := &stubKnowledgeMemoryStore{news: []domainkm.NewsKnowledgeItem{
		{ItemID: "public-news", Source: "wire", Topic: "public", Status: "candidate", Visibility: "public", CreatedAt: time.Now()},
		{ItemID: "owner-news", UserID: "owner-1", Source: "gmail", Topic: "owner", Status: "reviewed", Visibility: "private", CreatedAt: time.Now()},
		{ItemID: "other-news", UserID: "owner-2", Source: "gmail", Topic: "other", Status: "reviewed", Visibility: "private", CreatedAt: time.Now()},
		{ItemID: "hidden-news", UserID: "", Source: "wire", Topic: "hidden", Status: "candidate", Visibility: "private", CreatedAt: time.Now()},
	}}
	handler := WithKnowledgeMemoryNewsOwnerAccess(HandleKnowledgeMemoryStatus(store), "owner-1", []byte(knowledgeMemoryNewsOwnerTestToken))

	publicResponse := serveKnowledgeMemoryNewsOwnerRequest(handler, http.MethodGet, "/viewer/knowledge-memory", "", "", "")
	if publicResponse.Code != http.StatusOK {
		t.Fatalf("public status = %d, body=%s", publicResponse.Code, publicResponse.Body.String())
	}
	var publicBody struct {
		News []domainkm.NewsKnowledgeItem `json:"news_knowledge"`
	}
	if err := json.Unmarshal(publicResponse.Body.Bytes(), &publicBody); err != nil {
		t.Fatal(err)
	}
	if len(publicBody.News) != 1 || publicBody.News[0].ItemID != "public-news" {
		t.Fatalf("public news = %#v", publicBody.News)
	}

	ownerResponse := serveKnowledgeMemoryNewsOwnerRequest(handler, http.MethodGet, "/viewer/knowledge-memory", knowledgeMemoryNewsOwnerTestToken, "cmd-diagnostics", "127.0.0.1:4171")
	if ownerResponse.Code != http.StatusOK {
		t.Fatalf("owner status = %d, body=%s", ownerResponse.Code, ownerResponse.Body.String())
	}
	var ownerBody struct {
		News []domainkm.NewsKnowledgeItem `json:"news_knowledge"`
	}
	if err := json.Unmarshal(ownerResponse.Body.Bytes(), &ownerBody); err != nil {
		t.Fatal(err)
	}
	if len(ownerBody.News) != 2 || ownerBody.News[0].ItemID != "public-news" || ownerBody.News[1].ItemID != "owner-news" {
		t.Fatalf("owner news = %#v", ownerBody.News)
	}
	if ownerResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", ownerResponse.Header().Get("Cache-Control"))
	}
}

func TestKnowledgeMemoryNewsOwnerAccessProtectsDetailAndReview(t *testing.T) {
	now := time.Now()
	store := &stubKnowledgeMemoryStore{news: []domainkm.NewsKnowledgeItem{
		{ItemID: "owner-news", UserID: "owner-1", Source: "gmail", Topic: "owner", Status: "candidate", Visibility: "private", CreatedAt: now},
		{ItemID: "other-news", UserID: "owner-2", Source: "gmail", Topic: "other", Status: "candidate", Visibility: "private", CreatedAt: now},
		{ItemID: "public-news", Source: "wire", Topic: "public", Status: "candidate", Visibility: "public", CreatedAt: now},
	}}
	statusHandler := WithKnowledgeMemoryNewsOwnerAccess(HandleKnowledgeMemoryStatus(store), "owner-1", []byte(knowledgeMemoryNewsOwnerTestToken))
	reviewHandler := WithKnowledgeMemoryNewsOwnerAccess(HandleKnowledgeMemoryReview(store), "owner-1", []byte(knowledgeMemoryNewsOwnerTestToken))

	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{name: "public cannot read private detail", path: "/viewer/knowledge-memory?detail_type=news_knowledge&id=owner-news", want: http.StatusNotFound},
		{name: "wrong owner cannot read private detail", path: "/viewer/knowledge-memory?detail_type=news_knowledge&id=owner-news", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response *httptest.ResponseRecorder
			if tc.name == "wrong owner cannot read private detail" {
				wrongOwner := WithKnowledgeMemoryNewsOwnerAccess(HandleKnowledgeMemoryStatus(store), "owner-2", []byte(knowledgeMemoryNewsOwnerTestToken))
				response = serveKnowledgeMemoryNewsOwnerRequest(wrongOwner, http.MethodGet, tc.path, knowledgeMemoryNewsOwnerTestToken, "cmd-diagnostics", "127.0.0.1:4171")
			} else {
				response = serveKnowledgeMemoryNewsOwnerRequest(statusHandler, http.MethodGet, tc.path, "", "", "")
			}
			if response.Code != tc.want {
				t.Fatalf("status = %d, want %d, body=%s", response.Code, tc.want, response.Body.String())
			}
		})
	}

	detail := serveKnowledgeMemoryNewsOwnerRequest(statusHandler, http.MethodGet, "/viewer/knowledge-memory?detail_type=news_knowledge&id=owner-news", knowledgeMemoryNewsOwnerTestToken, "cmd-diagnostics", "127.0.0.1:4171")
	if detail.Code != http.StatusOK {
		t.Fatalf("owner detail status = %d, body=%s", detail.Code, detail.Body.String())
	}

	reviewBody := `{"detail_type":"news_knowledge","id":"owner-news","review_status":"adopted","reviewed_by":"spoofed-owner"}`
	review := serveKnowledgeMemoryNewsOwnerRequest(reviewHandler, http.MethodPost, "/viewer/knowledge-memory/review", reviewBody, knowledgeMemoryNewsOwnerTestToken, "cmd-control", "127.0.0.1:4171")
	if review.Code != http.StatusCreated {
		t.Fatalf("owner review status = %d, body=%s", review.Code, review.Body.String())
	}
	if got := store.news[len(store.news)-1].Status; got != "reviewed" {
		t.Fatalf("reviewed status = %q", got)
	}
	var reviewResult struct {
		ReviewedBy string `json:"reviewed_by"`
		ActorID    string `json:"actor_id"`
		RequestID  string `json:"request_id"`
	}
	if err := json.Unmarshal(review.Body.Bytes(), &reviewResult); err != nil {
		t.Fatal(err)
	}
	if reviewResult.ReviewedBy != "owner-1" || reviewResult.ActorID != "owner-1" || reviewResult.RequestID == "" {
		t.Fatalf("owner review receipt = %#v", reviewResult)
	}

	otherReview := serveKnowledgeMemoryNewsOwnerRequest(reviewHandler, http.MethodPost, "/viewer/knowledge-memory/review", `{"detail_type":"news_knowledge","id":"other-news","review_status":"adopted"}`, knowledgeMemoryNewsOwnerTestToken, "cmd-control", "127.0.0.1:4171")
	if otherReview.Code != http.StatusBadRequest {
		t.Fatalf("other-owner review status = %d, want %d, body=%s", otherReview.Code, http.StatusBadRequest, otherReview.Body.String())
	}
}

func TestKnowledgeMemoryNewsOwnerAccessValidatesPrivateCreateScope(t *testing.T) {
	tests := []struct {
		name       string
		userID     string
		visibility string
		auth       string
		profile    string
		remote     string
		want       int
	}{
		{name: "public without auth", userID: "", visibility: "public", want: http.StatusCreated},
		{name: "private without auth", userID: "owner-1", visibility: "private", want: http.StatusForbidden},
		{name: "private owner", userID: "owner-1", visibility: "private", auth: knowledgeMemoryNewsOwnerTestToken, profile: "cmd-control", remote: "127.0.0.1:4171", want: http.StatusCreated},
		{name: "private spoofed owner", userID: "owner-2", visibility: "private", auth: knowledgeMemoryNewsOwnerTestToken, profile: "cmd-control", remote: "127.0.0.1:4171", want: http.StatusForbidden},
		{name: "remote owner", userID: "owner-1", visibility: "private", auth: knowledgeMemoryNewsOwnerTestToken, profile: "cmd-control", remote: "203.0.113.8:4171", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &stubKnowledgeMemoryStore{}
			handler := WithKnowledgeMemoryNewsOwnerAccess(HandleNewsKnowledgeCreate(store), "owner-1", []byte(knowledgeMemoryNewsOwnerTestToken))
			body := `{"item_id":"` + tt.name + `","user_id":"` + tt.userID + `","source":"gmail","topic":"topic","visibility":"` + tt.visibility + `","status":"candidate"}`
			response := serveKnowledgeMemoryNewsOwnerRequest(handler, http.MethodPost, "/viewer/knowledge-memory/news-knowledge", body, tt.auth, tt.profile, tt.remote)
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d, body=%s", response.Code, tt.want, response.Body.String())
			}
			if tt.want == http.StatusCreated && len(store.news) != 1 {
				t.Fatalf("saved news = %#v", store.news)
			}
			if tt.want != http.StatusCreated && len(store.news) != 0 {
				t.Fatalf("rejected news was saved = %#v", store.news)
			}
		})
	}
}

func TestKnowledgeMemoryNewsOwnerAccessRejectsAuthWhenTokenUnavailable(t *testing.T) {
	store := &stubKnowledgeMemoryStore{}
	handler := WithKnowledgeMemoryNewsOwnerAccess(HandleKnowledgeMemoryStatus(store), "owner-1", nil)
	response := serveKnowledgeMemoryNewsOwnerRequest(handler, http.MethodGet, "/viewer/knowledge-memory", "missing-token", "cmd-diagnostics", "127.0.0.1:4171")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

type failingNewsKnowledgeStore struct {
	*stubKnowledgeMemoryStore
}

func (s failingNewsKnowledgeStore) ListNewsKnowledgeItems(context.Context, int) ([]domainkm.NewsKnowledgeItem, error) {
	return nil, errors.New("news read failed")
}

func TestKnowledgeMemoryNewsOwnerAccessDoesNotHideNewsReadFailure(t *testing.T) {
	store := failingNewsKnowledgeStore{stubKnowledgeMemoryStore: &stubKnowledgeMemoryStore{}}
	handler := WithKnowledgeMemoryNewsOwnerAccess(HandleKnowledgeMemoryStatus(store), "owner-1", []byte(knowledgeMemoryNewsOwnerTestToken))
	response := serveKnowledgeMemoryNewsOwnerRequest(handler, http.MethodGet, "/viewer/knowledge-memory", "", "", "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d, body=%s", response.Code, http.StatusServiceUnavailable, response.Body.String())
	}
}

func serveKnowledgeMemoryNewsOwnerRequest(handler http.HandlerFunc, method, path, body string, args ...string) *httptest.ResponseRecorder {
	// The optional argument layout is auth, profile, remote for GET calls and
	// body, auth, profile, remote for POST calls.
	auth, profile, remote := "", "", ""
	input := bytes.NewReader(nil)
	if method == http.MethodPost {
		if len(args) >= 3 {
			auth, profile, remote = args[0], args[1], args[2]
		}
		input = bytes.NewReader([]byte(body))
	} else if len(args) >= 2 {
		auth, profile, remote = body, args[0], args[1]
	}
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, input)
	if remote != "" {
		req.RemoteAddr = remote
	}
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", profile)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}
