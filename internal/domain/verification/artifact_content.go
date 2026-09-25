package verification

import (
	"encoding/json"
	"fmt"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// verificationReportBody is the single declaration of what the content digest of a
// verification report covers. The report holds no single content string, so its content
// is the final verification result together with the counts the producer derived from the
// claims, the claims, the questions, the evidence references and the skip reason, encoded
// as compact JSON in this fixed key order (IDENTITY_CANONICAL Step13 follow-up). The
// status here is the verification result computed from the claims, so it is content: it
// is not the same thing as the revenue status, which is policy state.
//
// Identity (artifact_id, artifact_kind, task_id, session_id, route), the policy input
// (trigger_level), created_at, content_hash itself and superseded_by are excluded by
// construction, so reminting an ArtifactID or appending a supersession later never changes
// the digest and two reports holding the same body share one digest. error_kind and error
// are excluded because pipeline.go annotates the same report with that diagnostic after a
// store Save failure; the annotated report therefore still matches its own digest. What
// the annotation says is not protected by this contract. A new ArtifactID is never reused
// as a hash.
//
// These fields carry no omitempty tag, so a missing top-level list would be written as
// null and an empty one as [], and the contract is that a missing list and an empty list
// are the same content: VerificationReportBodyBytes normalizes nil to an empty list here
// rather than leaving it to the encoder. Nested Claim.Evidence keeps its existing tag, and
// is normalized the same way so a claim with no recorded evidence keeps one digest whether
// the producer wrote nothing or nothing was recorded for it. The nested element types are
// the domain types themselves, so a field added to Claim, VerificationQuestion or
// EvidenceRef enters the digest through this one declaration and the JSON tags stay in
// step with what the store and the Viewer write.
type verificationReportBody struct {
	Status           VerificationStatus     `json:"status"`
	ClaimCount       int                    `json:"claim_count"`
	VerifiedCount    int                    `json:"verified_count"`
	WeakCount        int                    `json:"weak_count"`
	UnsupportedCount int                    `json:"unsupported_count"`
	ConflictCount    int                    `json:"conflict_count"`
	NotCheckedCount  int                    `json:"not_checked_count"`
	Claims           []Claim                `json:"claims"`
	Questions        []VerificationQuestion `json:"questions"`
	Evidence         []EvidenceRef          `json:"evidence"`
	SkipReason       string                 `json:"skip_reason"`
}

// digestedBody projects the report onto its content. It changes no element and no order and
// never writes through into the caller's slices: it copies a list before replacing a nil
// nested evidence list with an empty one.
func (item VerificationReport) digestedBody() verificationReportBody {
	return verificationReportBody{
		Status:           item.Status,
		ClaimCount:       item.ClaimCount,
		VerifiedCount:    item.VerifiedCount,
		WeakCount:        item.WeakCount,
		UnsupportedCount: item.UnsupportedCount,
		ConflictCount:    item.ConflictCount,
		NotCheckedCount:  item.NotCheckedCount,
		Claims:           digestedClaims(item.Claims),
		Questions:        digestedQuestions(item.Questions),
		Evidence:         digestedEvidence(item.Evidence),
		SkipReason:       item.SkipReason,
	}
}

func digestedClaims(claims []Claim) []Claim {
	if claims == nil {
		return []Claim{}
	}
	projected := make([]Claim, len(claims))
	copy(projected, claims)
	for i := range projected {
		projected[i].Evidence = digestedEvidence(projected[i].Evidence)
	}
	return projected
}

func digestedQuestions(questions []VerificationQuestion) []VerificationQuestion {
	if questions == nil {
		return []VerificationQuestion{}
	}
	projected := make([]VerificationQuestion, len(questions))
	copy(projected, questions)
	return projected
}

func digestedEvidence(evidence []EvidenceRef) []EvidenceRef {
	if evidence == nil {
		return []EvidenceRef{}
	}
	projected := make([]EvidenceRef, len(evidence))
	copy(projected, evidence)
	return projected
}

// VerificationReportBodyBytes returns the exact bytes that the content digest of a report
// covers. The digest form itself is owned by modules/core. EvidenceRef.RetrievedAt is a
// time.Time, and a year outside the RFC 3339 range cannot be encoded: the error is
// returned instead of panicking, and never replaced by empty bytes, so a caller cannot
// publish a digest of bytes that were never persisted. A stored content_hash is never filled
// in or overwritten on read back; recomputing the body here only serves validation, which
// compares the stored value against the body it was taken over.
func VerificationReportBodyBytes(item VerificationReport) ([]byte, error) {
	encoded, err := json.Marshal(item.digestedBody())
	if err != nil {
		return nil, fmt.Errorf("encode verification report body: %w", err)
	}
	return encoded, nil
}

// ComputeVerificationReportContentHash returns the canonical content digest of a report
// body, so a producer never invents the value by hand.
func ComputeVerificationReportContentHash(item VerificationReport) (string, error) {
	body, err := VerificationReportBodyBytes(item)
	if err != nil {
		return "", err
	}
	return modulecore.ContentHashOf(body), nil
}
