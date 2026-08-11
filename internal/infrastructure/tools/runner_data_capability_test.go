package tools

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"testing"
)

type dataCatalogStub struct{ op, name string }

func (s *dataCatalogStub) Execute(op, name string) (any, error) {
	s.op, s.name = op, name
	return map[string]any{"name": name}, nil
}
func TestDataCapabilityToolContract(t *testing.T) {
	s := &dataCatalogStub{}
	r := NewToolRunner(ToolRunnerConfig{DataCapabilityCatalog: s, DisableToolHarness: true})
	resp, err := r.ExecuteV2(context.Background(), "data_capability.describe", map[string]any{"operation": "describe", "name": "glossary"})
	if err != nil || resp.IsError() || s.name != "glossary" {
		t.Fatalf("resp=%#v err=%v", resp, err)
	}
	resp, err = r.ExecuteV2(context.Background(), "data_capability.describe", map[string]any{"operation": "list_catalog"})
	if err != nil || resp.IsError() || s.op != "list_catalog" {
		t.Fatalf("list_catalog resp=%#v err=%v", resp, err)
	}
	for _, args := range []map[string]any{{"operation": "describe"}, {"operation": "list_available", "name": "x"}, {"operation": "all"}, {"operation": "list_available", "path": "/srv/private"}} {
		resp, _ := r.ExecuteV2(context.Background(), "data_capability.describe", args)
		if resp == nil || !resp.IsError() || resp.Error.Code != tool.ErrValidationFailed {
			t.Fatalf("accepted %#v", args)
		}
	}
}
