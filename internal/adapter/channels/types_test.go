package channels

import (
	"context"
	"errors"
	"testing"
)

type stubAdapter struct {
	name string
	err  error
}

func (s *stubAdapter) Name() string                              { return s.name }
func (s *stubAdapter) Send(_ context.Context, _, _ string) error { return nil }
func (s *stubAdapter) Probe(_ context.Context) error             { return s.err }

func TestRegistry_RegisterListGetProbe(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(&stubAdapter{name: "line"}); err != nil {
		t.Fatalf("register line failed: %v", err)
	}
	dErr := errors.New("down")
	if err := r.Register(&stubAdapter{name: "discord", err: dErr}); err != nil {
		t.Fatalf("register discord failed: %v", err)
	}

	names := r.List()
	if len(names) != 2 || names[0] != "discord" || names[1] != "line" {
		t.Fatalf("unexpected names: %+v", names)
	}
	if _, ok := r.Get("line"); !ok {
		t.Fatal("line should be found")
	}
	probes := r.ProbeAll(context.Background())
	if probes["line"] != nil {
		t.Fatalf("line probe should be nil, got %v", probes["line"])
	}
	if !errors.Is(probes["discord"], dErr) {
		t.Fatalf("discord probe should be down, got %v", probes["discord"])
	}
}
