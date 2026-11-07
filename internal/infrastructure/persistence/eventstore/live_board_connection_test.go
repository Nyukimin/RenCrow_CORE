package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func boardConnectionPinFixture() BoardConnectionPin {
	return BoardConnectionPin{
		OwnerID:           "owner-alpha",
		JournalInstanceID: "journal_0123456789abcdef0123456789abcdef",
		PrincipalID:       "principal-alpha",
		CutoverAt:         time.Date(2026, 11, 5, 3, 4, 5, 0, time.UTC),
	}
}

func TestBoardConnectionPinIsImmutableTypedAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "event-store.sqlite")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})

	if pin, found, err := store.ReadBoardConnectionPin(ctx); err != nil || found || pin != (BoardConnectionPin{}) {
		t.Fatalf("ReadBoardConnectionPin() = %#v, %t, %v; want absent", pin, found, err)
	}
	want := boardConnectionPinFixture()
	if got, err := store.PinBoardConnection(ctx, want); err != nil || got != want {
		t.Fatalf("PinBoardConnection(first) = %#v, %v; want exact pin", got, err)
	}
	if got, err := store.PinBoardConnection(ctx, want); err != nil || got != want {
		t.Fatalf("PinBoardConnection(exact retry) = %#v, %v; want idempotent exact pin", got, err)
	}

	variants := []struct {
		name string
		edit func(*BoardConnectionPin)
	}{
		{name: "owner", edit: func(pin *BoardConnectionPin) { pin.OwnerID = "owner-other" }},
		{name: "journal instance", edit: func(pin *BoardConnectionPin) { pin.JournalInstanceID = "journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }},
		{name: "principal", edit: func(pin *BoardConnectionPin) { pin.PrincipalID = "principal-other" }},
		{name: "cutover", edit: func(pin *BoardConnectionPin) { pin.CutoverAt = pin.CutoverAt.Add(time.Second) }},
	}
	for _, variant := range variants {
		t.Run(variant.name, func(t *testing.T) {
			changed := want
			variant.edit(&changed)
			if _, err := store.PinBoardConnection(ctx, changed); err == nil {
				t.Fatal("PinBoardConnection accepted a different tuple")
			} else if strings.Contains(err.Error(), want.OwnerID) || strings.Contains(err.Error(), want.JournalInstanceID) || strings.Contains(err.Error(), want.PrincipalID) {
				t.Fatal("pin conflict exposed the current tuple")
			}
			got, found, err := store.ReadBoardConnectionPin(ctx)
			if err != nil || !found || got != want {
				t.Fatalf("ReadBoardConnectionPin() after conflict = %#v, %t, %v; want original pin", got, found, err)
			}
		})
	}

	var operationCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ? AND operation_kind = ?`, boardConnectionPinOperationID, boardConnectionPinOperationKind).Scan(&operationCount); err != nil {
		t.Fatalf("count typed board connection receipts: %v", err)
	}
	if operationCount != 1 {
		t.Fatalf("typed operation receipt count = %d, want one fixed namespace receipt", operationCount)
	}
	var resultJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT result_json FROM live_operation_receipt WHERE operation_id = ?`, boardConnectionPinOperationID).Scan(&resultJSON); err != nil {
		t.Fatalf("read typed board connection receipt: %v", err)
	}
	if strings.Contains(strings.ToLower(resultJSON), "token") {
		t.Fatal("board connection pin receipt contains auth material")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(before reopen): %v", err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteStore(reopen): %v", err)
	}
	defer reopened.Close()
	got, found, err := reopened.ReadBoardConnectionPin(ctx)
	if err != nil || !found || got != want {
		t.Fatalf("ReadBoardConnectionPin(after reopen) = %#v, %t, %v; want persisted pin", got, found, err)
	}
}

