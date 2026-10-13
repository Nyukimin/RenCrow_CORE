package config

import (
	"log"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// unknownSTTConfigKeys lists keys under `stt:` that STTConfig does not read.
// Deployed configs still carry keys of the removed gateway-less STT route
// (provider, model, provider_url, stream_url, ...). CORE ignores them; reporting
// them keeps the file honest without stopping a running installation.
func unknownSTTConfigKeys(root *yaml.Node) []string {
	stt := yamlMappingValue(yamlDocumentMapping(root), "stt")
	if stt == nil || stt.Kind != yaml.MappingNode {
		return nil
	}
	known := yamlFieldNames(reflect.TypeOf(STTConfig{}))
	var unknown []string
	for i := 0; i+1 < len(stt.Content); i += 2 {
		if key := stt.Content[i].Value; !known[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

func yamlFieldNames(t reflect.Type) map[string]bool {
	names := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("yaml"), ",")[0]
		if name != "" && name != "-" {
			names[name] = true
		}
	}
	return names
}

// warnSTTConfig logs STT settings that are accepted but probably wrong. It names
// keys and numbers only, never values that could carry a URL or credential.
func warnSTTConfig(root *yaml.Node, cfg *Config) {
	for _, key := range unknownSTTConfigKeys(root) {
		log.Printf("[Config] stt.%s is not a CORE setting and is ignored; remove it from the config file", key)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.STT.BusyPolicy)) {
	case "direct", "reject", "queue_latest":
	default:
		log.Printf("[Config] stt.busy_policy is not recognised (use direct, reject or queue_latest); direct is used")
	}
	if cfg.STT.Enabled && cfg.STT.TimeoutMS < cfg.STT.MaxUtteranceMS {
		log.Printf("[Config] stt.timeout_ms (%d) is shorter than stt.max_utterance_ms (%d); a long utterance can time out before the engine answers", cfg.STT.TimeoutMS, cfg.STT.MaxUtteranceMS)
	}
}
