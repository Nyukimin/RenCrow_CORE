package backlog

import (
	"context"
	"testing"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
)

// RED: a candidate must complete maturation before adoption.  The pre-feature
// service accepts Candidate -> Adopt directly, so this assertion is expected
// to fail until the maturation gate is implemented.
func TestMaturationAdoptRequiresPromoted(t *testing.T) {
	store := &memoryItemStore{}
	service := NewService(store, &memoryWorkstreamStore{})
	intake, err := service.Intake(context.Background(), IntakeRequest{
		ItemID:  "maturation-red",
		Title:   "maturation gate",
		Purpose: "verify before adoption",
		SourceRefs: []domainbacklog.SourceRef{{
			Type: "test", Locator: "maturation-red",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Candidate(context.Background(), intake.ItemID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Adopt(context.Background(), intake.ItemID, "owner selected"); err == nil {
		t.Fatal("adoption must fail before maturation PROMOTED")
	}
}
