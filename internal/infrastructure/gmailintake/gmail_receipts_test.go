package gmailintake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	backloginfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/backlog"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func gmailReceiptTestMessage(id, body string) gmailapp.GmailMessage {
	digest := sha256.Sum256([]byte(body))
	return gmailapp.GmailMessage{ID: id, ThreadID: "b2", Subject: "RenCrow Backlog test", From: "sender@example.com", ReceivedAt: "2026-09-13T00:00:00Z", Text: body, ContentHash: hex.EncodeToString(digest[:])}
}

func gmailReceiptTestReceipt(id, body, updatedAt string) gmailapp.GmailReceipt {
	message := gmailReceiptTestMessage(id, body)
	taskID, runID, traceID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewTraceID()
	return gmailapp.GmailReceipt{Schema: gmailapp.GmailEnvelopeSchema, Account: "user@example.com", Message: message, Status: "skipped", Reason: "test fixture skipped", ActorID: "shiro", TaskID: string(taskID), RunID: string(runID), TraceID: string(traceID), OriginTaskID: string(taskID), OriginRunID: string(runID), OriginTraceID: string(traceID), UpdatedAt: updatedAt}
}

func gmailReceiptTestDirectPrepared(id, body string) gmailapp.GmailReceipt {
	receipt := gmailReceiptTestReceipt(id, body, "2026-09-13T00:00:00Z")
	locator := "gmail://user%40example.com/messages/" + id
	receipt.Status = "prepared"
	receipt.Reason = ""
	receipt.Prepared = []backlogapp.IntakeRequest{{Kind: "idea", Title: receipt.Message.Subject, Body: body, Purpose: "メールに記載された仕様をRenCrowへ取り込むために検討する", Source: locator, Owner: "shiro", OwnerModule: domainbacklog.LifecycleOwnerModule, SourceRefs: []domainbacklog.SourceRef{{Type: "gmail", Locator: locator, ContentHash: receipt.Message.ContentHash, CapturedAt: receipt.Message.ReceivedAt, RawOrSummary: "Gmail message source"}}}}
	return receipt
}

func gmailReceiptTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGmailReceiptStorePersistsPrivateOriginalAtomically(t *testing.T) {
	root := gmailReceiptTestRoot(t)
	store := NewGmailReceiptStore(root)
	body := "  preserve indentation\n\nsecond line\n"
	receipt := gmailReceiptTestDirectPrepared("a1", body)
	if err := store.Save(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	path := store.receiptPath(receipt.Account, receipt.Message.ID)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("receipt mode=%o, want 0600", info.Mode().Perm())
	}
	got, found, err := store.Get(context.Background(), receipt.Account, receipt.Message.ID)
	if err != nil || !found || got.Message.Text != body || got.Prepared[0].Body != body {
		t.Fatalf("got=%+v found=%v err=%v", got, found, err)
	}
	if err := store.SaveCursor(context.Background(), receipt.Account, gmailapp.GmailQuery, "cursor-1"); err != nil {
		t.Fatal(err)
	}
	cursor, err := store.Cursor(context.Background(), receipt.Account, gmailapp.GmailQuery)
	if err != nil || cursor != "cursor-1" {
		t.Fatalf("cursor=%q err=%v", cursor, err)
	}
	list, err := store.List(context.Background(), receipt.Account, 100)
	if err != nil || len(list) != 1 || list[0].Message.Text != body {
		t.Fatalf("list=%+v err=%v", list, err)
	}
}

func TestGmailReceiptStoreRejectsInvalidHashBeforeCreatingFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "receipts")
	store := NewGmailReceiptStore(root)
	receipt := gmailReceiptTestReceipt("a1", "body", "2026-09-13T00:00:00Z")
	receipt.Message.ContentHash = strings.Repeat("0", 64)
	if err := store.Save(context.Background(), receipt); err == nil {
		t.Fatal("invalid content hash was accepted")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid save created storage root: %v", err)
	}
}

