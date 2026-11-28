package taskmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	researchAdmissionOperationVersion = "research-admission.v1"
	researchAdmissionOperationPrefix  = "research-admission.v1:"
	researchAdmissionTitle            = "Research request"
)

var (
	ErrResearchAdmissionReceiptCorrupt  = errors.New("Research admission receipt is corrupt")
	ErrResearchAdmissionReceiptConflict = errors.New("Research admission receipt conflicts with its Task or Run")
)

type researchAdmissionStore interface {
	ExecuteIdempotentGlobalTaskOperation(
		context.Context,
		string,
		string,
		func(Store) (json.RawMessage, error),
	) (json.RawMessage, error)
}

type researchAdmissionReader interface {
	LookupGlobalTaskOperation(context.Context, string, string) (json.RawMessage, bool, error)
}

type researchAdmissionRequest struct {
	Version       string `json:"version"`
	OwnerID       string `json:"owner_id"`
	RequestDigest string `json:"request_digest"`
	Mode          string `json:"mode"`
}

// ResearchAdmissionReceipt is the payload-free identity receipt for one
// admitted local Research Task. The current request body and client key are
// intentionally excluded.
type ResearchAdmissionReceipt struct {
	TaskID         modulecore.TaskID    `json:"task_id"`
	InitialRunID   modulecore.RunID     `json:"initial_run_id"`
	TraceID        modulecore.TraceID   `json:"trace_id"`
	RequestID      modulecore.RequestID `json:"request_id"`
	PolicyRevision string               `json:"policy_revision"`
	RequestDigest  string               `json:"request_digest"`
	CreatedAt      time.Time            `json:"created_at"`
}

// EnsureResearchAdmission creates a neutral local Research Task and its first
// Run once per owner-bound opaque client key. The caller supplies only trusted
// owner/policy metadata and a digest of its already-normalized request; request
// contents are never copied into Task or SharedRoleContext.
func (m *Manager) EnsureResearchAdmission(
	ctx context.Context,
	trustedOwnerID string,
	opaqueClientKey string,
	requestDigest string,
	policyRevision string,
	mode string,
) (ResearchAdmissionReceipt, bool, error) {
	if m == nil || m.store == nil {
		return ResearchAdmissionReceipt{}, false, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return ResearchAdmissionReceipt{}, false, errors.New("Research admission context is nil")
	}
	if err := ctx.Err(); err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	if err := validateResearchPolicyRevision(policyRevision); err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	operationID, requestHash, request, err := normalizeResearchAdmission(trustedOwnerID, opaqueClientKey, requestDigest, mode)
	if err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	reader, ok := m.store.(researchAdmissionReader)
	if !ok {
		return ResearchAdmissionReceipt{}, false, ErrGlobalTaskOperationReadUnsupported
	}
	if encoded, found, err := reader.LookupGlobalTaskOperation(ctx, operationID, requestHash); err != nil {
		return ResearchAdmissionReceipt{}, false, err
	} else if found {
		receipt, err := decodeResearchAdmissionReceipt(encoded)
		if err != nil {
			return ResearchAdmissionReceipt{}, false, err
		}
		if err := m.readResearchAdmissionReceipt(ctx, request.OwnerID, receipt); err != nil {
			return ResearchAdmissionReceipt{}, false, err
		}
		return receipt, false, nil
	}
	store, ok := m.store.(researchAdmissionStore)
	if !ok {
		return ResearchAdmissionReceipt{}, false, ErrGlobalTaskOperationUnsupported
	}
	created := false
	encoded, err := store.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, requestHash, func(tx Store) (json.RawMessage, error) {
		if tx == nil {
			return nil, errors.New("global Task transaction store is unavailable")
		}
		txManager := &Manager{
			store:                    tx,
			limits:                   m.limits,
			expectedCriteriaRevision: m.expectedCriteriaRevision,
			idIndexed:                m.idIndexed,
			now:                      m.now,
		}
		traceID := modulecore.NewTraceID()
		requestID := modulecore.NewRequestID()
		draft := domaintask.Task{
			Title: researchAdmissionTitle, Route: domaintask.RouteResearch,
			OwnerID: request.OwnerID, Assignee: request.OwnerID,
			Priority: domaintask.PriorityNormal,
			ResearchBinding: &domaintask.ResearchExecutionBinding{
				SchemaVersion: domaintask.ResearchExecutionBindingSchema,
				DataScope:     "user", OwnerID: request.OwnerID, OwnerRoute: "core.research.local",
			},
		}
		createdTask, err := txManager.createInTransaction(ctx, draft, domaintask.SharedRoleContext{})
		if err != nil {
			return nil, err
		}
		started, run, err := txManager.startWithReasonAndCheckpointAndTrace(
			ctx, createdTask.TaskID, domaintask.RunStartReasonFirst, "", traceID,
		)
		if err != nil {
			return nil, err
		}
		createdAt := time.Now().UTC()
		if txManager.now != nil {
			createdAt = txManager.now().UTC()
		}
		receipt := ResearchAdmissionReceipt{
			TaskID: started.TaskID, InitialRunID: run.RunID,
			TraceID: traceID, RequestID: requestID,
			PolicyRevision: policyRevision, RequestDigest: request.RequestDigest,
			CreatedAt: createdAt,
		}
		if err := validateResearchAdmissionReceipt(receipt); err != nil {
			return nil, err
		}
		result, err := json.Marshal(receipt)
		if err != nil {
			return nil, fmt.Errorf("encode Research admission receipt: %w", err)
		}
		created = true
		return result, nil
	})
	if err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	receipt, err := decodeResearchAdmissionReceipt(encoded)
	if err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	if err := m.readResearchAdmissionReceipt(ctx, request.OwnerID, receipt); err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	return receipt, created, nil
}

