package persona

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

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

func TestPersonaStorageHostReceiptSchemaHasExactOperationIDKey(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	defer store.Close()

	rows, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(persona_storagehost_operation_receipt)`)
	if err != nil {
		t.Fatalf("inspect Persona storage-host receipt schema: %v", err)
	}
	defer rows.Close()
	var primaryKeyColumns []string
	for rows.Next() {
		var cid, notNull, primaryKeyOrdinal int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKeyOrdinal); err != nil {
			t.Fatalf("scan Persona storage-host receipt schema: %v", err)
		}
		if primaryKeyOrdinal > 0 {
			primaryKeyColumns = append(primaryKeyColumns, name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate Persona storage-host receipt schema: %v", err)
	}
	if len(primaryKeyColumns) != 1 || primaryKeyColumns[0] != "op_id" {
		t.Fatalf("Persona storage-host receipt primary-key columns=%v; want exactly [op_id]", primaryKeyColumns)
	}
}

func TestPersonaStorageHostReceiptSchemaRejectsCompositeOperationKey(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "composite-persona.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open malformed-schema fixture: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE persona_storagehost_operation_receipt (
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
		t.Fatalf("create composite receipt fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close malformed-schema fixture: %v", err)
	}

	store, err := NewSQLiteStore(dbPath)
	if err == nil {
		_ = store.Close()
		t.Fatal("NewSQLiteStore accepted composite (op_id, operation) receipt key; want fail-closed schema rejection")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "primary key") {
		t.Fatalf("NewSQLiteStore rejected composite receipt schema for unrelated reason: %v", err)
	}
}

func TestPersonaStorageHostReceiptBindsOperationPayloadGenerationAndExactResult(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona-receipt.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	item := personaReceiptTestObservation("receipt-bindings")
	binding := PersonaObservationStorageHostBinding{ActorObserverID: item.ObserverID, AuthenticatedTargetUserID: item.TargetID}
	identity := personaReceiptTestIdentity("persona-receipt-bindings", PersonaSaveObservationLogStorageHostOperation, item, 4)
	if saved, err := store.SaveObservationLogForStorageHostOperation(ctx, identity, binding, item); err != nil || !reflect.DeepEqual(saved, item) {
		t.Fatalf("save result=%#v err=%v; want exact item", saved, err)
	}
	if result, found, err := store.LookupPersonaObservationLogStorageHostReceipt(ctx, identity, binding, item); err != nil || !found {
		t.Fatalf("exact receipt result=%s found=%v err=%v", result, found, err)
	}

	wrongPayload := identity
	wrongPayload.PayloadSHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("different payload")))
	if _, _, err := store.LookupPersonaObservationLogStorageHostReceipt(ctx, wrongPayload, binding, item); err == nil {
		t.Fatal("receipt lookup accepted a substituted payload hash")
	}
	wrongGeneration := identity
	wrongGeneration.WriterGeneration++
	if _, _, err := store.LookupPersonaObservationLogStorageHostReceipt(ctx, wrongGeneration, binding, item); err == nil {
		t.Fatal("receipt lookup accepted a substituted writer generation")
	}
	wrongOperation := identity
	wrongOperation.Operation = PersonaSaveMetaProfileUpdateStorageHostOperation
	if _, _, err := store.LookupPersonaObservationLogStorageHostReceipt(ctx, wrongOperation, binding, item); err == nil {
		t.Fatal("receipt lookup accepted a cross-operation identity")
	}
	substituted := item
	substituted.Summary = "substituted exact result"
	if _, _, err := store.LookupPersonaObservationLogStorageHostReceipt(ctx, identity, binding, substituted); err == nil {
		t.Fatal("receipt lookup accepted a substituted result payload")
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE persona_storagehost_operation_receipt SET effect_id = ? WHERE op_id = ?`, "different-effect", identity.OpID); err != nil {
		t.Fatalf("corrupt effect proof: %v", err)
	}
	if _, _, err := store.LookupPersonaObservationLogStorageHostReceipt(ctx, identity, binding, item); err == nil {
		t.Fatal("receipt lookup accepted a substituted effect identity")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE persona_storagehost_operation_receipt SET effect_id = ? WHERE op_id = ?`, item.EventID, identity.OpID); err != nil {
		t.Fatalf("restore effect proof: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE persona_storagehost_operation_receipt SET result_sha256 = ? WHERE op_id = ?`, strings.Repeat("0", 64), identity.OpID); err != nil {
		t.Fatalf("corrupt result hash proof: %v", err)
	}
	if _, _, err := store.LookupPersonaObservationLogStorageHostReceipt(ctx, identity, binding, item); err == nil {
		t.Fatal("receipt lookup accepted a corrupt result hash")
	}
}

