package memory

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	ProfilePromotionPending   = "pending"
	ProfilePromotionRunning   = "running"
	ProfilePromotionRetryWait = "retry_wait"
	ProfilePromotionCompleted = "completed"
	ProfilePromotionFailed    = "failed"
)

const (
	ProfilePromotionProjectionLimit        = 32
	ProfilePromotionProjectionStatementMax = 512
	ProfilePromotionProjectionTotalMax     = 8192
	ProfilePromotionRawCandidateLimit      = 16
	// ProfilePromotionPerGroupCandidateLimit keeps each logical LLM request
	// small while the merged extraction remains capped by RawCandidateLimit.
	ProfilePromotionPerGroupCandidateLimit = 4
	// ProfilePromotionRepairCandidateLimit is the stricter cap used only by the
	// one corrective generation after an invalid provider response.
	ProfilePromotionRepairCandidateLimit = 2
	ProfilePromotionPreferenceKeyMax     = 64
	ProfilePromotionPreferenceValueMax   = 448
	ProfilePromotionResponseBytesMax     = 64 * 1024
	// ProfilePromotionMaxTokens is the provider-independent completion budget
	// for one logical extraction request. It is not a physical model context
	// or provider setting.
	ProfilePromotionMaxTokens = 1024
	// ProfilePromotionRepairMaxTokens is the provider-independent completion
	// budget for the bounded corrective generation.
	ProfilePromotionRepairMaxTokens = 512
	// ProfilePromotionRepairStringMax bounds every string in a repaired
	// candidate payload, including facts and preference values.
	ProfilePromotionRepairStringMax = 100
	// ProfilePromotionEvidenceBlockMax bounds one extraction request. Evidence
	// longer than this is split into several requests instead of being cut,
	// so the tail of a long turn still reaches the extractor.
	ProfilePromotionEvidenceBlockMax = 3000
	// ProfilePromotionEvidenceGroupMax bounds how many extraction requests one
	// batch may cost. Beyond this the remainder is dropped, and the extractor
	// reports the dropped amount rather than passing it over in silence.
	// A long original draft pasted twice across Canvas edits needs the higher
	// ceiling to be covered end to end.
	ProfilePromotionEvidenceGroupMax = 12
	// ProfilePromotionMaterialLeadMax is the longest opening paragraph still
	// read as the user's own turn in front of pasted material. A longer opening
	// means the user is writing, not introducing a quote.
	ProfilePromotionMaterialLeadMax = 200
	// ProfilePromotionMaterialBodyMin is the shortest trailing block treated as
	// pasted material rather than the continuation of the user's own turn.
	ProfilePromotionMaterialBodyMin = 800
	// ProfilePromotionMaterialExcerptMax bounds one material excerpt. Material
	// only has to identify what the turn was about, so a head excerpt is
	// enough and its assertions never become user facts.
	ProfilePromotionMaterialExcerptMax = 500
	// ProfilePromotionMaterialDigestMax bounds all material excerpts in one
	// extraction request.
	ProfilePromotionMaterialDigestMax = 800
	// ProfilePromotionExistingContextMax bounds the known profile rendered into
	// one request. The extractor keeps complete deterministic lines only.
	ProfilePromotionExistingContextMax = 800
	// The prompt limits are logical CORE budgets. PromptInstructionMax covers
	// fixed labels/instructions and RepairInstructionMax covers the one fixed
	// corrective message appended to a repair request. They do not describe a
	// physical model n_ctx.
	ProfilePromotionPromptInstructionMax = 2048
	ProfilePromotionRepairInstructionMax = 512
	ProfilePromotionInitialPromptMax     = ProfilePromotionEvidenceBlockMax +
		ProfilePromotionExistingContextMax + ProfilePromotionMaterialDigestMax +
		ProfilePromotionPromptInstructionMax
	ProfilePromotionPromptMax = ProfilePromotionInitialPromptMax + ProfilePromotionRepairInstructionMax
	// ProfilePromotionEvidenceTagDensityPercent is the share of characters
	// inside angle brackets above which a turn is treated as a markup dump.
	// Pasted Confluence and web bodies sit near 75%; a turn that merely
	// discusses HTML stays far below it and keeps its markup.
	ProfilePromotionEvidenceTagDensityPercent = 30
)

