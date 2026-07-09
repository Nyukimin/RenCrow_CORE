package chat

import (
	"strings"

	moduleworker "github.com/Nyukimin/RenCrow_CORE/modules/worker"
)

const ForecastWorkerFallbackLabel = "Worker local"

type IdleChatCoderProviderConfig struct {
	Enabled  bool
	Provider string
	Model    string
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

func CoderProviderIsExternal(provider string) bool {
	return moduleworker.CoderProviderIsExternal(provider)
}

func ForecastCoderLabelIndex(label string) int {
	return moduleworker.CoderSlotIndex(label)
}

func ForecastCoderProviderAllowed(coder IdleChatCoderProviderConfig, externalEnabled bool) bool {
	if externalEnabled {
		return true
	}
	return !CoderProviderIsExternal(coder.Provider)
}

func BuildForecastProviderPlans(candidates []ForecastCoderCandidate, externalEnabled bool) []ForecastProviderPlan {
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
			ProviderLabel: BuildForecastProviderLabel(label, coder),
			Allowed:       true,
		}
		if !ForecastCoderProviderAllowed(coder, externalEnabled) {
			plan.Allowed = false
			plan.SkipReason = "external provider not explicitly enabled"
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

func ForecastProviderModelLabel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return "configured provider"
	}
	return model
}

func BuildForecastProviderLabel(label string, coder IdleChatCoderProviderConfig) string {
	return strings.TrimSpace(label) + " " + strings.TrimSpace(coder.Provider) + " (" + ForecastProviderModelLabel(coder.Model) + ")"
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
