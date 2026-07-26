package revenue

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

type Routes struct {
	Status                    http.HandlerFunc
	MarketResearch            http.HandlerFunc
	SNSPosts                  http.HandlerFunc
	Products                  http.HandlerFunc
	CustomerVoices            http.HandlerFunc
	Events                    http.HandlerFunc
	PolicyDecisions           http.HandlerFunc
	DailyRoutine              http.HandlerFunc
	ChannelDrafts             http.HandlerFunc
	ExternalSend              http.HandlerFunc
	Opportunities             http.HandlerFunc
	EconomicTasks             http.HandlerFunc
	Deliveries                http.HandlerFunc
	Reflections               http.HandlerFunc
	ReflectionFromEvent       http.HandlerFunc
	OpportunityWorkstreamGoal http.HandlerFunc
}

func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/revenue", routes.Status)
	registerRoute(mux, "/viewer/revenue/market-research", routes.MarketResearch)
	registerRoute(mux, "/viewer/revenue/sns-posts", routes.SNSPosts)
	registerRoute(mux, "/viewer/revenue/products", routes.Products)
	registerRoute(mux, "/viewer/revenue/customer-voices", routes.CustomerVoices)
	registerRoute(mux, "/viewer/revenue/events", routes.Events)
	registerRoute(mux, "/viewer/revenue/policy-decisions", routes.PolicyDecisions)
	registerRoute(mux, "/viewer/revenue/daily-routine", routes.DailyRoutine)
	registerRoute(mux, "/viewer/revenue/channel-drafts", routes.ChannelDrafts)
	registerRoute(mux, "/viewer/revenue/channel-drafts/external-send-apply", routes.ExternalSend)
	registerRoute(mux, "/viewer/revenue/opportunities", routes.Opportunities)
	registerRoute(mux, "/viewer/revenue/economic-tasks", routes.EconomicTasks)
	registerRoute(mux, "/viewer/revenue/deliveries", routes.Deliveries)
	registerRoute(mux, "/viewer/revenue/economic-reflections", routes.Reflections)
	registerRoute(mux, "/viewer/revenue/economic-reflections/from-revenue-event", routes.ReflectionFromEvent)
	registerRoute(mux, "/viewer/revenue/opportunities/workstream-goal", routes.OpportunityWorkstreamGoal)
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux != nil && pattern != "" && handler != nil {
		mux.HandleFunc(pattern, handler)
	}
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}
