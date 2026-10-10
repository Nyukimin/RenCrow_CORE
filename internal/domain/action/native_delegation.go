package action

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

const NativeDelegationActionName = "native_harness.delegate"

type NativeDelegationMode string

const (
	NativeDelegationModeOpenStart NativeDelegationMode = "open_start"
	NativeDelegationModeResume    NativeDelegationMode = "resume"
)

// NativeMutationSlot identifies one idempotent Harness mutation owned by a
// native delegation Attempt.
type NativeMutationSlot string

const (
	NativeMutationSlotOpen   NativeMutationSlot = "open"
	NativeMutationSlotStart  NativeMutationSlot = "start"
	NativeMutationSlotResume NativeMutationSlot = "resume"
)

// NativeMutationStatus records whether the exact request bytes have been
// prepared, may have been delivered, or have a definite outcome.
type NativeMutationStatus string

const (
	NativeMutationStatusPrepared        NativeMutationStatus = "prepared"
	NativeMutationStatusDeliveryUnknown NativeMutationStatus = "delivery_unknown"
	NativeMutationStatusAccepted        NativeMutationStatus = "accepted"
	NativeMutationStatusRejected        NativeMutationStatus = "rejected"
)

// NativeMutation stores the exact first RPC params bytes. JSON encodes Payload
// and Result as base64, so the bytes survive persistence without normalization.
type NativeMutation struct {
	Key           string               `json:"key"`
	Payload       []byte               `json:"payload"`
	PayloadSHA256 string               `json:"payload_sha256"`
	Status        NativeMutationStatus `json:"status"`
	Result        []byte               `json:"result,omitempty"`
	ErrorCode     string               `json:"error_code,omitempty"`
}

