package chatgptimport

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const (
	chatGPTImportMaxBatchRecords = 100
	chatGPTImportMaxBatchPayload = int64(domainmemory.CommonRawMaxBatchPayloadSize)
	chatGPTImportSourceType      = "chatgpt_export_source"
	chatGPTImportSourceContent   = "application/octet-stream"
	chatGPTImportAdapter         = "chatgpt-import-service/v1"
	chatGPTMessageRawSourceType  = "chatgpt_export"
	chatGPTMessageRawContentType = "application/vnd.rencrow.chatgpt-raw+json;v=1"
	chatGPTMessageRawAdapter     = "chatgpt-raw-adapter/v1"
	chatGPTImportDefaultTerminal = 2 * time.Second
)

var (
	errChatGPTImportSourceChanged = errors.New("ChatGPT source changed")
	errChatGPTImportPlan          = errors.New("ChatGPT import plan is invalid")
)

// Store is the only persistence boundary used by the ChatGPT import service.
// Implementations must keep the append-only ledger, Common Raw intake, and
// ChatGPT Raw/projection operations behind this domain-only interface.
type Store interface {
	AppendChatGPTImportEvent(context.Context, domainmemory.ChatGPTImportEventInput) (domainmemory.ChatGPTImportEvent, error)
	IntakeCommonRaw(context.Context, string, string, string, domainmemory.CommonRawIntakeRequest) (domainmemory.CommonRawIntakeReceipt, error)
	ImportChatGPTRawBatch(context.Context, string, string, string, domainmemory.ChatGPTRawImportBatch, bool) (domainmemory.ChatGPTRawImportResult, error)
}

// ImportRequest is the authenticated, already-staged import input. StageRoot,
// ManifestPath, and ArtifactPath are private operational inputs and are never
// copied into ImportResult or a public diagnostic.
type ImportRequest struct {
	RequestID    string
	OwnerID      string
	ActorID      string
	StageRoot    string
	ManifestPath string
	ArtifactPath string
	Apply        bool
}

// Request is a descriptive alias for callers that use the shorter request
// name. It intentionally does not add any fields to ImportRequest.
type Request = ImportRequest

// ImportResult contains only the bounded domain status view and replay flag.
// Raw IDs, receipts, payloads, and private staging paths are deliberately not
// part of this result contract.
type ImportResult struct {
	View             domainmemory.ChatGPTImportView
	IdempotentReplay bool
}

// Result is a descriptive alias for callers that use the shorter result name.
type Result = ImportResult

// ServiceOptions controls the verifier and bounded batch planner. Every
// service batch option defaults to the production cap and may only be lowered.
type ServiceOptions struct {
	BundleOptions Options
	// VerifyOptions is accepted as a descriptive alias for BundleOptions. If it
	// is non-zero, it is used for bundle verification.
	VerifyOptions Options

	MaxSourceBatchRecords  int
	MaxMessageBatchRecords int
	MaxBatchPayloadBytes   int64
	TerminalTimeout        time.Duration
}

// ImportOptions is a descriptive alias for ServiceOptions.
type ImportOptions = ServiceOptions

// Service owns verification, bounded planning, preflight, and Raw-first
// execution for one ChatGPT bundle.
type Service struct {
	store   Store
	options ServiceOptions
}

// NewService creates a ChatGPT import service. Invalid option values are
// returned as a safe request error when Import is called, keeping construction
// side-effect free.
func NewService(store Store, options ServiceOptions) *Service {
	return &Service{store: store, options: options}
}

// NewImportService is an explicit constructor alias for integrations that
// prefer the feature name in the constructor.
func NewImportService(store Store, options ServiceOptions) *Service {
	return NewService(store, options)
}

type normalizedServiceOptions struct {
	verify            Options
	maxSourceRecords  int
	maxMessageRecords int
	maxPayloadBytes   int64
}

