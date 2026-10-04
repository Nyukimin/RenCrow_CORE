package musiccatalog

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

type StorageHostPreferenceCandidate struct {
	CandidateID string `json:"candidate_id"`
	RequestID   string `json:"request_id"`
	UserID      string `json:"user_id"`
	ActorID     string `json:"actor_id"`
	PayloadHash string `json:"payload_hash"`
	TargetID    string `json:"target_item_id"`
	SignalType  string `json:"signal_type"`
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

type StorageHostOverviewItem struct {
	ItemID          string `json:"item_id"`
	Category        string `json:"category"`
	ItemType        string `json:"item_type"`
	Title           string `json:"title"`
	NormalizedTitle string `json:"normalized_title"`
	UpdatedAt       string `json:"updated_at"`
}

type StorageHostOverviewRelation struct {
	RelationID   string `json:"relation_id"`
	FromItemID   string `json:"from_item_id"`
	FromTitle    string `json:"from_title"`
	ToItemID     string `json:"to_item_id"`
	ToTitle      string `json:"to_title"`
	RelationType string `json:"relation_type"`
	Source       string `json:"source"`
	CreatedAt    string `json:"created_at"`
}

type StorageHostOverviewInteraction struct {
	InteractionID   string `json:"interaction_id"`
	ItemID          string `json:"item_id"`
	Title           string `json:"title"`
	Category        string `json:"category"`
	InteractionType string `json:"interaction_type"`
	Source          string `json:"source"`
	CreatedAt       string `json:"created_at"`
}

type StorageHostTopicCandidate struct {
	CandidateID  string `json:"candidate_id"`
	Category     string `json:"category"`
	TopicType    string `json:"topic_type"`
	TargetItemID string `json:"target_item_id"`
	TargetTitle  string `json:"target_title"`
	Title        string `json:"title"`
	Reason       string `json:"reason"`
	Status       string `json:"status"`
	GeneratedBy  string `json:"generated_by"`
	GeneratedAt  string `json:"generated_at"`
}

type StorageHostOverview struct {
	Stats           map[string]int                   `json:"stats"`
	Items           []StorageHostOverviewItem        `json:"items"`
	Relations       []StorageHostOverviewRelation    `json:"relations"`
	Interactions    []StorageHostOverviewInteraction `json:"interactions"`
	TopicCandidates []StorageHostTopicCandidate      `json:"topic_candidates"`
}

var (
	ErrStorageHostOperationConflict = errors.New("hobby graph storage-host operation conflict")
	ErrStorageHostReceiptUnknown    = errors.New("hobby graph storage-host receipt is unknown")
)

var storageHostHobbyGraphTables = []string{
	"hobby_items", "hobby_relations", "hobby_interactions", "hobby_title_observations",
	"hobby_preference_signals", "hobby_topic_candidates", "hobby_collection_runs",
	"hobby_collection_targets", "hobby_music_lyrics", "hobby_music_syntax_features",
	"hobby_music_collection_receipts",
}

type StorageHostSQLiteStore struct{ db *sql.DB }

func NewStorageHostSQLiteStore(ctx context.Context, db *sql.DB) (*StorageHostSQLiteStore, error) {
	if db == nil {
		return nil, errors.New("hobby graph database is nil")
	}
	if err := EnsureIndexedLookupSchema(ctx, db); err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS hobby_agent_preference_candidate (
  candidate_id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  target_item_id TEXT NOT NULL,
  signal_type TEXT NOT NULL CHECK(signal_type IN ('like','dislike','interest','avoid','experienced')),
  note TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'candidate' CHECK(state = 'candidate'),
  created_at TEXT NOT NULL,
  created_by_storagehost_op_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_hobby_agent_preference_candidate_user ON hobby_agent_preference_candidate(user_id,candidate_id);
CREATE INDEX IF NOT EXISTS idx_hobby_agent_preference_candidate_request ON hobby_agent_preference_candidate(request_id);
CREATE TABLE IF NOT EXISTS hobby_graph_storagehost_receipt (
  op_id TEXT PRIMARY KEY,
  operation TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  writer_generation INTEGER NOT NULL,
  result_json BLOB NOT NULL,
  result_sha256 TEXT NOT NULL,
  receipt_sha256 TEXT NOT NULL,
  created_at TEXT NOT NULL
);`); err != nil {
		return nil, fmt.Errorf("prepare hobby graph storage-host owner schema: %w", err)
	}
	markerExists, err := storageHostHobbyColumnExists(ctx, db, "hobby_agent_preference_candidate", "created_by_storagehost_op_id")
	if err != nil {
		return nil, fmt.Errorf("inspect hobby graph candidate recovery marker: %w", err)
	}
	if !markerExists {
		if _, err := db.ExecContext(ctx, `ALTER TABLE hobby_agent_preference_candidate ADD COLUMN created_by_storagehost_op_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return nil, fmt.Errorf("add hobby graph candidate recovery marker: %w", err)
		}
	}
	return &StorageHostSQLiteStore{db: db}, nil
}

