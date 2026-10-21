package worldprojection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	domainagentprofile "github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

// AgentProfileSource is the read-only CORE AgentProfile owner view used to
// derive one Actor's non-cognitive LifePolicy inputs.
type AgentProfileSource interface {
	Get(string) (domainagentprofile.Profile, bool)
}

// LifePolicySnapshot is one CORE-derived LifePolicy content decision. The
// projector adds the destination, source epoch/revision, and validity horizon.
type LifePolicySnapshot struct {
	PersonaRevision                string
	ConditionProfileRef            string
	AllowedNonCognitiveActivityIDs []string
	ExpiredFallbackActivityIDs     []string
}

// LifePolicySource derives the CORE-owned LifePolicy content for one Actor.
// It must fail closed when the Actor's Profile or Persona is missing; it must
// never substitute a default permission set.
type LifePolicySource interface {
	Snapshot(context.Context, ProjectionActor) (LifePolicySnapshot, error)
}

// CatalogLifePolicySource reads the existing CORE AgentProfile catalog and
// loaded Persona CharacterProfiles. It copies no Persona body or Memory into
// the projection; only content-addressed revision references leave CORE.
type CatalogLifePolicySource struct {
	Profiles AgentProfileSource
	Personas map[string]domainpersona.CharacterProfile
}

func (s *CatalogLifePolicySource) Snapshot(ctx context.Context, actor ProjectionActor) (LifePolicySnapshot, error) {
	if s == nil || s.Profiles == nil {
		return LifePolicySnapshot{}, fmt.Errorf("world projection: agent profile source is required")
	}
	if ctx == nil {
		return LifePolicySnapshot{}, fmt.Errorf("world projection: context is nil")
	}
	coreID := strings.TrimSpace(actor.CoreAssignee)
	if coreID == "" {
		coreID = strings.TrimSpace(actor.ActorID)
	}
	if coreID == "" {
		return LifePolicySnapshot{}, fmt.Errorf("world projection: actor has no CORE profile id")
	}
	profile, ok := s.Profiles.Get(coreID)
	if !ok {
		return LifePolicySnapshot{}, fmt.Errorf("world projection: CORE agent profile %q is not registered", coreID)
	}
	envelope, err := profile.NonCognitive.Normalized()
	if err != nil {
		return LifePolicySnapshot{}, fmt.Errorf("world projection: %s: %w", coreID, err)
	}
	persona, ok := s.Personas[coreID]
	if !ok {
		persona, ok = s.Personas[strings.ToLower(coreID)]
	}
	if !ok {
		return LifePolicySnapshot{}, fmt.Errorf("world projection: CORE persona %q is not loaded", coreID)
	}
	personaRevision, err := contentRevision("persona-sha256:", persona)
	if err != nil {
		return LifePolicySnapshot{}, err
	}
	canonicalProfile := profile
	canonicalProfile.NonCognitive = envelope
	conditionRef, err := contentRevision("agent-profile-sha256:", canonicalProfile)
	if err != nil {
		return LifePolicySnapshot{}, err
	}
	return LifePolicySnapshot{
		PersonaRevision:                personaRevision,
		ConditionProfileRef:            conditionRef,
		AllowedNonCognitiveActivityIDs: append([]string(nil), envelope.Allowed...),
		ExpiredFallbackActivityIDs:     append([]string(nil), envelope.ExpiredFallback...),
	}, nil
}

func contentRevision(prefix string, value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("world projection: content revision is not encodable: %w", err)
	}
	sum := sha256.Sum256(raw)
	return prefix + hex.EncodeToString(sum[:]), nil
}
