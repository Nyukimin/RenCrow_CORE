package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type nativePlanningOperationStore interface {
	ExecuteIdempotentNativePlanningOperation(context.Context, string, string, domaintask.NativePlanningParentAuthorizationCheck, func(Store) (json.RawMessage, error)) (json.RawMessage, error)
	LookupNativePlanningOperationReceipt(context.Context, string, string, domaintask.NativePlanningParentAuthorizationCheck) (json.RawMessage, bool, error)
}

type nativePlanningInvocationLink struct {
	TaskID modulecore.TaskID `json:"task_id"`
	RunID  modulecore.RunID  `json:"run_id"`
}

// ValidateNativePlanningParent proves that the current owner-selected Run is
// still active for the startup-bound actor and returns its persisted readonly
// restriction. It does not issue identities or authorize generic Worker tools.
func (m *Manager) ValidateNativePlanningParent(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string) (bool, error) {
	if m == nil || m.store == nil {
		return false, fmt.Errorf("task manager store is unavailable")
	}
	if err := taskID.Validate(); err != nil {
		return false, err
	}
	if err := runID.Validate(); err != nil {
		return false, err
	}
	if actorID == "" {
		return false, fmt.Errorf("native planning actor is required")
	}
	var readOnly bool
	err := m.readTransaction(ctx, func(txManager *Manager) error {
		if err := txManager.validateRunExecutionInTransaction(ctx, taskID, runID, actorID); err != nil {
			return err
		}
		task, err := txManager.store.GetTask(ctx, taskID)
		if err != nil {
			return err
		}
		if domaintask.IsOwnerManagedTask(task) || task.PrivateAccess != nil || task.Assignee != actorID {
			return domaintask.ErrTypedTaskRequiresOwner
		}
		readOnly = task.ReadOnly
		return nil
	})
	return readOnly, err
}

