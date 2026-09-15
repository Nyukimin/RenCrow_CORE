package heartbeat

import (
	"context"
	"errors"
	"sync"
)

// acquireCollectionWorker reserves the single execution slot shared by the
// Gmail and X Bookmark collectors. The slot is deliberately separate from the
// Task owner limits: waiting here keeps a canceled request from creating a
// queued Task that can never run.
func (s *HeartbeatService) acquireCollectionWorker(ctx context.Context) (func(), error) {
	if s == nil {
		return nil, errors.New("heartbeat collection service is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("heartbeat collection context is required")
	}
	s.collectionWorkerSlotOnce.Do(func() {
		s.collectionWorkerSlot = make(chan struct{}, 1)
		s.collectionWorkerSlot <- struct{}{}
	})

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.stopCh:
		return nil, context.Canceled
	case <-s.collectionWorkerSlot:
		if err := ctx.Err(); err != nil {
			s.collectionWorkerSlot <- struct{}{}
			return nil, err
		}
		select {
		case <-s.stopCh:
			s.collectionWorkerSlot <- struct{}{}
			return nil, context.Canceled
		default:
		}
		var releaseOnce sync.Once
		return func() {
			releaseOnce.Do(func() {
				s.collectionWorkerSlot <- struct{}{}
			})
		}, nil
	}
}
