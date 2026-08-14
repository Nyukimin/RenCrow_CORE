package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const (
	dataRecallAccessPublic   dataRecallAccess = "public"
	dataRecallAccessUser     dataRecallAccess = "user"
	dataRecallAccessInternal dataRecallAccess = "internal"

	runtimeDataRecallDefaultLimit = 10
	runtimeDataRecallMaxLimit     = 50
)

type dataRecallAccess string

type dataRecallCallback func(context.Context, tools.DataRecallRequest) (runtimeDataRecallResult, error)

type runtimeDataRecallKey struct {
	store     string
	operation string
}

type runtimeDataRecallRegistration struct {
	access   dataRecallAccess
	callback dataRecallCallback
}

// runtimeDataRecallRegistry is the Worker-owned name/operation dispatch
// boundary. Registration happens during startup; the mutex also makes
// concurrent reads safe for the lifetime of the runtime.
type runtimeDataRecallRegistry struct {
	mu            sync.RWMutex
	registrations map[runtimeDataRecallKey]runtimeDataRecallRegistration
}

var (
	errDataRecallRegistryInvalidRegistration = errors.New("data recall registration is invalid")
	errDataRecallRegistryUnknownOperation    = errors.New("data recall store or operation is unavailable")
	errDataRecallRegistryDenied              = errors.New("data recall access is denied")
	errDataRecallRegistryInvalidRequest      = errors.New("data recall request is invalid")
	errDataRecallRegistryCallbackFailed      = errors.New("data recall callback failed")
)

func newRuntimeDataRecallRegistry() *runtimeDataRecallRegistry {
	return &runtimeDataRecallRegistry{registrations: make(map[runtimeDataRecallKey]runtimeDataRecallRegistration)}
}

// Register adds one exact store/operation route. Names are normalized before
// duplicate detection, while the callback remains outside the registry's lock.
func (r *runtimeDataRecallRegistry) Register(store, operation string, access dataRecallAccess, callback dataRecallCallback) error {
	if r == nil || callback == nil {
		return errDataRecallRegistryInvalidRegistration
	}
	key := runtimeDataRecallKey{store: strings.TrimSpace(store), operation: strings.TrimSpace(operation)}
	if key.store == "" || key.operation == "" || !validDataRecallAccess(access) {
		return errDataRecallRegistryInvalidRegistration
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.registrations[key]; exists {
		return errDataRecallRegistryInvalidRegistration
	}
	r.registrations[key] = runtimeDataRecallRegistration{access: access, callback: callback}
	return nil
}

// Recall validates the trusted scope, normalizes the model request, checks the
// registered route's access class, and only then invokes its callback.
func (r *runtimeDataRecallRegistry) Recall(ctx context.Context, request tools.DataRecallRequest) (any, error) {
	if r == nil {
		return nil, errDataRecallRegistryUnknownOperation
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil {
		return nil, errDataRecallRegistryDenied
	}
	normalized, key, err := normalizeRuntimeDataRecallRequest(request)
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	registration, found := r.registrations[key]
	r.mu.RUnlock()
	if !found {
		return nil, errDataRecallRegistryUnknownOperation
	}
	if !runtimeDataRecallAccessAllowed(scope, registration.access) {
		return nil, errDataRecallRegistryDenied
	}
	result, callbackErr := registration.callback(ctx, normalized)
	if callbackErr != nil {
		return nil, errDataRecallRegistryCallbackFailed
	}
	if result.Store != normalized.Store || result.Operation != normalized.Operation || result.Records == nil {
		return nil, errDataRecallRegistryCallbackFailed
	}
	result.Evidence = runtimeDataRecallEvidence{
		RequestID:       scope.RequestID,
		ActorID:         scope.ActorID,
		AgentRole:       scope.AgentRole,
		Purpose:         scope.Purpose,
		DataScope:       string(registration.access),
		Owner:           normalized.Store,
		OwnerRoute:      normalized.Store + "/" + normalized.Operation,
		RetrievedAt:     time.Now().UTC().Format(time.RFC3339Nano),
		FreshnessState:  "observed_at_read",
		ValidationState: "owner_route_succeeded",
		BudgetLimit:     normalized.Limit,
		ReturnedCount:   len(result.Records),
	}
	return result, nil
}

func normalizeRuntimeDataRecallRequest(request tools.DataRecallRequest) (tools.DataRecallRequest, runtimeDataRecallKey, error) {
	request.Store = strings.TrimSpace(request.Store)
	request.Operation = strings.TrimSpace(request.Operation)
	request.Query = strings.TrimSpace(request.Query)
	if request.Store == "" || request.Operation == "" || request.Query == "" {
		return tools.DataRecallRequest{}, runtimeDataRecallKey{}, errDataRecallRegistryInvalidRequest
	}
	if request.Limit == 0 {
		request.Limit = runtimeDataRecallDefaultLimit
	}
	if request.Limit < 1 || request.Limit > runtimeDataRecallMaxLimit {
		return tools.DataRecallRequest{}, runtimeDataRecallKey{}, fmt.Errorf("%w: limit", errDataRecallRegistryInvalidRequest)
	}
	return request, runtimeDataRecallKey{store: request.Store, operation: request.Operation}, nil
}

func validDataRecallAccess(access dataRecallAccess) bool {
	switch access {
	case dataRecallAccessPublic, dataRecallAccessUser, dataRecallAccessInternal:
		return true
	default:
		return false
	}
}

func runtimeDataRecallAccessAllowed(scope domaintool.ToolExecutionScope, access dataRecallAccess) bool {
	if strings.TrimSpace(scope.AgentRole) != "worker" || strings.TrimSpace(scope.Purpose) != "ops" {
		return false
	}
	switch access {
	case dataRecallAccessPublic:
		return scope.Allows(domaintool.DataScopePublic)
	case dataRecallAccessUser:
		return scope.Allows(domaintool.DataScopeUser) && strings.TrimSpace(scope.AuthenticatedUserID) != ""
	case dataRecallAccessInternal:
		return scope.ActorKind == domaintool.ActorKindAgent && scope.Allows(domaintool.DataScopeInternal)
	default:
		return false
	}
}
