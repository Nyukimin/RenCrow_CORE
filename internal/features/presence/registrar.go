package presence

import (
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups PORTAL surface-presence Viewer route handlers supplied by cmd/rencrow.
// Chat and IdleChat are sibling surfaces; this feature owns the shared endpoint
// `/viewer/surface-presence` so that either surface can rely on it independently of
// the other surface's enablement state.
type Routes struct {
	SurfacePresence http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/surface-presence", routes.SurfacePresence)
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}
