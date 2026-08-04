package main

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestLiveStoryCodexExeProducesThreeReadyEpisodes(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_STORY_CODEX") != "1" {
		t.Skip("set RENCROW_LIVE_STORY_CODEX=1 to run real CodexExe story probes")
	}
	packageDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs(filepath.Join(packageDir, "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	workspacePrompts := os.Getenv("RENCROW_LIVE_STORY_WORKSPACE_PROMPTS")
	prompts := config.LoadPrompts(filepath.Join(repoRoot, "prompts"), workspacePrompts)
	personas := config.BuildIdleChatAgentPrompts(prompts)
	runner := tools.NewCodexExecRunner("codex", repoRoot, "read-only", "", 20*time.Minute, 256*1024, 2*1024*1024, true)
	target := 3
	if rawTarget := os.Getenv("RENCROW_LIVE_STORY_TARGET"); rawTarget != "" {
		if parsed, parseErr := strconv.Atoi(rawTarget); parseErr == nil && parsed > 0 {
			target = parsed
		}
	}
	service := idlechat.NewPersistentStoryEpisodeService(
		filepath.Join(t.TempDir(), "story_episodes.jsonl"),
		target,
		storyCodexExeGenerator{runner: runner},
		personas,
	)

	ctx := context.Background()
	var lastErr error
	for cycle := 1; cycle <= 3 && service.Snapshot().Ready < target; cycle++ {
		if err := service.PrepareToTarget(ctx); err != nil {
			lastErr = err
			t.Logf("prepare cycle %d incomplete: %v", cycle, err)
		}
		if service.Snapshot().Ready >= target {
			break
		}
		if err := service.RepairNeedsRepair(ctx); err != nil {
			lastErr = err
			t.Logf("repair cycle %d incomplete: %v", cycle, err)
		}
	}
	snapshot := service.Snapshot()
	if snapshot.Ready < target {
		t.Fatalf("ready=%d want>=%d last_error=%v snapshot=%+v", snapshot.Ready, target, lastErr, snapshot)
	}
	for _, episode := range snapshot.Episodes {
		if episode.ProductionStatus == idlechat.StoryProductionReady && !episode.Validation.Valid {
			t.Fatalf("ready episode is invalid: %+v", episode)
		}
	}
}
