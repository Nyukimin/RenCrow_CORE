package viewer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type pagedUserMemoryStoreStub struct {
	userMemoryStoreStub
	query  string
	offset int
}

func (s *pagedUserMemoryStoreStub) ListUserMemoriesPage(_ context.Context, userID, state string, includeInactive bool, query string, limit, offset int) ([]domainmemory.UserMemory, bool, error) {
	s.listUserID = userID
	s.listState = state
	s.listInactive = includeInactive
	s.listLimit = limit
	s.query = query
	s.offset = offset
	return append([]domainmemory.UserMemory(nil), s.items...), true, nil
}

func TestHandleUserMemoryListSupportsSearchStateAndPagination(t *testing.T) {
	store := &pagedUserMemoryStoreStub{
		userMemoryStoreStub: userMemoryStoreStub{items: []domainmemory.UserMemory{{ID: "mem-51"}}},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/user?user_id=ren&state=candidate&q=GPU&limit=50&offset=50", nil)
	rec := httptest.NewRecorder()

	HandleUserMemory(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.listUserID != "ren" || store.listState != "candidate" || store.query != "GPU" || store.listLimit != 50 || store.offset != 50 {
		t.Fatalf("unexpected page args: %+v", store)
	}
	for _, want := range []string{`"total":null`, `"offset":50`, `"limit":50`, `"has_more":true`} {
		if body := rec.Body.String(); !strings.Contains(body, want) {
			t.Fatalf("response missing %s: %s", want, body)
		}
	}
}

func TestHandleUserMemoryListRejectsInvalidOffset(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/user?offset=-1", nil)
	rec := httptest.NewRecorder()
	HandleUserMemory(&userMemoryStoreStub{})(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUserMemoryListRejectsSearchShorterThanThreeRunes(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/user?q=GP", nil)
	rec := httptest.NewRecorder()
	HandleUserMemory(&userMemoryStoreStub{})(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
