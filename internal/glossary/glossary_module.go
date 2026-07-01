package glossary

import (
	"context"
	"log"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/infrastructure/feed"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/application/service"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/infrastructure/persistence"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/interface/mio_adapter"
)

type GlossaryModule struct {
	Repository *persistence.SQLiteGlossaryRepository
	Service    *service.GlossaryService
	MioAdapter *mio_adapter.MioGlossaryAdapter
}

func NewGlossaryModule(dbPath string) (*GlossaryModule, error) {
	repo, err := persistence.NewSQLiteGlossaryRepository(dbPath)
	if err != nil {
		return nil, err
	}
	
	service := service.NewGlossaryService(repo)
	mioAdapter := mio_adapter.NewMioGlossaryAdapter(service)
	
	return &GlossaryModule{
		Repository: repo,
		Service:    service,
		MioAdapter: mioAdapter,
	}, nil
}

func (m *GlossaryModule) Close() error {
	if m.Repository != nil {
		return m.Repository.Close()
	}
	return nil
}

func (m *GlossaryModule) SyncFeeds(ctx context.Context, feedURLs []string) (int, error) {
	if len(feedURLs) == 0 {
		return 0, nil
	}
	parser := feed.NewRSSParser(feedURLs)
	items, err := parser.FetchAndParse(ctx)
	if err != nil {
		return 0, err
	}
	saved := 0
	for _, item := range items {
		if _, err := m.Service.AddGlossaryItem(ctx, item.Term, item.Explanation, item.Source, item.Category); err != nil {
			log.Printf("WARN: glossary save failed term=%q err=%v", item.Term, err)
			continue
		}
		saved++
	}
	return saved, nil
}
