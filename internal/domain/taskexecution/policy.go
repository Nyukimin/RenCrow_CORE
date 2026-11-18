package taskexecution

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	SchemaVersionV1       = 1
	AllowedAgentsAll      = "all_registered"
	AllowedAgentsExplicit = "explicit"
)

// Policy is the versioned per-kind execution-agent allowlist.
type Policy struct {
	SchemaVersion int                   `json:"schema_version" yaml:"schema_version"`
	TaskKinds     map[string]KindPolicy `json:"task_kinds" yaml:"task_kinds"`
}

type KindPolicy struct {
	AllowedAgents AllowedAgents `json:"allowed_agents" yaml:"allowed_agents"`
}

type AllowedAgents struct {
	Mode     string   `json:"mode" yaml:"mode"`
	ActorIDs []string `json:"actor_ids,omitempty" yaml:"actor_ids,omitempty"`
}

func (p Policy) Validate(registeredKinds, registeredActors []string) error {
	kinds, err := registrySet("kind", registeredKinds)
	if err != nil {
		return err
	}
	actors, err := registrySet("actor", registeredActors)
	if err != nil {
		return err
	}
	if p.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("schema_version must be %d", SchemaVersionV1)
	}
	if len(p.TaskKinds) == 0 {
		return errors.New("task_kinds must not be empty")
	}
	for kindID, policy := range p.TaskKinds {
		if err := validateCanonicalID("kind_id", kindID); err != nil {
			return err
		}
		if _, ok := kinds[kindID]; !ok {
			return fmt.Errorf("unknown kind %q", kindID)
		}
		if err := validateAllowedAgents(policy.AllowedAgents, actors); err != nil {
			return fmt.Errorf("task kind %q: %w", kindID, err)
		}
	}
	return nil
}

func (p Policy) Allowed(kindID, actorID string, registeredActors []string, subset *[]string) (bool, error) {
	if err := validateCanonicalID("kind_id", kindID); err != nil {
		return false, err
	}
	if err := validateCanonicalID("actor_id", actorID); err != nil {
		return false, err
	}
	actors, err := registrySet("actor", registeredActors)
	if err != nil {
		return false, err
	}
	if _, registered := actors[actorID]; !registered {
		return false, fmt.Errorf("unknown actor %q", actorID)
	}
	for registeredKind, registeredKindPolicy := range p.TaskKinds {
		if err := validateCanonicalID("kind_id", registeredKind); err != nil {
			return false, err
		}
		if err := validateAllowedAgents(registeredKindPolicy.AllowedAgents, actors); err != nil {
			return false, fmt.Errorf("task kind %q: %w", registeredKind, err)
		}
	}
	if p.SchemaVersion != SchemaVersionV1 {
		return false, fmt.Errorf("schema_version must be %d", SchemaVersionV1)
	}
	kindPolicy, ok := p.TaskKinds[kindID]
	if !ok {
		return false, fmt.Errorf("unknown kind %q", kindID)
	}
	if err := validateAllowedAgents(kindPolicy.AllowedAgents, actors); err != nil {
		return false, fmt.Errorf("task kind %q: %w", kindID, err)
	}
	base := make(map[string]struct{})
	switch kindPolicy.AllowedAgents.Mode {
	case AllowedAgentsAll:
		for registeredActor := range actors {
			base[registeredActor] = struct{}{}
		}
	case AllowedAgentsExplicit:
		for _, explicitActor := range kindPolicy.AllowedAgents.ActorIDs {
			base[explicitActor] = struct{}{}
		}
	default:
		return false, fmt.Errorf("invalid allowed_agents mode %q", kindPolicy.AllowedAgents.Mode)
	}
	if subset == nil {
		_, allowed := base[actorID]
		return allowed, nil
	}
	if len(*subset) == 0 {
		return false, errors.New("explicit actor subset must not be empty")
	}
	seen := make(map[string]struct{}, len(*subset))
	for _, subsetActor := range *subset {
		if err := validateCanonicalID("subset actor_id", subsetActor); err != nil {
			return false, err
		}
		if _, exists := seen[subsetActor]; exists {
			return false, fmt.Errorf("duplicate subset actor %q", subsetActor)
		}
		seen[subsetActor] = struct{}{}
		if _, registered := actors[subsetActor]; !registered {
			return false, fmt.Errorf("unknown subset actor %q", subsetActor)
		}
		if _, permitted := base[subsetActor]; !permitted {
			return false, fmt.Errorf("subset actor %q exceeds kind policy", subsetActor)
		}
	}
	_, allowed := seen[actorID]
	_, kindAllowed := base[actorID]
	return allowed && kindAllowed, nil
}

