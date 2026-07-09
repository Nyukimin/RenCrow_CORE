// Package chat defines user-facing conversation module contracts.
package chat

import (
	"context"

	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/Nyukimin/RenCrow_CORE/modules/llm"
	"github.com/Nyukimin/RenCrow_CORE/modules/stt"
	"github.com/Nyukimin/RenCrow_CORE/modules/tts"
	"github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

type Input struct {
	SessionID core.SessionID  `json:"session_id,omitempty"`
	Channel   string          `json:"channel,omitempty"`
	UserID    string          `json:"user_id,omitempty"`
	To        ViewerRecipient `json:"to,omitempty"`
	Text      string          `json:"text,omitempty"`
	Audio     []byte          `json:"-"`
}

type Route string

const (
	RouteChat   Route = "chat"
	RouteWorker Route = "worker"
	RouteLLM    Route = "llm"
	RouteTTS    Route = "tts"
	RouteSTT    Route = "stt"
)

type RouteDecision struct {
	Route  Route  `json:"route"`
	Reason string `json:"reason,omitempty"`
}

type RoutePolicy interface {
	DecideRoute(ctx context.Context, input Input) (RouteDecision, error)
}

type Output struct {
	SessionID core.SessionID       `json:"session_id,omitempty"`
	Text      string               `json:"text,omitempty"`
	Route     RouteDecision        `json:"route,omitempty"`
	JobID     string               `json:"job_id,omitempty"`
	Response  llm.GenerateResponse `json:"response,omitempty"`
}

type RuntimePorts struct {
	LLM    llm.Provider
	TTS    tts.Provider
	STT    stt.Provider
	Worker worker.Executor
}

type Service interface {
	RoutePolicy
	Respond(ctx context.Context, input Input, ports RuntimePorts) (Output, error)
}
