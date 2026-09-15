package gmailintake

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

const (
	gmailSchema            = "rencrow.gmail.v1"
	gmailPolicyRevision    = "gmail-daily-routing-v4"
	gmailDailySubject      = "AI・政治デイリーブリーフ"
	gmailIntakePurpose     = "メールに記載された仕様をRenCrowへ取り込むために検討する"
	maxGmailMessages       = 100
	maxGmailTextBytes      = 128 << 10
	maxGmailSnapshotBytes  = 32 << 10
	maxGmailProposals      = gmailBriefMaxTopics
	maxGmailKnowledgeItems = gmailBriefMaxTopics
	maxGmailTopicOutcomes  = gmailBriefMaxTopics
	maxGmailReasonBytes    = 512
	maxGmailSourceSummary  = 8 << 10
)

// GmailEnvelopeSchema is the persisted/transport schema owned by CORE.
// Infrastructure stores use this value when validating the cursor envelope.
const GmailEnvelopeSchema = gmailSchema

var ErrGmailIntakeBusy = errors.New("Gmail intake is already running")

// ErrGmailKnowledgeWriterUnavailable means a daily brief produced a
// Knowledge outcome but the owner persistence boundary was not wired. The
// intake fails closed instead of silently dropping that topic.
var ErrGmailKnowledgeWriterUnavailable = errors.New("Gmail knowledge writer unavailable")

type GmailKnowledgeWriterUnavailableError struct{ Reason string }

func (e *GmailKnowledgeWriterUnavailableError) Error() string {
	if e == nil || strings.TrimSpace(e.Reason) == "" {
		return ErrGmailKnowledgeWriterUnavailable.Error()
	}
	return ErrGmailKnowledgeWriterUnavailable.Error() + ": " + strings.TrimSpace(e.Reason)
}

func (e *GmailKnowledgeWriterUnavailableError) Unwrap() error {
	return ErrGmailKnowledgeWriterUnavailable
}

// GmailIntake is the CORE owner boundary for the bounded Gmail -> Atlas and
// private Knowledge flow.
// The collector and evaluator are injected so this type never owns provider
// credentials, model routing, or web collection.
type GmailIntake struct {
	account         string
	userID          string
	collector       GmailCollector
	evaluator       GmailBriefEvaluator
	receipts        GmailReceiptStore
	atlas           *appbacklog.Service
	knowledgeWriter GmailKnowledgeWriter
	clock           func() time.Time
	mu              sync.Mutex
}

func NewGmailIntake(account, userID string, collector GmailCollector, evaluator GmailBriefEvaluator, receipts GmailReceiptStore, atlas *appbacklog.Service) *GmailIntake {
	return &GmailIntake{
		account:   strings.TrimSpace(account),
		userID:    strings.TrimSpace(userID),
		collector: collector,
		evaluator: evaluator,
		receipts:  receipts,
		atlas:     atlas,
		clock:     func() time.Time { return time.Now().UTC() },
	}
}

// WithClock keeps receipt timestamps owned by Gmail intake while allowing
// deterministic owner tests. The Atlas service clock is deliberately not used.
func (g *GmailIntake) WithClock(clock func() time.Time) *GmailIntake {
	if g != nil && clock != nil {
		g.clock = clock
	}
	return g
}

// WithKnowledgeWriter wires the existing CORE Knowledge owner store. It is a
// construction-time setter; the writer remains behind the intake owner route.
func (g *GmailIntake) WithKnowledgeWriter(writer GmailKnowledgeWriter) *GmailIntake {
	if g != nil {
		g.knowledgeWriter = writer
	}
	return g
}

// Receipts is the authenticated owner read surface. The configured account is
// always used; a caller cannot select another account through this API.
func (g *GmailIntake) Receipts(ctx context.Context, limit int) ([]GmailReceipt, error) {
	if err := g.validateReceiptReadContext(ctx); err != nil {
		return nil, err
	}
	if g == nil || g.receipts == nil {
		return nil, errors.New("Gmail receipt store is unavailable")
	}
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	return g.receipts.List(ctx, g.account, limit)
}

func (g *GmailIntake) Run(ctx context.Context) (GmailRunReport, error) {
	var report GmailRunReport
	if err := g.validateRunContext(ctx); err != nil {
		return report, err
	}
	if !g.mu.TryLock() {
		return report, ErrGmailIntakeBusy
	}
	defer g.mu.Unlock()
	if err := g.validateDependencies(); err != nil {
		return report, err
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}
	report.Account = g.account

	cursor, err := g.receipts.Cursor(ctx, g.account, GmailQuery)
	if err != nil {
		return report, fmt.Errorf("read Gmail cursor: %w", err)
	}
	storedReceipts, err := g.receipts.List(ctx, g.account, maxGmailMessages)
	if err != nil {
		return report, fmt.Errorf("list Gmail receipts: %w", err)
	}
	eligibleReceipts := make([]GmailReceipt, 0, 1)
	for _, receipt := range storedReceipts {
		if isGmailReceiptEligibleForReprocess(receipt) {
			eligibleReceipts = append(eligibleReceipts, receipt)
		}
	}

	processForReport := func(message GmailMessage) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		report.Processed++
		created, status, ids, err := g.processMessage(ctx, message)
		if err != nil {
			return err
		}
		report.Created += created
		report.BacklogItemIDs = appendUniqueStrings(report.BacklogItemIDs, ids)
		processedReceipt, found, receiptErr := g.receipts.Get(ctx, g.account, message.ID)
		if receiptErr != nil {
			return fmt.Errorf("read processed Gmail receipt: %w", receiptErr)
		}
		if found {
			report.KnowledgeItemIDs = appendUniqueStrings(report.KnowledgeItemIDs, processedReceipt.KnowledgeItemIDs)
		}
		switch status {
		case "skipped":
			report.Skipped++
		case "blocked":
			report.Blocked++
		}
		return nil
	}
	if len(eligibleReceipts) > 0 {
		// Reprocess one old outputless terminal receipt before collection. The
		// cursor remains untouched so a pending page is retried after the
		// bounded replay budget is exhausted.
		report.HasMore = strings.TrimSpace(cursor) != "" || len(eligibleReceipts) > 1
		if err := processForReport(eligibleReceipts[0].Message); err != nil {
			return report, err
		}
		return report, nil
	}
	page, err := g.collector.Collect(ctx, cursor)
	if err != nil {
		// A stale cursor can make a provider repeatedly fail. Reset it only for
		// a non-cancelled provider failure, and retain the original failure as
		// the operation result.
		if cursor != "" && ctx.Err() == nil {
			if resetErr := g.receipts.SaveCursor(ctx, g.account, GmailQuery, ""); resetErr != nil {
				return report, errors.Join(fmt.Errorf("Gmail collection failed"), fmt.Errorf("reset Gmail cursor: %w", resetErr))
			}
		}
		return report, fmt.Errorf("Gmail collection failed: %w", err)
	}
	if err := validateGmailPage(page, g.account); err != nil {
		return report, err
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}

	for _, message := range page.Messages {
		if err := processForReport(message); err != nil {
			return report, err
		}
	}

	if err := ctx.Err(); err != nil {
		return report, err
	}
	if err := g.receipts.SaveCursor(ctx, g.account, GmailQuery, page.NextPageToken); err != nil {
		return report, fmt.Errorf("save Gmail cursor: %w", err)
	}
	report.HasMore = strings.TrimSpace(page.NextPageToken) != ""
	return report, nil
}

