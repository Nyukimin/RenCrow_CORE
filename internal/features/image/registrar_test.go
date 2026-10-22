package image

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesIncludesCanonicalImageOptionsPath(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Routes{
		Options: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/image/options", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("GET /viewer/image/options status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestRegisterRoutesIncludesVideoOptionsPath(t *testing.T) {
	mux := http.NewServeMux()
	called := false
	RegisterRoutes(mux, Routes{
		VideoOptions: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if r.Method != http.MethodGet {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}),
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/image/video/options?mode=i2v", nil))
	if rec.Code != http.StatusNoContent || !called {
		t.Fatalf("GET video options status=%d called=%t body=%q", rec.Code, called, rec.Body.String())
	}
}
