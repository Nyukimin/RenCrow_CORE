package turntiming

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRecorderSnapshotIsBoundedAndUsesMonotonicOffsets(t *testing.T) {
	startedAt := time.Now().Add(-10 * time.Millisecond)
	recorder := New(startedAt)
	recorder.Mark(PointRouteDecided)
	recorder.Mark(PointRouteDecided)
	recorder.Mark(Point("unapproved_point"))

	snapshot := recorder.Snapshot()
	if snapshot.SchemaVersion != SchemaVersion {
		t.Fatalf("schema_version=%d want=%d", snapshot.SchemaVersion, SchemaVersion)
	}
	if got, ok := snapshot.PointsMS[string(PointHandlerReceived)]; !ok || got != 0 {
		t.Fatalf("handler_received=%v present=%t want offset 0", got, ok)
	}
	if got, ok := snapshot.PointsMS[string(PointRouteDecided)]; !ok || got < 0 {
		t.Fatalf("route_decided=%v present=%t want a recorded nonnegative offset", got, ok)
	}
	if _, ok := snapshot.PointsMS["unapproved_point"]; ok {
		t.Fatal("unknown point was recorded")
	}
	if _, ok := snapshot.PointsMS[string(PointGenerationProviderStart)]; ok {
		t.Fatal("unobserved provider point must be absent")
	}

	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["started_at"]; ok {
		t.Fatal("snapshot must not contain wall-clock timestamps")
	}
	if _, ok := payload["trace_id"]; ok {
		t.Fatal("snapshot must not duplicate correlation identity")
	}

	snapshot.PointsMS[string(PointRouteDecided)] = -1
	if got := recorder.Snapshot().PointsMS[string(PointRouteDecided)]; got < 0 {
		t.Fatalf("mutating snapshot changed recorder value: %v", got)
	}
}

func TestRecorderAcceptsRequestedHotspotPoints(t *testing.T) {
	recorder := New(time.Now())
	requested := []Point{
		"classification_started",
		"classification_ended",
		"message_received_event_published",
		"server_received_metric_published",
		"session_turn_log_attempt_ended",
		"lead_agent_run_saved",
		"lead_agent_start_event_appended",
		"lead_context_pack_saved",
	}
	for _, point := range requested {
		recorder.Mark(point)
	}
	points := recorder.Snapshot().PointsMS
	for _, point := range requested {
		if _, ok := points[string(point)]; !ok {
			t.Errorf("approved hotspot point %q was discarded: %#v", point, points)
		}
	}
}

func TestGatewayTimingRequiresBodyGenerationContextAndIsFirstWins(t *testing.T) {
	recorder := New(time.Now())
	ctx := WithRecorder(context.Background(), recorder)
	MarkGatewayRequestStarted(ctx)
	MarkGatewayResponseHeaders(ctx)
	if points := recorder.Snapshot().PointsMS; len(points) != 1 {
		t.Fatalf("unmarked context recorded gateway points: %#v", points)
	}

	ctx = WithBodyGenerationContext(ctx)
	MarkGatewayRequestStarted(ctx)
	time.Sleep(time.Millisecond)
	MarkGatewayResponseHeaders(ctx)
	first := recorder.Snapshot().PointsMS
	requestStarted := first[string(PointGatewayRequestStarted)]
	responseHeaders := first[string(PointGatewayResponseHeaders)]
	if requestStarted <= 0 || responseHeaders <= requestStarted {
		t.Fatalf("gateway points are not ordered: %#v", first)
	}
	time.Sleep(time.Millisecond)
	MarkGatewayRequestStarted(ctx)
	MarkGatewayResponseHeaders(ctx)
	second := recorder.Snapshot().PointsMS
	if second[string(PointGatewayRequestStarted)] != requestStarted || second[string(PointGatewayResponseHeaders)] != responseHeaders {
		t.Fatalf("gateway points were replaced by later request: first=%#v second=%#v", first, second)
	}
}

func TestRecorderAcceptsOrchestrationPreparationPoints(t *testing.T) {
	recorder := New(time.Now())
	want := []Point{
		PointRootTaskCreated,
		PointRouteDecisionStarted,
		PointTaskActivationStarted,
		PointTaskActivationEnded,
		PointLeadRunPersistenceStarted,
		PointLeadRunPersistenceEnded,
	}
	for _, point := range want {
		recorder.Mark(point)
	}

	points := recorder.Snapshot().PointsMS
	for _, point := range want {
		if _, ok := points[string(point)]; !ok {
			t.Errorf("approved orchestration point %q was not recorded", point)
		}
	}
	if len(points) != len(want)+1 {
		t.Fatalf("recorded points=%#v, want handler_received plus %d approved points", points, len(want))
	}
}
