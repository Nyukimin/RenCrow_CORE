package durablestore

import (
	"testing"
	"time"
)

func TestHashStorageRequirementExcludesDerivedIDsAndNormalizesIntent(t *testing.T) {
	base := StorageRequirement{
		RequirementID: "derived-a", DedupeKey: "derived-dedupe-a", RequestID: "request-1", TraceID: "trace-1",
		RequestedBy: "user-1", UserScope: "user-1", RequestedOutcome: OutcomeImplement,
		FactsToStore: []string{"  X Bookmark   DB  "}, SourceSystems: []string{"X"}, OwnerHint: "RenCrow_CORE", OwnerModule: "RenCrow_CORE",
	}
	changedDerived := base
	changedDerived.RequirementID = "derived-b"
	changedDerived.DedupeKey = "derived-dedupe-b"
	if HashStorageRequirement(base) != HashStorageRequirement(changedDerived) {
		t.Fatal("derived IDs must not affect the payload hash")
	}
	changedRequest := base
	changedRequest.RequestID = "request-2"
	if HashStorageRequirement(base) == HashStorageRequirement(changedRequest) {
		t.Fatal("request ID must affect the payload hash")
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
	valid := RequestReceipt{RequestID: "request-1", UserScope: "user-1", PayloadHash: "hash", RequirementID: "requirement-1", CreatedAt: time.Now().UTC()}
	if err := ValidateRequestReceipt(valid); err != nil {
		t.Fatalf("valid receipt rejected: %v", err)
	}
	for name, mutate := range map[string]func(*RequestReceipt){
		"request":     func(r *RequestReceipt) { r.RequestID = "" },
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