func (o ServiceOptions) normalized() (normalizedServiceOptions, error) {
	if o.MaxSourceBatchRecords < 0 || o.MaxMessageBatchRecords < 0 || o.MaxBatchPayloadBytes < 0 || o.TerminalTimeout < 0 {
		return normalizedServiceOptions{}, domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInvalid, "import options are invalid")
	}
	if o.MaxSourceBatchRecords > chatGPTImportMaxBatchRecords || o.MaxMessageBatchRecords > chatGPTImportMaxBatchRecords || o.MaxBatchPayloadBytes > chatGPTImportMaxBatchPayload {
		return normalizedServiceOptions{}, domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInvalid, "import options exceed the production bound")
	}
	if o.MaxSourceBatchRecords == 0 {
		o.MaxSourceBatchRecords = chatGPTImportMaxBatchRecords
	}
	if o.MaxMessageBatchRecords == 0 {
		o.MaxMessageBatchRecords = chatGPTImportMaxBatchRecords
	}
	if o.MaxBatchPayloadBytes == 0 {
		o.MaxBatchPayloadBytes = chatGPTImportMaxBatchPayload
	}
	if o.TerminalTimeout == 0 {
		o.TerminalTimeout = chatGPTImportDefaultTerminal
	}
	verify := o.BundleOptions
	if o.VerifyOptions != (Options{}) {
		verify = o.VerifyOptions
	}
	if _, err := verify.normalized(); err != nil {
		return normalizedServiceOptions{}, domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInvalid, "bundle verification options are invalid")
	}
	return normalizedServiceOptions{
		verify: verify, maxSourceRecords: o.MaxSourceBatchRecords,
		maxMessageRecords: o.MaxMessageBatchRecords, maxPayloadBytes: o.MaxBatchPayloadBytes,
	}, nil
}

type importPlan struct {
	binding domainmemory.ChatGPTImportBinding

	sourceCount     int
	fileCount       int
	chunkCount      int
	objectCount     int
	messageCount    int
	messageBatches  int
	sourceBatches   int
	batchCount      int
	jobCount        int
	maxJSONLineSize int64
}

func (p importPlan) ledgerCounts() domainmemory.ChatGPTImportCounts {
	return domainmemory.ChatGPTImportCounts{
		SourceCount: p.sourceCount, FileCount: p.fileCount, ChunkCount: p.chunkCount,
		ObjectCount: p.objectCount, MessageCount: p.messageCount, BatchCount: p.batchCount,
	}
}

func (p importPlan) completedCounts(apply bool) domainmemory.ChatGPTImportCounts {
	counts := p.ledgerCounts()
	if apply {
		counts.RawCount = p.sourceCount + p.messageCount
		counts.ProjectionCount = p.messageCount
		counts.JobCount = p.jobCount
	}
	return counts
}

func domainBinding(binding Binding) (domainmemory.ChatGPTImportBinding, error) {
	result := domainmemory.ChatGPTImportBinding{
		ExportID: binding.ExportID, ManifestSHA256: binding.ManifestSHA256,
		ArtifactSHA256: binding.ArtifactSHA256, ArtifactBytes: binding.ArtifactBytes,
		Format: binding.Format, SchemaVersion: binding.SchemaVersion,
		ConverterVersion: binding.ConverterVersion, SourceFileCount: binding.SourceFileCount,
		SourceChunkCount: binding.SourceChunkCount, SourceObjectCount: binding.SourceObjectCount,
		MessageCount: binding.Messages,
	}
	if err := result.Validate(); err != nil {
		return domainmemory.ChatGPTImportBinding{}, err
	}
	return result, nil
}

func validateImportRequest(request ImportRequest) error {
	for name, value := range map[string]string{
		"request_id": request.RequestID, "owner_id": request.OwnerID, "actor_id": request.ActorID,
	} {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > domainmemory.ChatGPTImportMaxIdentifierByte || !utf8.ValidString(value) || strings.ContainsAny(value, "/\\\r\n\x00") {
			return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInvalid, fmt.Sprintf("%s is invalid", name))
		}
	}
	if strings.TrimSpace(request.StageRoot) == "" || strings.TrimSpace(request.ManifestPath) == "" || strings.TrimSpace(request.ArtifactPath) == "" {
		return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInvalid, "staged bundle inputs are required")
	}
	return nil
}

