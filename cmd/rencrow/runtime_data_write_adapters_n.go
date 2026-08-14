package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeHobbyPreferenceCandidateIDPrefix = "hobby-preference-candidate/sha256:"

const (
	runtimeHobbyPreferenceRequestMaxRunes = 256
	runtimeHobbyPreferenceUserMaxRunes    = 256
	runtimeHobbyPreferenceActorMaxRunes   = 128
	runtimeHobbyPreferenceTargetMaxRunes  = 256
	runtimeHobbyPreferenceNoteMaxRunes    = 4000
)

// runtimeHobbyPreferenceCandidate is the private owner projection shared by
// the write and exact-recall routes. Database details never enter a model
// payload or a recall record.
type runtimeHobbyPreferenceCandidate struct {
	CandidateID string
	RequestID   string
	UserID      string
	ActorID     string
	PayloadHash string
	TargetID    string
	SignalType  string
	Note        string
	State       string
	CreatedAt   string
}

type runtimeHobbyPreferenceCandidateWritePayload struct {
	TargetItemID string `json:"target_item_id"`
	SignalType   string `json:"signal_type"`
	Note         string `json:"note,omitempty"`
}

type runtimeHobbyPreferenceCandidateWriter struct {
	mu     sync.Mutex
	lookup *runtimeMusicCatalogLookup
}

// registerRuntimeDataWriteHobbyGraph installs the owner route for private
// preference proposals. It initializes only the dedicated candidate table;
// canonical hobby items, interactions, preference signals and lyrics remain
// read-only to this route.
func registerRuntimeDataWriteHobbyGraph(r *runtimeDataWriteRegistry, lookup *runtimeMusicCatalogLookup) error {
	if r == nil || lookup == nil {
		return fmt.Errorf("hobby graph preference candidate data write unavailable")
	}
	if err := ensureRuntimeHobbyPreferenceCandidateSchema(context.Background(), lookup); err != nil {
		return err
	}
	writer := &runtimeHobbyPreferenceCandidateWriter{lookup: lookup}
	return r.RegisterWithContract("hobby_graph", "propose_preference_candidate", dataRecallAccessUser, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"signal_type", "target_item_id"},
		OptionalPayloadFields: []string{"note"},
	}, writer.write)
}

func (w *runtimeHobbyPreferenceCandidateWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	if w == nil || w.lookup == nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("hobby graph preference candidate data write unavailable")
	}
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	userID := strings.TrimSpace(scope.AuthenticatedUserID)
	actorID := strings.TrimSpace(scope.ActorID)
	if userID == "" || !scope.Allows(domaintool.DataScopeUser) {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("hobby graph preference candidate requires authenticated user scope")
	}
	if err := validateRuntimeHobbyPreferenceText("request_id", scope.RequestID, runtimeHobbyPreferenceRequestMaxRunes, false); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeHobbyPreferenceText("user_id", userID, runtimeHobbyPreferenceUserMaxRunes, false); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeHobbyPreferenceText("actor_id", actorID, runtimeHobbyPreferenceActorMaxRunes, false); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, payloadHash, err := decodeRuntimeHobbyPreferenceCandidatePayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	candidate := runtimeHobbyPreferenceCandidate{
		CandidateID: runtimeDataWriteDerivedID(runtimeHobbyPreferenceCandidateIDPrefix, scope.RequestID),
		RequestID:   strings.TrimSpace(scope.RequestID),
		UserID:      userID,
		ActorID:     actorID,
		PayloadHash: payloadHash,
		TargetID:    payload.TargetItemID,
		SignalType:  payload.SignalType,
		Note:        payload.Note,
		State:       "candidate",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	stored, found, err := insertRuntimeHobbyPreferenceCandidate(ctx, w.lookup, candidate)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if found {
		if !runtimeHobbyPreferenceCandidateBindingEqual(stored, candidate) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("hobby graph preference candidate idempotency binding mismatch")
		}
		return runtimeHobbyPreferenceCandidateOwnerResult(stored.CandidateID, candidate.RequestID, true), nil
	}
	return runtimeHobbyPreferenceCandidateOwnerResult(candidate.CandidateID, candidate.RequestID, false), nil
}

