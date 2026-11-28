package task

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const ResearchResultSchema = "rencrow.core.research-task-result.v1"
const ResearchExecutionBindingSchema = "rencrow.core.research-execution-binding.v1"

var ErrResearchExecutionBindingImmutable = errors.New("Research execution binding is immutable after the first Task save")

// ResearchExecutionBinding is a payload-free scope pin set by a trusted local
// Research Task creator. Agent-supplied inputs cannot create or change it.
type ResearchExecutionBinding struct {
	SchemaVersion string `json:"schema_version"`
	DataScope     string `json:"data_scope"`
	OwnerID       string `json:"owner_id"`
	OwnerRoute    string `json:"owner_route"`
}

func (binding ResearchExecutionBinding) Validate(task Task) error {
	if binding.SchemaVersion != ResearchExecutionBindingSchema || binding.DataScope != "user" ||
		domainsession.ValidateOwnerID(binding.OwnerID) != nil ||
		binding.OwnerRoute != "core.research.local" || task.Route != RouteResearch ||
		task.OwnerID != binding.OwnerID || task.Assignee != binding.OwnerID || task.PrivateAccess != nil {
		return errors.New("Research execution binding is invalid")
	}
	return nil
}

// ResearchResultUserOwner resolves the durable Task provenance that may bind a
// user-scoped Research result. It does not use Task.OwnerID, which remains the
// CORE execution owner for ordinary Agent Tasks.
func ResearchResultUserOwner(task Task) (string, bool) {
	if task.PrivateAccess != nil {
		if task.PrivateAccess.Validate() != nil || task.ResearchBinding != nil {
			return "", false
		}
		return task.PrivateAccess.AuthenticatedUserID, true
	}
	if task.ResearchBinding != nil {
		if task.ResearchBinding.Validate(task) != nil {
			return "", false
		}
		return task.ResearchBinding.OwnerID, true
	}
	if task.OriginPublication == nil || task.OriginPublication.Validate(task.OriginSessionID) != nil ||
		task.OriginPublication.Audience != conversation.SessionAudiencePrivate {
		return "", false
	}
	return task.OriginPublication.OwnerID, true
}

// ResearchResultDataScopeMatches is the shared immutable provenance check for
// public and authenticated-user Research results.
func ResearchResultDataScopeMatches(task Task, dataScope, ownerID string) bool {
	switch dataScope {
	case "public":
		return ownerID == "" && task.PrivateAccess == nil && task.ResearchBinding == nil &&
			(task.OriginPublication == nil || task.OriginPublication.Validate(task.OriginSessionID) == nil && task.OriginPublication.Audience != conversation.SessionAudiencePrivate)
	case "user":
		owner, ok := ResearchResultUserOwner(task)
		return ownerID != "" && ok && owner == ownerID
	default:
		return false
	}
}

type ResearchResultCounts struct {
	SearchCalls          int `json:"search_calls"`
	Candidates           int `json:"candidates"`
	FetchAttempts        int `json:"fetch_attempts"`
	DocumentsFetched     int `json:"documents_fetched"`
	LocalDocumentsReused int `json:"local_documents_reused,omitempty"`
	DocumentsAnalyzed    int `json:"documents_analyzed"`
	RecordsSaved         int `json:"records_saved"`
	BoardPostsCreated    int `json:"board_posts_created"`
	AnalysisCalls        int `json:"analysis_calls"`
	PaidExternalCalls    int `json:"paid_external_calls"`
}

type ResearchVerifiedReceipt struct {
	Owner       string    `json:"owner"`
	Route       string    `json:"route"`
	CanonicalID string    `json:"canonical_id"`
	Digest      string    `json:"digest"`
	VerifiedAt  time.Time `json:"verified_at"`
}

// ResearchResultProjection contains only deterministic Task facts. It must
// never contain the report, query, source URLs, or fetched source text.
type ResearchResultProjection struct {
	SchemaVersion            string                    `json:"schema_version"`
	TaskID                   modulecore.TaskID         `json:"task_id"`
	ExecutionRunID           modulecore.RunID          `json:"execution_run_id"`
	ResearchRunID            string                    `json:"research_run_id"`
	ActorID                  string                    `json:"actor_id"`
	DataScope                string                    `json:"data_scope"`
	OwnerID                  string                    `json:"owner_id,omitempty"`
	ResultDigest             string                    `json:"result_digest"`
	ArtifactRef              string                    `json:"artifact_ref,omitempty"`
	Status                   string                    `json:"status"`
	Counts                   ResearchResultCounts      `json:"counts"`
	ReceiptCount             int                       `json:"receipt_count"`
	ReceiptState             string                    `json:"receipt_state"`
	RequiredReceiptsComplete bool                      `json:"required_receipts_complete"`
	Missing                  []string                  `json:"missing,omitempty"`
	VerifiedReceiptRefs      []ResearchVerifiedReceipt `json:"verified_receipt_refs,omitempty"`
}

