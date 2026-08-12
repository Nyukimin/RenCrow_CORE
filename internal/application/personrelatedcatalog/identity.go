package personrelatedcatalog

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// IdentityStatus is the bounded state used by exact person identity mapping.
// Candidate evidence is intentionally not a confirmed mapping.
type IdentityStatus string

const (
	IdentityStatusConfirmed  IdentityStatus = "confirmed"
	IdentityStatusCandidate  IdentityStatus = "candidate"
	IdentityStatusAmbiguous  IdentityStatus = "ambiguous"
	IdentityStatusUnresolved IdentityStatus = "unresolved"
)

var ErrIdentityAmbiguous = errors.New("person identity is ambiguous")

// IdentityEvidence is an exact cross-reference or a bounded candidate
// observation. ExternalID is never synthesized from CandidateName.
type IdentityEvidence struct {
	EvidenceID       string         `json:"evidence_id,omitempty"`
	PersonID         string         `json:"person_id"`
	PersonRefID      string         `json:"person_ref_id,omitempty"`
	Authority        string         `json:"authority"`
	ExternalID       string         `json:"external_id,omitempty"`
	CandidateName    string         `json:"candidate_name,omitempty"`
	CanonicalURL     string         `json:"canonical_url,omitempty"`
	State            IdentityStatus `json:"state"`
	EvidenceSource   string         `json:"evidence_source"`
	EvidenceURL      string         `json:"evidence_url"`
	RetrievedAt      string         `json:"retrieved_at"`
	MatchedFields    []string       `json:"matched_fields,omitempty"`
	ConflictedFields []string       `json:"conflicted_fields,omitempty"`
	Reason           string         `json:"reason,omitempty"`
}

// IdentityMapping is the normalized exact-ID projection.
type IdentityMapping struct {
	PersonID       string         `json:"person_id"`
	Authority      string         `json:"authority"`
	ExternalID     string         `json:"external_id"`
	CanonicalURL   string         `json:"canonical_url"`
	State          IdentityStatus `json:"state"`
	EvidenceSource string         `json:"evidence_source"`
	EvidenceURL    string         `json:"evidence_url"`
	RetrievedAt    string         `json:"retrieved_at"`
	Reason         string         `json:"reason,omitempty"`
}

// IdentityResolution is returned for an exact authority/ID or one person.
// It is semantic and does not turn unresolved/ambiguous identity into an
// internal transport error.
type IdentityResolution struct {
	Status     IdentityStatus     `json:"status"`
	PersonID   string             `json:"person_id,omitempty"`
	Authority  string             `json:"authority,omitempty"`
	ExternalID string             `json:"external_id,omitempty"`
	Mappings   []IdentityMapping  `json:"mappings,omitempty"`
	Candidates []IdentityEvidence `json:"candidates,omitempty"`
	Reason     string             `json:"reason,omitempty"`
}

type IdentitySchedule struct {
	Allowed      bool              `json:"allowed"`
	Status       IdentityStatus    `json:"status"`
	PersonID     string            `json:"person_id"`
	Reason       string            `json:"reason,omitempty"`
	ConfirmedIDs map[string]string `json:"confirmed_ids,omitempty"`
}

func validIdentityStatus(status IdentityStatus) bool {
	switch status {
	case IdentityStatusConfirmed, IdentityStatusCandidate, IdentityStatusAmbiguous, IdentityStatusUnresolved:
		return true
	default:
		return false
	}
}

func normalizeIdentityAuthority(authority string) string {
	// Keep the wire authority names stable. In particular, the provider
	// contract distinguishes wikidata_qid and ndl_authority_uri; collapsing
	// either into a generic authority would lose dispatch information.
	return strings.ToLower(strings.TrimSpace(authority))
}

