package verification

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLReportStoreSaveListGetSummary(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "verification_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	oldTaskID := modulecore.NewTaskID()
	latestTaskID := modulecore.NewTaskID()
	old := testReport(t, oldTaskID, domainverification.StatusWeaklySupported, time.Now().UTC().Add(-time.Minute))
	latest := testReport(t, latestTaskID, domainverification.StatusConflict, time.Now().UTC())
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatalf("Save old failed: %v", err)
	}
	if err := store.Save(context.Background(), latest); err != nil {
		t.Fatalf("Save latest failed: %v", err)
	}

	items, err := store.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(items) != 1 || items[0].TaskID != latestTaskID {
		t.Fatalf("expected latest task, got %+v", items)
	}

	got, err := store.GetByTaskID(context.Background(), oldTaskID)
	if err != nil {
		t.Fatalf("GetByTaskID failed: %v", err)
	}
	if got.Status != domainverification.StatusWeaklySupported {
		t.Fatalf("unexpected status: %s", got.Status)
	}
	// The stored row has to come back unchanged: the digest and the successor reference are
	// kept as saved, and the body and identity metadata around them are not rewritten.
	if got.ArtifactID != old.ArtifactID || got.Kind != old.Kind {
		t.Fatalf("artifact identity changed: saved=%s/%s got=%s/%s", old.ArtifactID, old.Kind, got.ArtifactID, got.Kind)
	}
	if got.ContentHash != old.ContentHash || got.SupersededBy != old.SupersededBy {
		t.Fatalf("content_hash/superseded_by changed: saved=%s/%s got=%s/%s", old.ContentHash, old.SupersededBy, got.ContentHash, got.SupersededBy)
	}
	if got.TaskID != old.TaskID || got.SessionID != old.SessionID || got.Route != old.Route {
		t.Fatalf("owner metadata changed: saved=%s/%s/%s got=%s/%s/%s", old.TaskID, old.SessionID, old.Route, got.TaskID, got.SessionID, got.Route)
	}
	if !got.CreatedAt.Equal(old.CreatedAt) {
		t.Fatalf("created_at changed: saved=%s got=%s", old.CreatedAt, got.CreatedAt)
	}
	if items[0].ContentHash != latest.ContentHash || items[0].SupersededBy != latest.SupersededBy {
		t.Fatalf("ListRecent lost the hash or successor: got=%s/%s want=%s/%s", items[0].ContentHash, items[0].SupersededBy, latest.ContentHash, latest.SupersededBy)
	}

	summary, err := store.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary failed: %v", err)
	}
	if summary["status"][string(domainverification.StatusConflict)] != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestJSONLReportStoreRejectsInvalidReport(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "verification_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}
	if err := store.Save(context.Background(), domainverification.VerificationReport{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestJSONLReportStoreRejectsInvalidLookupAndLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verification_report.jsonl")
	store, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetByTaskID(context.Background(), modulecore.TaskID(modulecore.NewMessageID())); err == nil {
		t.Fatal("wrong canonical ID type was accepted")
	}
	legacy, err := json.Marshal(map[string]any{"id": "verify-old", "session_id": "session-1", "status": "not_checked", "created_at": "2026-09-05T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	legacy = append(legacy, '\n')
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListRecent(context.Background(), 10); err == nil {
		t.Fatal("legacy identity row was silently accepted")
	}
}

func testReport(t *testing.T, taskID modulecore.TaskID, status domainverification.VerificationStatus, createdAt time.Time) domainverification.VerificationReport {
	report := domainverification.VerificationReport{
		ArtifactID:   modulecore.NewArtifactID(),
		Kind:         modulecore.ArtifactKindReport,
		TaskID:       taskID,
		SessionID:    "session-1",
		Route:        "CHAT",
		Status:       status,
		TriggerLevel: domainverification.TriggerMedium,
		SupersededBy: modulecore.ArtifactID("art_00000000-0000-7000-8000-0000000000a1"),
		CreatedAt:    createdAt,
	}
	report.ContentHash = testReportDigest(t, report)
	return report
}

// testReportDigest stamps the body digest the way the producer does, so the stored fixture
// carries the hash of the body it actually writes.
func testReportDigest(t *testing.T, report domainverification.VerificationReport) string {
	t.Helper()
	digest, err := domainverification.ComputeVerificationReportContentHash(report)
	if err != nil {
		t.Fatalf("compute fixture content hash: %v", err)
	}
	return digest
}

// TestJSONLReportStoreContentHashAndSupersessionGuards keeps the store honest about the two
// new fields: a row without its own digest, a digest belonging to another body, and a report
// that names itself as successor never reach the file, while canonical v5 and v7 successors
// are stored and read back as they were.
func TestJSONLReportStoreContentHashAndSupersessionGuards(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "verification_report.jsonl")
	store, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}
	now := time.Date(2026, 9, 21, 17, 0, 0, 0, time.UTC)
	taskID := modulecore.NewTaskID()
	lineCount := func() int {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read store file: %v", err)
		}
		trimmed := strings.TrimSpace(string(raw))
		if trimmed == "" {
			return 0
		}
		return len(strings.Split(trimmed, "\n"))
	}

	missingHash := testReport(t, taskID, domainverification.StatusVerified, now)
	missingHash.ContentHash = ""
	if err := store.Save(ctx, missingHash); err == nil || !strings.Contains(err.Error(), "content_hash") {
		t.Fatalf("report without content_hash: err=%v, want content_hash rejection", err)
	}

	foreignDigest := testReport(t, taskID, domainverification.StatusVerified, now.Add(time.Second))
	foreignDigest.ContentHash = "sha256:cd3668ffb26f36cd58f99b1253c07f7eb594d43b7f23e58b720889729499db2d"
	if err := store.Save(ctx, foreignDigest); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("report carrying another body's digest: err=%v, want digest mismatch rejection", err)
	}

	malformedHash := testReport(t, taskID, domainverification.StatusVerified, now.Add(2*time.Second))
	malformedHash.ContentHash = "sha256:not-a-digest"
	if err := store.Save(ctx, malformedHash); err == nil || !strings.Contains(err.Error(), "content_hash") {
		t.Fatalf("report with malformed content_hash: err=%v, want content_hash rejection", err)
	}

	selfSuperseded := testReport(t, taskID, domainverification.StatusVerified, now.Add(3*time.Second))
	selfSuperseded.SupersededBy = selfSuperseded.ArtifactID
	selfSuperseded.ContentHash = testReportDigest(t, selfSuperseded)
	if err := store.Save(ctx, selfSuperseded); err == nil || !strings.Contains(err.Error(), "superseded_by") {
		t.Fatalf("report superseding itself: err=%v, want superseded_by rejection", err)
	}

	if got := lineCount(); got != 0 {
		t.Fatalf("rejected reports must not be written, got %d rows", got)
	}

	for i, successor := range []modulecore.ArtifactID{
		modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b"),
		modulecore.ArtifactID("art_00000000-0000-7000-8000-00000000000c"),
	} {
		item := testReport(t, taskID, domainverification.StatusVerified, now.Add(time.Duration(10+i)*time.Second))
		item.SupersededBy = successor
		item.ContentHash = testReportDigest(t, item)
		if err := store.Save(ctx, item); err != nil {
			t.Fatalf("save report with canonical successor %s: %v", successor, err)
		}
	}
	items, err := store.ListRecent(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected the two accepted rows, got %+v", items)
	}
	if items[0].ContentHash == "" || items[1].ContentHash == "" {
		t.Fatalf("saved rows lost their content hash: %+v", items)
	}
	if items[1].SupersededBy != modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b") ||
		items[0].SupersededBy != modulecore.ArtifactID("art_00000000-0000-7000-8000-00000000000c") {
		t.Fatalf("successor references changed: got %s and %s", items[1].SupersededBy, items[0].SupersededBy)
	}
	if got, want := items[0].ContentHash, testReportDigest(t, items[0]); got != want {
		t.Fatalf("read-back row does not match its own digest: stored=%s recomputed=%s", got, want)
	}
}

