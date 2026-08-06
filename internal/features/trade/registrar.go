package trade

import "net/http"

type Routes struct {
	Status http.HandlerFunc
}

func RegisterRoutes(mux *http.ServeMux, routes Routes) {
	if mux == nil || routes.Status == nil {
		return
	}
	mux.HandleFunc("/viewer/trade/status", routes.Status)
}
