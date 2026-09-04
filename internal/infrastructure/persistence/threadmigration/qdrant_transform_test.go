package threadmigration

import (
	"bytes"
	"encoding/json"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/google/uuid"
)

func TestPrepareQdrantPointsHappyPath(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	pointID := qdrantTestPointID("source-1")
	input := QdrantPreparationInput{
		Phase: QdrantPreparationPhase,
		Plan:  plan,
		Points: []QdrantPointSnapshot{{
			PointID: pointID,
			Vector:  []float32{1, -2.5, 0.25},
			Payload: map[string]json.RawMessage{
				"session_id": json.RawMessage(`"legacy-session"`),
				"thread_id":  json.RawMessage(`7`),
				"body":       json.RawMessage(` { "nested": true } `),
				"score":      json.RawMessage(`1.0`),
			},
		}},
	}

	result, err := PrepareQdrantPoints(input)
	if err != nil {
		t.Fatalf("PrepareQdrantPoints() error = %v", err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("output points = %d, want 1", len(result.Points))
	}
	mapping, ok := plan.LookupGeneric(string(mustQdrantSession(t, "legacy-session")), 7)
	if !ok {
		t.Fatal("test mapping not found")
	}
	point := result.Points[0]
	if point.PointID != strings.TrimPrefix(string(mapping.ThreadID), "thr_") {
		t.Fatalf("output point ID = %q, want canonical UUID %q", point.PointID, strings.TrimPrefix(string(mapping.ThreadID), "thr_"))
	}
	if !reflect.DeepEqual(point.Vector, input.Points[0].Vector) {
		t.Fatalf("vector = %#v, want %#v", point.Vector, input.Points[0].Vector)
	}
	if !bytes.Equal(point.Payload["body"], input.Points[0].Payload["body"]) || !bytes.Equal(point.Payload["score"], input.Points[0].Payload["score"]) {
		t.Fatal("nonidentity payload bytes were not preserved")
	}
	if got, want := string(point.Payload["session_id"]), `"`+string(mapping.SessionID)+`"`; got != want {
		t.Fatalf("session_id = %s, want %s", got, want)
	}
	if got, want := string(point.Payload["thread_id"]), `"`+string(mapping.ThreadID)+`"`; got != want {
		t.Fatalf("thread_id = %s, want %s", got, want)
	}
	if got, want := string(point.Payload["thread_seq"]), "7"; got != want {
		t.Fatalf("thread_seq = %s, want %s", got, want)
	}
	if got, want := string(point.Payload["thread_kind"]), `"user_conversation"`; got != want {
		t.Fatalf("thread_kind = %s, want %s", got, want)
	}
	if result.Receipt.SchemaVersion != QdrantPreparationReceiptSchemaVersion || result.Receipt.Status != QdrantPreparationStatus {
		t.Fatalf("receipt identity = %+v", result.Receipt)
	}
	if result.Receipt.SourceCount != 1 || result.Receipt.OutputCount != 1 || result.Receipt.DuplicateSourceCount != 0 || result.Receipt.VectorDimension != 3 {
		t.Fatalf("receipt counts = %+v", result.Receipt)
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result.Validate() error = %v", err)
	}
}

func TestPrepareQdrantPointsHappyChatGPTMapping(t *testing.T) {
	conversationID := "chatgpt-qdrant-conversation"
	legacySessionID, legacyThreadID, err := qdrantHistoricalChatGPTTuple(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]LegacyThreadFact{{
		Surface: "qdrant_test", RecordKey: "chatgpt-point", ChatGPTConversationID: conversationID,
	}})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	result, err := PrepareQdrantPoints(QdrantPreparationInput{
		Phase: QdrantPreparationPhase,
		Plan:  plan,
		Points: []QdrantPointSnapshot{{
			PointID: qdrantTestPointID("chatgpt-source"),
			Vector:  []float32{0.5, -0.25},
			Payload: qdrantPayloadForSession(legacySessionID, legacyThreadID),
		}},
	})
	if err != nil {
		t.Fatalf("PrepareQdrantPoints() error = %v", err)
	}
	if len(result.Points) != 1 {
		t.Fatalf("output points = %d, want 1", len(result.Points))
	}
	mapping, ok := plan.LookupChatGPT(conversationID)
	if !ok {
		t.Fatal("ChatGPT mapping not found")
	}
	point := result.Points[0]
	if point.PointID != strings.TrimPrefix(string(mapping.ThreadID), "thr_") {
		t.Fatalf("output point ID = %q, want %q", point.PointID, strings.TrimPrefix(string(mapping.ThreadID), "thr_"))
	}
	if got := string(point.Payload["session_id"]); got != `"`+string(mapping.SessionID)+`"` {
		t.Fatalf("session_id = %s", got)
	}
	if got := string(point.Payload["thread_id"]); got != `"`+string(mapping.ThreadID)+`"` {
		t.Fatalf("thread_id = %s", got)
	}
	if got := string(point.Payload["thread_seq"]); got != "1" {
		t.Fatalf("thread_seq = %s, want 1", got)
	}
	if got := string(point.Payload["thread_kind"]); got != `"user_conversation"` {
		t.Fatalf("thread_kind = %s", got)
	}
}

