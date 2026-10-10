package actionmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrNativeDelegationConflict = errors.New("native delegation state conflict")

type EnsureNativeDelegationInput struct {
	TaskID                   modulecore.TaskID
	RunID                    modulecore.RunID
	Mode                     domainaction.NativeDelegationMode
	ExpectedCriteriaRevision string
	InputReference           *conversation.AcceptedOPSInputReference
	ResumeSource             *domainaction.NativeDelegationResumeSource
}

type PrepareNativeMutationInput struct {
	ActionID  modulecore.ActionID
	AttemptID modulecore.AttemptID
	Slot      domainaction.NativeMutationSlot
	Key       string
	Payload   []byte
}

type RecordNativeMutationOutcomeInput struct {
	ActionID  modulecore.ActionID
	AttemptID modulecore.AttemptID
	Slot      domainaction.NativeMutationSlot
	Status    domainaction.NativeMutationStatus
	Result    []byte
	ErrorCode string
}

type RecordNativeObservationInput struct {
	ActionID    modulecore.ActionID
	AttemptID   modulecore.AttemptID
	Observation domainaction.NativeObservation
}

type CompleteNativeDelegationInput struct {
	ActionID      modulecore.ActionID
	AttemptID     modulecore.AttemptID
	RunResult     []byte
	Proof         *domainaction.NativeDelegationProof
	AttemptStatus domainaction.AttemptStatus
	ActionStatus  domainaction.Status
	Summary       string
}