func runtimeHobbyPreferenceCandidateOwnerResult(candidateID, requestID string, replay bool) runtimeDataWriteOwnerResult {
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "hobby-preference-candidate/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         candidateID,
		IdempotencyKey:   requestID,
		IdempotentReplay: replay,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}
}

func decodeRuntimeHobbyPreferenceCandidatePayload(payload map[string]any) (runtimeHobbyPreferenceCandidateWritePayload, string, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"target_item_id": {}, "signal_type": {}, "note": {},
	}); err != nil {
		return runtimeHobbyPreferenceCandidateWritePayload{}, "", err
	}
	for key, value := range payload {
		if text, ok := value.(string); ok && !utf8.ValidString(text) {
			return runtimeHobbyPreferenceCandidateWritePayload{}, "", fmt.Errorf("%s must be valid UTF-8", key)
		}
	}
	var decoded runtimeHobbyPreferenceCandidateWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeHobbyPreferenceCandidateWritePayload{}, "", err
	}
	decoded.TargetItemID = strings.TrimSpace(decoded.TargetItemID)
	decoded.SignalType = strings.ToLower(strings.TrimSpace(decoded.SignalType))
	decoded.Note = strings.TrimSpace(decoded.Note)
	if err := validateRuntimeHobbyPreferenceText("target_item_id", decoded.TargetItemID, runtimeHobbyPreferenceTargetMaxRunes, false); err != nil {
		return runtimeHobbyPreferenceCandidateWritePayload{}, "", err
	}
	if err := validateRuntimeHobbyPreferenceText("signal_type", decoded.SignalType, 16, false); err != nil {
		return runtimeHobbyPreferenceCandidateWritePayload{}, "", err
	}
	switch decoded.SignalType {
	case "like", "dislike", "interest", "avoid", "experienced":
	default:
		return runtimeHobbyPreferenceCandidateWritePayload{}, "", fmt.Errorf("signal_type must be like, dislike, interest, avoid, or experienced")
	}
	if err := validateRuntimeHobbyPreferenceText("note", decoded.Note, runtimeHobbyPreferenceNoteMaxRunes, true); err != nil {
		return runtimeHobbyPreferenceCandidateWritePayload{}, "", err
	}
	canonical := struct {
		TargetItemID string `json:"target_item_id"`
		SignalType   string `json:"signal_type"`
		Note         string `json:"note,omitempty"`
	}{TargetItemID: decoded.TargetItemID, SignalType: decoded.SignalType, Note: decoded.Note}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return runtimeHobbyPreferenceCandidateWritePayload{}, "", fmt.Errorf("canonicalize hobby graph preference candidate payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return decoded, hex.EncodeToString(digest[:]), nil
}

func validateRuntimeHobbyPreferenceText(field, value string, maxRunes int, allowEmpty bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", field, maxRunes)
	}
	return nil
}

func runtimeHobbyPreferenceCandidateBindingEqual(left, right runtimeHobbyPreferenceCandidate) bool {
	return left.CandidateID == right.CandidateID &&
		left.RequestID == right.RequestID &&
		left.UserID == right.UserID &&
		left.ActorID == right.ActorID &&
		left.PayloadHash == right.PayloadHash &&
		left.TargetID == right.TargetID &&
		left.SignalType == right.SignalType &&
		left.Note == right.Note &&
		left.State == right.State
}

func ensureRuntimeHobbyPreferenceCandidateSchema(ctx context.Context, lookup *runtimeMusicCatalogLookup) error {
	if lookup == nil || strings.TrimSpace(lookup.dbPath) == "" {
		return fmt.Errorf("hobby graph preference candidate is unavailable")
	}
	db, err := openRuntimePersonRelatedCatalogReadWrite(lookup.dbPath)
	if err != nil {
		return fmt.Errorf("open hobby graph preference candidate schema: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect hobby graph preference candidate schema: %w", err)
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
  created_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("create hobby graph preference candidate table: %w", err)
	}
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS idx_hobby_agent_preference_candidate_user ON hobby_agent_preference_candidate(user_id,candidate_id)`,
		`CREATE INDEX IF NOT EXISTS idx_hobby_agent_preference_candidate_request ON hobby_agent_preference_candidate(request_id)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create hobby graph preference candidate index: %w", err)
		}
	}
	return nil
}