func TestPrepareQdrantPointsRejectsAmbiguousChatGPTGenericMapping(t *testing.T) {
	conversationID := "chatgpt-qdrant-ambiguous"
	legacySessionID, legacyThreadID, err := qdrantHistoricalChatGPTTuple(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := BuildPlan([]LegacyThreadFact{
		{Surface: "qdrant_test", RecordKey: "chatgpt", ChatGPTConversationID: conversationID},
		{Surface: "qdrant_test", RecordKey: "generic", SessionID: legacySessionID, LegacyThreadID: legacyThreadID},
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	result, err := PrepareQdrantPoints(QdrantPreparationInput{
		Phase: QdrantPreparationPhase,
		Plan:  plan,
		Points: []QdrantPointSnapshot{{
			PointID: qdrantTestPointID("chatgpt-ambiguous-source"), Vector: []float32{0.5},
			Payload: qdrantPayloadForSession(legacySessionID, legacyThreadID),
		}},
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "ambiguous") {
		t.Fatalf("error = %v, want ambiguous mapping error", err)
	}
	if len(result.Points) != 0 {
		t.Fatalf("ambiguous mapping returned %d points", len(result.Points))
	}
}

func TestPrepareQdrantPointsDeterministicAndInputImmutable(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	first := QdrantPointSnapshot{
		PointID: qdrantTestPointID("source-1"),
		Vector:  []float32{1, 2},
		Payload: map[string]json.RawMessage{
			"session_id": json.RawMessage(`"legacy-session"`),
			"thread_id":  json.RawMessage(`7`),
			"body":       json.RawMessage(`{"b":2,"a":1}`),
		},
	}
	second := QdrantPointSnapshot{
		PointID: qdrantTestPointID("source-2"),
		Vector:  []float32{1, 2},
		Payload: map[string]json.RawMessage{
			"session_id": json.RawMessage(`"legacy-session"`),
			"thread_id":  json.RawMessage(`7`),
			"body":       json.RawMessage(`{"b":2,"a":1}`),
		},
	}
	originalFirst := cloneQdrantTestPoint(first)
	originalSecond := cloneQdrantTestPoint(second)

	left, err := PrepareQdrantPoints(QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: plan, Points: []QdrantPointSnapshot{first, second}})
	if err != nil {
		t.Fatalf("first PrepareQdrantPoints() error = %v", err)
	}
	right, err := PrepareQdrantPoints(QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: plan, Points: []QdrantPointSnapshot{second, first}})
	if err != nil {
		t.Fatalf("second PrepareQdrantPoints() error = %v", err)
	}
	if !reflect.DeepEqual(left.Points, right.Points) {
		t.Fatalf("permuted input changed output:\nleft=%#v\nright=%#v", left.Points, right.Points)
	}
	leftReceipt, err := left.Receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	rightReceipt, err := right.Receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leftReceipt, rightReceipt) || left.Receipt.ReceiptSHA256 != right.Receipt.ReceiptSHA256 || left.Receipt.SourceSHA256 != right.Receipt.SourceSHA256 || left.Receipt.OutputSHA256 != right.Receipt.OutputSHA256 {
		t.Fatalf("permuted input changed hashes: left=%+v right=%+v", left.Receipt, right.Receipt)
	}
	if !reflect.DeepEqual(first, originalFirst) || !reflect.DeepEqual(second, originalSecond) {
		t.Fatal("PrepareQdrantPoints mutated input")
	}
	first.Vector[0] = 99
	first.Payload["body"][0] = 'x'
	if left.Points[0].Vector[0] == 99 || left.Points[0].Payload["body"][0] == 'x' {
		t.Fatal("output aliases input storage")
	}
}

func TestPrepareQdrantPointsDeduplicatesEquivalentSources(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	points := []QdrantPointSnapshot{
		{
			PointID: qdrantTestPointID("source-a"),
			Vector:  []float32{1, 2},
			Payload: qdrantPayloadWithBody(`{"b":2,"a":1}`),
		},
		{
			PointID: qdrantTestPointID("source-b"),
			Vector:  []float32{1, 2},
			Payload: qdrantPayloadWithBody(` { "a" : 1, "b" : 2 } `),
		},
	}
	result, err := PrepareQdrantPoints(QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: plan, Points: points})
	if err != nil {
		t.Fatalf("PrepareQdrantPoints() error = %v", err)
	}
	if result.Receipt.SourceCount != 2 || result.Receipt.OutputCount != 1 || result.Receipt.DuplicateSourceCount != 1 {
		t.Fatalf("receipt counts = %+v", result.Receipt)
	}
	if got, want := string(result.Points[0].Payload["body"]), ` { "a" : 1, "b" : 2 } `; got != want {
		t.Fatalf("deterministic duplicate payload representative = %q, want %q", got, want)
	}
	if result.Receipt.OutputCount+result.Receipt.DuplicateSourceCount != result.Receipt.SourceCount {
		t.Fatal("deduplication count invariant is false")
	}
}

