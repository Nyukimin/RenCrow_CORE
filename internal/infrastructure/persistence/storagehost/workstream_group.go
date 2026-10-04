package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	persistworkstream "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
)

const GroupWorkstream = "workstream"

const (
	workstreamOpListWorkstreams              = "list_workstreams"
	workstreamOpListGoals                    = "list_goals"
	workstreamOpListArtifacts                = "list_artifacts"
	workstreamOpListArtifactNotes            = "list_artifact_annotations"
	workstreamOpListSteeringItems            = "list_steering_items"
	workstreamOpListHeartbeatSchedule        = "list_heartbeat_schedules"
	workstreamOpListVaultUpdateLogs          = "list_vault_update_logs"
	workstreamOpFindGoal                     = "find_goal_by_id"
	workstreamOpSaveGoal                     = persistworkstream.WorkstreamSaveGoalStorageHostOperation
	workstreamOpGetQueueFreeze               = "get_queue_freeze"
	workstreamOpListQueueFreezes             = "list_queue_freezes"
	workstreamOpFindStageRunReceipt          = "find_stage_run_receipt"
	workstreamOpListStageRunReceipts         = "list_stage_run_receipts"
	workstreamOpFindClosureReceipt           = "find_closure_receipt"
	workstreamOpListClosureReceipts          = "list_closure_receipts"
	workstreamOpGetImplementationLease       = "get_implementation_lease"
	workstreamOpAcquireImplementationLease   = persistworkstream.WorkstreamAcquireLeaseStorageHostOperation
	workstreamOpResolveQueueFreeze           = persistworkstream.WorkstreamResolveFreezeStorageHostOperation
	workstreamOpReleaseImplementationLease   = persistworkstream.WorkstreamReleaseLeaseStorageHostOperation
	workstreamOpHeartbeatImplementationLease = persistworkstream.WorkstreamHeartbeatLeaseStorageHostOperation
)

type WorkstreamGroupOwner interface {
	ListWorkstreams(context.Context, int) ([]domainworkstream.Workstream, error)
	ListGoals(context.Context, int) ([]domainworkstream.Goal, error)
	ListArtifacts(context.Context, int) ([]domainworkstream.Artifact, error)
	ListArtifactAnnotations(context.Context, int) ([]domainworkstream.ArtifactAnnotation, error)
	ListSteeringItems(context.Context, int) ([]domainworkstream.SteeringItem, error)
	ListHeartbeatSchedules(context.Context, int) ([]domainworkstream.HeartbeatSchedule, error)
	ListVaultUpdateLogs(context.Context, int) ([]domainworkstream.VaultUpdateLog, error)
	FindGoalByID(context.Context, string) (domainworkstream.Goal, bool, error)
	GetQueueFreeze(context.Context, string) (domainworkstream.QueueFreeze, bool, error)
	ListQueueFreezes(context.Context, int) ([]domainworkstream.QueueFreeze, error)
	FindStageRunReceipt(context.Context, string) (domainworkstream.StageRunReceipt, bool, error)
	ListStageRunReceipts(context.Context, int) ([]domainworkstream.StageRunReceipt, error)
	FindClosureReceipt(context.Context, string) (domainworkstream.ClosureReceipt, bool, error)
	ListClosureReceipts(context.Context, int) ([]domainworkstream.ClosureReceipt, error)
	GetImplementationLease(context.Context, string) (domainworkstream.ImplementationLease, bool, error)
	SaveGoalForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.Goal) (domainworkstream.Goal, error)
	SaveWorkstreamRowForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.Workstream) (domainworkstream.Workstream, error)
	SaveArtifactForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.Artifact) (domainworkstream.Artifact, error)
	SaveArtifactAnnotationForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.ArtifactAnnotation) (domainworkstream.ArtifactAnnotation, error)
	SaveSteeringItemForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.SteeringItem) (domainworkstream.SteeringItem, error)
	SaveHeartbeatScheduleForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.HeartbeatSchedule) (domainworkstream.HeartbeatSchedule, error)
	SaveVaultUpdateLogForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.VaultUpdateLog) (domainworkstream.VaultUpdateLog, error)
	SaveQueueFreezeForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.QueueFreeze) (domainworkstream.QueueFreeze, error)
	SaveStageRunReceiptForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.StageRunReceipt) (domainworkstream.StageRunReceipt, error)
	SaveClosureReceiptForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.ClosureReceipt) (domainworkstream.ClosureReceipt, error)
	LookupWorkstreamStorageHostOperationResult(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, string, string, string) (json.RawMessage, bool, error)
	AcquireImplementationLeaseIfUnfrozenForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.ImplementationLease) (persistworkstream.WorkstreamLeaseAcquireResult, error)
	ResolveQueueFreezeAndAcquireLeaseForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, string, domainworkstream.QueueFreezeResolution, domainworkstream.ImplementationLease) (persistworkstream.WorkstreamFreezeLeaseResult, error)
	ReleaseImplementationLeaseForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, string, string) error
	HeartbeatImplementationLeaseForStorageHostOperation(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.ImplementationLease) error
	LookupWorkstreamStorageHostOperationProof(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, string) (json.RawMessage, bool, error)
	LookupWorkstreamStorageHostOperationReceipt(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, domainworkstream.Goal) (json.RawMessage, bool, error)
}