func isGmailReceiptEligibleForReprocess(receipt GmailReceipt) bool {
	switch receipt.PolicyRevision {
	case "", "gmail-daily-routing-v1", "gmail-daily-routing-v2", "gmail-daily-routing-v3":
		// Preserve the existing legacy replay allowlist and include v3 for
		// the current bounded migration. Unknown and future revisions remain
		// terminal until their own migration defines their semantics.
	default:
		return false
	}
	if receipt.Status != "blocked" && receipt.Status != "skipped" {
		return false
	}
	class := classifyGmailSubject(receipt.Message.Subject)
	if class != gmailSubjectDaily && class != gmailSubjectDirect {
		return false
	}
	return len(receipt.Prepared) == 0 && len(receipt.PreparedKnowledge) == 0 && len(receipt.BacklogItemIDs) == 0 && len(receipt.KnowledgeItemIDs) == 0
}

func (g *GmailIntake) validateDependencies() error {
	if g == nil {
		return errors.New("Gmail intake is unavailable")
	}
	if strings.TrimSpace(g.account) == "" || strings.TrimSpace(g.userID) == "" || !utf8.ValidString(g.account) || !utf8.ValidString(g.userID) || strings.ContainsAny(g.account+g.userID, "\x00\r\n") {
		return errors.New("Gmail account and user are required")
	}
	if g.collector == nil {
		return errors.New("Gmail collector is unavailable")
	}
	if g.receipts == nil {
		return errors.New("Gmail receipt store is unavailable")
	}
	if g.atlas == nil {
		return errors.New("Atlas service is unavailable")
	}
	return nil
}