// validateNativePlanningParentReceipt permits exact receipt/result recovery
// after the parent Run has closed. It is read-only authorization: only the
// same persisted, non-private parent Task/Run and bound actor can recover it.
func (m *Manager) validateNativePlanningParentReceipt(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID, requestID string) error {
	if m == nil || m.store == nil {
		return fmt.Errorf("task manager store is unavailable")
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil || identity.TaskID != taskID || identity.RunID != runID {
		return fmt.Errorf("native planning receipt requires the exact parent identity")
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != actorID || scope.RequestID != requestID {
		return fmt.Errorf("native planning receipt requires the bound parent actor and request")
	}
	return m.readTransaction(ctx, func(txManager *Manager) error {
		task, taskErr := txManager.store.GetTask(ctx, taskID)
		if taskErr != nil {
			return taskErr
		}
		run, runErr := txManager.store.GetRun(ctx, runID)
		if runErr != nil {
			return runErr
		}
		if domaintask.IsOwnerManagedTask(task) || task.PrivateAccess != nil || task.Assignee != actorID || run.TaskID != taskID || run.Assignee != actorID {
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
		return nil
	})
}

// EnsureNativePlanningInvocation issues one bounded child Task/Run per trusted
// native invocation. Its receipt key excludes live context; the first call
// freezes that context, while retries recover its exact Task/Run before any
// context is rebuilt.
func (m *Manager) EnsureNativePlanningInvocation(
	ctx context.Context,
	parentTaskID modulecore.TaskID,
	parentRunID modulecore.RunID,
	parentActor string,
	targetActor string,
	requestKey modulecore.RequestID,
	envelope json.RawMessage,
) (domaintask.Task, domaintask.Run, domaintask.SharedRoleContext, bool, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, fmt.Errorf("task manager store is unavailable")
	}
	if ctx == nil || len(envelope) == 0 || len(envelope) > 8<<10 || !json.Valid(envelope) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, fmt.Errorf("native planning envelope is invalid")
	}
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil || identity.TaskID != parentTaskID || identity.RunID != parentRunID {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, fmt.Errorf("native planning parent identity is not owner-bound")
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != parentActor || scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator || scope.RequestID != string(requestKey) {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, fmt.Errorf("native planning parent scope is not trusted")
	}
	if err := parentTaskID.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, err
	}
	if err := parentRunID.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, err
	}
	if err := requestKey.Validate(); err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, err
	}
	if strings.TrimSpace(parentActor) == "" || strings.ToLower(parentActor) != parentActor || strings.TrimSpace(targetActor) == "" || strings.ToLower(targetActor) != targetActor {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, fmt.Errorf("native planning actors must be canonical")
	}
	dataScope, authenticatedUserID := "public", ""
	if scope.Allows(domaintool.DataScopeUser) {
		if strings.TrimSpace(scope.AuthenticatedUserID) == "" {
			return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, fmt.Errorf("native planning user scope is not authenticated")
		}
		dataScope, authenticatedUserID = "user", scope.AuthenticatedUserID
	}
	if targetActor != parentActor && (parentActor != "shiro" || targetActor != "aka" && targetActor != "ao" && targetActor != "kin" && targetActor != "gin") {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, fmt.Errorf("only Shiro may issue a configured Coder planning child")
	}
	inputDigest := sha256.Sum256(envelope)
	inputDigestHex := hex.EncodeToString(inputDigest[:])
	operationMaterial := strings.Join([]string{string(parentTaskID), string(parentRunID), targetActor, "task_center_planning", string(requestKey)}, "\x00")
	operationDigest := sha256.Sum256([]byte(operationMaterial))
	operationID := "native-planning:" + hex.EncodeToString(operationDigest[:])
	requestMaterial := operationMaterial + "\x00" + inputDigestHex
	requestDigest := sha256.Sum256([]byte(requestMaterial))
	requestHash := hex.EncodeToString(requestDigest[:])
	authorizeParent := func(checkCtx context.Context) error {
		_, authErr := m.ValidateNativePlanningParent(checkCtx, parentTaskID, parentRunID, parentActor)
		return authErr
	}
	authorizeReceipt := func(checkCtx context.Context) error {
		return m.validateNativePlanningParentReceipt(checkCtx, parentTaskID, parentRunID, parentActor, string(requestKey))
	}
	store, ok := m.store.(nativePlanningOperationStore)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, ErrGlobalTaskOperationUnsupported
	}
	var result json.RawMessage
	var operationErr error
	var createdHere bool
	if receipt, found, lookupErr := store.LookupNativePlanningOperationReceipt(ctx, operationID, requestHash, authorizeReceipt); lookupErr != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, lookupErr
	} else if found {
		result = receipt
		createdHere = false
		operationErr = nil
	} else {
		result, operationErr = store.ExecuteIdempotentNativePlanningOperation(ctx, operationID, requestHash, authorizeParent, func(tx Store) (json.RawMessage, error) {
			if tx == nil {
				return nil, fmt.Errorf("native planning transaction is unavailable")
			}
			createdHere = true
			txManager := &Manager{store: tx, limits: m.limits, expectedCriteriaRevision: m.expectedCriteriaRevision, idIndexed: m.idIndexed, now: m.now}
			if err := txManager.validateRunExecutionInTransaction(ctx, parentTaskID, parentRunID, parentActor); err != nil {
				return nil, err
			}
			parent, err := tx.GetTask(ctx, parentTaskID)
			if err != nil {
				return nil, err
			}
			if parent.PrivateAccess != nil || parent.Assignee != parentActor {
				return nil, domaintask.ErrNativePlanningTaskRequiresOwner
			}
			parentRun, err := tx.GetRun(ctx, parentRunID)
			if err != nil {
				return nil, err
			}
			if parentRun.Status != domaintask.RunStatusRunning || parentRun.Assignee != parentActor {
				return nil, domaintask.ErrNativePlanningTaskRequiresOwner
			}
			parentShared, err := tx.GetContext(ctx, parentTaskID)
			if err != nil {
				return nil, err
			}
			now := txManager.now()
			child := domaintask.Task{
				TaskID: modulecore.NewTaskID(), Title: "TaskCenter planning for " + targetActor,
				ParentTaskID: parentTaskID, Route: domaintask.RoutePLAN, Assignee: targetActor,
				OwnerID: parent.OwnerID, ModuleID: parent.ModuleID, ModuleRoot: parent.ModuleRoot,
				OriginSessionID: parent.OriginSessionID, OriginThreadID: parent.OriginThreadID,
				OriginTurnID: parent.OriginTurnID, OriginMessageID: parent.OriginMessageID,
				OriginPublication: parent.OriginPublication,
				WorkstreamID:      parent.WorkstreamID, GoalID: parent.GoalID,
				Status: domaintask.StatusRunning, Priority: domaintask.PriorityNormal,
				ReadOnly: parent.ReadOnly || targetActor != parentActor, StartedAt: &now,
				NativePlanningInvocation: &domaintask.NativePlanningInvocation{
					ParentTaskID: parentTaskID, ParentRunID: parentRunID, ActorID: targetActor,
					ParentActorID: parentActor,
					Purpose:       "task_center_planning", RequestKey: requestKey,
					EnvelopeSHA256: inputDigestHex, ParentReadOnly: parent.ReadOnly,
					DataScope: dataScope, AuthenticatedUserID: authenticatedUserID,
				},
			}
			child.ApplyDefaults(now)
			child.Status, child.StartedAt = domaintask.StatusRunning, &now
			childCandidate := child
			childCandidate.NativePlanningInvocation = nil
			childCandidate.Status, childCandidate.StartedAt = domaintask.StatusQueued, nil
			allowed, reason, err := txManager.canStartInTransaction(ctx, childCandidate)
			if err != nil {
				return nil, err
			}
			if !allowed {
				return nil, fmt.Errorf("%w: %s", ErrParallelLimit, reason)
			}
			if err := child.ValidateStartTiming(now); err != nil {
				return nil, err
			}
			if err := child.Validate(); err != nil {
				return nil, err
			}
			generation, err := tx.WriterGeneration()
			if err != nil {
				return nil, fmt.Errorf("native planning Task writer generation is unavailable: %w", err)
			}
			if generation == 0 {
				return nil, fmt.Errorf("native planning Task writer generation is required")
			}
			run := domaintask.Run{WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: child.TaskID, TraceID: parentRun.TraceID,
				StartReason: domaintask.RunStartReasonFirst, Assignee: targetActor, Status: domaintask.RunStatusRunning, StartedAt: now}
			shared := parentShared
			shared.TaskID, shared.UpdatedAt = child.TaskID, now
			writer, ok := tx.(domaintask.NativePlanningInvocationWriter)
			if !ok {
				return nil, domaintask.ErrNativePlanningTaskRequiresOwner
			}
			stateAuth := func(_ context.Context, stored domaintask.Task, storedRun domaintask.Run, storedShared domaintask.SharedRoleContext) error {
				if stored.NativePlanningInvocation == nil || stored.NativePlanningInvocation.ParentTaskID != parentTaskID || stored.NativePlanningInvocation.ParentRunID != parentRunID || stored.NativePlanningInvocation.ParentActorID != parentActor ||
					stored.NativePlanningInvocation.ActorID != targetActor || stored.NativePlanningInvocation.EnvelopeSHA256 != inputDigestHex ||
					storedRun.TaskID != stored.TaskID || storedRun.Assignee != targetActor || storedShared.TaskID != stored.TaskID {
					return domaintask.ErrNativePlanningTaskRequiresOwner
				}
				return nil
			}
			if err := writer.SaveNativePlanningInvocationState(ctx, child, run, &shared, stateAuth); err != nil {
				return nil, err
			}
			return json.Marshal(nativePlanningInvocationLink{TaskID: child.TaskID, RunID: run.RunID})
		})
		if operationErr != nil {
			if recovered, found, recoverErr := store.LookupNativePlanningOperationReceipt(ctx, operationID, requestHash, authorizeReceipt); recoverErr == nil && found {
				result, operationErr = recovered, nil
				createdHere = false
			}
		}
	}
	if operationErr != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, operationErr
	}
	var link nativePlanningInvocationLink
	if err := json.Unmarshal(result, &link); err != nil || link.TaskID == "" || link.RunID == "" {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, domaintask.ErrNativePlanningTaskRequiresOwner
	}
	reader, ok := m.store.(domaintask.NativePlanningInvocationStateReader)
	if !ok {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, domaintask.ErrNativePlanningTaskRequiresOwner
	}
	stateAuth := func(_ context.Context, task domaintask.Task, run domaintask.Run, shared domaintask.SharedRoleContext) error {
		inv := task.NativePlanningInvocation
		if inv == nil || inv.ParentTaskID != parentTaskID || inv.ParentRunID != parentRunID || inv.ParentActorID != parentActor || inv.ActorID != targetActor || inv.RequestKey != requestKey || inv.EnvelopeSHA256 != inputDigestHex || inv.DataScope != dataScope || inv.AuthenticatedUserID != authenticatedUserID ||
			task.ReadOnly != (inv.ParentReadOnly || targetActor != parentActor) || run.TaskID != task.TaskID || run.RunID != link.RunID || run.Assignee != targetActor || shared.TaskID != task.TaskID {
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
		return nil
	}
	task, run, shared, err := reader.GetNativePlanningInvocationState(ctx, link.TaskID, link.RunID, authorizeReceipt, stateAuth)
	if err != nil {
		return domaintask.Task{}, domaintask.Run{}, domaintask.SharedRoleContext{}, false, err
	}
	return task, run, shared, operationErr == nil && createdHere, nil
}

