package agent

import "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"

const (
	defaultMioMaxTokens   = 512
	defaultMioTemperature = 0.7
)

// MioGenerationOptions controls request sampling for Mio's conversational replies.
type MioGenerationOptions struct {
	Stream         bool
	MaxTokens      int
	Temperature    float64
	TopP           *float64
	TopK           *int
	MinP           *float64
	Seed           *int64
	EnableThinking *bool
}

func defaultMioGenerationOptions() MioGenerationOptions {
	return MioGenerationOptions{
		MaxTokens:   defaultMioMaxTokens,
		Temperature: defaultMioTemperature,
	}
}

// WithGenerationOptions replaces Mio's generation policy while preserving legacy defaults.
func (m *MioAgent) WithGenerationOptions(options MioGenerationOptions) *MioAgent {
	if options.MaxTokens <= 0 {
		options.MaxTokens = defaultMioMaxTokens
	}
	if options.Temperature <= 0 {
		options.Temperature = defaultMioTemperature
	}
	m.generation = options
	return m
}

func (m *MioAgent) generationRequest(messages []llm.Message, onToken llm.StreamCallback) llm.GenerateRequest {
	if onToken == nil && m.generation.Stream {
		onToken = func(string) {}
	}
	options := make(map[string]any, 6)
	// CHATは応答速度と会話契約を優先し、設定値にかかわらずthinkingを使用しない。
	options["think"] = false
	options["chat_template_kwargs"] = map[string]any{
		"enable_thinking": false,
	}
	if m.generation.TopP != nil {
		options["top_p"] = *m.generation.TopP
	}
	if m.generation.TopK != nil {
		options["top_k"] = *m.generation.TopK
	}
	if m.generation.MinP != nil {
		options["min_p"] = *m.generation.MinP
	}
	if m.generation.Seed != nil {
		options["seed"] = *m.generation.Seed
	}
	return llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		Messages:        messages,
		MaxTokens:       m.generation.MaxTokens,
		Temperature:     m.generation.Temperature,
		ProviderOptions: options,
		OnToken:         onToken,
	})
}