func (g *GmailIntake) validateRunContext(ctx context.Context) error {
	if g == nil {
		return errors.New("Gmail intake is unavailable")
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		return errors.New("trusted Gmail execution scope is required")
	}
	if err := scope.Validate(); err != nil {
		return fmt.Errorf("trusted Gmail execution scope is invalid: %w", err)
	}
	if scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != "shiro" {
		return errors.New("Gmail intake requires the CORE Shiro agent actor")
	}
	if scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator {
		return errors.New("Gmail intake requires an authenticated agent orchestrator")
	}
	if scope.AuthenticatedUserID != g.userID {
		return errors.New("Gmail intake user scope does not match the configured user")
	}
	if !scope.Allows(domaintool.DataScopeUser) {
		return errors.New("Gmail intake requires user data scope")
	}
	if _, err := domainexecution.IdentityFromContext(ctx); err != nil {
		return fmt.Errorf("canonical Gmail execution identity is required: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func (g *GmailIntake) validateReceiptReadContext(ctx context.Context) error {
	if g == nil {
		return errors.New("Gmail intake is unavailable")
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		return errors.New("trusted Gmail receipt read scope is required")
	}
	if err := scope.Validate(); err != nil {
		return fmt.Errorf("trusted Gmail receipt read scope is invalid: %w", err)
	}
	if scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != g.userID || scope.AuthenticatedUserID != g.userID {
		return errors.New("Gmail receipt read requires the configured authenticated user")
	}
	if scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP || !scope.Allows(domaintool.DataScopeUser) {
		return errors.New("Gmail receipt read requires authenticated user data scope")
	}
	return ctx.Err()
}

func (g *GmailIntake) processMessage(ctx context.Context, message GmailMessage) (int, string, []string, error) {
	receipt, found, err := g.receipts.Get(ctx, g.account, message.ID)
	if err != nil {
		return 0, "", nil, fmt.Errorf("read Gmail receipt: %w", err)
	}
	if found {
		if err := validateStoredReceipt(receipt, g.account, message); err != nil {
			return 0, "", nil, err
		}
		if err := g.validateReceiptKnowledgeOwner(receipt); err != nil {
			return 0, "", nil, err
		}
		if !isGmailReceiptEligibleForReprocess(receipt) {
			switch receipt.Status {
			case "complete":
				return 0, "complete", append([]string(nil), receipt.BacklogItemIDs...), nil
			case "skipped":
				return 0, "skipped", append([]string(nil), receipt.BacklogItemIDs...), nil
			case "blocked":
				return 0, "blocked", append([]string(nil), receipt.BacklogItemIDs...), nil
			case "prepared":
				created, status, ids, resumeErr := g.resumePrepared(ctx, receipt)
				if resumeErr != nil {
					return created, "", ids, resumeErr
				}
				return created, status, ids, nil
			default:
				return 0, "", nil, fmt.Errorf("Gmail receipt has unsupported status %q", receipt.Status)
			}
		}
	}

	class := classifyGmailSubject(message.Subject)
	switch class {
	case gmailSubjectSkipped:
		receipt = g.newReceipt(ctx, message, "skipped", "subject_not_selected", nil, nil)
		if err := g.receipts.Save(ctx, receipt); err != nil {
			return 0, "", nil, fmt.Errorf("save skipped Gmail receipt: %w", err)
		}
		return 0, "skipped", nil, nil
	case gmailSubjectDirect:
		request := directGmailRequest(g.account, message)
		receipt = g.newReceipt(ctx, message, "prepared", "", []appbacklog.IntakeRequest{request}, nil)
		if err := g.receipts.Save(ctx, receipt); err != nil {
			return 0, "", nil, fmt.Errorf("save prepared Gmail receipt: %w", err)
		}
		return g.resumePrepared(ctx, receipt)
	case gmailSubjectDaily:
		return g.processDaily(ctx, message)
	default:
		return 0, "", nil, errors.New("unknown Gmail subject classification")
	}
}

func (g *GmailIntake) processDaily(ctx context.Context, message GmailMessage) (int, string, []string, error) {
	if g.evaluator == nil {
		return 0, "", nil, &GmailBriefEvaluatorUnavailableError{Reason: "daily brief evaluator is not wired"}
	}
	snapshot, err := g.atlasSnapshot(ctx)
	if err != nil {
		return 0, "", nil, fmt.Errorf("build Atlas snapshot: %w", err)
	}
	evaluation, err := g.evaluator.EvaluateBrief(ctx, message, snapshot)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, ErrGmailBriefEvaluatorUnavailable) || errors.Is(err, ErrGmailBriefRetryable) {
			if ctx.Err() != nil {
				return 0, "", nil, ctx.Err()
			}
			return 0, "", nil, err
		}
		receipt := g.newReceipt(ctx, message, "blocked", sanitizeReason(evaluation.Reason, "evaluator_error"), nil, nil)
		if saveErr := g.receipts.Save(ctx, receipt); saveErr != nil {
			return 0, "", nil, fmt.Errorf("save blocked Gmail receipt: %w", saveErr)
		}
		return 0, "blocked", nil, nil
	}

	status := strings.ToLower(strings.TrimSpace(evaluation.Status))
	safeReason := sanitizeReason(evaluation.Reason, "evaluator_result")
	switch status {
	case "skipped":
		if err := validateGmailTopicOutcomes(evaluation.TopicOutcomes); err != nil {
			receipt := g.newReceipt(ctx, message, "blocked", "invalid_topic_outcomes", nil, nil)
			if saveErr := g.receipts.Save(ctx, receipt); saveErr != nil {
				return 0, "", nil, fmt.Errorf("save blocked Gmail receipt: %w", saveErr)
			}
			return 0, "blocked", nil, nil
		}
		if len(evaluation.Proposals) != 0 || len(evaluation.KnowledgeProposals) != 0 {
			receipt := g.newReceipt(ctx, message, "blocked", "skipped_evaluator_returned_outputs", nil, nil)
			if saveErr := g.receipts.Save(ctx, receipt); saveErr != nil {
				return 0, "", nil, fmt.Errorf("save blocked Gmail receipt: %w", saveErr)
			}
			return 0, "blocked", nil, nil
		}
		receipt := g.newReceipt(ctx, message, "skipped", safeReason, nil, nil)
		receipt.TopicOutcomes = cloneGmailTopicOutcomes(evaluation.TopicOutcomes)
		if err := g.receipts.Save(ctx, receipt); err != nil {
			return 0, "", nil, fmt.Errorf("save skipped Gmail receipt: %w", err)
		}
		return 0, "skipped", nil, nil
	case "blocked":
		if err := validateGmailTopicOutcomes(evaluation.TopicOutcomes); err != nil {
			receipt := g.newReceipt(ctx, message, "blocked", "invalid_topic_outcomes", nil, nil)
			if saveErr := g.receipts.Save(ctx, receipt); saveErr != nil {
				return 0, "", nil, fmt.Errorf("save blocked Gmail receipt: %w", saveErr)
			}
			return 0, "blocked", nil, nil
		}
		receipt := g.newReceipt(ctx, message, "blocked", safeReason, nil, nil)
		receipt.TopicOutcomes = cloneGmailTopicOutcomes(evaluation.TopicOutcomes)
		if err := g.receipts.Save(ctx, receipt); err != nil {
			return 0, "", nil, fmt.Errorf("save blocked Gmail receipt: %w", err)
		}
		return 0, "blocked", nil, nil
	case "verified":
		if err := validateGmailTopicOutcomes(evaluation.TopicOutcomes); err != nil {
			receipt := g.newReceipt(ctx, message, "blocked", "invalid_topic_outcomes", nil, nil)
			if saveErr := g.receipts.Save(ctx, receipt); saveErr != nil {
				return 0, "", nil, fmt.Errorf("save blocked Gmail receipt: %w", saveErr)
			}
			return 0, "blocked", nil, nil
		}
		prepared, validationErr := g.prepareProposals(message, evaluation.Proposals)
		if validationErr != nil {
			receipt := g.newReceipt(ctx, message, "blocked", sanitizeReason(evaluation.Reason, "invalid_evaluator_result"), nil, nil)
			if saveErr := g.receipts.Save(ctx, receipt); saveErr != nil {
				return 0, "", nil, fmt.Errorf("save blocked Gmail receipt: %w", saveErr)
			}
			return 0, "blocked", nil, nil
		}
		preparedKnowledge, knowledgeErr := g.prepareKnowledgeProposals(message, evaluation.KnowledgeProposals)
		if knowledgeErr != nil {
			receipt := g.newReceipt(ctx, message, "blocked", "invalid_knowledge_evaluator_result", nil, nil)
			if saveErr := g.receipts.Save(ctx, receipt); saveErr != nil {
				return 0, "", nil, fmt.Errorf("save blocked Gmail receipt: %w", saveErr)
			}
			return 0, "blocked", nil, nil
		}
		if len(prepared) == 0 && len(preparedKnowledge) == 0 {
			receipt := g.newReceipt(ctx, message, "skipped", safeReason, nil, nil)
			receipt.TopicOutcomes = cloneGmailTopicOutcomes(evaluation.TopicOutcomes)
			if err := g.receipts.Save(ctx, receipt); err != nil {
				return 0, "", nil, fmt.Errorf("save skipped Gmail receipt: %w", err)
			}
			return 0, "skipped", nil, nil
		}
		receipt := g.newReceipt(ctx, message, "prepared", safeReason, prepared, nil)
		receipt.PreparedKnowledge = cloneGmailPreparedKnowledge(preparedKnowledge)
		receipt.TopicOutcomes = cloneGmailTopicOutcomes(evaluation.TopicOutcomes)
		if err := g.receipts.Save(ctx, receipt); err != nil {
			return 0, "", nil, fmt.Errorf("save prepared Gmail receipt: %w", err)
		}
		return g.resumePrepared(ctx, receipt)
	default:
		receipt := g.newReceipt(ctx, message, "blocked", "invalid_evaluator_status", nil, nil)
		if saveErr := g.receipts.Save(ctx, receipt); saveErr != nil {
			return 0, "", nil, fmt.Errorf("save blocked Gmail receipt: %w", saveErr)
		}
		return 0, "blocked", nil, nil
	}
}

