package eventstore

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	boardConnectionPinOperationID                = "board.connection:pin:v1"
	boardConnectionPinOperationKind              = "board_connection_pin_v1"
	boardConnectionPrincipalPinOperationIDPrefix = "board.connection:pin:principal:v1:"
	boardConnectionPrincipalPinOperationKind     = "board_connection_principal_pin_v1"
)

var ErrBoardConnectionPinCorrupt = errors.New("board connection pin receipt is corrupt")

// BoardConnectionPin is the immutable CORE-side identity and cutover for one
// authenticated BOARD connection. It intentionally contains no auth material.
type BoardConnectionPin struct {
	OwnerID           string    `json:"owner_id"`
	JournalInstanceID string    `json:"journal_instance_id"`
	PrincipalID       string    `json:"principal_id"`
	CutoverAt         time.Time `json:"cutover_at"`
}

// ReadBoardConnectionPin reads the fixed typed operation receipt.
func (s *SQLiteStore) ReadBoardConnectionPin(ctx context.Context) (BoardConnectionPin, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardConnectionPin{}, false, err
	}
	if ctx == nil {
		return BoardConnectionPin{}, false, errors.New("board connection pin context is required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BoardConnectionPin{}, false, err
	}
	defer tx.Rollback()

	pin, found, err := readBoardConnectionPinTx(ctx, tx)
	if err != nil {
		return BoardConnectionPin{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BoardConnectionPin{}, false, err
	}
	return pin, found, nil
}

// ReadBoardConnectionPinForPrincipal reads only the scoped receipt for
// principalID. It intentionally does not fall back to the legacy singleton.
func (s *SQLiteStore) ReadBoardConnectionPinForPrincipal(ctx context.Context, principalID string) (BoardConnectionPin, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardConnectionPin{}, false, err
	}
	if ctx == nil {
		return BoardConnectionPin{}, false, errors.New("board connection pin context is required")
	}
	if !validBoardConnectionPrincipal(principalID) {
		return BoardConnectionPin{}, false, errors.New("board connection pin principal is invalid")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return BoardConnectionPin{}, false, err
	}
	defer tx.Rollback()

	pin, found, err := readBoardConnectionPinForPrincipalTx(ctx, tx, principalID)
	if err != nil {
		return BoardConnectionPin{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return BoardConnectionPin{}, false, err
	}
	return pin, found, nil
}

// PinBoardConnection compare-and-sets the fixed typed operation receipt.
func (s *SQLiteStore) PinBoardConnection(ctx context.Context, pin BoardConnectionPin) (BoardConnectionPin, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardConnectionPin{}, err
	}
	if ctx == nil {
		return BoardConnectionPin{}, errors.New("board connection pin context is required")
	}
	if err := validateBoardConnectionPin(pin); err != nil {
		return BoardConnectionPin{}, err
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(pin)
		if err != nil {
			return BoardConnectionPin{}, fmt.Errorf("size BOARD connection pin write: %w", err)
		}
		var pinned BoardConnectionPin
		err = s.enqueueOrderedWrite(ctx, 0, sizeBytes, func(commandCtx context.Context) error {
			var pinErr error
			pinned, pinErr = s.pinBoardConnectionInline(commandCtx, pin)
			return pinErr
		})
		if err != nil {
			return BoardConnectionPin{}, err
		}
		return pinned, nil
	}
	return s.pinBoardConnectionInline(ctx, pin)
}

