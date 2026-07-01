package worker

import "strings"

type CoderSlotConfig struct {
	Name                string
	DisplayName         string
	Provider            string
	Model               string
	APIKey              string
	Enabled             bool
	LightMemoryEnabled  bool
	LightMemoryMaxTurns int
}

type LLMCapability struct {
	ProviderName string
	ModelName    string
	Available    bool
	Quality      int
}

type CoderCapabilityPlan struct {
	Name      string
	Quality   int
	Available bool
}

type CoderSetupPlan struct {
	Name                        string
	Enabled                     bool
	DisplayName                 string
	Provider                    string
	Model                       string
	UseLightMemory              bool
	InitializeSharedLightMemory bool
	SharedLightMemoryMaxTurns   int
}

const DefaultLightMemoryMaxTurns = 3

var canonicalCoderSlotNames = []string{"coder1", "coder2", "coder3", "coder4"}

func NormalizeCoderSlotName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func CoderSlotIndex(name string) int {
	normalized := NormalizeCoderSlotName(name)
	for i, slotName := range canonicalCoderSlotNames {
		if normalized == slotName {
			return i
		}
	}
	return -1
}

func CoderProviderIsExternal(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "local_openai", "ollama":
		return false
	default:
		return true
	}
}

func BuildExternalCoderPolicy(coders []CoderSlotConfig) map[string]bool {
	policy := make(map[string]bool, len(coders))
	for _, coder := range coders {
		name := NormalizeCoderSlotName(coder.Name)
		if name == "" {
			continue
		}
		policy[name] = CoderProviderIsExternal(coder.Provider)
	}
	return policy
}

func NormalizeLightMemoryMaxTurns(maxTurns int) int {
	if maxTurns <= 0 {
		return DefaultLightMemoryMaxTurns
	}
	return maxTurns
}

func BuildCoderSetupPlans(coders []CoderSlotConfig) []CoderSetupPlan {
	plans := make([]CoderSetupPlan, 0, len(coders))
	sharedLightMemoryInitialized := false
	for _, coder := range coders {
		name := NormalizeCoderSlotName(coder.Name)
		if name == "" {
			continue
		}
		plan := CoderSetupPlan{
			Name:        name,
			Enabled:     coder.Enabled,
			DisplayName: strings.TrimSpace(coder.DisplayName),
			Provider:    strings.TrimSpace(coder.Provider),
			Model:       strings.TrimSpace(coder.Model),
		}
		if coder.Enabled && coder.LightMemoryEnabled {
			plan.UseLightMemory = true
			plan.SharedLightMemoryMaxTurns = NormalizeLightMemoryMaxTurns(coder.LightMemoryMaxTurns)
			if !sharedLightMemoryInitialized {
				plan.InitializeSharedLightMemory = true
				sharedLightMemoryInitialized = true
			}
		}
		plans = append(plans, plan)
	}
	return plans
}

func BuildCoderCapabilityPlans(llms []LLMCapability, coders []CoderSlotConfig, qualityOverrides map[string]int) []CoderCapabilityPlan {
	detected := make(map[string]LLMCapability, len(llms))
	for _, llm := range llms {
		detected[llm.ProviderName+"/"+llm.ModelName] = llm
	}

	providerDefault := map[string]int{
		"claude":   5,
		"openai":   4,
		"deepseek": 3,
		"ollama":   2,
	}

	plans := make([]CoderCapabilityPlan, 0, len(coders))
	anyUsable := false
	for _, coder := range coders {
		var quality int
		var available bool
		if llm, ok := detected[coder.Provider+"/"+coder.Model]; ok {
			quality = llm.Quality
			available = coder.Enabled && llm.Available
		} else {
			quality = qualityOverrides[coder.Model]
			if quality == 0 {
				quality = providerDefault[coder.Provider]
			}
			available = coder.Enabled && coder.APIKey != ""
		}
		if quality > 0 {
			anyUsable = true
		}
		plans = append(plans, CoderCapabilityPlan{
			Name:      coder.Name,
			Quality:   quality,
			Available: available,
		})
	}
	if !anyUsable {
		return nil
	}
	return plans
}