// LookupResearchAdmission only reads the global operation receipt. It never
// invokes the Task creation transaction or issues replacement identities.
func (m *Manager) LookupResearchAdmission(
	ctx context.Context,
	trustedOwnerID string,
	opaqueClientKey string,
	requestDigest string,
	mode string,
) (ResearchAdmissionReceipt, bool, error) {
	if m == nil || m.store == nil {
		return ResearchAdmissionReceipt{}, false, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return ResearchAdmissionReceipt{}, false, errors.New("Research admission lookup context is nil")
	}
	if err := ctx.Err(); err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	operationID, requestHash, request, err := normalizeResearchAdmission(trustedOwnerID, opaqueClientKey, requestDigest, mode)
	if err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	reader, ok := m.store.(researchAdmissionReader)
	if !ok {
		return ResearchAdmissionReceipt{}, false, ErrGlobalTaskOperationReadUnsupported
	}
	encoded, found, err := reader.LookupGlobalTaskOperation(ctx, operationID, requestHash)
	if err != nil || !found {
		return ResearchAdmissionReceipt{}, found, err
	}
	receipt, err := decodeResearchAdmissionReceipt(encoded)
	if err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	if err := m.readResearchAdmissionReceipt(ctx, request.OwnerID, receipt); err != nil {
		return ResearchAdmissionReceipt{}, false, err
	}
	return receipt, true, nil
}

func normalizeResearchAdmission(ownerID, clientKey, requestDigest, mode string) (string, string, researchAdmissionRequest, error) {
	if err := domainsession.ValidateOwnerID(ownerID); err != nil {
		return "", "", researchAdmissionRequest{}, fmt.Errorf("trusted owner ID is invalid: %w", err)
	}
	if len(clientKey) == 0 || len(clientKey) > 256 || strings.TrimSpace(clientKey) != clientKey {
		return "", "", researchAdmissionRequest{}, errors.New("opaque client key must contain 1 to 256 normalized bytes")
	}
	for i := 0; i < len(clientKey); i++ {
		if clientKey[i] < 0x21 || clientKey[i] > 0x7e {
			return "", "", researchAdmissionRequest{}, errors.New("opaque client key must be printable ASCII")
		}
	}
	if err := validateResearchDigest(requestDigest); err != nil {
		return "", "", researchAdmissionRequest{}, err
	}
	switch mode {
	case "search_only", "collect", "investigate", "refresh", "index_build":
	default:
		return "", "", researchAdmissionRequest{}, fmt.Errorf("Research admission mode %q is not supported", mode)
	}
	request := researchAdmissionRequest{
		Version: researchAdmissionOperationVersion, OwnerID: ownerID,
		RequestDigest: requestDigest, Mode: mode,
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return "", "", researchAdmissionRequest{}, fmt.Errorf("encode Research admission identity: %w", err)
	}
	requestSum := sha256.Sum256(requestBytes)
	keySum := sha256.Sum256([]byte(researchAdmissionOperationVersion + "\x00" + ownerID + "\x00" + clientKey))
	return researchAdmissionOperationPrefix + hex.EncodeToString(keySum[:]), hex.EncodeToString(requestSum[:]), request, nil
}

