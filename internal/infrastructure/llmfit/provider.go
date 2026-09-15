// Package llmfit adapts the external llmfit capability sensor (HTTP server or
// CLI) to the RenCrow LLM Ops domain model. llmfit is an observation source,
// never a source of truth: this package only fetches, decodes, and maps.
package llmfit

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
)

// CapabilityProvider fetches llmfit observations for one node. Implementations:
// HTTPProvider (remote llmfit server), CLIProvider (local llmfit executable),
// MockProvider (tests). The application service depends on this interface only.
type CapabilityProvider interface {
	// Health reports nil when llmfit on the node is reachable and healthy.
	Health(ctx context.Context) error
	// System returns the node hardware profile.
	System(ctx context.Context) (*domainllmops.NodeHardwareProfile, error)
	// TopModels returns the best-fitting models for the node (llmfit "top").
	TopModels(ctx context.Context, query ModelFitQuery) ([]domainllmops.ModelFitAssessment, error)
	// SearchModels returns models matching query.Keyword (llmfit "models" search).
	SearchModels(ctx context.Context, query ModelFitQuery) ([]domainllmops.ModelFitAssessment, error)
}

// ModelFitQuery narrows a model fit request. Zero values mean "llmfit default".
// Values are validated against llmfit's documented enumerations before any
// request is built, so user input is never forwarded unchecked.
type ModelFitQuery struct {
	Limit           int
	MinFit          string // perfect | good | marginal | too_tight
	UseCase         string // general | coding | reasoning | chat | multimodal | embedding
	Runtime         string // any | mlx | llamacpp | vllm | bitnetcpp
	MaxContext      int
	Sort            string // score | tps | params | mem | ctx | date | use_case
	Keyword         string // SearchModels only
	IncludeTooTight bool
}

var (
	allowedMinFit  = []string{"perfect", "good", "marginal", "too_tight"}
	allowedUseCase = []string{"general", "coding", "reasoning", "chat", "multimodal", "embedding"}
	allowedRuntime = []string{"any", "mlx", "llamacpp", "vllm", "bitnetcpp"}
	allowedSort    = []string{"score", "tps", "params", "mem", "ctx", "date", "use_case"}
)

// AllowedMinFit lists accepted min_fit values in fit order (best first).
func AllowedMinFit() []string { return append([]string(nil), allowedMinFit...) }

// AllowedUseCase lists accepted use_case values.
func AllowedUseCase() []string { return append([]string(nil), allowedUseCase...) }

// AllowedRuntime lists accepted runtime values.
func AllowedRuntime() []string { return append([]string(nil), allowedRuntime...) }

// AllowedSort lists accepted sort values.
func AllowedSort() []string { return append([]string(nil), allowedSort...) }

// Normalized trims and lower-cases enumerated fields.
func (q ModelFitQuery) Normalized() ModelFitQuery {
	q.MinFit = strings.ToLower(strings.TrimSpace(q.MinFit))
	q.UseCase = strings.ToLower(strings.TrimSpace(q.UseCase))
	q.Runtime = strings.ToLower(strings.TrimSpace(q.Runtime))
	q.Sort = strings.ToLower(strings.TrimSpace(q.Sort))
	q.Keyword = strings.TrimSpace(q.Keyword)
	return q
}

// Validate rejects values outside llmfit's documented enumerations and ranges.
func (q ModelFitQuery) Validate() error {
	n := q.Normalized()
	if n.Limit < 0 {
		return errors.New("limit must not be negative")
	}
	if n.MaxContext < 0 {
		return errors.New("max_context must not be negative")
	}
	if n.MinFit != "" && !containsString(allowedMinFit, n.MinFit) {
		return fmt.Errorf("min_fit must be one of %s", strings.Join(allowedMinFit, "|"))
	}
	if n.UseCase != "" && !containsString(allowedUseCase, n.UseCase) {
		return fmt.Errorf("use_case must be one of %s", strings.Join(allowedUseCase, "|"))
	}
	if n.Runtime != "" && !containsString(allowedRuntime, n.Runtime) {
		return fmt.Errorf("runtime must be one of %s", strings.Join(allowedRuntime, "|"))
	}
	if n.Sort != "" && !containsString(allowedSort, n.Sort) {
		return fmt.Errorf("sort must be one of %s", strings.Join(allowedSort, "|"))
	}
	return nil
}

func containsString(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// ErrorKind classifies provider failures so the service can decide whether a
// node is offline (unreachable) or llmfit itself misbehaved.
type ErrorKind string

// Provider error kinds.
const (
	ErrorKindUnknown         ErrorKind = ""
	ErrorKindConfiguration   ErrorKind = "configuration"    // provider misconfigured (empty base URL etc.)
	ErrorKindUnreachable     ErrorKind = "unreachable"      // connection refused, timeout, killed command
	ErrorKindBadRequest      ErrorKind = "bad_request"      // HTTP 4xx or a rejected query
	ErrorKindServerError     ErrorKind = "server_error"     // HTTP 5xx, unhealthy status, non-zero exit
	ErrorKindInvalidResponse ErrorKind = "invalid_response" // JSON decode failure
	ErrorKindExecutable      ErrorKind = "executable"       // llmfit executable not found
)

// ProviderError is the error type returned by providers.
type ProviderError struct {
	Kind    ErrorKind
	NodeID  string
	Op      string
	Message string
	Err     error
}

func (e *ProviderError) Error() string {
	var b strings.Builder
	b.WriteString("llmfit")
	if e.NodeID != "" {
		b.WriteString(" ")
		b.WriteString(e.NodeID)
	}
	if e.Op != "" {
		b.WriteString(" ")
		b.WriteString(e.Op)
	}
	b.WriteString(": ")
	if e.Message != "" {
		b.WriteString(e.Message)
	} else if e.Err != nil {
		b.WriteString(e.Err.Error())
	} else {
		b.WriteString(string(e.Kind))
	}
	return b.String()
}

// Unwrap exposes the underlying error for errors.Is / errors.As.
func (e *ProviderError) Unwrap() error { return e.Err }

// ErrorKindOf returns the ErrorKind carried by err, or ErrorKindUnknown.
func ErrorKindOf(err error) ErrorKind {
	var perr *ProviderError
	if errors.As(err, &perr) {
		return perr.Kind
	}
	return ErrorKindUnknown
}

func newProviderError(kind ErrorKind, nodeID, op, message string, err error) *ProviderError {
	return &ProviderError{Kind: kind, NodeID: nodeID, Op: op, Message: message, Err: err}
}

func validateQuery(nodeID, op string, query ModelFitQuery) (ModelFitQuery, error) {
	if err := query.Validate(); err != nil {
		return ModelFitQuery{}, newProviderError(ErrorKindBadRequest, nodeID, op, err.Error(), err)
	}
	return query.Normalized(), nil
}
