package stt

import (
	"strings"
)

type BusyPolicyPlan struct {
	Policy     string
	UsesQueue  bool
	UsesReject bool
	UsesDirect bool
}

func BuildBusyPolicyPlan(policy string) BusyPolicyPlan {
	normalized := NormalizeBusyPolicy(policy)
	return BusyPolicyPlan{
		Policy:     normalized,
		UsesQueue:  normalized == BusyPolicyQueueLatest,
		UsesReject: normalized == BusyPolicyReject,
		UsesDirect: normalized == BusyPolicyDirect,
	}
}

func NormalizeBusyPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case BusyPolicyDirect:
		return BusyPolicyDirect
	case BusyPolicyReject:
		return BusyPolicyReject
	case BusyPolicyQueueLatest, "":
		return BusyPolicyQueueLatest
	default:
		return BusyPolicyQueueLatest
	}
}
