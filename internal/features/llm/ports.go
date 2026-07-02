package llm

import "net/http"

// Ports groups external capabilities used by the llm feature as migration progresses.
type Ports struct{}

// LLMOpsRoutes groups Viewer LLM Ops proxy handlers owned by the llm feature
// registrar boundary.
type LLMOpsRoutes struct {
	Health  http.HandlerFunc
	Status  http.HandlerFunc
	Start   http.HandlerFunc
	Stop    http.HandlerFunc
	Restart http.HandlerFunc
}