// EnsureNativeDelegation returns the one canonical delegation Action and its
// first Attempt for a Task Run. The store transaction is the uniqueness
// boundary across managers and processes.
func (m *Manager) EnsureNativeDelegation(ctx context.Context, input EnsureNativeDelegationInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := input.TaskID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := input.RunID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("run_id is invalid: %w", err)
	}
	mode := input.Mode
	if mode == "" {
		mode = domainaction.NativeDelegationModeOpenStart
	}
	if mode != domainaction.NativeDelegationModeOpenStart && mode != domainaction.NativeDelegationModeResume {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("native delegation mode is invalid")
	}
	if input.ExpectedCriteriaRevision != "" && !domaintask.ValidCriteriaRevision(input.ExpectedCriteriaRevision) {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("expected criteria revision is invalid")
	}
	var resumeSource *domainaction.NativeDelegationResumeSource
	if input.ResumeSource != nil {
		if err := input.ResumeSource.Validate(); err != nil {
			return domainaction.Action{}, domainaction.Attempt{}, conflict("resume source is invalid")
		}
		copy := *input.ResumeSource
		if copy.CheckpointID != nil {
			checkpoint := *copy.CheckpointID
			copy.CheckpointID = &checkpoint
		}
		resumeSource = &copy
	}
	if mode == domainaction.NativeDelegationModeResume && (resumeSource == nil || input.InputReference != nil) ||
		mode == domainaction.NativeDelegationModeOpenStart && resumeSource != nil {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("native delegation mode payload is inconsistent")
	}
	var reference *conversation.AcceptedOPSInputReference
	if input.InputReference != nil {
		if err := input.InputReference.Validate(); err != nil {
			return domainaction.Action{}, domainaction.Attempt{}, conflict("input reference is invalid")
		}
		copy := *input.InputReference
		reference = &copy
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	var resultAction domainaction.Action
	var resultAttempt domainaction.Attempt
	err := m.store.Transaction(ctx, func(tx Store) error {
		if err := validateContext(ctx); err != nil {
			return err
		}
		actions, err := tx.ListActions(ctx, domainaction.Filter{TaskID: input.TaskID, RunID: input.RunID})
		if err != nil {
			return err
		}
		var existing *domainaction.Action
		for index := range actions {
			if !isNativeDelegationAction(actions[index]) {
				continue
			}
			if existing != nil {
				return conflict("multiple native delegation actions already exist for Task Run")
			}
			item := actions[index]
			existing = &item
		}
		if existing != nil {
			if existing.CurrentAttemptID == "" {
				return conflict("native delegation has no current first attempt")
			}
			attempt, err := tx.GetAttempt(ctx, existing.CurrentAttemptID)
			if err != nil {
				return conflict("native delegation current attempt is unavailable")
			}
			wantReason := domainaction.AttemptStartReasonFirst
			if mode == domainaction.NativeDelegationModeResume {
				wantReason = domainaction.AttemptStartReasonExplicitResume
			}
			if attempt.ActionID != existing.ActionID || attempt.StartReason != wantReason || attempt.NativeDelegation == nil {
				return conflict("existing native action is not owned by the requested typed delegation mode")
			}
			attempts, err := tx.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: existing.ActionID})
			if err != nil {
				return err
			}
			if len(attempts) != 1 || attempts[0].AttemptID != attempt.AttemptID {
				return conflict("native delegation must keep exactly its initial attempt")
			}
			if attempt.NativeDelegation.EffectiveMode() != mode || !nativeReferencesEqual(attempt.NativeDelegation.InputReference, reference) ||
				!nativeResumeSourcesEqual(attempt.NativeDelegation.ResumeSource, resumeSource) {
				return conflict("input reference differs from the persisted delegation")
			}
			if input.ExpectedCriteriaRevision != "" && attempt.NativeDelegation.ExpectedCriteriaRevision != input.ExpectedCriteriaRevision {
				return conflict("criteria revision differs from the persisted delegation")
			}
			resultAction = cloneNativeAction(*existing)
			resultAttempt = attempt.Clone()
			return nil
		}

		if !domaintask.ValidCriteriaRevision(input.ExpectedCriteriaRevision) {
			return conflict("new native delegation requires a valid expected criteria revision")
		}
		now := m.now().UTC()
		actionID := modulecore.NewActionID()
		attemptID := modulecore.NewAttemptID()
		action := domainaction.Action{
			ActionID:         actionID,
			TaskID:           input.TaskID,
			RunID:            input.RunID,
			Kind:             domainaction.KindDelegation,
			Name:             domainaction.NativeDelegationActionName,
			Status:           domainaction.StatusOpen,
			CurrentAttemptID: attemptID,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		startReason := domainaction.AttemptStartReasonFirst
		if mode == domainaction.NativeDelegationModeResume {
			startReason = domainaction.AttemptStartReasonExplicitResume
		}
		attempt := domainaction.Attempt{
			AttemptID:   attemptID,
			ActionID:    actionID,
			StartReason: startReason,
			Status:      domainaction.AttemptStatusRunning,
			StartedAt:   now,
			NativeDelegation: &domainaction.NativeDelegation{
				Mode: mode, InputReference: reference, ExpectedCriteriaRevision: input.ExpectedCriteriaRevision, ResumeSource: resumeSource,
			},
		}
		if err := action.Validate(); err != nil {
			return err
		}
		if err := attempt.Validate(); err != nil {
			return err
		}
		if err := tx.SaveAction(ctx, action); err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, attempt); err != nil {
			return err
		}
		resultAction = cloneNativeAction(action)
		resultAttempt = attempt.Clone()
		return nil
	})
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return resultAction, resultAttempt, nil
}

func nativeResumeSourcesEqual(left, right *domainaction.NativeDelegationResumeSource) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ActionID == right.ActionID && left.AttemptID == right.AttemptID && left.HarnessTaskID == right.HarnessTaskID &&
		left.ThreadID == right.ThreadID && left.RunID == right.RunID && left.ReceiptID == right.ReceiptID &&
		left.PreviousRunID == right.PreviousRunID && left.RunResultSHA256 == right.RunResultSHA256 &&
		(left.CheckpointID == nil && right.CheckpointID == nil || left.CheckpointID != nil && right.CheckpointID != nil && *left.CheckpointID == *right.CheckpointID)
}