func TestPrepareQdrantPointsRejectsConflictingDuplicateSources(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	base := QdrantPointSnapshot{
		PointID: qdrantTestPointID("source-a"),
		Vector:  []float32{1, 2},
		Payload: qdrantLegacyPayload("7"),
	}
	tests := []struct {
		name   string
		second QdrantPointSnapshot
	}{
		{
			name: "vector",
			second: QdrantPointSnapshot{
				PointID: qdrantTestPointID("source-b"), Vector: []float32{1, 3}, Payload: qdrantLegacyPayload("7"),
			},
		},
		{
			name: "payload",
			second: QdrantPointSnapshot{
				PointID: qdrantTestPointID("source-b"), Vector: []float32{1, 2}, Payload: qdrantPayloadWithBody(`{"different":true}`),
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := PrepareQdrantPoints(QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: plan, Points: []QdrantPointSnapshot{base, test.second}})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "different") {
				t.Fatalf("error = %v, want conflicting duplicate error", err)
			}
			if len(result.Points) != 0 {
				t.Fatalf("conflicting duplicate returned %d points", len(result.Points))
			}
		})
	}
}

func TestPrepareQdrantPointsRejectsMissingMapping(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	input := QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: plan, Points: []QdrantPointSnapshot{{
		PointID: qdrantTestPointID("missing"), Vector: []float32{1}, Payload: qdrantLegacyPayload(`8`),
	}}}
	result, err := PrepareQdrantPoints(input)
	if err == nil || !strings.Contains(err.Error(), "mapping") {
		t.Fatalf("missing mapping error = %v", err)
	}
	if len(result.Points) != 0 || result.Receipt.SourceCount != 0 {
		t.Fatalf("failed preparation returned output: %+v", result)
	}
}

func TestPrepareQdrantPointsRejectsWrongPhase(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	input := QdrantPreparationInput{Phase: "wrong-phase", Plan: plan, Points: []QdrantPointSnapshot{{
		PointID: qdrantTestPointID("wrong-phase"), Vector: []float32{1}, Payload: qdrantLegacyPayload(`7`),
	}}}
	result, err := PrepareQdrantPoints(input)
	if err == nil || !strings.Contains(err.Error(), "phase") {
		t.Fatalf("wrong phase error = %v", err)
	}
	if len(result.Points) != 0 || result.Receipt.SourceCount != 0 {
		t.Fatalf("wrong phase returned output: %+v", result)
	}
}

func TestPrepareQdrantPointsRejectsMissingPhase(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	result, err := PrepareQdrantPoints(QdrantPreparationInput{Plan: plan, Points: []QdrantPointSnapshot{{
		PointID: qdrantTestPointID("missing-phase"), Vector: []float32{1}, Payload: qdrantLegacyPayload("7"),
	}}})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "phase") {
		t.Fatalf("missing phase error = %v", err)
	}
	if len(result.Points) != 0 {
		t.Fatalf("missing phase returned %d points", len(result.Points))
	}
}

