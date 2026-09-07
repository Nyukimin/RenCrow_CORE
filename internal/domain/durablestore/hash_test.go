package durablestore

import (
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestHashStorageRequirementExcludesDerivedIDsAndNormalizesIntent(t *testing.T) {
	base := StorageRequirement{
		RequirementID: "derived-a", DedupeKey: "derived-dedupe-a", ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"), TraceID: "trace-1",
		RequestedBy: "user-1", UserScope: "user-1", RequestedOutcome: OutcomeImplement,
		FactsToStore: []string{"  X Bookmark   DB  "}, SourceSystems: []string{"X"}, OwnerHint: "RenCrow_CORE", OwnerModule: "RenCrow_CORE",
	}
	changedDerived := base
	changedDerived.RequirementID = "derived-b"
	changedDerived.DedupeKey = "derived-dedupe-b"
	if HashStorageRequirement(base) != HashStorageRequirement(changedDerived) {
		t.Fatal("derived IDs must not affect the payload hash")
	}
	changedAction := base
	changedAction.ActionID = modulecore.ActionID("act_00000000-0000-5000-8000-000000000002")
	if HashStorageRequirement(base) == HashStorageRequirement(changedAction) {
		t.Fatal("action ID must affect the payload hash")
	}
	changedIntent := base
	changedIntent.FactsToStore = []string{"different intent"}
	if HashStorageRequirement(base) == HashStorageRequirement(changedIntent) {
		t.Fatal("semantic intent must affect the payload hash")
	}
	whitespace := base
	whitespace.FactsToStore = []string{"x   bookmark db"}
	if HashStorageRequirement(base) != HashStorageRequirement(whitespace) {
		t.Fatal("semantic intent whitespace must be normalized")
	}
}

func TestValidateRequestReceipt(t *testing.T) {
	valid := RequestReceipt{ActionID: modulecore.ActionID("act_00000000-0000-5000-8000-000000000001"), UserScope: "user-1", PayloadHash: "hash", RequirementID: "requirement-1", CreatedAt: time.Now().UTC()}
	if err := ValidateRequestReceipt(valid); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	for name, mutate := range map[string]func(*RequestReceipt){
		"action":      func(r *RequestReceipt) { r.ActionID = "" },
		"hash":        func(r *RequestReceipt) { r.PayloadHash = "" },
		"requirement": func(r *RequestReceipt) { r.RequirementID = "" },
		"created_at":  func(r *RequestReceipt) { r.CreatedAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := ValidateRequestReceipt(candidate); err == nil {
				t.Fatalf("expected %s validation error", name)
			}
		})
	}
}