// PrepareNativeMutation durably fixes a mutation's exact params bytes and
// idempotency key before a caller can attempt delivery.
func (m *Manager) PrepareNativeMutation(ctx context.Context, input PrepareNativeMutationInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	payload := append([]byte(nil), input.Payload...)
	if strings.TrimSpace(input.Key) == "" || strings.TrimSpace(input.Key) != input.Key || len(payload) == 0 {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("mutation key and payload are required")
	}
	hash := sha256.Sum256(payload)
	prepared := domainaction.NativeMutation{
		Key:           input.Key,
		Payload:       payload,
		PayloadSHA256: hex.EncodeToString(hash[:]),
		Status:        domainaction.NativeMutationStatusPrepared,
	}
	if err := prepared.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("mutation is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateNativeAttempt(ctx, input.ActionID, input.AttemptID, func(attempt *domainaction.Attempt, now time.Time) (bool, error) {
		delegation := attempt.NativeDelegation
		slot := nativeMutationSlot(delegation, input.Slot)
		if slot == nil {
			return false, conflict("unknown mutation slot")
		}
		mode := delegation.EffectiveMode()
		if mode == domainaction.NativeDelegationModeOpenStart && input.Slot == domainaction.NativeMutationSlotResume ||
			mode == domainaction.NativeDelegationModeResume && input.Slot != domainaction.NativeMutationSlotResume {
			return false, conflict("mutation slot does not belong to the frozen delegation mode")
		}
		if *slot != nil {
			current := *slot
			if current.Key == prepared.Key && current.PayloadSHA256 == prepared.PayloadSHA256 && bytes.Equal(current.Payload, prepared.Payload) {
				return false, nil
			}
			return false, conflict("mutation slot is already fixed to different bytes or key")
		}
		if input.Slot == domainaction.NativeMutationSlotStart && (delegation.Open == nil || delegation.Open.Status != domainaction.NativeMutationStatusAccepted || len(delegation.Open.Result) == 0) {
			return false, conflict("start mutation requires an accepted open result")
		}
		if input.Slot == domainaction.NativeMutationSlotResume && (mode != domainaction.NativeDelegationModeResume || delegation.ResumeSource == nil) {
			return false, conflict("resume mutation requires a frozen Resume source")
		}
		copy := prepared
		copy.Payload = append([]byte(nil), prepared.Payload...)
		*slot = &copy
		return true, nil
	})
}

func (m *Manager) MarkNativeMutationDeliveryUnknown(ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID, slotName domainaction.NativeMutationSlot) (domainaction.Action, domainaction.Attempt, error) {
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateNativeAttempt(ctx, actionID, attemptID, func(attempt *domainaction.Attempt, _ time.Time) (bool, error) {
		slot := nativeMutationSlot(attempt.NativeDelegation, slotName)
		if slot == nil || *slot == nil {
			return false, conflict("mutation slot is not prepared")
		}
		switch (*slot).Status {
		case domainaction.NativeMutationStatusPrepared:
			(*slot).Status = domainaction.NativeMutationStatusDeliveryUnknown
			return true, nil
		case domainaction.NativeMutationStatusDeliveryUnknown:
			return false, nil
		default:
			return false, conflict("mutation already has a definite outcome")
		}
	})
}

