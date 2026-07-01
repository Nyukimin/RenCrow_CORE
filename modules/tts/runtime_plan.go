package tts

import (
	"fmt"
	"strings"
	"time"
)

const (
	RuntimeProviderIrodori = "irodori"
	RuntimeProviderAzure   = "azure"
	RuntimeProviderEleven  = "eleven"
)

type RuntimeConfig struct {
	Enabled          bool
	VoiceID          string
	ProviderPriority []string
	PlaybackCommands []CommandSpec
	Irodori          IrodoriRuntimeConfig
}

type IrodoriRuntimeConfig struct {
	Enabled               bool
	BaseURL               string
	EndpointPath          string
	VoiceID               string
	VoiceName             string
	ReferenceAudio        string
	ReferenceAudioURL     string
	TimeoutSec            int
	Checkpoint            string
	ModelDevice           string
	ModelPrecision        string
	CodecDevice           string
	CodecPrecision        string
	EnableWatermark       bool
	NumSteps              int
	NumCandidates         int
	SeedRaw               string
	CFGGuidanceMode       string
	CFGScaleText          float64
	CFGScaleSpeaker       float64
	CFGScaleRaw           string
	CFGMinT               float64
	CFGMaxT               float64
	ContextKVCache        bool
	TruncationFactorRaw   string
	RescaleKRaw           string
	RescaleSigmaRaw       string
	SpeakerKVScaleRaw     string
	SpeakerKVMinTRaw      string
	SpeakerKVMaxLayersRaw string
}

type CommandSpec struct {
	Name string
	Args []string
}

type RuntimeProviderPlan struct {
	Name        string
	Available   bool
	Unavailable string
	Irodori     IrodoriProviderPlan
}

type RuntimeProviderSelectionLogInput struct {
	Name     string
	BaseURL  string
	Endpoint string
}

type IrodoriProviderPlan struct {
	BaseURL               string
	EndpointPath          string
	VoiceID               string
	VoiceName             string
	ReferenceAudio        string
	ReferenceAudioURL     string
	Timeout               time.Duration
	Checkpoint            string
	ModelDevice           string
	ModelPrecision        string
	CodecDevice           string
	CodecPrecision        string
	EnableWatermark       bool
	NumSteps              int
	NumCandidates         int
	SeedRaw               string
	CFGGuidanceMode       string
	CFGScaleText          float64
	CFGScaleSpeaker       float64
	CFGScaleRaw           string
	CFGMinT               float64
	CFGMaxT               float64
	ContextKVCache        bool
	TruncationFactorRaw   string
	RescaleKRaw           string
	RescaleSigmaRaw       string
	SpeakerKVScaleRaw     string
	SpeakerKVMinTRaw      string
	SpeakerKVMaxLayersRaw string
}