func TestJSONLReportStoreStorageHostOperationRecoversWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "verification_report.jsonl")
	store, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	report := testReport(t, modulecore.NewTaskID(), domainverification.StatusVerified, time.Now().UTC())
	identity := testVerificationOperationIdentity("verification-report-op-1", []byte("save"), 7)
	if err := store.SaveForStorageHostOperation(ctx, identity, report); err != nil {
		t.Fatalf("SaveForStorageHostOperation: %v", err)
	}

	reopened, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, found, err := reopened.LookupStorageHostOperationReceipt(ctx, identity)
	if err != nil || !found || strings.TrimSpace(string(raw)) != "null" {
		t.Fatalf("LookupStorageHostOperationReceipt found=%v raw=%s err=%v", found, raw, err)
	}
	if err := reopened.SaveForStorageHostOperation(ctx, identity, report); err != nil {
		t.Fatalf("exact replay: %v", err)
	}
	items, err := reopened.ListRecent(ctx, 10)
	if err != nil || len(items) != 1 || items[0].ArtifactID != report.ArtifactID {
		t.Fatalf("items=%+v err=%v, want one exact report", items, err)
	}
	changed := identity
	changed.PayloadSHA256 = strings.Repeat("a", 64)
	if _, found, err := reopened.LookupStorageHostOperationReceipt(ctx, changed); !errors.Is(err, ErrVerificationReportOperationConflict) || found {
		t.Fatalf("changed payload found=%v err=%v, want conflict", found, err)
	}
	changed = identity
	changed.WriterGeneration++
	if _, found, err := reopened.LookupStorageHostOperationReceipt(ctx, changed); !errors.Is(err, ErrVerificationReportOperationConflict) || found {
		t.Fatalf("changed generation found=%v err=%v, want conflict", found, err)
	}
}