// NativeObservation is bounded diagnostic state. It carries codes only, never
// event bodies, prompts, or RPC payloads.
type NativeObservation struct {
	Kind       string    `json:"kind"`
	Code       string    `json:"code,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// NativeDelegation is the CORE-owned durable state for one Harness handoff.
// The accepted OPS body remains in the Conversation owner; this stores only
// its optional compact reference.
type NativeDelegation struct {
	Mode                     NativeDelegationMode                    `json:"mode"`
	InputReference           *conversation.AcceptedOPSInputReference `json:"input_reference,omitempty"`
	ExpectedCriteriaRevision string                                  `json:"expected_criteria_revision,omitempty"`
	ResumeSource             *NativeDelegationResumeSource           `json:"resume_source,omitempty"`
	Open                     *NativeMutation                         `json:"open,omitempty"`
	Start                    *NativeMutation                         `json:"start,omitempty"`
	Resume                   *NativeMutation                         `json:"resume,omitempty"`
	RunResult                []byte                                  `json:"run_result,omitempty"`
	Proof                    *NativeDelegationProof                  `json:"proof,omitempty"`
	LastObservation          *NativeObservation                      `json:"last_observation,omitempty"`
}

// EffectiveMode interprets a historical record without mode as open/start
// only when it has no Resume provenance or mutation. It never changes the
// persisted record.
func (d NativeDelegation) EffectiveMode() NativeDelegationMode {
	if d.Mode != "" {
		return d.Mode
	}
	if d.ResumeSource != nil || d.Resume != nil {
		return ""
	}
	return NativeDelegationModeOpenStart
}

// NativeDelegationResumeSource is the persisted provenance for an explicit
// Resume. It points to the exact prior CORE Action/Attempt and child run.
type NativeDelegationResumeSource struct {
	ActionID        string  `json:"action_id"`
	AttemptID       string  `json:"attempt_id"`
	HarnessTaskID   string  `json:"harness_task_id"`
	ThreadID        string  `json:"thread_id"`
	RunID           string  `json:"run_id"`
	ReceiptID       string  `json:"receipt_id"`
	PreviousRunID   string  `json:"previous_run_id,omitempty"`
	CheckpointID    *string `json:"checkpoint_id,omitempty"`
	RunResultSHA256 string  `json:"run_result_sha256"`
}

const NativeDelegationProofVersion = 1

// NativeDelegationProof is the compact owner-read proof that binds a saved
// RunResult to the frozen Task criteria revision and sealed verifier evidence.
// Child Task/Run/Thread identities remain in the frozen StartResult.
type NativeDelegationProof struct {
	Version                  int                             `json:"version"`
	ExpectedCriteriaRevision string                          `json:"expected_criteria_revision"`
	RunResultSHA256          string                          `json:"run_result_sha256"`
	TerminalEventID          string                          `json:"terminal_event_id"`
	TerminalEventSeq         int64                           `json:"terminal_event_seq"`
	Evidence                 []NativeDelegationEvidenceProof `json:"evidence"`
}

type NativeDelegationEvidenceProof struct {
	EvidenceID        string `json:"evidence_id"`
	VerifierActionID  string `json:"verifier_action_id"`
	VerifierAttemptID string `json:"verifier_attempt_id"`
	CompletionEventID string `json:"completion_event_id"`
	RawHash           string `json:"raw_hash"`
	TotalBytes        int64  `json:"total_bytes"`
}

func (p NativeDelegationProof) Validate() error {
	if p.Version != NativeDelegationProofVersion || !validSHA256(p.ExpectedCriteriaRevision) || !validSHA256(p.RunResultSHA256) ||
		!validProofID(p.TerminalEventID) || p.TerminalEventSeq < 1 || len(p.Evidence) == 0 {
		return fmt.Errorf("native delegation proof header is invalid")
	}
	seen := make(map[string]struct{}, len(p.Evidence))
	for _, item := range p.Evidence {
		if !validProofID(item.EvidenceID) || !validProofID(item.VerifierActionID) || !validProofID(item.VerifierAttemptID) ||
			!validProofID(item.CompletionEventID) || !validSHA256(item.RawHash) || item.TotalBytes < 0 {
			return fmt.Errorf("native delegation evidence proof is invalid")
		}
		if _, exists := seen[item.EvidenceID]; exists {
			return fmt.Errorf("native delegation proof contains duplicate evidence")
		}
		seen[item.EvidenceID] = struct{}{}
	}
	return nil
}

func validProofID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= 256 && utf8.ValidString(value)
}

func (m NativeMutation) Validate() error {
	if strings.TrimSpace(m.Key) == "" || !utf8.ValidString(m.Key) || strings.TrimSpace(m.Key) != m.Key {
		return fmt.Errorf("native mutation key is required and must be trimmed UTF-8")
	}
	if len(m.Payload) == 0 {
		return fmt.Errorf("native mutation payload is required")
	}
	if !validSHA256(m.PayloadSHA256) {
		return fmt.Errorf("native mutation payload_sha256 is invalid")
	}
	hash := sha256.Sum256(m.Payload)
	if hex.EncodeToString(hash[:]) != m.PayloadSHA256 {
		return fmt.Errorf("native mutation payload hash does not match payload")
	}
	switch m.Status {
	case NativeMutationStatusPrepared, NativeMutationStatusDeliveryUnknown:
		if len(m.Result) != 0 || m.ErrorCode != "" {
			return fmt.Errorf("unresolved native mutation must not have an outcome")
		}
	case NativeMutationStatusAccepted:
		if len(m.Result) == 0 || m.ErrorCode != "" {
			return fmt.Errorf("accepted native mutation requires a result and no error code")
		}
	case NativeMutationStatusRejected:
		if !validBoundedCode(m.ErrorCode, 128) || len(m.Result) != 0 {
			return fmt.Errorf("rejected native mutation requires a bounded error code and no result")
		}
	default:
		return fmt.Errorf("invalid native mutation status: %s", m.Status)
	}
	return nil
}

func (d NativeDelegation) Validate() error {
	mode := d.EffectiveMode()
	if mode != NativeDelegationModeOpenStart && mode != NativeDelegationModeResume {
		return fmt.Errorf("native delegation mode is invalid")
	}
	if d.InputReference != nil {
		if err := d.InputReference.Validate(); err != nil {
			return fmt.Errorf("native delegation input reference is invalid: %w", err)
		}
	}
	if d.ExpectedCriteriaRevision != "" && !validSHA256(d.ExpectedCriteriaRevision) {
		return fmt.Errorf("native delegation expected criteria revision is invalid")
	}
	if d.ResumeSource != nil {
		if err := d.ResumeSource.Validate(); err != nil {
			return fmt.Errorf("native delegation resume source is invalid: %w", err)
		}
	}
	if d.Open != nil {
		if err := d.Open.Validate(); err != nil {
			return fmt.Errorf("native delegation open mutation is invalid: %w", err)
		}
	}
	if d.Start != nil {
		if err := d.Start.Validate(); err != nil {
			return fmt.Errorf("native delegation start mutation is invalid: %w", err)
		}
		if d.Open == nil || d.Open.Status != NativeMutationStatusAccepted || len(d.Open.Result) == 0 {
			return fmt.Errorf("native delegation start requires an accepted open result")
		}
	}
	if d.Resume != nil {
		if err := d.Resume.Validate(); err != nil {
			return fmt.Errorf("native delegation resume mutation is invalid: %w", err)
		}
	}
	switch mode {
	case NativeDelegationModeOpenStart:
		if d.ResumeSource != nil || d.Resume != nil {
			return fmt.Errorf("open/start delegation must not contain Resume state")
		}
	case NativeDelegationModeResume:
		if d.InputReference != nil || d.Open != nil || d.Start != nil || d.ResumeSource == nil {
			return fmt.Errorf("Resume delegation requires only its source and Resume mutation")
		}
	}
	if len(d.RunResult) != 0 {
		if mode == NativeDelegationModeOpenStart && (d.Start == nil || d.Start.Status != NativeMutationStatusAccepted) {
			return fmt.Errorf("native delegation run result requires an accepted start")
		}
		if mode == NativeDelegationModeResume && (d.Resume == nil || d.Resume.Status != NativeMutationStatusAccepted) {
			return fmt.Errorf("native delegation run result requires an accepted Resume")
		}
	}
	if d.Proof != nil {
		if len(d.RunResult) == 0 || d.ExpectedCriteriaRevision == "" {
			return fmt.Errorf("native delegation proof requires a RunResult and frozen criteria revision")
		}
		if err := d.Proof.Validate(); err != nil {
			return err
		}
		if d.Proof.ExpectedCriteriaRevision != d.ExpectedCriteriaRevision {
			return fmt.Errorf("native delegation proof criteria revision differs from frozen Task revision")
		}
		hash := sha256.Sum256(d.RunResult)
		if hex.EncodeToString(hash[:]) != d.Proof.RunResultSHA256 {
			return fmt.Errorf("native delegation proof RunResult hash does not match stored bytes")
		}
	}
	if d.LastObservation != nil {
		if err := d.LastObservation.Validate(); err != nil {
			return err
		}
	}
	return nil
}

func (o NativeObservation) Validate() error {
	if !validBoundedCode(o.Kind, 64) || !validBoundedCode(o.Code, 128) && o.Code != "" {
		return fmt.Errorf("native observation codes must be bounded UTF-8 identifiers")
	}
	if o.ObservedAt.IsZero() {
		return fmt.Errorf("native observation observed_at is required")
	}
	return nil
}

func (d NativeDelegation) Clone() NativeDelegation {
	if d.InputReference != nil {
		reference := *d.InputReference
		d.InputReference = &reference
	}
	if d.Open != nil {
		mutation := cloneNativeMutation(*d.Open)
		d.Open = &mutation
	}
	if d.Start != nil {
		mutation := cloneNativeMutation(*d.Start)
		d.Start = &mutation
	}
	if d.ResumeSource != nil {
		source := *d.ResumeSource
		if source.CheckpointID != nil {
			checkpoint := *source.CheckpointID
			source.CheckpointID = &checkpoint
		}
		d.ResumeSource = &source
	}
	if d.Resume != nil {
		mutation := cloneNativeMutation(*d.Resume)
		d.Resume = &mutation
	}
	d.RunResult = cloneBytes(d.RunResult)
	if d.Proof != nil {
		proof := *d.Proof
		proof.Evidence = append([]NativeDelegationEvidenceProof(nil), d.Proof.Evidence...)
		d.Proof = &proof
	}
	if d.LastObservation != nil {
		observation := *d.LastObservation
		d.LastObservation = &observation
	}
	return d
}

func (s NativeDelegationResumeSource) Validate() error {
	for name, value := range map[string]string{
		"action_id": s.ActionID, "attempt_id": s.AttemptID, "harness_task_id": s.HarnessTaskID,
		"thread_id": s.ThreadID, "run_id": s.RunID, "receipt_id": s.ReceiptID,
	} {
		if !validProofID(value) {
			return fmt.Errorf("resume source %s is invalid", name)
		}
	}
	if s.PreviousRunID != "" && !validProofID(s.PreviousRunID) || s.CheckpointID != nil && !validProofID(*s.CheckpointID) || !validSHA256(s.RunResultSHA256) {
		return fmt.Errorf("resume source child provenance is invalid")
	}
	return nil
}

func cloneNativeMutation(m NativeMutation) NativeMutation {
	m.Payload = cloneBytes(m.Payload)
	m.Result = cloneBytes(m.Result)
	return m
}

func cloneBytes(value []byte) []byte {
	if value == nil {
		return nil
	}
	return append([]byte(nil), value...)
}

func validSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validBoundedCode(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
