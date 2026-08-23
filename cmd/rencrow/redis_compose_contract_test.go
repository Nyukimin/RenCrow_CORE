package main

import (
	"strings"
	"testing"
)

func TestRedisComposeHostPublicationIsLoopbackOnly(t *testing.T) {
	compose := readRepoText(t, "docker-compose.infra.yml")
	mappings := redisComposePortMappings(compose)
	if len(mappings) != 1 || mappings[0] != "127.0.0.1:6379:6379" {
		t.Fatalf("Redis host port mappings = %#v, want [127.0.0.1:6379:6379]", mappings)
	}
}

func redisComposePortMappings(compose string) []string {
	lines := strings.Split(strings.ReplaceAll(compose, "\r\n", "\n"), "\n")
	redisIndent := -1
	portsIndent := -1
	inRedis := false
	inPorts := false
	var mappings []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))

		if !inRedis {
			if trimmed == "redis:" {
				inRedis = true
				redisIndent = indent
			}
			continue
		}
		if indent <= redisIndent {
			break
		}

		if !inPorts {
			if trimmed == "ports:" {
				inPorts = true
				portsIndent = indent
			}
			continue
		}
		if indent <= portsIndent {
			inPorts = false
			continue
		}
		if !strings.HasPrefix(trimmed, "-") {
			continue
		}

		mapping := strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
		mapping = strings.Trim(mapping, "\"'")
		if mapping != "" {
			mappings = append(mappings, mapping)
		}
	}

	return mappings
}