func (s *Service) Import(ctx context.Context, request ImportRequest) (ImportResult, error) {
	if ctx == nil {
		return ImportResult{}, domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInvalid, "import context is required")
	}
	if s == nil || s.store == nil {
		return ImportResult{}, domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorUnavailable, chatGPTImportUnavailableReason)
	}
	if err := validateImportRequest(request); err != nil {
		return ImportResult{}, err
	}
	options, err := s.options.normalized()
	if err != nil {
		return ImportResult{}, err
	}

	// Verification is deliberately the first bundle operation and happens
	// before the store is consulted. The verifier owns the temporary stage;
	// every return path below closes it.
	bundle, err := VerifyBundle(ctx, request.StageRoot, request.ManifestPath, request.ArtifactPath, options.verify)
	if err != nil {
		return ImportResult{}, safeImportError(err)
	}
	closed := false
	closeBundle := func() error {
		if closed {
			return nil
		}
		closed = true
		return bundle.Close()
	}
	defer func() { _ = closeBundle() }()

	plan, err := buildImportPlan(ctx, bundle, options)
	if err != nil {
		return ImportResult{}, safeImportError(err)
	}
	auditReference, err := importAuditReference(request.OwnerID, plan.binding)
	if err != nil {
		return ImportResult{}, safeImportError(err)
	}
	validatingInput := domainmemory.ChatGPTImportEventInput{
		RequestID: request.RequestID, OwnerID: request.OwnerID, ActorID: request.ActorID,
		Binding: plan.binding, Apply: request.Apply, State: domainmemory.ChatGPTImportStateValidating,
		Counts: plan.ledgerCounts(), Warnings: []string{}, AuditReference: auditReference,
	}
	validatingEvent, err := s.store.AppendChatGPTImportEvent(ctx, validatingInput)
	if err != nil {
		return s.finishFailure(ctx, request, plan, validatingInput, domainmemory.ChatGPTImportStateValidating, err)
	}
	if validatingEvent.State == domainmemory.ChatGPTImportStateCompleted {
		return ImportResult{View: validatingEvent.View(), IdempotentReplay: true}, nil
	}
	if validatingEvent.State != domainmemory.ChatGPTImportStateValidating {
		return s.finishFailure(ctx, request, plan, validatingInput, domainmemory.ChatGPTImportStateValidating, errChatGPTImportPlan)
	}

	if err := s.preflightMessages(ctx, request, bundle, plan, options); err != nil {
		return s.finishFailure(ctx, request, plan, validatingInput, domainmemory.ChatGPTImportStateValidating, err)
	}

	committingInput := validatingInput
	committingInput.State = domainmemory.ChatGPTImportStateCommitting
	committingEvent, err := s.store.AppendChatGPTImportEvent(ctx, committingInput)
	if err != nil {
		return s.finishFailure(ctx, request, plan, validatingInput, domainmemory.ChatGPTImportStateValidating, err)
	}
	if committingEvent.State != domainmemory.ChatGPTImportStateCommitting {
		return s.finishFailure(ctx, request, plan, committingInput, domainmemory.ChatGPTImportStateCommitting, errChatGPTImportPlan)
	}

	if !request.Apply {
		if err := closeBundle(); err != nil {
			return s.finishFailure(ctx, request, plan, committingInput, domainmemory.ChatGPTImportStateCommitting, err)
		}
		completedInput := committingInput
		completedInput.State = domainmemory.ChatGPTImportStateCompleted
		completedInput.Counts = plan.completedCounts(false)
		completedEvent, err := s.store.AppendChatGPTImportEvent(ctx, completedInput)
		if err != nil {
			return s.finishFailure(ctx, request, plan, committingInput, domainmemory.ChatGPTImportStateCommitting, err)
		}
		if completedEvent.State != domainmemory.ChatGPTImportStateCompleted {
			return s.finishFailure(ctx, request, plan, committingInput, domainmemory.ChatGPTImportStateCommitting, errChatGPTImportPlan)
		}
		return ImportResult{View: completedEvent.View()}, nil
	}

	progress := importProgress{counts: plan.ledgerCounts()}
	if err := s.applySources(ctx, request, bundle, plan, options, &progress); err != nil {
		return s.finishFailure(ctx, request, plan, committingInput, domainmemory.ChatGPTImportStateCommitting, errWithProgress{err: err, progress: progress})
	}
	if err := s.applyMessages(ctx, request, bundle, plan, options, &progress); err != nil {
		return s.finishFailure(ctx, request, plan, committingInput, domainmemory.ChatGPTImportStateCommitting, errWithProgress{err: err, progress: progress})
	}
	if err := closeBundle(); err != nil {
		return s.finishFailure(ctx, request, plan, committingInput, domainmemory.ChatGPTImportStateCommitting, errWithProgress{err: err, progress: progress})
	}

	completedInput := committingInput
	completedInput.State = domainmemory.ChatGPTImportStateCompleted
	completedInput.Counts = plan.completedCounts(true)
	completedEvent, err := s.store.AppendChatGPTImportEvent(ctx, completedInput)
	if err != nil {
		return s.finishFailure(ctx, request, plan, committingInput, domainmemory.ChatGPTImportStateCommitting, errWithProgress{err: err, progress: progress})
	}
	if completedEvent.State != domainmemory.ChatGPTImportStateCompleted {
		return s.finishFailure(ctx, request, plan, committingInput, domainmemory.ChatGPTImportStateCommitting, errWithProgress{err: errChatGPTImportPlan, progress: progress})
	}
	return ImportResult{View: completedEvent.View()}, nil
}

