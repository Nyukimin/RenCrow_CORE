package viewer

import (
	"net/http"
)

func HandlePage(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedPage(w, "viewer.html", "viewer page not found")
}

// HandleXBookmarksPage serves the dedicated read-only X Bookmark workbench.
func HandleXBookmarksPage(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedPage(w, "x_bookmarks.html", "X Bookmark page not found")
}

func serveEmbeddedPage(w http.ResponseWriter, name, errorMessage string) {
	data, err := viewerFS.ReadFile(name)
	if err != nil {
		http.Error(w, errorMessage, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// MessageHandler processes a user message from the viewer.