// BindNativePlanningInvocationContext replaces a verified parent identity with
// the exact child identity returned by EnsureNativePlanningInvocation and
// derives only inherited public/user data scope for the configured child actor.
func BindNativePlanningInvocationContext(ctx context.Context, task domaintask.Task, run domaintask.Run) (context.Context, error) {
	if ctx == nil || task.NativePlanningInvocation == nil || task.NativePlanningInvocation.Validate(task) != nil || run.TaskID != task.TaskID || run.Assignee != task.Assignee {
		return nil, domaintask.ErrNativePlanningTaskRequiresOwner
	}
	inv := task.NativePlanningInvocation
	parentIdentity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil || parentIdentity.TaskID != inv.ParentTaskID || parentIdentity.RunID != inv.ParentRunID {
		return nil, domaintask.ErrNativePlanningTaskRequiresOwner
	}
	parentScope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || parentScope.Validate() != nil || parentScope.ActorKind != domaintool.ActorKindAgent || parentScope.ActorID != inv.ParentActorID || parentScope.RequestID != string(inv.RequestKey) {
		return nil, domaintask.ErrNativePlanningTaskRequiresOwner
	}
	childIdentity := domainexecution.Identity{TaskID: task.TaskID, RunID: run.RunID, TraceID: run.TraceID}
	childCtx, err := domainexecution.WithIssuedChildIdentity(ctx, parentIdentity, childIdentity)
	if err != nil {
		return nil, err
	}
	if task.PrivateAccess != nil {
		if parentScope.AuthenticatedUserID == "" || parentScope.AuthenticatedUserID != task.PrivateAccess.AuthenticatedUserID || !parentScope.Allows(domaintool.DataScopeUser) {
			return nil, domaintask.ErrNativePlanningTaskRequiresOwner
		}
		childCtx, err = domaintool.DeriveAgentToolExecutionScope(childCtx, string(inv.RequestKey), inv.ActorID, "native_planning", inv.Purpose, false)
	} else {
		allowedScopes := []string{domaintool.DataScopePublic}
		userID := ""
		if inv.DataScope == "user" {
			if parentScope.AuthenticatedUserID != inv.AuthenticatedUserID || !parentScope.Allows(domaintool.DataScopeUser) {
				return nil, domaintask.ErrNativePlanningTaskRequiresOwner
			}
			allowedScopes = append(allowedScopes, domaintool.DataScopeUser)
			userID = inv.AuthenticatedUserID
		} else if parentScope.Allows(domaintool.DataScopeUser) {
			return nil, domaintask.ErrNativePlanningTaskRequiresOwner
		}
		var childScope domaintool.ToolExecutionScope
		childScope, err = domaintool.NewToolExecutionScope(string(inv.RequestKey), domaintool.ActorKindAgent, inv.ActorID, userID, allowedScopes, domaintool.AuthenticationSourceAgentOrchestrator)
		if err == nil {
			childCtx = domaintool.WithToolExecutionScope(childCtx, childScope)
		}
	}
	if err != nil {
		return nil, err
	}
	return childCtx, nil
}

