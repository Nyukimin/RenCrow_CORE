package knowledgememory

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type DreamConsolidationStore interface {
	ListPersonalArchiveEntries(ctx context.Context, limit int) ([]domainkm.PersonalArchiveEntry, error)
	ListCreativeKnowledgeItems(ctx context.Context, limit int) ([]domainkm.CreativeKnowledgeItem, error)
	ListNewsKnowledgeItems(ctx context.Context, limit int) ([]domainkm.NewsKnowledgeItem, error)
	ListTemporalMemoryMarkers(ctx context.Context, limit int) ([]domainkm.TemporalMemoryMarker, error)
	SaveDreamConsolidationRun(ctx context.Context, item domainkm.DreamConsolidationRun) error
}

type DreamProposalInput struct {
	TaskID  modulecore.TaskID `json:"task_id"`
	RunID   modulecore.RunID  `json:"run_id"`
	ActorID string            `json:"actor_id"`
	Scope   []string          `json:"scope,omitempty"`
	Limit   int               `json:"limit,omitempty"`
	Now     time.Time         `json:"now,omitempty"`
}

func BuildDreamConsolidationProposal(ctx context.Context, store DreamConsolidationStore, input DreamProposalInput) (domainkm.DreamConsolidationRun, error) {
	if store == nil {
		return domainkm.DreamConsolidationRun{}, fmt.Errorf("dream consolidation store is nil")
	}
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}
	scope := compactScope(input.Scope)
	if len(scope) == 0 {
		scope = []string{"personal_archive", "creative_knowledge", "news_knowledge", "temporal_memory"}
	}
	seeds, err := dreamIdeaSeeds(ctx, store, scope, limit)
	if err != nil {
		return domainkm.DreamConsolidationRun{}, err
	}
	if len(seeds) == 0 {
		seeds = append(seeds, "no reviewed memory source was available; keep this dream proposal pending")
	}
	run := domainkm.DreamConsolidationRun{
		TaskID:       input.TaskID,
		RunID:        input.RunID,
		ActorID:      input.ActorID,
		Scope:        scope,
		IdeaSeeds:    seeds,
		Status:       "proposal",
		ReviewStatus: "pending",
		CreatedAt:    now.UTC(),
	}
	if err := store.SaveDreamConsolidationRun(ctx, run); err != nil {
		return domainkm.DreamConsolidationRun{}, err
	}
	return run, nil
}

func dreamIdeaSeeds(ctx context.Context, store DreamConsolidationStore, scope []string, limit int) ([]string, error) {
	var seeds []string
	for _, item := range scope {
		switch strings.ToLower(strings.TrimSpace(item)) {
		case "personal_archive":
			items, err := store.ListPersonalArchiveEntries(ctx, limit)
			if err != nil {
				return nil, err
			}
			for _, entry := range items {
				seeds = append(seeds, "personal_archive:"+entry.EntryID+" "+firstLine(entry.OriginalText))
			}
		case "creative_knowledge":
			items, err := store.ListCreativeKnowledgeItems(ctx, limit)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				seeds = append(seeds, "creative_knowledge:"+item.ItemID+" "+strings.TrimSpace(item.Title))
			}
		case "news_knowledge":
			items, err := store.ListNewsKnowledgeItems(ctx, limit)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				seeds = append(seeds, "news_knowledge:"+item.ItemID+" "+strings.TrimSpace(strings.Join([]string{item.Topic, item.Summary}, " ")))
			}
		case "temporal_memory", "temporal_marker":
			items, err := store.ListTemporalMemoryMarkers(ctx, limit)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				seeds = append(seeds, "temporal_memory:"+item.MarkerID+" "+strings.TrimSpace(item.Summary))
			}
		}
	}
	return compactStrings(seeds), nil
}

func compactScope(scope []string) []string {
	return compactStrings(scope)
}

func compactStrings(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if idx := strings.IndexAny(text, "\r\n"); idx >= 0 {
		text = strings.TrimSpace(text[:idx])
	}
	return text
}