func normalizeIdentityEvidence(evidence IdentityEvidence) (IdentityEvidence, error) {
	evidence.PersonID = strings.TrimSpace(evidence.PersonID)
	if evidence.PersonID == "" {
		evidence.PersonID = strings.TrimSpace(evidence.PersonRefID)
	}
	evidence.PersonRefID = strings.TrimSpace(evidence.PersonRefID)
	evidence.Authority = normalizeIdentityAuthority(evidence.Authority)
	evidence.ExternalID = strings.TrimSpace(evidence.ExternalID)
	evidence.CandidateName = strings.TrimSpace(evidence.CandidateName)
	evidence.CanonicalURL = strings.TrimSpace(evidence.CanonicalURL)
	evidence.EvidenceSource = strings.ToLower(strings.TrimSpace(evidence.EvidenceSource))
	evidence.EvidenceURL = strings.TrimSpace(evidence.EvidenceURL)
	evidence.RetrievedAt = strings.TrimSpace(evidence.RetrievedAt)
	evidence.Reason = strings.TrimSpace(evidence.Reason)
	if evidence.State == "" {
		if evidence.ExternalID != "" {
			evidence.State = IdentityStatusConfirmed
		} else {
			evidence.State = IdentityStatusCandidate
		}
	}
	if evidence.PersonID == "" || evidence.Authority == "" || !validIdentityStatus(evidence.State) || evidence.EvidenceSource == "" || !validHTTPURL(evidence.EvidenceURL) {
		return IdentityEvidence{}, fmt.Errorf("%w: identity evidence required fields are invalid", ErrInvalidArtifact)
	}
	if evidence.RetrievedAt == "" {
		return IdentityEvidence{}, fmt.Errorf("%w: identity retrieved_at is required", ErrInvalidArtifact)
	}
	if _, err := time.Parse(time.RFC3339, evidence.RetrievedAt); err != nil {
		return IdentityEvidence{}, fmt.Errorf("%w: identity retrieved_at must be RFC3339", ErrInvalidArtifact)
	}
	if evidence.State == IdentityStatusConfirmed && evidence.ExternalID == "" {
		return IdentityEvidence{}, fmt.Errorf("%w: confirmed identity requires an exact external_id", ErrInvalidArtifact)
	}
	if evidence.State == IdentityStatusCandidate && evidence.ExternalID == "" && evidence.CandidateName == "" {
		return IdentityEvidence{}, fmt.Errorf("%w: candidate identity requires a name or exact external_id", ErrInvalidArtifact)
	}
	if evidence.ExternalID != "" && evidence.CanonicalURL == "" {
		return IdentityEvidence{}, fmt.Errorf("%w: exact identity requires canonical_url", ErrInvalidArtifact)
	}
	if evidence.CanonicalURL != "" && !validHTTPURL(evidence.CanonicalURL) {
		return IdentityEvidence{}, fmt.Errorf("%w: identity canonical_url is invalid", ErrInvalidArtifact)
	}
	evidence.MatchedFields = normalizeIdentityFields(evidence.MatchedFields)
	evidence.ConflictedFields = normalizeIdentityFields(evidence.ConflictedFields)
	if evidence.State == IdentityStatusConfirmed && !hasIndependentIdentityEvidence(evidence.MatchedFields) {
		// A caller/provider cannot promote an ID using a name-only assertion.
		// Keep the exact candidate for later evidence, but do not create the
		// normalized mapping or allow collection scheduling.
		evidence.State = IdentityStatusCandidate
	}
	if evidence.EvidenceID == "" {
		evidence.EvidenceID = identityEvidenceID(evidence)
	}
	return evidence, nil
}

func hasIndependentIdentityEvidence(fields []string) bool {
	for _, field := range fields {
		switch field {
		case "", "name", "normalized_name", "person_name":
			continue
		default:
			return true
		}
	}
	return false
}

