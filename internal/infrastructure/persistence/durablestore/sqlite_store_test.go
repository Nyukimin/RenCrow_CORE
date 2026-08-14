package durablestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
)

func TestSQLiteStoreRoundTripAndRequiresExistingParent(t *testing.T) {
	if _, err := NewSQLiteStore(filepath.Join(t.TempDir(), "missing", "workflow.db")); err == nil {
		t.Fatal("store must not create an unconfigured parent directory")
	}
	dir := t.TempDir()
	store, err := NewSQLiteStore(filepath.Join(dir, "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	want := domain.WorkflowResult{Status: domain.StatusCompleted, Lifecycle: domain.LifecycleValidated, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Requirement: domain.StorageRequirement{RequirementID: "sr-1", DedupeKey: "dedupe-1"}}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.FindByDedupeKey(context.Background(), "dedupe-1")
	if err != nil || got == nil || got.Requirement.RequirementID != "sr-1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestSQLiteStoreRequestReceiptRoundTripAndExactConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workflow.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	result := domain.WorkflowResult{
		Status: domain.StatusCompleted, Lifecycle: domain.LifecycleValidated, CreatedAt: now, UpdatedAt: now,
		Requirement: domain.StorageRequirement{
			RequirementID: "sr-receipt", DedupeKey: "dedupe-receipt", RequestID: "request-receipt",
			TraceID: "trace-receipt", RequestedBy: "shiro", UserScope: "user-1",
			RequestedOutcome: domain.OutcomeAssess, FactsToStore: []string{"x_bookmark"}, OwnerModule: "RenCrow_CORE",
		},
		Classification: domain.Classification{OwnerModule: "RenCrow_CORE"},
	}
	receipt := domain.RequestReceipt{
		RequestID: result.Requirement.RequestID, UserScope: result.Requirement.UserScope,
		PayloadHash: domain.HashStorageRequirement(result.Requirement), RequirementID: result.Requirement.RequirementID, CreatedAt: now,
	}
	if err := store.SaveWithReceipt(context.Background(), &result, receipt); err != nil {
		t.Fatal(err)
	}
	gotReceipt, err := store.FindByRequestID(context.Background(), receipt.RequestID)
	if err != nil || gotReceipt == nil || *gotReceipt != receipt {
		t.Fatalf("receipt=%+v err=%v want=%+v", gotReceipt, err, receipt)
	}
	gotResult, err := store.FindByRequirementID(context.Background(), result.Requirement.RequirementID)
	if err != nil || gotResult == nil || gotResult.Requirement.RequirementID != result.Requirement.RequirementID {
		t.Fatalf("result=%+v err=%v", gotResult, err)
	}
	if got, err := store.FindByRequestID(context.Background(), "request-receipt-suffix"); err != nil || got != nil {
		t.Fatalf("request lookup must be exact: got=%+v err=%v", got, err)
	}
	if err := store.SaveWithReceipt(context.Background(), nil, receipt); err == nil {
		t.Fatal("duplicate request receipt must fail")
	}
}

func TestSQLiteStoreMigratesV1WorkflowRowsToReceipts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	result := domain.WorkflowResult{
		Status: domain.StatusCompleted, Lifecycle: domain.LifecycleValidated, CreatedAt: now, UpdatedAt: now,
		Requirement: domain.StorageRequirement{
			RequirementID: "sr-v1", DedupeKey: "dedupe-v1", RequestID: "request-v1", RequestedBy: "shiro", UserScope: "user-1",
			RequestedOutcome: domain.OutcomeAssess, FactsToStore: []string{"x_bookmark"}, OwnerModule: "RenCrow_CORE",
		},
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE durable_store_workflow (
		requirement_id TEXT PRIMARY KEY, dedupe_key TEXT NOT NULL UNIQUE, status TEXT NOT NULL,
		lifecycle TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, payload TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO durable_store_workflow (requirement_id, dedupe_key, status, lifecycle, created_at, updated_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?)`, result.Requirement.RequirementID, result.Requirement.DedupeKey, result.Status, result.Lifecycle, now.Format(timeFormat), now.Format(timeFormat), string(payload)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receipt, err := store.FindByRequestID(context.Background(), result.Requirement.RequestID)
	if err != nil || receipt == nil {
		t.Fatalf("migrated receipt=%+v err=%v", receipt, err)
	}
	if receipt.RequirementID != result.Requirement.RequirementID || receipt.UserScope != result.Requirement.UserScope || receipt.PayloadHash != domain.HashStorageRequirement(result.Requirement) || !receipt.CreatedAt.Equal(now) {
		t.Fatalf("migrated receipt=%+v", receipt)
	}
}

func TestSQLiteStoreMigrationFailsClosedForMalformedOrDuplicateLegacyRequests(t *testing.T) {
	tests := []struct {
		name string
		rows []struct {
			requirementID string
			dedupeKey     string
			payload       string
		}
	}{
		{name: "malformed", rows: []struct {
			requirementID string
			dedupeKey     string
			payload       string
		}{{requirementID: "sr-bad", dedupeKey: "dedupe-bad", payload: "{"}}},
		{name: "duplicate_request", rows: func() []struct {
			requirementID string
			dedupeKey     string
			payload       string
		} {
			makeRow := func(id, dedupe string) string {
				value := domain.WorkflowResult{Status: domain.StatusCompleted, Lifecycle: domain.LifecycleValidated, Requirement: domain.StorageRequirement{RequirementID: id, DedupeKey: dedupe, RequestID: "same-request", UserScope: "user-1", RequestedOutcome: domain.OutcomeAssess, FactsToStore: []string{"x"}}}
				encoded, _ := json.Marshal(value)
				return string(encoded)
			}
			return []struct {
				requirementID string
				dedupeKey     string
				payload       string
			}{{"sr-one", "dedupe-one", makeRow("sr-one", "dedupe-one")}, {"sr-two", "dedupe-two", makeRow("sr-two", "dedupe-two")}}
		}()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "workflow.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE durable_store_workflow (
				requirement_id TEXT PRIMARY KEY, dedupe_key TEXT NOT NULL UNIQUE, status TEXT NOT NULL,
				lifecycle TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL, payload TEXT NOT NULL
			)`); err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
			for _, row := range test.rows {
				if _, err := db.Exec(`INSERT INTO durable_store_workflow (requirement_id, dedupe_key, status, lifecycle, created_at, updated_at, payload) VALUES (?, ?, ?, ?, ?, ?, ?)`, row.requirementID, row.dedupeKey, domain.StatusCompleted, domain.LifecycleValidated, now.Format(timeFormat), now.Format(timeFormat), row.payload); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.Exec("PRAGMA user_version = 1"); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if store, err := NewSQLiteStore(path); err == nil {
				store.Close()
				t.Fatal("legacy migration must fail closed")
			}
		})
	}
}
