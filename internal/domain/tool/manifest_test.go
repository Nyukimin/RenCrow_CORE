package tool

import "testing"

func TestToolManifest_Validate(t *testing.T) {
	m := ToolManifest{ID: "shell", Version: "1.0.0", SideEffect: SideEffectProcess}
	if err := m.Validate(); err != nil {
		t.Fatalf("expected valid manifest, got %v", err)
	}
	if err := (ToolManifest{Version: "1.0.0", SideEffect: SideEffectNone}).Validate(); err == nil {
		t.Fatal("expected missing id error")
	}
	if err := (ToolManifest{ID: "x", SideEffect: "invalid"}).Validate(); err == nil {
		t.Fatal("expected invalid side effect error")
	}
}

func TestManifestFromMetadata(t *testing.T) {
	meta := ToolMetadata{ToolID: "file_write", Version: "1.0.0", Category: "mutation"}
	m := ManifestFromMetadata(meta)
	if m.ID != "file_write" || m.SideEffect != SideEffectLocalWrite {
		t.Fatalf("unexpected manifest conversion: %+v", m)
	}
}