type WorkstreamClient struct{ client *Client }

func NewWorkstreamClient(client *Client) *WorkstreamClient {
	return &WorkstreamClient{client: client}
}

type workstreamListPayload struct {
	Limit int `json:"limit"`
}

type workstreamGoalPayload struct {
	Goal domainworkstream.Goal `json:"goal"`
}

type workstreamFindGoalPayload struct {
	GoalID string `json:"goal_id"`
}

type workstreamFindGoalResult struct {
	Goal  domainworkstream.Goal `json:"goal"`
	Found bool                  `json:"found"`
}

type workstreamFindQueueFreezePayload struct {
	FreezeID string `json:"freeze_id"`
}

type workstreamFindQueueFreezeResult struct {
	Freeze domainworkstream.QueueFreeze `json:"freeze"`
	Found  bool                         `json:"found"`
}

type workstreamFindReceiptPayload struct {
	Key string `json:"key"`
}

type workstreamFindStageRunReceiptResult struct {
	Receipt domainworkstream.StageRunReceipt `json:"receipt"`
	Found   bool                             `json:"found"`
}

type workstreamFindClosureReceiptResult struct {
	Receipt domainworkstream.ClosureReceipt `json:"receipt"`
	Found   bool                            `json:"found"`
}

type workstreamGetLeasePayload struct {
	LeaseName string `json:"lease_name"`
}

type workstreamGetLeaseResult struct {
	Lease domainworkstream.ImplementationLease `json:"lease"`
	Found bool                                 `json:"found"`
}

type workstreamSavePayload[T any] struct {
	Item T `json:"item"`
}

type workstreamLeasePayload struct {
	Lease domainworkstream.ImplementationLease `json:"lease"`
}

type workstreamResolveFreezePayload struct {
	FreezeID    string                                 `json:"freeze_id"`
	Resolution  domainworkstream.QueueFreezeResolution `json:"resolution"`
	Replacement domainworkstream.ImplementationLease   `json:"replacement_lease"`
}

type workstreamReleaseLeasePayload struct {
	LeaseName    string `json:"lease_name"`
	HolderUnitID string `json:"holder_unit_id"`
}

