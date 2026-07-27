package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCOREDoesNotOwnDirectImageBackend(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	directBackendDir := filepath.Join(repoRoot, "internal", "infrastructure", "comfyui")
	if _, err := os.Stat(directBackendDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CORE must not own a direct image backend implementation: %s", directBackendDir)
	}

	forbidden := map[string][]string{
		filepath.Join("cmd", "rencrow", "runtime_agents.go"): {
			"internal/infrastructure/comfyui",
			"comfyuiinfra.NewClient",
			"cfg.ComfyUI",
		},
		filepath.Join("internal", "adapter", "config", "config_types.go"): {
			`yaml:"comfyui"`,
		},
		filepath.Join("internal", "adapter", "config", "config_defaults.go"): {
			"c.ComfyUI",
		},
	}

	for relativePath, fragments := range forbidden {
		path := filepath.Join(repoRoot, relativePath)
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", relativePath, err)
		}
		for _, fragment := range fragments {
			if strings.Contains(string(content), fragment) {
				t.Errorf("%s still contains direct image backend dependency %q", relativePath, fragment)
			}
		}
	}
}
