package channels_test

import (
	"context"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/discord"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/slack"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels/telegram"
)

func TestStubAdapters_ProbeRequiresToken(t *testing.T) {
	adapters := []struct {
		name string
		fn   func() error
	}{
		{name: "telegram", fn: func() error { return telegram.NewAdapter("").Probe(context.Background()) }},
		{name: "discord", fn: func() error { return discord.NewAdapter("").Probe(context.Background()) }},
		{name: "slack", fn: func() error { return slack.NewAdapter("", "").Probe(context.Background()) }},
	}
	for _, tt := range adapters {
		if err := tt.fn(); err == nil {
			t.Fatalf("%s probe should fail without token", tt.name)
		}
	}
}
