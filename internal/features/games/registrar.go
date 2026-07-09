package games

import "net/http"

// Dependencies groups game bridge dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Routes Routes
}

// Routes groups RenCrow_GAMES bridge route handlers.
//
// Handler implementations stay in adapter packages; this registrar owns only
// the /viewer/games route boundary.
type Routes struct {
	Status        http.HandlerFunc
	Decision      http.HandlerFunc
	Result        http.HandlerFunc
	Sessions      http.HandlerFunc
	Events        http.HandlerFunc
	ObserverPage  http.HandlerFunc
	ObserverProxy http.HandlerFunc
}

// RegisterRoutes reserves the RenCrow_GAMES bridge route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/games/status", routes.Status)
	registerRoute(mux, "/viewer/games/decision", routes.Decision)
	registerRoute(mux, "/viewer/games/result", routes.Result)
	registerRoute(mux, "/viewer/games/sessions", routes.Sessions)
	registerRoute(mux, "/viewer/games/events", routes.Events)
	registerRoute(mux, "/viewer/games/observer", routes.ObserverPage)
	registerRoute(mux, "/viewer/games/observer-api", routes.ObserverProxy)
	registerRoute(mux, "/viewer/games/observer-api/", routes.ObserverProxy)
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}