// RecordNativeMutationOutcome stores a definite owner response. Callers must
// never translate transport errors, cancellation, or interruption into
// NativeMutationStatusRejected; those cases remain delivery_unknown. A definite
// rejection stores the rejected slot and fails the Action/Attempt atomically,
// without inventing a Harness RunResult.
func (m *Manager) RecordNativeMutationOutcome(ctx context.Context, input RecordNativeMutationOutcomeInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	result := append([]byte(nil), input.Result...)
	if input.Status != domainaction.NativeMutationStatusAccepted && input.Status != domainaction.NativeMutationStatusRejected {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("mutation outcome must be accepted or rejected")
	}
	if input.Status == domainaction.NativeMutationStatusAccepted && len(result) == 0 || input.Status == domainaction.NativeMutationStatusRejected && (input.ErrorCode == "" || len(result) != 0) {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("mutation outcome is incomplete")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var resultAction domainaction.Action
	var resultAttempt domainaction.Attempt
	err := m.store.Transaction(ctx, func(tx Store) error {
		action, err := tx.GetAction(ctx, input.ActionID)
		if err != nil {
			return err
		}
		attempt, err := tx.GetAttempt(ctx, input.AttemptID)
		if err != nil {
			return err
		}
		if !isNativeDelegationAction(action) || action.CurrentAttemptID != input.AttemptID || attempt.ActionID != input.ActionID || attempt.NativeDelegation == nil {
			return conflict("outcome pair is not the current typed native delegation")
		}
		slot := nativeMutationSlot(attempt.NativeDelegation, input.Slot)
		if slot == nil || *slot == nil {
			return conflict("mutation slot is not prepared")
		}
		current := *slot
		if !action.IsOpen() || !attempt.IsActive() {
			if input.Status == domainaction.NativeMutationStatusRejected && current.Status == input.Status &&
				current.ErrorCode == input.ErrorCode && bytes.Equal(current.Result, result) &&
				action.Status == domainaction.StatusFailed && attempt.Status == domainaction.AttemptStatusFailed && len(attempt.NativeDelegation.RunResult) == 0 {
				resultAction = cloneNativeAction(action)
				resultAttempt = attempt.Clone()
				return nil
			}
			return conflict("native delegation already has a different terminal outcome")
		}
		if current.Status == input.Status {
			if bytes.Equal(current.Result, result) && current.ErrorCode == input.ErrorCode {
				resultAction = cloneNativeAction(action)
				resultAttempt = attempt.Clone()
				return nil
			}
			return conflict("mutation outcome is immutable")
		}
		if current.Status != domainaction.NativeMutationStatusDeliveryUnknown {
			return conflict("definite outcome requires a delivery-unknown mutation")
		}
		current.Status = input.Status
		current.Result = append([]byte(nil), result...)
		current.ErrorCode = input.ErrorCode
		if err := current.Validate(); err != nil {
			return conflict("mutation outcome is invalid")
		}
		if input.Status == domainaction.NativeMutationStatusAccepted {
			if err := attempt.Validate(); err != nil {
				return conflict("accepted mutation produced invalid native state")
			}
			if err := tx.SaveAttempt(ctx, attempt); err != nil {
				return err
			}
			resultAction = cloneNativeAction(action)
			resultAttempt = attempt.Clone()
			return nil
		}
		now := m.now().UTC()
		summary := "native " + string(input.Slot) + " mutation rejected"
		closedAttempt, err := attempt.Close(domainaction.AttemptStatusFailed, now, summary)
		if err != nil {
			return err
		}
		closedAction, err := action.Close(domainaction.StatusFailed, now, summary)
		if err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, closedAttempt); err != nil {
			return err
		}
		if err := tx.SaveAction(ctx, closedAction); err != nil {
			return err
		}
		resultAction = cloneNativeAction(closedAction)
		resultAttempt = closedAttempt.Clone()
		return nil
	})
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return resultAction, resultAttempt, nil
}

func (m *Manager) RecordNativeObservation(ctx context.Context, input RecordNativeObservationInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	observation := input.Observation
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = m.now().UTC()
	}
	if err := observation.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("observation is invalid")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updateNativeAttempt(ctx, input.ActionID, input.AttemptID, func(attempt *domainaction.Attempt, _ time.Time) (bool, error) {
		copy := observation
		attempt.NativeDelegation.LastObservation = &copy
		return true, nil
	})
}

