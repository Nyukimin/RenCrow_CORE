package main

import (
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	ttsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tts"
)

func buildTTSCommandSpecs(cfg *config.Config) []ttsinfra.CommandSpec {
	if cfg == nil {
		return nil
	}
	cmds := make([]ttsinfra.CommandSpec, 0, len(cfg.TTS.PlaybackCommands))
	for _, command := range cfg.TTS.PlaybackCommands {
		if command.Name == "" {
			continue
		}
		cmds = append(cmds, ttsinfra.CommandSpec{Name: command.Name, Args: append([]string(nil), command.Args...)})
	}
	return cmds
}

func chooseTTSVoiceID(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.TTS.VoiceID
}
