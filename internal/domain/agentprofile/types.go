package agentprofile

import (
	"fmt"
	"sort"
	"strings"
)

type Capability struct {
	ID          string
	Description string
}

type Goal struct {
	ID          string
	Description string
	Weight      float64
}

type MotivationSignal struct {
	ID          string
	Description string
	Weight      float64
}

type UtilityProfile struct {
	SuccessRate       float64
	UserValue         float64
	Quality           float64
	ReuseValue        float64
	StrategicValue    float64
	ReworkPenalty     float64
	RiskPenalty       float64
	ReputationPenalty float64
}

type EconomicProfile struct {
	Enabled       bool
	NetProfit     float64
	CustomerValue float64
	Automation    float64
	FutureValue   float64
}

type KnowledgeAffinity struct {
	Topic  string
	Weight float64
}

type AutonomyEnvelope struct {
	Observe    []string
	Decide     []string
	ActAllowed []string
	Forbidden  []string
}

func (e AutonomyEnvelope) CanDecide(action string) bool {
	return containsAction(e.Decide, action)
}

func (e AutonomyEnvelope) CanAct(action string) bool {
	if e.IsForbidden(action) {
		return false
	}
	return containsAction(e.ActAllowed, action)
}

func (e AutonomyEnvelope) IsForbidden(action string) bool {
	return containsAction(e.Forbidden, action)
}

func containsAction(values []string, action string) bool {
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		return false
	}
	for _, value := range values {
		if strings.ToLower(strings.TrimSpace(value)) == action {
			return true
		}
	}
	return false
}

// NonCognitiveEnvelope is CORE's explicit permission set for WORLD life
// activities. It carries opaque activity IDs only; it never carries Persona,
// Memory, model, or hardware details. The WORLD activity catalog owns the
// meaning and availability of each ID.
type NonCognitiveEnvelope struct {
	Allowed         []string
	ExpiredFallback []string
}

const (
	MaxNonCognitiveActivities    = 128
	MaxExpiredFallbackActivities = 16
)

// Normalized validates the envelope and returns sorted, copied activity IDs.
// It rejects an empty allowed set, empty IDs, duplicates, and a fallback that
// is not a subset of the allowed set. A missing envelope is not a valid
// permission decision for a CORE Actor.
func (e NonCognitiveEnvelope) Normalized() (NonCognitiveEnvelope, error) {
	allowed, err := normalizeActivityIDs(e.Allowed, MaxNonCognitiveActivities, "allowed")
	if err != nil {
		return NonCognitiveEnvelope{}, err
	}
	if len(allowed) == 0 {
		return NonCognitiveEnvelope{}, fmt.Errorf("non-cognitive envelope: allowed activity ids are required")
	}
	fallback, err := normalizeActivityIDs(e.ExpiredFallback, MaxExpiredFallbackActivities, "expired fallback")
	if err != nil {
		return NonCognitiveEnvelope{}, err
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		allowedSet[id] = struct{}{}
	}
	for _, id := range fallback {
		if _, ok := allowedSet[id]; !ok {
			return NonCognitiveEnvelope{}, fmt.Errorf("non-cognitive envelope: expired fallback %q is not allowed", id)
		}
	}
	return NonCognitiveEnvelope{Allowed: allowed, ExpiredFallback: fallback}, nil
}

func normalizeActivityIDs(values []string, max int, label string) ([]string, error) {
	if len(values) > max {
		return nil, fmt.Errorf("non-cognitive envelope: too many %s activity ids", label)
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			return nil, fmt.Errorf("non-cognitive envelope: %s activity id is empty", label)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("non-cognitive envelope: duplicate %s activity id %q", label, id)
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

type Profile struct {
	ID                string
	DisplayName       string
	Role              string
	Capabilities      []Capability
	Goals             []Goal
	Motivation        []MotivationSignal
	UtilityProfile    UtilityProfile
	AutonomyEnvelope  AutonomyEnvelope
	NonCognitive      NonCognitiveEnvelope
	EconomicProfile   *EconomicProfile
	KnowledgeAffinity []KnowledgeAffinity
}