func RegisterWorkstreamGroup(handler *Handler, owner WorkstreamGroupOwner) error {
	if handler == nil || owner == nil {
		return errors.New("storagehost: workstream owner is required")
	}
	registrations := []func() error{
		func() error {
			return registerWorkstreamList(handler, workstreamOpListWorkstreams, owner.ListWorkstreams)
		},
		func() error { return registerWorkstreamList(handler, workstreamOpListGoals, owner.ListGoals) },
		func() error { return registerWorkstreamList(handler, workstreamOpListArtifacts, owner.ListArtifacts) },
		func() error {
			return registerWorkstreamList(handler, workstreamOpListArtifactNotes, owner.ListArtifactAnnotations)
		},
		func() error {
			return registerWorkstreamList(handler, workstreamOpListSteeringItems, owner.ListSteeringItems)
		},
		func() error {
			return registerWorkstreamList(handler, workstreamOpListHeartbeatSchedule, owner.ListHeartbeatSchedules)
		},
		func() error {
			return registerWorkstreamList(handler, workstreamOpListVaultUpdateLogs, owner.ListVaultUpdateLogs)
		},
		func() error {
			return registerWorkstreamList(handler, workstreamOpListQueueFreezes, owner.ListQueueFreezes)
		},
		func() error {
			return registerWorkstreamList(handler, workstreamOpListStageRunReceipts, owner.ListStageRunReceipts)
		},
		func() error {
			return registerWorkstreamList(handler, workstreamOpListClosureReceipts, owner.ListClosureReceipts)
		},
	}
	for _, register := range registrations {
		if err := register(); err != nil {
			return err
		}
	}
	if err := handler.Register(GroupWorkstream, workstreamOpFindGoal, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeWorkstreamPayload[workstreamFindGoalPayload](raw)
		if err != nil || payload.GoalID == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "workstream goal lookup request rejected")
		}
		goal, found, err := owner.FindGoalByID(ctx, payload.GoalID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "workstream goal lookup failed")
		}
		return workstreamFindGoalResult{Goal: goal, Found: found}, nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupWorkstream, workstreamOpGetQueueFreeze, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeWorkstreamPayload[workstreamFindQueueFreezePayload](raw)
		if err != nil || payload.FreezeID == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "workstream queue freeze lookup request rejected")
		}
		freeze, found, err := owner.GetQueueFreeze(ctx, payload.FreezeID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "workstream queue freeze lookup failed")
		}
		return workstreamFindQueueFreezeResult{Freeze: freeze, Found: found}, nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupWorkstream, workstreamOpFindStageRunReceipt, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeWorkstreamPayload[workstreamFindReceiptPayload](raw)
		if err != nil || payload.Key == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "workstream stage receipt lookup request rejected")
		}
		receipt, found, err := owner.FindStageRunReceipt(ctx, payload.Key)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "workstream stage receipt lookup failed")
		}
		return workstreamFindStageRunReceiptResult{Receipt: receipt, Found: found}, nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupWorkstream, workstreamOpFindClosureReceipt, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeWorkstreamPayload[workstreamFindReceiptPayload](raw)
		if err != nil || payload.Key == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "workstream closure receipt lookup request rejected")
		}
		receipt, found, err := owner.FindClosureReceipt(ctx, payload.Key)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "workstream closure receipt lookup failed")
		}
		return workstreamFindClosureReceiptResult{Receipt: receipt, Found: found}, nil
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupWorkstream, workstreamOpGetImplementationLease, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeWorkstreamPayload[workstreamGetLeasePayload](raw)
		if err != nil || payload.LeaseName == "" {
			return nil, NewError(ErrorCodeSchemaRejected, "workstream implementation lease lookup request rejected")
		}
		lease, found, err := owner.GetImplementationLease(ctx, payload.LeaseName)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "workstream implementation lease lookup failed")
		}
		return workstreamGetLeaseResult{Lease: lease, Found: found}, nil
	}); err != nil {
		return err
	}

	mutations := []func() error{
		func() error {
			return registerWorkstreamMutation(handler, owner, workstreamOpSaveGoal,
				func(raw json.RawMessage) (domainworkstream.Goal, error) {
					payload, err := decodeWorkstreamPayload[workstreamGoalPayload](raw)
					return payload.Goal, err
				},
				domainworkstream.ValidateGoal, "workstream_goal", "goal_id", func(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.Goal) (domainworkstream.Goal, error) {
					return owner.SaveGoalForStorageHostOperation(ctx, identity, item)
				})
		},
		func() error {
			return registerWorkstreamMutation(handler, owner, persistworkstream.WorkstreamSaveWorkstreamStorageHostOperation, decodeWorkstreamItem[domainworkstream.Workstream], domainworkstream.ValidateWorkstream, "workstream", "workstream_id", owner.SaveWorkstreamRowForStorageHostOperation)
		},
		func() error {
			return registerWorkstreamMutation(handler, owner, persistworkstream.WorkstreamSaveArtifactStorageHostOperation, decodeWorkstreamItem[domainworkstream.Artifact], domainworkstream.ValidateArtifact, "artifact", "artifact_id", owner.SaveArtifactForStorageHostOperation)
		},
		func() error {
			return registerWorkstreamMutation(handler, owner, persistworkstream.WorkstreamSaveArtifactAnnotationStorageHostOperation, decodeWorkstreamItem[domainworkstream.ArtifactAnnotation], domainworkstream.ValidateArtifactAnnotation, "artifact_annotation", "annotation_id", owner.SaveArtifactAnnotationForStorageHostOperation)
		},
		func() error {
			return registerWorkstreamMutation(handler, owner, persistworkstream.WorkstreamSaveSteeringItemStorageHostOperation, decodeWorkstreamItem[domainworkstream.SteeringItem], domainworkstream.ValidateSteeringItem, "steering_queue", "steering_id", owner.SaveSteeringItemForStorageHostOperation)
		},
		func() error {
			return registerWorkstreamMutation(handler, owner, persistworkstream.WorkstreamSaveHeartbeatScheduleStorageHostOperation, decodeWorkstreamHeartbeatSchedule, domainworkstream.ValidateHeartbeatSchedule, "heartbeat_schedule", "schedule_id", owner.SaveHeartbeatScheduleForStorageHostOperation)
		},
		func() error {
			return registerWorkstreamMutation(handler, owner, persistworkstream.WorkstreamSaveVaultUpdateLogStorageHostOperation, decodeWorkstreamItem[domainworkstream.VaultUpdateLog], domainworkstream.ValidateVaultUpdateLog, "vault_update_log", "update_id", owner.SaveVaultUpdateLogForStorageHostOperation)
		},
		func() error {
			return registerWorkstreamMutation(handler, owner, persistworkstream.WorkstreamSaveQueueFreezeStorageHostOperation, decodeWorkstreamQueueFreeze, domainworkstream.ValidateQueueFreeze, "queue_freeze", "freeze_id", owner.SaveQueueFreezeForStorageHostOperation)
		},
		func() error {
			return registerWorkstreamMutation(handler, owner, persistworkstream.WorkstreamSaveStageRunReceiptStorageHostOperation, decodeWorkstreamItem[domainworkstream.StageRunReceipt], domainworkstream.ValidateStageRunReceipt, "stage_run_receipt", "receipt_id", owner.SaveStageRunReceiptForStorageHostOperation)
		},
		func() error {
			return registerWorkstreamMutation(handler, owner, persistworkstream.WorkstreamSaveClosureReceiptStorageHostOperation, decodeWorkstreamItem[domainworkstream.ClosureReceipt], domainworkstream.ValidateClosureReceipt, "closure_receipt", "receipt_id", owner.SaveClosureReceiptForStorageHostOperation)
		},
	}
	for _, register := range mutations {
		if err := register(); err != nil {
			return err
		}
	}
	if err := registerWorkstreamProofMutation(handler, owner, workstreamOpAcquireImplementationLease,
		func(raw json.RawMessage) (domainworkstream.ImplementationLease, error) {
			payload, err := decodeWorkstreamPayload[workstreamLeasePayload](raw)
			return payload.Lease, err
		},
		domainworkstream.ValidateImplementationLease, validateWorkstreamAcquireLeaseResult, func(item domainworkstream.ImplementationLease) string { return item.LeaseName },
		func(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.ImplementationLease) (persistworkstream.WorkstreamLeaseAcquireResult, error) {
			return owner.AcquireImplementationLeaseIfUnfrozenForStorageHostOperation(ctx, identity, item)
		}); err != nil {
		return err
	}
	if err := registerWorkstreamProofMutation(handler, owner, workstreamOpResolveQueueFreeze,
		func(raw json.RawMessage) (workstreamResolveFreezePayload, error) {
			return decodeWorkstreamPayload[workstreamResolveFreezePayload](raw)
		},
		func(item workstreamResolveFreezePayload) error {
			if item.FreezeID == "" {
				return errors.New("freeze_id is required")
			}
			return domainworkstream.ValidateQueueFreezeResolution(item.Resolution, item.Replacement)
		}, validateWorkstreamResolveFreezeResult, func(item workstreamResolveFreezePayload) string { return item.FreezeID },
		func(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item workstreamResolveFreezePayload) (persistworkstream.WorkstreamFreezeLeaseResult, error) {
			return owner.ResolveQueueFreezeAndAcquireLeaseForStorageHostOperation(ctx, identity, item.FreezeID, item.Resolution, item.Replacement)
		}); err != nil {
		return err
	}
	if err := registerWorkstreamProofMutation(handler, owner, workstreamOpReleaseImplementationLease,
		func(raw json.RawMessage) (workstreamReleaseLeasePayload, error) {
			return decodeWorkstreamPayload[workstreamReleaseLeasePayload](raw)
		},
		func(workstreamReleaseLeasePayload) error { return nil }, func(workstreamReleaseLeasePayload, struct{}) error { return nil }, func(item workstreamReleaseLeasePayload) string { return item.LeaseName },
		func(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item workstreamReleaseLeasePayload) (struct{}, error) {
			return struct{}{}, owner.ReleaseImplementationLeaseForStorageHostOperation(ctx, identity, item.LeaseName, item.HolderUnitID)
		}); err != nil {
		return err
	}
	if err := registerWorkstreamProofMutation(handler, owner, workstreamOpHeartbeatImplementationLease,
		func(raw json.RawMessage) (domainworkstream.ImplementationLease, error) {
			payload, err := decodeWorkstreamPayload[workstreamLeasePayload](raw)
			return payload.Lease, err
		},
		func(item domainworkstream.ImplementationLease) error {
			if item.LeaseName == "" || item.HolderUnitID == "" {
				return errors.New("lease_name and holder_unit_id are required")
			}
			return nil
		}, func(domainworkstream.ImplementationLease, struct{}) error { return nil }, func(item domainworkstream.ImplementationLease) string { return item.LeaseName },
		func(ctx context.Context, identity persistworkstream.WorkstreamStorageHostOperationIdentity, item domainworkstream.ImplementationLease) (struct{}, error) {
			return struct{}{}, owner.HeartbeatImplementationLeaseForStorageHostOperation(ctx, identity, item)
		}); err != nil {
		return err
	}
	return nil
}

