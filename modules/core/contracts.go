// Package core defines shared module contracts that are not owned by a
// provider-specific module.
package core

import (
	"context"
	"time"
)

type SessionID string
type RequestID string
type ResponseID string
type UtteranceID string
type MessageID string

type ChunkRef struct {
	SessionID   SessionID   `json:"session_id,omitempty"`
	ResponseID  ResponseID  `json:"response_id,omitempty"`
	UtteranceID UtteranceID `json:"utterance_id,omitempty"`
	MessageID   MessageID   `json:"message_id,omitempty"`
	ChunkIndex  int         `json:"chunk_index"`
}

type HealthStatus string

const (
	HealthReady   HealthStatus = "ready"
	HealthBlocked HealthStatus = "blocked"
	HealthLive    HealthStatus = "live"
	HealthDown    HealthStatus = "down"
)

type HealthReport struct {
	Module    string         `json:"module"`
	Status    HealthStatus   `json:"status"`
	Ready     bool           `json:"ready"`
	Detail    string         `json:"detail,omitempty"`
	CheckedAt time.Time      `json:"checked_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type HealthSnapshot struct {
	Status    HealthStatus   `json:"status"`
	Ready     bool           `json:"ready"`
	UpdatedAt string         `json:"updated_at"`
	Modules   []HealthReport `json:"modules"`
}

type HealthProvider interface {
	Health(context.Context) HealthReport
}

func ProviderHealth(ctx context.Context, module string, provider HealthProvider, checkedAt time.Time) HealthReport {
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	if provider == nil {
		return HealthReport{
			Module:    module,
			Status:    HealthDown,
			Detail:    "provider is nil",
			CheckedAt: checkedAt,
		}
	}
	report := provider.Health(ctx)
	report.Module = module
	report.CheckedAt = checkedAt
	return report
}

func AggregateHealthReports(reports []HealthReport) HealthReport {
	overall := HealthReport{
		Module: "modules",
		Status: HealthReady,
		Ready:  true,
	}
	for _, report := range reports {
		if !report.Ready {
			overall.Ready = false
		}
		switch report.Status {
		case HealthDown:
			overall.Status = HealthDown
			overall.Ready = false
		case HealthBlocked:
			if overall.Status != HealthDown {
				overall.Status = HealthBlocked
			}
			overall.Ready = false
		case HealthLive:
			if overall.Status != HealthDown && overall.Status != HealthBlocked {
				overall.Status = HealthLive
			}
		}
	}
	return overall
}

func BuildHealthSnapshot(reports []HealthReport, updatedAt time.Time) HealthSnapshot {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	aggregate := AggregateHealthReports(reports)
	return HealthSnapshot{
		Status:    aggregate.Status,
		Ready:     aggregate.Ready,
		UpdatedAt: updatedAt.UTC().Format(time.RFC3339),
		Modules:   reports,
	}
}

type Event struct {
	Type      string         `json:"type"`
	SessionID SessionID      `json:"session_id,omitempty"`
	TraceID   string         `json:"trace_id,omitempty"`
	At        time.Time      `json:"at,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

type StateOwner string

const (
	StateOwnerCore   StateOwner = "core"
	StateOwnerChat   StateOwner = "chat"
	StateOwnerWorker StateOwner = "worker"
	StateOwnerLLM    StateOwner = "llm"
	StateOwnerTTS    StateOwner = "tts"
	StateOwnerSTT    StateOwner = "stt"
	StateOwnerVoice  StateOwner = "voice"
	StateOwnerWeb    StateOwner = "web"
)

type OwnedState struct {
	Name      string     `json:"name"`
	Owner     StateOwner `json:"owner"`
	Unit      string     `json:"unit,omitempty"`
	Lifetime  string     `json:"lifetime,omitempty"`
	RebuildBy string     `json:"rebuild_by,omitempty"`
}

type ModuleDescriptor struct {
	Name        string       `json:"name"`
	Owner       StateOwner   `json:"owner"`
	Kind        string       `json:"kind"`
	Contracts   []string     `json:"contracts,omitempty"`
	Endpoints   []string     `json:"endpoints,omitempty"`
	OwnsState   []OwnedState `json:"owns_state,omitempty"`
	Description string       `json:"description,omitempty"`
}
