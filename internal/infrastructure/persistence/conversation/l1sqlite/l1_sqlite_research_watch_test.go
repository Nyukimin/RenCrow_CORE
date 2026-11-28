package l1sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestL1ResearchWatchSchemaExists(t *testing.T) {
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.db.QueryContext(context.Background(), `PRAGMA table_info(l1_research_watch)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[string]bool{
		"watch_id": false, "owner_id": false, "scope": false, "payload_json": false,
		"payload_digest": false, "source_refs_json": false, "cadence_ns": false,
		"next_due_at": false, "last_attempt_at": false, "last_success_at": false,
		"cursor_json": false, "enabled": false, "lease_token": false,
		"lease_until": false, "created_at": false, "updated_at": false,
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for name, found := range want {
		if !found {
			t.Errorf("research watch column %q is missing", name)
		}
	}
}

func TestL1ResearchWatchExactClaimDoesNotLeaseAnotherWatch(t *testing.T) {
	store := newResearchWatchTestStore(t)
	ctx := context.Background()
	addResearchWatchSource(t, store, "rss:exact", "https://example.org/feed", true, "public feed")
	now := time.Now().UTC()
	first, err := store.RegisterResearchWatch(ctx, "user", "ren", `{"objective":"one"}`, []string{"rss:exact"}, time.Hour, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RegisterResearchWatch(ctx, "user", "ren", `{"objective":"two"}`, []string{"rss:exact"}, time.Hour, now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.ClaimResearchWatch(ctx, "user", "ren", second.WatchID, now)
	if err != nil || !found || claimed.WatchID != second.WatchID || claimed.LeaseToken == "" {
		t.Fatalf("%+v %v %v", claimed, found, err)
	}
	untouched, found, err := store.GetResearchWatch(ctx, "user", "ren", first.WatchID)
	if err != nil || !found || untouched.LeaseToken != "" || !untouched.LastAttemptAt.IsZero() {
		t.Fatal("explicit claim touched another due watch")
	}
	if _, found, err := store.ClaimResearchWatch(ctx, "user", "ren", second.WatchID, now); err != nil || found {
		t.Fatal("exact watch received concurrent live lease")
	}
	if _, found, err := store.ClaimResearchWatch(ctx, "user", "other", second.WatchID, now); err != nil || found {
		t.Fatal("foreign owner claimed watch")
	}
}

func TestL1ResearchWatchRefreshBindingRetryRegrantAndAtomicCheckpoint(t *testing.T) {
	store := newResearchWatchTestStore(t)
	ctx := context.Background()
	addResearchWatchSource(t, store, "web:refresh", "https://example.org/source", true, "public retention authorized")
	now := time.Now().UTC().Truncate(time.Second)
	watch, err := store.RegisterResearchWatch(ctx, "user", "ren", `{"objective":"track"}`, []string{"web:refresh"}, time.Hour, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	claimed, found, err := store.ClaimResearchWatch(ctx, "user", "ren", watch.WatchID, now)
	if err != nil || !found || claimed.LeaseToken == "" {
		t.Fatalf("claim=%+v found=%v err=%v", claimed, found, err)
	}
	binding := L1ResearchWatchRefresh{
		Scope: "user", OwnerID: "ren", WatchID: watch.WatchID, PlanID: "opaque-plan",
		LeaseToken: claimed.LeaseToken, PlanJSON: `{"plan":"frozen"}`, PlanDigest: strings.Repeat("a", 64),
		OriginalExecutionJSON: `{"task_id":"task-original","run_id":"run-original"}`,
		CurrentInvocationJSON: `{"task_id":"task-original","run_id":"run-original"}`,
		SourcePolicyRevision:  strings.Repeat("b", 64), PreviousCursor: claimed.CursorJSON,
	}
	if err := store.BindResearchWatchRefresh(ctx, binding, now); err != nil {
		t.Fatal(err)
	}
	attemptDigest, attemptRef := strings.Repeat("c", 64), strings.Repeat("d", 64)
	retryAt, err := store.ScheduleResearchWatchRefreshRetry(ctx, binding, attemptDigest, attemptRef, time.Minute, now)
	if err != nil || !retryAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("retryAt=%v err=%v", retryAt, err)
	}
	stored, found, err := store.GetResearchWatch(ctx, "user", "ren", watch.WatchID)
	if err != nil || !found || stored.CursorJSON != claimed.CursorJSON || stored.LeaseToken != "" || !stored.NextDueAt.Equal(retryAt) {
		t.Fatalf("retry changed cursor or retained lease: %+v found=%v err=%v", stored, found, err)
	}
	if _, allowed, err := store.RevalidateResearchWatchRefresh(ctx, binding, `{"task_id":"task-original","run_id":"run-next"}`, retryAt.Add(-time.Second)); err != nil || allowed {
		t.Fatalf("early retry revalidation allowed=%v err=%v", allowed, err)
	}
	continued, allowed, err := store.RevalidateResearchWatchRefresh(ctx, binding, `{"task_id":"task-original","run_id":"run-next"}`, retryAt)
	if err != nil || !allowed || continued.LeaseToken != binding.LeaseToken || continued.State != "active" || continued.CurrentInvocationJSON == binding.CurrentInvocationJSON {
		t.Fatalf("same-token retry grant=%+v allowed=%v err=%v", continued, allowed, err)
	}
	cursor := `{"item_hashes":{},"schema_version":"rencrow.research.watch_cursor.v1"}`
	receipt := `{"canonical_id":"` + watch.WatchID + `","state":"committed"}`
	binding.CurrentInvocationJSON = continued.CurrentInvocationJSON
	if err := store.CommitResearchWatchRefresh(ctx, binding, cursor, strings.Repeat("e", 64), strings.Repeat("f", 64), receipt, retryAt); err != nil {
		t.Fatal(err)
	}
	committed, found, err := store.GetResearchWatchRefresh(ctx, "user", "ren", watch.WatchID)
	if err != nil || !found || committed.State != "committed" || committed.CommittedCursor != cursor || committed.CheckpointDigest != strings.Repeat("e", 64) || committed.ExecutionDigest != strings.Repeat("f", 64) || committed.ReceiptJSON != receipt {
		t.Fatalf("checkpoint not durably reconciliable: %+v found=%v err=%v", committed, found, err)
	}
	stored, found, err = store.GetResearchWatch(ctx, "user", "ren", watch.WatchID)
	if err != nil || !found || stored.CursorJSON != cursor || stored.LeaseToken != "" || stored.LastSuccessAt.IsZero() {
		t.Fatalf("cursor not atomically committed: %+v found=%v err=%v", stored, found, err)
	}
}

func TestL1ResearchWatchRegistrationIsCanonicalAndOwnerScoped(t *testing.T) {
	store := newResearchWatchTestStore(t)
	ctx := context.Background()
	addResearchWatchSource(t, store, "rss:alpha", "https://example.org/feed", true, "public feed")
	addResearchWatchSource(t, store, "rss:beta", "https://example.net/feed", true, "public feed")
	addResearchWatchSource(t, store, "rss:disabled", "https://disabled.example/feed", false, "public feed")
	addResearchWatchSource(t, store, "rss:unlicensed", "https://unlicensed.example/feed", true, "public feed")
	if _, err := store.db.ExecContext(context.Background(), `UPDATE l1_source_registry SET license_note = '' WHERE source_id = ?`, "rss:unlicensed"); err != nil {
		t.Fatal(err)
	}
	addResearchWatchSource(t, store, "rss:private", "http://127.0.0.1/feed", true, "private source")
	due := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	first, err := store.RegisterResearchWatch(ctx, "user", "owner-a", `{"b":2,"a":1}`, []string{"rss:beta", "rss:alpha"}, time.Hour, due)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RegisterResearchWatch(ctx, "user", "owner-a", `{"a":1,"b":2}`, []string{"rss:alpha", "rss:beta"}, time.Hour, due.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if first.WatchID != second.WatchID || first.PayloadDigest != second.PayloadDigest || first.NextDueAt != second.NextDueAt {
		t.Fatalf("canonical duplicate did not return the exact existing row: first=%+v second=%+v", first, second)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_source_registry SET enabled = 0 WHERE source_id = ?`, "rss:alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterResearchWatch(ctx, "user", "owner-a", `{"a":1,"b":2}`, []string{"rss:alpha", "rss:beta"}, time.Hour, due); err == nil {
		t.Fatal("idempotent re-registration must revalidate current source enabled state")
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_source_registry SET enabled = 1 WHERE source_id = ?`, "rss:alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RegisterResearchWatch(ctx, "user", "owner-a", `{"a":1}`, []string{"rss:alpha"}, time.Hour, due); err != nil {
		t.Fatal(err)
	}
	addResearchWatchSourceInterval(t, store, "rss:slow", "https://slow.example/feed", true, "public feed", 2*time.Hour)
	if _, err := store.RegisterResearchWatch(ctx, "public", "owner-a", `{}`, []string{"rss:slow"}, time.Hour, due); err == nil {
		t.Fatal("watch cadence must not be shorter than the registered source fetch interval")
	}
	foreign, found, err := store.GetResearchWatch(ctx, "user", "owner-b", first.WatchID)
	if err != nil || found || foreign.WatchID != "" {
		t.Fatalf("foreign owner readback must be hidden: watch=%+v found=%v err=%v", foreign, found, err)
	}
	foreignScope, found, err := store.GetResearchWatch(ctx, "public", "owner-a", first.WatchID)
	if err != nil || found || foreignScope.WatchID != "" {
		t.Fatalf("foreign scope readback must be hidden: watch=%+v found=%v err=%v", foreignScope, found, err)
	}
	if _, err := store.RegisterResearchWatch(ctx, "public", "owner-a", `{}`, []string{"rss:disabled"}, time.Hour, due); err == nil {
		t.Fatal("disabled source should not be accepted")
	}
	if _, err := store.RegisterResearchWatch(ctx, "public", "owner-a", `{}`, []string{"rss:unlicensed"}, time.Hour, due); err == nil {
		t.Fatal("unlicensed source should not be accepted")
	}
	if _, err := store.RegisterResearchWatch(ctx, "public", "owner-a", `{}`, []string{"rss:private"}, time.Hour, due); err == nil {
		t.Fatal("non-public source URL should not be accepted")
	}
}

func TestL1ResearchWatchRegistrationRejectsStoredIdentityConflict(t *testing.T) {
	store := newResearchWatchTestStore(t)
	ctx := context.Background()
	addResearchWatchSource(t, store, "rss:alpha", "https://example.org/feed", true, "public feed")
	item, err := store.RegisterResearchWatch(ctx, "public", "owner", `{"topic":"one"}`, []string{"rss:alpha"}, time.Hour, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_research_watch SET payload_digest = ? WHERE watch_id = ?`, "conflicting-digest", item.WatchID); err != nil {
		t.Fatal(err)
	}
	_, err = store.RegisterResearchWatch(ctx, "public", "owner", `{"topic":"one"}`, []string{"rss:alpha"}, time.Hour, time.Now().UTC())
	if !errors.Is(err, ErrL1ResearchWatchConflict) {
		t.Fatalf("same deterministic ID with mismatched row should conflict: %v", err)
	}
}

