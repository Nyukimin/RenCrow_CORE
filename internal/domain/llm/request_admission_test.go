package llm

import (
	"errors"
	"testing"
)

func TestGenerateRequestAdmissionIsOneShotAndNotSerialized(t *testing.T) {
	calls := 0
	capability := NewGenerateRequestAdmission(func() error {
		calls++
		return nil
	})
	request := GenerateRequest{RequestAdmission: capability}
	if err := request.RequestAdmission.Admit(); err != nil {
		t.Fatalf("first Admit: %v", err)
	}
	if err := request.RequestAdmission.Admit(); !errors.Is(err, ErrGenerateRequestAdmissionConsumed) {
		t.Fatalf("second Admit error = %v, want consumed capability", err)
	}
	if calls != 1 {
		t.Fatalf("admission callback calls = %d, want 1", calls)
	}
}
