package avatar

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRegisterRoutesOwnsCharacterRuntime(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Dependencies{Routes: Routes{
		CharacterRuntime: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusCreated) },
	}})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/character-runtime", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusCreated)
	}
}
