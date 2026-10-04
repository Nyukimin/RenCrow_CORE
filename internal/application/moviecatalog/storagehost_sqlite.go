package moviecatalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// StorageHostPreferenceCandidate is the owner-side private proposal record.
// UserID is supplied by the authenticated CORE caller and is never a query
// argument derived from model text.
type StorageHostPreferenceCandidate struct {
	ID          string `json:"id"`
	RequestID   string `json:"request_id"`
	UserID      string `json:"user_id"`
	ActorID     string `json:"actor_id"`
	PayloadHash string `json:"payload_hash"`
	TargetKind  string `json:"target_kind"`
	TargetID    string `json:"target_id"`
	Familiarity string `json:"familiarity"`
	Sentiment   string `json:"sentiment"`
	Note        string `json:"note"`
	State       string `json:"state"`
	CreatedAt   string `json:"created_at"`
}

type StorageHostOperationIdentity struct {
	OpID             string
	Operation        string
	PayloadHash      string
	WriterGeneration int64
}

type StorageHostOperationReceipt struct {
	Identity      StorageHostOperationIdentity
	ResultJSON    []byte
	ResultSHA256  string
	ReceiptSHA256 string
}

type StorageHostCandidateResult struct {
	Candidate StorageHostPreferenceCandidate `json:"candidate"`
	Replay    bool                           `json:"replay"`
}

var (
	ErrStorageHostOperationConflict = errors.New("movie catalog storage-host operation conflict")
	ErrStorageHostReceiptUnknown    = errors.New("movie catalog storage-host receipt is unknown")
)

// StorageHostSQLiteStore is a typed adapter over the existing catalog DB. It
// exposes only the movie catalog projections used by runtime and Viewer; it
// does not accept SQL or filesystem paths from callers.
type StorageHostSQLiteStore struct{ db *sql.DB }

func NewStorageHostSQLiteStore(ctx context.Context, db *sql.DB) (*StorageHostSQLiteStore, error) {
	if db == nil {
		return nil, errors.New("movie catalog database is nil")
	}
	if err := EnsureIndexedLookupSchema(ctx, db); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS movie_preference_candidate (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  target_kind TEXT NOT NULL CHECK(target_kind IN ('movie','person')),
  target_id TEXT NOT NULL,
  familiarity TEXT NOT NULL DEFAULT '',
  sentiment TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'candidate' CHECK(state = 'candidate'),
  created_at TEXT NOT NULL,
  created_by_storagehost_op_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_movie_preference_candidate_user_id ON movie_preference_candidate(user_id,id);
CREATE INDEX IF NOT EXISTS idx_movie_preference_candidate_request_id ON movie_preference_candidate(request_id);
CREATE TABLE IF NOT EXISTS movie_catalog_storagehost_receipt (
  op_id TEXT PRIMARY KEY,
  operation TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  writer_generation INTEGER NOT NULL,
  result_json BLOB NOT NULL,
  result_sha256 TEXT NOT NULL,
  receipt_sha256 TEXT NOT NULL,
  created_at TEXT NOT NULL
);`); err != nil {
		return nil, fmt.Errorf("prepare movie catalog storage-host owner schema: %w", err)
	}
	markerExists, err := tableColumnExists(ctx, db, "movie_preference_candidate", "created_by_storagehost_op_id")
	if err != nil {
		return nil, fmt.Errorf("inspect movie catalog candidate recovery marker: %w", err)
	}
	if !markerExists {
		if _, err := db.ExecContext(ctx, `ALTER TABLE movie_preference_candidate ADD COLUMN created_by_storagehost_op_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return nil, fmt.Errorf("add movie catalog candidate recovery marker: %w", err)
		}
	}
	return &StorageHostSQLiteStore{db: db}, nil
}

func (s *StorageHostSQLiteStore) Lookup(_ context.Context, request LookupRequest) (LookupResult, error) {
	if s == nil || s.db == nil {
		return LookupResult{}, errors.New("movie catalog storage-host owner is closed")
	}
	return Lookup(s.db, request)
}

func (s *StorageHostSQLiteStore) ViewerStats(_ context.Context) (map[string]int, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("movie catalog storage-host owner is closed")
	}
	return Stats(s.db)
}

