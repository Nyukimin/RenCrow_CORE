package durablestore

import (
	"fmt"
	"path/filepath"
	"strings"
)

func ValidateManifest(m Manifest) error {
	if strings.TrimSpace(m.ContractVersion) != "rencrow-durable-stores/v1" {
		return fmt.Errorf("contract_version must be rencrow-durable-stores/v1")
	}
	if strings.TrimSpace(m.ModuleID) == "" {
		return fmt.Errorf("module_id is required")
	}
	if len(m.Stores) == 0 {
		return fmt.Errorf("stores must not be empty")
	}
	storeIDs := map[string]struct{}{}
	configKeys := map[string]struct{}{}
	for i, s := range m.Stores {
		prefix := fmt.Sprintf("stores[%d]", i)
		for key, value := range map[string]string{
			"store_id": s.StoreID, "owner_module": s.OwnerModule, "store_kind": s.StoreKind,
			"durability_class": s.DurabilityClass, "production_root_template": s.ProductionRootTemplate,
			"authoritative_writer": s.AuthoritativeWriter, "schema_revision": s.SchemaRevision,
			"migration_owner": s.MigrationOwner, "retention_policy": s.RetentionPolicy,
			"backup_profile": s.BackupProfile, "restore_check": s.RestoreCheck, "rpo": s.RPO, "rto": s.RTO,
			"sensitivity": s.Sensitivity, "fallback_policy": s.FallbackPolicy,
		} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("%s.%s is required", prefix, key)
			}
		}
		if s.OwnerModule != m.ModuleID {
			return fmt.Errorf("%s.owner_module must equal module_id", prefix)
		}
		if len(s.DataClasses) == 0 {
			return fmt.Errorf("%s.data_classes must not be empty", prefix)
		}
		if s.MigrationOwner != m.ModuleID {
			return fmt.Errorf("%s.migration_owner must equal module_id", prefix)
		}
		if len(s.CanonicalConfigKeys) == 0 {
			return fmt.Errorf("%s.canonical_config_keys must not be empty", prefix)
		}
		if !oneOf(s.StoreKind, "sqlite", "object", "append_log", "parquet", "external") {
			return fmt.Errorf("%s.store_kind is invalid", prefix)
		}
		if !oneOf(s.DurabilityClass, "durable", "reconstructible") {
			return fmt.Errorf("%s.durability_class is invalid", prefix)
		}
		if s.DurabilityClass == "reconstructible" && (strings.TrimSpace(s.RebuildSource) == "" || strings.TrimSpace(s.RebuildCheck) == "") {
			return fmt.Errorf("%s reconstructible store requires rebuild_source and rebuild_check", prefix)
		}
		if strings.ToLower(strings.TrimSpace(s.FallbackPolicy)) != "fail_closed" {
			return fmt.Errorf("%s.fallback_policy must be fail_closed", prefix)
		}
		if !oneOf(string(s.ChangeClass), "S0", "S1", "S2") {
			return fmt.Errorf("%s.change_class is invalid", prefix)
		}
		if s.ProposalRevision < 1 {
			return fmt.Errorf("%s.proposal_revision must be >= 1", prefix)
		}
		if !oneOf(string(s.LifecycleStatus), "proposed", "validated", "implemented", "provisioned", "active") {
			return fmt.Errorf("%s.lifecycle_status is invalid", prefix)
		}
		root := filepath.Clean(s.ProductionRootTemplate)
		if !filepath.IsAbs(root) || root != expectedOwnerRoot(m.ModuleID) {
			return fmt.Errorf("%s.production_root_template must equal owner module subtree %s", prefix, expectedOwnerRoot(m.ModuleID))
		}
		if _, ok := storeIDs[s.StoreID]; ok {
			return fmt.Errorf("duplicate store_id %q", s.StoreID)
		}
		storeIDs[s.StoreID] = struct{}{}
		for _, configKey := range s.CanonicalConfigKeys {
			configKey = strings.TrimSpace(configKey)
			if configKey == "" {
				return fmt.Errorf("%s.canonical_config_keys contains empty value", prefix)
			}
			if _, ok := configKeys[configKey]; ok {
				return fmt.Errorf("duplicate config_key %q", configKey)
			}
			configKeys[configKey] = struct{}{}
		}
	}
	return nil
}

func ValidateRegistry(manifests []Manifest) error {
	storeOwner := map[string]string{}
	configOwner := map[string]string{}
	for _, m := range manifests {
		if err := ValidateManifest(m); err != nil {
			return fmt.Errorf("module %q: %w", m.ModuleID, err)
		}
		for _, s := range m.Stores {
			if owner, ok := storeOwner[s.StoreID]; ok {
				return fmt.Errorf("store_id %q is owned by both %s and %s", s.StoreID, owner, m.ModuleID)
			}
			storeOwner[s.StoreID] = m.ModuleID
			for _, configKey := range s.CanonicalConfigKeys {
				if owner, ok := configOwner[configKey]; ok {
					return fmt.Errorf("config_key %q is owned by both %s and %s", configKey, owner, m.ModuleID)
				}
				configOwner[configKey] = m.ModuleID
			}
		}
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}

func expectedOwnerRoot(moduleID string) string {
	suffix := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(moduleID), "RenCrow_"))
	if suffix == "" || suffix == strings.ToLower(moduleID) {
		suffix = "core"
	}
	return filepath.Join("/srv/rencrow/db", suffix)
}

func Classify(req StorageRequirement, manifests []Manifest) Classification {
	joined := strings.ToLower(strings.Join(req.FactsToStore, " "))
	if containsAny(joined, "cache", "キャッシュ") {
		return Classification{Class: ClassCache, OwnerModule: req.OwnerModule, Reason: "rebuildable cache"}
	}
	if containsAny(joined, "temporary", "一時", "scratch") {
		return Classification{Class: ClassEphemeral, OwnerModule: req.OwnerModule, Reason: "temporary lifetime"}
	}
	if containsAny(joined, "artifact", "成果物", "report", "レポート") {
		return Classification{Class: ClassArtifact, OwnerModule: req.OwnerModule, Reason: "file artifact"}
	}
	if containsAny(joined, "existing_memory", "会話記憶", "ユーザー記憶") {
		return Classification{Class: ClassExistingMemory, OwnerModule: "RenCrow_CORE", Reason: "existing memory contract"}
	}
	if strings.TrimSpace(req.OwnerModule) == "" {
		return Classification{Status: StatusBlocked, Reason: "owner module could not be resolved"}
	}
	for _, m := range manifests {
		for _, s := range m.Stores {
			for _, class := range s.DataClasses {
				if dataClassMatches(joined, class) {
					return Classification{Class: ClassExistingStore, ChangeClass: ChangeS0, StoreID: s.StoreID, OwnerModule: s.OwnerModule, Reason: "matched registered data class " + class}
				}
			}
		}
	}
	changeClass := ChangeS1
	if containsAny(joined, "external database", "remote database", "外部database", "外部db", "新しいengine", "mount", "マウント", "root変更") {
		changeClass = ChangeS2
	}
	return Classification{Class: ClassNewStore, ChangeClass: changeClass, OwnerModule: req.OwnerModule, Reason: "no registered store accepts the data class"}
}

func dataClassMatches(joined, class string) bool {
	n := strings.ToLower(strings.TrimSpace(class))
	if n != "" && strings.Contains(joined, n) {
		return true
	}
	if n == "x_bookmark" && containsAny(joined, "x bookmark", "xのbookmark", "bookmark", "ブックマーク") {
		return true
	}
	return false
}
