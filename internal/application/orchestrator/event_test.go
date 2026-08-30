package orchestrator

import (
	"context"
	"errors"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestEventPublicationFailureTrackerRetainsFirstFailureAndClears(t *testing.T) {
	tracker := newEventPublicationFailureTracker()
	traceID := modulecore.NewTraceID()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	tracker.Begin(traceID, cancel)

	firstErr := errors.New("first canonical append failure")
	secondErr := errors.New("later canonical append failure")
	tracker.Record(traceID, firstErr)
	tracker.Record(traceID, secondErr)

	if cause := context.Cause(ctx); !errors.Is(cause, firstErr) {
		t.Fatalf("context cause = %v, want first error %v", cause, firstErr)
	}
	got := tracker.End(traceID)
	if !errors.Is(got, firstErr) {
		t.Fatalf("End() error = %v, want first error %v", got, firstErr)
	}
	if errors.Is(got, secondErr) {
		t.Fatalf("End() replaced first error with later error: %v", got)
	}
	if errors.Is(tracker.End(traceID), firstErr) || errors.Is(tracker.End(traceID), secondErr) {
		t.Fatal("End() retained a failure after clearing the trace")
	}
}
