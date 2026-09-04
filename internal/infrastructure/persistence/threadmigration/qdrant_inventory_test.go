package threadmigration

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestInventoryQdrantPointsDeterministicAndInputImmutable(t *testing.T) {
	known, err := BuildPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	points := []QdrantPointSnapshot{
		inventoryQdrantPoint("source-z", "qdrant-session", 9, []float32{1, -2}),
		inventoryQdrantPoint("source-a", "qdrant-session", 9, []float32{1, -2}),
	}
	original := cloneQdrantInventoryPoints(points)

	first, err := InventoryQdrantPoints(QdrantInventoryInput{Phase: QdrantInventoryPhase, KnownPlan: known, Points: points})
	if err != nil {
		t.Fatalf("first inventory error = %v", err)
	}
	second, err := InventoryQdrantPoints(QdrantInventoryInput{Phase: QdrantInventoryPhase, KnownPlan: known, Points: []QdrantPointSnapshot{points[1], points[0]}})
	if err != nil {
		t.Fatalf("second inventory error = %v", err)
	}
	if first.Receipt != second.Receipt || first.Plan.MappingSHA256 != second.Plan.MappingSHA256 {
		t.Fatalf("inventory is not deterministic: first=%+v second=%+v", first.Receipt, second.Receipt)
	}
	if first.Receipt.SourceCount != 2 || first.Receipt.FactCount != 2 || first.Receipt.GenericMappingCount != 1 || first.Receipt.ChatGPTMappingCount != 0 || first.Receipt.VectorDimension != 2 {
		t.Fatalf("unexpected result counts: %+v", first.Receipt)
	}
	if len(first.Plan.Generic) != 1 || len(first.Plan.Generic[0].Sources) != 2 {
		t.Fatalf("unexpected generic plan: %+v", first.Plan)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("result Validate() error = %v", err)
	}
	if !reflect.DeepEqual(points, original) {
		t.Fatal("inventory mutated input")
	}
	points[0].Vector[0] = 99
	points[0].Payload["body"][0] = 'x'
	if first.Receipt.SourceSHA256 == "" || first.Plan.Generic[0].Sources[0].RecordKey == "" {
		t.Fatal("inventory did not produce a bound result")
	}
}