func registerWorkstreamList[T any](handler *Handler, op string, list func(context.Context, int) ([]T, error)) error {
	return handler.Register(GroupWorkstream, op, false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		payload, err := decodeWorkstreamPayload[workstreamListPayload](raw)
		if err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "workstream list request rejected")
		}
		items, err := list(ctx, payload.Limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "workstream list failed")
		}
		return items, nil
	})
}

func registerWorkstreamMutation[T any](handler *Handler, owner WorkstreamGroupOwner, operation string, decode func(json.RawMessage) (T, error), validate func(T) error, table, idColumn string, apply func(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, T) (T, error)) error {
	return handler.RegisterRecoverable(GroupWorkstream, operation, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		item, err := decode(mutation.Payload)
		if err != nil || validate(item) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "workstream "+operation+" request rejected"))
		}
		identity, err := workstreamOperationIdentity(mutation, operation)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "workstream operation identity rejected"))
		}
		return apply(ctx, identity, item)
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		item, err := decode(mutation.Payload)
		if err != nil || validate(item) != nil {
			return UnknownOutcome(), nil
		}
		identity, err := workstreamOperationIdentity(mutation, operation)
		if err != nil {
			return UnknownOutcome(), nil
		}
		id := workstreamEffectID(item)
		result, found, err := owner.LookupWorkstreamStorageHostOperationResult(ctx, identity, table, idColumn, id)
		if err != nil {
			return UnknownOutcome(), nil
		}
		if !found {
			return ConfirmedNotCommitted(), nil
		}
		var stored T
		if err := decodeWorkstreamPayloadBytes(result, &stored); err != nil || validate(stored) != nil {
			return UnknownOutcome(), nil
		}
		requestedJSON, requestErr := json.Marshal(item)
		storedJSON, storedErr := json.Marshal(stored)
		if requestErr != nil || storedErr != nil || !bytes.Equal(requestedJSON, storedJSON) {
			return UnknownOutcome(), nil
		}
		return Committed(stored), nil
	})
}