// TaskSummary returns the public facts suitable for the Task Summary field.
// It contains no query, source text, report, or source URL; report content stays
// in the separately owned Research artifact.
func (result ResearchResultProjection) TaskSummary() string {
	missing := "none"
	if len(result.Missing) > 0 {
		quoted := make([]string, 0, len(result.Missing))
		for _, item := range result.Missing {
			quoted = append(quoted, strconv.Quote(item))
		}
		missing = strings.Join(quoted, ",")
	}
	refs := make([]string, 0, 1+len(result.VerifiedReceiptRefs))
	if result.ArtifactRef != "" {
		refs = append(refs, "result="+strconv.Quote(result.ArtifactRef))
	}
	if result.ResearchRunID != "" {
		refs = append(refs, "research_run="+strconv.Quote(result.ResearchRunID))
	}
	for _, receipt := range result.VerifiedReceiptRefs {
		refs = append(refs, "receipt="+strconv.Quote(receipt.Route)+"/"+strconv.Quote(receipt.CanonicalID)+"/"+strconv.Quote(receipt.Digest))
	}
	if len(refs) == 0 {
		refs = append(refs, "none")
	}
	summary := fmt.Sprintf(
		"Research status=%s; sources_fetched=%d; documents_analyzed=%d; records_saved=%d; board_posts_created=%d; receipt_state=%s; verified_receipts=%d/%d; required_receipts_complete=%t; missing=%s; refs=%s",
		result.Status, result.Counts.DocumentsFetched, result.Counts.DocumentsAnalyzed,
		result.Counts.RecordsSaved, result.Counts.BoardPostsCreated, result.ReceiptState,
		len(result.VerifiedReceiptRefs), result.ReceiptCount, result.RequiredReceiptsComplete,
		missing, strings.Join(refs, ","),
	)
	if result.Counts.LocalDocumentsReused > 0 {
		summary += fmt.Sprintf("; local_documents_reused=%d; external_documents_acquired=%d", result.Counts.LocalDocumentsReused, result.Counts.DocumentsFetched-result.Counts.LocalDocumentsReused)
	}
	return summary
}

func (result ResearchResultProjection) Validate(task Task) error {
	if result.SchemaVersion != ResearchResultSchema || result.TaskID != task.TaskID || result.TaskID.Validate() != nil ||
		result.ExecutionRunID == "" || result.ExecutionRunID.Validate() != nil || !safeResearchFact(result.ResearchRunID) ||
		strings.TrimSpace(result.ActorID) == "" || strings.TrimSpace(result.ActorID) != result.ActorID || !validSHA256(result.ResultDigest) {
		return errors.New("research result projection identity is invalid")
	}
	if result.ArtifactRef != "" && !safeResearchFact(result.ArtifactRef) {
		return errors.New("research result artifact reference is invalid")
	}
	switch result.Status {
	case "accepted", "running", "waiting_input", "succeeded", "partial", "failed", "cancelled", "unknown":
	default:
		return errors.New("research result projection status is invalid")
	}
	switch result.DataScope {
	case "public":
		if !ResearchResultDataScopeMatches(task, result.DataScope, result.OwnerID) {
			return errors.New("public research result cannot bind a private Task")
		}
	case "user":
		if domainsession.ValidateOwnerID(result.OwnerID) != nil || !ResearchResultDataScopeMatches(task, result.DataScope, result.OwnerID) {
			return errors.New("user research result does not match Task owner scope")
		}
	default:
		return errors.New("research result data_scope is invalid")
	}
	counts := result.Counts
	if counts.SearchCalls < 0 || counts.Candidates < 0 || counts.FetchAttempts < 0 || counts.DocumentsFetched < 0 ||
		counts.DocumentsAnalyzed < 0 || counts.RecordsSaved < 0 || counts.BoardPostsCreated < 0 || counts.AnalysisCalls < 0 || counts.PaidExternalCalls < 0 {
		return errors.New("research result counts cannot be negative")
	}
	if counts.LocalDocumentsReused < 0 || counts.LocalDocumentsReused > counts.DocumentsFetched {
		return errors.New("research local reuse count exceeds the acquired document count")
	}
	if result.ReceiptCount < 0 || result.ReceiptCount < len(result.VerifiedReceiptRefs) {
		return errors.New("research receipt count is invalid")
	}
	switch result.ReceiptState {
	case "none":
		if result.ReceiptCount != 0 || len(result.VerifiedReceiptRefs) != 0 {
			return errors.New("empty research receipt state has receipt evidence")
		}
	case "verified":
		if result.ReceiptCount == 0 || result.ReceiptCount != len(result.VerifiedReceiptRefs) {
			return errors.New("verified research receipt state is incomplete")
		}
	case "partially_verified":
		if result.ReceiptCount == 0 || len(result.VerifiedReceiptRefs) == 0 || result.ReceiptCount <= len(result.VerifiedReceiptRefs) {
			return errors.New("partially verified research receipt state is invalid")
		}
	case "unavailable":
		if result.ReceiptCount == 0 || len(result.VerifiedReceiptRefs) != 0 {
			return errors.New("unavailable research receipt state is invalid")
		}
	default:
		return errors.New("research receipt state is invalid")
	}
	for _, receipt := range result.VerifiedReceiptRefs {
		if !safeResearchFact(receipt.Owner) || !safeResearchFact(receipt.Route) || !safeResearchFact(receipt.CanonicalID) ||
			!validSHA256(receipt.Digest) || receipt.VerifiedAt.IsZero() {
			return errors.New("verified research receipt projection is invalid")
		}
	}
	for _, missing := range result.Missing {
		if !safeResearchFact(missing) {
			return errors.New("research result missing entry is invalid")
		}
	}
	return nil
}

func safeResearchFact(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && !strings.Contains(value, "://") &&
		!strings.ContainsAny(value, "?#\r\n")
}
