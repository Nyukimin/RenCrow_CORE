package gmailintake

import (
	"context"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

// GmailQuery is CORE's source-selection policy. The collector only transports it.
const GmailQuery = `-in:spam -in:trash ((subject:RenCrow AND subject:Backlog) OR subject:"AI・政治デイリーブリーフ")`

type GmailMessage struct {
	ID          string `json:"id"`
	ThreadID    string `json:"thread_id"`
	Subject     string `json:"subject"`
	From        string `json:"from"`
	ReceivedAt  string `json:"received_at"`
	Text        string `json:"text"`
	ContentHash string `json:"content_hash"`
}

type GmailPage struct {
	Schema        string         `json:"schema"`
	Account       string         `json:"account"`
	Query         string         `json:"query"`
	Messages      []GmailMessage `json:"messages"`
	NextPageToken string         `json:"next_page_token"`
}

type GmailCollector interface {
	Collect(context.Context, string) (GmailPage, error)
}

// GmailBriefEvaluator returns proposals, never writes Atlas state or invokes tools.
// The production evaluator uses the existing CORE LLM and web-gather routes.
type GmailBriefEvaluator interface {
	EvaluateBrief(context.Context, GmailMessage, []string) (GmailBriefEvaluation, error)
}

// GmailKnowledgeWriter is the CORE-owned persistence boundary for daily-brief
// topics that do not become Atlas proposals. The writer owns the durable
// request receipt and must treat sourceKey as an idempotency key.
type GmailKnowledgeWriter interface {
	SaveNewsKnowledgeItemWithReceipt(context.Context, domainkm.NewsKnowledgeItem, string, string) (bool, error)
}

// GmailKnowledgeQuote is an exact quote retained as source evidence for a
// private NewsKnowledgeItem. The quote is checked against the complete
// fetched body before this value leaves the evaluator.
type GmailKnowledgeQuote struct {
	URL     string `json:"url"`
	Quote   string `json:"quote"`
	Primary bool   `json:"primary"`
}

// GmailKnowledgeSource records one requested original URL and its bounded
// verification evidence. Full fetched bodies remain in-process only; the
// complete-body hash, capture time, and exact quotes are persisted.
type GmailKnowledgeSource struct {
	URL                string                `json:"url"`
	FinalURL           string                `json:"final_url,omitempty"`
	ContentHash        string                `json:"content_hash,omitempty"`
	CapturedAt         string                `json:"captured_at,omitempty"`
	VerificationStatus string                `json:"verification_status"`
	VerificationReason string                `json:"verification_reason"`
	Quotes             []GmailKnowledgeQuote `json:"quotes,omitempty"`
}

// GmailKnowledgeProposal contains only evaluator-owned semantic content. The
// intake owner fills ItemID, UserID, Status, Visibility, and CreatedAt before
// persisting it, so model output cannot assign identity or visibility.
type GmailKnowledgeProposal struct {
	Topic              string                 `json:"topic"`
	Claim              string                 `json:"claim"`
	Kind               string                 `json:"kind"`
	SectionIndex       int                    `json:"section_index,omitempty"`
	Summary            string                 `json:"summary"`
	VerificationStatus string                 `json:"verification_status"`
	VerificationReason string                 `json:"verification_reason"`
	Sources            []GmailKnowledgeSource `json:"sources,omitempty"`
	FailedURLs         []string               `json:"failed_urls,omitempty"`
}

// GmailPreparedKnowledge is the durable, owner-completed form of a Knowledge
// proposal. It is stored in the Gmail receipt before any Knowledge write.
type GmailPreparedKnowledge struct {
	SourceKey string                     `json:"source_key"`
	Item      domainkm.NewsKnowledgeItem `json:"item"`
	Evidence  GmailKnowledgeProposal     `json:"evidence"`
}

// GmailTopicOutcome is the per-topic audit result for one daily brief. Status
// is one of atlas, reviewed, candidate, or blocked.
type GmailTopicOutcome struct {
	TopicIndex   int      `json:"topic_index"`
	SectionIndex int      `json:"section_index,omitempty"`
	Kind         string   `json:"kind"`
	Topic        string   `json:"topic"`
	Status       string   `json:"status"`
	Reason       string   `json:"reason"`
	SourceURLs   []string `json:"source_urls,omitempty"`
	FailedURLs   []string `json:"failed_urls,omitempty"`
}

type GmailBriefEvaluation struct {
	Status             string                     `json:"status"` // verified, skipped, blocked
	Reason             string                     `json:"reason"`
	Proposals          []appbacklog.IntakeRequest `json:"proposals,omitempty"`
	KnowledgeProposals []GmailKnowledgeProposal   `json:"knowledge_proposals,omitempty"`
	TopicOutcomes      []GmailTopicOutcome        `json:"topic_outcomes,omitempty"`
}

// Prepared proposals are persisted before Atlas writes so recovery cannot rerun
// semantic generation after a partial write. Terminal records are never retried.
type GmailReceipt struct {
	Schema            string                     `json:"schema"`
	Account           string                     `json:"account"`
	Message           GmailMessage               `json:"message"`
	Status            string                     `json:"status"` // prepared, complete, skipped, blocked
	PolicyRevision    string                     `json:"policy_revision,omitempty"`
	Reason            string                     `json:"reason"`
	Prepared          []appbacklog.IntakeRequest `json:"prepared,omitempty"`
	PreparedKnowledge []GmailPreparedKnowledge   `json:"prepared_knowledge,omitempty"`
	TopicOutcomes     []GmailTopicOutcome        `json:"topic_outcomes,omitempty"`
	BacklogItemIDs    []string                   `json:"backlog_item_ids,omitempty"`
	KnowledgeItemIDs  []string                   `json:"knowledge_item_ids,omitempty"`
	ActorID           string                     `json:"actor_id"`
	TaskID            string                     `json:"task_id"`
	RunID             string                     `json:"run_id"`
	TraceID           string                     `json:"trace_id"`
	OriginTaskID      string                     `json:"origin_task_id"`
	OriginRunID       string                     `json:"origin_run_id"`
	OriginTraceID     string                     `json:"origin_trace_id"`
	UpdatedAt         string                     `json:"updated_at"`
}

type GmailReceiptStore interface {
	Get(context.Context, string, string) (GmailReceipt, bool, error)
	Save(context.Context, GmailReceipt) error
	List(context.Context, string, int) ([]GmailReceipt, error)
	Cursor(context.Context, string, string) (string, error)
	SaveCursor(context.Context, string, string, string) error
}

type GmailRunReport struct {
	Account          string   `json:"account"`
	Processed        int      `json:"processed"`
	Created          int      `json:"created"`
	Skipped          int      `json:"skipped"`
	Blocked          int      `json:"blocked"`
	HasMore          bool     `json:"has_more"`
	BacklogItemIDs   []string `json:"backlog_item_ids,omitempty"`
	KnowledgeItemIDs []string `json:"knowledge_item_ids,omitempty"`
}
