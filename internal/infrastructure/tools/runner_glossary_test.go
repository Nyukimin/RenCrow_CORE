package tools

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"testing"
)

type glossaryStub struct {
	op, term, category string
	limit              int
}

func (s *glossaryStub) Lookup(_ context.Context, op, term, category string, limit int) (any, error) {
	s.op, s.term, s.category, s.limit = op, term, category, limit
	return []string{"ok"}, nil
}
func TestGlossaryToolContract(t *testing.T) {
	s := &glossaryStub{}
	r := NewToolRunner(ToolRunnerConfig{GlossaryLookup: s, DisableToolHarness: true})
	resp, err := r.ExecuteV2(context.Background(), "glossary.lookup", map[string]any{"operation": "define_term", "term": "Go", "limit": float64(5)})
	if err != nil || resp.IsError() || s.term != "Go" || s.limit != 5 {
		t.Fatalf("resp=%#v err=%v stub=%#v", resp, err, s)
	}
	for _, args := range []map[string]any{{"operation": "define_term"}, {"operation": "define_term", "term": "x", "category": "y"}, {"operation": "list_category", "category": "x", "limit": 21}, {"operation": "all"}, {"operation": "define_term", "term": "x", "sql": "select"}} {
		resp, _ := r.ExecuteV2(context.Background(), "glossary.lookup", args)
		if resp == nil || !resp.IsError() || resp.Error.Code != tool.ErrValidationFailed {
			t.Fatalf("accepted %#v", args)
		}
	}
}