func TestL1ResearchWatchAtomicClaimExpiryAndPersistentCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "l1.db")
	store, err := NewL1SQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	addResearchWatchSource(t, store, "rss:alpha", "https://example.org/feed", true, "public feed")
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	item, err := store.RegisterResearchWatch(ctx, "user", "owner", `{"topic":"alpha"}`, []string{"rss:alpha"}, time.Hour, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	claims := make(chan []L1ResearchWatch, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := store.ClaimDueResearchWatches(ctx, "user", "owner", now, 1)
			claims <- claimed
			errs <- err
		}()
	}
	wg.Wait()
	close(claims)
	close(errs)
	claimedCount := 0
	var lease L1ResearchWatch
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for batch := range claims {
		claimedCount += len(batch)
		if len(batch) > 0 {
			lease = batch[0]
		}
	}
	if claimedCount != 1 || lease.WatchID != item.WatchID || lease.LeaseToken == "" || !lease.LeaseUntil.Equal(now.Add(L1ResearchWatchLeaseTTL)) {
		t.Fatalf("concurrent claims must grant exactly one bounded lease: count=%d lease=%+v", claimedCount, lease)
	}
	if err := store.CommitResearchWatchCursor(ctx, "user", "owner", item.WatchID, lease.LeaseToken, `{"page":1}`, false, now.Add(time.Minute), time.Minute); err != nil {
		t.Fatal(err)
	}
	afterFailure, found, err := store.GetResearchWatch(ctx, "user", "owner", item.WatchID)
	if err != nil || !found || afterFailure.CursorJSON != "{}" || !afterFailure.LastSuccessAt.IsZero() || !afterFailure.NextDueAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("failed attempt must preserve cursor/success and schedule retry: watch=%+v found=%v err=%v", afterFailure, found, err)
	}
	retryClaim, err := store.ClaimDueResearchWatches(ctx, "user", "owner", now.Add(2*time.Minute), 1)
	if err != nil || len(retryClaim) != 1 {
		t.Fatalf("retry watch was not claimable after retry due: batch=%+v err=%v", retryClaim, err)
	}
	if err := store.CommitResearchWatchCursor(ctx, "user", "owner", item.WatchID, retryClaim[0].LeaseToken, `{"page":2}`, true, now.Add(3*time.Minute), 0); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewL1SQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	persisted, found, err := store.GetResearchWatch(ctx, "user", "owner", item.WatchID)
	if err != nil || !found || persisted.CursorJSON != `{"page":2}` || !persisted.LastSuccessAt.Equal(now.Add(3*time.Minute)) || !persisted.NextDueAt.Equal(now.Add(63*time.Minute)) {
		t.Fatalf("successful cursor did not persist across reopen: watch=%+v found=%v err=%v", persisted, found, err)
	}
	if err := store.CommitResearchWatchCursor(ctx, "user", "owner", item.WatchID, retryClaim[0].LeaseToken, `{"page":3}`, true, now.Add(4*time.Minute), 0); err == nil {
		t.Fatal("expired/consumed lease should not be reusable")
	}
	expiringClaim, err := store.ClaimDueResearchWatches(ctx, "user", "owner", now.Add(63*time.Minute), 1)
	if err != nil || len(expiringClaim) != 1 {
		t.Fatalf("next cadence due was not claimable: batch=%+v err=%v", expiringClaim, err)
	}
	if err := store.CommitResearchWatchCursor(ctx, "user", "owner", item.WatchID, expiringClaim[0].LeaseToken, `{"page":3}`, true, expiringClaim[0].LeaseUntil, 0); err == nil {
		t.Fatal("lease must be invalid at its exact expiry")
	}
	expiredReclaim, err := store.ClaimDueResearchWatches(ctx, "user", "owner", expiringClaim[0].LeaseUntil, 1)
	if err != nil || len(expiredReclaim) != 1 || expiredReclaim[0].LeaseToken == expiringClaim[0].LeaseToken {
		t.Fatalf("expired lease should be reclaimable with a new token: batch=%+v err=%v", expiredReclaim, err)
	}
}

