package worker

import "strings"

type CoderSlotConfig struct {
	Name                string
	DisplayName         string
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
	detected := make(map[string]LLMCapability, len(llms)*2)
	for _, llm := range llms {
		if key := NormalizeCoderSlotName(llm.ProviderName); key != "" {
			detected[key] = llm
		}
		if key := NormalizeCoderSlotName(llm.ModelName); key != "" {
			detected[key] = llm
		}
	}

	plans := make([]CoderCapabilityPlan, 0, len(coders))
	anyUsable := false
	for _, coder := range coders {
		var quality int
		var available bool
		name := NormalizeCoderSlotName(coder.Name)
		if llm, ok := detected[name]; ok {
			quality = llm.Quality
			available = coder.Enabled && llm.Available
		} else {
			quality = qualityOverrides[name]
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
