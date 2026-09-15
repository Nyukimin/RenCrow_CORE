package idlechat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// finishRunningWordCheckpoint closes the exact Run that a retained word
// checkpoint still names. A matching stock item is proof that generation was
// published; otherwise the owner is moved to waiting so a later admission can
// resume the saved checkpoint. It never issues a successor Run.
func (o *IdleChatOrchestrator) finishRunningWordCheckpoint(ctx context.Context, stock *wordTopicStock, checkpoint GenerationCheckpoint) (bool, error) {
	if stock == nil {
		return false, errors.New("word topic stock is not configured")
	}
	stock.mu.Lock()
	stockPath, stockLoadErr := stock.path, stock.loadErr
	stock.mu.Unlock()
	if strings.TrimSpace(stockPath) == "" {
		return false, errors.New("word topic stock is not configured")
	}
	if stockLoadErr != nil {
		return false, fmt.Errorf("word topic stock unavailable: %w", stockLoadErr)
	}
	if stock.hasRunID(checkpoint.RunID) {
		if err := verifySavedWordTopic(stock, checkpoint); err != nil {
			return false, err
		}
		if err := o.finishWordRun(ctx, checkpoint, domaintask.StatusSucceeded, "word topic saved", ""); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := o.finishWordRun(ctx, checkpoint, domaintask.StatusWaiting, "word generation interrupted", "retry from saved generation checkpoint"); err != nil {
		return false, err
	}
	return false, nil
}

func validateWordCheckpoint(checkpoint GenerationCheckpoint, category TopicCategory) error {
	if checkpoint.Key != "word:"+string(category) {
		return errors.New("word checkpoint key does not match category")
	}
	if checkpoint.Kind != "word" {
		return errors.New("word checkpoint kind does not match")
	}
	if checkpoint.Category != category {
		return errors.New("word checkpoint category does not match")
	}
	return validateIdleChatRunIdentity(checkpoint.TaskID, checkpoint.RunID)
}

// finalizePendingWordRuns retries only retained Running word pairs after all
// generation work has joined. It deliberately does not resume generation,
// issue a successor, call a provider, or mutate stock.
func (o *IdleChatOrchestrator) finalizePendingWordRuns(ctx context.Context) error {
	if o == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("word completion retry context is nil")
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
	stock := o.wordTopicStock
	o.mu.Unlock()

	var errs []error
	for _, category := range wordTopicStockCategories {
		if err := ctx.Err(); err != nil {
			errs = append(errs, err)
			break
		}
		key := "word:" + string(category)
		checkpoint, found := store.Get(key)
		if !found {
			continue
		}
		if err := validateWordCheckpoint(checkpoint, category); err != nil {
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
		saved, err := o.finishRunningWordCheckpoint(ctx, stock, checkpoint)
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