func insertRuntimeHobbyPreferenceCandidate(ctx context.Context, lookup *runtimeMusicCatalogLookup, candidate runtimeHobbyPreferenceCandidate) (runtimeHobbyPreferenceCandidate, bool, error) {
	if lookup == nil || strings.TrimSpace(lookup.dbPath) == "" {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("hobby graph preference candidate is unavailable")
	}
	db, err := openRuntimePersonRelatedCatalogReadWrite(lookup.dbPath)
	if err != nil {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("open hobby graph preference candidate write: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("connect hobby graph preference candidate write: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("begin hobby graph preference candidate transaction: %w", err)
	}
	rollback := func(cause error) (runtimeHobbyPreferenceCandidate, bool, error) {
		_ = tx.Rollback()
		return runtimeHobbyPreferenceCandidate{}, false, cause
	}
	if existing, found, err := queryRuntimeHobbyPreferenceCandidateTx(ctx, tx, "request_id", candidate.RequestID); err != nil {
		return rollback(err)
	} else if found {
		if err := tx.Rollback(); err != nil {
			return runtimeHobbyPreferenceCandidate{}, false, err
		}
		return existing, true, nil
	}
	if existing, found, err := queryRuntimeHobbyPreferenceCandidateTx(ctx, tx, "candidate_id", candidate.CandidateID); err != nil {
		return rollback(err)
	} else if found {
		if err := tx.Rollback(); err != nil {
			return runtimeHobbyPreferenceCandidate{}, false, err
		}
		return existing, true, nil
	}
	var targetCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM hobby_items WHERE item_id = ?`, candidate.TargetID).Scan(&targetCount); err != nil {
		return rollback(fmt.Errorf("verify hobby graph preference target: %w", err))
	}
	if targetCount != 1 {
		return rollback(fmt.Errorf("hobby graph preference target is not found"))
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO hobby_agent_preference_candidate
  (candidate_id,request_id,user_id,actor_id,payload_hash,target_item_id,signal_type,note,state,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?)`,
		candidate.CandidateID, candidate.RequestID, candidate.UserID, candidate.ActorID, candidate.PayloadHash,
		candidate.TargetID, candidate.SignalType, candidate.Note, candidate.State, candidate.CreatedAt); err != nil {
		return rollback(fmt.Errorf("insert hobby graph preference candidate: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("commit hobby graph preference candidate: %w", err)
	}
	return candidate, false, nil
}

func queryRuntimeHobbyPreferenceCandidateTx(ctx context.Context, tx *sql.Tx, column, value string) (runtimeHobbyPreferenceCandidate, bool, error) {
	if column != "candidate_id" && column != "request_id" {
		return runtimeHobbyPreferenceCandidate{}, false, fmt.Errorf("unsupported hobby graph preference candidate lookup column")
	}
	row := tx.QueryRowContext(ctx, `
SELECT candidate_id,request_id,user_id,actor_id,payload_hash,target_item_id,signal_type,note,state,created_at
FROM hobby_agent_preference_candidate WHERE `+column+` = ?`, value)
	candidate, err := scanRuntimeHobbyPreferenceCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return runtimeHobbyPreferenceCandidate{}, false, nil
	}
	if err != nil {
		return runtimeHobbyPreferenceCandidate{}, false, err
	}
	return candidate, true, nil
}

type runtimeHobbyPreferenceCandidateRow interface {
	Scan(...any) error
}

func scanRuntimeHobbyPreferenceCandidate(row runtimeHobbyPreferenceCandidateRow) (runtimeHobbyPreferenceCandidate, error) {
	var candidate runtimeHobbyPreferenceCandidate
	err := row.Scan(
		&candidate.CandidateID, &candidate.RequestID, &candidate.UserID, &candidate.ActorID,
		&candidate.PayloadHash, &candidate.TargetID, &candidate.SignalType, &candidate.Note,
		&candidate.State, &candidate.CreatedAt,
	)
	return candidate, err
}