func TestBoardConnectionPinConcurrentCompareAndSetHasOneWinner(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	first := boardConnectionPinFixture()
	second := first
	second.PrincipalID = "principal-beta"

	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for _, candidate := range []BoardConnectionPin{first, second} {
		candidate := candidate
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, err := store.PinBoardConnection(ctx, candidate)
			results <- err
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	winners := 0
	losers := 0
	for err := range results {
		if err == nil {
			winners++
		} else {
			losers++
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("concurrent pin results: winners=%d losers=%d; want one each", winners, losers)
	}
	got, found, err := store.ReadBoardConnectionPin(ctx)
	if err != nil || !found || got != first && got != second {
		t.Fatalf("ReadBoardConnectionPin() after concurrent CAS = %#v, %t, %v; want one contender", got, found, err)
	}
}

func TestBoardConnectionPinMalformedReceiptIsAnError(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	_, err := store.db.ExecContext(ctx, `INSERT INTO live_operation_receipt (operation_id, operation_kind, input_sha256, result_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		boardConnectionPinOperationID, boardConnectionPinOperationKind, strings.Repeat("0", 64), "{malformed", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatalf("insert malformed synthetic receipt: %v", err)
	}
	if _, _, err := store.ReadBoardConnectionPin(ctx); !errors.Is(err, ErrBoardConnectionPinCorrupt) || errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("ReadBoardConnectionPin(malformed receipt) error = %v; want corruption error, not absence", err)
	}
}

func TestBoardConnectionPinForPrincipalIsIsolatedFromLegacySingleton(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	scoped := boardConnectionPinFixture()
	scoped.JournalInstanceID = "journal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	scoped.CutoverAt = scoped.CutoverAt.Add(time.Minute)
	if got, err := store.PinBoardConnectionForPrincipal(ctx, scoped); err != nil || got != scoped {
		t.Fatalf("PinBoardConnectionForPrincipal(scoped) = %#v, %v; want scoped pin", got, err)
	}
	if got, found, err := store.ReadBoardConnectionPin(ctx); err != nil || found || got != (BoardConnectionPin{}) {
		t.Fatalf("ReadBoardConnectionPin() after scoped-only pin = %#v, %t, %v; want absent", got, found, err)
	}

	legacy := boardConnectionPinFixture()
	if got, err := store.PinBoardConnection(ctx, legacy); err != nil || got != legacy {
		t.Fatalf("PinBoardConnection(legacy) = %#v, %v; want legacy pin", got, err)
	}
	if got, found, err := store.ReadBoardConnectionPinForPrincipal(ctx, scoped.PrincipalID); err != nil || !found || got != scoped {
		t.Fatalf("ReadBoardConnectionPinForPrincipal(scoped) = %#v, %t, %v; want scoped pin", got, found, err)
	}
	if got, found, err := store.ReadBoardConnectionPinForPrincipal(ctx, "principal-beta"); err != nil || found || got != (BoardConnectionPin{}) {
		t.Fatalf("ReadBoardConnectionPinForPrincipal(unpinned principal) = %#v, %t, %v; want absent despite legacy pin", got, found, err)
	}
	if got, found, err := store.ReadBoardConnectionPin(ctx); err != nil || !found || got != legacy {
		t.Fatalf("ReadBoardConnectionPin() after both pins = %#v, %t, %v; want unchanged legacy pin", got, found, err)
	}
}

func TestBoardConnectionPinsForPrincipalsAreIndependentAndImmutable(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	alpha := boardConnectionPinFixture()
	beta := alpha
	beta.PrincipalID = "principal-beta"
	beta.JournalInstanceID = "journal_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	beta.CutoverAt = beta.CutoverAt.Add(time.Hour)
	for _, pin := range []BoardConnectionPin{alpha, beta} {
		if got, err := store.PinBoardConnectionForPrincipal(ctx, pin); err != nil || got != pin {
			t.Fatalf("PinBoardConnectionForPrincipal(%s) = %#v, %v; want exact pin", pin.PrincipalID, got, err)
		}
		if got, err := store.PinBoardConnectionForPrincipal(ctx, pin); err != nil || got != pin {
			t.Fatalf("PinBoardConnectionForPrincipal(%s retry) = %#v, %v; want idempotent exact pin", pin.PrincipalID, got, err)
		}
	}

	for _, pin := range []BoardConnectionPin{alpha, beta} {
		got, found, err := store.ReadBoardConnectionPinForPrincipal(ctx, pin.PrincipalID)
		if err != nil || !found || got != pin {
			t.Fatalf("ReadBoardConnectionPinForPrincipal(%s) = %#v, %t, %v; want exact pin", pin.PrincipalID, got, found, err)
		}
	}

	conflicts := []struct {
		name string
		edit func(*BoardConnectionPin)
	}{
		{name: "journal instance", edit: func(pin *BoardConnectionPin) { pin.JournalInstanceID = "journal_cccccccccccccccccccccccccccccccc" }},
		{name: "cutover", edit: func(pin *BoardConnectionPin) { pin.CutoverAt = pin.CutoverAt.Add(time.Second) }},
		{name: "owner", edit: func(pin *BoardConnectionPin) { pin.OwnerID = "owner-other" }},
	}
	for _, conflict := range conflicts {
		t.Run(conflict.name, func(t *testing.T) {
			changed := alpha
			conflict.edit(&changed)
			if _, err := store.PinBoardConnectionForPrincipal(ctx, changed); !errors.Is(err, ErrOperationConflict) {
				t.Fatalf("PinBoardConnectionForPrincipal(conflict) error = %v; want ErrOperationConflict", err)
			}
		})
	}
	for _, pin := range []BoardConnectionPin{alpha, beta} {
		got, found, err := store.ReadBoardConnectionPinForPrincipal(ctx, pin.PrincipalID)
		if err != nil || !found || got != pin {
			t.Fatalf("ReadBoardConnectionPinForPrincipal(%s) after conflict = %#v, %t, %v; want unchanged pin", pin.PrincipalID, got, found, err)
		}
		var receiptCount int
		operationID := boardConnectionPinOperationIDForPrincipal(pin.PrincipalID)
		if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_id = ? AND operation_kind = ?`, operationID, boardConnectionPrincipalPinOperationKind).Scan(&receiptCount); err != nil {
			t.Fatalf("count scoped receipt for %s: %v", pin.PrincipalID, err)
		}
		if receiptCount != 1 {
			t.Fatalf("scoped receipt count for %s = %d, want one distinct typed receipt", pin.PrincipalID, receiptCount)
		}
	}
}

func TestBoardConnectionPinForPrincipalRejectsMalformedPrincipal(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	for _, principalID := range []string{"", "Principal-alpha", "principal alpha", "principal:alpha", "prïnc"} {
		if _, _, err := store.ReadBoardConnectionPinForPrincipal(ctx, principalID); err == nil {
			t.Errorf("ReadBoardConnectionPinForPrincipal(%q) accepted malformed principal", principalID)
		}
	}
	malformed := boardConnectionPinFixture()
	malformed.PrincipalID = "Principal-alpha"
	if _, err := store.PinBoardConnectionForPrincipal(ctx, malformed); err == nil {
		t.Fatal("PinBoardConnectionForPrincipal accepted malformed principal")
	}
	var receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM live_operation_receipt WHERE operation_kind = ?`, boardConnectionPrincipalPinOperationKind).Scan(&receiptCount); err != nil {
		t.Fatalf("count scoped receipts after malformed principals: %v", err)
	}
	if receiptCount != 0 {
		t.Fatalf("scoped receipts after malformed principals = %d, want none", receiptCount)
	}
}