func (s *StorageHostSQLiteStore) ViewerMovies(_ context.Context, params QueryParams, limit, offset int) (int, []MovieItem, error) {
	if s == nil || s.db == nil {
		return 0, nil, errors.New("movie catalog storage-host owner is closed")
	}
	return Movies(s.db, params, limit, offset)
}

func (s *StorageHostSQLiteStore) ViewerPeople(_ context.Context, params QueryParams, limit, offset int) (int, []PersonItem, error) {
	if s == nil || s.db == nil {
		return 0, nil, errors.New("movie catalog storage-host owner is closed")
	}
	return People(s.db, params, limit, offset)
}

func (s *StorageHostSQLiteStore) ViewerCards(_ context.Context, limit, offset int) (int, []Card, error) {
	if s == nil || s.db == nil {
		return 0, nil, errors.New("movie catalog storage-host owner is closed")
	}
	return Cards(s.db, limit, offset)
}

func (s *StorageHostSQLiteStore) ViewerMovie(_ context.Context, id string) (map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("movie catalog storage-host owner is closed")
	}
	return MovieDetail(s.db, id)
}

func (s *StorageHostSQLiteStore) ViewerPerson(_ context.Context, id string) (map[string]any, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("movie catalog storage-host owner is closed")
	}
	return PersonDetail(s.db, id)
}

func (s *StorageHostSQLiteStore) FindPreferenceCandidateByID(ctx context.Context, userID, candidateID string) (StorageHostPreferenceCandidate, bool, error) {
	return s.findPreferenceCandidate(ctx, "id", candidateID, userID)
}

func (s *StorageHostSQLiteStore) FindPreferenceCandidateByRequestID(ctx context.Context, userID, requestID string) (StorageHostPreferenceCandidate, bool, error) {
	return s.findPreferenceCandidate(ctx, "request_id", requestID, userID)
}

