package sandbox

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

func TestJSONLStoreSaveAndListSandboxes(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveSandbox(ctx, domainsandbox.SandboxRecord{
		SandboxID: "sbx_1",
		Type:      "code",
		Path:      "sandbox/ws/sbx_1",
		Status:    domainsandbox.SandboxStatusActive,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveSandbox failed: %v", err)
	}
	if err := store.SaveSandbox(ctx, domainsandbox.SandboxRecord{
		SandboxID: "sbx_2",
		Type:      "artifact",
		Path:      "sandbox/ws/sbx_2",
		Status:    domainsandbox.SandboxStatusClosed,
		CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveSandbox failed: %v", err)
	}

	items, err := store.ListSandboxes(ctx, 1)
	if err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}
	if len(items) != 1 || items[0].SandboxID != "sbx_2" {
		t.Fatalf("items = %#v", items)
	}
}

func TestJSONLStoreSaveAndListSandboxArtifacts(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveSandboxArtifact(ctx, domainsandbox.SandboxArtifact{
		ArtifactID: "art_1",
		SandboxID:  "sbx_1",
		Type:       "report",
		FilePath:   "sandbox/sbx_1/reports/report.md",
		Title:      "Sandbox Report",
		Status:     "draft",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveSandboxArtifact failed: %v", err)
	}

	items, err := store.ListSandboxArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListSandboxArtifacts failed: %v", err)
	}
	if len(items) != 1 || items[0].ArtifactID != "art_1" {
		t.Fatalf("items = %#v", items)
	}
}

func TestJSONLStoreSaveAndListPromotionRequests(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SavePromotionRequest(ctx, domainsandbox.PromotionRequest{
		PromotionID:      "prom_1",
		SandboxID:        "sbx_1",
		TargetPath:       "docs/a.md",
		DiffPath:         "sandbox/sbx_1/diff.patch",
		Reason:           "docs update",
		TestResultPath:   "sandbox/sbx_1/test.txt",
		RollbackPlanPath: "sandbox/sbx_1/rollback.md",
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("SavePromotionRequest failed: %v", err)
	}

	items, err := store.ListPromotionRequests(ctx, 10)
	if err != nil {
		t.Fatalf("ListPromotionRequests failed: %v", err)
	}
	if len(items) != 1 || items[0].PromotionID != "prom_1" {
		t.Fatalf("items = %#v", items)
	}
}

func TestJSONLStoreSaveAndListPromotionGateLogs(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SavePromotionGateLog(ctx, domainsandbox.PromotionGateLog{
		EventID:     "evt_promotion_gate_1",
		PromotionID: "prom_1",
		GateStatus:  domainsandbox.GateStatusNeedsReview,
		Reason:      "promotion requirements missing: rollback_plan_path",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SavePromotionGateLog failed: %v", err)
	}

	items, err := store.ListPromotionGateLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListPromotionGateLogs failed: %v", err)
	}
	if len(items) != 1 || items[0].EventID != "evt_promotion_gate_1" {
		t.Fatalf("items = %#v", items)
	}
}

func TestJSONLStoreMissingFilesReturnEmptyLists(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()

	sandboxes, err := store.ListSandboxes(ctx, 10)
	if err != nil {
		t.Fatalf("ListSandboxes failed: %v", err)
	}
	if len(sandboxes) != 0 {
		t.Fatalf("sandboxes = %#v", sandboxes)
	}
	artifacts, err := store.ListSandboxArtifacts(ctx, 10)
	if err != nil {
		t.Fatalf("ListSandboxArtifacts failed: %v", err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("artifacts = %#v", artifacts)
	}
	promotions, err := store.ListPromotionRequests(ctx, 10)
	if err != nil {
		t.Fatalf("ListPromotionRequests failed: %v", err)
	}
	if len(promotions) != 0 {
		t.Fatalf("promotions = %#v", promotions)
	}
	logs, err := store.ListPromotionGateLogs(ctx, 10)
	if err != nil {
		t.Fatalf("ListPromotionGateLogs failed: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("logs = %#v", logs)
	}
}

func TestJSONLStoreFindSandboxRecordsByIDReturnsLatestExactRecords(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore(root)
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)

	sandbox := domainsandbox.SandboxRecord{
		SandboxID: "sbx-exact", Type: "code", Path: "sandbox/sbx-exact", Status: domainsandbox.SandboxStatusActive, CreatedAt: now,
	}
	sandboxSuffix := sandbox
	sandboxSuffix.SandboxID = "sbx-exact-suffix"
	sandboxLatest := sandbox
	sandboxLatest.Path = "sandbox/sbx-exact/latest"
	sandboxLatest.CreatedAt = now.Add(time.Minute)
	for _, item := range []domainsandbox.SandboxRecord{sandbox, sandboxSuffix, sandboxLatest} {
		if err := store.SaveSandbox(ctx, item); err != nil {
			t.Fatalf("SaveSandbox(%q) failed: %v", item.SandboxID, err)
		}
	}
	gotSandbox, found, err := store.FindSandboxByID(ctx, sandbox.SandboxID)
	if err != nil || !found || gotSandbox.Path != sandboxLatest.Path {
		t.Fatalf("FindSandboxByID() = %#v, found=%v, err=%v", gotSandbox, found, err)
	}
	if gotSandbox, found, err := store.FindSandboxByID(ctx, "missing"); err != nil || found || !reflect.DeepEqual(gotSandbox, domainsandbox.SandboxRecord{}) {
		t.Fatalf("missing FindSandboxByID() = %#v, found=%v, err=%v", gotSandbox, found, err)
	}

	artifact := domainsandbox.SandboxArtifact{
		ArtifactID: "artifact-exact", SandboxID: sandbox.SandboxID, Type: "report", FilePath: "sandbox/sbx-exact/report.md", Status: "draft", CreatedAt: now,
	}
	artifactSuffix := artifact
	artifactSuffix.ArtifactID = "artifact-exact-suffix"
	artifactLatest := artifact
	artifactLatest.Title = "latest"
	artifactLatest.CreatedAt = now.Add(time.Minute)
	for _, item := range []domainsandbox.SandboxArtifact{artifact, artifactSuffix, artifactLatest} {
		if err := store.SaveSandboxArtifact(ctx, item); err != nil {
			t.Fatalf("SaveSandboxArtifact(%q) failed: %v", item.ArtifactID, err)
		}
	}
	gotArtifact, found, err := store.FindSandboxArtifactByID(ctx, artifact.ArtifactID)
	if err != nil || !found || gotArtifact.Title != artifactLatest.Title {
		t.Fatalf("FindSandboxArtifactByID() = %#v, found=%v, err=%v", gotArtifact, found, err)
	}

	promotion := domainsandbox.PromotionRequest{
		PromotionID: "promotion-exact", SandboxID: sandbox.SandboxID, TargetPath: "docs/a.md", DiffPath: "sandbox/sbx-exact/diff.patch", TestResultPath: "sandbox/sbx-exact/test.txt", Reason: "first", RollbackPlanPath: "sandbox/sbx-exact/rollback.md", CreatedAt: now,
	}
	promotionSuffix := promotion
	promotionSuffix.PromotionID = "promotion-exact-suffix"
	promotionLatest := promotion
	promotionLatest.Reason = "latest"
	promotionLatest.CreatedAt = now.Add(time.Minute)
	for _, item := range []domainsandbox.PromotionRequest{promotion, promotionSuffix, promotionLatest} {
		if err := store.SavePromotionRequest(ctx, item); err != nil {
			t.Fatalf("SavePromotionRequest(%q) failed: %v", item.PromotionID, err)
		}
	}
	gotPromotion, found, err := store.FindPromotionRequestByID(ctx, promotion.PromotionID)
	if err != nil || !found || gotPromotion.Reason != promotionLatest.Reason {
		t.Fatalf("FindPromotionRequestByID() = %#v, found=%v, err=%v", gotPromotion, found, err)
	}

	gate := domainsandbox.PromotionGateLog{
		EventID: "gate-exact", PromotionID: promotion.PromotionID, GateStatus: domainsandbox.GateStatusNeedsReview, Reason: "first", CreatedAt: now,
	}
	gateSuffix := gate
	gateSuffix.EventID = "gate-exact-suffix"
	gateLatest := gate
	gateLatest.Reason = "latest"
	gateLatest.CreatedAt = now.Add(time.Minute)
	for _, item := range []domainsandbox.PromotionGateLog{gate, gateSuffix, gateLatest} {
		if err := store.SavePromotionGateLog(ctx, item); err != nil {
			t.Fatalf("SavePromotionGateLog(%q) failed: %v", item.EventID, err)
		}
	}
	gotGate, found, err := store.FindPromotionGateLogByID(ctx, gate.EventID)
	if err != nil || !found || gotGate.Reason != gateLatest.Reason {
		t.Fatalf("FindPromotionGateLogByID() = %#v, found=%v, err=%v", gotGate, found, err)
	}
}

func TestJSONLStoreFindSandboxRecordsByIDRejectsMalformedJSON(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore(root)
	ctx := context.Background()
	paths := []string{
		filepath.Join(root, "sandbox_registry.jsonl"),
		filepath.Join(root, "sandbox_artifact.jsonl"),
		filepath.Join(root, "sandbox_promotion_request.jsonl"),
		filepath.Join(root, "promotion_gate_log.jsonl"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("{malformed}\n"), 0644); err != nil {
			t.Fatalf("write malformed JSONL %q: %v", path, err)
		}
	}
	if _, found, err := store.FindSandboxByID(ctx, "sandbox"); err == nil || found {
		t.Fatalf("expected malformed sandbox error, found=%v err=%v", found, err)
	}
	if _, found, err := store.FindSandboxArtifactByID(ctx, "artifact"); err == nil || found {
		t.Fatalf("expected malformed artifact error, found=%v err=%v", found, err)
	}
	if _, found, err := store.FindPromotionRequestByID(ctx, "promotion"); err == nil || found {
		t.Fatalf("expected malformed promotion error, found=%v err=%v", found, err)
	}
	if _, found, err := store.FindPromotionGateLogByID(ctx, "gate"); err == nil || found {
		t.Fatalf("expected malformed gate error, found=%v err=%v", found, err)
	}
}

func TestJSONLStoreFindSandboxRecordsByIDRejectsValidJSONInvalidDomain(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore(root)
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	invalid := []struct {
		name  string
		path  string
		id    string
		value any
		find  func() (bool, error)
	}{
		{
			name: "sandbox",
			path: filepath.Join(root, "sandbox_registry.jsonl"),
			id:   "sbx-invalid",
			value: domainsandbox.SandboxRecord{
				SandboxID: "sbx-invalid", Path: "sandbox/sbx-invalid", Status: domainsandbox.SandboxStatusActive, CreatedAt: now,
			},
			find: func() (bool, error) { _, found, err := store.FindSandboxByID(ctx, "sbx-invalid"); return found, err },
		},
		{
			name: "artifact",
			path: filepath.Join(root, "sandbox_artifact.jsonl"),
			id:   "artifact-invalid",
			value: domainsandbox.SandboxArtifact{
				ArtifactID: "artifact-invalid", SandboxID: "sbx", FilePath: "sandbox/sbx/artifact.md", Status: "draft", CreatedAt: now,
			},
			find: func() (bool, error) {
				_, found, err := store.FindSandboxArtifactByID(ctx, "artifact-invalid")
				return found, err
			},
		},
		{
			name: "promotion",
			path: filepath.Join(root, "sandbox_promotion_request.jsonl"),
			id:   "promotion-invalid",
			value: domainsandbox.PromotionRequest{
				PromotionID: "promotion-invalid", SandboxID: "sbx", DiffPath: "sandbox/sbx/diff.patch", TestResultPath: "sandbox/sbx/test.txt", Reason: "reason", RollbackPlanPath: "sandbox/sbx/rollback.md", CreatedAt: now,
			},
			find: func() (bool, error) {
				_, found, err := store.FindPromotionRequestByID(ctx, "promotion-invalid")
				return found, err
			},
		},
		{
			name: "gate",
			path: filepath.Join(root, "promotion_gate_log.jsonl"),
			id:   "gate-invalid",
			value: domainsandbox.PromotionGateLog{
				EventID: "gate-invalid", PromotionID: "promotion", Reason: "reason", CreatedAt: now,
			},
			find: func() (bool, error) {
				_, found, err := store.FindPromotionGateLogByID(ctx, "gate-invalid")
				return found, err
			},
		},
	}
	for _, item := range invalid {
		line, err := json.Marshal(item.value)
		if err != nil {
			t.Fatalf("marshal invalid %s payload: %v", item.name, err)
		}
		if err := os.WriteFile(item.path, append(line, '\n'), 0644); err != nil {
			t.Fatalf("write invalid %s payload: %v", item.name, err)
		}
		found, err := item.find()
		if err == nil || found {
			t.Fatalf("expected valid-JSON invalid-domain error for %s id %q, found=%v err=%v", item.name, item.id, found, err)
		}
	}
}