func normalizeIdentityFields(fields []string) []string {
	seen := make(map[string]struct{}, len(fields))
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.ToLower(strings.TrimSpace(field))
		if field == "" {
			continue
		}
		if _, exists := seen[field]; exists {
			continue
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	sort.Strings(result)
	return result
}

func identityEvidenceID(evidence IdentityEvidence) string {
	value := strings.Join([]string{
		evidence.PersonID, evidence.Authority, evidence.ExternalID,
		strings.ToLower(evidence.CandidateName), evidence.EvidenceSource,
		evidence.EvidenceURL, evidence.RetrievedAt,
	}, "\x00")
	sum := sha256.Sum256([]byte(value))
	return "identity-" + hex.EncodeToString(sum[:])
}

// UpsertIdentityEvidence records exact evidence or a candidate. Name-only
// evidence is stored only in the evidence table and cannot create a mapping.
func UpsertIdentityEvidence(ctx context.Context, db *sql.DB, evidence IdentityEvidence) (IdentityResolution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return IdentityResolution{}, err
	}
	normalized, err := normalizeIdentityEvidence(evidence)
	if err != nil {
		return IdentityResolution{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return IdentityResolution{}, fmt.Errorf("begin identity evidence: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()
	resolution, err := upsertIdentityEvidenceTx(ctx, tx, normalized)
	if err != nil {
		return IdentityResolution{}, err
	}
	if err := tx.Commit(); err != nil {
		return IdentityResolution{}, fmt.Errorf("commit identity evidence: %w", err)
	}
	rollback = false
	return resolution, nil
}

func RecordIdentityEvidence(ctx context.Context, db *sql.DB, evidence IdentityEvidence) (IdentityResolution, error) {
	return UpsertIdentityEvidence(ctx, db, evidence)
}

func RecordIdentityCandidate(ctx context.Context, db *sql.DB, evidence IdentityEvidence) (IdentityResolution, error) {
	evidence.State = IdentityStatusCandidate
	evidence.ExternalID = ""
	return UpsertIdentityEvidence(ctx, db, evidence)
}

func upsertIdentityEvidenceTx(ctx context.Context, tx *sql.Tx, evidence IdentityEvidence) (IdentityResolution, error) {
	matchedJSON, err := json.Marshal(evidence.MatchedFields)
	if err != nil {
		return IdentityResolution{}, fmt.Errorf("encode identity matched fields: %w", err)
	}
	conflictedJSON, err := json.Marshal(evidence.ConflictedFields)
	if err != nil {
		return IdentityResolution{}, fmt.Errorf("encode identity conflicted fields: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO hobby_person_identity_evidence(
  evidence_id,person_id,authority,candidate_id,normalized_name,state,
  matched_fields_json,conflicted_fields_json,evidence_source,evidence_url,retrieved_at,reason,created_at,updated_at
)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)
ON CONFLICT(evidence_id) DO UPDATE SET
  state=excluded.state,
  matched_fields_json=excluded.matched_fields_json,
  conflicted_fields_json=excluded.conflicted_fields_json,
  reason=excluded.reason,
  updated_at=CURRENT_TIMESTAMP`,
		evidence.EvidenceID, evidence.PersonID, evidence.Authority, evidence.ExternalID,
		normalizedIdentityName(evidence.CandidateName), string(evidence.State), string(matchedJSON),
		string(conflictedJSON), evidence.EvidenceSource, evidence.EvidenceURL, evidence.RetrievedAt,
		evidence.Reason); err != nil {
		return IdentityResolution{}, fmt.Errorf("record identity evidence: %w", err)
	}
	if evidence.State != IdentityStatusConfirmed || evidence.ExternalID == "" {
		return resolvePersonIdentityTx(ctx, tx, evidence.PersonID)
	}
	return upsertConfirmedMappingTx(ctx, tx, evidence)
}

func resolveIdentityMappingTx(ctx context.Context, tx *sql.Tx, authority, externalID string) (IdentityResolution, error) {
	var mapping IdentityMapping
	var state string
	err := tx.QueryRowContext(ctx, `
SELECT person_id,authority,external_id,canonical_url,state,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_external_ids INDEXED BY idx_hobby_person_external_ids_authority_external
WHERE authority=? AND external_id=? LIMIT 1`, authority, externalID).Scan(
		&mapping.PersonID, &mapping.Authority, &mapping.ExternalID, &mapping.CanonicalURL,
		&state, &mapping.EvidenceSource, &mapping.EvidenceURL, &mapping.RetrievedAt, &mapping.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		rows, queryErr := tx.QueryContext(ctx, `
SELECT evidence_id,person_id,authority,candidate_id,normalized_name,state,matched_fields_json,conflicted_fields_json,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_identity_evidence INDEXED BY idx_hobby_person_identity_evidence_authority_candidate
WHERE authority=? AND candidate_id=? ORDER BY person_id,retrieved_at`, authority, externalID)
		if queryErr != nil {
			return IdentityResolution{}, fmt.Errorf("list exact identity evidence: %w", queryErr)
		}
		candidates, scanErr := scanIdentityEvidenceRows(rows)
		closeErr := rows.Close()
		if scanErr != nil {
			return IdentityResolution{}, scanErr
		}
		if closeErr != nil {
			return IdentityResolution{}, fmt.Errorf("close exact identity evidence: %w", closeErr)
		}
		status := IdentityStatusUnresolved
		if len(candidates) > 1 {
			status = IdentityStatusAmbiguous
		}
		return IdentityResolution{Status: status, Authority: authority, ExternalID: externalID, Candidates: candidates, Reason: identityResolutionReason(status)}, nil
	}
	if err != nil {
		return IdentityResolution{}, fmt.Errorf("resolve identity mapping: %w", err)
	}
	mapping.State = IdentityStatus(state)
	rows, err := tx.QueryContext(ctx, `
SELECT evidence_id,person_id,authority,candidate_id,normalized_name,state,matched_fields_json,conflicted_fields_json,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_identity_evidence INDEXED BY idx_hobby_person_identity_evidence_authority_candidate
WHERE authority=? AND candidate_id=? ORDER BY person_id,retrieved_at`, authority, externalID)
	if err != nil {
		return IdentityResolution{}, fmt.Errorf("list exact identity evidence: %w", err)
	}
	candidates, scanErr := scanIdentityEvidenceRows(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return IdentityResolution{}, scanErr
	}
	if closeErr != nil {
		return IdentityResolution{}, fmt.Errorf("close exact identity evidence: %w", closeErr)
	}
	status := mapping.State
	if status == IdentityStatusConfirmed {
		for _, candidate := range candidates {
			if candidate.PersonID != mapping.PersonID && candidate.State != IdentityStatusCandidate {
				status = IdentityStatusAmbiguous
				break
			}
		}
	}
	return IdentityResolution{Status: status, PersonID: mapping.PersonID, Authority: authority, ExternalID: externalID, Mappings: []IdentityMapping{mapping}, Candidates: candidates, Reason: identityResolutionReason(status)}, nil
}

func upsertConfirmedMappingTx(ctx context.Context, tx *sql.Tx, evidence IdentityEvidence) (IdentityResolution, error) {
	var existing IdentityMapping
	var found bool
	var rowState string
	var err error
	err = tx.QueryRowContext(ctx, `
SELECT person_id,authority,external_id,canonical_url,state,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_external_ids INDEXED BY idx_hobby_person_external_ids_authority_external
WHERE authority=? AND external_id=? LIMIT 1`, evidence.Authority, evidence.ExternalID).Scan(
		&existing.PersonID, &existing.Authority, &existing.ExternalID, &existing.CanonicalURL,
		&rowState, &existing.EvidenceSource, &existing.EvidenceURL, &existing.RetrievedAt, &existing.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		found = false
	} else if err != nil {
		return IdentityResolution{}, fmt.Errorf("resolve exact identity mapping: %w", err)
	} else {
		found = true
		existing.State = IdentityStatus(rowState)
	}
	if found && existing.PersonID != evidence.PersonID {
		if err := markIdentityConflictTx(ctx, tx, evidence.Authority, evidence.ExternalID, "conflicting_person"); err != nil {
			return IdentityResolution{}, err
		}
		if err := markEvidenceConflictTx(ctx, tx, evidence.Authority, []string{evidence.ExternalID}, evidence.ExternalID, "conflicting_person"); err != nil {
			return IdentityResolution{}, err
		}
		return resolveIdentityMappingTx(ctx, tx, evidence.Authority, evidence.ExternalID)
	}

	conflictIDs, err := confirmedAuthorityIDsTx(ctx, tx, evidence.PersonID, evidence.Authority, evidence.ExternalID)
	if err != nil {
		return IdentityResolution{}, err
	}
	state := IdentityStatusConfirmed
	reason := evidence.Reason
	if len(conflictIDs) > 0 {
		state = IdentityStatusAmbiguous
		if reason == "" {
			reason = "multiple_exact_ids_same_authority"
		}
		for _, externalID := range conflictIDs {
			if err := markIdentityConflictTx(ctx, tx, evidence.Authority, externalID, reason); err != nil {
				return IdentityResolution{}, err
			}
		}
		if err := markEvidenceConflictTx(ctx, tx, evidence.Authority, conflictIDs, evidence.ExternalID, reason); err != nil {
			return IdentityResolution{}, err
		}
	}
	if found && existing.State == IdentityStatusAmbiguous {
		state = IdentityStatusAmbiguous
		if reason == "" {
			reason = existing.Reason
		}
	}
	if !found {
		_, err = tx.ExecContext(ctx, `
INSERT INTO hobby_person_external_ids(
  person_id,authority,external_id,canonical_url,state,evidence_source,evidence_url,retrieved_at,reason,created_at,updated_at
)
VALUES(?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`,
			evidence.PersonID, evidence.Authority, evidence.ExternalID, evidence.CanonicalURL,
			string(state), evidence.EvidenceSource, evidence.EvidenceURL, evidence.RetrievedAt, reason)
	} else {
		_, err = tx.ExecContext(ctx, `
UPDATE hobby_person_external_ids
SET canonical_url=?,state=?,evidence_source=?,evidence_url=?,retrieved_at=?,reason=?,updated_at=CURRENT_TIMESTAMP
WHERE authority=? AND external_id=?`,
			evidence.CanonicalURL, string(state), evidence.EvidenceSource, evidence.EvidenceURL,
			evidence.RetrievedAt, reason, evidence.Authority, evidence.ExternalID)
	}
	if err != nil {
		return IdentityResolution{}, fmt.Errorf("upsert normalized identity: %w", err)
	}
	return resolveIdentityMappingTx(ctx, tx, evidence.Authority, evidence.ExternalID)
}

func confirmedAuthorityIDsTx(ctx context.Context, tx *sql.Tx, personID, authority, exceptID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT external_id
FROM hobby_person_external_ids INDEXED BY idx_hobby_person_external_ids_person
WHERE person_id=? AND authority=? AND state=? AND external_id<>?
ORDER BY external_id LIMIT 20`, personID, authority, IdentityStatusConfirmed, exceptID)
	if err != nil {
		return nil, fmt.Errorf("list identity authority mappings: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var externalID string
		if err := rows.Scan(&externalID); err != nil {
			return nil, fmt.Errorf("scan identity authority mapping: %w", err)
		}
		ids = append(ids, externalID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read identity authority mappings: %w", err)
	}
	return ids, nil
}

func markIdentityConflictTx(ctx context.Context, tx *sql.Tx, authority, externalID, reason string) error {
	if _, err := tx.ExecContext(ctx, `
UPDATE hobby_person_external_ids
SET state=?,reason=?,updated_at=CURRENT_TIMESTAMP
WHERE authority=? AND external_id=?`, IdentityStatusAmbiguous, reason, authority, externalID); err != nil {
		return fmt.Errorf("mark identity mapping ambiguous: %w", err)
	}
	return nil
}

func markEvidenceConflictTx(ctx context.Context, tx *sql.Tx, authority string, oldIDs []string, newID, reason string) error {
	ids := append(append([]string{}, oldIDs...), newID)
	for _, externalID := range ids {
		rows, err := tx.QueryContext(ctx, `
SELECT evidence_id FROM hobby_person_identity_evidence INDEXED BY idx_hobby_person_identity_evidence_authority_candidate
WHERE authority=? AND candidate_id=?`, authority, externalID)
		if err != nil {
			return fmt.Errorf("find conflicting identity evidence: %w", err)
		}
		evidenceIDs := []string{}
		for rows.Next() {
			var evidenceID string
			if err := rows.Scan(&evidenceID); err != nil {
				_ = rows.Close()
				return fmt.Errorf("scan conflicting identity evidence: %w", err)
			}
			evidenceIDs = append(evidenceIDs, evidenceID)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close conflicting identity evidence: %w", err)
		}
		for _, evidenceID := range evidenceIDs {
			if _, err := tx.ExecContext(ctx, `UPDATE hobby_person_identity_evidence SET state=?,reason=?,updated_at=CURRENT_TIMESTAMP WHERE evidence_id=?`, IdentityStatusAmbiguous, reason, evidenceID); err != nil {
				return fmt.Errorf("mark identity evidence ambiguous: %w", err)
			}
		}
	}
	return nil
}

// ResolveIdentityMapping resolves one authority/external ID with exact
// equality predicates and a named index. Missing exact evidence is unresolved.
func ResolveIdentityMapping(ctx context.Context, db *sql.DB, authority, externalID string) (IdentityResolution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return IdentityResolution{}, err
	}
	authority = normalizeIdentityAuthority(authority)
	externalID = strings.TrimSpace(externalID)
	if authority == "" || externalID == "" {
		return IdentityResolution{}, fmt.Errorf("%w: identity authority and external_id are required", ErrInvalidArtifact)
	}
	return resolveIdentityMapping(ctx, db, authority, externalID)
}

func ResolveIdentity(ctx context.Context, db *sql.DB, authority, externalID string) (IdentityResolution, error) {
	return ResolveIdentityMapping(ctx, db, authority, externalID)
}

func resolveIdentityMapping(ctx context.Context, db *sql.DB, authority, externalID string) (IdentityResolution, error) {
	var mapping IdentityMapping
	var state string
	err := db.QueryRowContext(ctx, `
SELECT person_id,authority,external_id,canonical_url,state,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_external_ids INDEXED BY idx_hobby_person_external_ids_authority_external
WHERE authority=? AND external_id=? LIMIT 1`, authority, externalID).Scan(
		&mapping.PersonID, &mapping.Authority, &mapping.ExternalID, &mapping.CanonicalURL,
		&state, &mapping.EvidenceSource, &mapping.EvidenceURL, &mapping.RetrievedAt, &mapping.Reason)
	if errors.Is(err, sql.ErrNoRows) {
		candidates, candidateErr := listIdentityEvidenceByExactID(ctx, db, authority, externalID)
		if candidateErr != nil {
			return IdentityResolution{}, candidateErr
		}
		status := IdentityStatusUnresolved
		if len(candidates) > 1 {
			status = IdentityStatusAmbiguous
		}
		return IdentityResolution{Status: status, Authority: authority, ExternalID: externalID, Candidates: candidates, Reason: identityResolutionReason(status)}, nil
	}
	if err != nil {
		return IdentityResolution{}, fmt.Errorf("resolve identity mapping: %w", err)
	}
	mapping.State = IdentityStatus(state)
	candidates, err := listIdentityEvidenceByExactID(ctx, db, authority, externalID)
	if err != nil {
		return IdentityResolution{}, err
	}
	status := mapping.State
	if status == IdentityStatusConfirmed {
		for _, candidate := range candidates {
			if candidate.PersonID != mapping.PersonID && candidate.State != IdentityStatusCandidate {
				status = IdentityStatusAmbiguous
				break
			}
		}
	}
	return IdentityResolution{Status: status, PersonID: mapping.PersonID, Authority: authority, ExternalID: externalID, Mappings: []IdentityMapping{mapping}, Candidates: candidates, Reason: identityResolutionReason(status)}, nil
}

func listIdentityEvidenceByExactID(ctx context.Context, db *sql.DB, authority, externalID string) ([]IdentityEvidence, error) {
	rows, err := db.QueryContext(ctx, `
SELECT evidence_id,person_id,authority,candidate_id,normalized_name,state,matched_fields_json,conflicted_fields_json,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_identity_evidence INDEXED BY idx_hobby_person_identity_evidence_authority_candidate
WHERE authority=? AND candidate_id=? ORDER BY person_id,retrieved_at`, authority, externalID)
	if err != nil {
		return nil, fmt.Errorf("list exact identity evidence: %w", err)
	}
	defer rows.Close()
	return scanIdentityEvidenceRows(rows)
}

// ListIdentityMappings reads one person's normalized mappings through the
// person/authority/state index and caps the result at twenty.
func ListIdentityMappings(ctx context.Context, db *sql.DB, personID string, limit int) ([]IdentityMapping, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return nil, err
	}
	personID = strings.TrimSpace(personID)
	if personID == "" {
		return nil, fmt.Errorf("%w: identity person_id is required", ErrInvalidArtifact)
	}
	if limit < 1 || limit > 20 {
		return nil, ErrInvalidLimit
	}
	rows, err := db.QueryContext(ctx, `
SELECT person_id,authority,external_id,canonical_url,state,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_external_ids INDEXED BY idx_hobby_person_external_ids_person
WHERE person_id=? ORDER BY authority,external_id LIMIT ?`, personID, limit)
	if err != nil {
		return nil, fmt.Errorf("list person identity mappings: %w", err)
	}
	defer rows.Close()
	result := []IdentityMapping{}
	for rows.Next() {
		var mapping IdentityMapping
		var state string
		if err := rows.Scan(&mapping.PersonID, &mapping.Authority, &mapping.ExternalID, &mapping.CanonicalURL, &state, &mapping.EvidenceSource, &mapping.EvidenceURL, &mapping.RetrievedAt, &mapping.Reason); err != nil {
			return nil, fmt.Errorf("scan person identity mapping: %w", err)
		}
		mapping.State = IdentityStatus(state)
		result = append(result, mapping)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read person identity mappings: %w", err)
	}
	return result, nil
}

func ListPersonIdentityMappings(ctx context.Context, db *sql.DB, personID string, limit int) ([]IdentityMapping, error) {
	return ListIdentityMappings(ctx, db, personID, limit)
}

func ConfirmedIdentityIDs(ctx context.Context, db *sql.DB, personID string, limit int) (map[string]string, error) {
	mappings, err := ListIdentityMappings(ctx, db, personID, limit)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string)
	for _, mapping := range mappings {
		if mapping.State == IdentityStatusConfirmed {
			result[mapping.Authority] = mapping.ExternalID
		}
	}
	return result, nil
}

// ResolvePersonIdentity returns confirmed only when every stored exact mapping
// is non-conflicting and at least one confirmed mapping exists.
func ResolvePersonIdentity(ctx context.Context, db *sql.DB, personID string) (IdentityResolution, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := requireHobbySchema(ctx, db); err != nil {
		return IdentityResolution{}, err
	}
	personID = strings.TrimSpace(personID)
	if personID == "" {
		return IdentityResolution{}, fmt.Errorf("%w: identity person_id is required", ErrInvalidArtifact)
	}
	mappings, err := ListIdentityMappings(ctx, db, personID, 20)
	if err != nil {
		return IdentityResolution{}, err
	}
	candidates, err := listIdentityEvidenceByPerson(ctx, db, personID)
	if err != nil {
		return IdentityResolution{}, err
	}
	status := IdentityStatusUnresolved
	for _, mapping := range mappings {
		if mapping.State == IdentityStatusAmbiguous {
			status = IdentityStatusAmbiguous
			break
		}
		if mapping.State == IdentityStatusConfirmed {
			status = IdentityStatusConfirmed
		}
	}
	for _, candidate := range candidates {
		if candidate.State == IdentityStatusAmbiguous {
			status = IdentityStatusAmbiguous
			break
		}
	}
	return IdentityResolution{Status: status, PersonID: personID, Mappings: mappings, Candidates: candidates, Reason: identityResolutionReason(status)}, nil
}

func listIdentityEvidenceByPerson(ctx context.Context, db *sql.DB, personID string) ([]IdentityEvidence, error) {
	rows, err := db.QueryContext(ctx, `
SELECT evidence_id,person_id,authority,candidate_id,normalized_name,state,matched_fields_json,conflicted_fields_json,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_identity_evidence INDEXED BY idx_hobby_person_identity_evidence_candidate
WHERE person_id=? ORDER BY authority,candidate_id,retrieved_at LIMIT 20`, personID)
	if err != nil {
		return nil, fmt.Errorf("list person identity evidence: %w", err)
	}
	defer rows.Close()
	return scanIdentityEvidenceRows(rows)
}

func resolvePersonIdentityTx(ctx context.Context, tx *sql.Tx, personID string) (IdentityResolution, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT person_id,authority,external_id,canonical_url,state,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_external_ids INDEXED BY idx_hobby_person_external_ids_person
WHERE person_id=? ORDER BY authority,external_id LIMIT 20`, personID)
	if err != nil {
		return IdentityResolution{}, fmt.Errorf("list person identity mappings: %w", err)
	}
	mappings := []IdentityMapping{}
	for rows.Next() {
		var mapping IdentityMapping
		var state string
		if err := rows.Scan(&mapping.PersonID, &mapping.Authority, &mapping.ExternalID, &mapping.CanonicalURL, &state, &mapping.EvidenceSource, &mapping.EvidenceURL, &mapping.RetrievedAt, &mapping.Reason); err != nil {
			_ = rows.Close()
			return IdentityResolution{}, fmt.Errorf("scan person identity mapping: %w", err)
		}
		mapping.State = IdentityStatus(state)
		mappings = append(mappings, mapping)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return IdentityResolution{}, fmt.Errorf("read person identity mappings: %w", err)
	}
	if err := rows.Close(); err != nil {
		return IdentityResolution{}, fmt.Errorf("close person identity mappings: %w", err)
	}
	rows, err = tx.QueryContext(ctx, `
SELECT evidence_id,person_id,authority,candidate_id,normalized_name,state,matched_fields_json,conflicted_fields_json,evidence_source,evidence_url,retrieved_at,reason
FROM hobby_person_identity_evidence INDEXED BY idx_hobby_person_identity_evidence_candidate
WHERE person_id=? ORDER BY authority,candidate_id,retrieved_at LIMIT 20`, personID)
	if err != nil {
		return IdentityResolution{}, fmt.Errorf("list person identity evidence: %w", err)
	}
	candidates, scanErr := scanIdentityEvidenceRows(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return IdentityResolution{}, scanErr
	}
	if closeErr != nil {
		return IdentityResolution{}, fmt.Errorf("close person identity evidence: %w", closeErr)
	}
	status := IdentityStatusUnresolved
	for _, mapping := range mappings {
		if mapping.State == IdentityStatusAmbiguous {
			status = IdentityStatusAmbiguous
			break
		}
		if mapping.State == IdentityStatusConfirmed {
			status = IdentityStatusConfirmed
		}
	}
	for _, candidate := range candidates {
		if candidate.State == IdentityStatusAmbiguous {
			status = IdentityStatusAmbiguous
			break
		}
	}
	return IdentityResolution{Status: status, PersonID: personID, Mappings: mappings, Candidates: candidates, Reason: identityResolutionReason(status)}, nil
}

func scanIdentityEvidenceRows(rows *sql.Rows) ([]IdentityEvidence, error) {
	result := []IdentityEvidence{}
	for rows.Next() {
		var evidence IdentityEvidence
		var state, normalizedName, matchedJSON, conflictedJSON string
		if err := rows.Scan(&evidence.EvidenceID, &evidence.PersonID, &evidence.Authority, &evidence.ExternalID, &normalizedName, &state, &matchedJSON, &conflictedJSON, &evidence.EvidenceSource, &evidence.EvidenceURL, &evidence.RetrievedAt, &evidence.Reason); err != nil {
			return nil, fmt.Errorf("scan identity evidence: %w", err)
		}
		evidence.CandidateName = normalizedName
		evidence.State = IdentityStatus(state)
		if err := json.Unmarshal([]byte(matchedJSON), &evidence.MatchedFields); err != nil {
			return nil, fmt.Errorf("decode identity matched fields: %w", err)
		}
		if err := json.Unmarshal([]byte(conflictedJSON), &evidence.ConflictedFields); err != nil {
			return nil, fmt.Errorf("decode identity conflicted fields: %w", err)
		}
		result = append(result, evidence)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read identity evidence: %w", err)
	}
	return result, nil
}

func normalizedIdentityName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(name)), " "))
}

func identityResolutionReason(status IdentityStatus) string {
	switch status {
	case IdentityStatusConfirmed:
		return "confirmed_exact_evidence"
	case IdentityStatusAmbiguous:
		return "conflicting_exact_evidence"
	default:
		return "no_confirmed_exact_identity"
	}
}

// IdentityScheduleDecision is the fail-closed gate used before related
// collection. Candidate/unresolved/ambiguous people are never scheduled.
func IdentityScheduleDecision(ctx context.Context, db *sql.DB, personID string) (IdentitySchedule, error) {
	resolution, err := ResolvePersonIdentity(ctx, db, personID)
	if err != nil {
		return IdentitySchedule{}, err
	}
	confirmed, err := ConfirmedIdentityIDs(ctx, db, personID, 20)
	if err != nil {
		return IdentitySchedule{}, err
	}
	return IdentitySchedule{
		Allowed:      resolution.Status == IdentityStatusConfirmed,
		Status:       resolution.Status,
		PersonID:     personID,
		Reason:       resolution.Reason,
		ConfirmedIDs: confirmed,
	}, nil
}

func CanScheduleCollection(ctx context.Context, db *sql.DB, personID string) (bool, error) {
	decision, err := IdentityScheduleDecision(ctx, db, personID)
	return decision.Allowed, err
}

func upsertImportedIdentityMappingsTx(ctx context.Context, tx *sql.Tx, artifact parsedArtifact) error {
	authorities := make([]string, 0, len(artifact.Identity.ExternalIDs))
	for authority := range artifact.Identity.ExternalIDs {
		authorities = append(authorities, authority)
	}
	sort.Strings(authorities)
	for _, authority := range authorities {
		externalID := strings.TrimSpace(artifact.Identity.ExternalIDs[authority])
		normalizedAuthority := normalizeIdentityAuthority(authority)
		evidence, err := normalizeIdentityEvidence(IdentityEvidence{
			PersonID:       artifact.Identity.MovieCatalogPersonID,
			PersonRefID:    artifact.Identity.PersonRefID,
			Authority:      normalizedAuthority,
			ExternalID:     externalID,
			CanonicalURL:   artifact.Identity.EvidenceURL,
			State:          IdentityStatusConfirmed,
			EvidenceSource: artifact.Manifest.Source,
			EvidenceURL:    artifact.Identity.EvidenceURL,
			RetrievedAt:    artifact.Manifest.RetrievedAt,
			MatchedFields:  []string{"validated_artifact_identity"},
		})
		if err != nil {
			return fmt.Errorf("normalize imported identity: %w", err)
		}
		resolution, err := upsertIdentityEvidenceTx(ctx, tx, evidence)
		if err != nil {
			return err
		}
		if resolution.Status != IdentityStatusConfirmed {
			return fmt.Errorf("%w: %s/%s", ErrIdentityAmbiguous, normalizedAuthority, externalID)
		}
	}
	return nil
}
