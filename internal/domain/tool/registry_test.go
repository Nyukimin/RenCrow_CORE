package tool

import "testing"

func TestRegistry_RegisterGetList(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ToolManifest{ID: "shell", Version: "1.0.0", SideEffect: SideEffectProcess}); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := r.Register(ToolManifest{ID: "file_read", Version: "1.0.0", SideEffect: SideEffectNone}); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if _, ok := r.Get("shell"); !ok {
		t.Fatal("shell should be registered")
	}
	list := r.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 manifests, got %d", len(list))
	}
}

func TestRegistry_VersionConflict(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(ToolManifest{ID: "shell", Version: "1.0.0", SideEffect: SideEffectProcess}); err != nil {
		t.Fatalf("register failed: %v", err)
	}
	if err := r.Register(ToolManifest{ID: "shell", Version: "2.0.0", SideEffect: SideEffectProcess}); err == nil {
		t.Fatal("expected version conflict")
	}
}
