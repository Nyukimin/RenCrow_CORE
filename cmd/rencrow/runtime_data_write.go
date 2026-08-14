package main

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeDataWriteMaxPayloadBytes = 64 * 1024

// runtimeDataWriteOwnerResult is the typed result returned by one registered
// owner route. The registry supplies identity, route, status and completion
// time from the trusted scope and normalized request.
type runtimeDataWriteOwnerResult struct {
	SchemaVersion    string `json:"schema_version"`
	MigrationState   string `json:"migration_state"`
	ValidationState  string `json:"validation_state"`
	AuditRef         string `json:"audit_ref"`
	IdempotencyKey   string `json:"idempotency_key"`
	IdempotentReplay bool   `json:"idempotent_replay"`
	PolicyRevision   string `json:"policy_revision"`
}

// runtimeDataWriteReceipt is the only result exposed by the common write
// boundary after an owner route succeeds.
type runtimeDataWriteReceipt struct {
	Owner            string `json:"owner"`
	OwnerRoute       string `json:"owner_route"`
	AuditRef         string `json:"audit_ref"`
	RequestID        string `json:"-"`
	ActorID          string `json:"actor_id"`
	AgentRole        string `json:"agent_role"`
	Purpose          string `json:"purpose"`
	DataScope        string `json:"data_scope"`
	Status           string `json:"status"`
	SchemaVersion    string `json:"schema_version"`
	MigrationState   string `json:"migration_state"`
	ValidationState  string `json:"validation_state"`
	IdempotencyKey   string `json:"-"`
	IdempotentReplay bool   `json:"idempotent_replay"`
	PolicyRevision   string `json:"policy_revision"`
	CompletedAt      string `json:"completed_at"`
}

type dataWriteCallback func(context.Context, tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error)

type runtimeDataWriteKey struct {
	store     string
	operation string
}

type runtimeDataWriteRegistration struct {
	access   dataRecallAccess
	contract runtimeDataWriteContract
	callback dataWriteCallback
}

type runtimeDataWriteContract struct {
	RequiredPayloadFields []string
	OptionalPayloadFields []string
}

type runtimeDataWriteRoute struct {
	Store                 string
	Operation             string
	Access                dataRecallAccess
	RequiredPayloadFields []string
	OptionalPayloadFields []string
}

// runtimeDataWriteRegistry is the Worker-owned exact store/operation dispatch
// boundary. Registration happens during startup; the mutex keeps concurrent
// reads safe for the runtime lifetime.
type runtimeDataWriteRegistry struct {
	mu            sync.RWMutex
	registrations map[runtimeDataWriteKey]runtimeDataWriteRegistration
}

var (
	errDataWriteRegistryInvalidRegistration = errors.New("data write registration is invalid")
	errDataWriteRegistryUnknownOperation    = errors.New("data write store or operation is unavailable")
	errDataWriteRegistryDenied              = errors.New("data write access is denied")
	errDataWriteRegistryInvalidRequest      = errors.New("data write request is invalid")
	errDataWriteRegistryCallbackFailed      = errors.New("data write callback failed")
)

func newRuntimeDataWriteRegistry() *runtimeDataWriteRegistry {
	return &runtimeDataWriteRegistry{registrations: make(map[runtimeDataWriteKey]runtimeDataWriteRegistration)}
}

// Register adds one exact store/operation route. Names are normalized before
// duplicate detection, while the callback remains outside the registry lock.
func (r *runtimeDataWriteRegistry) Register(store, operation string, access dataRecallAccess, callback dataWriteCallback) error {
	return r.RegisterWithContract(store, operation, access, runtimeDataWriteContract{}, callback)
}

// RegisterWithContract adds one executable route and its model-visible
// payload field contract. The contract is the sole source for capability
// projection; it contains no schema implementation or callback detail.
func (r *runtimeDataWriteRegistry) RegisterWithContract(store, operation string, access dataRecallAccess, contract runtimeDataWriteContract, callback dataWriteCallback) error {
	if r == nil || callback == nil {
		return errDataWriteRegistryInvalidRegistration
	}
	key := runtimeDataWriteKey{store: strings.TrimSpace(store), operation: strings.TrimSpace(operation)}
	normalizedContract, ok := normalizeRuntimeDataWriteContract(contract)
	if key.store == "" || key.operation == "" || !validDataRecallAccess(access) || !ok {
		return errDataWriteRegistryInvalidRegistration
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.registrations[key]; exists {
		return errDataWriteRegistryInvalidRegistration
	}
	r.registrations[key] = runtimeDataWriteRegistration{access: access, contract: normalizedContract, callback: callback}
	return nil
}

func normalizeRuntimeDataWriteContract(contract runtimeDataWriteContract) (runtimeDataWriteContract, bool) {
	seen := map[string]struct{}{}
	normalize := func(fields []string) ([]string, bool) {
		out := make([]string, 0, len(fields))
		for _, field := range fields {
			field = strings.TrimSpace(field)
			if field == "" {
				return nil, false
			}
			if _, exists := seen[field]; exists {
				return nil, false
			}
			seen[field] = struct{}{}
			out = append(out, field)
		}
		sort.Strings(out)
		return out, true
	}
	required, ok := normalize(contract.RequiredPayloadFields)
	if !ok {
		return runtimeDataWriteContract{}, false
	}
	optional, ok := normalize(contract.OptionalPayloadFields)
	if !ok {
		return runtimeDataWriteContract{}, false
	}
	return runtimeDataWriteContract{RequiredPayloadFields: required, OptionalPayloadFields: optional}, true
}

// Snapshot returns a deterministic deep copy of executable write contracts.
func (r *runtimeDataWriteRegistry) Snapshot() []runtimeDataWriteRoute {
	if r == nil {
		return []runtimeDataWriteRoute{}
	}
	r.mu.RLock()
	routes := make([]runtimeDataWriteRoute, 0, len(r.registrations))
	for key, registration := range r.registrations {
		routes = append(routes, runtimeDataWriteRoute{
			Store: key.store, Operation: key.operation, Access: registration.access,
			RequiredPayloadFields: append([]string(nil), registration.contract.RequiredPayloadFields...),
			OptionalPayloadFields: append([]string(nil), registration.contract.OptionalPayloadFields...),
		})
	}
	r.mu.RUnlock()
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Store != routes[j].Store {
			return routes[i].Store < routes[j].Store
		}
		return routes[i].Operation < routes[j].Operation
	})
	return routes
}