func (g *GmailIntake) atlasSnapshot(ctx context.Context) ([]string, error) {
	items, err := g.atlas.List(ctx, 0)
	if err != nil {
		return nil, err
	}
	snapshot := make([]string, 0, len(items))
	used := 0
	for _, item := range items {
		entry := fmt.Sprintf("id: %s\nconcept_state: %s\ndelivery_state: %s\nowner_module: %s\naffected_modules: %s\ntitle: %s\npurpose: %s\nbody: %s", item.BacklogItemID, item.ConceptState, item.DeliveryState, item.OwnerModule, strings.Join(item.AffectedModules, ", "), strings.TrimSpace(item.Title), strings.TrimSpace(item.Purpose), strings.TrimSpace(item.Body))
		if used >= maxGmailSnapshotBytes {
			break
		}
		remaining := maxGmailSnapshotBytes - used
		entry = truncateUTF8(entry, remaining)
		if entry == "" {
			break
		}
		snapshot = append(snapshot, entry)
		used += len([]byte(entry))
	}
	return snapshot, nil
}

func (g *GmailIntake) prepareProposals(message GmailMessage, proposals []appbacklog.IntakeRequest) ([]appbacklog.IntakeRequest, error) {
	if len(proposals) > maxGmailProposals {
		return nil, errors.New("Gmail evaluator proposal bound exceeded")
	}
	if len(proposals) == 0 {
		return nil, nil
	}
	prepared := make([]appbacklog.IntakeRequest, 0, len(proposals))
	for index, proposal := range proposals {
		if strings.TrimSpace(proposal.Title) == "" || strings.TrimSpace(proposal.Purpose) == "" || strings.TrimSpace(proposal.Body) == "" || !hasNonEmptyAcceptance(proposal.AcceptanceCriteria) {
			return nil, errors.New("Gmail evaluator proposal is incomplete")
		}
		if !utf8.ValidString(proposal.Title) || !utf8.ValidString(proposal.Purpose) || !utf8.ValidString(proposal.Body) {
			return nil, errors.New("Gmail evaluator proposal is not valid UTF-8")
		}
		if len([]byte(proposal.Body)) > maxGmailTextBytes {
			return nil, errors.New("Gmail evaluator proposal body is too large")
		}
		for _, criterion := range proposal.AcceptanceCriteria {
			if !utf8.ValidString(criterion) || strings.TrimSpace(criterion) == "" || len([]byte(criterion)) > maxGmailTextBytes {
				return nil, errors.New("Gmail evaluator acceptance criterion is invalid")
			}
		}
		if err := appbacklog.ValidateSpecificationRefs(proposal.SpecificationRefs); err != nil {
			return nil, err
		}
		refs, err := g.sanitizeEvaluatorSourceRefs(message, proposal.SourceRefs)
		if err != nil {
			return nil, err
		}
		locator := gmailLocator(g.account, message.ID) + "#topic=" + strconv.Itoa(index+1)
		refs = append([]domainbacklog.SourceRef{gmailSourceRef(message, locator)}, refs...)
		proposal.BacklogItemID = ""
		proposal.Owner = "shiro"
		proposal.OwnerModule = domainbacklog.LifecycleOwnerModule
		proposal.Source = gmailLocator(g.account, message.ID)
		proposal.SourceRefs = refs
		proposal.Reason = ""
		proposal.AcceptanceCriteria = cleanAcceptance(proposal.AcceptanceCriteria)
		prepared = append(prepared, cloneIntakeRequest(proposal))
	}
	return prepared, nil
}

func (g *GmailIntake) sanitizeEvaluatorSourceRefs(_ GmailMessage, refs []domainbacklog.SourceRef) ([]domainbacklog.SourceRef, error) {
	if len(refs) == 0 {
		return nil, errors.New("Gmail evaluator proposal requires fetched web evidence")
	}
	cleaned := make([]domainbacklog.SourceRef, 0, len(refs))
	for _, ref := range refs {
		if ref.Type != "web" {
			return nil, errors.New("Gmail evaluator source reference type is invalid")
		}
		if ref.Strength != "primary" && ref.Strength != "secondary" {
			return nil, errors.New("Gmail evaluator source reference strength is invalid")
		}
		locator := strings.TrimSpace(ref.Locator)
		if _, err := modulewebgather.NormalizeURL(locator, false); err != nil || strings.ContainsAny(locator, "\x00\r\n") {
			return nil, errors.New("Gmail evaluator source reference is unsafe")
		}
		hash, err := normalizedSHA256(ref.ContentHash)
		if err != nil {
			return nil, errors.New("Gmail evaluator source reference hash is invalid")
		}
		captured := strings.TrimSpace(ref.CapturedAt)
		if captured == "" {
			return nil, errors.New("Gmail evaluator source reference timestamp is required")
		}
		if _, err := time.Parse(time.RFC3339Nano, captured); err != nil {
			return nil, errors.New("Gmail evaluator source reference timestamp is invalid")
		}
		summary := sanitizeSourceSummary(ref.RawOrSummary)
		cleaned = append(cleaned, domainbacklog.SourceRef{
			Type:         "web",
			Locator:      locator,
			Strength:     ref.Strength,
			ContentHash:  hash,
			CapturedAt:   captured,
			RawOrSummary: summary,
		})
	}
	return cleaned, nil
}

