package main

import (
	"log"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	ttsinfra "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tts"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

func buildTTSCommandSpecs(cfg *config.Config) []ttsinfra.CommandSpec {
	if cfg == nil {
		return nil
	}
	moduleCommands := moduletts.BuildCommandSpecs(ttsRuntimeConfigFromAppConfig(cfg).PlaybackCommands)
	cmds := make([]ttsinfra.CommandSpec, 0, len(moduleCommands))
	for _, command := range moduleCommands {
		cmds = append(cmds, ttsinfra.CommandSpec{Name: command.Name, Args: append([]string(nil), command.Args...)})
	}
	return cmds
}

func chooseTTSVoiceID(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return moduletts.ChooseRuntimeVoiceID(ttsRuntimeConfigFromAppConfig(cfg))
}

func logTTSProviderSelection(sel ttsProviderSelection) {
	if msg, ok := moduletts.RuntimeProviderSelectionLogMessage(moduletts.RuntimeProviderSelectionLogInput{
		Name:     sel.Name,
		BaseURL:  sel.BaseURL,
		Endpoint: sel.Endpoint,
	}); ok {
		log.Print(msg)
	}
}
