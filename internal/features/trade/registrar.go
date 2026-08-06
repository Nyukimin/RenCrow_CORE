package trade

import "net/http"

type Routes struct {
	Status         http.HandlerFunc
	PolicyEvaluate http.HandlerFunc
}

func RegisterRoutes(mux *http.ServeMux, routes Routes) {
	if mux == nil {
		return
	}
	if routes.Status != nil {
		mux.HandleFunc("/viewer/trade/status", routes.Status)
	}
	if routes.PolicyEvaluate != nil {
		mux.HandleFunc("/viewer/trade/policy/evaluate", routes.PolicyEvaluate)
	}
}
