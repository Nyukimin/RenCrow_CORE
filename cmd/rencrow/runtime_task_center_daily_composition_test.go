package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func TestRuntimeTaskCenterDailyCompositionPreservesUnavailableOwnerCoverage(t *testing.T) {
	t.Setenv("RENCROW_MOVIE_CATALOG_BACKFILL_DISABLED", "")
	t.Setenv("RENCROW_MOVIE_CATALOG_BACKFILL", "1")
	t.Setenv("RENCROW_MOVIE_CATALOG_BACKFILL_INTERVAL_SEC", "300")
	for _, name := range []string{
		"RENCROW_MEMORY_LIFECYCLE_INTERVAL_MS",
		"RENCROW_MEMORY_LIFECYCLE_INTERVAL_SEC",
		"RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_MS",
		"RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC",
	} {
		t.Setenv(name, "")
	}
	t.Run("known_disabled_config", func(t *testing.T) {
		cfg := &config.Config{}
		cfg.Backup.CoreSource = "/private/backup/root"
		cfg.Backup.CoreSnapshotRoot = "/private/snapshots"
		assertRuntimeTaskCenterDailyComposition(t, cfg)
	})
	t.Run("nil_config", func(t *testing.T) {
		assertRuntimeTaskCenterDailyComposition(t, nil)
	})
}

