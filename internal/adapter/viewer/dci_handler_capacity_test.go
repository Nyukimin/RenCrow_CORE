package viewer

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
)

// A refused DCI search admission must not leave a Run-less queued Task behind.
func TestNewDCISearchHandlerLeavesNothingWhenExecutionCapacityIsUnavailable(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)
	searcher := &stubDCISearcher{}
	rec := httptest.NewRecorder()

	NewDCISearchHandler(searcher, saturated.Manager, "ren", "shiro", []byte("owner-token")).
		ServeHTTP(rec, ownerDCIRequest(`{"query":"DCI"}`))

	if rec.Code == http.StatusOK || searcher.calls != 0 {
		t.Fatalf("status=%d searches=%d, want a refusal before any search", rec.Code, searcher.calls)
	}
	saturated.AssertNothingPersisted(t)
}
