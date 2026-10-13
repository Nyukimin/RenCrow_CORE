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

// DefaultBusyPolicy lets every utterance reach the engine. The engine
// serialises requests itself and each session transcribes its utterances in
// order, so no caller needs to be superseded. queue_latest drops older
// requests and stays available only when configured explicitly.
const DefaultBusyPolicy = BusyPolicyDirect

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
	case BusyPolicyQueueLatest:
		return BusyPolicyQueueLatest
	default:
		return DefaultBusyPolicy
	}
}