// Execute is a descriptive alias for Import.
func (s *Service) Execute(ctx context.Context, request ImportRequest) (ImportResult, error) {
	return s.Import(ctx, request)
}

func importAuditReference(ownerID string, binding domainmemory.ChatGPTImportBinding) (string, error) {
	bindingSHA, err := domainmemory.DeterministicChatGPTImportBindingSHA256(ownerID, binding)
	if err != nil {
		return "", err
	}
	return domainmemory.DeterministicChatGPTImportID(ownerID, bindingSHA), nil
}

type importProgress struct {
	counts domainmemory.ChatGPTImportCounts
}

type errWithProgress struct {
	err      error
	progress importProgress
}

func (e errWithProgress) Error() string { return e.err.Error() }
func (e errWithProgress) Unwrap() error { return e.err }

func (s *Service) finishFailure(ctx context.Context, request ImportRequest, plan importPlan, previous domainmemory.ChatGPTImportEventInput, previousState domainmemory.ChatGPTImportState, cause error) (ImportResult, error) {
	progress := importProgress{counts: previous.Counts}
	var withProgress errWithProgress
	if errors.As(cause, &withProgress) {
		progress = withProgress.progress
		cause = withProgress.err
	}
	state, code, reason := classifyImportFailure(cause)
	terminalInput := previous
	terminalInput.State = state
	terminalInput.Counts = progress.counts
	terminalInput.ErrorCode = string(code)
	terminalInput.FailureReason = reason
	if previousState == domainmemory.ChatGPTImportStateValidating && state == domainmemory.ChatGPTImportStateCompleted {
		state = domainmemory.ChatGPTImportStateBlocked
		terminalInput.State = state
		terminalInput.ErrorCode = string(domainmemory.ChatGPTImportErrorUnavailable)
		terminalInput.FailureReason = chatGPTImportUnavailableReason
	}
	terminalTimeout := s.options.TerminalTimeout
	if terminalTimeout <= 0 {
		terminalTimeout = chatGPTImportDefaultTerminal
	}
	terminalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalTimeout)
	defer cancel()
	event, appendErr := s.store.AppendChatGPTImportEvent(terminalCtx, terminalInput)
	if appendErr != nil {
		return ImportResult{}, domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInternal, chatGPTImportInternalReason)
	}
	if event.State != terminalInput.State || (event.State != domainmemory.ChatGPTImportStateRejected && event.State != domainmemory.ChatGPTImportStateBlocked) {
		return ImportResult{}, domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInternal, chatGPTImportInternalReason)
	}
	return ImportResult{View: event.View()}, domainmemory.NewChatGPTImportError(code, reason)
}