func assertRuntimeTaskCenterDailyComposition(t *testing.T, cfg *config.Config) {
	t.Helper()
	deps := &Dependencies{}

	registerRuntimeBackupDailySource(deps, cfg)
	registerRuntimeLogMaintenanceDailySource(deps)
	registerRuntimeResilienceDailySource(deps)
	registerRuntimeTaskStoreRotationDailySource(deps, cfg, nil)
	registerRuntimeActionStoreRotationDailySource(deps, cfg, nil)
	registerRuntimePersonRelatedDailySource(deps, cfg, nil, nil, nil)
	registerRuntimeWorkstreamDailySource(deps, cfg, nil)
	registerRuntimeRevenueDailySource(deps, cfg)
	registerRuntimeDailyIntakeSource(deps, cfg, nil)
	registerRuntimeDailyNewsBriefSource(deps, nil)
	registerRuntimeMovieCatalogBackfillDailySource(deps, cfg, false)
	registerRuntimeIdleChatDailySource(deps, cfg)
	registerRuntimeMemoryDailySource(deps, cfg, nil, memoryLifecycleJobConfig{Interval: 24 * time.Hour})
	registerRuntimeArchiveParquetDailySource(deps)
	registerRuntimeTaskMeetingDailySource(deps, cfg)
	registerRuntimeHeartbeatDailySource(deps, cfg, nil)
	registerRuntimePronunciationDailySource(deps, cfg)

	finalizeTaskCenterDaily(deps)
	if deps.taskCenterCenter == nil {
		t.Fatal("Daily center did not build with unavailable optional owners")
	}
	scope, err := tool.NewToolExecutionScope(
		"daily-composition-test", tool.ActorKindUser, "test-user", "test-user",
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	capabilities, err := deps.taskCenterCenter.Capabilities(ctx)
	if err != nil {
		t.Fatalf("Capabilities() error = %v", err)
	}
	if capabilities.ManagedAvailable || !capabilities.DailyAvailable || capabilities.CanSubmit || capabilities.CanClaim {
		t.Fatalf("managed-nil/default-owner capabilities = %#v", capabilities)
	}
	result, err := deps.taskCenterCenter.ListDaily(ctx, taskcenter.DailyQuery{Limit: 100})
	if err != nil {
		t.Fatalf("ListDaily() error = %v", err)
	}
	if len(result.Definitions) == 0 {
		t.Fatal("unavailable optional owners were collapsed into an empty Daily result")
	}

	byKind := make(map[string]taskcenter.DailyDefinition, len(result.Definitions))
	byDefinitionID := make(map[string]taskcenter.DailyDefinition, len(result.Definitions))
	for _, definition := range result.Definitions {
		if _, exists := byKind[definition.KindID]; exists {
			t.Fatalf("duplicate Daily kind %q", definition.KindID)
		}
		byKind[definition.KindID] = definition
		byDefinitionID[definition.DefinitionID] = definition
		if definition.DataScope == tool.DataScopeUser && definition.UserID == "" {
			t.Fatalf("private Daily definition %q has no owner UserID", definition.DefinitionID)
		}
		if len(definition.SourceRefs) > 100 || len(definition.NativeRefs) > taskcenter.DailyMaxNativeRefs ||
			len(definition.LatestEvidence.Refs) > taskcenter.DailyMaxNativeRefs || len(definition.Occurrences) > taskcenter.DailyMaxOccurrences {
			t.Fatalf("Daily references exceed bounded projection for %q", definition.DefinitionID)
		}
		for _, occurrence := range definition.Occurrences {
			if len(occurrence.NativeRefs) > taskcenter.DailyMaxNativeRefs {
				t.Fatalf("Daily occurrence references exceed bounded projection for %q", definition.DefinitionID)
			}
		}
		encoded, marshalErr := json.Marshal(definition)
		if marshalErr != nil {
			t.Fatalf("marshal Daily definition %q: %v", definition.DefinitionID, marshalErr)
		}
		var object map[string]any
		if err := json.Unmarshal(encoded, &object); err != nil {
			t.Fatalf("unmarshal Daily definition %q: %v", definition.DefinitionID, err)
		}
		timing, ok := object["timing"].(map[string]any)
		if !ok {
			t.Fatalf("Daily definition %q has no timing object: %s", definition.DefinitionID, encoded)
		}
		for _, field := range []string{"timezone", "scheduled_start_at", "not_before", "allowed_windows", "deadline_at", "recurrence", "estimated_duration_seconds"} {
			value, exists := timing[field].(map[string]any)
			state, stateOK := value["state"].(string)
			sourceRef, sourceRefOK := value["source_ref"].(string)
			if !exists || !stateOK || strings.TrimSpace(state) == "" || !sourceRefOK || strings.TrimSpace(sourceRef) == "" {
				t.Fatalf("Daily definition %q timing.%s lacks state/source_ref: %s", definition.DefinitionID, field, encoded)
			}
		}
		if strings.Contains(string(encoded), "/private/backup/root") || strings.Contains(string(encoded), "/private/snapshots") {
			t.Fatalf("Daily definition %q exposed a configured path", definition.DefinitionID)
		}
		if hasDailyIssueCode(definition.Issues, taskcenter.DailyIssueInvalidProjection) {
			t.Fatalf("Daily definition %q contains invalid_projection", definition.DefinitionID)
		}
	}
	for _, kindID := range []string{
		"backup.external_maintenance",
		"maintenance.log_rotation",
		"maintenance.task_store_rotation",
		"maintenance.action_store_rotation",
		"ops.resilience_status",
		"memory.archive_export",
		"memory.profile_promotion",
		"memory.lifecycle_maintenance",
		"person_related.collection",
		"person_related.summary",
		"person_related.identity",
		"news.daily_seed_collection",
		"news.newsdb",
		"news.daily_brief",
		"knowledge_memory.daily_intake",
		"movie_catalog.backfill",
		"revenue.daily_report",
		"quality.tts_pronunciation_check",
		"workstream.heartbeat",
		"backlog.heartbeat_intake",
		"atlas.heartbeat_maturation",
		"economic.heartbeat_discovery",
		"heartbeat.tick",
		"heartbeat.gmail_intake",
		"heartbeat.x_bookmarks",
		"heartbeat.workspace_file",
		"idlechat.topic_stock",
		"idlechat.night_story",
		"ops.idlechat_watchdog",
		"meeting.daily_report",
	} {
		if _, ok := byKind[kindID]; !ok {
			t.Errorf("Daily owner kind %q is missing", kindID)
		}
	}
	storyStock, ok := byDefinitionID["idlechat.night_story_stock"]
	if !ok {
		t.Fatal("Daily definition \"idlechat.night_story_stock\" is missing")
	}
	if storyStock.KindID != "idlechat.night_story" {
		t.Fatalf("story stock definition kind_id = %q, want owner kind \"idlechat.night_story\"", storyStock.KindID)
	}
	if _, ok := byKind["agent.individual_routine"]; ok {
		t.Fatal("unowned Agent routine was represented as a runnable definition")
	}
	if strings.Contains(strings.ToLower(string(mustMarshalDaily(t, result))), "invalid_projection") {
		t.Fatal("Daily envelope contains invalid_projection")
	}
}

func hasDailyIssueCode(issues []taskcenter.DailyIssue, want taskcenter.DailyIssueCode) bool {
	for _, issue := range issues {
		if issue.Code == want {
			return true
		}
	}
	return false
}

func mustMarshalDaily(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
