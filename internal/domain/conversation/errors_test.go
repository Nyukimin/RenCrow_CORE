package conversation

import (
	"errors"
	"testing"
)

func TestErrorValues_NonNil(t *testing.T) {
	errs := []error{
		ErrThreadNotFound,
		ErrSessionNotFound,
		ErrInvalidThreadStatus,
	}
	for _, err := range errs {
		if err == nil {
			t.Error("sentinel error should not be nil")
		}
	}
}

func TestErrorValues_Distinct(t *testing.T) {
	if errors.Is(ErrThreadNotFound, ErrSessionNotFound) {
		t.Error("ErrThreadNotFound and ErrSessionNotFound should be distinct")
	}
	if errors.Is(ErrThreadNotFound, ErrInvalidThreadStatus) {
		t.Error("ErrThreadNotFound and ErrInvalidThreadStatus should be distinct")
	}
	if errors.Is(ErrSessionNotFound, ErrInvalidThreadStatus) {
		t.Error("ErrSessionNotFound and ErrInvalidThreadStatus should be distinct")
	}
}

func TestErrorValues_Messages(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{ErrThreadNotFound, "thread not found"},
		{ErrSessionNotFound, "session not found"},
		{ErrInvalidThreadStatus, "invalid thread status"},
	}
	for _, tt := range tests {
		if tt.err.Error() != tt.want {
			t.Errorf("error message: want %q, got %q", tt.want, tt.err.Error())
		}
	}
}