// CanonicalHash returns a stable SHA-256 revision for semantically equivalent
// policy values, regardless of map or set ordering.
func (p Policy) CanonicalHash() (string, error) {
	if err := p.validateShape(); err != nil {
		return "", err
	}
	normalized := Policy{SchemaVersion: p.SchemaVersion, TaskKinds: make(map[string]KindPolicy, len(p.TaskKinds))}
	for kindID, kindPolicy := range p.TaskKinds {
		copyPolicy := kindPolicy
		copyPolicy.AllowedAgents.ActorIDs = append([]string(nil), kindPolicy.AllowedAgents.ActorIDs...)
		sort.Strings(copyPolicy.AllowedAgents.ActorIDs)
		normalized.TaskKinds[kindID] = copyPolicy
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode canonical policy: %w", err)
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}

// CanonicalHash is the package-level form for callers that prefer a function.
func CanonicalHash(policy Policy) (string, error) { return policy.CanonicalHash() }

func (p Policy) validateShape() error {
	if p.SchemaVersion != SchemaVersionV1 {
		return fmt.Errorf("schema_version must be %d", SchemaVersionV1)
	}
	if len(p.TaskKinds) == 0 {
		return errors.New("task_kinds must not be empty")
	}
	for kindID, policy := range p.TaskKinds {
		if err := validateCanonicalID("kind_id", kindID); err != nil {
			return err
		}
		if err := validateAllowedAgents(policy.AllowedAgents, nil); err != nil {
			return fmt.Errorf("task kind %q: %w", kindID, err)
		}
		for _, actorID := range policy.AllowedAgents.ActorIDs {
			if err := validateCanonicalID("actor_id", actorID); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAllowedAgents(allowed AllowedAgents, registeredActors map[string]struct{}) error {
	switch allowed.Mode {
	case AllowedAgentsAll:
		if len(allowed.ActorIDs) != 0 {
			return errors.New("all_registered mode must not set actor_ids")
		}
	case AllowedAgentsExplicit:
		if len(allowed.ActorIDs) == 0 {
			return errors.New("explicit mode requires at least one actor_id")
		}
		seen := make(map[string]struct{}, len(allowed.ActorIDs))
		for _, actorID := range allowed.ActorIDs {
			if err := validateCanonicalID("actor_id", actorID); err != nil {
				return err
			}
			if _, exists := seen[actorID]; exists {
				return fmt.Errorf("duplicate actor_id %q", actorID)
			}
			seen[actorID] = struct{}{}
			if registeredActors != nil {
				if _, ok := registeredActors[actorID]; !ok {
					return fmt.Errorf("unknown actor %q", actorID)
				}
			}
		}
	default:
		return fmt.Errorf("invalid allowed_agents mode %q", allowed.Mode)
	}
	return nil
}

func registrySet(kind string, values []string) (map[string]struct{}, error) {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		if err := validateCanonicalID(kind+" registry ID", value); err != nil {
			return nil, err
		}
		if _, exists := result[value]; exists {
			return nil, fmt.Errorf("duplicate registered %s %q", kind, value)
		}
		result[value] = struct{}{}
	}
	return result, nil
}

func validateCanonicalID(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.ToLower(value) != value {
		return fmt.Errorf("%s %q is not canonical lowercase", name, value)
	}
	for _, char := range value {
		if unicode.IsSpace(char) {
			return fmt.Errorf("%s %q must not contain whitespace", name, value)
		}
	}
	return nil
}