func registerWorkstreamProofMutation[P any, R any](handler *Handler, owner WorkstreamGroupOwner, operation string, decode func(json.RawMessage) (P, error), validate func(P) error, validateResult func(P, R) error, effectID func(P) string, apply func(context.Context, persistworkstream.WorkstreamStorageHostOperationIdentity, P) (R, error)) error {
	return handler.RegisterRecoverable(GroupWorkstream, operation, func(ctx context.Context, mutation MutationMetadata) (any, error) {
		item, err := decode(mutation.Payload)
		if err != nil || validate(item) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "workstream "+operation+" request rejected"))
		}
		identity, err := workstreamOperationIdentity(mutation, operation)
		if err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "workstream operation identity rejected"))
		}
		return apply(ctx, identity, item)
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		item, err := decode(mutation.Payload)
		if err != nil || validate(item) != nil {
			return UnknownOutcome(), nil
		}
		identity, err := workstreamOperationIdentity(mutation, operation)
		if err != nil {
			return UnknownOutcome(), nil
		}
		result, found, err := owner.LookupWorkstreamStorageHostOperationProof(ctx, identity, effectID(item))
		if err != nil || !found {
			return UnknownOutcome(), nil
		}
		var stored R
		if err := decodeWorkstreamPayloadBytes(result, &stored); err != nil || validateResult(item, stored) != nil {
			return UnknownOutcome(), nil
		}
		return Committed(stored), nil
	})
}

func validateWorkstreamAcquireLeaseResult(request domainworkstream.ImplementationLease, result persistworkstream.WorkstreamLeaseAcquireResult) error {
	if err := domainworkstream.ValidateImplementationLease(request); err != nil {
		return err
	}
	if result.Acquired {
		if result.Reason != "" {
			return errors.New("workstream acquire result has a reason despite success")
		}
		return nil
	}
	if result.Reason != domainworkstream.ErrQueueFrozen.Error() && result.Reason != domainworkstream.ErrImplementationLeaseHeld.Error() {
		return errors.New("workstream acquire result has an unknown rejection reason")
	}
	return nil
}

func validateWorkstreamResolveFreezeResult(request workstreamResolveFreezePayload, result persistworkstream.WorkstreamFreezeLeaseResult) error {
	if request.FreezeID == "" || result.Freeze.FreezeID != request.FreezeID {
		return errors.New("workstream freeze resolution result is not bound to its request")
	}
	if result.Acquired {
		if result.Freeze.Status != domainworkstream.QueueFreezeResolved || !result.Freeze.ResolutionAcquired || result.Freeze.ReplacementLease != request.Replacement || result.Lease != request.Replacement {
			return errors.New("workstream freeze resolution success differs from its request")
		}
		return nil
	}
	if result.Freeze.Status == domainworkstream.QueueFreezeResolved || result.Freeze.ResolutionAcquired || result.Lease != (domainworkstream.ImplementationLease{}) {
		return errors.New("workstream freeze resolution failure differs from its request")
	}
	return nil
}

func workstreamEffectID[T any](item T) string {
	switch value := any(item).(type) {
	case domainworkstream.Workstream:
		return value.WorkstreamID
	case domainworkstream.Goal:
		return value.GoalID
	case domainworkstream.Artifact:
		return value.ArtifactID
	case domainworkstream.ArtifactAnnotation:
		return value.AnnotationID
	case domainworkstream.SteeringItem:
		return value.SteeringID
	case domainworkstream.HeartbeatSchedule:
		return string(value.ScheduleID)
	case domainworkstream.VaultUpdateLog:
		return value.UpdateID
	case domainworkstream.QueueFreeze:
		return value.FreezeID
	case domainworkstream.StageRunReceipt:
		return string(value.ReceiptID)
	case domainworkstream.ClosureReceipt:
		return string(value.ReceiptID)
	default:
		return ""
	}
}

func decodeWorkstreamItem[T any](raw json.RawMessage) (T, error) {
	payload, err := decodeWorkstreamPayload[workstreamSavePayload[T]](raw)
	return payload.Item, err
}

func decodeWorkstreamHeartbeatSchedule(raw json.RawMessage) (domainworkstream.HeartbeatSchedule, error) {
	return decodeWorkstreamItem[domainworkstream.HeartbeatSchedule](raw)
}