func TestJSONLReportStoreStorageHostOperationRecoversLostCommitRecord(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "verification_report.jsonl")
	store, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	report := testReport(t, modulecore.NewTaskID(), domainverification.StatusVerified, time.Now().UTC())
	identity := testVerificationOperationIdentity("verification-report-lost-response", []byte("save"), 11)
	store.afterReportHook = func() error { return errors.New("simulated process loss") }
	if err := store.SaveForStorageHostOperation(ctx, identity, report); err == nil {
		t.Fatal("expected simulated loss after durable report append")
	}

	reopened, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, found, err := reopened.LookupStorageHostOperationReceipt(ctx, identity)
	if err != nil || !found || string(raw) != "null" {
		t.Fatalf("recovered receipt found=%v raw=%q err=%v", found, raw, err)
	}
	if err := reopened.SaveForStorageHostOperation(ctx, identity, report); err != nil {
		t.Fatalf("replay recovered mutation: %v", err)
	}
	items, err := reopened.ListRecent(ctx, 10)
	if err != nil || len(items) != 1 || items[0].ContentHash != report.ContentHash {
		t.Fatalf("recovered items=%+v err=%v", items, err)
	}
}

func TestJSONLReportStoreStorageHostOperationSyncsNewLogParentBeforeEffect(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "verification_report.jsonl")
	store, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	parentSyncs := 0
	store.operationParentSyncHook = func(parent string) error {
		if parent != filepath.Dir(store.operationPath()) {
			t.Fatalf("synced parent=%q want=%q", parent, filepath.Dir(store.operationPath()))
		}
		parentSyncs++
		return nil
	}
	store.afterReportHook = func() error {
		if parentSyncs != 1 {
			return fmt.Errorf("effect append preceded durable operation-log parent: syncs=%d", parentSyncs)
		}
		return errors.New("simulated process loss after effect")
	}
	report := testReport(t, modulecore.NewTaskID(), domainverification.StatusVerified, time.Now().UTC())
	identity := testVerificationOperationIdentity("verification-report-parent-sync", []byte("save"), 17)
	if err := store.SaveForStorageHostOperation(ctx, identity, report); err == nil {
		t.Fatal("expected simulated process loss")
	}
	if parentSyncs != 1 {
		t.Fatalf("new operation log parent syncs=%d want 1 before effect", parentSyncs)
	}
}

