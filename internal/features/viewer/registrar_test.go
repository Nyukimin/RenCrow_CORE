package viewer

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterBaseRoutesRegistersViewerBasePaths(t *testing.T) {
	mux := http.NewServeMux()
	RegisterBaseRoutes(mux, Dependencies{Base: BaseRoutes{
		Page:                        statusHandler(http.StatusOK),
		XBookmarksPage:              statusHandler(http.StatusPartialContent),
		Asset:                       statusHandler(http.StatusCreated),
		RuntimeConfig:               statusHandler(http.StatusAccepted),
		Events:                      statusHandler(http.StatusNoContent),
		ConversationArchiveDatabase: statusHandler(http.StatusNonAuthoritativeInfo),
		GlossaryDatabase:            statusHandler(http.StatusMultiStatus),
		ToolRegistryDatabase:        statusHandler(http.StatusAlreadyReported),
		DataCapabilityCatalog:       statusHandler(http.StatusIMUsed),
	}})

	tests := []struct {
		path string
		want int
	}{
		{path: "/viewer", want: http.StatusOK},
		{path: "/viewer/x-bookmarks", want: http.StatusPartialContent},
		{path: "/viewer/assets/js/viewer.js", want: http.StatusCreated},
		{path: "/viewer/runtime-config", want: http.StatusAccepted},
		{path: "/viewer/events", want: http.StatusNoContent},
		{path: "/viewer/databases/conversation-archive", want: http.StatusNonAuthoritativeInfo},
		{path: "/viewer/databases/glossary", want: http.StatusMultiStatus},
		{path: "/viewer/databases/tool-registry", want: http.StatusAlreadyReported},
		{path: "/viewer/databases/catalog", want: http.StatusIMUsed},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d", rec.Code, tt.want)
			}
		})
	}
}

func TestRegisterBaseRoutesSkipsNilHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterBaseRoutes(mux, Dependencies{})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func statusHandler(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}
}
