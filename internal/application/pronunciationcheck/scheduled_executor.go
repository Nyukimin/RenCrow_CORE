package pronunciationcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
)

const ScheduledTarget = "tts_pronunciation_check"

type ScheduledExecutor struct {
	service *Service
}

func NewScheduledExecutor(service *Service) *ScheduledExecutor {
	return &ScheduledExecutor{service: service}
}

func (e *ScheduledExecutor) ExecuteScheduledJob(ctx context.Context, job domainscheduler.Job) (string, error) {
	if job.Target != ScheduledTarget {
		return "", fmt.Errorf("unsupported scheduled target %q", job.Target)
	}
	report, err := e.service.Run(ctx)
	if err != nil {
		var deferred *DeferredError
		if errors.As(err, &deferred) {
			return deferred.Reason, schedulerapp.NewDeferredError(deferred.RetryAfter, deferred)
		}
		return "", err
	}
	summary, err := json.Marshal(report)
	if err != nil {
		return "", err
	}
	return string(summary), nil
}
