package revenue

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domainrevenue "github.com/Nyukimin/RenCrow_CORE/internal/domain/revenue"
)

func TestRevenueStorageHostReceiptSurvivesReopenAndLaterOpportunityUpdate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "revenue.db")
	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	ctx := context.Background()
	identity := RevenueStorageHostOperationIdentity{
		OpID:             "revenue-replay-1",
		Operation:        RevenueSaveOpportunityStorageHostOperation,
		PayloadSHA256:    strings.Repeat("a", 64),
		WriterGeneration: 7,
	}
	original := domainrevenue.Opportunity{
		OpportunityID: "opp_receipt_replay",
		SourceKind:    "viewer",
		Title:         "initial opportunity",
		CreatedAt:     time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
	saved, err := store.SaveOpportunityForStorageHostOperation(ctx, identity, original)
	if err != nil {
		_ = store.Close()
		t.Fatalf("initial storage-host save failed: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close before replay failed: %v", err)
	}

	store, err = NewSQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("reopen SQLite store: %v", err)
	}
	defer store.Close()

	replayed, err := store.SaveOpportunityForStorageHostOperation(ctx, identity, original)
	if err != nil {
		t.Fatalf("same operation after reopen failed: %v", err)
	}
	if replayed != saved {
		t.Fatalf("replayed result = %#v, want original exact result %#v", replayed, saved)
	}
	rows, err := store.ListOpportunities(ctx, 10)
	if err != nil {
		t.Fatalf("ListOpportunities() after replay failed: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("opportunity rows after replay = %d, want one", len(rows))
	}

	updated := original
	updated.Title = "later valid update"
	updated.UpdatedAt = original.CreatedAt.Add(time.Minute)
	if err := store.SaveOpportunity(ctx, updated); err != nil {
		t.Fatalf("later valid opportunity update failed: %v", err)
	}

	proof, found, err := store.LookupRevenueOpportunityStorageHostReceipt(ctx, identity, original)
	if err != nil {
		t.Fatalf("lookup of original receipt after later valid update failed: %v", err)
	}
	if !found || len(proof) == 0 {
		t.Fatalf("original receipt found=%v result=%q, want the original exact result", found, proof)
	}
	var originalResult domainrevenue.Opportunity
	if err := json.Unmarshal(proof, &originalResult); err != nil {
		t.Fatalf("decode original exact result: %v", err)
	}
	if originalResult != saved {
		t.Fatalf("receipt result = %#v, want original saved result %#v", originalResult, saved)
	}
	current, found, err := store.FindOpportunityByID(ctx, original.OpportunityID)
	if err != nil || !found || current.Title != updated.Title {
		t.Fatalf("current canonical opportunity = %#v, found=%v, err=%v; want later update", current, found, err)
	}
}

