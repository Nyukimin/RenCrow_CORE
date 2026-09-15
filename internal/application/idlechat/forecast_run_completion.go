package idlechat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func validateForecastCheckpoint(checkpoint GenerationCheckpoint, domain ForecastDomain) error {
	domainName := strings.TrimSpace(domain.Name)
	if domainName == "" {
		return errors.New("forecast checkpoint domain is empty")
	}
	if checkpoint.Key != "forecast:"+domainName {
		return errors.New("forecast checkpoint key does not match domain")
	}
	if checkpoint.Kind != "forecast" {
		return errors.New("forecast checkpoint kind does not match")
	}
	if checkpoint.Category != TopicCategoryForecast {
		return errors.New("forecast checkpoint category does not match")
	}
	if checkpoint.Domain.Name != domainName {
		return fmt.Errorf("forecast checkpoint domain mismatch: got %s, want %s", checkpoint.Domain.Name, domainName)
	}
	if err := validateIdleChatRunIdentity(checkpoint.TaskID, checkpoint.RunID); err != nil {
		return fmt.Errorf("forecast checkpoint identity: %w", err)
	}
	if checkpoint.Result == nil {
		return nil
	}
	if strings.TrimSpace(checkpoint.Result.Topic) == "" {
		return errors.New("forecast checkpoint result topic is empty")
	}
	if checkpoint.Result.Category != "" && checkpoint.Result.Category != TopicCategoryForecast {
		return fmt.Errorf("forecast checkpoint result category mismatch: got %s", checkpoint.Result.Category)
	}
	if checkpoint.Result.Seed.Category != "" && checkpoint.Result.Seed.Category != TopicCategoryForecast {
		return fmt.Errorf("forecast checkpoint result seed category mismatch: got %s", checkpoint.Result.Seed.Category)
	}
	if resultDomain := strings.TrimSpace(checkpoint.Result.Seed.ForecastDomain); resultDomain != "" && resultDomain != domainName {
		return fmt.Errorf("forecast checkpoint result domain mismatch: got %s, want %s", resultDomain, domainName)
	}
	return nil
}

// finishRunningForecastCheckpoint closes the exact Run retained by a Forecast
// checkpoint. A matching stock item proves publication; without one, the
// owner is moved to waiting so a later admission can resume the checkpoint.
// It never issues a successor Run or invokes a provider.
func (o *IdleChatOrchestrator) finishRunningForecastCheckpoint(ctx context.Context, stock *forecastTopicStock, checkpoint GenerationCheckpoint) (bool, error) {
	if o == nil {
		return false, errors.New("forecast topic orchestrator is not configured")
	}
	if err := validateForecastCheckpoint(checkpoint, checkpoint.Domain); err != nil {
		return false, err
	}
	if stock == nil {
		return false, errors.New("forecast topic stock is not configured")
	}
	stock.mu.Lock()
	stockPath, stockLoadErr := stock.path, stock.loadErr
	stock.mu.Unlock()
	if strings.TrimSpace(stockPath) == "" {
		return false, errors.New("forecast topic stock is not configured")
	}
	if stockLoadErr != nil {
		return false, fmt.Errorf("forecast topic stock unavailable: %w", stockLoadErr)
	}
	o.mu.Lock()
	issuer := o.runIssuer
	o.mu.Unlock()
	if stock.hasRunID(checkpoint.RunID) {
		if !stock.hasExactCheckpointItem(checkpoint) {
			return false, errors.New("forecast topic completion artifact is mismatched")
		}
		recovered, err := recoverGenerationRunCompletionAfterProcessRestart(ctx, issuer, checkpoint, domaintask.StatusSucceeded, "forecast topic saved", "")
		if err != nil {
			return false, err
		}
		if recovered {
			return true, nil
		}
		if err := finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusSucceeded, "forecast topic saved", ""); err != nil {
			return false, err
		}
		return true, nil
	}
	recovered, err := recoverGenerationRunCompletionAfterProcessRestart(ctx, issuer, checkpoint, domaintask.StatusWaiting, "forecast generation interrupted", "retry from saved generation checkpoint")
	if err != nil {
		return false, err
	}
	if recovered {
		return false, nil
	}
	if err := finishGenerationRun(ctx, issuer, checkpoint, domaintask.StatusWaiting, "forecast generation interrupted", "retry from saved generation checkpoint"); err != nil {
		return false, err
	}
	return false, nil
}

// finalizePendingForecastRuns retries only retained Running Forecast pairs
// after generation work has joined. It does not resume generation, issue a
// successor, invoke a provider, or mutate stock.
func (o *IdleChatOrchestrator) finalizePendingForecastRuns(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("forecast completion retry context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	store := o.generationCheckpointStore()
	if store == nil {
		return nil
	}
	if err := store.LoadError(); err != nil {
		return err
	}
	o.mu.Lock()
	issuer := o.runIssuer
	stock := o.topicStockBuf
	o.mu.Unlock()

	var errs []error
	for _, domain := range forecastDomains {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		key := "forecast:" + domain.Name
		checkpoint, found := store.Get(key)
		if !found {
			continue
		}
		if err := validateForecastCheckpoint(checkpoint, domain); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
			continue
		}
		run, err := loadGenerationCheckpointRun(ctx, issuer, checkpoint)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
			continue
		}
		if run.Status != domaintask.RunStatusRunning {
			continue
		}
		saved, err := o.finishRunningForecastCheckpoint(ctx, stock, checkpoint)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", key, err))
			continue
		}
		if saved {
			if err := store.Delete(key); err != nil {
				errs = append(errs, fmt.Errorf("%s checkpoint cleanup: %w", key, err))
			}
		}
	}
	return errors.Join(errs...)
}
