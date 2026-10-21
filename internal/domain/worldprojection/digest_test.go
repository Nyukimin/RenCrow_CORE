package worldprojection

import (
	"testing"
	"time"
)

func TestPayloadDigestUsesCanonicalJSON(t *testing.T) {
	left, err := PayloadDigest([]byte(`{"b":2,"a":1,"nested":{"z":true,"y":null}}`))
	if err != nil {
		t.Fatalf("PayloadDigest(left) error = %v", err)
	}
	right, err := PayloadDigest([]byte(`{"nested":{"y":null,"z":true},"a":1,"b":2}`))
	if err != nil {
		t.Fatalf("PayloadDigest(right) error = %v", err)
	}
	if left != right {
		t.Fatalf("canonical digests differ: %s != %s", left, right)
	}
}

func TestWorkSetContentDigestIgnoresRevisionAndValidity(t *testing.T) {
	work := []WorkItem{{
		TaskID: "task-1", ExecutionState: ExecutionQueued,
		WorkClass: WorkClassAssignedWork, ClassificationReasonCode: "assigned",
	}}
	first, err := NewWorkSet("world", "mio", "epoch", 1, time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), work)
	if err != nil {
		t.Fatalf("NewWorkSet(first) error = %v", err)
	}
	second, err := NewWorkSet("world", "mio", "epoch", 9, time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), work)
	if err != nil {
		t.Fatalf("NewWorkSet(second) error = %v", err)
	}
	left, err := WorkSetContentDigest(first)
	if err != nil {
		t.Fatalf("WorkSetContentDigest(first) error = %v", err)
	}
	right, err := WorkSetContentDigest(second)
	if err != nil {
		t.Fatalf("WorkSetContentDigest(second) error = %v", err)
	}
	if left != right {
		t.Fatalf("content digests differ: %s != %s", left, right)
	}
	changed := []WorkItem{{
		TaskID: "task-2", ExecutionState: ExecutionBlocked,
		WorkClass: WorkClassAssignedWork, ClassificationReasonCode: "blocked",
	}}
	third, err := NewWorkSet("world", "mio", "epoch", 1, time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC), changed)
	if err != nil {
		t.Fatalf("NewWorkSet(third) error = %v", err)
	}
	changedDigest, err := WorkSetContentDigest(third)
	if err != nil {
		t.Fatalf("WorkSetContentDigest(third) error = %v", err)
	}
	if changedDigest == left {
		t.Fatal("content digest did not change with active work")
	}
}

func TestPayloadDigestRejectsNonIntegerNumberAndTrailingData(t *testing.T) {
	for _, raw := range []string{`{"revision":1.5}`, `{"revision":1} trailing`} {
		if _, err := PayloadDigest([]byte(raw)); err == nil {
			t.Fatalf("PayloadDigest(%q) error = nil", raw)
		}
	}
}

func TestLifePolicyContentDigestIgnoresRevisionAndValidity(t *testing.T) {
	allowed := []string{"idle", "rest", "walk"}
	fallback := []string{"idle", "rest"}
	first, err := NewLifePolicy("world", "mio", "epoch", 1,
		time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		"persona-sha256:one", "agent-profile-sha256:one", allowed, fallback)
	if err != nil {
		t.Fatalf("NewLifePolicy(first) error = %v", err)
	}
	second, err := NewLifePolicy("world", "mio", "epoch", 9,
		time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
		"persona-sha256:one", "agent-profile-sha256:one", allowed, fallback)
	if err != nil {
		t.Fatalf("NewLifePolicy(second) error = %v", err)
	}
	left, err := LifePolicyContentDigest(first)
	if err != nil {
		t.Fatalf("LifePolicyContentDigest(first) error = %v", err)
	}
	right, err := LifePolicyContentDigest(second)
	if err != nil {
		t.Fatalf("LifePolicyContentDigest(second) error = %v", err)
	}
	if left != right {
		t.Fatalf("content digests differ: %s != %s", left, right)
	}
	changed, err := NewLifePolicy("world", "mio", "epoch", 1,
		time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC),
		"persona-sha256:two", "agent-profile-sha256:one", allowed, fallback)
	if err != nil {
		t.Fatalf("NewLifePolicy(changed) error = %v", err)
	}
	changedDigest, err := LifePolicyContentDigest(changed)
	if err != nil {
		t.Fatalf("LifePolicyContentDigest(changed) error = %v", err)
	}
	if changedDigest == left {
		t.Fatal("content digest did not change with persona revision")
	}
}