type ProfilePromotionJob struct {
	EvidenceEventID string    `json:"evidence_event_id"`
	SessionID       string    `json:"session_id"`
	ThreadID        int64     `json:"thread_id"`
	State           string    `json:"state"`
	AttemptCount    int       `json:"attempt_count"`
	LeaseToken      string    `json:"-"`
	LeaseExpiresAt  time.Time `json:"lease_expires_at,omitempty"`
	NextAttemptAt   time.Time `json:"next_attempt_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// L1DBPoolStats is the cumulative sql.DB pool snapshot exposed with
// ProfilePromotion diagnostics. Wait counters are cumulative for the life of
// the database handle, as reported by database/sql.DB.Stats.
type L1DBPoolStats struct {
	Max                int   `json:"max"`
	Open               int   `json:"open"`
	InUse              int   `json:"in_use"`
	Idle               int   `json:"idle"`
	PoolWaitCount      int64 `json:"pool_wait_count"`
	PoolWaitDurationMS int64 `json:"pool_wait_duration_ms"`
}

// ProfilePromotionDiagnostics describes all persisted promotion jobs, while
// the handler may still return a limited page of job details.
type ProfilePromotionDiagnostics struct {
	StateCounts                map[string]int `json:"state_counts"`
	FailedCount                int            `json:"failed_count"`
	RetryableFailedCount       int            `json:"retryable_failed_count"`
	MissingEvidenceFailedCount int            `json:"missing_evidence_failed_count"`
	DBPoolStats                L1DBPoolStats  `json:"db_pool_stats"`
}

// ProfilePromotionRetryResult is the result of an explicit retry request.
// Missing-evidence rows are reported but remain terminal and untouched.
type ProfilePromotionRetryResult struct {
	RequeuedCount        int `json:"requeued_count"`
	MissingEvidenceCount int `json:"missing_evidence_count"`
}

type ProfilePromotionMessage struct {
	EventID   string
	SessionID string
	ThreadID  int64
	Text      string
	CreatedAt time.Time
}

type ProfilePromotionBatch struct {
	LeaseToken string
	SessionID  string
	ThreadID   int64
	Messages   []ProfilePromotionMessage
}

type ProfileCandidate struct {
	Type        string
	Statement   string
	Confidence  float64
	Sensitivity string
	Scope       string
}

// NormalizeProfilePromotionStatement returns the deterministic statement form
// used for candidate and existing-projection exact deduplication.
func NormalizeProfilePromotionStatement(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

// ProfilePromotionStatementKey is the CORE-owned deduplication key. Statement
// text is normalized and case-folded, while the semantic type remains exact.
func ProfilePromotionStatementKey(memoryType, statement string) string {
	return strings.TrimSpace(memoryType) + "\x00" + strings.ToLower(NormalizeProfilePromotionStatement(statement))
}

func validateProfilePromotionText(value string, maxRunes int, field string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s contains forbidden control characters", field)
	}
	if len([]rune(value)) > maxRunes {
		return fmt.Errorf("%s exceeds %d runes", field, maxRunes)
	}
	return nil
}

func validProfilePromotionConfidence(confidence float64) bool {
	return !math.IsNaN(confidence) && !math.IsInf(confidence, 0) && confidence > 0 && confidence <= 1
}

func validProfilePromotionSensitivity(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "normal", "sensitive":
		return true
	default:
		return false
	}
}

func validProfilePromotionScope(value string) bool {
	scope := strings.ToLower(strings.TrimSpace(value))
	if scope == "" || scope == "all" || scope == "all_personas" || scope == "global" {
		return true
	}
	for _, persona := range []string{"mio", "shiro", "kuro", "midori"} {
		if scope == persona || scope == persona+"_only" {
			return true
		}
	}
	return false
}

// ValidateProfilePromotionCandidate validates the complete candidate shape.
// Defaults are deliberately not applied here: CORE must decide every field.
func ValidateProfilePromotionCandidate(candidate ProfileCandidate) error {
	if candidate.Type != UserMemoryTypePreference && candidate.Type != UserMemoryTypeProfile {
		return fmt.Errorf("profile promotion candidate type must be preference or profile")
	}
	if candidate.Statement != NormalizeProfilePromotionStatement(candidate.Statement) {
		return fmt.Errorf("profile promotion candidate statement is not normalized")
	}
	if err := validateProfilePromotionText(candidate.Statement, ProfilePromotionProjectionStatementMax, "profile promotion candidate statement"); err != nil {
		return err
	}
	if candidate.Sensitivity != "normal" {
		return fmt.Errorf("profile promotion candidate sensitivity must be normal")
	}
	if candidate.Scope != "all_personas" {
		return fmt.Errorf("profile promotion candidate scope must be all_personas")
	}
	if !validProfilePromotionConfidence(candidate.Confidence) {
		return fmt.Errorf("profile promotion candidate confidence must be in (0,1]")
	}
	return nil
}

// ValidateProfilePromotionCandidates validates before persistence so a store
// never silently skips, defaults, truncates, or partially accepts a batch.
func ValidateProfilePromotionCandidates(candidates []ProfileCandidate) error {
	if len(candidates) > ProfilePromotionRawCandidateLimit {
		return fmt.Errorf("profile promotion candidate count exceeds %d", ProfilePromotionRawCandidateLimit)
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if err := ValidateProfilePromotionCandidate(candidate); err != nil {
			return err
		}
		key := ProfilePromotionStatementKey(candidate.Type, candidate.Statement)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate profile promotion candidate")
		}
		seen[key] = struct{}{}
	}
	return nil
}

// ValidateProfilePromotionBatchEvidence validates the evidence binding owned
// by the claimed batch. The store additionally verifies the active lease in
// the same transaction before committing candidates.
func ValidateProfilePromotionBatchEvidence(batch ProfilePromotionBatch) error {
	if strings.TrimSpace(batch.LeaseToken) == "" {
		return fmt.Errorf("profile promotion lease is required")
	}
	if len(batch.Messages) == 0 {
		return fmt.Errorf("profile promotion evidence batch is empty")
	}
	if strings.TrimSpace(batch.SessionID) == "" {
		return fmt.Errorf("profile promotion batch session id is required")
	}
	if batch.ThreadID <= 0 {
		return fmt.Errorf("profile promotion batch thread id is required")
	}
	seen := make(map[string]struct{}, len(batch.Messages))
	for _, item := range batch.Messages {
		evidenceID := strings.TrimSpace(item.EventID)
		if evidenceID == "" {
			return fmt.Errorf("profile promotion evidence event id is required")
		}
		if item.SessionID != batch.SessionID || item.ThreadID != batch.ThreadID {
			return fmt.Errorf("profile promotion evidence %q is not bound to batch", evidenceID)
		}
		if strings.TrimSpace(item.Text) == "" {
			return fmt.Errorf("profile promotion evidence %q message is required", evidenceID)
		}
		if _, exists := seen[evidenceID]; exists {
			return fmt.Errorf("profile promotion evidence event ids must be unique")
		}
		seen[evidenceID] = struct{}{}
	}
	return nil
}

// ValidateProfilePromotionProjection validates the bounded owner-scoped
// projection before it is rendered into the extractor's UserProfile.
func ValidateProfilePromotionProjection(items []UserMemory, userID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return fmt.Errorf("profile promotion projection user id is required")
	}
	if len(items) > ProfilePromotionProjectionLimit {
		return fmt.Errorf("profile promotion projection count exceeds %d", ProfilePromotionProjectionLimit)
	}
	namespace := NamespaceKindUser + ":" + userID
	totalRunes := 0
	for _, item := range items {
		if item.UserID != userID || item.Namespace != namespace {
			return fmt.Errorf("profile promotion projection owner is invalid")
		}
		if !item.Active {
			return fmt.Errorf("profile promotion projection contains inactive memory")
		}
		switch item.State {
		case MemoryStateCandidate, MemoryStateConfirmed, MemoryStatePinned:
		default:
			return fmt.Errorf("profile promotion projection state is invalid")
		}
		if err := ValidateUserMemoryType(item.Type); err != nil {
			return err
		}
		if err := validateProfilePromotionText(item.Statement, ProfilePromotionProjectionStatementMax, "profile promotion projection statement"); err != nil {
			return err
		}
		if !validProfilePromotionConfidence(item.Confidence) {
			return fmt.Errorf("profile promotion projection confidence is invalid")
		}
		if strings.TrimSpace(item.Sensitivity) == "" || strings.ContainsAny(item.Sensitivity, "\r\n\x00") || !validProfilePromotionSensitivity(item.Sensitivity) {
			return fmt.Errorf("profile promotion projection sensitivity is invalid")
		}
		if strings.TrimSpace(item.Scope) == "" || strings.ContainsAny(item.Scope, "\r\n\x00") || !validProfilePromotionScope(item.Scope) {
			return fmt.Errorf("profile promotion projection scope is invalid")
		}
		totalRunes += len([]rune(item.Statement))
		if totalRunes > ProfilePromotionProjectionTotalMax {
			return fmt.Errorf("profile promotion projection statements exceed %d runes", ProfilePromotionProjectionTotalMax)
		}
	}
	return nil
}
