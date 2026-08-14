package sandbox

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
)

func TestSQLiteStoreSaveAndListSandboxRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	if err := store.SaveSandbox(ctx, domainsandbox.SandboxRecord{
		SandboxID:    "sbx_1",
		WorkstreamID: "ws_1",
		GoalID:       "goal_1",
		Type:         "code",
		Path:         "sandbox/ws_1/sbx_1",
		Status:       domainsandbox.SandboxStatusActive,
		CreatedAt:    now,
	}); err != nil {
		t.Fatalf("SaveSandbox failed: %v", err)
	}
	if err := store.SaveSandboxArtifact(ctx, domainsandbox.SandboxArtifact{
		ArtifactID: "art_1",
		SandboxID:  "sbx_1",
		Type:       "rollback_plan",
		FilePath:   "sandbox/ws_1/sbx_1/reports/rollback.md",
		Status:     "pending_review",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveSandboxArtifact failed: %v", err)
	}
	if err := store.SavePromotionRequest(ctx, domainsandbox.PromotionRequest{
		PromotionID:      "prom_1",
		SandboxID:        "sbx_1",
		WorkstreamID:     "ws_1",
		GoalID:           "goal_1",
		TargetPath:       "docs/example.md",
		DiffPath:         "sandbox/ws_1/sbx_1/diff.patch",
		Reason:           "仕様反映",
		TestResultPath:   "sandbox/ws_1/sbx_1/test.txt",
		RollbackPlanPath: "sandbox/ws_1/sbx_1/rollback.md",
		CreatedAt:        now,
	}); err != nil {
		t.Fatalf("SavePromotionRequest failed: %v", err)
	}
	if err := store.SavePromotionGateLog(ctx, domainsandbox.PromotionGateLog{
		EventID:     "evt_gate_1",
		PromotionID: "prom_1",
		GateStatus:  domainsandbox.GateStatusNeedsReview,
		Reason:      "promotion requirements missing: rollback_plan_path",
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SavePromotionGateLog failed: %v", err)
	}

	sandboxes, err := store.ListSandboxes(ctx, 10)
	if err != nil || len(sandboxes) != 1 || sandboxes[0].SandboxID != "sbx_1" {
		t.Fatalf("sandboxes=%#v err=%v", sandboxes, err)
	}
	artifacts, err := store.ListSandboxArtifacts(ctx, 10)
	if err != nil || len(artifacts) != 1 || artifacts[0].ArtifactID != "art_1" {
		t.Fatalf("artifacts=%#v err=%v", artifacts, err)
	}
	promotions, err := store.ListPromotionRequests(ctx, 10)
	if err != nil || len(promotions) != 1 || promotions[0].PromotionID != "prom_1" {
		t.Fatalf("promotions=%#v err=%v", promotions, err)
	}
	logs, err := store.ListPromotionGateLogs(ctx, 10)
	if err != nil || len(logs) != 1 || logs[0].EventID != "evt_gate_1" {
		t.Fatalf("logs=%#v err=%v", logs, err)
	}
}

func TestSQLiteStoreMissingRowsReturnEmptyLists(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if items, err := store.ListSandboxes(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("sandboxes=%#v err=%v", items, err)
	}
	if items, err := store.ListSandboxArtifacts(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("artifacts=%#v err=%v", items, err)
	}
	if items, err := store.ListPromotionRequests(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("promotions=%#v err=%v", items, err)
	}
	if items, err := store.ListPromotionGateLogs(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("logs=%#v err=%v", items, err)
	}
}

func TestSQLiteStoreFindSandboxRecordsByIDUsesExactPrimaryKeys(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	sandbox := domainsandbox.SandboxRecord{SandboxID: "sbx-exact", Type: "code", Path: "sandbox/sbx-exact", Status: domainsandbox.SandboxStatusActive, CreatedAt: now}
	artifact := domainsandbox.SandboxArtifact{ArtifactID: "artifact-exact", SandboxID: sandbox.SandboxID, Type: "report", FilePath: "sandbox/sbx-exact/report.md", Status: "draft", CreatedAt: now}
	promotion := domainsandbox.PromotionRequest{PromotionID: "promotion-exact", SandboxID: sandbox.SandboxID, TargetPath: "docs/a.md", DiffPath: "sandbox/sbx-exact/diff.patch", TestResultPath: "sandbox/sbx-exact/test.txt", Reason: "reason", RollbackPlanPath: "sandbox/sbx-exact/rollback.md", CreatedAt: now}
	gate := domainsandbox.PromotionGateLog{EventID: "gate-exact", PromotionID: promotion.PromotionID, GateStatus: domainsandbox.GateStatusNeedsReview, Reason: "reason", CreatedAt: now}
	if err := store.SaveSandbox(ctx, sandbox); err != nil {
		t.Fatalf("SaveSandbox() failed: %v", err)
	}
	if err := store.SaveSandboxArtifact(ctx, artifact); err != nil {
		t.Fatalf("SaveSandboxArtifact() failed: %v", err)
	}
	if err := store.SavePromotionRequest(ctx, promotion); err != nil {
		t.Fatalf("SavePromotionRequest() failed: %v", err)
	}
	if err := store.SavePromotionGateLog(ctx, gate); err != nil {
		t.Fatalf("SavePromotionGateLog() failed: %v", err)
	}

	gotSandbox, found, err := store.FindSandboxByID(ctx, sandbox.SandboxID)
	if err != nil || !found || !reflect.DeepEqual(gotSandbox, sandbox) {
		t.Fatalf("FindSandboxByID() = %#v, found=%v, err=%v", gotSandbox, found, err)
	}
	gotArtifact, found, err := store.FindSandboxArtifactByID(ctx, artifact.ArtifactID)
	if err != nil || !found || !reflect.DeepEqual(gotArtifact, artifact) {
		t.Fatalf("FindSandboxArtifactByID() = %#v, found=%v, err=%v", gotArtifact, found, err)
	}
	gotPromotion, found, err := store.FindPromotionRequestByID(ctx, promotion.PromotionID)
	if err != nil || !found || !reflect.DeepEqual(gotPromotion, promotion) {
		t.Fatalf("FindPromotionRequestByID() = %#v, found=%v, err=%v", gotPromotion, found, err)
	}
	gotGate, found, err := store.FindPromotionGateLogByID(ctx, gate.EventID)
	if err != nil || !found || !reflect.DeepEqual(gotGate, gate) {
		t.Fatalf("FindPromotionGateLogByID() = %#v, found=%v, err=%v", gotGate, found, err)
	}
	if got, found, err := store.FindSandboxByID(ctx, "sbx-exact-suffix"); err != nil || found || !reflect.DeepEqual(got, domainsandbox.SandboxRecord{}) {
		t.Fatalf("missing FindSandboxByID() = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestSQLiteStoreFindSandboxRecordsByIDRejectsMalformedPayload(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	sandbox := domainsandbox.SandboxRecord{SandboxID: "sbx-malformed", Type: "code", Path: "sandbox/sbx-malformed", Status: domainsandbox.SandboxStatusActive, CreatedAt: now}
	artifact := domainsandbox.SandboxArtifact{ArtifactID: "artifact-malformed", SandboxID: sandbox.SandboxID, Type: "report", FilePath: "sandbox/sbx-malformed/report.md", Status: "draft", CreatedAt: now}
	promotion := domainsandbox.PromotionRequest{PromotionID: "promotion-malformed", SandboxID: sandbox.SandboxID, TargetPath: "docs/a.md", DiffPath: "sandbox/sbx-malformed/diff.patch", TestResultPath: "sandbox/sbx-malformed/test.txt", Reason: "reason", RollbackPlanPath: "sandbox/sbx-malformed/rollback.md", CreatedAt: now}
	gate := domainsandbox.PromotionGateLog{EventID: "gate-malformed", PromotionID: promotion.PromotionID, GateStatus: domainsandbox.GateStatusNeedsReview, Reason: "reason", CreatedAt: now}
	if err := store.SaveSandbox(ctx, sandbox); err != nil {
		t.Fatalf("SaveSandbox() failed: %v", err)
	}
	if err := store.SaveSandboxArtifact(ctx, artifact); err != nil {
		t.Fatalf("SaveSandboxArtifact() failed: %v", err)
	}
	if err := store.SavePromotionRequest(ctx, promotion); err != nil {
		t.Fatalf("SavePromotionRequest() failed: %v", err)
	}
	if err := store.SavePromotionGateLog(ctx, gate); err != nil {
		t.Fatalf("SavePromotionGateLog() failed: %v", err)
	}
	corrupt := []struct {
		query string
		id    string
		find  func() (bool, error)
	}{
		{query: `UPDATE sandbox_registry SET payload = ? WHERE sandbox_id = ?`, id: sandbox.SandboxID, find: func() (bool, error) {
			_, found, err := store.FindSandboxByID(ctx, sandbox.SandboxID)
			return found, err
		}},
		{query: `UPDATE sandbox_artifact SET payload = ? WHERE artifact_id = ?`, id: artifact.ArtifactID, find: func() (bool, error) {
			_, found, err := store.FindSandboxArtifactByID(ctx, artifact.ArtifactID)
			return found, err
		}},
		{query: `UPDATE sandbox_promotion_request SET payload = ? WHERE promotion_id = ?`, id: promotion.PromotionID, find: func() (bool, error) {
			_, found, err := store.FindPromotionRequestByID(ctx, promotion.PromotionID)
			return found, err
		}},
		{query: `UPDATE promotion_gate_log SET payload = ? WHERE event_id = ?`, id: gate.EventID, find: func() (bool, error) {
			_, found, err := store.FindPromotionGateLogByID(ctx, gate.EventID)
			return found, err
		}},
	}
	for _, item := range corrupt {
		if _, err := store.db.Exec(item.query, "{malformed}", item.id); err != nil {
			t.Fatalf("corrupt %q: %v", item.id, err)
		}
		found, err := item.find()
		if err == nil || found {
			t.Fatalf("expected malformed payload error for %q, found=%v err=%v", item.id, found, err)
		}
	}
}

func TestSQLiteStoreFindSandboxRecordsByIDRejectsValidJSONInvalidDomain(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	invalid := []struct {
		name   string
		query  string
		id     string
		value  any
		lookup func() (bool, error)
	}{
		{
			name: "sandbox", query: `INSERT INTO sandbox_registry (sandbox_id, payload) VALUES (?, ?)`, id: "sbx-invalid",
			value:  domainsandbox.SandboxRecord{SandboxID: "sbx-invalid", Path: "sandbox/sbx-invalid", Status: domainsandbox.SandboxStatusActive, CreatedAt: now},
			lookup: func() (bool, error) { _, found, err := store.FindSandboxByID(ctx, "sbx-invalid"); return found, err },
		},
		{
			name: "artifact", query: `INSERT INTO sandbox_artifact (artifact_id, payload) VALUES (?, ?)`, id: "artifact-invalid",
			value: domainsandbox.SandboxArtifact{ArtifactID: "artifact-invalid", SandboxID: "sbx", FilePath: "sandbox/sbx/artifact.md", Status: "draft", CreatedAt: now},
			lookup: func() (bool, error) {
				_, found, err := store.FindSandboxArtifactByID(ctx, "artifact-invalid")
				return found, err
			},
		},
		{
			name: "promotion", query: `INSERT INTO sandbox_promotion_request (promotion_id, payload) VALUES (?, ?)`, id: "promotion-invalid",
			value: domainsandbox.PromotionRequest{PromotionID: "promotion-invalid", SandboxID: "sbx", DiffPath: "sandbox/sbx/diff.patch", TestResultPath: "sandbox/sbx/test.txt", Reason: "reason", RollbackPlanPath: "sandbox/sbx/rollback.md", CreatedAt: now},
			lookup: func() (bool, error) {
				_, found, err := store.FindPromotionRequestByID(ctx, "promotion-invalid")
				return found, err
			},
		},
		{
			name: "gate", query: `INSERT INTO promotion_gate_log (event_id, payload) VALUES (?, ?)`, id: "gate-invalid",
			value: domainsandbox.PromotionGateLog{EventID: "gate-invalid", PromotionID: "promotion", Reason: "reason", CreatedAt: now},
			lookup: func() (bool, error) {
				_, found, err := store.FindPromotionGateLogByID(ctx, "gate-invalid")
				return found, err
			},
		},
	}
	for _, item := range invalid {
		payload, err := json.Marshal(item.value)
		if err != nil {
			t.Fatalf("marshal invalid %s payload: %v", item.name, err)
		}
		if _, err := store.db.Exec(item.query, item.id, string(payload)); err != nil {
			t.Fatalf("insert invalid %s payload: %v", item.name, err)
		}
		found, err := item.lookup()
		if err == nil || found {
			t.Fatalf("expected valid-JSON invalid-domain error for %s id %q, found=%v err=%v", item.name, item.id, found, err)
		}
	}
}

func TestSQLiteStoreFindSandboxRecordsByIDRejectsPayloadIDMismatch(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	mismatched := []struct {
		name   string
		query  string
		rowID  string
		value  any
		lookup func() (bool, error)
	}{
		{
			name: "sandbox", query: `INSERT INTO sandbox_registry (sandbox_id, payload) VALUES (?, ?)`, rowID: "row-sandbox",
			value:  domainsandbox.SandboxRecord{SandboxID: "payload-sandbox", Type: "code", Path: "sandbox/payload-sandbox", Status: domainsandbox.SandboxStatusActive, CreatedAt: now},
			lookup: func() (bool, error) { _, found, err := store.FindSandboxByID(ctx, "row-sandbox"); return found, err },
		},
		{
			name: "artifact", query: `INSERT INTO sandbox_artifact (artifact_id, payload) VALUES (?, ?)`, rowID: "row-artifact",
			value: domainsandbox.SandboxArtifact{ArtifactID: "payload-artifact", SandboxID: "sbx", Type: "report", FilePath: "sandbox/sbx/report.md", Status: "draft", CreatedAt: now},
			lookup: func() (bool, error) {
				_, found, err := store.FindSandboxArtifactByID(ctx, "row-artifact")
				return found, err
			},
		},
		{
			name: "promotion", query: `INSERT INTO sandbox_promotion_request (promotion_id, payload) VALUES (?, ?)`, rowID: "row-promotion",
			value: domainsandbox.PromotionRequest{PromotionID: "payload-promotion", SandboxID: "sbx", TargetPath: "docs/a.md", DiffPath: "sandbox/sbx/diff.patch", TestResultPath: "sandbox/sbx/test.txt", Reason: "reason", RollbackPlanPath: "sandbox/sbx/rollback.md", CreatedAt: now},
			lookup: func() (bool, error) {
				_, found, err := store.FindPromotionRequestByID(ctx, "row-promotion")
				return found, err
			},
		},
		{
			name: "gate", query: `INSERT INTO promotion_gate_log (event_id, payload) VALUES (?, ?)`, rowID: "row-gate",
			value: domainsandbox.PromotionGateLog{EventID: "payload-gate", PromotionID: "promotion", GateStatus: domainsandbox.GateStatusNeedsReview, Reason: "reason", CreatedAt: now},
			lookup: func() (bool, error) {
				_, found, err := store.FindPromotionGateLogByID(ctx, "row-gate")
				return found, err
			},
		},
	}
	for _, item := range mismatched {
		payload, err := json.Marshal(item.value)
		if err != nil {
			t.Fatalf("marshal mismatched %s payload: %v", item.name, err)
		}
		if _, err := store.db.Exec(item.query, item.rowID, string(payload)); err != nil {
			t.Fatalf("insert mismatched %s payload: %v", item.name, err)
		}
		found, err := item.lookup()
		if err == nil || found || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("expected primary-key/payload-ID mismatch for %s row %q, found=%v err=%v", item.name, item.rowID, found, err)
		}
	}
}