func (s *StorageHostSQLiteStore) LookupCatalog(ctx context.Context, request CatalogRequest) (CatalogResult, error) {
	if s == nil || s.db == nil {
		return CatalogResult{}, errors.New("hobby graph storage-host owner is closed")
	}
	return LookupCatalog(ctx, s.db, request)
}

func (s *StorageHostSQLiteStore) LookupLyrics(ctx context.Context, request LyricsRequest) (LyricsResult, error) {
	if s == nil || s.db == nil {
		return LyricsResult{}, errors.New("hobby graph storage-host owner is closed")
	}
	return LookupLyrics(ctx, s.db, request)
}

func (s *StorageHostSQLiteStore) ViewerStats(ctx context.Context) (map[string]int, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("hobby graph storage-host owner is closed")
	}
	stats := make(map[string]int, len(storageHostHobbyGraphTables))
	for _, table := range storageHostHobbyGraphTables {
		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
			return nil, err
		}
		stats[table] = count
	}
	return stats, nil
}

func (s *StorageHostSQLiteStore) ViewerOverview(ctx context.Context, limit int) (StorageHostOverview, error) {
	if s == nil || s.db == nil {
		return StorageHostOverview{}, errors.New("hobby graph storage-host owner is closed")
	}
	if limit < 1 || limit > 20 {
		return StorageHostOverview{}, errors.New("hobby graph overview limit is invalid")
	}
	stats, err := s.ViewerStats(ctx)
	if err != nil {
		return StorageHostOverview{}, err
	}
	items, err := storageHostHobbyOverviewItems(ctx, s.db, limit)
	if err != nil {
		return StorageHostOverview{}, err
	}
	relations, err := storageHostHobbyOverviewRelations(ctx, s.db, limit)
	if err != nil {
		return StorageHostOverview{}, err
	}
	interactions, err := storageHostHobbyOverviewInteractions(ctx, s.db, limit)
	if err != nil {
		return StorageHostOverview{}, err
	}
	candidates, err := storageHostHobbyOverviewCandidates(ctx, s.db, limit)
	if err != nil {
		return StorageHostOverview{}, err
	}
	return StorageHostOverview{Stats: stats, Items: items, Relations: relations, Interactions: interactions, TopicCandidates: candidates}, nil
}

func (s *StorageHostSQLiteStore) FindPreferenceCandidateByID(ctx context.Context, userID, candidateID string) (StorageHostPreferenceCandidate, bool, error) {
	return s.findPreferenceCandidate(ctx, "candidate_id", candidateID, userID)
}

func (s *StorageHostSQLiteStore) FindPreferenceCandidateByRequestID(ctx context.Context, userID, requestID string) (StorageHostPreferenceCandidate, bool, error) {
	return s.findPreferenceCandidate(ctx, "request_id", requestID, userID)
}

