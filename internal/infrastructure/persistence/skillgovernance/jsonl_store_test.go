package skillgovernance

import (
	"context"
	"testing"
	"time"

	domainskill "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/skillgovernance"
)

func TestJSONLStoreSaveAndListSkillGovernanceRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveSkillManifest(ctx, domainskill.SkillManifest{
		SkillID:   "core.pr-readiness",
		Name:      "PR Readiness",
		Scope:     domainskill.ScopeCore,
		Version:   "1.0.0",
		Path:      "skills/core/pr-readiness",
		Enabled:   true,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("SaveSkillManifest failed: %v", err)
	}
	if err := store.SaveSkillTriggerLog(ctx, domainskill.SkillTriggerLog{
		EventID:     "evt_skill_1",
		SkillID:     "core.pr-readiness",
		TriggerType: "keyword",
		Status:      domainskill.TriggerStatusTriggered,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveSkillTriggerLog failed: %v", err)
	}
	if err := store.SaveSkillChangeLog(ctx, domainskill.SkillChangeLog{
		ChangeID:   "chg_1",
		SkillID:    "core.pr-readiness",
		OldVersion: "1.0.0",
		NewVersion: "1.0.1",
		EvalResult: "passed",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveSkillChangeLog failed: %v", err)
	}
	if err := store.SaveContributionGateLog(ctx, domainskill.ContributionGateLog{
		EventID:             "evt_contrib_1",
		Repo:                "example/repo",
		ExistingPRsChecked:  true,
		RealProblemVerified: false,
		GateStatus:          domainskill.GateStatusBlocked,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SaveContributionGateLog failed: %v", err)
	}
	if err := store.SaveExternalPRSubmitRecord(ctx, domainskill.ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		ApprovalStatus:      "approved",
		HumanApproved:       true,
		SubmitStatus:        domainskill.ExternalPRSubmitStatusBlocked,
		FailureReason:       "external PR adapter is not configured",
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SaveExternalPRSubmitRecord failed: %v", err)
	}
	if err := store.SaveCoderTranscriptEntry(ctx, domainskill.CoderTranscriptEntry{
		EventID:   "evt_coder_transcript_1",
		JobID:     "job-1",
		Route:     "CODE3",
		Agent:     "Coder",
		Role:      "coder",
		Segment:   "plan",
		Text:      "complete diff を提示して Human approval を待つ",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveCoderTranscriptEntry failed: %v", err)
	}

	manifests, err := store.ListSkillManifests(ctx, 10)
	if err != nil || len(manifests) != 1 || manifests[0].SkillID != "core.pr-readiness" {
		t.Fatalf("manifests=%#v err=%v", manifests, err)
	}
	triggers, err := store.ListSkillTriggerLogs(ctx, 10)
	if err != nil || len(triggers) != 1 || triggers[0].EventID != "evt_skill_1" {
		t.Fatalf("triggers=%#v err=%v", triggers, err)
	}
	changes, err := store.ListSkillChangeLogs(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].ChangeID != "chg_1" {
		t.Fatalf("changes=%#v err=%v", changes, err)
	}
	gates, err := store.ListContributionGateLogs(ctx, 10)
	if err != nil || len(gates) != 1 || gates[0].EventID != "evt_contrib_1" {
		t.Fatalf("gates=%#v err=%v", gates, err)
	}
	submits, err := store.ListExternalPRSubmitRecords(ctx, 10)
	if err != nil || len(submits) != 1 || submits[0].SubmitID != "submit_1" {
		t.Fatalf("submits=%#v err=%v", submits, err)
	}
	transcripts, err := store.ListCoderTranscriptEntries(ctx, 10)
	if err != nil || len(transcripts) != 1 || transcripts[0].EventID != "evt_coder_transcript_1" {
		t.Fatalf("transcripts=%#v err=%v", transcripts, err)
	}
}

func TestJSONLStoreMissingFilesReturnEmptyLists(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	if items, err := store.ListSkillManifests(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("manifests=%#v err=%v", items, err)
	}
	if items, err := store.ListSkillTriggerLogs(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("triggers=%#v err=%v", items, err)
	}
	if items, err := store.ListSkillChangeLogs(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("changes=%#v err=%v", items, err)
	}
	if items, err := store.ListContributionGateLogs(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("gates=%#v err=%v", items, err)
	}
	if items, err := store.ListExternalPRSubmitRecords(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("submits=%#v err=%v", items, err)
	}
	if items, err := store.ListCoderTranscriptEntries(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("transcripts=%#v err=%v", items, err)
	}
}

func TestJSONLStoreListSkillManifestsReturnsLatestPerSkill(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	base := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

	if err := store.SaveSkillManifest(ctx, domainskill.SkillManifest{
		SkillID:   "core.codebase-complexity-hotspot",
		Name:      "Complexity Hotspot",
		Scope:     domainskill.ScopeCore,
		Version:   "1.0.0",
		Path:      "skills/core/complexity-hotspot",
		Enabled:   true,
		UpdatedAt: base,
	}); err != nil {
		t.Fatalf("SaveSkillManifest initial failed: %v", err)
	}
	if err := store.SaveSkillManifest(ctx, domainskill.SkillManifest{
		SkillID:   "core.revenue-operating-workflow",
		Name:      "Revenue Operating Workflow",
		Scope:     domainskill.ScopeCore,
		Version:   "1.0.0",
		Path:      "skills/core/revenue-operating-workflow",
		Enabled:   true,
		UpdatedAt: base.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveSkillManifest other failed: %v", err)
	}
	if err := store.SaveSkillManifest(ctx, domainskill.SkillManifest{
		SkillID:   "core.codebase-complexity-hotspot",
		Name:      "Complexity Hotspot",
		Scope:     domainskill.ScopeCore,
		Version:   "1.0.1",
		Path:      "skills/core/complexity-hotspot",
		Enabled:   true,
		UpdatedAt: base.Add(2 * time.Minute),
	}); err != nil {
		t.Fatalf("SaveSkillManifest latest failed: %v", err)
	}

	manifests, err := store.ListSkillManifests(ctx, 10)
	if err != nil {
		t.Fatalf("ListSkillManifests failed: %v", err)
	}
	if len(manifests) != 2 {
		t.Fatalf("expected latest manifest per skill, got %#v", manifests)
	}
	if manifests[0].SkillID != "core.codebase-complexity-hotspot" || manifests[0].Version != "1.0.1" {
		t.Fatalf("latest complexity manifest first = %#v", manifests[0])
	}
	if manifests[1].SkillID != "core.revenue-operating-workflow" {
		t.Fatalf("second manifest = %#v", manifests[1])
	}
}
