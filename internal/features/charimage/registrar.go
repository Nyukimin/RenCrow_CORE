package charimage

import "net/http"

type Routes struct {
	Characters http.HandlerFunc
	Compose    http.HandlerFunc
	Generate   http.HandlerFunc
	History    http.HandlerFunc
}

func RegisterRoutes(mux *http.ServeMux, routes Routes) {
	if mux == nil {
		return
	}
	if routes.Characters != nil {
		mux.HandleFunc("/viewer/charimage/characters", routes.Characters)
	}
	if routes.Compose != nil {
		mux.HandleFunc("/viewer/charimage/compose", routes.Compose)
	}
	if routes.Generate != nil {
		mux.HandleFunc("/viewer/charimage/generate", routes.Generate)
	}
	if routes.History != nil {
		mux.HandleFunc("/viewer/charimage/history", routes.History)
	}
}
