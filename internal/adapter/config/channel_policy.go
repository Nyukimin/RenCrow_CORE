package config

import (
	"strings"

	domainsecurity "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/security"
)

func ResolveChannelPolicyConfig(cfg ChannelPolicyConfig) (domainsecurity.ChannelPolicy, bool) {
	if !cfg.Enabled {
		return domainsecurity.ChannelPolicy{}, false
	}
	return domainsecurity.ChannelPolicy{
		AllowDM:        boolValue(cfg.AllowDM, true),
		AllowGroups:    boolValue(cfg.AllowGroups, false),
		AllowedSenders: compactStrings(cfg.AllowedSenders),
		PairedGroups:   compactStrings(cfg.PairedGroups),
	}, true
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}
