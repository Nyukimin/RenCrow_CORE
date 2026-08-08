package durablestore

import "testing"

func validManifest() Manifest {
	return Manifest{
		ContractVersion: "rencrow-durable-stores/v1",
		ModuleID:        "RenCrow_CORE",
		Stores: []StoreManifest{{
			StoreID: "core.conversation_l1", OwnerModule: "RenCrow_CORE",
			StoreKind: "sqlite", DurabilityClass: "durable", DataClasses: []string{"conversation", "user_memory", "x_bookmark"},
			CanonicalConfigKeys: []string{"storage.databases.conversation_l1"}, ProductionRootTemplate: "/srv/rencrow/db/core",
			AuthoritativeWriter: "rencrow-core", SchemaRevision: "conversation-l1/v1", MigrationOwner: "RenCrow_CORE",
			RetentionPolicy: "class-specific", BackupProfile: "core-snapshot/v1", RestoreCheck: "sqlite-integrity/v1", RPO: "PT24H", RTO: "PT4H",
			Sensitivity: "private", FallbackPolicy: "fail_closed", ChangeClass: ChangeS1, ProposalRevision: 1, LifecycleStatus: LifecycleValidated,
		}},
	}
}

func TestValidateManifestAndRegistryCollisions(t *testing.T) {
	m := validManifest()
	if err := ValidateManifest(m); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	broken := m
	broken.Stores = append([]StoreManifest(nil), m.Stores...)
	broken.Stores[0].BackupProfile = ""
	if err := ValidateManifest(broken); err == nil {
		t.Fatal("manifest without backup policy must be rejected")
	}

	other := validManifest()
	other.ModuleID = "RenCrow_Tools"
	other.Stores[0].OwnerModule = "RenCrow_Tools"
	other.Stores[0].MigrationOwner = "RenCrow_Tools"
	other.Stores[0].ProductionRootTemplate = "/srv/rencrow/db/tools"
	if err := ValidateRegistry([]Manifest{m, other}); err == nil {
		t.Fatal("duplicate store/config ownership must be rejected")
	}
}

func TestClassifyRequirement(t *testing.T) {
	m := validManifest()
	tests := []struct {
		name  string
		req   StorageRequirement
		class StorageClass
		store string
	}{
		{"bookmark uses existing store", StorageRequirement{FactsToStore: []string{"X bookmark"}, OwnerModule: "RenCrow_CORE"}, ClassExistingStore, "core.conversation_l1"},
		{"cache precedence", StorageRequirement{FactsToStore: []string{"temporary search cache"}, OwnerModule: "RenCrow_CORE"}, ClassCache, ""},
		{"new owned data", StorageRequirement{FactsToStore: []string{"garden telemetry"}, OwnerModule: "RenCrow_GAMES"}, ClassNewStore, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Classify(tt.req, []Manifest{m})
			if got.Class != tt.class || got.StoreID != tt.store {
				t.Fatalf("got class=%q store=%q, want class=%q store=%q", got.Class, got.StoreID, tt.class, tt.store)
			}
		})
	}
	blocked := Classify(StorageRequirement{FactsToStore: []string{"unknown facts"}}, []Manifest{m})
	if blocked.Status != StatusBlocked {
		t.Fatalf("unresolved owner status=%q, want blocked", blocked.Status)
	}
}