func TestInventoryQdrantPointsRecognizesKnownChatGPTTuple(t *testing.T) {
	const conversationID = "qdrant-inventory-chatgpt"
	known, err := BuildPlan([]LegacyThreadFact{{
		Surface: "sqlite", RecordKey: "chatgpt-source", ChatGPTConversationID: conversationID,
	}})
	if err != nil {
		t.Fatal(err)
	}
	sessionID, threadID, err := qdrantHistoricalChatGPTTuple(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := InventoryQdrantPoints(QdrantInventoryInput{
		Phase:     QdrantInventoryPhase,
		KnownPlan: known,
		Points: []QdrantPointSnapshot{{
			PointID: qdrantTestPointID("inventory-chatgpt"),
			Vector:  []float32{0.25, 0.5},
			Payload: qdrantPayloadForSession(sessionID, threadID),
		}},
	})
	if err != nil {
		t.Fatalf("InventoryQdrantPoints() error = %v", err)
	}
	if len(result.Plan.ChatGPT) != 1 || len(result.Plan.Generic) != 0 {
		t.Fatalf("unexpected plan: %+v", result.Plan)
	}
	if result.Plan.ChatGPT[0].ChatGPTConversationID != conversationID {
		t.Fatalf("ChatGPT mapping = %+v", result.Plan.ChatGPT[0])
	}
	if result.Receipt.Status != QdrantInventoryStatus {
		t.Fatalf("status = %q, want %q", result.Receipt.Status, QdrantInventoryStatus)
	}
}

func TestInventoryQdrantPointsEmitsQdrantOnlyGenericFact(t *testing.T) {
	known, err := BuildPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := InventoryQdrantPoints(QdrantInventoryInput{
		Phase:     QdrantInventoryPhase,
		KnownPlan: known,
		Points: []QdrantPointSnapshot{{
			PointID: qdrantTestPointID("inventory-generic"),
			Vector:  []float32{1},
			Payload: qdrantPayloadForSession("qdrant-only-session", 17),
		}},
	})
	if err != nil {
		t.Fatalf("InventoryQdrantPoints() error = %v", err)
	}
	if len(result.Plan.Generic) != 1 || len(result.Plan.ChatGPT) != 0 {
		t.Fatalf("unexpected plan: %+v", result.Plan)
	}
	mapping := result.Plan.Generic[0]
	if mapping.LegacyThreadID != 17 || mapping.Sources[0].Surface != QdrantInventorySurface || mapping.Sources[0].RecordKey == "" {
		t.Fatalf("generic mapping = %+v", mapping)
	}
}

func TestInventoryQdrantPointsRejectsKnownChatGPTGenericAmbiguity(t *testing.T) {
	const conversationID = "qdrant-inventory-ambiguous"
	sessionID, threadID, err := qdrantHistoricalChatGPTTuple(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	known, err := BuildPlan([]LegacyThreadFact{
		{Surface: "sqlite", RecordKey: "chatgpt-source", ChatGPTConversationID: conversationID},
		{Surface: "sqlite", RecordKey: "generic-source", SessionID: sessionID, LegacyThreadID: threadID},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = InventoryQdrantPoints(QdrantInventoryInput{
		Phase:     QdrantInventoryPhase,
		KnownPlan: known,
		Points: []QdrantPointSnapshot{{
			PointID: qdrantTestPointID("inventory-ambiguous"), Vector: []float32{1}, Payload: qdrantPayloadForSession(sessionID, threadID),
		}},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("error = %v, want ambiguity", err)
	}
}

func TestInventoryQdrantPointsRejectsStrictSourceFailures(t *testing.T) {
	known, err := BuildPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	valid := func(pointID string, vector []float32, payload map[string]json.RawMessage) []QdrantPointSnapshot {
		return []QdrantPointSnapshot{{PointID: pointID, Vector: vector, Payload: payload}}
	}
	large := strings.Repeat("x", QdrantInventoryMaxPayloadBytes)
	tests := []struct {
		name   string
		phase  string
		points []QdrantPointSnapshot
		want   string
	}{
		{name: "wrong phase", phase: "runtime", points: valid(qdrantTestPointID("inventory-wrong-phase"), []float32{1}, qdrantPayloadForSession("x", 1)), want: "phase"},
		{name: "invalid point UUID", phase: QdrantInventoryPhase, points: valid("not-a-uuid", []float32{1}, qdrantPayloadForSession("x", 1)), want: "UUID"},
		{name: "empty vector", phase: QdrantInventoryPhase, points: valid(qdrantTestPointID("inventory-empty-vector"), nil, qdrantPayloadForSession("x", 1)), want: "vector"},
		{name: "non-finite vector", phase: QdrantInventoryPhase, points: valid(qdrantTestPointID("inventory-nan"), []float32{float32(math.NaN())}, qdrantPayloadForSession("x", 1)), want: "finite"},
		{name: "inconsistent dimensions", phase: QdrantInventoryPhase, points: []QdrantPointSnapshot{
			{PointID: qdrantTestPointID("inventory-dimension-a"), Vector: []float32{1}, Payload: qdrantPayloadForSession("x", 1)},
			{PointID: qdrantTestPointID("inventory-dimension-b"), Vector: []float32{1, 2}, Payload: qdrantPayloadForSession("x", 2)},
		}, want: "dimension"},
		{name: "nil payload", phase: QdrantInventoryPhase, points: valid(qdrantTestPointID("inventory-nil-payload"), []float32{1}, nil), want: "payload"},
		{name: "malformed JSON value", phase: QdrantInventoryPhase, points: valid(qdrantTestPointID("inventory-malformed"), []float32{1}, map[string]json.RawMessage{
			"session_id": json.RawMessage(`"x"`), "thread_id": json.RawMessage(`1`), "body": json.RawMessage(`true false`),
		}), want: "JSON"},
		{name: "blank session", phase: QdrantInventoryPhase, points: valid(qdrantTestPointID("inventory-blank-session"), []float32{1}, qdrantPayloadForSession("  ", 1)), want: "session_id"},
		{name: "non-positive thread", phase: QdrantInventoryPhase, points: valid(qdrantTestPointID("inventory-zero-thread"), []float32{1}, qdrantPayloadForSession("x", 0)), want: "thread_id"},
		{name: "preexisting thread sequence", phase: QdrantInventoryPhase, points: valid(qdrantTestPointID("inventory-seq"), []float32{1}, inventoryQdrantPayloadWithField("x", 1, "thread_seq", `1`)), want: "thread_seq"},
		{name: "preexisting thread kind", phase: QdrantInventoryPhase, points: valid(qdrantTestPointID("inventory-kind"), []float32{1}, inventoryQdrantPayloadWithField("x", 1, "thread_kind", `"user_conversation"`)), want: "thread_kind"},
		{name: "oversize payload", phase: QdrantInventoryPhase, points: valid(qdrantTestPointID("inventory-oversize"), []float32{1}, map[string]json.RawMessage{
			"session_id": json.RawMessage(`"x"`), "thread_id": json.RawMessage(`1`), "body": json.RawMessage(`"` + large + `"`),
		}), want: "payload"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := InventoryQdrantPoints(QdrantInventoryInput{Phase: test.phase, KnownPlan: known, Points: test.points})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if result.Plan.MappingSHA256 != "" || result.Receipt.ReceiptSHA256 != "" {
				t.Fatalf("failed inventory returned output: %+v", result)
			}
		})
	}
}

func TestInventoryQdrantPointsRejectsDuplicateSourcePointIDs(t *testing.T) {
	known, err := BuildPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	pointID := qdrantTestPointID("inventory-duplicate")
	_, err = InventoryQdrantPoints(QdrantInventoryInput{
		Phase:     QdrantInventoryPhase,
		KnownPlan: known,
		Points: []QdrantPointSnapshot{
			{PointID: pointID, Vector: []float32{1}, Payload: qdrantPayloadForSession("x", 1)},
			{PointID: pointID, Vector: []float32{1}, Payload: qdrantPayloadForSession("x", 1)},
		},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "duplicate point id") {
		t.Fatalf("error = %v, want duplicate point ID", err)
	}
}

func TestInventoryQdrantReceiptCanonicalJSONAndValidation(t *testing.T) {
	known, err := BuildPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := InventoryQdrantPoints(QdrantInventoryInput{
		Phase:     QdrantInventoryPhase,
		KnownPlan: known,
		Points: []QdrantPointSnapshot{{
			PointID: qdrantTestPointID("inventory-receipt"), Vector: []float32{1, 2}, Payload: qdrantPayloadForSession("x", 1),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Receipt.Validate(); err != nil {
		t.Fatalf("receipt Validate() error = %v", err)
	}
	canonical, err := result.Receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte("receipt_sha256")) || bytes.Contains(canonical, []byte("payload")) || bytes.Contains(canonical, []byte("content")) || bytes.Contains(canonical, []byte("path")) {
		t.Fatalf("receipt contains forbidden source data: %s", canonical)
	}
	tampered := result.Receipt
	tampered.SourceCount++
	if err := tampered.Validate(); err == nil {
		t.Fatal("tampered receipt unexpectedly validated")
	}
}

func inventoryQdrantPoint(seed, sessionID string, threadID int64, vector []float32) QdrantPointSnapshot {
	return QdrantPointSnapshot{PointID: qdrantTestPointID(seed), Vector: vector, Payload: qdrantPayloadForSession(sessionID, threadID)}
}

func cloneQdrantInventoryPoints(points []QdrantPointSnapshot) []QdrantPointSnapshot {
	clone := make([]QdrantPointSnapshot, len(points))
	for index, point := range points {
		clone[index] = cloneQdrantTestPoint(point)
	}
	return clone
}

func inventoryQdrantPayloadWithField(sessionID string, threadID int64, key, value string) map[string]json.RawMessage {
	payload := qdrantPayloadForSession(sessionID, threadID)
	payload[key] = json.RawMessage(value)
	return payload
}

func TestInventoryQdrantPointsUsesCanonicalPositiveThreadIDInput(t *testing.T) {
	known, err := BuildPlan(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"1.0", `"1"`, "0", "-1", "01"} {
		t.Run(raw, func(t *testing.T) {
			payload := qdrantPayloadForSession("x", 1)
			payload["thread_id"] = json.RawMessage(raw)
			_, err := InventoryQdrantPoints(QdrantInventoryInput{Phase: QdrantInventoryPhase, KnownPlan: known, Points: []QdrantPointSnapshot{{
				PointID: qdrantTestPointID("inventory-thread-" + strconv.Quote(raw)), Vector: []float32{1}, Payload: payload,
			}}})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "thread_id") {
				t.Fatalf("error = %v, want thread_id validation", err)
			}
		})
	}
}