func TestRevenueStorageHostReceiptRejectsChangedBindingsAndMissingProof(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	identity := RevenueStorageHostOperationIdentity{
		OpID:             "revenue-binding-1",
		Operation:        RevenueSaveOpportunityStorageHostOperation,
		PayloadSHA256:    strings.Repeat("c", 64),
		WriterGeneration: 11,
	}
	item := revenueStorageHostTestOpportunity("opp_receipt_binding", "original")
	if _, err := store.SaveOpportunityForStorageHostOperation(ctx, identity, item); err != nil {
		t.Fatalf("SaveOpportunityForStorageHostOperation() error = %v", err)
	}

	tests := []struct {
		name     string
		identity RevenueStorageHostOperationIdentity
		item     domainrevenue.Opportunity
	}{
		{"payload", func() RevenueStorageHostOperationIdentity {
			v := identity
			v.PayloadSHA256 = strings.Repeat("d", 64)
			return v
		}(), item},
		{"generation", func() RevenueStorageHostOperationIdentity { v := identity; v.WriterGeneration++; return v }(), item},
		{"operation", func() RevenueStorageHostOperationIdentity {
			v := identity
			v.Operation = RevenueSaveDailyRoutineStorageHostOperation
			return v
		}(), item},
		{"effect id", identity, revenueStorageHostTestOpportunity("opp_other_receipt_binding", "original")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, found, err := store.LookupRevenueOpportunityStorageHostReceipt(ctx, test.identity, test.item); err == nil || found {
				t.Fatalf("LookupRevenueOpportunityStorageHostReceipt() found=%v err=%v; want fail-closed binding rejection", found, err)
			}
		})
	}

	missingIdentity := identity
	missingIdentity.PayloadSHA256 = strings.Repeat("e", 64)
	if _, err := store.SaveOpportunityForStorageHostOperation(ctx, missingIdentity, item); err == nil {
		t.Fatal("SaveOpportunityForStorageHostOperation() accepted an op_id with a changed payload binding")
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM `+revenueStorageHostReceiptTable+` WHERE op_id = ?`, identity.OpID); err != nil {
		t.Fatalf("delete receipt fixture: %v", err)
	}
	if _, found, err := store.LookupRevenueOpportunityStorageHostReceipt(ctx, identity, item); err == nil || found {
		t.Fatalf("lookup without an operation receipt found=%v err=%v; want missing-proof rejection", found, err)
	}
}

func TestRevenueStorageHostReceiptRejectsCorruptAndSubstitutedResult(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	identity := RevenueStorageHostOperationIdentity{
		OpID:             "revenue-result-proof-1",
		Operation:        RevenueSaveOpportunityStorageHostOperation,
		PayloadSHA256:    strings.Repeat("f", 64),
		WriterGeneration: 13,
	}
	item := revenueStorageHostTestOpportunity("opp_receipt_proof", "original")
	if _, err := store.SaveOpportunityForStorageHostOperation(ctx, identity, item); err != nil {
		t.Fatalf("SaveOpportunityForStorageHostOperation() error = %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE `+revenueStorageHostReceiptTable+` SET result_json = result_json || ' ' WHERE op_id = ?`, identity.OpID); err != nil {
		t.Fatalf("corrupt receipt result fixture: %v", err)
	}
	if _, found, err := store.LookupRevenueOpportunityStorageHostReceipt(ctx, identity, item); err == nil || found {
		t.Fatalf("lookup with corrupt result found=%v err=%v; want integrity rejection", found, err)
	}

	identity.OpID = "revenue-result-substitute-1"
	identity.PayloadSHA256 = strings.Repeat("1", 64)
	if _, err := store.SaveOpportunityForStorageHostOperation(ctx, identity, item); err != nil {
		t.Fatalf("second SaveOpportunityForStorageHostOperation() error = %v", err)
	}
	substitute := item
	substitute.Title = "substituted result"
	substitute = domainrevenue.NormalizeOpportunityEconomics(substitute)
	substituteJSON, err := json.Marshal(substitute)
	if err != nil {
		t.Fatalf("marshal substituted result fixture: %v", err)
	}
	substituteHash := fmt.Sprintf("%x", sha256.Sum256(substituteJSON))
	if _, err := store.db.ExecContext(ctx, `UPDATE `+revenueStorageHostReceiptTable+` SET result_json = ?, result_sha256 = ? WHERE op_id = ?`, string(substituteJSON), substituteHash, identity.OpID); err != nil {
		t.Fatalf("substitute receipt result fixture: %v", err)
	}
	if _, found, err := store.LookupRevenueOpportunityStorageHostReceipt(ctx, identity, item); err == nil || found {
		t.Fatalf("lookup with a self-consistent substituted result found=%v err=%v; want request/result binding rejection", found, err)
	}
}

