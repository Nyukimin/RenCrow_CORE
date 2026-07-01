package viewer

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed viewer.html assets
var viewerFS embed.FS

// HandleLogo serves the RenCrow logo image.

func HandleLogo(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedAsset(w, r, "assets/images/rencrow-logo.png")
}

// HandleMioLipSyncClosed serves Mio closed-mouth SVG.

func HandleMioLipSyncClosed(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedAsset(w, r, "assets/images/mio-lipsync-closed.svg")
}

// HandleMioLipSyncOpen serves Mio open-mouth SVG.

func HandleMioLipSyncOpen(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedAsset(w, r, "assets/images/mio-lipsync-open.svg")
}

// HandleMioPortrait serves the full-body Mio portrait used by Lab mode.

func HandleMioPortrait(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedAsset(w, r, "assets/images/mio/mio_basic.png")
}

// HandleShiroPortrait serves the transparent Shiro portrait used by Lab mode.

func HandleShiroPortrait(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedAsset(w, r, "assets/images/shiro/parts/expressions/shiro_normal.png")
}

// HandleShiroLipSyncClosed serves Shiro closed-mouth SVG.

func HandleShiroLipSyncClosed(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedAsset(w, r, "assets/images/shiro-lipsync-closed.svg")
}

// HandleShiroLipSyncOpen serves Shiro open-mouth SVG.

func HandleShiroLipSyncOpen(w http.ResponseWriter, r *http.Request) {
	serveEmbeddedAsset(w, r, "assets/images/shiro-lipsync-open.svg")
}

// HandleAsset serves modular Viewer CSS, JS, and image assets.

func HandleAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/viewer/assets/")
	if name == "" || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		http.NotFound(w, r)
		return
	}
	serveEmbeddedAsset(w, r, "assets/"+name)
}

func serveEmbeddedAsset(w http.ResponseWriter, r *http.Request, name string) {
	if strings.HasSuffix(name, ".js") || strings.HasSuffix(name, ".css") {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	http.ServeFileFS(w, r, viewerFS, name)
}

// HandleSSE streams orchestrator events to the client via Server-Sent Events.
