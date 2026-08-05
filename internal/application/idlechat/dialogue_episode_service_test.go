package idlechat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDialogueEpisodeRepairsOnlySuffixAndPreservesAcceptedPrefixID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	generator := &queuedIdleChatCodexGenerator{responses: []string{
		`{"turns":[{"speaker":"mio","display_text":"防災設備の赤いランプを店員が見落とす場面から、判断の難しさが見えるね。","speech_text":"防災設備の赤いランプを店員が見落とす場面から、判断の難しさが見えるね。"},{"speaker":"mio","display_text":"順番を壊した発話です。","speech_text":"順番を壊した発話です。"}]}`,
		`{"turns":[{"speaker":"shiro","display_text":"その赤いランプを誰が確認したか記録すれば、店での判断の責任を分けられます。","speech_text":"その赤いランプを誰が確認したか記録すれば、店での判断の責任を分けられます。"}]}`,
	}}
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	service := NewPersistentDialogueEpisodeService(path, generator, map[string]string{"mio": "Mio canonical", "shiro": "Shiro canonical"}, config)
	result := TopicGenerationResult{
		Topic: "防災設備を店頭に入れるとき誰が最後の判断を持つか", Category: TopicCategorySingle, Strategy: string(StrategySingleGenre),
		InterestingnessAxis: "観察", Seed: TopicSeed{Category: TopicCategorySingle, Genre1: "防災"},
	}
	artifact, err := service.Prepare(context.Background(), "idle-dialogue-test", result, 2)
	if err != nil {
		t.Fatalf("dialogue preparation failed: %v artifact=%+v", err, artifact)
	}
	if artifact.ProductionStatus != DialogueProductionReady || artifact.Revision != 2 || artifact.FixedPrefixLength != 1 || artifact.RepairFromTurn != 2 {
		t.Fatalf("artifact = %+v", artifact)
	}
	if artifact.InitiatedBy != "shiro" {
		t.Fatalf("dialogue initiator = %q, want shiro", artifact.InitiatedBy)
	}
	if len(artifact.Turns) != 2 || artifact.Turns[0].MessageID == "" || artifact.Turns[1].MessageID == "" {
		t.Fatalf("turns = %+v", artifact.Turns)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("stored revisions = %d, want initial invalid and repaired ready", len(lines))
	}
	var initial DialogueEpisodeArtifact
	if err := decodeStoryJSON(lines[0], &initial); err != nil {
		t.Fatal(err)
	}
	if initial.Turns[0].MessageID != artifact.Turns[0].MessageID {
		t.Fatalf("accepted prefix message id changed: %q -> %q", initial.Turns[0].MessageID, artifact.Turns[0].MessageID)
	}
	if len(generator.requests) != 2 || !strings.Contains(generator.requests[0], "Mio canonical") || !strings.Contains(generator.requests[0], "Shiro canonical") || !strings.Contains(generator.requests[1], "turn 2より前") {
		t.Fatalf("unexpected CodexExe requests: %#v", generator.requests)
	}
}

func TestValidateDialogueEpisodeReportsFirstInvalidTurn(t *testing.T) {
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	result := TopicGenerationResult{Topic: "防災設備の判断", Category: TopicCategorySingle, Strategy: string(StrategySingleGenre), InterestingnessAxis: "観察"}
	plan := NewDialogueDirector(config).BuildArcPlan(result)
	plan.TurnPlans = buildDialogueTurnPlans(2, dialogueCategorySpec(TopicCategorySingle))
	artifact := DialogueEpisodeArtifact{
		SessionID: "validate-dialogue", TopicResult: result, ArcPlan: plan, Participants: []string{"mio", "shiro"},
		Turns: []DialogueEpisodeTurn{
			{TurnIndex: 1, Speaker: "mio", DisplayText: "防災設備の赤いランプを店で確認する場面から始めよう。", SpeechText: "防災設備の赤いランプを店で確認する場面から始めよう。"},
			{TurnIndex: 2, Speaker: "mio", DisplayText: "順番を壊した発話です。", SpeechText: "順番を壊した発話です。"},
		},
	}
	validation := ValidateDialogueEpisode(artifact, config)
	if validation.Valid || validation.FirstInvalidTurn != 2 {
		t.Fatalf("validation = %+v", validation)
	}
}
