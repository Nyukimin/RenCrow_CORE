package vectordb

import (
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"github.com/qdrant/go-client/qdrant"
)

func testThreadSummary(t *testing.T) *conversation.ThreadSummary {
	t.Helper()
	sessionRaw, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "vectordb_test", "session_id", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	threadRaw, err := modulecore.NewMigrationID(modulecore.CanonicalThreadID, "vectordb_test", "thread_id", "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	return &conversation.ThreadSummary{
		ThreadID:   modulecore.ThreadID(threadRaw),
		ThreadSeq:  2,
		ThreadKind: modulecore.ThreadKindUserConversation,
		SessionID:  sessionRaw,
		Domain:     "conversation",
		Summary:    "summary",
		Keywords:   []string{"keyword"},
		Embedding:  []float32{0.1, 0.2},
		StartTime:  now,
		EndTime:    now.Add(time.Minute),
	}
}

func TestVectorDBStore_SaveThreadSummaryPayloadRoundTripsCanonicalTuple(t *testing.T) {
	summary := testThreadSummary(t)
	pointID, err := canonicalThreadSummaryPointID(summary.ThreadID)
	if err != nil {
		t.Fatalf("canonicalThreadSummaryPointID() error = %v", err)
	}
	point, err := threadSummaryPoint(summary, pointID)
	if err != nil {
		t.Fatalf("threadSummaryPoint() error = %v", err)
	}
	if got := point.GetId().GetUuid(); got != pointID {
		t.Fatalf("point ID = %q, want canonical Thread UUID %q", got, pointID)
	}

	if got, ok := point.Payload["thread_id"].GetKind().(*qdrant.Value_StringValue); !ok || got.StringValue != string(summary.ThreadID) {
		t.Fatalf("thread_id payload = %#v, want canonical string %q", point.Payload["thread_id"], summary.ThreadID)
	}
	if got, ok := point.Payload["thread_seq"].GetKind().(*qdrant.Value_IntegerValue); !ok || got.IntegerValue != int64(summary.ThreadSeq) {
		t.Fatalf("thread_seq payload = %#v, want %d", point.Payload["thread_seq"], summary.ThreadSeq)
	}
	if got, ok := point.Payload["thread_kind"].GetKind().(*qdrant.Value_StringValue); !ok || got.StringValue != string(summary.ThreadKind) {
		t.Fatalf("thread_kind payload = %#v, want %q", point.Payload["thread_kind"], summary.ThreadKind)
	}

	decoded, err := pointToThreadSummary(&qdrant.ScoredPoint{Payload: point.Payload})
	if err != nil {
		t.Fatalf("pointToThreadSummary() error = %v", err)
	}
	if decoded.ThreadID != summary.ThreadID || decoded.ThreadSeq != summary.ThreadSeq || decoded.ThreadKind != summary.ThreadKind {
		t.Fatalf("scored tuple = %s/%d/%s, want %s/%d/%s", decoded.ThreadID, decoded.ThreadSeq, decoded.ThreadKind, summary.ThreadID, summary.ThreadSeq, summary.ThreadKind)
	}

	retrieved, err := retrievedPointToThreadSummary(&qdrant.RetrievedPoint{Payload: point.Payload})
	if err != nil {
		t.Fatalf("retrievedPointToThreadSummary() error = %v", err)
	}
	if retrieved.ThreadID != summary.ThreadID || retrieved.ThreadSeq != summary.ThreadSeq || retrieved.ThreadKind != summary.ThreadKind {
		t.Fatalf("retrieved tuple = %s/%d/%s, want %s/%d/%s", retrieved.ThreadID, retrieved.ThreadSeq, retrieved.ThreadKind, summary.ThreadID, summary.ThreadSeq, summary.ThreadKind)
	}
}