// PinBoardConnectionForPrincipal compare-and-sets the typed receipt scoped to
// pin.PrincipalID. Pins for other principals use independent receipts.
func (s *SQLiteStore) PinBoardConnectionForPrincipal(ctx context.Context, pin BoardConnectionPin) (BoardConnectionPin, error) {
	if err := s.ensureOpen(); err != nil {
		return BoardConnectionPin{}, err
	}
	if ctx == nil {
		return BoardConnectionPin{}, errors.New("board connection pin context is required")
	}
	if err := validateBoardConnectionPin(pin); err != nil {
		return BoardConnectionPin{}, err
	}
	if s.shouldQueueWrite(ctx) {
		sizeBytes, err := queuedWriteSize(pin)
		if err != nil {
			return BoardConnectionPin{}, fmt.Errorf("size scoped BOARD connection pin write: %w", err)
		}
		var pinned BoardConnectionPin
		err = s.enqueueOrderedWrite(ctx, 0, sizeBytes, func(commandCtx context.Context) error {
			var pinErr error
			pinned, pinErr = s.pinBoardConnectionForPrincipalInline(commandCtx, pin)
			return pinErr
		})
		if err != nil {
			return BoardConnectionPin{}, err
		}
		return pinned, nil
	}
	return s.pinBoardConnectionForPrincipalInline(ctx, pin)
}

func (s *SQLiteStore) pinBoardConnectionInline(ctx context.Context, pin BoardConnectionPin) (BoardConnectionPin, error) {
	inputHash, err := hashJSON(pin)
	if err != nil {
		return BoardConnectionPin{}, fmt.Errorf("hash BOARD connection pin: %w", err)
	}
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	prior, found, err := readOperationReceipt(ctx, tx, boardConnectionPinOperationID)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	if found {
		stored, err := decodeBoardConnectionPinReceipt(prior)
		if err != nil {
			return BoardConnectionPin{}, err
		}
		if !sameBoardConnectionPin(stored, pin) {
			return BoardConnectionPin{}, ErrOperationConflict
		}
		if err := tx.Commit(); err != nil {
			return BoardConnectionPin{}, err
		}
		committed = true
		return stored, nil
	}
	if err := insertOperationReceipt(ctx, tx, boardConnectionPinOperationID, boardConnectionPinOperationKind, inputHash, pin); err != nil {
		return BoardConnectionPin{}, fmt.Errorf("insert BOARD connection pin receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BoardConnectionPin{}, err
	}
	committed = true
	return pin, nil
}

func (s *SQLiteStore) pinBoardConnectionForPrincipalInline(ctx context.Context, pin BoardConnectionPin) (BoardConnectionPin, error) {
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	pinned, err := pinBoardConnectionForPrincipalTx(ctx, tx, pin)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	if err := tx.Commit(); err != nil {
		return BoardConnectionPin{}, err
	}
	committed = true
	return pinned, nil
}

func pinBoardConnectionForPrincipalTx(ctx context.Context, tx *sql.Tx, pin BoardConnectionPin) (BoardConnectionPin, error) {
	if err := validateBoardConnectionPin(pin); err != nil {
		return BoardConnectionPin{}, err
	}
	operationID := boardConnectionPinOperationIDForPrincipal(pin.PrincipalID)
	inputHash, err := hashJSON(pin)
	if err != nil {
		return BoardConnectionPin{}, fmt.Errorf("hash scoped BOARD connection pin: %w", err)
	}

	prior, found, err := readOperationReceipt(ctx, tx, operationID)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	if found {
		stored, err := decodeBoardConnectionPrincipalPinReceipt(prior, pin.PrincipalID)
		if err != nil {
			return BoardConnectionPin{}, err
		}
		if !sameBoardConnectionPin(stored, pin) {
			return BoardConnectionPin{}, ErrOperationConflict
		}
		return stored, nil
	}
	if err := insertOperationReceipt(ctx, tx, operationID, boardConnectionPrincipalPinOperationKind, inputHash, pin); err != nil {
		return BoardConnectionPin{}, fmt.Errorf("insert scoped BOARD connection pin receipt: %w", err)
	}
	return pin, nil
}

func readBoardConnectionPinTx(ctx context.Context, tx *sql.Tx) (BoardConnectionPin, bool, error) {
	prior, found, err := readOperationReceipt(ctx, tx, boardConnectionPinOperationID)
	if err != nil || !found {
		return BoardConnectionPin{}, found, err
	}
	pin, err := decodeBoardConnectionPinReceipt(prior)
	if err != nil {
		return BoardConnectionPin{}, false, err
	}
	return pin, true, nil
}

func readBoardConnectionPinForPrincipalTx(ctx context.Context, tx *sql.Tx, principalID string) (BoardConnectionPin, bool, error) {
	if !validBoardConnectionPrincipal(principalID) {
		return BoardConnectionPin{}, false, errors.New("board connection pin principal is invalid")
	}
	prior, found, err := readOperationReceipt(ctx, tx, boardConnectionPinOperationIDForPrincipal(principalID))
	if err != nil || !found {
		return BoardConnectionPin{}, found, err
	}
	pin, err := decodeBoardConnectionPrincipalPinReceipt(prior, principalID)
	if err != nil {
		return BoardConnectionPin{}, false, err
	}
	return pin, true, nil
}

func decodeBoardConnectionPinReceipt(receipt operationReceipt) (BoardConnectionPin, error) {
	return decodeBoardConnectionPinReceiptOfKind(receipt, boardConnectionPinOperationKind)
}

func decodeBoardConnectionPrincipalPinReceipt(receipt operationReceipt, principalID string) (BoardConnectionPin, error) {
	pin, err := decodeBoardConnectionPinReceiptOfKind(receipt, boardConnectionPrincipalPinOperationKind)
	if err != nil {
		return BoardConnectionPin{}, err
	}
	if !validBoardConnectionPrincipal(principalID) || pin.PrincipalID != principalID {
		return BoardConnectionPin{}, ErrBoardConnectionPinCorrupt
	}
	return pin, nil
}

func decodeBoardConnectionPinReceiptOfKind(receipt operationReceipt, operationKind string) (BoardConnectionPin, error) {
	if receipt.operationKind != operationKind {
		return BoardConnectionPin{}, ErrBoardConnectionPinCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(receipt.resultJSON)))
	decoder.DisallowUnknownFields()
	var pin BoardConnectionPin
	if err := decoder.Decode(&pin); err != nil {
		return BoardConnectionPin{}, ErrBoardConnectionPinCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return BoardConnectionPin{}, ErrBoardConnectionPinCorrupt
	}
	if err := validateBoardConnectionPin(pin); err != nil {
		return BoardConnectionPin{}, ErrBoardConnectionPinCorrupt
	}
	encoded, err := json.Marshal(pin)
	if err != nil || !bytes.Equal(encoded, []byte(receipt.resultJSON)) {
		return BoardConnectionPin{}, ErrBoardConnectionPinCorrupt
	}
	inputHash, err := hashJSON(pin)
	if err != nil || receipt.inputSHA256 != inputHash {
		return BoardConnectionPin{}, ErrBoardConnectionPinCorrupt
	}
	return pin, nil
}

