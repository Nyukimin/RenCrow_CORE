package trade

import "net/http"

type Routes struct {
	Status              http.HandlerFunc
	PolicyEvaluate      http.HandlerFunc
	RiskPreview         http.HandlerFunc
	SimulationCommit    http.HandlerFunc
	ShadowObservation   http.HandlerFunc
	ShadowOutcome       http.HandlerFunc
	ShadowOutcomeReport http.HandlerFunc
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
	if routes.RiskPreview != nil {
		mux.HandleFunc("/viewer/trade/risk-preview", routes.RiskPreview)
	}
	if routes.SimulationCommit != nil {
		mux.HandleFunc("/viewer/trade/simulation-commit", routes.SimulationCommit)
	}
	if routes.ShadowObservation != nil {
		mux.HandleFunc("/viewer/trade/shadow/observations", routes.ShadowObservation)
	}
	if routes.ShadowOutcome != nil {
		mux.HandleFunc("/viewer/trade/shadow/outcomes", routes.ShadowOutcome)
	}
	if routes.ShadowOutcomeReport != nil {
		mux.HandleFunc("/viewer/trade/shadow/outcomes/report", routes.ShadowOutcomeReport)
	}
}
