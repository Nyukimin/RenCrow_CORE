package policydecision

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	domainpolicy "github.com/Nyukimin/RenCrow_CORE/internal/domain/policydecision"
)

func TestJSONLStoreReturnsNewestFirst(t *testing.T) {
	store, err := NewJSONLStore(filepath.Join(t.TempDir(), "policy", "decisions.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		record := persistenceRecord(id)
		if err := store.Save(context.Background(), record); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.List(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].DecisionID != "two" {
		t.Fatalf("items=%+v", items)
	}
}

func TestJSONLStoreRejectsCorruptEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.jsonl")
	store, err := NewJSONLStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background(), 10); err == nil {
		t.Fatal("List accepted corrupt evidence")
	}
}

func persistenceRecord(id string) domainpolicy.Record {
	return domainpolicy.Record{
		RecordVersion: domainpolicy.RecordVersion, DecisionID: id, Requester: "user:local",
		Module: "trade", Action: "paper_order", BinaryContractRevision: "trade-binary/v1",
		GlobalBundleRevision: "bundle.1", ModulePolicyRevision: "trade-policy.1",
		DeploymentRevision: "production.1", Outcome: domainpolicy.OutcomeBlocked,
		Reasons: []string{"disabled"}, CreatedAt: time.Now().UTC(),
	}
}
