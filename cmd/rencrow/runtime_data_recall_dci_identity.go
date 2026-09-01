package main

import (
	"context"
	"errors"
	"strings"

	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	runtimeDataRecallDCIIdentityStore     = "dci"
	runtimeDataRecallDCIIdentityOperation = "identity_evidence"
)

// runtimeDCIIdentityEvidenceVerifier is the narrow owner boundary used by
// the Worker data.recall adapter. It exposes no DCI store or projection data.
type runtimeDCIIdentityEvidenceVerifier interface {
	VerifyAction(context.Context, modulecore.ActionID) (dcipersistence.IdentityEvidence, error)
}

var (
	errRuntimeDataRecallDCIIdentityUnavailable = errors.New("dci identity evidence recall is unavailable")
	errRuntimeDataRecallDCIIdentityRequest     = errors.New("dci identity evidence request is invalid")
	errRuntimeDataRecallDCIIdentityFailed      = errors.New("dci identity evidence verification failed")
	errRuntimeDataRecallDCIIdentityReceipt     = errors.New("dci identity evidence receipt is invalid")
	errRuntimeDataRecallDCIIdentityMismatch    = errors.New("dci identity evidence action does not match")
)

// registerRuntimeDataRecallDCIIdentityEvidence exposes the accepted DCI
// owner verifier through the internal, read-only data.recall boundary.
func registerRuntimeDataRecallDCIIdentityEvidence(r *runtimeDataRecallRegistry, verifier runtimeDCIIdentityEvidenceVerifier) error {
	if r == nil || verifier == nil {
		return errRuntimeDataRecallDCIIdentityUnavailable
	}
	return r.Register(runtimeDataRecallDCIIdentityStore, runtimeDataRecallDCIIdentityOperation, dataRecallAccessInternal, func(ctx context.Context, q tools.DataRecallRequest) (runtimeDataRecallResult, error) {
		if q.Limit != 1 {
			return runtimeDataRecallResult{}, errRuntimeDataRecallDCIIdentityRequest
		}
		actionID := modulecore.ActionID(strings.TrimSpace(q.Query))
		if err := actionID.Validate(); err != nil {
			return runtimeDataRecallResult{}, errRuntimeDataRecallDCIIdentityRequest
		}
		receipt, err := verifier.VerifyAction(ctx, actionID)
		if err != nil {
			return runtimeDataRecallResult{}, errRuntimeDataRecallDCIIdentityFailed
		}
		if err := dcipersistence.ValidateIdentityEvidence(receipt); err != nil {
			return runtimeDataRecallResult{}, errRuntimeDataRecallDCIIdentityReceipt
		}
		if receipt.ActionID != actionID {
			return runtimeDataRecallResult{}, errRuntimeDataRecallDCIIdentityMismatch
		}
		return newRuntimeDataRecallResult(q.Store, q.Operation, []map[string]any{{
			"schema_version":           receipt.SchemaVersion,
			"status":                   receipt.Status,
			"action_id":                string(receipt.ActionID),
			"trace_id":                 string(receipt.TraceID),
			"actor_kind":               receipt.ActorKind,
			"actor_id":                 receipt.ActorID,
			"search_status":            receipt.SearchStatus,
			"event_count":              receipt.EventCount,
			"step_count":               receipt.StepCount,
			"evidence_count":           receipt.EvidenceCount,
			"current_projection_count": receipt.CurrentProjectionCount,
			"archive_projection_count": receipt.ArchiveProjectionCount,
			"event_graph_sha256":       receipt.EventGraphSHA256,
		}}), nil
	})
}