// CompleteNativePlanningInvocation stores the complete JSON result on the
// canonical child Task and Run. It is idempotent only for the exact result.
func (m *Manager) CompleteNativePlanningInvocation(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, parentTaskID modulecore.TaskID, parentRunID modulecore.RunID, parentActor, actorID, summary string) error {
	return m.finishNativePlanningInvocation(ctx, taskID, parentTaskID, runID, parentRunID, parentActor, actorID, summary, true)
}

func (m *Manager) FailNativePlanningInvocation(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, parentTaskID modulecore.TaskID, parentRunID modulecore.RunID, parentActor, actorID, summary string) error {
	return m.finishNativePlanningInvocation(ctx, taskID, parentTaskID, runID, parentRunID, parentActor, actorID, summary, false)
}

func (m *Manager) finishNativePlanningInvocation(ctx context.Context, taskID, parentTaskID modulecore.TaskID, runID, parentRunID modulecore.RunID, parentActor, actorID, summary string, succeeded bool) error {
	if len(summary) == 0 || len(summary) > 48<<10 {
		return fmt.Errorf("native planning result must be between 1 and 49152 bytes")
	}
	parentScope, scopeOK := domaintool.ToolExecutionScopeFromContext(ctx)
	if !scopeOK || parentScope.Validate() != nil || parentScope.ActorID != parentActor || parentScope.RequestID == "" {
		return domaintask.ErrNativePlanningTaskRequiresOwner
	}
	return m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		reader, ok := txManager.store.(domaintask.NativePlanningInvocationStateReader)
		if !ok {
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
		var child domaintask.Task
		var childRun domaintask.Run
		var shared domaintask.SharedRoleContext
		parentAuth := func(checkCtx context.Context) error {
			return txManager.validateNativePlanningParentReceipt(checkCtx, parentTaskID, parentRunID, parentActor, parentScope.RequestID)
		}
		stateAuth := func(_ context.Context, task domaintask.Task, run domaintask.Run, storedShared domaintask.SharedRoleContext) error {
			inv := task.NativePlanningInvocation
			scopeData, scopeUser := "public", ""
			if parentScope.Allows(domaintool.DataScopeUser) {
				scopeData, scopeUser = "user", parentScope.AuthenticatedUserID
			}
			if inv == nil || inv.ActorID != actorID || inv.ParentTaskID != parentTaskID || inv.ParentRunID != parentRunID || inv.ParentActorID != parentActor || string(inv.RequestKey) != parentScope.RequestID || inv.DataScope != scopeData || inv.AuthenticatedUserID != scopeUser || run.TaskID != task.TaskID || run.RunID != runID || run.Assignee != actorID || storedShared.TaskID != task.TaskID {
				return domaintask.ErrNativePlanningTaskRequiresOwner
			}
			return nil
		}
		child, childRun, shared, err := reader.GetNativePlanningInvocationState(ctx, taskID, runID, parentAuth, stateAuth)
		if err != nil {
			return err
		}
		wantTaskStatus, wantRunStatus := domaintask.StatusFailed, domaintask.RunStatusFailed
		if succeeded {
			wantTaskStatus, wantRunStatus = domaintask.StatusSucceeded, domaintask.RunStatusSucceeded
		}
		if child.Status == wantTaskStatus && childRun.Status == wantRunStatus {
			if child.Summary == summary && childRun.Summary == summary {
				return nil
			}
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
		if child.Status != domaintask.StatusRunning || childRun.Status != domaintask.RunStatusRunning {
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
		now := txManager.now()
		closedRun, err := childRun.Close(wantRunStatus, now, summary)
		if err != nil {
			return err
		}
		child.Status, child.Summary, child.UpdatedAt = wantTaskStatus, summary, now
		child.FinishedAt = &now
		if err := child.Validate(); err != nil {
			return err
		}
		writer, ok := txManager.store.(domaintask.NativePlanningInvocationWriter)
		if !ok {
			return domaintask.ErrNativePlanningTaskRequiresOwner
		}
		return writer.SaveNativePlanningInvocationState(ctx, child, closedRun, &shared, stateAuth)
	})
}
