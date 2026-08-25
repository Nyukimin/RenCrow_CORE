package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleLive2DCharacterServesEmbeddedHTMLIndependentOfWorkingDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, characterID := range []string{"mio", "shiro"} {
		t.Run(characterID, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/viewer/live2d/character?character_id="+characterID, nil)
			w := httptest.NewRecorder()

			HandleLive2DCharacter(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("HandleLive2DCharacter(%s) status = %d, want %d: %s", characterID, w.Code, http.StatusOK, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), "<!DOCTYPE html>") {
				t.Fatalf("HandleLive2DCharacter(%s) should return embedded HTML", characterID)
			}
		})
	}
}

func TestHandleLive2DCharacter(t *testing.T) {
	t.Skip("Skipping Live2D character test - requires large HTML files")
}

func TestHandleLive2DCharacterEmbed(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/live2d/embed?character_id=mio&emotion=happy", nil)
	w := httptest.NewRecorder()

	HandleLive2DCharacterEmbed(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleLive2DCharacterEmbed() status = %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if !strings.Contains(body, "<!DOCTYPE html>") {
		t.Error("HandleLive2DCharacterEmbed() should return HTML")
	}
	if !strings.Contains(body, "happy") {
		t.Error("HandleLive2DCharacterEmbed() should include emotion")
	}
	if strings.Contains(body, "mode=") {
		t.Error("HandleLive2DCharacterEmbed() should not emit a Viewer mode")
	}
}

func TestHandleLive2DCharacterEmbedFitsMioToFrame(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/live2d/character?character_id=mio&emotion=normal&hide_ui=true", nil)
	w := httptest.NewRecorder()

	HandleLive2DCharacter(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("HandleLive2DCharacter() status = %d, want %d", w.Code, http.StatusOK)
	}
	body := w.Body.String()
	for _, want := range []string{
		"--mio-fit-scale: 1.62",
		"transform-origin: center bottom !important",
		"object-position: center bottom !important",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("embed body missing %q", want)
		}
	}
}
