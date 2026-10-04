package l1sqlite

import (
	"context"
	"errors"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type userMemoryStorageHostResult struct {
	Memory *domainmemory.UserMemory `json:"memory"`
}

type userMemoryStorageHostCandidateResult struct {
	Memory           *domainmemory.UserMemory `json:"memory"`
	IdempotentReplay bool                     `json:"idempotent_replay"`
}

func (s *L1SQLiteStore) CreateUserMemoryForStorageHostOperation(ctx context.Context, identity UserMemoryStorageHostOperationIdentity, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error) {
	if identity.Operation != "create_user_memory" {
		return nil, errors.New("user memory create operation identity mismatch")
	}
	tx, prior, found, err := s.beginUserMemoryStorageHostOperation(ctx, identity)
	if err != nil {
		return nil, err
	}
	if found {
		var result userMemoryStorageHostResult
		if err := decodeConversationL1OperationResult(prior, &result); err != nil {
			return nil, err
		}
		if result.Memory == nil {
			return nil, errors.New("user memory receipt result is empty")
		}
		return result.Memory, nil
	}
	defer tx.Rollback()
	return s.createUserMemoryWithTx(ctx, tx, &identity, input)
}
func (s *L1SQLiteStore) CreateUserMemoryCandidateWithRequestForStorageHostOperation(ctx context.Context, identity UserMemoryStorageHostOperationIdentity, requestID, actorID string, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, bool, error) {
	if identity.Operation != "create_user_memory_candidate_with_request" {
		return nil, false, errors.New("user memory candidate operation identity mismatch")
	}
	tx, prior, found, err := s.beginUserMemoryStorageHostOperation(ctx, identity)
	if err != nil {
		return nil, false, err
	}
	if found {
		var result userMemoryStorageHostCandidateResult
		if err := decodeConversationL1OperationResult(prior, &result); err != nil {
			return nil, false, err
		}
		if result.Memory == nil {
			return nil, false, errors.New("user memory candidate receipt result is empty")
		}
		return result.Memory, result.IdempotentReplay, nil
	}
	defer tx.Rollback()
	return s.createUserMemoryCandidateWithRequest(ctx, tx, &identity, requestID, actorID, input)
}
func (s *L1SQLiteStore) UpdateUserMemoryStateForStorageHostOperation(ctx context.Context, identity UserMemoryStorageHostOperationIdentity, id, state, reason string) (*domainmemory.UserMemory, error) {
	if identity.Operation != "update_user_memory_state" {
		return nil, errors.New("user memory state operation identity mismatch")
	}
	tx, prior, found, err := s.beginUserMemoryStorageHostOperation(ctx, identity)
	if err != nil {
		return nil, err
	}
	if found {
		return decodeUserMemoryItemReceipt(prior)
	}
	defer tx.Rollback()
	return s.updateUserMemoryState(ctx, tx, &identity, id, state, reason)
}
func (s *L1SQLiteStore) ForgetUserMemoryForStorageHostOperation(ctx context.Context, identity UserMemoryStorageHostOperationIdentity, id, reason string) (*domainmemory.UserMemory, error) {
	if identity.Operation != "forget_user_memory" {
		return nil, errors.New("user memory forget operation identity mismatch")
	}
	tx, prior, found, err := s.beginUserMemoryStorageHostOperation(ctx, identity)
	if err != nil {
		return nil, err
	}
	if found {
		return decodeUserMemoryItemReceipt(prior)
	}
	defer tx.Rollback()
	return s.forgetUserMemory(ctx, tx, &identity, id, reason)
}
func (s *L1SQLiteStore) SupersedeUserMemoryForStorageHostOperation(ctx context.Context, identity UserMemoryStorageHostOperationIdentity, id, newID, reason string) (*domainmemory.UserMemory, error) {
	if identity.Operation != "supersede_user_memory" {
		return nil, errors.New("user memory supersede operation identity mismatch")
	}
	tx, prior, found, err := s.beginUserMemoryStorageHostOperation(ctx, identity)
	if err != nil {
		return nil, err
	}
	if found {
		return decodeUserMemoryItemReceipt(prior)
	}
	defer tx.Rollback()
	return s.supersedeUserMemory(ctx, tx, &identity, id, newID, reason)
}

func decodeUserMemoryItemReceipt(raw []byte) (*domainmemory.UserMemory, error) {
	var result userMemoryStorageHostResult
	if err := decodeConversationL1OperationResult(raw, &result); err != nil {
		return nil, err
	}
	if result.Memory == nil {
		return nil, errors.New("user memory receipt result is empty")
	}
	return result.Memory, nil
}