// Write validates the trusted scope, normalizes the model request, checks the
// registered route's access class, and only then invokes its typed callback.
func (r *runtimeDataWriteRegistry) Write(ctx context.Context, request tools.DataWriteRequest) (any, error) {
	if r == nil {
		return nil, errDataWriteRegistryUnknownOperation
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent {
		return nil, errDataWriteRegistryDenied
	}
	normalized, key, err := normalizeRuntimeDataWriteRequest(request)
	if err != nil {
		return nil, err
	}
	r.mu.RLock()
	registration, found := r.registrations[key]
	r.mu.RUnlock()
	if !found {
		return nil, errDataWriteRegistryUnknownOperation
	}
	if !runtimeDataRecallAccessAllowed(scope, registration.access) {
		return nil, errDataWriteRegistryDenied
	}
	owner, callbackErr := registration.callback(ctx, normalized)
	if callbackErr != nil {
		return nil, errDataWriteRegistryCallbackFailed
	}
	owner, ok := normalizeRuntimeDataWriteOwnerResult(owner)
	if !ok {
		return nil, errDataWriteRegistryCallbackFailed
	}
	return runtimeDataWriteReceipt{
		RequestID:        strings.TrimSpace(scope.RequestID),
		ActorID:          strings.TrimSpace(scope.ActorID),
		AgentRole:        strings.TrimSpace(scope.AgentRole),
		Purpose:          strings.TrimSpace(scope.Purpose),
		DataScope:        string(registration.access),
		Owner:            normalized.Store,
		OwnerRoute:       normalized.Store + "/" + normalized.Operation,
		Status:           "completed",
		SchemaVersion:    owner.SchemaVersion,
		MigrationState:   owner.MigrationState,
		ValidationState:  owner.ValidationState,
		AuditRef:         owner.AuditRef,
		IdempotencyKey:   owner.IdempotencyKey,
		IdempotentReplay: owner.IdempotentReplay,
		PolicyRevision:   owner.PolicyRevision,
		CompletedAt:      time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func normalizeRuntimeDataWriteRequest(request tools.DataWriteRequest) (tools.DataWriteRequest, runtimeDataWriteKey, error) {
	request.Store = strings.TrimSpace(request.Store)
	request.Operation = strings.TrimSpace(request.Operation)
	if request.Store == "" || request.Operation == "" || request.Payload == nil {
		return tools.DataWriteRequest{}, runtimeDataWriteKey{}, errDataWriteRegistryInvalidRequest
	}
	payloadJSON, err := json.Marshal(request.Payload)
	if err != nil || len(payloadJSON) > runtimeDataWriteMaxPayloadBytes || tools.ValidateDataWritePayload(request.Payload) != nil {
		return tools.DataWriteRequest{}, runtimeDataWriteKey{}, errDataWriteRegistryInvalidRequest
	}
	return request, runtimeDataWriteKey{store: request.Store, operation: request.Operation}, nil
}

func normalizeRuntimeDataWriteOwnerResult(owner runtimeDataWriteOwnerResult) (runtimeDataWriteOwnerResult, bool) {
	owner.SchemaVersion = strings.TrimSpace(owner.SchemaVersion)
	owner.MigrationState = strings.TrimSpace(owner.MigrationState)
	owner.ValidationState = strings.TrimSpace(owner.ValidationState)
	owner.AuditRef = strings.TrimSpace(owner.AuditRef)
	owner.IdempotencyKey = strings.TrimSpace(owner.IdempotencyKey)
	owner.PolicyRevision = strings.TrimSpace(owner.PolicyRevision)
	if owner.SchemaVersion == "" || owner.MigrationState == "" || owner.ValidationState == "" || owner.AuditRef == "" || owner.IdempotencyKey == "" {
		return runtimeDataWriteOwnerResult{}, false
	}
	return owner, true
}
