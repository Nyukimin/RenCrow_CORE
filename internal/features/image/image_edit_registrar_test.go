package image

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesIncludesCanonicalImageEditPath(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Routes{
		EditOptions: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}),
		Edit: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	})

	options := httptest.NewRecorder()
	mux.ServeHTTP(options, httptest.NewRequest(http.MethodGet, "/viewer/image/edit/options", nil))
	if options.Code != http.StatusNoContent {
		t.Fatalf("GET /viewer/image/edit/options status=%d body=%q", options.Code, options.Body.String())
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/viewer/image/edit", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("POST /viewer/image/edit status=%d body=%q", response.Code, response.Body.String())
	}
}
