package chat

import "strings"

func NormalizeRouteName(route string) Route {
	switch strings.ToUpper(strings.TrimSpace(route)) {
	case "CHAT":
		return RouteChat
	case "CODE", "CODE1", "CODE2", "CODE3", "CODE4", "OPS", "PLAN", "ANALYZE":
		return RouteWorker
	case "STT":
		return RouteSTT
	case "TTS":
		return RouteTTS
	default:
		return RouteLLM
	}
}

func NormalizeRouteDecision(route string, reason string) RouteDecision {
	trimmedReason := strings.TrimSpace(reason)
	if trimmedReason == "" {
		trimmedReason = strings.TrimSpace(route)
	}
	return RouteDecision{
		Route:  NormalizeRouteName(route),
		Reason: trimmedReason,
	}
}