func TestPersonaStorageHostMissingReceiptProofFailsClosedWhenEffectExists(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona-missing-proof.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	item := personaReceiptTestObservation("missing-proof")
	binding := PersonaObservationStorageHostBinding{ActorObserverID: item.ObserverID, AuthenticatedTargetUserID: item.TargetID}
	if err := store.SaveObservationLog(ctx, item); err != nil {
		t.Fatalf("save canonical observation fixture: %v", err)
	}
	identity := personaReceiptTestIdentity("missing-receipt", PersonaSaveObservationLogStorageHostOperation, item, 2)
	if _, found, err := store.LookupPersonaObservationLogStorageHostReceipt(ctx, identity, binding, item); err == nil || found {
		t.Fatalf("lookup with missing proof found=%v err=%v; want fail closed", found, err)
	}

	missing := personaReceiptTestObservation("no-effect")
	missing.EventID = "persona-no-effect"
	missingIdentity := personaReceiptTestIdentity("no-effect-op", PersonaSaveObservationLogStorageHostOperation, missing, 2)
	if _, found, err := store.LookupPersonaObservationLogStorageHostReceipt(ctx, missingIdentity,
		PersonaObservationStorageHostBinding{ActorObserverID: missing.ObserverID, AuthenticatedTargetUserID: missing.TargetID}, missing); err != nil || found {
		t.Fatalf("lookup with no receipt and no canonical effect found=%v err=%v; want confirmed absence", found, err)
	}
}

func TestPersonaStorageHostSaveRollsBackCanonicalEffectWhenReceiptInsertFails(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "persona-atomic.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	item := personaReceiptTestObservation("atomic-write")
	binding := PersonaObservationStorageHostBinding{ActorObserverID: item.ObserverID, AuthenticatedTargetUserID: item.TargetID}
	if _, err := store.db.ExecContext(ctx, `CREATE TRIGGER reject_persona_receipt BEFORE INSERT ON persona_storagehost_operation_receipt BEGIN SELECT RAISE(ABORT, 'fixture receipt failure'); END`); err != nil {
		t.Fatalf("install receipt failure trigger: %v", err)
	}
	identity := personaReceiptTestIdentity("persona-atomic-write", PersonaSaveObservationLogStorageHostOperation, item, 3)
	if _, err := store.SaveObservationLogForStorageHostOperation(ctx, identity, binding, item); err == nil {
		t.Fatal("save succeeded despite receipt insert failure")
	}
	if _, found, err := store.FindObservationLogByID(ctx, item.EventID); err != nil || found {
		t.Fatalf("canonical effect after failed receipt insert found=%v err=%v; want transaction rollback", found, err)
	}
	var receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM persona_storagehost_operation_receipt WHERE op_id = ?`, identity.OpID).Scan(&receiptCount); err != nil || receiptCount != 0 {
		t.Fatalf("receipt count after failed insert=%d err=%v; want 0", receiptCount, err)
	}
}

func personaReceiptTestObservation(suffix string) domainpersona.ObservationLog {
	return domainpersona.ObservationLog{
		EventID: "persona-receipt-" + suffix, ObserverID: "mio", TargetID: "ren", ObservationType: "daily",
		Summary: "observation receipt fixture", Sensitivity: "normal", ReviewStatus: "pending",
		CreatedAt: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC),
	}
}

func personaReceiptTestIdentity(opID, operation string, payload any, generation int64) PersonaStorageHostOperationIdentity {
	encoded, _ := json.Marshal(payload)
	return PersonaStorageHostOperationIdentity{
		OpID: opID, Operation: operation, PayloadSHA256: fmt.Sprintf("%x", sha256.Sum256(encoded)), WriterGeneration: generation,
	}
}