func TestJSONLReportStoreStorageHostOperationDoesNotCertifyVisibleBytesAfterSyncFailure(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "verification_report.jsonl")
	store, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.reportSyncHook = func(*os.File) error { return errors.New("injected report sync failure") }
	report := testReport(t, modulecore.NewTaskID(), domainverification.StatusVerified, time.Now().UTC())
	identity := testVerificationOperationIdentity("verification-report-effect-sync-failure", []byte("save"), 18)
	if err := store.SaveForStorageHostOperation(ctx, identity, report); err == nil {
		t.Fatal("expected report sync failure")
	}
	if rows := strings.TrimSpace(string(mustReadVerificationFile(t, path))); rows == "" {
		t.Fatal("fault seam did not leave report bytes visible")
	}
	if raw, found, err := store.LookupStorageHostOperationReceipt(ctx, identity); !errors.Is(err, ErrVerificationReportOperationUnknown) || found || raw != nil {
		t.Fatalf("visible unsynced effect was certified: raw=%q found=%v err=%v", raw, found, err)
	}
}

func TestJSONLReportStoreStorageHostOperationReestablishesFailedParentBarrierBeforeEffect(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "verification_report.jsonl")
	store, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	store.operationParentSyncHook = func(string) error { return errors.New("injected operation-log parent sync failure") }
	report := testReport(t, modulecore.NewTaskID(), domainverification.StatusVerified, time.Now().UTC())
	identity := testVerificationOperationIdentity("verification-report-parent-sync-failure", []byte("save"), 19)
	if err := store.SaveForStorageHostOperation(ctx, identity, report); err == nil {
		t.Fatal("expected initial parent sync failure")
	}
	if err := store.SaveForStorageHostOperation(ctx, identity, report); err == nil {
		t.Fatal("expected retry parent sync failure")
	}
	if rows := strings.TrimSpace(string(mustReadVerificationFile(t, path))); rows != "" {
		t.Fatalf("report effect appended before parent barrier recovery: %s", rows)
	}
}

func mustReadVerificationFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestJSONLReportStoreStorageHostOperationRejectsSubstitutedEffectAndMalformedProof(t *testing.T) {
	t.Run("substituted report", func(t *testing.T) {
		ctx := context.Background()
		path := filepath.Join(t.TempDir(), "verification_report.jsonl")
		store, err := NewJSONLReportStore(path)
		if err != nil {
			t.Fatal(err)
		}
		report := testReport(t, modulecore.NewTaskID(), domainverification.StatusVerified, time.Now().UTC())
		identity := testVerificationOperationIdentity("verification-report-substituted", []byte("save"), 12)
		if err := store.SaveForStorageHostOperation(ctx, identity, report); err != nil {
			t.Fatal(err)
		}
		substitute := testReport(t, modulecore.NewTaskID(), domainverification.StatusConflict, report.CreatedAt)
		raw, err := json.Marshal(substitute)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.LookupStorageHostOperationReceipt(ctx, identity); !errors.Is(err, ErrVerificationReportOperationUnknown) || found {
			t.Fatalf("substituted lookup found=%v err=%v", found, err)
		}
		if err := store.SaveForStorageHostOperation(ctx, identity, report); !errors.Is(err, ErrVerificationReportOperationUnknown) {
			t.Fatalf("substituted replay err=%v", err)
		}
	})

	for _, proof := range []string{"{\n", `{"version":0}` + "\n"} {
		t.Run("malformed proof "+proof[:1], func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "verification_report.jsonl")
			store, err := NewJSONLReportStore(path)
			if err != nil {
				t.Fatal(err)
			}
			identity := testVerificationOperationIdentity("verification-report-malformed", []byte("save"), 13)
			if err := os.WriteFile(store.operationPath(), []byte(proof), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, found, err := store.LookupStorageHostOperationReceipt(ctx, identity); !errors.Is(err, ErrVerificationReportOperationCorrupt) || found {
				t.Fatalf("malformed proof found=%v err=%v", found, err)
			}
		})
	}
}

func testVerificationOperationIdentity(opID string, payload []byte, generation int64) StorageHostOperationIdentity {
	hash := sha256.Sum256(payload)
	return StorageHostOperationIdentity{OpID: opID, PayloadSHA256: hex.EncodeToString(hash[:]), WriterGeneration: generation}
}
