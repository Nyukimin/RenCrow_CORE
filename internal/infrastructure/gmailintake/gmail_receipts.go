package gmailintake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	gmailapp "github.com/Nyukimin/RenCrow_CORE/internal/application/gmailintake"
)

const (
	gmailReceiptMaxBytes  = 2 << 20
	gmailReceiptListLimit = 100
	gmailReceiptMaxToken  = 4096
)

type GmailReceiptStore struct {
	root string
	mu   sync.Mutex
}

type gmailCursorRecord struct {
	Schema  string `json:"schema"`
	Account string `json:"account"`
	Query   string `json:"query"`
	Cursor  string `json:"cursor"`
}

func NewGmailReceiptStore(root string) *GmailReceiptStore {
	root = strings.TrimSpace(root)
	if root != "" {
		root = filepath.Clean(root)
	}
	return &GmailReceiptStore{root: root}
}

func (s *GmailReceiptStore) Get(ctx context.Context, account, messageID string) (gmailapp.GmailReceipt, bool, error) {
	if err := contextError(ctx); err != nil {
		return gmailapp.GmailReceipt{}, false, err
	}
	if s == nil {
		return gmailapp.GmailReceipt{}, false, errors.New("Gmail receipt store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return gmailapp.GmailReceipt{}, false, err
	}
	account = strings.TrimSpace(account)
	messageID = strings.TrimSpace(messageID)
	if account == "" || messageID == "" {
		return gmailapp.GmailReceipt{}, false, errors.New("Gmail receipt identity is required")
	}
	path := s.receiptPath(account, messageID)
	receipt, found, err := s.readReceipt(path)
	if err != nil || !found {
		return receipt, found, err
	}
	if !strings.EqualFold(receipt.Account, account) || receipt.Message.ID != messageID {
		return gmailapp.GmailReceipt{}, false, errors.New("Gmail receipt identity does not match its key")
	}
	return receipt, true, nil
}

func (s *GmailReceiptStore) Save(ctx context.Context, receipt gmailapp.GmailReceipt) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return errors.New("Gmail receipt store is unavailable")
	}
	if err := gmailapp.ValidateGmailReceipt(receipt); err != nil {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return fmt.Errorf("marshal Gmail receipt: %w", err)
	}
	if len(data) > gmailReceiptMaxBytes {
		return errors.New("Gmail receipt exceeds the storage bound")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return err
	}
	path := s.receiptPath(receipt.Account, receipt.Message.ID)
	if err := s.validateExistingTarget(path); err != nil {
		return err
	}
	return s.atomicWrite(path, data)
}

func (s *GmailReceiptStore) List(ctx context.Context, account string, limit int) ([]gmailapp.GmailReceipt, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("Gmail receipt store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	account = strings.TrimSpace(account)
	if account == "" {
		return nil, errors.New("Gmail receipt account is required")
	}
	if limit <= 0 || limit > gmailReceiptListLimit {
		limit = gmailReceiptListLimit
	}
	return s.listReceiptsLocked(account, false, limit, nil)
}

// ListDiaryReceipts returns every diary receipt for one exact account and
// subject date. Unlike List, it has no newest-100 cap because a day's BOARD
// payload is composed from the complete set of source receipts.
func (s *GmailReceiptStore) ListDiaryReceipts(ctx context.Context, account, date string) ([]gmailapp.GmailReceipt, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("Gmail receipt store is unavailable")
	}
	if strings.TrimSpace(account) != account || account == "" || !validDiaryReceiptDate(date) {
		return nil, errors.New("Gmail diary receipt account or date is invalid")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	return s.listReceiptsLocked(account, true, 0, func(receipt gmailapp.GmailReceipt) bool {
		return receipt.PreparedDiary != nil && receipt.PreparedDiary.Diary.Date == date
	})
}

// ListPendingDiaryPublicationIntents returns every unacknowledged intent for
// the exact account so intake can reconcile uncertain BOARD writes before
// processing another source.
func (s *GmailReceiptStore) ListPendingDiaryPublicationIntents(ctx context.Context, account string) ([]gmailapp.GmailReceipt, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, errors.New("Gmail receipt store is unavailable")
	}
	if strings.TrimSpace(account) != account || account == "" {
		return nil, errors.New("Gmail receipt account is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return nil, err
	}
	return s.listReceiptsLocked(account, true, 0, func(receipt gmailapp.GmailReceipt) bool {
		return receipt.PreparedDiary != nil && receipt.PreparedDiary.PublicationIntent != nil && !receipt.DiarySaved
	})
}

func (s *GmailReceiptStore) listReceiptsLocked(account string, exactAccount bool, limit int, include func(gmailapp.GmailReceipt) bool) ([]gmailapp.GmailReceipt, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return nil, fmt.Errorf("list Gmail receipt directory: %w", err)
	}
	type record struct {
		receipt gmailapp.GmailReceipt
		name    string
	}
	capacity := len(entries)
	if limit > 0 && capacity > limit {
		capacity = limit
	} else if limit <= 0 {
		capacity = 0
	}
	records := make([]record, 0, capacity)
	for _, entry := range entries {
		name := entry.Name()
		path := filepath.Join(s.root, name)
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("inspect Gmail receipt file: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, errors.New("Gmail receipt directory contains an unsafe entry")
		}
		if isTemporaryFilename(name) {
			if err := validateGmailPermissions(path, info, 0600); err != nil {
				return nil, err
			}
			if info.Size() > gmailReceiptMaxBytes {
				return nil, errors.New("Gmail receipt temporary file exceeds the storage bound")
			}
			continue
		}
		if isCursorFilename(name) {
			record, found, err := s.readCursor(path)
			if err != nil {
				return nil, err
			}
			if found && s.cursorPath(record.Account, record.Query) != path {
				return nil, errors.New("Gmail cursor identity does not match its key")
			}
			continue
		}
		if !isReceiptFilename(name) {
			return nil, errors.New("Gmail receipt directory contains an unknown entry")
		}
		receipt, found, err := s.readReceipt(path)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if s.receiptPath(receipt.Account, receipt.Message.ID) != path {
			return nil, errors.New("Gmail receipt identity does not match its key")
		}
		accountMatches := strings.EqualFold(receipt.Account, account)
		if exactAccount {
			accountMatches = receipt.Account == account
		}
		if accountMatches {
			if include == nil || include(receipt) {
				records = append(records, record{receipt: receipt, name: name})
				sort.SliceStable(records, func(i, j int) bool {
					left, _ := time.Parse(time.RFC3339Nano, records[i].receipt.UpdatedAt)
					right, _ := time.Parse(time.RFC3339Nano, records[j].receipt.UpdatedAt)
					if !left.Equal(right) {
						return left.After(right)
					}
					return records[i].name < records[j].name
				})
				if limit > 0 && len(records) > limit {
					records = records[:limit]
				}
			}
		}
	}
	result := make([]gmailapp.GmailReceipt, 0, len(records))
	for _, item := range records {
		result = append(result, item.receipt)
	}
	return result, nil
}

func validDiaryReceiptDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func (s *GmailReceiptStore) Cursor(ctx context.Context, account, query string) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	if s == nil {
		return "", errors.New("Gmail receipt store is unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return "", err
	}
	account = strings.TrimSpace(account)
	query = strings.TrimSpace(query)
	if account == "" || query == "" {
		return "", errors.New("Gmail cursor identity is required")
	}
	path := s.cursorPath(account, query)
	record, found, err := s.readCursor(path)
	if err != nil || !found {
		return "", err
	}
	if !strings.EqualFold(record.Account, account) || record.Query != query {
		return "", errors.New("Gmail cursor identity does not match its key")
	}
	return record.Cursor, nil
}

func (s *GmailReceiptStore) SaveCursor(ctx context.Context, account, query, cursor string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil {
		return errors.New("Gmail receipt store is unavailable")
	}
	if strings.TrimSpace(account) != account || strings.TrimSpace(query) != query {
		return errors.New("Gmail cursor identity is not canonical")
	}
	if account == "" || query == "" || !utf8.ValidString(account) || !utf8.ValidString(query) || strings.ContainsAny(account+query, "\x00\r\n") || len([]byte(cursor)) > gmailReceiptMaxToken || !utf8.ValidString(cursor) || strings.ContainsAny(cursor, "\x00\r\n") {
		return errors.New("Gmail cursor is invalid")
	}
	record := gmailCursorRecord{Schema: gmailapp.GmailEnvelopeSchema, Account: account, Query: query, Cursor: cursor}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal Gmail cursor: %w", err)
	}
	if len(data) > gmailReceiptMaxBytes {
		return errors.New("Gmail cursor exceeds the storage bound")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureRoot(); err != nil {
		return err
	}
	path := s.cursorPath(account, query)
	if err := s.validateExistingTarget(path); err != nil {
		return err
	}
	return s.atomicWrite(path, data)
}

func (s *GmailReceiptStore) receiptPath(account, messageID string) string {
	return filepath.Join(s.root, hashKey(strings.ToLower(strings.TrimSpace(account))+"\x00"+strings.TrimSpace(messageID))+".json")
}

func (s *GmailReceiptStore) cursorPath(account, query string) string {
	return filepath.Join(s.root, "cursor-"+hashKey(strings.ToLower(strings.TrimSpace(account))+"\x00"+strings.TrimSpace(query))+".json")
}

func hashKey(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *GmailReceiptStore) ensureRoot() error {
	if s == nil || strings.TrimSpace(s.root) == "" || strings.ContainsRune(s.root, '\x00') {
		return errors.New("Gmail receipt root is required")
	}
	info, err := os.Lstat(s.root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(s.root, 0700); err != nil {
			return fmt.Errorf("create Gmail receipt root: %w", err)
		}
		if err := secureGmailDirectory(s.root); err != nil {
			return fmt.Errorf("secure Gmail receipt root: %w", err)
		}
		info, err = os.Lstat(s.root)
	}
	if err != nil {
		return fmt.Errorf("inspect Gmail receipt root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("Gmail receipt root is not a directory")
	}
	return validateGmailPermissions(s.root, info, 0700)
}

func (s *GmailReceiptStore) validateExistingTarget(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return errors.New("Gmail receipt target is unsafe")
	}
	return validateGmailPermissions(path, info, 0600)
}

