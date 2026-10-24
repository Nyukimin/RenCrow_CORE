package superagent

import (
	"context"
	"errors"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
)

// ErrAdmissionQueueMismatch indicates that a request carried an explicit
// persistence capability for a different queue than the store is configured
// to use. Admitted writes fail closed instead of falling back to direct I/O.
var ErrAdmissionQueueMismatch = errors.New("SuperAgent persistence admission does not match the store queue")

func enqueueAdmittedSuperAgentWrite(
	ctx context.Context,
	queue *persistencequeue.Queue[persistencequeue.PersistenceCommand],
	kind string,
	payload string,
	apply func(context.Context) error,
) (bool, error) {
	admission, admitted := persistencequeue.AdmissionFromContext(ctx)
	if !admitted {
		return false, nil
	}
	if queue == nil || !admission.MatchesQueue(queue) {
		return true, ErrAdmissionQueueMismatch
	}
	if payload == "" || apply == nil {
		return true, persistencequeue.ErrInvalidPersistenceCommand
	}
	_, err := admission.Enqueue(persistencequeue.PersistenceCommand{
		Owner:     persistencequeue.OwnerSuperAgent,
		Kind:      kind,
		SizeBytes: int64(len(payload)),
		Apply:     apply,
	})
	return true, err
}