func TestVectorDBStore_SaveThreadSummaryValidatesTupleBeforeUpsert(t *testing.T) {
	base := testThreadSummary(t)
	pointID, err := canonicalThreadSummaryPointID(base.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(*conversation.ThreadSummary)
		want   string
	}{
		{name: "missing thread id", mutate: func(summary *conversation.ThreadSummary) { summary.ThreadID = "" }, want: "thread_id"},
		{name: "missing thread seq", mutate: func(summary *conversation.ThreadSummary) { summary.ThreadSeq = 0 }, want: "thread sequence"},
		{name: "wrong thread kind", mutate: func(summary *conversation.ThreadSummary) { summary.ThreadKind = "wrong" }, want: "thread kind"},
		{name: "legacy session id", mutate: func(summary *conversation.ThreadSummary) { summary.SessionID = "legacy-session" }, want: "session_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			summary := *base
			tc.mutate(&summary)
			err := (&VectorDBStore{}).SaveThreadSummaryWithPointID(t.Context(), &summary, pointID)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SaveThreadSummaryWithPointID() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestCanonicalThreadSummaryPointIDUsesThreadIdentityAndRejectsMismatch(t *testing.T) {
	summary := testThreadSummary(t)
	want := strings.TrimPrefix(string(summary.ThreadID), "thr_")
	got, err := canonicalThreadSummaryPointID(summary.ThreadID)
	if err != nil {
		t.Fatalf("canonicalThreadSummaryPointID() error = %v", err)
	}
	if got != want {
		t.Fatalf("canonicalThreadSummaryPointID() = %q, want %q", got, want)
	}
	if _, err := threadSummaryPoint(summary, "00000000-0000-5000-8000-000000000001"); err == nil || !strings.Contains(err.Error(), "canonical Thread UUID") {
		t.Fatalf("threadSummaryPoint(mismatched ID) error = %v", err)
	}
	if _, err := canonicalThreadSummaryPointID("123"); err == nil {
		t.Fatal("canonicalThreadSummaryPointID(legacy numeric) succeeded")
	}
}

func TestVectorDBStore_ThreadSummaryDecodersRejectMissingWrongAndLegacyTuple(t *testing.T) {
	valid := threadSummaryPayload(testThreadSummary(t))
	cases := []struct {
		name   string
		mutate func(map[string]*qdrant.Value)
		want   string
	}{
		{name: "missing thread id", mutate: func(payload map[string]*qdrant.Value) { delete(payload, "thread_id") }, want: "thread_id"},
		{name: "numeric legacy thread id", mutate: func(payload map[string]*qdrant.Value) {
			payload["thread_id"] = &qdrant.Value{Kind: &qdrant.Value_IntegerValue{IntegerValue: 123}}
		}, want: "string"},
		{name: "missing thread seq", mutate: func(payload map[string]*qdrant.Value) { delete(payload, "thread_seq") }, want: "thread_seq"},
		{name: "wrong thread seq type", mutate: func(payload map[string]*qdrant.Value) {
			payload["thread_seq"] = &qdrant.Value{Kind: &qdrant.Value_StringValue{StringValue: "2"}}
		}, want: "integer"},
		{name: "missing thread kind", mutate: func(payload map[string]*qdrant.Value) { delete(payload, "thread_kind") }, want: "thread_kind"},
		{name: "wrong thread kind type", mutate: func(payload map[string]*qdrant.Value) {
			payload["thread_kind"] = &qdrant.Value{Kind: &qdrant.Value_IntegerValue{IntegerValue: 2}}
		}, want: "string"},
	}
	for _, tc := range cases {
		t.Run("scored/"+tc.name, func(t *testing.T) {
			payload := cloneThreadSummaryPayload(valid)
			tc.mutate(payload)
			_, err := pointToThreadSummary(&qdrant.ScoredPoint{Payload: payload})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("pointToThreadSummary() error = %v, want %q", err, tc.want)
			}
		})
		t.Run("retrieved/"+tc.name, func(t *testing.T) {
			payload := cloneThreadSummaryPayload(valid)
			tc.mutate(payload)
			_, err := retrievedPointToThreadSummary(&qdrant.RetrievedPoint{Payload: payload})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("retrievedPointToThreadSummary() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func cloneThreadSummaryPayload(payload map[string]*qdrant.Value) map[string]*qdrant.Value {
	clone := make(map[string]*qdrant.Value, len(payload))
	for key, value := range payload {
		clone[key] = value
	}
	return clone
}