func RuntimeProviderPriority(cfg RuntimeConfig) []string {
	if len(cfg.ProviderPriority) > 0 {
		out := make([]string, 0, len(cfg.ProviderPriority))
		for _, name := range cfg.ProviderPriority {
			if normalized := normalizeProviderName(name); normalized != "" {
				out = append(out, normalized)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{RuntimeProviderIrodori}
}

func BuildCommandSpecs(commands []CommandSpec) []CommandSpec {
	out := make([]CommandSpec, 0, len(commands))
	for _, command := range commands {
		name := strings.TrimSpace(command.Name)
		if name == "" {
			continue
		}
		out = append(out, CommandSpec{Name: name, Args: append([]string(nil), command.Args...)})
	}
	return out
}

func ChooseRuntimeVoiceID(cfg RuntimeConfig) string {
	if cfg.Irodori.Enabled && strings.TrimSpace(cfg.Irodori.VoiceID) != "" {
		return cfg.Irodori.VoiceID
	}
	return cfg.VoiceID
}

func RuntimeProviderSelectionLogMessage(input RuntimeProviderSelectionLogInput) (string, bool) {
	switch normalizeProviderName(input.Name) {
	case RuntimeProviderIrodori:
		return fmt.Sprintf("TTS Irodori bridge enabled (base=%s endpoint=%s)", strings.TrimSpace(input.BaseURL), strings.TrimSpace(input.Endpoint)), true
	default:
		return "", false
	}
}

func BuildRuntimeProviderPlan(cfg RuntimeConfig, name string, includeUnavailable bool) (RuntimeProviderPlan, bool) {
	normalized := normalizeProviderName(name)
	switch normalized {
	case RuntimeProviderIrodori:
		if cfg.Irodori.Enabled && strings.TrimSpace(cfg.Irodori.BaseURL) != "" {
			return RuntimeProviderPlan{
				Name:      RuntimeProviderIrodori,
				Available: true,
				Irodori:   buildIrodoriProviderPlan(cfg.Irodori),
			}, true
		}
		if includeUnavailable {
			return RuntimeProviderPlan{
				Name:        RuntimeProviderIrodori,
				Unavailable: "irodori is not configured",
			}, true
		}
	case RuntimeProviderAzure, RuntimeProviderEleven:
		if includeUnavailable {
			return RuntimeProviderPlan{
				Name:        normalized,
				Unavailable: normalized + " provider is not configured yet",
			}, true
		}
	}
	return RuntimeProviderPlan{}, false
}

func BuildRuntimeProviderPlans(cfg RuntimeConfig, includeUnavailable bool) []RuntimeProviderPlan {
	priority := RuntimeProviderPriority(cfg)
	plans := make([]RuntimeProviderPlan, 0, len(priority))
	for _, name := range priority {
		plan, ok := BuildRuntimeProviderPlan(cfg, name, includeUnavailable)
		if ok {
			plans = append(plans, plan)
		}
	}
	return plans
}

func FirstRuntimeProviderPlan(cfg RuntimeConfig, includeUnavailable bool) (RuntimeProviderPlan, bool) {
	for _, plan := range BuildRuntimeProviderPlans(cfg, includeUnavailable) {
		return plan, true
	}
	return RuntimeProviderPlan{}, false
}

func buildIrodoriProviderPlan(cfg IrodoriRuntimeConfig) IrodoriProviderPlan {
	return IrodoriProviderPlan{
		BaseURL:               cfg.BaseURL,
		EndpointPath:          cfg.EndpointPath,
		VoiceID:               cfg.VoiceID,
		VoiceName:             cfg.VoiceName,
		ReferenceAudio:        cfg.ReferenceAudio,
		ReferenceAudioURL:     cfg.ReferenceAudioURL,
		Timeout:               time.Duration(cfg.TimeoutSec) * time.Second,
		Checkpoint:            cfg.Checkpoint,
		ModelDevice:           cfg.ModelDevice,
		ModelPrecision:        cfg.ModelPrecision,
		CodecDevice:           cfg.CodecDevice,
		CodecPrecision:        cfg.CodecPrecision,
		EnableWatermark:       cfg.EnableWatermark,
		NumSteps:              cfg.NumSteps,
		NumCandidates:         cfg.NumCandidates,
		SeedRaw:               cfg.SeedRaw,
		CFGGuidanceMode:       cfg.CFGGuidanceMode,
		CFGScaleText:          cfg.CFGScaleText,
		CFGScaleSpeaker:       cfg.CFGScaleSpeaker,
		CFGScaleRaw:           cfg.CFGScaleRaw,
		CFGMinT:               cfg.CFGMinT,
		CFGMaxT:               cfg.CFGMaxT,
		ContextKVCache:        cfg.ContextKVCache,
		TruncationFactorRaw:   cfg.TruncationFactorRaw,
		RescaleKRaw:           cfg.RescaleKRaw,
		RescaleSigmaRaw:       cfg.RescaleSigmaRaw,
		SpeakerKVScaleRaw:     cfg.SpeakerKVScaleRaw,
		SpeakerKVMinTRaw:      cfg.SpeakerKVMinTRaw,
		SpeakerKVMaxLayersRaw: cfg.SpeakerKVMaxLayersRaw,
	}
}

func normalizeProviderName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
