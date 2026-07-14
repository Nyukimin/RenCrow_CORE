package agentprofile

import "strings"

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
	Observe          []string
	Decide           []string
	ActAllowed       []string
	ApprovalRequired []string
	Forbidden        []string
}

func (e AutonomyEnvelope) CanDecide(action string) bool {
	return containsAction(e.Decide, action)
}

func (e AutonomyEnvelope) CanAct(action string) bool {
	if e.IsForbidden(action) || e.RequiresApproval(action) {
		return false
	}
	return containsAction(e.ActAllowed, action)
}

func (e AutonomyEnvelope) RequiresApproval(action string) bool {
	return containsAction(e.ApprovalRequired, action)
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

type Profile struct {
	ID                string
	DisplayName       string
	Role              string
	Capabilities      []Capability
	Goals             []Goal
	Motivation        []MotivationSignal
	UtilityProfile    UtilityProfile
	AutonomyEnvelope  AutonomyEnvelope
	EconomicProfile   *EconomicProfile
	KnowledgeAffinity []KnowledgeAffinity
}
