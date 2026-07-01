package config

import (
	"net/url"
	"strings"
)

func (c *Config) LocalLLMWarmupEnabled() bool {
	return c.LocalLLM.Warmup != nil && *c.LocalLLM.Warmup
}

func shouldEnableLocalTLSSkipVerify(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}
