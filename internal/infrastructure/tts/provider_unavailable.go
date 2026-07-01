package tts

import (
	"context"
	"fmt"
)

type unavailableProvider struct {
	name   string
	reason string
}

func NewUnavailableProvider(name, reason string) Provider {
	return unavailableProvider{name: name, reason: reason}
}

func (p unavailableProvider) Name() string {
	return p.name
}

func (p unavailableProvider) Synthesize(_ context.Context, _ SynthesisInput) (SynthesisOutput, error) {
	return SynthesisOutput{}, fmt.Errorf("%w: %s", ErrProviderUnavailable, p.reason)
}