func (g *GmailIntake) resumePrepared(ctx context.Context, receipt GmailReceipt) (int, string, []string, error) {
	if (len(receipt.Prepared) == 0 && len(receipt.PreparedKnowledge) == 0) || len(receipt.Prepared) > maxGmailProposals || len(receipt.PreparedKnowledge) > maxGmailKnowledgeItems {
		return 0, "", nil, errors.New("prepared Gmail receipt has invalid requests")
	}
	if err := ValidateGmailReceipt(receipt); err != nil {
		return 0, "", nil, fmt.Errorf("prepared Gmail receipt is invalid: %w", err)
	}
	if receipt.Account != g.account || receipt.Status != "prepared" {
		return 0, "", nil, errors.New("prepared Gmail receipt does not match the configured account or status")
	}
	if err := g.validateReceiptKnowledgeOwner(receipt); err != nil {
		return 0, "", nil, err
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		return 0, "", nil, fmt.Errorf("canonical Gmail execution identity is required: %w", err)
	}
	// A prepared receipt may be resumed by a later heartbeat execution. Keep
	// the original preparation identity for provenance, while making the
	// current attempt identity explicit before any Atlas write can occur.
	receipt.TaskID = string(identity.TaskID)
	receipt.RunID = string(identity.RunID)
	receipt.TraceID = string(identity.TraceID)
	receipt.Status = "prepared"
	receipt.UpdatedAt = g.receiptNow().Format(time.RFC3339Nano)
	if err := g.receipts.Save(ctx, receipt); err != nil {
		return 0, "", nil, fmt.Errorf("save Gmail receipt resume identity: %w", err)
	}
	ids := append([]string(nil), receipt.BacklogItemIDs...)
	created := 0
	for _, request := range receipt.Prepared {
		if err := ctx.Err(); err != nil {
			return created, "", ids, err
		}
		result, err := g.atlas.Intake(ctx, cloneIntakeRequest(request))
		if err != nil {
			return created, "", ids, fmt.Errorf("Atlas Gmail intake failed: %w", err)
		}
		if result.BacklogItemID != "" {
			ids = appendUniqueStrings(ids, []string{string(result.BacklogItemID)})
		}
		if !result.Duplicate {
			created++
		}
		receipt.BacklogItemIDs = append([]string(nil), ids...)
		receipt.Status = "prepared"
		receipt.UpdatedAt = g.receiptNow().Format(time.RFC3339Nano)
		if err := g.receipts.Save(ctx, receipt); err != nil {
			return created, "", ids, fmt.Errorf("save Gmail receipt progress: %w", err)
		}

		// Intake initially creates RADAR. Existing CANDIDATE/ADOPTED/
		// DEFERRED/REJECTED records are preserved exactly as returned.
		if result.Item.ConceptState == domainbacklog.ConceptRadar {
			candidate, err := g.atlas.Candidate(ctx, string(result.BacklogItemID))
			if err != nil {
				return created, "", ids, fmt.Errorf("promote Atlas Gmail item to candidate: %w", err)
			}
			ids = appendUniqueStrings(ids, []string{string(candidate.BacklogItemID)})
			receipt.BacklogItemIDs = append([]string(nil), ids...)
			receipt.Status = "prepared"
			receipt.UpdatedAt = g.receiptNow().Format(time.RFC3339Nano)
			if err := g.receipts.Save(ctx, receipt); err != nil {
				return created, "", ids, fmt.Errorf("save Gmail candidate progress: %w", err)
			}
		}
	}
	for _, prepared := range receipt.PreparedKnowledge {
		if err := ctx.Err(); err != nil {
			return created, "", ids, err
		}
		if containsString(receipt.KnowledgeItemIDs, prepared.Item.ItemID) {
			continue
		}
		if g.knowledgeWriter == nil {
			return created, "", ids, &GmailKnowledgeWriterUnavailableError{Reason: "prepared daily brief requires Knowledge persistence"}
		}
		reused, err := g.knowledgeWriter.SaveNewsKnowledgeItemWithReceipt(ctx, cloneNewsKnowledgeItem(prepared.Item), prepared.SourceKey, receipt.ActorID)
		if err != nil {
			return created, "", ids, fmt.Errorf("Knowledge Gmail intake failed: %w", err)
		}
		receipt.KnowledgeItemIDs = appendUniqueStrings(receipt.KnowledgeItemIDs, []string{prepared.Item.ItemID})
		receipt.Status = "prepared"
		receipt.UpdatedAt = g.receiptNow().Format(time.RFC3339Nano)
		if err := g.receipts.Save(ctx, receipt); err != nil {
			return created, "", ids, fmt.Errorf("save Gmail Knowledge progress: %w", err)
		}
		if !reused {
			created++
		}
	}
	receipt.Status = "complete"
	receipt.BacklogItemIDs = append([]string(nil), ids...)
	receipt.KnowledgeItemIDs = append([]string(nil), receipt.KnowledgeItemIDs...)
	receipt.UpdatedAt = g.receiptNow().Format(time.RFC3339Nano)
	if err := g.receipts.Save(ctx, receipt); err != nil {
		return created, "", ids, fmt.Errorf("save completed Gmail receipt: %w", err)
	}
	return created, "complete", ids, nil
}

func (g *GmailIntake) newReceipt(ctx context.Context, message GmailMessage, status, reason string, prepared []appbacklog.IntakeRequest, ids []string) GmailReceipt {
	var actorID, taskID, runID, traceID string
	if scope, ok := domaintool.ToolExecutionScopeFromContext(ctx); ok {
		actorID = scope.ActorID
	}
	if identity, err := domainexecution.IdentityFromContext(ctx); err == nil {
		taskID = string(identity.TaskID)
		runID = string(identity.RunID)
		traceID = string(identity.TraceID)
	}
	return GmailReceipt{Schema: gmailSchema, Account: g.account, Message: cloneGmailMessage(message), Status: status, PolicyRevision: gmailPolicyRevision, Reason: sanitizeReason(reason, ""), Prepared: cloneIntakeRequests(prepared), BacklogItemIDs: appendUniqueStrings(nil, ids), ActorID: actorID, TaskID: taskID, RunID: runID, TraceID: traceID, OriginTaskID: taskID, OriginRunID: runID, OriginTraceID: traceID, UpdatedAt: g.receiptNow().Format(time.RFC3339Nano)}
}

func (g *GmailIntake) receiptNow() time.Time {
	if g != nil && g.clock != nil {
		return g.clock().UTC()
	}
	return time.Now().UTC()
}

func validateGmailPage(page GmailPage, account string) error {
	if page.Schema != gmailSchema {
		return errors.New("Gmail page schema is invalid")
	}
	if !utf8.ValidString(page.Account) || strings.TrimSpace(page.Account) == "" || strings.ContainsAny(page.Account, "\x00\r\n") || !strings.EqualFold(page.Account, account) {
		return errors.New("Gmail page account does not match the configured account")
	}
	if page.Query != GmailQuery {
		return errors.New("Gmail page query does not match CORE policy")
	}
	if len(page.Messages) > maxGmailMessages {
		return errors.New("Gmail page message bound exceeded")
	}
	if !utf8.ValidString(page.NextPageToken) || len([]byte(page.NextPageToken)) > 4096 || strings.ContainsAny(page.NextPageToken, "\x00\r\n") {
		return errors.New("Gmail page cursor is invalid")
	}
	seen := make(map[string]struct{}, len(page.Messages))
	for _, message := range page.Messages {
		messageKey := strings.ToLower(message.ID)
		if _, exists := seen[messageKey]; exists {
			return errors.New("Gmail page contains duplicate message IDs")
		}
		seen[messageKey] = struct{}{}
		if err := validateGmailMessage(message); err != nil {
			return err
		}
	}
	return nil
}