func (s *GmailReceiptStore) atomicWrite(path string, data []byte) error {
	temporary, err := os.CreateTemp(s.root, ".gmail-receipt-*.tmp")
	if err != nil {
		return fmt.Errorf("create Gmail receipt temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := secureGmailFile(temporary); err != nil {
		return fmt.Errorf("secure Gmail receipt temporary file: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		return fmt.Errorf("write Gmail receipt temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync Gmail receipt temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Gmail receipt temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit Gmail receipt file: %w", err)
	}
	committed = true
	if runtime.GOOS != "windows" {
		if err := syncDirectory(s.root); err != nil {
			return fmt.Errorf("sync Gmail receipt directory: %w", err)
		}
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *GmailReceiptStore) readReceipt(path string) (gmailapp.GmailReceipt, bool, error) {
	data, found, err := readSecureFile(path)
	if err != nil || !found {
		return gmailapp.GmailReceipt{}, found, err
	}
	var receipt gmailapp.GmailReceipt
	if err := decodeSingleJSON(data, &receipt); err != nil {
		return gmailapp.GmailReceipt{}, false, fmt.Errorf("decode Gmail receipt: %w", err)
	}
	if err := gmailapp.ValidateGmailReceipt(receipt); err != nil {
		return gmailapp.GmailReceipt{}, false, fmt.Errorf("validate Gmail receipt: %w", err)
	}
	return receipt, true, nil
}

func (s *GmailReceiptStore) readCursor(path string) (gmailCursorRecord, bool, error) {
	data, found, err := readSecureFile(path)
	if err != nil || !found {
		return gmailCursorRecord{}, found, err
	}
	var record gmailCursorRecord
	if err := decodeSingleJSON(data, &record); err != nil {
		return gmailCursorRecord{}, false, fmt.Errorf("decode Gmail cursor: %w", err)
	}
	if record.Schema != gmailapp.GmailEnvelopeSchema || strings.TrimSpace(record.Account) != record.Account || strings.TrimSpace(record.Query) != record.Query || record.Account == "" || record.Query == "" || !utf8.ValidString(record.Account) || !utf8.ValidString(record.Query) || strings.ContainsAny(record.Account+record.Query, "\x00\r\n") || len([]byte(record.Cursor)) > gmailReceiptMaxToken || !utf8.ValidString(record.Cursor) || strings.ContainsAny(record.Cursor, "\x00\r\n") {
		return gmailCursorRecord{}, false, errors.New("validate Gmail cursor: malformed record")
	}
	return record, true, nil
}

func readSecureFile(path string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, false, errors.New("Gmail receipt file is unsafe")
	}
	if err := validateGmailPermissions(path, info, 0600); err != nil {
		return nil, false, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() {
		return nil, false, errors.New("Gmail receipt file is not regular")
	}
	currentInfo, err := os.Lstat(path)
	if err != nil || currentInfo.Mode()&os.ModeSymlink != 0 || !currentInfo.Mode().IsRegular() || !os.SameFile(openedInfo, currentInfo) {
		return nil, false, errors.New("Gmail receipt file changed during read")
	}
	if err := validateGmailPermissions(path, currentInfo, 0600); err != nil {
		return nil, false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, gmailReceiptMaxBytes+1))
	if err != nil {
		return nil, false, err
	}
	if len(data) > gmailReceiptMaxBytes {
		return nil, false, errors.New("Gmail receipt file exceeds the storage bound")
	}
	return data, true, nil
}

func decodeSingleJSON(data []byte, destination any) error {
	if !utf8.Valid(data) {
		return errors.New("JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON data")
		}
		return err
	}
	return nil
}

func isReceiptFilename(name string) bool {
	if len(name) != 64+len(".json") || !strings.HasSuffix(name, ".json") {
		return false
	}
	for _, char := range strings.TrimSuffix(name, ".json") {
		if !isLowerHex(char) {
			return false
		}
	}
	return true
}

func isCursorFilename(name string) bool {
	const prefix = "cursor-"
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	return isReceiptFilename(strings.TrimPrefix(name, prefix))
}

func isTemporaryFilename(name string) bool {
	const prefix = ".gmail-receipt-"
	const suffix = ".tmp"
	return strings.HasPrefix(name, prefix) && strings.HasSuffix(name, suffix) && len(name) > len(prefix)+len(suffix)
}

func isLowerHex(char rune) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	return ctx.Err()
}

var _ gmailapp.GmailReceiptStore = (*GmailReceiptStore)(nil)
