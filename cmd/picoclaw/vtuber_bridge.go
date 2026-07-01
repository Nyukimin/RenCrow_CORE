package main

import (
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	vtuberinfra "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/vtuber"
)

func buildVTuberBridge(cfg *config.Config) orchestrator.VTuberBridge {
	if cfg == nil || !cfg.VTuber.Enabled {
		return nil
	}
	characters := make(map[string]vtuberinfra.CharacterConfig, len(cfg.VTuber.Characters))
	for name, ch := range cfg.VTuber.Characters {
		key := strings.TrimSpace(strings.ToLower(name))
		if key == "" {
			continue
		}
		characters[key] = vtuberinfra.CharacterConfig{
			AudioOutput:   ch.AudioOutput,
			Host:          ch.VTSHost,
			Port:          ch.VTSPort,
			ExpressionMap: ch.ExpressionMap,
		}
	}
	if len(characters) == 0 {
		log.Printf("WARN: VTuber bridge disabled (no characters configured)")
		return nil
	}
	bridge := vtuberinfra.NewClientBridge(vtuberinfra.ClientConfig{
		ConnectTimeout: time.Duration(cfg.VTuber.ConnectTimeout) * time.Millisecond,
		WriteTimeout:   time.Duration(cfg.VTuber.WriteTimeout) * time.Millisecond,
		Characters:     characters,
	})
	log.Printf("VTuber bridge enabled (characters=%d)", len(characters))
	return bridge
}