func validateGmailMessage(message GmailMessage) error {
	if !isGmailHexID(message.ID) || !isGmailHexID(message.ThreadID) {
		return errors.New("Gmail message identity is invalid")
	}
	if !utf8.ValidString(message.Text) || strings.TrimSpace(message.Text) == "" || len([]byte(message.Text)) > maxGmailTextBytes {
		return errors.New("Gmail message text is invalid")
	}
	if !utf8.ValidString(message.Subject) || !utf8.ValidString(message.From) {
		return errors.New("Gmail message metadata is not valid UTF-8")
	}
	if _, err := time.Parse(time.RFC3339Nano, message.ReceivedAt); err != nil {
		return errors.New("Gmail message timestamp is invalid")
	}
	provided, err := normalizedSHA256(message.ContentHash)
	if err != nil {
		return errors.New("Gmail message content hash is invalid")
	}
	digest := sha256.Sum256([]byte(message.Text))
	if !strings.EqualFold(provided, hex.EncodeToString(digest[:])) {
		return errors.New("Gmail message content hash does not match text")
	}
	return nil
}

// ValidateGmailReceipt validates the semantic contents of one committed Gmail
// receipt. File stores call this owner function so persisted records cannot
// grow a second, independently maintained policy implementation.
func ValidateGmailReceipt(receipt GmailReceipt) error {
	if receipt.Schema != GmailEnvelopeSchema || strings.TrimSpace(receipt.Account) != receipt.Account || receipt.Account == "" || !utf8.ValidString(receipt.Account) || strings.ContainsAny(receipt.Account, "\x00\r\n") {
		return errors.New("receipt schema or account is invalid")
	}
	if err := validateGmailMessage(receipt.Message); err != nil {
		return err
	}
	if receipt.PolicyRevision != "" && (strings.TrimSpace(receipt.PolicyRevision) != receipt.PolicyRevision || !utf8.ValidString(receipt.PolicyRevision) || len([]byte(receipt.PolicyRevision)) > 128 || strings.ContainsAny(receipt.PolicyRevision, "\x00\r\n")) {
		return errors.New("receipt policy revision is invalid")
	}
	preparedCount := len(receipt.Prepared) + len(receipt.PreparedKnowledge)
	switch receipt.Status {
	case "prepared":
		if preparedCount == 0 || len(receipt.Prepared) > maxGmailProposals || len(receipt.PreparedKnowledge) > maxGmailKnowledgeItems || preparedCount > maxGmailProposals+maxGmailKnowledgeItems {
			return errors.New("prepared receipt requests are invalid")
		}
	case "complete":
		if preparedCount == 0 || len(receipt.Prepared) > maxGmailProposals || len(receipt.PreparedKnowledge) > maxGmailKnowledgeItems || preparedCount > maxGmailProposals+maxGmailKnowledgeItems {
			return errors.New("complete receipt requests are invalid")
		}
	case "skipped", "blocked":
		if preparedCount != 0 || len(receipt.BacklogItemIDs) != 0 || len(receipt.KnowledgeItemIDs) != 0 {
			return errors.New("terminal receipt must not contain prepared requests or backlog item identities")
		}
	default:
		return errors.New("receipt status is invalid")
	}
	if (receipt.Status == "skipped" || receipt.Status == "blocked") && strings.TrimSpace(receipt.Reason) == "" {
		return errors.New("terminal Gmail receipt reason is required")
	}
	if !utf8.ValidString(receipt.Reason) || len([]byte(receipt.Reason)) > maxGmailReasonBytes || strings.ContainsAny(receipt.Reason, "\x00\r\n") {
		return errors.New("receipt reason is invalid")
	}
	if receipt.ActorID != "shiro" {
		return errors.New("receipt actor is invalid")
	}
	parsedTaskID, err := modulecore.ParseTaskID(receipt.TaskID)
	if err != nil || string(parsedTaskID) != receipt.TaskID {
		return errors.New("receipt task identity is invalid")
	}
	if err := modulecore.RunID(receipt.RunID).Validate(); err != nil {
		return errors.New("receipt run identity is invalid")
	}
	if receipt.TraceID != "" {
		if err := modulecore.TraceID(receipt.TraceID).Validate(); err != nil {
			return errors.New("receipt trace identity is invalid")
		}
	}
	parsedOriginTaskID, err := modulecore.ParseTaskID(receipt.OriginTaskID)
	if err != nil || string(parsedOriginTaskID) != receipt.OriginTaskID {
		return errors.New("receipt origin task identity is invalid")
	}
	if err := modulecore.RunID(receipt.OriginRunID).Validate(); err != nil {
		return errors.New("receipt origin run identity is invalid")
	}
	if receipt.OriginTraceID != "" {
		if err := modulecore.TraceID(receipt.OriginTraceID).Validate(); err != nil {
			return errors.New("receipt origin trace identity is invalid")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, receipt.UpdatedAt); err != nil {
		return errors.New("receipt timestamp is invalid")
	}
	if len(receipt.BacklogItemIDs) > maxGmailProposals || len(receipt.KnowledgeItemIDs) > maxGmailKnowledgeItems {
		return errors.New("receipt backlog item identity bound exceeded")
	}
	if receipt.Status == "complete" && len(receipt.BacklogItemIDs)+len(receipt.KnowledgeItemIDs) == 0 {
		return errors.New("complete receipt backlog item identities are invalid")
	}
	if receipt.Status == "complete" && len(receipt.BacklogItemIDs)+len(receipt.KnowledgeItemIDs) > preparedCount {
		return errors.New("complete receipt item identities are invalid")
	}
	if receipt.Status == "prepared" && len(receipt.BacklogItemIDs)+len(receipt.KnowledgeItemIDs) > preparedCount {
		return errors.New("prepared receipt backlog item identities are invalid")
	}
	seenIDs := make(map[string]struct{}, len(receipt.BacklogItemIDs))
	for _, id := range receipt.BacklogItemIDs {
		if strings.TrimSpace(id) != id {
			return errors.New("receipt backlog item identity is not canonical")
		}
		if _, exists := seenIDs[id]; exists {
			return errors.New("receipt backlog item identity is duplicated")
		}
		seenIDs[id] = struct{}{}
		if err := modulecore.BacklogItemID(id).Validate(); err != nil {
			return errors.New("receipt backlog item identity is invalid")
		}
	}
	for index, request := range receipt.Prepared {
		if err := validatePreparedGmailRequest(receipt, index, request); err != nil {
			return err
		}
	}
	seenKnowledgeIDs := make(map[string]struct{}, len(receipt.KnowledgeItemIDs))
	for _, id := range receipt.KnowledgeItemIDs {
		if strings.TrimSpace(id) != id || id == "" {
			return errors.New("receipt Knowledge item identity is not canonical")
		}
		if _, exists := seenKnowledgeIDs[id]; exists {
			return errors.New("receipt Knowledge item identity is duplicated")
		}
		seenKnowledgeIDs[id] = struct{}{}
	}
	for index, prepared := range receipt.PreparedKnowledge {
		if err := validatePreparedGmailKnowledge(receipt, index, prepared); err != nil {
			return err
		}
	}
	if len(receipt.PreparedKnowledge) > 0 {
		preparedKnowledgeIDs := make(map[string]struct{}, len(receipt.PreparedKnowledge))
		for _, prepared := range receipt.PreparedKnowledge {
			preparedKnowledgeIDs[prepared.Item.ItemID] = struct{}{}
		}
		for _, id := range receipt.KnowledgeItemIDs {
			if _, exists := preparedKnowledgeIDs[id]; !exists {
				return errors.New("receipt Knowledge item identity does not match prepared evidence")
			}
		}
	}
	if len(receipt.TopicOutcomes) > maxGmailTopicOutcomes {
		return errors.New("receipt topic outcome bound exceeded")
	}
	if err := validateGmailTopicOutcomes(receipt.TopicOutcomes); err != nil {
		return err
	}
	return nil
}

func validatePreparedGmailRequest(receipt GmailReceipt, index int, request appbacklog.IntakeRequest) error {
	class := classifyGmailSubject(receipt.Message.Subject)
	if class == gmailSubjectSkipped || (class == gmailSubjectDirect && len(request.AcceptanceCriteria) != 0) || (class == gmailSubjectDaily && len(request.AcceptanceCriteria) == 0) {
		return errors.New("receipt prepared request does not match the current subject policy")
	}
	if request.BacklogItemID != "" || strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Purpose) == "" || strings.TrimSpace(request.Body) == "" {
		return errors.New("receipt prepared request is incomplete")
	}
	if !utf8.ValidString(request.Title) || !utf8.ValidString(request.Purpose) || !utf8.ValidString(request.Body) || len([]byte(request.Body)) > maxGmailTextBytes {
		return errors.New("receipt prepared request text is invalid")
	}
	if class == gmailSubjectDirect && (request.Title != receipt.Message.Subject || request.Body != receipt.Message.Text) {
		return errors.New("direct Gmail request must preserve original message")
	}
	if request.Owner != "shiro" || request.OwnerModule != domainbacklog.LifecycleOwnerModule || request.Source != gmailLocator(receipt.Account, receipt.Message.ID) {
		return errors.New("receipt prepared request owner or source is invalid")
	}
	if err := appbacklog.ValidateSpecificationRefs(request.SpecificationRefs); err != nil {
		return err
	}
	if len(request.SourceRefs) == 0 {
		return errors.New("receipt prepared request source references are required")
	}
	if len(request.SourceRefs) > 16 {
		return errors.New("receipt prepared request source reference bound exceeded")
	}
	if len(request.AcceptanceCriteria) == 0 {
		if len(receipt.Prepared) != 1 || request.Purpose != gmailIntakePurpose {
			return errors.New("receipt prepared request acceptance criteria are invalid")
		}
	} else if !hasNonEmptyAcceptance(request.AcceptanceCriteria) {
		return errors.New("receipt prepared request acceptance criteria are invalid")
	}
	for _, criterion := range request.AcceptanceCriteria {
		if !utf8.ValidString(criterion) || len([]byte(criterion)) > maxGmailTextBytes {
			return errors.New("receipt acceptance criterion is invalid")
		}
	}
	expectedGmailLocator := gmailLocator(receipt.Account, receipt.Message.ID)
	messageHash, _ := normalizedSHA256(receipt.Message.ContentHash)
	gmailRefs, webRefs := 0, 0
	for _, ref := range request.SourceRefs {
		if err := validateGmailReceiptSourceRef(ref); err != nil {
			return err
		}
		switch ref.Type {
		case "gmail":
			gmailRefs++
			refHash, _ := normalizedSHA256(ref.ContentHash)
			if refHash != messageHash {
				return errors.New("receipt Gmail source hash does not match message")
			}
			wantLocator := expectedGmailLocator
			if len(request.AcceptanceCriteria) != 0 {
				wantLocator += "#topic=" + strconv.Itoa(index+1)
			}
			if ref.Locator != wantLocator || ref.CapturedAt != receipt.Message.ReceivedAt {
				return errors.New("receipt Gmail source locator is invalid")
			}
		case "web":
			webRefs++
		}
	}
	if gmailRefs != 1 {
		return errors.New("receipt must contain exactly one Gmail source reference")
	}
	if len(request.AcceptanceCriteria) == 0 && webRefs != 0 {
		return errors.New("direct Gmail request must not contain web source references")
	}
	if len(request.AcceptanceCriteria) != 0 && webRefs == 0 {
		return errors.New("verified Gmail request requires fetched web evidence")
	}
	return nil
}

