package l1sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

// claimableMaxAttempts is the maxAttempts both the probe and the claim receive in
// these tests.
const claimableMaxAttempts = 5

// claimableNow is the shared "now": the probe and the claim must read the same
// clock, as Service.RunOne gives them.
var claimableNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// claimableJobSnapshot is every job column the claim may rewrite, as stored text,
// so a claim that changed nothing compares equal to the row before it.
type claimableJobSnapshot struct {
	State        string
	AttemptCount int
	LeaseToken   string
	LeaseExpires string
	NextAttempt  string
	UpdatedAt    string
}

func snapshotClaimableJobs(t *testing.T, store *L1SQLiteStore) []claimableJobSnapshot {
	t.Helper()
	rows, err := store.db.QueryContext(context.Background(), `
SELECT state, attempt_count, lease_token,
	COALESCE(CAST(lease_expires_at AS TEXT), ''), COALESCE(CAST(next_attempt_at AS TEXT), ''), CAST(updated_at AS TEXT)
FROM l1_profile_promotion_job
ORDER BY evidence_event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []claimableJobSnapshot
	for rows.Next() {
		var s claimableJobSnapshot
		if err := rows.Scan(&s.State, &s.AttemptCount, &s.LeaseToken, &s.LeaseExpires, &s.NextAttempt, &s.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// claimableJob describes one job row under test. A nil time is stored as NULL.
type claimableJob struct {
	state        string
	attemptCount int
	leaseExpires *time.Time
	nextAttempt  *time.Time
}

func at(offset time.Duration) *time.Time {
	value := claimableNow.Add(offset)
	return &value
}

func nullableTime(value *time.Time) interface{} {
	if value == nil {
		return nil
	}
	return *value
}

// seedClaimableJobs saves one user message per job (each creates a pending job
// with its evidence event) and then rewrites the job row to the state under test.
func seedClaimableJobs(t *testing.T, store *L1SQLiteStore, jobs []claimableJob) {
	t.Helper()
	ctx := context.Background()
	for i := range jobs {
		thread := string(rune('a'+i)) + "-claimable"
		if err := l1TestSaveMessageInNamespace(store, ctx, "ren", thread, 1, "conv:"+thread,
			domconv.NewMessage(domconv.SpeakerUser, "claimable job "+thread, nil), MemoryStateObserved); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.db.QueryContext(ctx, `SELECT evidence_event_id FROM l1_profile_promotion_job ORDER BY evidence_event_id`)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != len(jobs) {
		t.Fatalf("seeded %d jobs, want %d", len(ids), len(jobs))
	}
	for i, job := range jobs {
		leaseToken := ""
		if job.state == domainmemory.ProfilePromotionRunning {
			leaseToken = "lease-" + ids[i]
		}
		if _, err := store.db.ExecContext(ctx, `
UPDATE l1_profile_promotion_job
SET state = ?, attempt_count = ?, lease_token = ?, lease_expires_at = ?, next_attempt_at = ?
WHERE evidence_event_id = ?`,
			job.state, job.attemptCount, leaseToken, nullableTime(job.leaseExpires), nullableTime(job.nextAttempt), ids[i]); err != nil {
			t.Fatal(err)
		}
	}
}

// claimableCases is the table the probe is checked against. want is whether the
// claim would act: return a batch or rewrite any job. The probe must be true for
// every such state (no false negative) and, for these states, false for the rest.
var claimableCases = []struct {
	name string
	jobs []claimableJob
	want bool
}{
	{"no jobs", nil, false},
	{"pending first attempt", []claimableJob{{state: "pending"}}, true},
	{"pending below the limit", []claimableJob{{state: "pending", attemptCount: claimableMaxAttempts - 1}}, true},
	{"pending at the limit is finalized by the claim", []claimableJob{{state: "pending", attemptCount: claimableMaxAttempts}}, true},
	{"pending over the limit", []claimableJob{{state: "pending", attemptCount: claimableMaxAttempts + 4}}, true},
	{"retry wait that became due", []claimableJob{{state: "retry_wait", attemptCount: 1, nextAttempt: at(-time.Second)}}, true},
	{"retry wait due exactly now", []claimableJob{{state: "retry_wait", attemptCount: 1, nextAttempt: at(0)}}, true},
	{"retry wait still in the future", []claimableJob{{state: "retry_wait", attemptCount: 1, nextAttempt: at(time.Second)}}, false},
	{"retry wait without a next attempt", []claimableJob{{state: "retry_wait", attemptCount: 1}}, false},
	{"exhausted retry wait in the future", []claimableJob{{state: "retry_wait", attemptCount: claimableMaxAttempts, nextAttempt: at(time.Hour)}}, true},
	{"exhausted retry wait without a next attempt", []claimableJob{{state: "retry_wait", attemptCount: claimableMaxAttempts}}, true},
	{"running with an expired lease", []claimableJob{{state: "running", attemptCount: 1, leaseExpires: at(-time.Second)}}, true},
	{"running with a lease expiring exactly now", []claimableJob{{state: "running", attemptCount: 1, leaseExpires: at(0)}}, true},
	{"running with a live lease", []claimableJob{{state: "running", attemptCount: 1, leaseExpires: at(time.Second)}}, false},
	{"running without a lease expiry", []claimableJob{{state: "running", attemptCount: 1}}, false},
	{"running expired and exhausted", []claimableJob{{state: "running", attemptCount: claimableMaxAttempts, leaseExpires: at(-time.Minute)}}, true},
	{"completed", []claimableJob{{state: "completed", attemptCount: 1}}, false},
	{"completed with stale timestamps", []claimableJob{{state: "completed", attemptCount: 1, leaseExpires: at(-time.Hour), nextAttempt: at(-time.Hour)}}, false},
	{"failed", []claimableJob{{state: "failed", attemptCount: claimableMaxAttempts}}, false},
	{"failed with stale timestamps", []claimableJob{{state: "failed", attemptCount: claimableMaxAttempts, leaseExpires: at(-time.Hour), nextAttempt: at(-time.Hour)}}, false},
	{"only idle states together", []claimableJob{
		{state: "completed", attemptCount: 1},
		{state: "failed", attemptCount: claimableMaxAttempts},
		{state: "retry_wait", attemptCount: 2, nextAttempt: at(time.Hour)},
		{state: "running", attemptCount: 1, leaseExpires: at(time.Hour)},
	}, false},
	{"one pending among idle states", []claimableJob{
		{state: "completed", attemptCount: 1},
		{state: "failed", attemptCount: claimableMaxAttempts},
		{state: "retry_wait", attemptCount: 2, nextAttempt: at(time.Hour)},
		{state: "pending"},
	}, true},
	{"one expired lease among idle states", []claimableJob{
		{state: "completed", attemptCount: 1},
		{state: "retry_wait", attemptCount: 2, nextAttempt: at(time.Hour)},
		{state: "running", attemptCount: 1, leaseExpires: at(-time.Hour)},
	}, true},
}

// The probe must never be narrower than the claim. For every state the claim
// would act on, the probe is true; and where the probe says false the claim must
// leave every job row byte-identical and return no batch. This is the property
// that lets the caller skip the claim.
func TestHasClaimableProfilePromotionJobMatchesWhatTheClaimWouldDo(t *testing.T) {
	for _, tc := range claimableCases {
		t.Run(tc.name, func(t *testing.T) {
			store := newProfilePromotionTestStore(t)
			ctx := context.Background()
			seedClaimableJobs(t, store, tc.jobs)

			got, err := store.HasClaimableProfilePromotionJob(ctx, claimableMaxAttempts, claimableNow)
			if err != nil {
				t.Fatal(err)
			}

			before := snapshotClaimableJobs(t, store)
			batch, err := store.ClaimProfilePromotionBatch(ctx, 24, claimableMaxAttempts, time.Minute, claimableNow)
			if err != nil {
				t.Fatal(err)
			}
			after := snapshotClaimableJobs(t, store)
			claimActed := batch != nil || !equalClaimableSnapshots(before, after)

			if !got && claimActed {
				t.Fatalf("false negative: probe=false but the claim acted (batch=%v rows_before=%+v rows_after=%+v)", batch != nil, before, after)
			}
			if got != tc.want {
				t.Fatalf("probe = %v, want %v", got, tc.want)
			}
			if claimActed != tc.want {
				t.Fatalf("the claim acted = %v, want %v: the table's expectation does not match the claim", claimActed, tc.want)
			}
		})
	}
}

func equalClaimableSnapshots(a, b []claimableJobSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The probe is read-only: asking never changes a job, whatever its state.
func TestHasClaimableProfilePromotionJobDoesNotWrite(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	seedClaimableJobs(t, store, []claimableJob{
		{state: "pending", attemptCount: claimableMaxAttempts},
		{state: "retry_wait", attemptCount: 1, nextAttempt: at(-time.Hour)},
		{state: "running", attemptCount: 1, leaseExpires: at(-time.Hour)},
	})
	before := snapshotClaimableJobs(t, store)

	got, err := store.HasClaimableProfilePromotionJob(ctx, claimableMaxAttempts, claimableNow)
	if err != nil || !got {
		t.Fatalf("probe = %v err=%v, want true", got, err)
	}
	if after := snapshotClaimableJobs(t, store); !equalClaimableSnapshots(before, after) {
		t.Fatalf("the probe changed jobs: before=%+v after=%+v", before, after)
	}
}

// The probe answers from the read pool: it must not wait for a write connection
// that another store operation holds.
func TestHasClaimableProfilePromotionJobBypassesTheOccupiedWriteConnection(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	seedClaimableJobs(t, store, []claimableJob{{state: "pending"}})
	writeConn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer writeConn.Close()

	probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	got, err := store.HasClaimableProfilePromotionJob(probeCtx, claimableMaxAttempts, claimableNow)
	if err != nil || !got {
		t.Fatalf("probe = %v err=%v, want true without the write connection", got, err)
	}
}

// A non-positive maxAttempts and a zero now fall back to the claim's defaults, so
// the two never disagree about the limit.
func TestHasClaimableProfilePromotionJobUsesTheClaimsDefaults(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	// attempt_count 5 is the claim's default limit; a probe with maxAttempts 0
	// must treat the exhausted retry_wait (future next attempt) as claimable.
	seedClaimableJobs(t, store, []claimableJob{{state: "retry_wait", attemptCount: 5, nextAttempt: at(time.Hour)}})

	got, err := store.HasClaimableProfilePromotionJob(ctx, 0, claimableNow)
	if err != nil || !got {
		t.Fatalf("probe(maxAttempts=0) = %v err=%v, want true (default limit 5)", got, err)
	}
}

func TestHasClaimableProfilePromotionJobReturnsTheQueryError(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := store.HasClaimableProfilePromotionJob(ctx, claimableMaxAttempts, claimableNow)
	if err == nil || got {
		t.Fatalf("probe = %v err=%v, want the error and no claim", got, err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the context cancellation preserved", err)
	}
}

// A pending job whose evidence event is gone is neither taken nor closed by the
// claim, so the probe must not count it; otherwise it would be true forever and
// every idle tick would again open an execution subject.
func TestHasClaimableProfilePromotionJobIgnoresPendingJobWithoutEvidence(t *testing.T) {
	tests := []struct {
		name         string
		attemptCount int
		want         bool
	}{
		// Below the limit the claim's join finds nothing and it changes nothing.
		{"below the limit", 0, false},
		// At the limit the claim closes the job as failed with no join, so it acts.
		{"at the limit", claimableMaxAttempts, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := newProfilePromotionTestStore(t)
			ctx := context.Background()
			seedClaimableJobs(t, store, []claimableJob{{state: "pending", attemptCount: tc.attemptCount}})
			if _, err := store.db.ExecContext(ctx, `DELETE FROM l1_memory_event WHERE id IN (SELECT evidence_event_id FROM l1_profile_promotion_job)`); err != nil {
				t.Fatal(err)
			}

			got, err := store.HasClaimableProfilePromotionJob(ctx, claimableMaxAttempts, claimableNow)
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotClaimableJobs(t, store)
			batch, err := store.ClaimProfilePromotionBatch(ctx, 24, claimableMaxAttempts, time.Minute, claimableNow)
			if err != nil {
				t.Fatal(err)
			}
			claimActed := batch != nil || !equalClaimableSnapshots(before, snapshotClaimableJobs(t, store))

			if got != tc.want || claimActed != tc.want {
				t.Fatalf("probe=%v claim_acted=%v, want both %v", got, claimActed, tc.want)
			}
		})
	}
}

// Times are stored and compared as UTC text. A caller that hands over a local
// time must get the same answer as one that hands over the same instant in UTC.
func TestHasClaimableProfilePromotionJobNormalizesTheClockToUTC(t *testing.T) {
	jst := time.FixedZone("JST", 9*60*60)
	// With a local clock left as is, the text "2026-10-13 21:00:00+09:00" sorts
	// after a UTC "12:01" and the not-due retry would wrongly look due.
	for _, tc := range []struct {
		name string
		next time.Duration
		want bool
	}{
		{"due by one minute", -time.Minute, true},
		{"not due by one minute", time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newProfilePromotionTestStore(t)
			seedClaimableJobs(t, store, []claimableJob{{state: "retry_wait", attemptCount: 1, nextAttempt: at(tc.next)}})

			local := claimableNow.In(jst)
			got, err := store.HasClaimableProfilePromotionJob(context.Background(), claimableMaxAttempts, local)
			if err != nil || got != tc.want {
				t.Fatalf("probe(local now) = %v err=%v, want %v", got, err, tc.want)
			}
		})
	}
}
