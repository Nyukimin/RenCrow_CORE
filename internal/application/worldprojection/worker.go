package worldprojection

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const (
	DefaultDrainLimit = 32
	DefaultRetryBase  = time.Second
	DefaultRetryMax   = 5 * time.Minute
)

// DrainResult reports one bounded outbox delivery pass.
type DrainResult struct {
	Claimed int
	Sent    int
	Retried int
	Failed  int
}

// Worker drains CORE's durable projection outbox. It holds no in-memory queue:
// a crash after Publish but before Complete causes an idempotent replay of the
// same envelope, which WORLD accepts as a duplicate.
type Worker struct {
	Outbox     Outbox
	Publisher  Publisher
	HeadReader HeadReader
	Now        func() time.Time
	Limit      int
	RetryBase  time.Duration
	RetryMax   time.Duration
}

var errHeadVerificationMismatch = errors.New("world projection: accepted head does not match the submitted revision and digest")

func (w *Worker) Drain(ctx context.Context) (DrainResult, error) {
	var result DrainResult
	if w == nil || w.Outbox == nil || w.Publisher == nil {
		return result, errors.New("world projection worker: outbox and publisher are required")
	}
	if ctx == nil {
		return result, errors.New("world projection worker: context is nil")
	}
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	limit := w.Limit
	if limit <= 0 {
		limit = DefaultDrainLimit
	}
	records, err := w.Outbox.Claim(ctx, now, limit)
	if err != nil {
		return result, fmt.Errorf("world projection worker: claim: %w", err)
	}
	result.Claimed = len(records)
	for _, record := range records {
		publishResult, publishErr := w.Publisher.Publish(ctx, record.Target, record.Kind, record.Envelope, record.Reconcile)
		switch {
		case publishErr == nil && publishResult.Successful():
			if publishResult.Status == "epoch_unchanged" {
				if verifyErr := w.verifyEpochUnchanged(ctx, record); verifyErr != nil {
					message := "epoch_unchanged head verification failed: " + verifyErr.Error()
					if headReadRetryable(verifyErr) {
						retryAt := now.Add(w.retryDelay(record.Attempts))
						if err := w.Outbox.Fail(ctx, record.ID, retryAt, false, message, now); err != nil {
							return result, fmt.Errorf("world projection worker: schedule head retry %s: %w", record.ID, err)
						}
						result.Retried++
					} else {
						if err := w.Outbox.Fail(ctx, record.ID, time.Time{}, true, message, now); err != nil {
							return result, fmt.Errorf("world projection worker: fail head verification %s: %w", record.ID, err)
						}
						result.Failed++
					}
					continue
				}
			}
			if err := w.Outbox.Complete(ctx, record.ID, publishResult, now); err != nil {
				return result, fmt.Errorf("world projection worker: complete %s: %w", record.ID, err)
			}
			result.Sent++
		case publishResult.Retryable:
			message := publishResult.Status
			if publishErr != nil {
				message = publishErr.Error()
			}
			retryAt := now.Add(w.retryDelay(record.Attempts))
			if err := w.Outbox.Fail(ctx, record.ID, retryAt, false, message, now); err != nil {
				return result, fmt.Errorf("world projection worker: schedule retry %s: %w", record.ID, err)
			}
			result.Retried++
		default:
			message := publishResult.Status
			if publishResult.Reason != "" {
				message += ": " + publishResult.Reason
			}
			if publishErr != nil {
				message = publishErr.Error()
			}
			if err := w.Outbox.Fail(ctx, record.ID, time.Time{}, true, message, now); err != nil {
				return result, fmt.Errorf("world projection worker: fail %s: %w", record.ID, err)
			}
			result.Failed++
		}
	}
	return result, nil
}

func (w *Worker) verifyEpochUnchanged(ctx context.Context, record OutboxRecord) error {
	if w.HeadReader == nil {
		return errors.New("head reader is not configured")
	}
	head, err := w.HeadReader.ReadHead(ctx, record.Target, record.Kind)
	if err != nil {
		return err
	}
	if head.ActorID != record.Target.ActorID ||
		head.ActorInstanceID != record.Target.ActorInstanceID ||
		head.Kind != record.Kind ||
		head.SourceEpoch != record.SourceEpoch ||
		head.SourceRevision != record.SourceRevision ||
		head.Digest != record.Digest {
		return fmt.Errorf("%w: head actor=%q instance=%q epoch=%q revision=%d digest=%q",
			errHeadVerificationMismatch, head.ActorID, head.ActorInstanceID,
			head.SourceEpoch, head.SourceRevision, head.Digest)
	}
	return nil
}

func headReadRetryable(err error) bool {
	if err == nil || errors.Is(err, ErrProjectionHeadNotFound) || errors.Is(err, errHeadVerificationMismatch) {
		return false
	}
	var readErr *HeadReadError
	if errors.As(err, &readErr) {
		return readErr.Retryable
	}
	return false
}

func (w *Worker) retryDelay(attempts int) time.Duration {
	base := w.RetryBase
	if base <= 0 {
		base = DefaultRetryBase
	}
	max := w.RetryMax
	if max <= 0 {
		max = DefaultRetryMax
	}
	if max < base {
		max = base
	}
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 30 {
		return max
	}
	delay := base
	for i := 0; i < attempts; i++ {
		if delay >= max/2 {
			return max
		}
		delay *= 2
	}
	if delay > max {
		return max
	}
	return delay
}