func TestRevenueStorageHostMutationAndReceiptRollbackTogether(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER fail_revenue_storagehost_receipt
		BEFORE INSERT ON `+revenueStorageHostReceiptTable+`
		WHEN NEW.op_id = 'revenue-atomic-1'
		BEGIN SELECT RAISE(ABORT, 'test receipt failure'); END`); err != nil {
		t.Fatalf("create receipt fault trigger: %v", err)
	}
	identity := RevenueStorageHostOperationIdentity{
		OpID:             "revenue-atomic-1",
		Operation:        RevenueSaveOpportunityStorageHostOperation,
		PayloadSHA256:    strings.Repeat("2", 64),
		WriterGeneration: 17,
	}
	item := revenueStorageHostTestOpportunity("opp_revenue_atomic", "atomic")
	if _, err := store.SaveOpportunityForStorageHostOperation(ctx, identity, item); err == nil {
		t.Fatal("SaveOpportunityForStorageHostOperation() succeeded despite receipt insertion failure")
	}
	if _, found, err := store.FindOpportunityByID(ctx, item.OpportunityID); err != nil || found {
		t.Fatalf("canonical opportunity after failed receipt insert found=%v err=%v; want rolled-back row", found, err)
	}
	if _, found, err := store.LookupRevenueOpportunityStorageHostReceipt(ctx, identity, item); err != nil || found {
		t.Fatalf("reconciliation after rolled-back transaction found=%v err=%v; want proven non-commit", found, err)
	}
}

func TestRevenueDailyRoutineStorageHostReceiptRoundTrip(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "revenue.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	identity := RevenueStorageHostOperationIdentity{
		OpID:             "revenue-daily-report-1",
		Operation:        RevenueSaveDailyRoutineStorageHostOperation,
		PayloadSHA256:    strings.Repeat("3", 64),
		WriterGeneration: 19,
	}
	report := fixtureReport(time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC))
	saved, err := store.SaveDailyRoutineReportForStorageHostOperation(ctx, identity, report)
	if err != nil {
		t.Fatalf("SaveDailyRoutineReportForStorageHostOperation() error = %v", err)
	}
	replayed, err := store.SaveDailyRoutineReportForStorageHostOperation(ctx, identity, report)
	if err != nil {
		t.Fatalf("same daily report operation replay error = %v", err)
	}
	if !reflect.DeepEqual(replayed, saved) {
		t.Fatalf("replayed report = %#v, want exact original %#v", replayed, saved)
	}
	rows, err := store.ListDailyRoutineReports(ctx, 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("daily report rows=%d err=%v; want one", len(rows), err)
	}
	proof, found, err := store.LookupRevenueDailyRoutineStorageHostReceipt(ctx, identity, report)
	if err != nil || !found {
		t.Fatalf("LookupRevenueDailyRoutineStorageHostReceipt() found=%v err=%v", found, err)
	}
	expectedJSON, err := json.Marshal(saved)
	if err != nil {
		t.Fatalf("marshal expected report result: %v", err)
	}
	if string(proof) != string(expectedJSON) {
		t.Fatalf("daily report receipt=%s want exact result %s", proof, expectedJSON)
	}

	var opIDPK, receiptPKCount int
	rowsInfo, err := store.db.QueryContext(ctx, `PRAGMA table_info(`+revenueStorageHostReceiptTable+`)`)
	if err != nil {
		t.Fatalf("inspect receipt schema: %v", err)
	}
	defer rowsInfo.Close()
	for rowsInfo.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rowsInfo.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan receipt schema: %v", err)
		}
		if name == "op_id" {
			opIDPK = primaryKey
		}
		if primaryKey > 0 {
			receiptPKCount++
		}
	}
	if err := rowsInfo.Err(); err != nil {
		t.Fatalf("iterate receipt schema: %v", err)
	}
	if opIDPK != 1 || receiptPKCount != 1 {
		t.Fatalf("receipt primary-key columns=%d op_id ordinal=%d; want exactly op_id at ordinal 1", receiptPKCount, opIDPK)
	}
}

func TestRevenueStorageHostRejectsCompositeReceiptPrimaryKey(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "composite-receipt.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open malformed-schema fixture: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE revenue_storagehost_operation_receipt (
		op_id TEXT NOT NULL,
		operation TEXT NOT NULL,
		payload_sha256 TEXT NOT NULL,
		writer_generation INTEGER NOT NULL,
		effect_table TEXT NOT NULL,
		effect_id TEXT NOT NULL,
		result_json TEXT NOT NULL,
		result_sha256 TEXT NOT NULL,
		PRIMARY KEY (op_id, operation)
	)`)
	if err != nil {
		_ = db.Close()
		t.Fatalf("create composite primary-key receipt fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close malformed-schema fixture: %v", err)
	}

	store, err := NewSQLiteStore(dbPath)
	if err != nil {
		if !strings.Contains(strings.ToLower(err.Error()), "primary key") {
			t.Fatalf("NewSQLiteStore() rejected malformed receipt schema for an unrelated reason: %v", err)
		}
		return
	}
	_ = store.Close()
	t.Fatal("NewSQLiteStore accepted a composite (op_id, operation) receipt primary key; want fail-closed migration rejection")
}

func revenueStorageHostTestOpportunity(id, title string) domainrevenue.Opportunity {
	return domainrevenue.Opportunity{
		OpportunityID:   id,
		SourceKind:      "viewer",
		Title:           title,
		ExpectedRevenue: 100,
		ExpectedCost:    40,
		CreatedAt:       time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
}
