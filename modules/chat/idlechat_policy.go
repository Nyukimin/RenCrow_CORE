package chat

import (
	"strings"

	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

const (
	ForecastWorkerFallbackLabel = "Worker via RenCrow_LLM"
	ForecastTopicGeneratorAgent = "Shiro"
)

type IdleChatCoderProviderConfig struct {
	Enabled bool
}

type IdleChatLLMOptions struct {
	Think *bool
}

type ForecastCoderCandidate struct {
	Label string
	Coder IdleChatCoderProviderConfig
}

type ForecastProviderPlan struct {
	Label         string
	Coder         IdleChatCoderProviderConfig
	ProviderLabel string
	Allowed       bool
	SkipReason    string
}

func ForecastCoderLabelIndex(label string) int {
	return moduleworker.CoderSlotIndex(label)
}

func ForecastCoderAlias(label string) string {
	index := ForecastCoderLabelIndex(label)
	if index < 0 {
		return ""
	}
	return "coder" + string(rune('1'+index))
}

func BuildForecastProviderPlans(candidates []ForecastCoderCandidate) []ForecastProviderPlan {
	plans := make([]ForecastProviderPlan, 0, len(candidates))
	for _, candidate := range candidates {
		label := strings.TrimSpace(candidate.Label)
		coder := candidate.Coder
		if !coder.Enabled {
			continue
		}
		plan := ForecastProviderPlan{
			Label:         label,
			Coder:         coder,
			ProviderLabel: BuildForecastProviderLabel(label),
			Allowed:       true,
		}
		plans = append(plans, plan)
	}
	return plans
}

func ForecastProviderLogLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		return "unavailable"
	}
	return label
}

func BuildForecastProviderLabel(label string) string {
	return strings.TrimSpace(label) + " via RenCrow_LLM"
}

func IdleChatProviderOptions(options map[string]IdleChatLLMOptions) map[string]map[string]any {
	out := make(map[string]map[string]any, len(options))
	for name, opts := range options {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" || opts.Think == nil {
			continue
		}
		out[key] = map[string]any{"think": *opts.Think}
	}
	return out
}