func decodeWorkstreamQueueFreeze(raw json.RawMessage) (domainworkstream.QueueFreeze, error) {
	item, err := decodeWorkstreamItem[domainworkstream.QueueFreeze](raw)
	if err != nil {
		return item, err
	}
	if item.FreezeRevision < 1 {
		item.FreezeRevision = 1
	}
	if item.Status == "" {
		item.Status = domainworkstream.QueueFreezeActive
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	return item, nil
}

func workstreamOperationIdentity(mutation MutationMetadata, operation string) (persistworkstream.WorkstreamStorageHostOperationIdentity, error) {
	if mutation.OpID == "" || len(mutation.Payload) == 0 || mutation.JournalGeneration <= 0 || mutation.RequestGeneration <= 0 {
		return persistworkstream.WorkstreamStorageHostOperationIdentity{}, errors.New("workstream mutation identity is incomplete")
	}
	return persistworkstream.WorkstreamStorageHostOperationIdentity{
		OpID: mutation.OpID, Operation: operation,
		PayloadSHA256:    payloadHash(GroupWorkstream, operation, mutation.Payload),
		WriterGeneration: mutation.JournalGeneration,
	}, nil
}

func decodeWorkstreamPayload[T any](raw json.RawMessage) (T, error) {
	var result T
	if err := decodeWorkstreamPayloadBytes(raw, &result); err != nil {
		return result, err
	}
	return result, nil
}

func decodeWorkstreamPayloadBytes(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("workstream payload contains trailing value")
		}
		return err
	}
	return nil
}

