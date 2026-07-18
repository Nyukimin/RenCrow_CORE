package verification

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type VerificationStatus string

const (
	StatusVerified        VerificationStatus = "verified"
	StatusWeaklySupported VerificationStatus = "weakly_supported"
	StatusUnsupported     VerificationStatus = "unsupported"
	StatusConflict        VerificationStatus = "conflict"
	StatusNotChecked      VerificationStatus = "not_checked"
)

func (s VerificationStatus) Valid() bool {
	switch s {
	case StatusVerified, StatusWeaklySupported, StatusUnsupported, StatusConflict, StatusNotChecked:
		return true
	default:
		return false
	}
}

type TriggerLevel string

const (
	TriggerLow    TriggerLevel = "low"
	TriggerMedium TriggerLevel = "medium"
	TriggerHigh   TriggerLevel = "high"
)

func (l TriggerLevel) Valid() bool {
	switch l {
	case TriggerLow, TriggerMedium, TriggerHigh:
		return true
	default:
		return false
	}
}

type EvidenceSourceType string

const (
	EvidenceRecallPack         EvidenceSourceType = "recall_pack"
	EvidenceConversationMemory EvidenceSourceType = "conversation_memory"
	EvidenceL1SQLite           EvidenceSourceType = "l1_sqlite"
	EvidenceVectorThreadMemory EvidenceSourceType = "vector_thread_memory"
	EvidenceVectorKB           EvidenceSourceType = "vector_kb"
	EvidenceSQLiteArchive      EvidenceSourceType = "sqlite_archive"
	EvidenceSourceRegistry     EvidenceSourceType = "source_registry"
	EvidenceSearchCache        EvidenceSourceType = "search_cache"
	EvidenceRawExternalSource  EvidenceSourceType = "raw_external_source"
	EvidenceExecutionReport    EvidenceSourceType = "execution_report"
)

func (t EvidenceSourceType) Valid() bool {
	// 旧リリースが保存した値は読み取り互換だけを維持する。
	if t == EvidenceSourceType("duckdb_archive") {
		return true
	}
	switch t {
	case EvidenceRecallPack, EvidenceConversationMemory, EvidenceL1SQLite, EvidenceVectorThreadMemory,
		EvidenceVectorKB, EvidenceSQLiteArchive, EvidenceSourceRegistry, EvidenceSearchCache,
		EvidenceRawExternalSource, EvidenceExecutionReport:
		return true
	default:
		return false
	}
}

type ErrorKind string

const (
	ErrorNone                  ErrorKind = ""
	ErrorVerifierDisabled      ErrorKind = "verifier_disabled"
	ErrorProviderUnavailable   ErrorKind = "provider_unavailable"
	ErrorProviderFailed        ErrorKind = "provider_failed"
	ErrorEvidenceUnavailable   ErrorKind = "evidence_unavailable"
	ErrorClaimExtractionFailed ErrorKind = "claim_extraction_failed"
	ErrorPersistenceFailed     ErrorKind = "persistence_failed"
)

type ClaimID string

type VerificationQuestionID string

type EvidenceRef struct {
	ID          string             `json:"id"`
	SourceType  EvidenceSourceType `json:"source_type"`
	SourceID    string             `json:"source_id,omitempty"`
	SourceURL   string             `json:"source_url,omitempty"`
	Field       string             `json:"field,omitempty"`
	Value       string             `json:"value,omitempty"`
	Note        string             `json:"note,omitempty"`
	RetrievedAt time.Time          `json:"retrieved_at,omitempty"`
	Supports    bool               `json:"supports"`
	Conflicts   bool               `json:"conflicts,omitempty"`
}

func (e EvidenceRef) Validate() error {
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("evidence id is required")
	}
	if !e.SourceType.Valid() {
		return fmt.Errorf("invalid evidence source type: %s", e.SourceType)
	}
	if e.Supports && e.Conflicts {
		return errors.New("evidence cannot both support and conflict")
	}
	return nil
}

