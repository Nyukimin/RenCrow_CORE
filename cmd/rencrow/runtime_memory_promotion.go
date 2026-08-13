package main

import (
	"context"
	"errors"
	"log"
	"time"

	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
)

type memoryPromotionRunner interface {
	RunOne(context.Context) (memorypromotionapp.RunResult, error)
}

func startMemoryPromotionWorker(
	runner memoryPromotionRunner,
	tracker *llmBusyTracker,
	idleGrace time.Duration,
	timeout time.Duration,
	reporter backgroundJobFailureReporter,
) context.CancelFunc {
	return startMemoryPromotionWorkerRunner(runner, tracker, idleGrace, timeout, time.Second, reporter)
}

func startMemoryPromotionWorkerRunner(
	runner memoryPromotionRunner,
	tracker *llmBusyTracker,
	idleGrace time.Duration,
	timeout time.Duration,
	pollInterval time.Duration,
	reporter backgroundJobFailureReporter,
) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	if runner == nil || tracker == nil {
		return cancel
	}
	if idleGrace <= 0 {
		idleGrace = 10 * time.Second
	}
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		var idleSince time.Time
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if tracker.ExternalBusy() {
					idleSince = time.Time{}
					continue
				}
				if idleSince.IsZero() {
					idleSince = now
					continue
				}
				if now.Sub(idleSince) < idleGrace {
					continue
				}
				leaseCtx, release, ok := tracker.TryAcquireIdleLease(ctx)
				if !ok {
					continue
				}
				for {
					runCtx, runCancel := context.WithTimeout(leaseCtx, timeout)
					result, err := runner.RunOne(runCtx)
					runCancel()
					if err != nil {
						if !errors.Is(err, context.Canceled) {
							reporter.Failed("memory_profile_promotion", err, "L1 raw UserMemory candidate extraction")
						}
						break
					}
					if !result.Processed {
						break
					}
					log.Printf(
						"Memory ProfilePromotion processed messages=%d candidates=%d",
						result.MessageCount,
						result.CandidateCount,
					)
					select {
					case <-leaseCtx.Done():
						break
					default:
						continue
					}
					break
				}
				release()
				idleSince = time.Time{}
			}
		}
	}()
	return cancel
}