const (
	chatGPTImportInvalidReason       = "ChatGPT import bundle is invalid"
	chatGPTImportTooLargeReason      = "ChatGPT import bundle exceeds the allowed bound"
	chatGPTImportArtifactReason      = "ChatGPT import artifact is invalid"
	chatGPTImportForbiddenReason     = "ChatGPT import is forbidden"
	chatGPTImportConflictReason      = "ChatGPT import conflicts with existing state"
	chatGPTImportSourceChangedReason = "ChatGPT import source changed"
	chatGPTImportUnavailableReason   = "ChatGPT import is unavailable"
	chatGPTImportInternalReason      = "ChatGPT import failed internally"
)

func classifyImportFailure(err error) (domainmemory.ChatGPTImportState, domainmemory.ChatGPTImportErrorCode, string) {
	if err == nil {
		return domainmemory.ChatGPTImportStateBlocked, domainmemory.ChatGPTImportErrorUnavailable, chatGPTImportUnavailableReason
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return domainmemory.ChatGPTImportStateBlocked, domainmemory.ChatGPTImportErrorUnavailable, chatGPTImportUnavailableReason
	}
	if errors.Is(err, ErrBounds) || errors.Is(err, domainmemory.ErrChatGPTImportTooLarge) {
		return domainmemory.ChatGPTImportStateRejected, domainmemory.ChatGPTImportErrorTooLarge, chatGPTImportTooLargeReason
	}
	if errors.Is(err, errChatGPTImportSourceChanged) || errors.Is(err, domainmemory.ErrChatGPTImportSourceChanged) || errors.Is(err, domainmemory.ErrCommonRawSourceChanged) {
		return domainmemory.ChatGPTImportStateRejected, domainmemory.ChatGPTImportErrorSourceChanged, chatGPTImportSourceChangedReason
	}
	if errors.Is(err, domainmemory.ErrChatGPTImportForbidden) || errors.Is(err, domainmemory.ErrCommonRawForbidden) {
		return domainmemory.ChatGPTImportStateRejected, domainmemory.ChatGPTImportErrorForbidden, chatGPTImportForbiddenReason
	}
	if errors.Is(err, domainmemory.ErrChatGPTImportConflict) || errors.Is(err, domainmemory.ErrCommonRawConflict) {
		return domainmemory.ChatGPTImportStateRejected, domainmemory.ChatGPTImportErrorConflict, chatGPTImportConflictReason
	}
	if errors.Is(err, ErrInvalidManifest) || errors.Is(err, domainmemory.ErrChatGPTImportInvalid) || errors.Is(err, domainmemory.ErrCommonRawInvalid) || errors.Is(err, domainmemory.ErrCommonRawSchema) {
		return domainmemory.ChatGPTImportStateRejected, domainmemory.ChatGPTImportErrorInvalid, chatGPTImportInvalidReason
	}
	if errors.Is(err, ErrInvalidBundle) || errors.Is(err, domainmemory.ErrChatGPTImportArtifactInvalid) || errors.Is(err, domainmemory.ErrCommonRawObject) {
		return domainmemory.ChatGPTImportStateRejected, domainmemory.ChatGPTImportErrorArtifactInvalid, chatGPTImportArtifactReason
	}
	if errors.Is(err, domainmemory.ErrCommonRawRoot) || errors.Is(err, domainmemory.ErrCommonRawUnavailable) || errors.Is(err, domainmemory.ErrChatGPTImportUnavailable) {
		return domainmemory.ChatGPTImportStateBlocked, domainmemory.ChatGPTImportErrorUnavailable, chatGPTImportUnavailableReason
	}
	if errors.Is(err, domainmemory.ErrChatGPTImportInternal) {
		return domainmemory.ChatGPTImportStateBlocked, domainmemory.ChatGPTImportErrorInternal, chatGPTImportInternalReason
	}
	return domainmemory.ChatGPTImportStateBlocked, domainmemory.ChatGPTImportErrorInternal, chatGPTImportInternalReason
}

func safeImportError(err error) error {
	_, code, reason := classifyImportFailure(err)
	return domainmemory.NewChatGPTImportError(code, reason)
}