type Claim struct {
	ID         ClaimID            `json:"id"`
	Text       string             `json:"text"`
	Priority   TriggerLevel       `json:"priority"`
	SourceHint string             `json:"source_hint,omitempty"`
	Status     VerificationStatus `json:"status"`
	Reason     string             `json:"reason,omitempty"`
	Evidence   []EvidenceRef      `json:"evidence,omitempty"`
}

func (c Claim) Validate() error {
	if strings.TrimSpace(string(c.ID)) == "" {
		return errors.New("claim id is required")
	}
	if strings.TrimSpace(c.Text) == "" {
		return errors.New("claim text is required")
	}
	if c.Priority != "" && !c.Priority.Valid() {
		return fmt.Errorf("invalid claim priority: %s", c.Priority)
	}
	if c.Status != "" && !c.Status.Valid() {
		return fmt.Errorf("invalid claim status: %s", c.Status)
	}
	for _, ev := range c.Evidence {
		if err := ev.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type VerificationQuestion struct {
	ID      VerificationQuestionID `json:"id"`
	ClaimID ClaimID                `json:"claim_id"`
	Query   string                 `json:"query"`
}

func (q VerificationQuestion) Validate() error {
	if strings.TrimSpace(string(q.ID)) == "" {
		return errors.New("verification question id is required")
	}
	if strings.TrimSpace(string(q.ClaimID)) == "" {
		return errors.New("verification question claim id is required")
	}
	if strings.TrimSpace(q.Query) == "" {
		return errors.New("verification question query is required")
	}
	return nil
}

type VerificationPolicy struct {
	Enabled bool         `json:"enabled"`
	Mode    string       `json:"mode,omitempty"`
	Default TriggerLevel `json:"default"`
}

func (p VerificationPolicy) Normalized() VerificationPolicy {
	if p.Default == "" {
		p.Default = TriggerLow
	}
	if strings.TrimSpace(p.Mode) == "" {
		p.Mode = "dry_run"
	}
	return p
}

type VerificationReport struct {
	ID               string                 `json:"id"`
	JobID            string                 `json:"job_id"`
	SessionID        string                 `json:"session_id"`
	Route            string                 `json:"route"`
	Status           VerificationStatus     `json:"status"`
	TriggerLevel     TriggerLevel           `json:"trigger_level"`
	ClaimCount       int                    `json:"claim_count"`
	VerifiedCount    int                    `json:"verified_count"`
	WeakCount        int                    `json:"weak_count"`
	UnsupportedCount int                    `json:"unsupported_count"`
	ConflictCount    int                    `json:"conflict_count"`
	NotCheckedCount  int                    `json:"not_checked_count"`
	Claims           []Claim                `json:"claims,omitempty"`
	Questions        []VerificationQuestion `json:"questions,omitempty"`
	Evidence         []EvidenceRef          `json:"evidence,omitempty"`
	ErrorKind        ErrorKind              `json:"error_kind,omitempty"`
	Error            string                 `json:"error,omitempty"`
	SkipReason       string                 `json:"skip_reason,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
}

func (r VerificationReport) Validate() error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("verification report id is required")
	}
	if strings.TrimSpace(r.JobID) == "" {
		return errors.New("verification report job_id is required")
	}
	if strings.TrimSpace(r.SessionID) == "" {
		return errors.New("verification report session_id is required")
	}
	if !r.Status.Valid() {
		return fmt.Errorf("invalid verification report status: %s", r.Status)
	}
	if r.TriggerLevel != "" && !r.TriggerLevel.Valid() {
		return fmt.Errorf("invalid verification report trigger level: %s", r.TriggerLevel)
	}
	if r.CreatedAt.IsZero() {
		return errors.New("verification report created_at is required")
	}
	for _, claim := range r.Claims {
		if err := claim.Validate(); err != nil {
			return err
		}
	}
	for _, question := range r.Questions {
		if err := question.Validate(); err != nil {
			return err
		}
	}
	for _, evidence := range r.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
	}
	return nil
}
