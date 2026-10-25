package agent

import (
	"context"
	"errors"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/advisor"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agentprofile"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// Classifier はタスク分類器のインターフェース
type Classifier interface {
	Classify(ctx context.Context, t conversation.TurnInput) (routing.Decision, error)
}

// RouteReplyClassification keeps a route decision and an optional CHAT reply
// from one provider generation together until the orchestrator adopts it.
type RouteReplyClassification struct {
	Decision   routing.Decision
	Reply      string
	ReplyValid bool
	Generation llm.GenerateResponse
}

// RouteReplyClassifier is an optional classifier capability for generating a
// structured route and CHAT reply in one request. The supplied request already
// contains Mio's prepared persona, RecallPack, and user messages.
type RouteReplyClassifier interface {
	ClassifyWithChatReply(ctx context.Context, req llm.GenerateRequest) (RouteReplyClassification, error)
}

// MioTurnOutcomeKind is the mutually exclusive result branch of Mio's
// versioned route-and-reply generation contract.
type MioTurnOutcomeKind string

const (
	MioTurnOutcomeAnswer   MioTurnOutcomeKind = "answer"
	MioTurnOutcomeRetrieve MioTurnOutcomeKind = "retrieve"
	MioTurnOutcomeHandoff  MioTurnOutcomeKind = "handoff"
)

// MioRetrievalSourceKind names a semantic source category. It never carries
// an owner ID, physical store name, scope, or permission.
type MioRetrievalSourceKind string

const (
	MioRetrievalSourceConversationMemory MioRetrievalSourceKind = "conversation_memory"
	MioRetrievalSourceUserMemory         MioRetrievalSourceKind = "user_memory"
	MioRetrievalSourceKnowledgeMemory    MioRetrievalSourceKind = "knowledge_memory"
	// Catalog is a semantic category and must be resolved through CORE's
	// registered category source; it is not a physical catalog identifier.
	MioRetrievalSourceCatalog  MioRetrievalSourceKind = "catalog"
	MioRetrievalSourceGlossary MioRetrievalSourceKind = "glossary"
	// WebSearch can only be dispatched through the existing policy-checked
	// web search route.
	MioRetrievalSourceWebSearch MioRetrievalSourceKind = "web_search"
)

// IsRegistered reports whether kind is one of the semantic categories CORE
// currently recognizes for Mio retrieval requests.
func (kind MioRetrievalSourceKind) IsRegistered() bool {
	switch kind {
	case MioRetrievalSourceConversationMemory,
		MioRetrievalSourceUserMemory,
		MioRetrievalSourceKnowledgeMemory,
		MioRetrievalSourceCatalog,
		MioRetrievalSourceGlossary,
		MioRetrievalSourceWebSearch:
		return true
	default:
		return false
	}
}

// ErrInvalidMioTurnOutcome identifies a malformed or inconsistent v2
// MioTurnOutcome response. Callers must terminate the turn without retrieval
// or semantic repair when this error is returned.
var ErrInvalidMioTurnOutcome = errors.New("invalid Mio turn outcome")

// MioRetrievalRequest is a bounded semantic request. The authenticated owner
// and scope are selected by CORE and are never supplied by the model.
type MioRetrievalRequest struct {
	SourceKind        MioRetrievalSourceKind `json:"source_kind"`
	Query             string                 `json:"query"`
	References        []string               `json:"references"`
	InformationNeeded string                 `json:"information_needed"`
}

// MioTurnOutcome is exactly one of a direct answer, a bounded retrieval
// request, or a handoff to an existing route.
type MioTurnOutcome struct {
	Kind     MioTurnOutcomeKind    `json:"kind"`
	Text     string                `json:"text,omitempty"`
	Requests []MioRetrievalRequest `json:"requests,omitempty"`
}

// MioTurnClassification keeps the route, typed v2 outcome, and provider
// response from one generation together until the orchestrator adopts it.
type MioTurnClassification struct {
	Decision   routing.Decision
	Outcome    MioTurnOutcome
	Boundary   conversation.ThreadBoundaryProposal
	Generation llm.GenerateResponse
}

// MioTurnClassifier is an optional single-generation classifier capability.
// A non-nil fixedRoute prevents a new route from being selected for an already
// resolved route or a continuation.
type MioTurnClassifier interface {
	ClassifyWithMioTurnOutcome(ctx context.Context, req llm.GenerateRequest, fixedRoute *routing.Route) (MioTurnClassification, error)
}

// RuleDictionary はルール辞書のインターフェース
type RuleDictionary interface {
	Match(t conversation.TurnInput) (routing.Route, float64, bool) // ルート, 確信度, マッチしたか
}

// ToolRunner はツール実行のインターフェース
type ToolRunner interface {
	ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) // Phase 4.2: 構造化レスポンス
	ListTools(ctx context.Context) ([]tool.ToolMetadata, error)
}

type AdvisorService interface {
	RequestAdvice(ctx context.Context, req advisor.AdviceRequest) (advisor.AdviceResult, error)
}

type AdvisorAdoptionRecorder interface {
	RecordAdoption(ctx context.Context, record advisor.AdvisorAdoptionRecord) error
}

type AgentPolicyService interface {
	Decide(agentID string, action string) (agentprofile.PolicyDecision, error)
}

// MCPClient はMCPクライアントのインターフェース
type MCPClient interface {
	CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error)
	ListTools(ctx context.Context, serverName string) ([]string, error)
}
