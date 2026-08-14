package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const runtimeMoviePreferenceCandidateIDPrefix = "movie-preference-candidate/sha256:"

const (
	runtimeMoviePreferenceRequestMaxRunes = 256
	runtimeMoviePreferenceUserMaxRunes    = 256
	runtimeMoviePreferenceActorMaxRunes   = 128
	runtimeMoviePreferenceTargetMaxRunes  = 256
	runtimeMoviePreferenceNoteMaxRunes    = 4000
)

type runtimeMoviePreferenceCandidateWritePayload struct {
	TargetKind  string `json:"target_kind"`
	TargetID    string `json:"target_id"`
	Familiarity string `json:"familiarity,omitempty"`
	Sentiment   string `json:"sentiment,omitempty"`
	Note        string `json:"note,omitempty"`
}

type runtimeMoviePreferenceCandidateWriter struct {
	mu     sync.Mutex
	lookup *runtimeMovieCatalogLookup
}

func registerRuntimeDataWriteMovieCatalog(r *runtimeDataWriteRegistry, lookup *runtimeMovieCatalogLookup) error {
	if r == nil || lookup == nil {
		return fmt.Errorf("movie catalog preference candidate data write unavailable")
	}
	if err := lookup.ensureRuntimeMoviePreferenceCandidateSchema(context.Background()); err != nil {
		return err
	}
	writer := &runtimeMoviePreferenceCandidateWriter{lookup: lookup}
	return r.RegisterWithContract("movie_catalog", "propose_preference_candidate", dataRecallAccessUser, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"target_id", "target_kind"},
		OptionalPayloadFields: []string{"familiarity", "note", "sentiment"},
	}, writer.write)
}

func (w *runtimeMoviePreferenceCandidateWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	if w == nil || w.lookup == nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("movie catalog preference candidate data write unavailable")
	}
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	userID := strings.TrimSpace(scope.AuthenticatedUserID)
	if !scope.Allows(domaintool.DataScopeUser) || userID == "" {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("movie catalog preference candidate requires authenticated user scope")
	}
	if err := validateRuntimeMoviePreferenceText("request_id", scope.RequestID, runtimeMoviePreferenceRequestMaxRunes, false); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeMoviePreferenceText("user_id", userID, runtimeMoviePreferenceUserMaxRunes, false); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if err := validateRuntimeMoviePreferenceText("actor_id", scope.ActorID, runtimeMoviePreferenceActorMaxRunes, false); err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, payloadHash, err := decodeRuntimeMoviePreferenceCandidatePayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	candidate := runtimeMoviePreferenceCandidate{
		ID:          runtimeDataWriteDerivedID(runtimeMoviePreferenceCandidateIDPrefix, scope.RequestID),
		RequestID:   scope.RequestID,
		UserID:      userID,
		ActorID:     strings.TrimSpace(scope.ActorID),
		PayloadHash: payloadHash,
		TargetKind:  payload.TargetKind,
		TargetID:    payload.TargetID,
		Familiarity: payload.Familiarity,
		Sentiment:   payload.Sentiment,
		Note:        payload.Note,
		State:       "candidate",
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	stored, existing, err := w.lookup.insertRuntimeMoviePreferenceCandidate(ctx, candidate)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if existing {
		if !runtimeMoviePreferenceCandidateBindingEqual(stored, candidate) {
			return runtimeDataWriteOwnerResult{}, fmt.Errorf("movie preference candidate idempotency binding mismatch")
		}
		return runtimeMoviePreferenceCandidateOwnerResult(stored.ID, scope.RequestID, true), nil
	}
	return runtimeMoviePreferenceCandidateOwnerResult(candidate.ID, scope.RequestID, false), nil
}

func runtimeMoviePreferenceCandidateOwnerResult(candidateID, requestID string, replay bool) runtimeDataWriteOwnerResult {
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "movie-preference-candidate/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         candidateID,
		IdempotencyKey:   requestID,
		IdempotentReplay: replay,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}
}

func decodeRuntimeMoviePreferenceCandidatePayload(payload map[string]any) (runtimeMoviePreferenceCandidateWritePayload, string, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"target_kind": {}, "target_id": {}, "familiarity": {}, "sentiment": {}, "note": {},
	}); err != nil {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", err
	}
	var decoded runtimeMoviePreferenceCandidateWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", err
	}
	decoded.TargetKind = strings.ToLower(strings.TrimSpace(decoded.TargetKind))
	decoded.TargetID = strings.TrimSpace(decoded.TargetID)
	decoded.Familiarity = strings.ToLower(strings.TrimSpace(decoded.Familiarity))
	decoded.Sentiment = strings.ToLower(strings.TrimSpace(decoded.Sentiment))
	decoded.Note = strings.TrimSpace(decoded.Note)
	if err := validateRuntimeMoviePreferenceText("target_kind", decoded.TargetKind, 16, false); err != nil {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", err
	}
	if decoded.TargetKind != "movie" && decoded.TargetKind != "person" {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", fmt.Errorf("target_kind must be movie or person")
	}
	if err := validateRuntimeMoviePreferenceText("target_id", decoded.TargetID, runtimeMoviePreferenceTargetMaxRunes, false); err != nil {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", err
	}
	if decoded.Familiarity != "" && decoded.Familiarity != "known" && decoded.Familiarity != "unknown" {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", fmt.Errorf("familiarity must be known, unknown, or empty")
	}
	if decoded.Sentiment != "" && decoded.Sentiment != "like" && decoded.Sentiment != "neutral" && decoded.Sentiment != "dislike" {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", fmt.Errorf("sentiment must be like, neutral, dislike, or empty")
	}
	if decoded.Familiarity == "" && decoded.Sentiment == "" {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", fmt.Errorf("familiarity or sentiment is required")
	}
	if err := validateRuntimeMoviePreferenceText("familiarity", decoded.Familiarity, 16, true); err != nil {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", err
	}
	if err := validateRuntimeMoviePreferenceText("sentiment", decoded.Sentiment, 16, true); err != nil {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", err
	}
	if err := validateRuntimeMoviePreferenceText("note", decoded.Note, runtimeMoviePreferenceNoteMaxRunes, true); err != nil {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", err
	}
	encoded, err := json.Marshal(decoded)
	if err != nil {
		return runtimeMoviePreferenceCandidateWritePayload{}, "", fmt.Errorf("canonicalize movie preference candidate payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return decoded, hex.EncodeToString(digest[:]), nil
}

func validateRuntimeMoviePreferenceText(field, value string, maxRunes int, allowEmpty bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	// The common data.write boundary JSON-encodes model payloads before the
	// owner callback; encoding/json replaces malformed bytes with U+FFFD. Treat
	// that replacement marker as invalid here so malformed input cannot be
	// silently persisted as a different binding.
	if strings.ContainsRune(value, utf8.RuneError) {
		return fmt.Errorf("%s contains an invalid UTF-8 replacement marker", field)
	}
	if !allowEmpty && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", field, maxRunes)
	}
	return nil
}

func runtimeMoviePreferenceCandidateBindingEqual(left, right runtimeMoviePreferenceCandidate) bool {
	return left.ID == right.ID && left.RequestID == right.RequestID && left.UserID == right.UserID && left.ActorID == right.ActorID && left.PayloadHash == right.PayloadHash && left.TargetKind == right.TargetKind && left.TargetID == right.TargetID && left.Familiarity == right.Familiarity && left.Sentiment == right.Sentiment && left.Note == right.Note && left.State == right.State
}
