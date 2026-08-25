package idlechat

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestTopicGenerationPromptNamesSingleGenreLiteral(t *testing.T) {
	prompt := buildTopicGenerationPromptForTest(t, TopicCategorySingle, TopicSeed{
		Category: TopicCategorySingle,
		Genre1:   "生成AI",
	})

	want := "- topic 文字列には「生成AI」をそのまま1回以上含める。"
	if !strings.Contains(prompt, want) {
		t.Fatalf("single prompt must name the exact genre value in its topic requirement; missing %q in:\n%s", want, prompt)
	}
}

func TestTopicGenerationPromptNamesDoubleGenreLiterals(t *testing.T) {
	prompt := buildTopicGenerationPromptForTest(t, TopicCategoryDouble, TopicSeed{
		Category: TopicCategoryDouble,
		Genre1:   "生成AI",
		Genre2:   "防災",
	})

	want := "- topic 文字列には「生成AI」と「防災」をそれぞれそのまま1回以上含める。"
	if !strings.Contains(prompt, want) {
		t.Fatalf("double prompt must name both exact genre values in its topic requirement; missing %q in:\n%s", want, prompt)
	}
}

func buildTopicGenerationPromptForTest(t *testing.T, category TopicCategory, seed TopicSeed) string {
	t.Helper()
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Join(filepath.Dir(sourceFile), "..", "..", "..")
	generator := NewTopicGenerator(nil, TopicGenerationConfig{
		CandidatesPerAttempt: 1,
		PromptPaths: TopicGenerationPromptPaths{
			Common: filepath.Join(root, "prompts", "idle_chat", "topic_generator_common.md"),
			Single: filepath.Join(root, "prompts", "idle_chat", "topic_generator_single.md"),
			Double: filepath.Join(root, "prompts", "idle_chat", "topic_generator_double.md"),
		},
	})
	prompt, err := generator.BuildGenerationPrompt(category, seed, nil, 1, nil)
	if err != nil {
		t.Fatalf("BuildGenerationPrompt() error = %v", err)
	}
	return prompt
}