func boardConnectionPinOperationIDForPrincipal(principalID string) string {
	return boardConnectionPrincipalPinOperationIDPrefix + principalID
}

func sameBoardConnectionPin(left, right BoardConnectionPin) bool {
	return left.OwnerID == right.OwnerID && left.JournalInstanceID == right.JournalInstanceID &&
		left.PrincipalID == right.PrincipalID && left.CutoverAt.Equal(right.CutoverAt)
}

func validateBoardConnectionPin(pin BoardConnectionPin) error {
	if pin.OwnerID == "" || strings.TrimSpace(pin.OwnerID) != pin.OwnerID {
		return errors.New("board connection pin owner is invalid")
	}
	if !validBoardConnectionPrincipal(pin.PrincipalID) || !validBoardConnectionJournal(pin.JournalInstanceID) {
		return errors.New("board connection pin identity is invalid")
	}
	if pin.CutoverAt.IsZero() || pin.CutoverAt.Location() != time.UTC {
		return errors.New("board connection pin cutover must be a fixed UTC time")
	}
	return nil
}

func validBoardConnectionPrincipal(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for index, char := range value {
		valid := char >= 'a' && char <= 'z' || char >= '0' && char <= '9' ||
			(index > 0 && (char == '_' || char == '-'))
		if !valid {
			return false
		}
	}
	return true
}

func validBoardConnectionJournal(value string) bool {
	const prefix = "journal_"
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, char := range value[len(prefix):] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
