package core

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestNewMessageIDReturnsOpaqueUUID(t *testing.T) {
	first := string(NewMessageID())
	second := string(NewMessageID())

	if first == second {
		t.Fatalf("message IDs must be unique: %q", first)
	}
	if !strings.HasPrefix(first, "msg_") {
		t.Fatalf("message ID = %q, want msg_ prefix", first)
	}
	if _, err := uuid.Parse(strings.TrimPrefix(first, "msg_")); err != nil {
		t.Fatalf("message ID = %q, want UUID payload: %v", first, err)
	}
}

func TestNewTraceIDReturnsOpaqueUUID(t *testing.T) {
	first := string(NewTraceID())
	second := string(NewTraceID())
	if first == second || !strings.HasPrefix(first, "trc_") {
		t.Fatalf("trace IDs must be unique and prefixed: first=%q second=%q", first, second)
	}
	if _, err := uuid.Parse(strings.TrimPrefix(first, "trc_")); err != nil {
		t.Fatalf("trace ID = %q, want UUID payload: %v", first, err)
	}
}