// CompleteNativeDelegation records the immutable Harness result and terminal
// Action/Attempt in one owner transaction.
func (m *Manager) CompleteNativeDelegation(ctx context.Context, input CompleteNativeDelegationInput) (domainaction.Action, domainaction.Attempt, error) {
	if err := validateContext(ctx); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	result := append([]byte(nil), input.RunResult...)
	if len(result) == 0 || !domainaction.IsAttemptTerminal(input.AttemptStatus) || !domainaction.IsTerminal(input.ActionStatus) {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("terminal completion requires result bytes and terminal statuses")
	}
	if input.AttemptStatus == domainaction.AttemptStatusSucceeded || input.ActionStatus == domainaction.StatusSucceeded {
		if input.AttemptStatus != domainaction.AttemptStatusSucceeded || input.ActionStatus != domainaction.StatusSucceeded || input.Proof == nil {
			return domainaction.Action{}, domainaction.Attempt{}, conflict("successful completion requires an atomic criteria proof")
		}
	}
	if input.Proof != nil {
		if err := input.Proof.Validate(); err != nil {
			return domainaction.Action{}, domainaction.Attempt{}, conflict("criteria proof is invalid")
		}
		hash := sha256.Sum256(result)
		if hex.EncodeToString(hash[:]) != input.Proof.RunResultSHA256 {
			return domainaction.Action{}, domainaction.Attempt{}, conflict("criteria proof does not bind the RunResult bytes")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var resultAction domainaction.Action
	var resultAttempt domainaction.Attempt
	err := m.store.Transaction(ctx, func(tx Store) error {
		action, err := tx.GetAction(ctx, input.ActionID)
		if err != nil {
			return err
		}
		attempt, err := tx.GetAttempt(ctx, input.AttemptID)
		if err != nil {
			return err
		}
		if action.CurrentAttemptID != input.AttemptID || attempt.ActionID != action.ActionID || attempt.NativeDelegation == nil || !isNativeDelegationAction(action) {
			return conflict("completion pair is not the current typed native delegation")
		}
		if input.Proof != nil && input.Proof.ExpectedCriteriaRevision != attempt.NativeDelegation.ExpectedCriteriaRevision {
			return conflict("criteria proof does not match the frozen Task revision")
		}
		if !action.IsOpen() || !attempt.IsActive() {
			delegation := attempt.NativeDelegation
			if action.CurrentAttemptID == input.AttemptID && !action.IsOpen() && !attempt.IsActive() &&
				attempt.Status == input.AttemptStatus && action.Status == input.ActionStatus &&
				bytes.Equal(delegation.RunResult, result) && sameNativeProof(delegation.Proof, input.Proof) &&
				action.Summary == strings.TrimSpace(input.Summary) && attempt.Summary == strings.TrimSpace(input.Summary) {
				resultAction = cloneNativeAction(action)
				resultAttempt = attempt.Clone()
				return nil
			}
			return conflict("native delegation is already terminal or not active")
		}
		delegation := attempt.NativeDelegation
		mode := delegation.EffectiveMode()
		if mode == domainaction.NativeDelegationModeOpenStart && (delegation.Start == nil || delegation.Start.Status != domainaction.NativeMutationStatusAccepted || len(delegation.Start.Result) == 0) {
			return conflict("completion requires an accepted start result")
		}
		if mode == domainaction.NativeDelegationModeResume && (delegation.Resume == nil || delegation.Resume.Status != domainaction.NativeMutationStatusAccepted || len(delegation.Resume.Result) == 0) {
			return conflict("completion requires an accepted Resume result")
		}
		if len(delegation.RunResult) != 0 {
			if !bytes.Equal(delegation.RunResult, result) {
				return conflict("native run result is immutable")
			}
			return conflict("run result is already recorded")
		}
		now := m.now().UTC()
		delegation.RunResult = append([]byte(nil), result...)
		if input.Proof != nil {
			proof := *input.Proof
			proof.Evidence = append([]domainaction.NativeDelegationEvidenceProof(nil), input.Proof.Evidence...)
			delegation.Proof = &proof
		}
		closedAttempt, err := attempt.Close(input.AttemptStatus, now, strings.TrimSpace(input.Summary))
		if err != nil {
			return err
		}
		closedAction, err := action.Close(input.ActionStatus, now, strings.TrimSpace(input.Summary))
		if err != nil {
			return err
		}
		if err := tx.SaveAttempt(ctx, closedAttempt); err != nil {
			return err
		}
		if err := tx.SaveAction(ctx, closedAction); err != nil {
			return err
		}
		resultAction = cloneNativeAction(closedAction)
		resultAttempt = closedAttempt.Clone()
		return nil
	})
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return resultAction, resultAttempt, nil
}

func (m *Manager) updateNativeAttempt(ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID, update func(*domainaction.Attempt, time.Time) (bool, error)) (domainaction.Action, domainaction.Attempt, error) {
	if err := actionID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("action_id is invalid: %w", err)
	}
	if err := attemptID.Validate(); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, fmt.Errorf("attempt_id is invalid: %w", err)
	}
	var resultAction domainaction.Action
	var resultAttempt domainaction.Attempt
	err := m.store.Transaction(ctx, func(tx Store) error {
		action, attempt, err := currentNativePair(tx, ctx, actionID, attemptID)
		if err != nil {
			return err
		}
		changed, err := update(&attempt, m.now().UTC())
		if err != nil {
			return err
		}
		if changed {
			if err := attempt.Validate(); err != nil {
				return conflict("updated native attempt is invalid")
			}
			if err := tx.SaveAttempt(ctx, attempt); err != nil {
				return err
			}
		}
		resultAction = cloneNativeAction(action)
		resultAttempt = attempt.Clone()
		return nil
	})
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	return resultAction, resultAttempt, nil
}