func TestL1ResearchWatchDisableLeavesOtherWatchesClaimable(t *testing.T) {
	store := newResearchWatchTestStore(t)
	ctx := context.Background()
	addResearchWatchSource(t, store, "rss:alpha", "https://example.org/feed", true, "public feed")
	now := time.Now().UTC()
	first, err := store.RegisterResearchWatch(ctx, "public", "owner", `{"topic":"one"}`, []string{"rss:alpha"}, time.Hour, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RegisterResearchWatch(ctx, "public", "owner", `{"topic":"two"}`, []string{"rss:alpha"}, time.Hour, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if disabled, err := store.DisableResearchWatch(ctx, "public", "owner", first.WatchID); err != nil || !disabled {
		t.Fatalf("disable watch failed: disabled=%v err=%v", disabled, err)
	}
	disabledRow, found, err := store.GetResearchWatch(ctx, "public", "owner", first.WatchID)
	if err != nil || !found || disabledRow.Enabled {
		t.Fatalf("disabled row not retained: watch=%+v found=%v err=%v", disabledRow, found, err)
	}
	claimed, err := store.ClaimDueResearchWatches(ctx, "public", "owner", now, 10)
	if err != nil || len(claimed) != 1 || claimed[0].WatchID != second.WatchID {
		t.Fatalf("disabling one watch must preserve other registrations: batch=%+v err=%v", claimed, err)
	}
}

func newResearchWatchTestStore(t *testing.T) *L1SQLiteStore {
	t.Helper()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func addResearchWatchSource(t *testing.T, store *L1SQLiteStore, id, sourceURL string, enabled bool, license string) {
	addResearchWatchSourceInterval(t, store, id, sourceURL, enabled, license, time.Hour)
}

func addResearchWatchSourceInterval(t *testing.T, store *L1SQLiteStore, id, sourceURL string, enabled bool, license string, interval time.Duration) {
	t.Helper()
	_, err := store.SaveSourceRegistryEntry(context.Background(), L1SourceRegistryEntry{
		SourceID: id, URL: sourceURL, Kind: L1SourceKindRSS, TrustScore: 0.8,
		FetchInterval: interval, LicenseNote: license, Enabled: enabled,
	})
	if err != nil {
		t.Fatal(err)
	}
}