func (s *StorageHostSQLiteStore) findPreferenceCandidate(ctx context.Context, column, key, userID string) (StorageHostPreferenceCandidate, bool, error) {
	if s == nil || s.db == nil {
		return StorageHostPreferenceCandidate{}, false, errors.New("hobby graph storage-host owner is closed")
	}
	if column != "candidate_id" && column != "request_id" {
		return StorageHostPreferenceCandidate{}, false, errors.New("invalid hobby graph candidate key")
	}
	if !storageHostHobbyText(userID, 256, true) || !storageHostHobbyText(key, 256, true) {
		return StorageHostPreferenceCandidate{}, false, errors.New("invalid hobby graph candidate query")
	}
	candidate, err := scanStorageHostHobbyCandidate(s.db.QueryRowContext(ctx, `SELECT candidate_id,request_id,user_id,actor_id,payload_hash,target_item_id,signal_type,note,state,created_at FROM hobby_agent_preference_candidate WHERE `+column+`=? AND user_id=?`, key, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return StorageHostPreferenceCandidate{}, false, nil
	}
	return candidate, err == nil, err
}

func (s *StorageHostSQLiteStore) SavePreferenceCandidate(ctx context.Context, identity StorageHostOperationIdentity, candidate StorageHostPreferenceCandidate) (StorageHostCandidateResult, error) {
	if s == nil || s.db == nil {
		return StorageHostCandidateResult{}, errors.New("hobby graph storage-host owner is closed")
	}
	if !validStorageHostHobbyCandidate(candidate) || !validStorageHostHobbyIdentity(identity) {
		return StorageHostCandidateResult{}, errors.New("hobby graph candidate mutation is invalid")
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
	err = tx.QueryRowContext(ctx, `SELECT operation,payload_hash,writer_generation,result_json,result_sha256,receipt_sha256 FROM hobby_graph_storagehost_receipt WHERE op_id=?`, identity.OpID).Scan(&operation, &payloadHash, &generation, &resultJSON, &resultHash, &receiptHash)
	if err == nil {
		storedIdentity := StorageHostOperationIdentity{OpID: identity.OpID, Operation: operation, PayloadHash: payloadHash, WriterGeneration: generation}
		if storedIdentity != identity || !validStorageHostHobbyReceipt(storedIdentity, resultJSON, resultHash, receiptHash) {
			if storedIdentity != identity {
				return StorageHostCandidateResult{}, ErrStorageHostOperationConflict
			}
			return StorageHostCandidateResult{}, ErrStorageHostReceiptUnknown
		}
		result, decodeErr := decodeStorageHostHobbyCandidateResult(resultJSON)
		if decodeErr != nil {
			return StorageHostCandidateResult{}, ErrStorageHostReceiptUnknown
		}
		stored, createdBy, found, verifyErr := queryStorageHostHobbyCandidate(ctx, tx, "candidate_id", result.Candidate.CandidateID)
		if verifyErr != nil || !found || stored != result.Candidate || result.Replay != (createdBy != identity.OpID) {
			return StorageHostCandidateResult{}, ErrStorageHostReceiptUnknown
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return StorageHostCandidateResult{}, err
	}
	stored, createdBy, found, err := queryStorageHostHobbyCandidate(ctx, tx, "request_id", candidate.RequestID)
	if err != nil {
		return StorageHostCandidateResult{}, err
	}
	if !found {
		stored, createdBy, found, err = queryStorageHostHobbyCandidate(ctx, tx, "candidate_id", candidate.CandidateID)
	}
	if err != nil {
		return StorageHostCandidateResult{}, err
	}
	result := StorageHostCandidateResult{Candidate: candidate}
	if found {
		if !sameStorageHostHobbyCandidateBinding(stored, candidate) {
			return StorageHostCandidateResult{}, ErrStorageHostOperationConflict
		}
		if createdBy == identity.OpID {
			return StorageHostCandidateResult{}, ErrStorageHostReceiptUnknown
		}
		result.Candidate, result.Replay = stored, true
	} else {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM hobby_items WHERE item_id=?`, candidate.TargetID).Scan(&count); err != nil {
			return StorageHostCandidateResult{}, err
		}
		if count != 1 {
			return StorageHostCandidateResult{}, errors.New("hobby graph preference target is not found")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO hobby_agent_preference_candidate(candidate_id,request_id,user_id,actor_id,payload_hash,target_item_id,signal_type,note,state,created_at,created_by_storagehost_op_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, candidate.CandidateID, candidate.RequestID, candidate.UserID, candidate.ActorID, candidate.PayloadHash, candidate.TargetID, candidate.SignalType, candidate.Note, candidate.State, candidate.CreatedAt, identity.OpID); err != nil {
			return StorageHostCandidateResult{}, err
		}
	}
	resultJSON, err = json.Marshal(result)
	if err != nil {
		return StorageHostCandidateResult{}, err
	}
	resultHash = storageHostHobbyResultHash(resultJSON)
	receiptHash = storageHostHobbyReceiptHash(identity, resultHash)
	if _, err := tx.ExecContext(ctx, `INSERT INTO hobby_graph_storagehost_receipt(op_id,operation,payload_hash,writer_generation,result_json,result_sha256,receipt_sha256,created_at) VALUES(?,?,?,?,?,?,?,?)`, identity.OpID, identity.Operation, identity.PayloadHash, identity.WriterGeneration, resultJSON, resultHash, receiptHash, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return StorageHostCandidateResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return StorageHostCandidateResult{}, err
	}
	return result, nil
}

func (s *StorageHostSQLiteStore) FindStorageHostOperationReceipt(ctx context.Context, opID string) (StorageHostOperationReceipt, bool, error) {
	if s == nil || s.db == nil {
		return StorageHostOperationReceipt{}, false, errors.New("hobby graph storage-host owner is closed")
	}
	var receipt StorageHostOperationReceipt
	receipt.Identity.OpID = opID
	var result []byte
	err := s.db.QueryRowContext(ctx, `SELECT operation,payload_hash,writer_generation,result_json,result_sha256,receipt_sha256 FROM hobby_graph_storagehost_receipt WHERE op_id=?`, opID).Scan(&receipt.Identity.Operation, &receipt.Identity.PayloadHash, &receipt.Identity.WriterGeneration, &result, &receipt.ResultSHA256, &receipt.ReceiptSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return StorageHostOperationReceipt{}, false, nil
	}
	if err != nil {
		return StorageHostOperationReceipt{}, false, err
	}
	receipt.ResultJSON = append([]byte(nil), result...)
	if !validStorageHostHobbyReceipt(receipt.Identity, receipt.ResultJSON, receipt.ResultSHA256, receipt.ReceiptSHA256) {
		return StorageHostOperationReceipt{}, true, ErrStorageHostReceiptUnknown
	}
	decoded, decodeErr := decodeStorageHostHobbyCandidateResult(receipt.ResultJSON)
	if decodeErr != nil {
		return StorageHostOperationReceipt{}, true, ErrStorageHostReceiptUnknown
	}
	stored, createdBy, found, verifyErr := queryStorageHostHobbyCandidate(ctx, s.db, "candidate_id", decoded.Candidate.CandidateID)
	if verifyErr != nil || !found || stored != decoded.Candidate || decoded.Replay != (createdBy != opID) {
		return StorageHostOperationReceipt{}, true, ErrStorageHostReceiptUnknown
	}
	return receipt, true, nil
}

func ValidateStorageHostOperationReceipt(receipt StorageHostOperationReceipt) bool {
	return validStorageHostHobbyReceipt(receipt.Identity, receipt.ResultJSON, receipt.ResultSHA256, receipt.ReceiptSHA256)
}

func storageHostHobbyOverviewItems(ctx context.Context, db *sql.DB, limit int) ([]StorageHostOverviewItem, error) {
	rows, err := db.QueryContext(ctx, `SELECT item_id,category,item_type,title,normalized_title,updated_at FROM hobby_items ORDER BY updated_at DESC,created_at DESC,title LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StorageHostOverviewItem{}
	for rows.Next() {
		var item StorageHostOverviewItem
		if err := rows.Scan(&item.ItemID, &item.Category, &item.ItemType, &item.Title, &item.NormalizedTitle, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func storageHostHobbyOverviewRelations(ctx context.Context, db *sql.DB, limit int) ([]StorageHostOverviewRelation, error) {
	rows, err := db.QueryContext(ctx, `SELECT r.relation_id,r.from_item_id,COALESCE(fi.title,r.from_item_id),r.to_item_id,COALESCE(ti.title,r.to_item_id),r.relation_type,r.source,r.created_at FROM hobby_relations r LEFT JOIN hobby_items fi ON fi.item_id=r.from_item_id LEFT JOIN hobby_items ti ON ti.item_id=r.to_item_id ORDER BY r.created_at DESC,r.relation_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StorageHostOverviewRelation{}
	for rows.Next() {
		var item StorageHostOverviewRelation
		if err := rows.Scan(&item.RelationID, &item.FromItemID, &item.FromTitle, &item.ToItemID, &item.ToTitle, &item.RelationType, &item.Source, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func storageHostHobbyOverviewInteractions(ctx context.Context, db *sql.DB, limit int) ([]StorageHostOverviewInteraction, error) {
	rows, err := db.QueryContext(ctx, `SELECT i.interaction_id,COALESCE(i.item_id,''),COALESCE(h.title,i.original_title),i.category,i.interaction_type,i.source,i.created_at FROM hobby_interactions i LEFT JOIN hobby_items h ON h.item_id=i.item_id ORDER BY i.created_at DESC,i.interaction_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StorageHostOverviewInteraction{}
	for rows.Next() {
		var item StorageHostOverviewInteraction
		if err := rows.Scan(&item.InteractionID, &item.ItemID, &item.Title, &item.Category, &item.InteractionType, &item.Source, &item.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func storageHostHobbyOverviewCandidates(ctx context.Context, db *sql.DB, limit int) ([]StorageHostTopicCandidate, error) {
	rows, err := db.QueryContext(ctx, `SELECT c.candidate_id,COALESCE(c.category,''),c.topic_type,COALESCE(c.target_item_id,''),COALESCE(h.title,c.target_item_id,''),c.title,c.reason,c.status,c.generated_by,c.generated_at FROM hobby_topic_candidates c LEFT JOIN hobby_items h ON h.item_id=c.target_item_id ORDER BY c.generated_at DESC,c.candidate_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StorageHostTopicCandidate{}
	for rows.Next() {
		var item StorageHostTopicCandidate
		if err := rows.Scan(&item.CandidateID, &item.Category, &item.TopicType, &item.TargetItemID, &item.TargetTitle, &item.Title, &item.Reason, &item.Status, &item.GeneratedBy, &item.GeneratedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type storageHostHobbyQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func queryStorageHostHobbyCandidate(ctx context.Context, queryer storageHostHobbyQueryer, column, key string) (StorageHostPreferenceCandidate, string, bool, error) {
	if column != "candidate_id" && column != "request_id" {
		return StorageHostPreferenceCandidate{}, "", false, errors.New("invalid hobby graph candidate key")
	}
	candidate, createdBy, err := scanStorageHostHobbyCandidateAndOperation(queryer.QueryRowContext(ctx, `SELECT candidate_id,request_id,user_id,actor_id,payload_hash,target_item_id,signal_type,note,state,created_at,created_by_storagehost_op_id FROM hobby_agent_preference_candidate WHERE `+column+`=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return StorageHostPreferenceCandidate{}, "", false, nil
	}
	return candidate, createdBy, err == nil, err
}

type storageHostHobbyRow interface{ Scan(...any) error }

func scanStorageHostHobbyCandidate(row storageHostHobbyRow) (StorageHostPreferenceCandidate, error) {
	var candidate StorageHostPreferenceCandidate
	err := row.Scan(&candidate.CandidateID, &candidate.RequestID, &candidate.UserID, &candidate.ActorID, &candidate.PayloadHash, &candidate.TargetID, &candidate.SignalType, &candidate.Note, &candidate.State, &candidate.CreatedAt)
	return candidate, err
}
func scanStorageHostHobbyCandidateAndOperation(row storageHostHobbyRow) (StorageHostPreferenceCandidate, string, error) {
	var candidate StorageHostPreferenceCandidate
	var createdBy string
	err := row.Scan(&candidate.CandidateID, &candidate.RequestID, &candidate.UserID, &candidate.ActorID, &candidate.PayloadHash, &candidate.TargetID, &candidate.SignalType, &candidate.Note, &candidate.State, &candidate.CreatedAt, &createdBy)
	return candidate, createdBy, err
}
func sameStorageHostHobbyCandidateBinding(a, b StorageHostPreferenceCandidate) bool {
	return a.CandidateID == b.CandidateID && a.RequestID == b.RequestID && a.UserID == b.UserID && a.ActorID == b.ActorID && a.PayloadHash == b.PayloadHash && a.TargetID == b.TargetID && a.SignalType == b.SignalType && a.Note == b.Note && a.State == b.State
}
func validStorageHostHobbyCandidate(c StorageHostPreferenceCandidate) bool {
	if !storageHostHobbyText(c.CandidateID, 320, true) || !strings.HasPrefix(c.CandidateID, "hobby-preference-candidate/sha256:") || !storageHostHobbyText(c.RequestID, 256, true) || !storageHostHobbyText(c.UserID, 256, true) || !storageHostHobbyText(c.ActorID, 128, true) || !validStorageHostHobbyHash(c.PayloadHash) || !storageHostHobbyText(c.TargetID, 256, true) || !storageHostHobbyText(c.Note, 4000, false) || c.State != "candidate" {
		return false
	}
	if len(strings.TrimPrefix(c.CandidateID, "hobby-preference-candidate/sha256:")) != 64 {
		return false
	}
	for _, c := range strings.TrimPrefix(c.CandidateID, "hobby-preference-candidate/sha256:") {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	if !validStorageHostHobbyHash(strings.TrimPrefix(c.CandidateID, "hobby-preference-candidate/sha256:")) {
		return false
	}
	switch c.SignalType {
	case "like", "dislike", "interest", "avoid", "experienced":
	default:
		return false
	}
	createdAt, err := time.Parse(time.RFC3339Nano, c.CreatedAt)
	return err == nil && !createdAt.IsZero()
}
func validStorageHostHobbyIdentity(i StorageHostOperationIdentity) bool {
	if !storageHostHobbyText(i.OpID, 128, true) || i.Operation != "candidate_save" || !validStorageHostHobbyHash(i.PayloadHash) || i.WriterGeneration <= 0 {
		return false
	}
	for _, c := range i.OpID {
		if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') && c != '.' && c != '_' && c != ':' && c != '-' {
			return false
		}
	}
	return true
}
func storageHostHobbyText(value string, maxRunes int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, utf8.RuneError) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maxRunes && (!required || strings.TrimSpace(value) != "")
}
func validStorageHostHobbyHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func storageHostHobbyColumnExists(ctx context.Context, db *sql.DB, table, column string) (bool, error) {
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column).Scan(&count)
	return count == 1, err
}
func storageHostHobbyResultHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func storageHostHobbyReceiptHash(i StorageHostOperationIdentity, resultHash string) string {
	raw, _ := json.Marshal(struct {
		OpID             string `json:"op_id"`
		Operation        string `json:"operation"`
		PayloadHash      string `json:"payload_hash"`
		WriterGeneration int64  `json:"writer_generation"`
		ResultSHA256     string `json:"result_sha256"`
	}{i.OpID, i.Operation, i.PayloadHash, i.WriterGeneration, resultHash})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func validStorageHostHobbyReceipt(i StorageHostOperationIdentity, result []byte, resultHash, receiptHash string) bool {
	return validStorageHostHobbyIdentity(i) && storageHostHobbyResultHash(result) == resultHash && storageHostHobbyReceiptHash(i, resultHash) == receiptHash
}
func decodeStorageHostHobbyCandidateResult(raw []byte) (StorageHostCandidateResult, error) {
	var result StorageHostCandidateResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return StorageHostCandidateResult{}, err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return StorageHostCandidateResult{}, errors.New("trailing hobby graph receipt data")
	}
	canonical, err := json.Marshal(result)
	if err != nil || !bytes.Equal(canonical, raw) {
		return StorageHostCandidateResult{}, errors.New("non-canonical hobby graph receipt")
	}
	return result, nil
}

// ValidateStorageHostPreferenceCandidate validates the private candidate DTO
// before it crosses the storage-host owner boundary.
func ValidateStorageHostPreferenceCandidate(candidate StorageHostPreferenceCandidate) error {
	if !validStorageHostHobbyCandidate(candidate) {
		return errors.New("hobby graph candidate fields are invalid or exceed bounds")
	}
	return nil
}