func (s *StorageHostSQLiteStore) findPreferenceCandidate(ctx context.Context, column, key, userID string) (StorageHostPreferenceCandidate, bool, error) {
	if s == nil || s.db == nil {
		return StorageHostPreferenceCandidate{}, false, errors.New("movie catalog storage-host owner is closed")
	}
	if column != "id" && column != "request_id" {
		return StorageHostPreferenceCandidate{}, false, errors.New("invalid movie catalog candidate key")
	}
	if !validStorageHostText(userID, 256, true) || !validStorageHostText(key, 256, true) {
		return StorageHostPreferenceCandidate{}, false, errors.New("invalid movie catalog candidate query")
	}
	candidate, err := scanStorageHostMovieCandidate(s.db.QueryRowContext(ctx, `SELECT id,request_id,user_id,actor_id,payload_hash,target_kind,target_id,familiarity,sentiment,note,state,created_at FROM movie_preference_candidate WHERE `+column+`=? AND user_id=?`, key, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return StorageHostPreferenceCandidate{}, false, nil
	}
	return candidate, err == nil, err
}

func (s *StorageHostSQLiteStore) SavePreferenceCandidate(ctx context.Context, identity StorageHostOperationIdentity, candidate StorageHostPreferenceCandidate) (StorageHostCandidateResult, error) {
	if s == nil || s.db == nil {
		return StorageHostCandidateResult{}, errors.New("movie catalog storage-host owner is closed")
	}
	if !validStorageHostCandidate(candidate) || !validStorageHostIdentity(identity) {
		return StorageHostCandidateResult{}, errors.New("movie catalog candidate mutation is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return StorageHostCandidateResult{}, err
	}
	defer tx.Rollback()
	var operation, payloadHash string
	var generation int64
	var resultJSON []byte
	var resultHash, receiptHash string
	err = tx.QueryRowContext(ctx, `SELECT operation,payload_hash,writer_generation,result_json,result_sha256,receipt_sha256 FROM movie_catalog_storagehost_receipt WHERE op_id=?`, identity.OpID).Scan(&operation, &payloadHash, &generation, &resultJSON, &resultHash, &receiptHash)
	if err == nil {
		storedIdentity := StorageHostOperationIdentity{OpID: identity.OpID, Operation: operation, PayloadHash: payloadHash, WriterGeneration: generation}
		if storedIdentity != identity || !validStorageHostReceipt(storedIdentity, resultJSON, resultHash, receiptHash) {
			if storedIdentity != identity {
				return StorageHostCandidateResult{}, ErrStorageHostOperationConflict
			}
			return StorageHostCandidateResult{}, ErrStorageHostReceiptUnknown
		}
		if operation != identity.Operation || payloadHash != identity.PayloadHash || generation != identity.WriterGeneration {
			return StorageHostCandidateResult{}, ErrStorageHostOperationConflict
		}
		result, decodeErr := decodeStorageHostCandidateResult(resultJSON)
		if decodeErr != nil || !validStorageHostCandidate(result.Candidate) {
			return StorageHostCandidateResult{}, ErrStorageHostReceiptUnknown
		}
		stored, createdBy, found, verifyErr := queryStorageHostMovieCandidate(ctx, tx, "id", result.Candidate.ID)
		if verifyErr != nil || !found || stored != result.Candidate || result.Replay != (createdBy != identity.OpID) {
			return StorageHostCandidateResult{}, ErrStorageHostReceiptUnknown
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return StorageHostCandidateResult{}, err
	}
	stored, createdBy, found, err := queryStorageHostMovieCandidate(ctx, tx, "request_id", candidate.RequestID)
	if err != nil {
		return StorageHostCandidateResult{}, err
	}
	if !found {
		stored, createdBy, found, err = queryStorageHostMovieCandidate(ctx, tx, "id", candidate.ID)
	}
	if err != nil {
		return StorageHostCandidateResult{}, err
	}
	result := StorageHostCandidateResult{Candidate: candidate}
	if found {
		if !sameStorageHostMovieCandidateBinding(stored, candidate) {
			return StorageHostCandidateResult{}, ErrStorageHostOperationConflict
		}
		if createdBy == identity.OpID {
			return StorageHostCandidateResult{}, ErrStorageHostReceiptUnknown
		}
		result.Candidate, result.Replay = stored, true
	} else {
		targetTable, targetColumn := "movies", "movie_id"
		if candidate.TargetKind == "person" {
			targetTable, targetColumn = "people", "person_id"
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+targetTable+" WHERE "+targetColumn+"=?", candidate.TargetID).Scan(&count); err != nil {
			return StorageHostCandidateResult{}, err
		}
		if count != 1 {
			return StorageHostCandidateResult{}, errors.New("movie catalog preference target is not found")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO movie_preference_candidate(id,request_id,user_id,actor_id,payload_hash,target_kind,target_id,familiarity,sentiment,note,state,created_at,created_by_storagehost_op_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, candidate.ID, candidate.RequestID, candidate.UserID, candidate.ActorID, candidate.PayloadHash, candidate.TargetKind, candidate.TargetID, candidate.Familiarity, candidate.Sentiment, candidate.Note, candidate.State, candidate.CreatedAt, identity.OpID); err != nil {
			return StorageHostCandidateResult{}, err
		}
	}
	resultJSON, err = json.Marshal(result)
	if err != nil {
		return StorageHostCandidateResult{}, err
	}
	resultHash = hashStorageHostResult(resultJSON)
	receiptHash = hashStorageHostReceipt(identity, resultHash)
	if _, err := tx.ExecContext(ctx, `INSERT INTO movie_catalog_storagehost_receipt(op_id,operation,payload_hash,writer_generation,result_json,result_sha256,receipt_sha256,created_at) VALUES(?,?,?,?,?,?,?,?)`, identity.OpID, identity.Operation, identity.PayloadHash, identity.WriterGeneration, resultJSON, resultHash, receiptHash, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return StorageHostCandidateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return StorageHostCandidateResult{}, err
	}
	return result, nil
}

func (s *StorageHostSQLiteStore) FindStorageHostOperationReceipt(ctx context.Context, opID string) (StorageHostOperationReceipt, bool, error) {
	if s == nil || s.db == nil {
		return StorageHostOperationReceipt{}, false, errors.New("movie catalog storage-host owner is closed")
	}
	var receipt StorageHostOperationReceipt
	receipt.Identity.OpID = opID
	var result []byte
	err := s.db.QueryRowContext(ctx, `SELECT operation,payload_hash,writer_generation,result_json,result_sha256,receipt_sha256 FROM movie_catalog_storagehost_receipt WHERE op_id=?`, opID).Scan(&receipt.Identity.Operation, &receipt.Identity.PayloadHash, &receipt.Identity.WriterGeneration, &result, &receipt.ResultSHA256, &receipt.ReceiptSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return StorageHostOperationReceipt{}, false, nil
	}
	if err != nil {
		return StorageHostOperationReceipt{}, false, err
	}
	receipt.ResultJSON = append([]byte(nil), result...)
	if !validStorageHostReceipt(receipt.Identity, receipt.ResultJSON, receipt.ResultSHA256, receipt.ReceiptSHA256) {
		return StorageHostOperationReceipt{}, true, ErrStorageHostReceiptUnknown
	}
	decoded, decodeErr := decodeStorageHostCandidateResult(receipt.ResultJSON)
	if decodeErr != nil {
		return StorageHostOperationReceipt{}, true, ErrStorageHostReceiptUnknown
	}
	stored, createdBy, found, verifyErr := queryStorageHostMovieCandidate(ctx, s.db, "id", decoded.Candidate.ID)
	if verifyErr != nil || !found || stored != decoded.Candidate || decoded.Replay != (createdBy != opID) {
		return StorageHostOperationReceipt{}, true, ErrStorageHostReceiptUnknown
	}
	return receipt, true, nil
}

// ValidateStorageHostOperationReceipt binds the stored exact result bytes to
// the accepted op identity. A damaged or substituted receipt is not replayed.
func ValidateStorageHostOperationReceipt(receipt StorageHostOperationReceipt) bool {
	return validStorageHostReceipt(receipt.Identity, receipt.ResultJSON, receipt.ResultSHA256, receipt.ReceiptSHA256)
}

func validStorageHostReceipt(identity StorageHostOperationIdentity, resultJSON []byte, resultHash, receiptHash string) bool {
	return validStorageHostIdentity(identity) && resultHash == hashStorageHostResult(resultJSON) && receiptHash == hashStorageHostReceipt(identity, resultHash)
}

func hashStorageHostResult(resultJSON []byte) string {
	digest := sha256.Sum256(resultJSON)
	return hex.EncodeToString(digest[:])
}

func hashStorageHostReceipt(identity StorageHostOperationIdentity, resultHash string) string {
	canonical, _ := json.Marshal(struct {
		OpID             string `json:"op_id"`
		Operation        string `json:"operation"`
		PayloadHash      string `json:"payload_hash"`
		WriterGeneration int64  `json:"writer_generation"`
		ResultSHA256     string `json:"result_sha256"`
	}{OpID: identity.OpID, Operation: identity.Operation, PayloadHash: identity.PayloadHash, WriterGeneration: identity.WriterGeneration, ResultSHA256: resultHash})
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:])
}

func validStorageHostIdentity(identity StorageHostOperationIdentity) bool {
	if !validStorageHostText(identity.OpID, 128, true) || identity.Operation != "candidate_save" || !validStorageHostHash(identity.PayloadHash) || identity.WriterGeneration <= 0 {
		return false
	}
	for _, char := range identity.OpID {
		if !(char >= 'a' && char <= 'z') && !(char >= 'A' && char <= 'Z') && !(char >= '0' && char <= '9') && char != '.' && char != '_' && char != ':' && char != '-' {
			return false
		}
	}
	return true
}

func validStorageHostCandidate(candidate StorageHostPreferenceCandidate) bool {
	if !validStorageHostText(candidate.ID, 256, true) || !validStorageHostText(candidate.RequestID, 256, true) || !validStorageHostText(candidate.UserID, 256, true) || !validStorageHostText(candidate.ActorID, 128, true) || !validStorageHostHash(candidate.PayloadHash) || (candidate.TargetKind != "movie" && candidate.TargetKind != "person") || !validStorageHostText(candidate.TargetID, 256, true) || (candidate.Familiarity != "" && candidate.Familiarity != "known" && candidate.Familiarity != "unknown") || (candidate.Sentiment != "" && candidate.Sentiment != "like" && candidate.Sentiment != "neutral" && candidate.Sentiment != "dislike") || (candidate.Familiarity == "" && candidate.Sentiment == "") || !validStorageHostText(candidate.Note, 4000, false) || candidate.State != "candidate" || !validStorageHostText(candidate.CreatedAt, 64, true) {
		return false
	}
	createdAt, err := time.Parse(time.RFC3339Nano, candidate.CreatedAt)
	return err == nil && !createdAt.IsZero()
}

// ValidateStorageHostPreferenceCandidate validates the closed private
// candidate DTO before it crosses the storage-host boundary.
func ValidateStorageHostPreferenceCandidate(candidate StorageHostPreferenceCandidate) error {
	if !validStorageHostCandidate(candidate) {
		return errors.New("movie catalog candidate fields are invalid or exceed bounds")
	}
	return nil
}

func validStorageHostText(value string, maxRunes int, required bool) bool {
	if !utf8.ValidString(value) || strings.ContainsRune(value, utf8.RuneError) || strings.ContainsRune(value, 0) || utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	return !required || strings.TrimSpace(value) != ""
}

func validStorageHostHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func sameStorageHostMovieCandidateBinding(left, right StorageHostPreferenceCandidate) bool {
	return left.ID == right.ID && left.RequestID == right.RequestID && left.UserID == right.UserID && left.ActorID == right.ActorID && left.PayloadHash == right.PayloadHash && left.TargetKind == right.TargetKind && left.TargetID == right.TargetID && left.Familiarity == right.Familiarity && left.Sentiment == right.Sentiment && left.Note == right.Note && left.State == right.State
}

type storageHostQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryStorageHostMovieCandidate(ctx context.Context, queryer storageHostQueryer, column, key string) (StorageHostPreferenceCandidate, string, bool, error) {
	if column != "id" && column != "request_id" {
		return StorageHostPreferenceCandidate{}, "", false, errors.New("invalid movie catalog candidate key")
	}
	candidate, createdBy, err := scanStorageHostMovieCandidateAndOperation(queryer.QueryRowContext(ctx, `SELECT id,request_id,user_id,actor_id,payload_hash,target_kind,target_id,familiarity,sentiment,note,state,created_at,created_by_storagehost_op_id FROM movie_preference_candidate WHERE `+column+`=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return StorageHostPreferenceCandidate{}, "", false, nil
	}
	return candidate, createdBy, err == nil, err
}

type storageHostMovieRow interface{ Scan(...any) error }

func scanStorageHostMovieCandidate(row storageHostMovieRow) (StorageHostPreferenceCandidate, error) {
	var candidate StorageHostPreferenceCandidate
	err := row.Scan(&candidate.ID, &candidate.RequestID, &candidate.UserID, &candidate.ActorID, &candidate.PayloadHash, &candidate.TargetKind, &candidate.TargetID, &candidate.Familiarity, &candidate.Sentiment, &candidate.Note, &candidate.State, &candidate.CreatedAt)
	return candidate, err
}

func scanStorageHostMovieCandidateAndOperation(row storageHostMovieRow) (StorageHostPreferenceCandidate, string, error) {
	var candidate StorageHostPreferenceCandidate
	var createdBy string
	err := row.Scan(&candidate.ID, &candidate.RequestID, &candidate.UserID, &candidate.ActorID, &candidate.PayloadHash, &candidate.TargetKind, &candidate.TargetID, &candidate.Familiarity, &candidate.Sentiment, &candidate.Note, &candidate.State, &candidate.CreatedAt, &createdBy)
	return candidate, createdBy, err
}

func decodeStorageHostCandidateResult(raw []byte) (StorageHostCandidateResult, error) {
	var result StorageHostCandidateResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return StorageHostCandidateResult{}, err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return StorageHostCandidateResult{}, errors.New("trailing candidate receipt data")
	}
	canonical, err := json.Marshal(result)
	if err != nil || !bytes.Equal(canonical, raw) {
		return StorageHostCandidateResult{}, errors.New("non-canonical candidate receipt")
	}
	return result, nil
}