func TestGmailReceiptStoreRejectsEmptyCompleteReceiptOnSaveAndRead(t *testing.T) {
	root := gmailReceiptTestRoot(t)
	store := NewGmailReceiptStore(root)
	receipt := gmailReceiptTestReceipt("a1", "body", "2026-09-13T00:00:00Z")
	receipt.Status = "complete"
	receipt.Reason = ""
	if err := store.Save(context.Background(), receipt); err == nil {
		t.Fatal("empty complete receipt was accepted on save")
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	path := store.receiptPath(receipt.Account, receipt.Message.ID)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, found, err := store.Get(context.Background(), receipt.Account, receipt.Message.ID); err == nil || found {
		t.Fatalf("empty complete receipt was accepted on read: found=%v err=%v", found, err)
	}
}

func TestGmailReceiptStoreIgnoresSafeOrphanTemporaryWrite(t *testing.T) {
	root := gmailReceiptTestRoot(t)
	store := NewGmailReceiptStore(root)
	temporary := filepath.Join(root, ".gmail-receipt-crash.tmp")
	if err := os.WriteFile(temporary, []byte(`{"partial":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(context.Background(), "user@example.com", 100); err != nil {
		t.Fatalf("safe orphan temp blocked listing: %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(temporary, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.List(context.Background(), "user@example.com", 100); err == nil {
			t.Fatal("unsafe orphan temp was accepted")
		}
	}
}

func TestGmailReceiptStoreRejectsMalformedCommittedAndSymlinkEntries(t *testing.T) {
	t.Run("malformed JSON", func(t *testing.T) {
		root := gmailReceiptTestRoot(t)
		store := NewGmailReceiptStore(root)
		path := filepath.Join(root, strings.Repeat("a", 64)+".json")
		if err := os.WriteFile(path, []byte(`{"schema":`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.List(context.Background(), "user@example.com", 100); err == nil {
			t.Fatal("malformed committed receipt was silently accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink permissions are platform-specific")
		}
		root := gmailReceiptTestRoot(t)
		store := NewGmailReceiptStore(root)
		target := filepath.Join(root, "target")
		if err := os.WriteFile(target, []byte(`{"schema":`), 0600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, strings.Repeat("b", 64)+".json")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := store.List(context.Background(), "user@example.com", 100); err == nil {
			t.Fatal("symlink receipt was accepted")
		}
	})
}

func TestGmailReceiptStoreListsNewestHundredWithAccountIsolation(t *testing.T) {
	root := gmailReceiptTestRoot(t)
	store := NewGmailReceiptStore(root)
	for index := 0; index < 105; index++ {
		id := fmt.Sprintf("%x", index+1)
		updated := time.Date(2026, 9, 13, 0, 0, index, 0, time.UTC).Format(time.RFC3339Nano)
		receipt := gmailReceiptTestReceipt(id, "body", updated)
		if index%2 == 0 {
			receipt.Account = "other@example.com"
		}
		if err := store.Save(context.Background(), receipt); err != nil {
			t.Fatalf("save %d: %v", index, err)
		}
	}
	list, err := store.List(context.Background(), "user@example.com", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 52 {
		t.Fatalf("account-filtered list len=%d, want 52", len(list))
	}
	for index := 1; index < len(list); index++ {
		if list[index-1].UpdatedAt < list[index].UpdatedAt {
			t.Fatalf("list is not newest-first at %d: %s then %s", index, list[index-1].UpdatedAt, list[index].UpdatedAt)
		}
	}
	if _, err := store.List(context.Background(), "user@example.com", 101); err != nil {
		t.Fatal(err)
	}
}

func TestGmailReceiptStoreHonorsCancellationBeforeFilesystemAccess(t *testing.T) {
	store := NewGmailReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := store.Get(ctx, "user@example.com", "a1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get cancellation err=%v", err)
	}
	if _, err := os.Stat(store.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled Get touched root: %v", err)
	}
}

func TestGmailReceiptFlowAtlasJSONLRoundTripsOriginalBody(t *testing.T) {
	root := gmailReceiptTestRoot(t)
	store := backloginfra.NewJSONLStore(filepath.Join(root, "atlas.jsonl"))
	atlas := backlogapp.NewService(store, nil)
	body := "  keep indentation\n\nsecond line\n"
	_, err := atlas.Intake(context.Background(), backlogapp.IntakeRequest{
		Title: "Backlog body roundtrip", Body: body, Purpose: "purpose", Source: "gmail://user%40example.com/messages/a1",
		SourceRefs: []domainbacklog.SourceRef{{Type: "gmail", Locator: "gmail://user%40example.com/messages/a1", CapturedAt: "2026-09-13T00:00:00Z", RawOrSummary: "Gmail message source"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := store.List(context.Background(), 5000)
	if err != nil || len(items) != 1 || items[0].Body != body {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}