func validateResearchPolicyRevision(revision string) error {
	if len(revision) == 0 || len(revision) > 256 || strings.TrimSpace(revision) != revision {
		return errors.New("server policy revision must contain 1 to 256 normalized bytes")
	}
	for i := 0; i < len(revision); i++ {
		if revision[i] < 0x21 || revision[i] > 0x7e {
			return errors.New("server policy revision must be printable ASCII")
		}
	}
	return nil
}

func validateResearchDigest(digest string) error {
	if len(digest) != sha256.Size*2 || digest != strings.ToLower(digest) {
		return errors.New("request digest must be lowercase SHA-256 hex")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("request digest must be lowercase SHA-256 hex")
	}
	return nil
}

func validateResearchAdmissionReceipt(receipt ResearchAdmissionReceipt) error {
	if receipt.TaskID.Validate() != nil || receipt.InitialRunID.Validate() != nil ||
		receipt.TraceID.Validate() != nil || receipt.RequestID.Validate() != nil ||
		validateResearchPolicyRevision(receipt.PolicyRevision) != nil ||
		validateResearchDigest(receipt.RequestDigest) != nil || receipt.CreatedAt.IsZero() {
		return ErrResearchAdmissionReceiptCorrupt
	}
	return nil
}

func decodeResearchAdmissionReceipt(encoded json.RawMessage) (ResearchAdmissionReceipt, error) {
	var receipt ResearchAdmissionReceipt
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return ResearchAdmissionReceipt{}, fmt.Errorf("%w: %v", ErrResearchAdmissionReceiptCorrupt, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ResearchAdmissionReceipt{}, fmt.Errorf("%w: trailing JSON", ErrResearchAdmissionReceiptCorrupt)
	}
	if err := validateResearchAdmissionReceipt(receipt); err != nil {
		return ResearchAdmissionReceipt{}, err
	}
	return receipt, nil
}

func (m *Manager) readResearchAdmissionReceipt(ctx context.Context, ownerID string, receipt ResearchAdmissionReceipt) error {
	return m.readTransaction(ctx, func(txManager *Manager) error {
		task, err := txManager.store.GetTask(ctx, receipt.TaskID)
		if err != nil {
			return fmt.Errorf("read Research Task from admission receipt: %w", err)
		}
		run, err := txManager.store.GetRun(ctx, receipt.InitialRunID)
		if err != nil {
			return fmt.Errorf("read Research Run from admission receipt: %w", err)
		}
		shared, err := txManager.store.GetContext(ctx, receipt.TaskID)
		if err != nil {
			return fmt.Errorf("read Research Task context from admission receipt: %w", err)
		}
		if task.TaskID != receipt.TaskID || task.Route != domaintask.RouteResearch ||
			task.OwnerID != ownerID || task.Assignee != ownerID || task.PrivateAccess != nil ||
			task.ResearchBinding == nil || task.ResearchBinding.Validate(task) != nil ||
			run.RunID != receipt.InitialRunID || run.TaskID != receipt.TaskID ||
			run.Assignee != ownerID || run.TraceID != receipt.TraceID ||
			run.StartReason != domaintask.RunStartReasonFirst || shared.TaskID != receipt.TaskID {
			return ErrResearchAdmissionReceiptConflict
		}
		return nil
	})
}
