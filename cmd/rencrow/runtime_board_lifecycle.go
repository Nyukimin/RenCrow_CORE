package main

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
)

const (
	boardCommunicationLifecycleMinRetry = 250 * time.Millisecond
	boardCommunicationLifecycleMaxRetry = 5 * time.Second
)

var ErrBoardCommunicationLifecycleUnavailable = errors.New("BOARD communication lifecycle unavailable")

type boardCommunicationLifecycleGeneration struct {
	openEvents func(context.Context, int64) (boardCommunicationEventStream, error)
	catchUp    func(context.Context) error
	close      func()
}

type boardCommunicationEventStream interface {
	Next() (boardcommunication.BoardEvent, error)
	Close() error
}

type boardCommunicationLifecycleDeps struct {
	enabled bool
	open    func(context.Context) (boardCommunicationLifecycleGeneration, error)
	wait    func(context.Context, time.Duration) error
}

// runBoardCommunicationLifecycle owns only the optional BOARD inbox wake
// lifecycle. Connection opening and durable inbox catch-up stay with their
// existing owners and are injected per connected generation.
func runBoardCommunicationLifecycle(ctx context.Context, deps boardCommunicationLifecycleDeps) error {
	if !deps.enabled {
		return nil
	}
	if ctx == nil || deps.open == nil {
		return ErrBoardCommunicationLifecycleUnavailable
	}

	retryDelay := boardCommunicationLifecycleMinRetry
	var lastSequence atomic.Int64
	for ctx.Err() == nil {
		generation, err := deps.open(ctx)
		if ctx.Err() != nil {
			closeBoardCommunicationLifecycleGeneration(generation, nil, nil)
			return nil
		}
		if err != nil {
			if !waitBoardCommunicationLifecycleRetry(ctx, deps.wait, retryDelay) {
				return nil
			}
			retryDelay = nextBoardCommunicationLifecycleRetry(retryDelay)
			continue
		}
		if generation.openEvents == nil || generation.catchUp == nil || generation.close == nil {
			closeBoardCommunicationLifecycleGeneration(generation, nil, nil)
			return ErrBoardCommunicationLifecycleUnavailable
		}

		stream, err := generation.openEvents(ctx, lastSequence.Load())
		if ctx.Err() != nil {
			closeBoardCommunicationLifecycleGeneration(generation, stream, nil)
			return nil
		}
		if err != nil || stream == nil {
			closeBoardCommunicationLifecycleGeneration(generation, stream, nil)
			if !waitBoardCommunicationLifecycleRetry(ctx, deps.wait, retryDelay) {
				return nil
			}
			retryDelay = nextBoardCommunicationLifecycleRetry(retryDelay)
			continue
		}

		wake := make(chan struct{}, 1)
		streamErrors := make(chan error, 1)
		readerDone := make(chan struct{})
		go drainBoardCommunicationHints(ctx, stream, &lastSequence, wake, streamErrors, readerDone)

		if err := generation.catchUp(ctx); err != nil {
			closeBoardCommunicationLifecycleGeneration(generation, stream, readerDone)
			if ctx.Err() != nil {
				return nil
			}
			if !waitBoardCommunicationLifecycleRetry(ctx, deps.wait, retryDelay) {
				return nil
			}
			retryDelay = nextBoardCommunicationLifecycleRetry(retryDelay)
			continue
		}

		var generationErr error
		for generationErr == nil {
			select {
			case <-ctx.Done():
				closeBoardCommunicationLifecycleGeneration(generation, stream, readerDone)
				return nil
			case <-wake:
				generationErr = generation.catchUp(ctx)
			case generationErr = <-streamErrors:
			}
		}
		closeBoardCommunicationLifecycleGeneration(generation, stream, readerDone)
		if ctx.Err() != nil {
			return nil
		}
		if !waitBoardCommunicationLifecycleRetry(ctx, deps.wait, retryDelay) {
			return nil
		}
		retryDelay = nextBoardCommunicationLifecycleRetry(retryDelay)
	}
	return nil
}

func drainBoardCommunicationHints(
	ctx context.Context,
	stream boardCommunicationEventStream,
	lastSequence *atomic.Int64,
	wake chan<- struct{},
	streamErrors chan<- error,
	readerDone chan<- struct{},
) {
	defer close(readerDone)
	for {
		event, err := stream.Next()
		if err != nil {
			if ctx.Err() == nil {
				select {
				case streamErrors <- err:
				default:
				}
			}
			return
		}
		previous := lastSequence.Load()
		if event.Seq <= previous {
			select {
			case streamErrors <- boardcommunication.ErrProtocol:
			default:
			}
			return
		}
		lastSequence.Store(event.Seq)
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

func closeBoardCommunicationLifecycleGeneration(
	generation boardCommunicationLifecycleGeneration,
	stream boardCommunicationEventStream,
	readerDone <-chan struct{},
) {
	if stream != nil {
		_ = stream.Close()
	}
	if generation.close != nil {
		generation.close()
	}
	if readerDone != nil {
		<-readerDone
	}
}

func waitBoardCommunicationLifecycleRetry(
	ctx context.Context,
	wait func(context.Context, time.Duration) error,
	delay time.Duration,
) bool {
	if ctx.Err() != nil {
		return false
	}
	if wait != nil {
		return wait(ctx, delay) == nil && ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

func nextBoardCommunicationLifecycleRetry(delay time.Duration) time.Duration {
	if delay >= boardCommunicationLifecycleMaxRetry/2 {
		return boardCommunicationLifecycleMaxRetry
	}
	next := delay * 2
	if next > boardCommunicationLifecycleMaxRetry {
		return boardCommunicationLifecycleMaxRetry
	}
	return next
}
