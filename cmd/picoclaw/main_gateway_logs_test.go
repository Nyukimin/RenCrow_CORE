package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
)

func TestGatewayHealthURL(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{Host: "0.0.0.0", Port: 18790}}
	got := gatewayHealthURL(cfg)
	want := "http://127.0.0.1:18790/health"
	if got != want {
		t.Fatalf("gatewayHealthURL()=%s, want %s", got, want)
	}
}

func TestPrintLastLines(t *testing.T) {
	p := filepath.Join(t.TempDir(), "app.log")
	data := "1\n2\n3\n4\n5\n"
	if err := os.WriteFile(p, []byte(data), 0644); err != nil {
		t.Fatalf("write log failed: %v", err)
	}
	if err := printLastLines(p, 3); err != nil {
		t.Fatalf("printLastLines failed: %v", err)
	}
}
