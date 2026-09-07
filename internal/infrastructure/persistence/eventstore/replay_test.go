package eventstore

import (
	"context"
	"path/filepath"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestReadComponentPageKeepsWatermarkAcrossAppendAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "events.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	trace := modulecore.NewTraceID()
	for _, component := range []string{"orchestrator", "other", "orchestrator", "orchestrator"} {
		if err := store.Append(ctx, eventFixture(trace, "test.event", component)); err != nil {
			t.Fatal(err)
		}
	}
	first, through, err := store.ReadComponentPage(ctx, "orchestrator", 0, 0, 1)
	if err != nil || len(first) != 1 || first[0].EventSeq != 1 || through != 4 {
		t.Fatalf("first=%v through=%d err=%v", first, through, err)
	}
	if err := store.Append(ctx, eventFixture(trace, "late.event", "orchestrator")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	rest, sameThrough, err := store.ReadComponentPage(ctx, "orchestrator", first[0].EventSeq, through, 10)
	if err != nil || sameThrough != through || len(rest) != 2 || rest[0].EventSeq != 3 || rest[1].EventSeq != 4 {
		t.Fatalf("rest=%v through=%d err=%v", rest, sameThrough, err)
	}
	empty, _, err := store.ReadComponentPage(ctx, "orchestrator", through, through, 1)
	if err != nil || len(empty) != 0 {
		t.Fatalf("end=%v err=%v", empty, err)
	}
	latest, upper, err := store.ReadComponentPage(ctx, "orchestrator", through, 0, 10)
	if err != nil || len(latest) != 1 || latest[0].EventSeq != 5 || upper != 5 {
		t.Fatalf("next=%v upper=%d err=%v", latest, upper, err)
	}
}

func TestReadComponentPageRejectsInvalidCursorAndCancelledContext(t *testing.T) {
	store := newTestStore(t)
	for _, query := range []struct {
		component      string
		after, through modulecore.EventSeq
		limit          int
	}{
		{"", 0, 0, 1}, {" orchestrator", 0, 0, 1}, {"orchestrator", -1, 0, 1}, {"orchestrator", 0, -1, 1},
		{"orchestrator", 0, 0, 0}, {"orchestrator", 0, 0, maxListLimit + 1}, {"orchestrator", 1, 0, 1}, {"orchestrator", 0, 1, 1},
	} {
		if _, _, err := store.ReadComponentPage(context.Background(), query.component, query.after, query.through, query.limit); err == nil {
			t.Fatalf("accepted invalid query %+v", query)
		}
	}
	if _, _, err := store.ReadComponentPage(nil, "orchestrator", 0, 0, 1); err == nil {
		t.Fatal("accepted nil context")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.ReadComponentPage(ctx, "orchestrator", 0, 0, 1); err == nil {
		t.Fatal("ignored cancellation")
	}
}
