package idlechat

import (
	"context"
	"errors"
)

func (o *IdleChatOrchestrator) finalizePendingTopicRuns(ctx context.Context) error {
	if o == nil {
		return nil
	}
	var errs []error
	if err := o.finalizePendingWordRuns(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := o.finalizePendingForecastRuns(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// retryPendingTopicCompletions runs maintenance under the existing topic
// producer single-flight gate. A live producer keeps ownership and is left
// untouched; callers retry on the next monitor tick or during Stop.
func (o *IdleChatOrchestrator) retryPendingTopicCompletions() error {
	if o == nil || !o.tryBeginTopicProduction() {
		return nil
	}
	defer o.endTopicProduction()
	ctx, cancel := o.conversationRunCleanupContext()
	defer cancel()
	return o.finalizePendingTopicRuns(ctx)
}