func TestPrepareQdrantPointsRejectsInvalidVectorsAndDuplicatePointIDs(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	valid := func(id string, vector []float32) QdrantPointSnapshot {
		return QdrantPointSnapshot{PointID: id, Vector: vector, Payload: qdrantLegacyPayload(`7`)}
	}
	tests := []struct {
		name   string
		points []QdrantPointSnapshot
		want   string
	}{
		{name: "empty", points: []QdrantPointSnapshot{valid(qdrantTestPointID("empty"), nil)}, want: "vector"},
		{name: "nan", points: []QdrantPointSnapshot{valid(qdrantTestPointID("nan"), []float32{float32(math.NaN())})}, want: "finite"},
		{name: "infinite", points: []QdrantPointSnapshot{valid(qdrantTestPointID("infinite"), []float32{float32(math.Inf(1))})}, want: "finite"},
		{name: "inconsistent dimension", points: []QdrantPointSnapshot{valid(qdrantTestPointID("dimension-a"), []float32{1}), valid(qdrantTestPointID("dimension-b"), []float32{1, 2})}, want: "dimension"},
		{name: "duplicate point id", points: []QdrantPointSnapshot{valid(qdrantTestPointID("duplicate"), []float32{1}), valid(qdrantTestPointID("duplicate"), []float32{1})}, want: "duplicate point ID"},
		{name: "invalid uuid", points: []QdrantPointSnapshot{valid("not-a-uuid", []float32{1})}, want: "UUID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := PrepareQdrantPoints(QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: plan, Points: test.points})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if len(result.Points) != 0 {
				t.Fatalf("failed preparation returned %d points", len(result.Points))
			}
		})
	}
}

func TestPrepareQdrantPointsRejectsMalformedIdentityAndPayload(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	tests := []struct {
		name    string
		payload map[string]json.RawMessage
		want    string
	}{
		{name: "missing session", payload: map[string]json.RawMessage{"thread_id": json.RawMessage(`7`)}, want: "session_id"},
		{name: "wrong session type", payload: map[string]json.RawMessage{"session_id": json.RawMessage(`7`), "thread_id": json.RawMessage(`7`)}, want: "session_id"},
		{name: "blank session", payload: map[string]json.RawMessage{"session_id": json.RawMessage(`"  "`), "thread_id": json.RawMessage(`7`)}, want: "session_id"},
		{name: "missing thread", payload: map[string]json.RawMessage{"session_id": json.RawMessage(`"legacy-session"`)}, want: "thread_id"},
		{name: "wrong thread type", payload: map[string]json.RawMessage{"session_id": json.RawMessage(`"legacy-session"`), "thread_id": json.RawMessage(`"7"`)}, want: "thread_id"},
		{name: "noncanonical thread number", payload: map[string]json.RawMessage{"session_id": json.RawMessage(`"legacy-session"`), "thread_id": json.RawMessage(`7.0`)}, want: "thread_id"},
		{name: "zero thread number", payload: map[string]json.RawMessage{"session_id": json.RawMessage(`"legacy-session"`), "thread_id": json.RawMessage(`0`)}, want: "thread_id"},
		{name: "preexisting sequence", payload: map[string]json.RawMessage{"session_id": json.RawMessage(`"legacy-session"`), "thread_id": json.RawMessage(`7`), "thread_seq": json.RawMessage(`null`)}, want: "thread_seq"},
		{name: "preexisting kind", payload: map[string]json.RawMessage{"session_id": json.RawMessage(`"legacy-session"`), "thread_id": json.RawMessage(`7`), "thread_kind": json.RawMessage(`"user_conversation"`)}, want: "thread_kind"},
		{name: "invalid JSON value", payload: map[string]json.RawMessage{"session_id": json.RawMessage(`"legacy-session"`), "thread_id": json.RawMessage(`7`), "body": json.RawMessage(`true false`)}, want: "JSON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := PrepareQdrantPoints(QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: plan, Points: []QdrantPointSnapshot{{
				PointID: qdrantTestPointID(test.name), Vector: []float32{1}, Payload: test.payload,
			}}})
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.want)) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
			if len(result.Points) != 0 {
				t.Fatalf("failed preparation returned %d points", len(result.Points))
			}
		})
	}
}

