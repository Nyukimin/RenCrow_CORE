package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsRetiredDuckDBSettings(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "top level duckdb path",
			content: "server:\n  port: 8080\nduckdb_path: /state/memory.duckdb\n",
			want:    "duckdb_path",
		},
		{
			name:    "legacy database inventory",
			content: "server:\n  port: 8080\nstorage:\n  legacy_databases:\n    memory_duckdb: /state/memory.duckdb\n",
			want:    "storage.legacy_databases",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "retired") {
				t.Fatalf("LoadConfig() error=%v, want retired %q rejection", err, tt.want)
			}
		})
	}
}