func currentNativePair(tx Store, ctx context.Context, actionID modulecore.ActionID, attemptID modulecore.AttemptID) (domainaction.Action, domainaction.Attempt, error) {
	action, err := tx.GetAction(ctx, actionID)
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	attempt, err := tx.GetAttempt(ctx, attemptID)
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if !isNativeDelegationAction(action) || action.CurrentAttemptID != attemptID || attempt.ActionID != actionID || attempt.NativeDelegation == nil || !action.IsOpen() || !attempt.IsActive() {
		return domainaction.Action{}, domainaction.Attempt{}, conflict("action and attempt are not the active typed native delegation pair")
	}
	return action, attempt, nil
}

func nativeMutationSlot(delegation *domainaction.NativeDelegation, slot domainaction.NativeMutationSlot) **domainaction.NativeMutation {
	if delegation == nil {
		return nil
	}
	switch slot {
	case domainaction.NativeMutationSlotOpen:
		if delegation.EffectiveMode() != domainaction.NativeDelegationModeOpenStart {
			return nil
		}
		return &delegation.Open
	case domainaction.NativeMutationSlotStart:
		if delegation.EffectiveMode() != domainaction.NativeDelegationModeOpenStart {
			return nil
		}
		return &delegation.Start
	case domainaction.NativeMutationSlotResume:
		if delegation.EffectiveMode() != domainaction.NativeDelegationModeResume {
			return nil
		}
		return &delegation.Resume
	default:
		return nil
	}
}

func nativeReferencesEqual(left, right *conversation.AcceptedOPSInputReference) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameNativeProof(left, right *domainaction.NativeDelegationProof) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Version != right.Version || left.ExpectedCriteriaRevision != right.ExpectedCriteriaRevision ||
		left.RunResultSHA256 != right.RunResultSHA256 || left.TerminalEventID != right.TerminalEventID ||
		left.TerminalEventSeq != right.TerminalEventSeq || len(left.Evidence) != len(right.Evidence) {
		return false
	}
	for index := range left.Evidence {
		if left.Evidence[index] != right.Evidence[index] {
			return false
		}
	}
	return true
}

func isNativeDelegationAction(action domainaction.Action) bool {
	return action.Kind == domainaction.KindDelegation && domainaction.NormalizeName(action.Name) == domainaction.NativeDelegationActionName
}

func cloneNativeAction(action domainaction.Action) domainaction.Action {
	if action.CompletedAt != nil {
		completedAt := *action.CompletedAt
		action.CompletedAt = &completedAt
	}
	return action
}

func conflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrNativeDelegationConflict, fmt.Sprintf(format, args...))
}