func TestPrepareQdrantPointsRejectsInvalidPlanAndOversizePayload(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	tampered := plan
	tampered.MappingSHA256 = strings.Repeat("0", 64)
	input := QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: tampered, Points: []QdrantPointSnapshot{{
		PointID: qdrantTestPointID("tampered"), Vector: []float32{1}, Payload: qdrantLegacyPayload(`7`),
	}}}
	result, err := PrepareQdrantPoints(input)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "plan") {
		t.Fatalf("tampered plan error = %v", err)
	}
	if len(result.Points) != 0 {
		t.Fatalf("tampered plan returned output: %+v", result)
	}

	large := strings.Repeat("x", QdrantPreparationMaxPayloadBytes)
	result, err = PrepareQdrantPoints(QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: plan, Points: []QdrantPointSnapshot{{
		PointID: qdrantTestPointID("oversize"), Vector: []float32{1}, Payload: map[string]json.RawMessage{
			"session_id": json.RawMessage(`"legacy-session"`), "thread_id": json.RawMessage(`7`), "body": json.RawMessage(`"` + large + `"`),
		},
	}}})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "payload") {
		t.Fatalf("oversize payload error = %v", err)
	}
	if len(result.Points) != 0 {
		t.Fatalf("oversize payload returned output: %+v", result)
	}
}

func TestQdrantPreparationReceiptValidationAndCanonicalJSON(t *testing.T) {
	plan := buildQdrantTestPlan(t, "legacy-session", 7)
	result, err := PrepareQdrantPoints(QdrantPreparationInput{Phase: QdrantPreparationPhase, Plan: plan, Points: []QdrantPointSnapshot{{
		PointID: qdrantTestPointID("receipt"), Vector: []float32{1, 2}, Payload: qdrantLegacyPayload(`7`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := result.Receipt.Validate(); err != nil {
		t.Fatalf("receipt.Validate() error = %v", err)
	}
	canonical, err := result.Receipt.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(canonical, []byte("receipt_sha256")) {
		t.Fatalf("canonical receipt includes self hash: %s", canonical)
	}
	tampered := result.Receipt
	tampered.OutputCount++
	if err := tampered.Validate(); err == nil {
		t.Fatal("tampered receipt unexpectedly validated")
	}
}

func buildQdrantTestPlan(t *testing.T, sourceSession string, legacyThreadID int64) Plan {
	t.Helper()
	plan, err := BuildPlan([]LegacyThreadFact{{
		Surface: "qdrant_test", RecordKey: "point", SessionID: sourceSession, LegacyThreadID: legacyThreadID,
		KindHint: string(modulecore.ThreadKindUserConversation),
	}})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	return plan
}

func mustQdrantSession(t *testing.T, sourceSession string) modulecore.SessionID {
	t.Helper()
	sessionID, err := canonicalGenericSessionID(sourceSession)
	if err != nil {
		t.Fatalf("canonicalGenericSessionID() error = %v", err)
	}
	return modulecore.SessionID(sessionID)
}

func qdrantLegacyPayload(threadID string) map[string]json.RawMessage {
	return qdrantPayloadForSession("legacy-session", mustQdrantTestThreadID(threadID))
}

func qdrantPayloadForSession(sessionID string, threadID int64) map[string]json.RawMessage {
	return map[string]json.RawMessage{
		"session_id": json.RawMessage(`"` + sessionID + `"`),
		"thread_id":  json.RawMessage(strconv.FormatInt(threadID, 10)),
		"body":       json.RawMessage(`{"message":"same"}`),
	}
}

func mustQdrantTestThreadID(raw string) int64 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		panic(err)
	}
	return value
}

func qdrantPayloadWithBody(body string) map[string]json.RawMessage {
	payload := qdrantLegacyPayload("7")
	payload["body"] = json.RawMessage(body)
	return payload
}

func qdrantTestPointID(seed string) string {
	return uuid.NewSHA1(uuid.Nil, []byte("qdrant:"+seed)).String()
}

func cloneQdrantTestPoint(point QdrantPointSnapshot) QdrantPointSnapshot {
	clone := QdrantPointSnapshot{PointID: point.PointID, Vector: append([]float32(nil), point.Vector...), Payload: make(map[string]json.RawMessage, len(point.Payload))}
	for key, value := range point.Payload {
		clone.Payload[key] = append(json.RawMessage(nil), value...)
	}
	return clone
}