func (client *WorkstreamClient) NewSaveGoalOperation(requestID string, goal domainworkstream.Goal) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, workstreamOpSaveGoal, workstreamGoalPayload{Goal: goal})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveGoal(ctx context.Context, requestID string, goal domainworkstream.Goal) (domainworkstream.Goal, error) {
	var result domainworkstream.Goal
	if err := client.client.Do(ctx, client.NewSaveGoalOperation(requestID, goal), &result); err != nil {
		return domainworkstream.Goal{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewSaveWorkstreamRowOperation(requestID string, item domainworkstream.Workstream) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, persistworkstream.WorkstreamSaveWorkstreamStorageHostOperation, workstreamSavePayload[domainworkstream.Workstream]{Item: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveWorkstreamRow(ctx context.Context, requestID string, item domainworkstream.Workstream) (domainworkstream.Workstream, error) {
	var result domainworkstream.Workstream
	if err := client.client.Do(ctx, client.NewSaveWorkstreamRowOperation(requestID, item), &result); err != nil {
		return domainworkstream.Workstream{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewSaveArtifactOperation(requestID string, item domainworkstream.Artifact) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, persistworkstream.WorkstreamSaveArtifactStorageHostOperation, workstreamSavePayload[domainworkstream.Artifact]{Item: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveArtifact(ctx context.Context, requestID string, item domainworkstream.Artifact) (domainworkstream.Artifact, error) {
	var result domainworkstream.Artifact
	if err := client.client.Do(ctx, client.NewSaveArtifactOperation(requestID, item), &result); err != nil {
		return domainworkstream.Artifact{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewSaveArtifactAnnotationOperation(requestID string, item domainworkstream.ArtifactAnnotation) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, persistworkstream.WorkstreamSaveArtifactAnnotationStorageHostOperation, workstreamSavePayload[domainworkstream.ArtifactAnnotation]{Item: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveArtifactAnnotation(ctx context.Context, requestID string, item domainworkstream.ArtifactAnnotation) (domainworkstream.ArtifactAnnotation, error) {
	var result domainworkstream.ArtifactAnnotation
	if err := client.client.Do(ctx, client.NewSaveArtifactAnnotationOperation(requestID, item), &result); err != nil {
		return domainworkstream.ArtifactAnnotation{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewSaveSteeringItemOperation(requestID string, item domainworkstream.SteeringItem) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, persistworkstream.WorkstreamSaveSteeringItemStorageHostOperation, workstreamSavePayload[domainworkstream.SteeringItem]{Item: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveSteeringItem(ctx context.Context, requestID string, item domainworkstream.SteeringItem) (domainworkstream.SteeringItem, error) {
	var result domainworkstream.SteeringItem
	if err := client.client.Do(ctx, client.NewSaveSteeringItemOperation(requestID, item), &result); err != nil {
		return domainworkstream.SteeringItem{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewSaveHeartbeatScheduleOperation(requestID string, item domainworkstream.HeartbeatSchedule) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, persistworkstream.WorkstreamSaveHeartbeatScheduleStorageHostOperation, workstreamSavePayload[domainworkstream.HeartbeatSchedule]{Item: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveHeartbeatSchedule(ctx context.Context, requestID string, item domainworkstream.HeartbeatSchedule) (domainworkstream.HeartbeatSchedule, error) {
	var result domainworkstream.HeartbeatSchedule
	if err := client.client.Do(ctx, client.NewSaveHeartbeatScheduleOperation(requestID, item), &result); err != nil {
		return domainworkstream.HeartbeatSchedule{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewSaveVaultUpdateLogOperation(requestID string, item domainworkstream.VaultUpdateLog) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, persistworkstream.WorkstreamSaveVaultUpdateLogStorageHostOperation, workstreamSavePayload[domainworkstream.VaultUpdateLog]{Item: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveVaultUpdateLog(ctx context.Context, requestID string, item domainworkstream.VaultUpdateLog) (domainworkstream.VaultUpdateLog, error) {
	var result domainworkstream.VaultUpdateLog
	if err := client.client.Do(ctx, client.NewSaveVaultUpdateLogOperation(requestID, item), &result); err != nil {
		return domainworkstream.VaultUpdateLog{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewSaveQueueFreezeOperation(requestID string, item domainworkstream.QueueFreeze) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, persistworkstream.WorkstreamSaveQueueFreezeStorageHostOperation, workstreamSavePayload[domainworkstream.QueueFreeze]{Item: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveQueueFreeze(ctx context.Context, requestID string, item domainworkstream.QueueFreeze) (domainworkstream.QueueFreeze, error) {
	var result domainworkstream.QueueFreeze
	if err := client.client.Do(ctx, client.NewSaveQueueFreezeOperation(requestID, item), &result); err != nil {
		return domainworkstream.QueueFreeze{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewSaveStageRunReceiptOperation(requestID string, item domainworkstream.StageRunReceipt) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, persistworkstream.WorkstreamSaveStageRunReceiptStorageHostOperation, workstreamSavePayload[domainworkstream.StageRunReceipt]{Item: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveStageRunReceipt(ctx context.Context, requestID string, item domainworkstream.StageRunReceipt) (domainworkstream.StageRunReceipt, error) {
	var result domainworkstream.StageRunReceipt
	if err := client.client.Do(ctx, client.NewSaveStageRunReceiptOperation(requestID, item), &result); err != nil {
		return domainworkstream.StageRunReceipt{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewSaveClosureReceiptOperation(requestID string, item domainworkstream.ClosureReceipt) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, persistworkstream.WorkstreamSaveClosureReceiptStorageHostOperation, workstreamSavePayload[domainworkstream.ClosureReceipt]{Item: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) SaveClosureReceipt(ctx context.Context, requestID string, item domainworkstream.ClosureReceipt) (domainworkstream.ClosureReceipt, error) {
	var result domainworkstream.ClosureReceipt
	if err := client.client.Do(ctx, client.NewSaveClosureReceiptOperation(requestID, item), &result); err != nil {
		return domainworkstream.ClosureReceipt{}, err
	}
	return result, nil
}

func (client *WorkstreamClient) NewAcquireImplementationLeaseIfUnfrozenOperation(requestID string, item domainworkstream.ImplementationLease) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, workstreamOpAcquireImplementationLease, workstreamLeasePayload{Lease: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) AcquireImplementationLeaseIfUnfrozen(ctx context.Context, requestID string, item domainworkstream.ImplementationLease) (bool, string, error) {
	var result persistworkstream.WorkstreamLeaseAcquireResult
	if err := client.client.Do(ctx, client.NewAcquireImplementationLeaseIfUnfrozenOperation(requestID, item), &result); err != nil {
		return false, "", err
	}
	return result.Acquired, result.Reason, nil
}

func (client *WorkstreamClient) NewResolveQueueFreezeAndAcquireLeaseOperation(requestID, freezeID string, resolution domainworkstream.QueueFreezeResolution, replacement domainworkstream.ImplementationLease) *Operation {
	payload := workstreamResolveFreezePayload{FreezeID: freezeID, Resolution: resolution, Replacement: replacement}
	operation := client.client.NewOperation(GroupWorkstream, workstreamOpResolveQueueFreeze, payload)
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) ResolveQueueFreezeAndAcquireLease(ctx context.Context, requestID, freezeID string, resolution domainworkstream.QueueFreezeResolution, replacement domainworkstream.ImplementationLease) (domainworkstream.QueueFreeze, domainworkstream.ImplementationLease, bool, error) {
	var result persistworkstream.WorkstreamFreezeLeaseResult
	if err := client.client.Do(ctx, client.NewResolveQueueFreezeAndAcquireLeaseOperation(requestID, freezeID, resolution, replacement), &result); err != nil {
		return domainworkstream.QueueFreeze{}, domainworkstream.ImplementationLease{}, false, err
	}
	return result.Freeze, result.Lease, result.Acquired, nil
}

func (client *WorkstreamClient) NewReleaseImplementationLeaseOperation(requestID, leaseName, holderUnitID string) *Operation {
	payload := workstreamReleaseLeasePayload{LeaseName: leaseName, HolderUnitID: holderUnitID}
	operation := client.client.NewOperation(GroupWorkstream, workstreamOpReleaseImplementationLease, payload)
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) ReleaseImplementationLease(ctx context.Context, requestID, leaseName, holderUnitID string) error {
	var result struct{}
	return client.client.Do(ctx, client.NewReleaseImplementationLeaseOperation(requestID, leaseName, holderUnitID), &result)
}

func (client *WorkstreamClient) NewHeartbeatImplementationLeaseOperation(requestID string, item domainworkstream.ImplementationLease) *Operation {
	operation := client.client.NewOperation(GroupWorkstream, workstreamOpHeartbeatImplementationLease, workstreamLeasePayload{Lease: item})
	operation.OpID = requestID
	return operation
}

func (client *WorkstreamClient) HeartbeatImplementationLease(ctx context.Context, requestID string, item domainworkstream.ImplementationLease) error {
	var result struct{}
	return client.client.Do(ctx, client.NewHeartbeatImplementationLeaseOperation(requestID, item), &result)
}

func (client *WorkstreamClient) FindGoalByID(ctx context.Context, goalID string) (domainworkstream.Goal, bool, error) {
	var result workstreamFindGoalResult
	if err := client.client.Call(ctx, GroupWorkstream, workstreamOpFindGoal, workstreamFindGoalPayload{GoalID: goalID}, &result); err != nil {
		return domainworkstream.Goal{}, false, err
	}
	return result.Goal, result.Found, nil
}

func (client *WorkstreamClient) GetQueueFreeze(ctx context.Context, freezeID string) (domainworkstream.QueueFreeze, bool, error) {
	var result workstreamFindQueueFreezeResult
	if err := client.client.Call(ctx, GroupWorkstream, workstreamOpGetQueueFreeze, workstreamFindQueueFreezePayload{FreezeID: freezeID}, &result); err != nil {
		return domainworkstream.QueueFreeze{}, false, err
	}
	return result.Freeze, result.Found, nil
}

func (client *WorkstreamClient) FindStageRunReceipt(ctx context.Context, key string) (domainworkstream.StageRunReceipt, bool, error) {
	var result workstreamFindStageRunReceiptResult
	if err := client.client.Call(ctx, GroupWorkstream, workstreamOpFindStageRunReceipt, workstreamFindReceiptPayload{Key: key}, &result); err != nil {
		return domainworkstream.StageRunReceipt{}, false, err
	}
	return result.Receipt, result.Found, nil
}

func (client *WorkstreamClient) ListStageRunReceipts(ctx context.Context, limit int) ([]domainworkstream.StageRunReceipt, error) {
	return workstreamListCall[domainworkstream.StageRunReceipt](ctx, client.client, workstreamOpListStageRunReceipts, limit)
}

func (client *WorkstreamClient) FindClosureReceipt(ctx context.Context, key string) (domainworkstream.ClosureReceipt, bool, error) {
	var result workstreamFindClosureReceiptResult
	if err := client.client.Call(ctx, GroupWorkstream, workstreamOpFindClosureReceipt, workstreamFindReceiptPayload{Key: key}, &result); err != nil {
		return domainworkstream.ClosureReceipt{}, false, err
	}
	return result.Receipt, result.Found, nil
}

func (client *WorkstreamClient) ListClosureReceipts(ctx context.Context, limit int) ([]domainworkstream.ClosureReceipt, error) {
	return workstreamListCall[domainworkstream.ClosureReceipt](ctx, client.client, workstreamOpListClosureReceipts, limit)
}

func (client *WorkstreamClient) GetImplementationLease(ctx context.Context, leaseName string) (domainworkstream.ImplementationLease, bool, error) {
	var result workstreamGetLeaseResult
	if err := client.client.Call(ctx, GroupWorkstream, workstreamOpGetImplementationLease, workstreamGetLeasePayload{LeaseName: leaseName}, &result); err != nil {
		return domainworkstream.ImplementationLease{}, false, err
	}
	return result.Lease, result.Found, nil
}

func (client *WorkstreamClient) ListWorkstreams(ctx context.Context, limit int) ([]domainworkstream.Workstream, error) {
	return workstreamListCall[domainworkstream.Workstream](ctx, client.client, workstreamOpListWorkstreams, limit)
}
func (client *WorkstreamClient) ListGoals(ctx context.Context, limit int) ([]domainworkstream.Goal, error) {
	return workstreamListCall[domainworkstream.Goal](ctx, client.client, workstreamOpListGoals, limit)
}
func (client *WorkstreamClient) ListArtifacts(ctx context.Context, limit int) ([]domainworkstream.Artifact, error) {
	return workstreamListCall[domainworkstream.Artifact](ctx, client.client, workstreamOpListArtifacts, limit)
}
func (client *WorkstreamClient) ListArtifactAnnotations(ctx context.Context, limit int) ([]domainworkstream.ArtifactAnnotation, error) {
	return workstreamListCall[domainworkstream.ArtifactAnnotation](ctx, client.client, workstreamOpListArtifactNotes, limit)
}
func (client *WorkstreamClient) ListSteeringItems(ctx context.Context, limit int) ([]domainworkstream.SteeringItem, error) {
	return workstreamListCall[domainworkstream.SteeringItem](ctx, client.client, workstreamOpListSteeringItems, limit)
}
func (client *WorkstreamClient) ListHeartbeatSchedules(ctx context.Context, limit int) ([]domainworkstream.HeartbeatSchedule, error) {
	return workstreamListCall[domainworkstream.HeartbeatSchedule](ctx, client.client, workstreamOpListHeartbeatSchedule, limit)
}
func (client *WorkstreamClient) ListVaultUpdateLogs(ctx context.Context, limit int) ([]domainworkstream.VaultUpdateLog, error) {
	return workstreamListCall[domainworkstream.VaultUpdateLog](ctx, client.client, workstreamOpListVaultUpdateLogs, limit)
}

func (client *WorkstreamClient) ListQueueFreezes(ctx context.Context, limit int) ([]domainworkstream.QueueFreeze, error) {
	return workstreamListCall[domainworkstream.QueueFreeze](ctx, client.client, workstreamOpListQueueFreezes, limit)
}

func workstreamListCall[T any](ctx context.Context, client *Client, op string, limit int) ([]T, error) {
	var items []T
	if err := client.Call(ctx, GroupWorkstream, op, workstreamListPayload{Limit: limit}, &items); err != nil {
		return nil, err
	}
	return items, nil
}