func validateGmailReceiptSourceRef(ref domainbacklog.SourceRef) error {
	if strings.TrimSpace(ref.Locator) != ref.Locator || ref.Locator == "" || strings.ContainsAny(ref.Locator, "\x00\r\n") {
		return errors.New("receipt source reference locator is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, ref.CapturedAt); err != nil {
		return errors.New("receipt source reference timestamp is invalid")
	}
	if !utf8.ValidString(ref.RawOrSummary) || len([]byte(ref.RawOrSummary)) > maxGmailSourceSummary || strings.ContainsRune(ref.RawOrSummary, '\x00') {
		return errors.New("receipt source reference summary is invalid")
	}
	if _, err := normalizedSHA256(ref.ContentHash); err != nil {
		return errors.New("receipt source reference hash is invalid")
	}
	switch ref.Type {
	case "gmail":
		if !strings.HasPrefix(ref.Locator, "gmail://") || ref.Strength != "" {
			return errors.New("Gmail source reference is invalid")
		}
	case "web":
		if ref.Strength != "primary" && ref.Strength != "secondary" {
			return errors.New("web source reference strength is invalid")
		}
		if _, err := modulewebgather.NormalizeURL(ref.Locator, false); err != nil {
			return errors.New("web source reference locator is invalid")
		}
	default:
		return errors.New("receipt source reference type is invalid")
	}
	return nil
}

func validateStoredReceipt(receipt GmailReceipt, account string, message GmailMessage) error {
	storedHash, storedHashErr := normalizedSHA256(receipt.Message.ContentHash)
	incomingHash, incomingHashErr := normalizedSHA256(message.ContentHash)
	if err := ValidateGmailReceipt(receipt); err != nil {
		return err
	}
	if !strings.EqualFold(receipt.Account, account) || receipt.Message.ID != message.ID || receipt.Message.ThreadID != message.ThreadID || storedHashErr != nil || incomingHashErr != nil || storedHash != incomingHash {
		return errors.New("Gmail receipt does not match the selected message")
	}
	return nil
}

func (g *GmailIntake) validateReceiptKnowledgeOwner(receipt GmailReceipt) error {
	if g == nil || strings.TrimSpace(g.userID) == "" {
		return errors.New("Gmail configured Knowledge owner is unavailable")
	}
	for _, prepared := range receipt.PreparedKnowledge {
		if prepared.Item.UserID != g.userID {
			return errors.New("prepared Gmail Knowledge item user does not match the configured owner")
		}
	}
	return nil
}

type gmailSubjectClass string

const (
	gmailSubjectDaily   gmailSubjectClass = "daily"
	gmailSubjectDirect  gmailSubjectClass = "direct"
	gmailSubjectSkipped gmailSubjectClass = "skipped"
)

func classifyGmailSubject(subject string) gmailSubjectClass {
	lower := strings.ToLower(subject)
	if strings.Contains(lower, "rencrow") && strings.Contains(lower, "backlog") {
		return gmailSubjectDirect
	}
	if strings.Contains(lower, strings.ToLower(gmailDailySubject)) {
		return gmailSubjectDaily
	}
	return gmailSubjectSkipped
}

func directGmailRequest(account string, message GmailMessage) appbacklog.IntakeRequest {
	locator := gmailLocator(account, message.ID)
	return appbacklog.IntakeRequest{Kind: "idea", Title: message.Subject, Body: message.Text, Purpose: gmailIntakePurpose, Source: locator, SourceRefs: []domainbacklog.SourceRef{gmailSourceRef(message, locator)}, Owner: "shiro", OwnerModule: domainbacklog.LifecycleOwnerModule}
}

func gmailSourceRef(message GmailMessage, locator string) domainbacklog.SourceRef {
	hash, _ := normalizedSHA256(message.ContentHash)
	return domainbacklog.SourceRef{Type: "gmail", Locator: locator, ContentHash: hash, CapturedAt: message.ReceivedAt, RawOrSummary: "Gmail message source"}
}

func gmailLocator(account, id string) string {
	accountSegment := strings.ReplaceAll(url.QueryEscape(strings.TrimSpace(account)), "+", "%20")
	return "gmail://" + accountSegment + "/messages/" + url.PathEscape(strings.TrimSpace(id))
}

func normalizedSHA256(value string) (string, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(strings.ToLower(value), "sha256:") {
		value = strings.TrimSpace(value[len("sha256:"):])
	}
	if len(value) != sha256.Size*2 {
		return "", errors.New("sha256 must be 64 hexadecimal characters")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return "", errors.New("sha256 is not hexadecimal")
	}
	return strings.ToLower(value), nil
}

func isGmailHexID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

// appendUniqueStrings is a local deterministic collection helper for Gmail
// report and receipt IDs. Backlog lifecycle helpers remain package-private.
func appendUniqueStrings(existing, incoming []string) []string {
	out := append([]string(nil), existing...)
	seen := make(map[string]struct{}, len(out)+len(incoming))
	for _, value := range out {
		seen[value] = struct{}{}
	}
	for _, value := range incoming {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func hasNonEmptyAcceptance(values []string) bool {
	if len(values) == 0 {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func cleanAcceptance(values []string) []string {
	cleaned := make([]string, 0, len(values))
	for _, value := range values {
		cleaned = append(cleaned, strings.TrimSpace(value))
	}
	return cleaned
}

func sanitizeReason(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if !utf8.ValidString(value) {
		return fallback
	}
	value = strings.Join(strings.Fields(value), " ")
	if len([]byte(value)) > maxGmailReasonBytes {
		value = truncateUTF8(value, maxGmailReasonBytes)
	}
	return value
}

func sanitizeSourceSummary(value string) string {
	value = strings.TrimSpace(value)
	if !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n") {
		return ""
	}
	return truncateUTF8(value, maxGmailSourceSummary)
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	raw := []byte(value)
	if len(raw) <= maxBytes {
		return value
	}
	raw = raw[:maxBytes]
	for len(raw) > 0 && !utf8.Valid(raw) {
		raw = raw[:len(raw)-1]
	}
	return string(raw)
}

func cloneGmailMessage(message GmailMessage) GmailMessage { return message }

func cloneIntakeRequest(request appbacklog.IntakeRequest) appbacklog.IntakeRequest {
	request.ExpectedEffect = append([]string(nil), request.ExpectedEffect...)
	request.RelationRefs = append([]string(nil), request.RelationRefs...)
	request.TargetModules = append([]string(nil), request.TargetModules...)
	request.ConsumerModules = append([]string(nil), request.ConsumerModules...)
	request.AffectedModules = append([]string(nil), request.AffectedModules...)
	request.AcceptanceCriteria = append([]string(nil), request.AcceptanceCriteria...)
	request.SourceRefs = append([]domainbacklog.SourceRef(nil), request.SourceRefs...)
	request.SpecificationRefs = append([]string(nil), request.SpecificationRefs...)
	request.Tags = append([]string(nil), request.Tags...)
	request.DependsOn = append([]string(nil), request.DependsOn...)
	request.RelatedIDs = append([]string(nil), request.RelatedIDs...)
	return request
}

func cloneIntakeRequests(requests []appbacklog.IntakeRequest) []appbacklog.IntakeRequest {
	if len(requests) == 0 {
		return nil
	}
	cloned := make([]appbacklog.IntakeRequest, 0, len(requests))
	for _, request := range requests {
		cloned = append(cloned, cloneIntakeRequest(request))
	}
	return cloned
}
