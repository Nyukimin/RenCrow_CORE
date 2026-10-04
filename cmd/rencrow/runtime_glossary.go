package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	glossary "github.com/Nyukimin/RenCrow_CORE/internal/glossary"
	glossaryservice "github.com/Nyukimin/RenCrow_CORE/internal/glossary/application/service"
	glossaryrepository "github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/repository"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/infrastructure/feed"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/interface/mio_adapter"
)

type glossaryRuntime struct {
	RecentContext  func(context.Context, int) (string, error)
	RecentTopics   func(context.Context, int) ([]string, error)
	RecentHandler  http.HandlerFunc
	IndexedLookup  runtimeGlossaryLookupExecutor
	CandidateStore runtimeGlossaryCandidateStore
}

type runtimeGlossaryRemoteOwner interface {
	glossaryrepository.GlossaryRepository
	runtimeGlossaryCandidateStore
	runtimeGlossaryLookupExecutor
}

func buildGlossaryRuntime(cfg *config.Config, selectedOwner ...runtimeGlossaryRemoteOwner) glossaryRuntime {
	var runtime glossaryRuntime
	if !cfg.Glossary.Enabled {
		return runtime
	}
	if len(selectedOwner) > 0 && selectedOwner[0] != nil {
		return buildRemoteGlossaryRuntime(cfg, selectedOwner[0])
	}
	dbPath := cfg.Glossary.DBPath
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		log.Printf("WARN: glossary directory create failed: %v", err)
		return runtime
	}
	glossaryModule, err := glossary.NewGlossaryModule(dbPath)
	if err != nil {
		log.Printf("WARN: glossary disabled: %v", err)
		return runtime
	}
	runtime.CandidateStore = glossaryModule.Repository
	if indexedLookup, lookupErr := prepareRuntimeGlossaryLookup(context.Background(), dbPath); lookupErr != nil {
		log.Printf("WARN: glossary indexed lookup unavailable: %v", lookupErr)
	} else {
		runtime.IndexedLookup = indexedLookup
	}
	syncGlossary := func() {
		count, err := glossaryModule.SyncFeeds(context.Background(), cfg.Glossary.FeedURLs)
		if err != nil {
			log.Printf("WARN: glossary sync failed: %v", err)
			return
		}
		log.Printf("Glossary sync complete: %d items", count)
	}
	syncGlossary()
	if cfg.Glossary.RefreshIntervalHr > 0 {
		go func(interval time.Duration) {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for range ticker.C {
				syncGlossary()
			}
		}(time.Duration(cfg.Glossary.RefreshIntervalHr) * time.Hour)
	}
	runtime.RecentContext = glossaryModule.MioAdapter.GetRecentContext
	runtime.RecentTopics = glossaryModule.MioAdapter.GetRecentTopics
	runtime.RecentHandler = viewer.HandleGlossaryRecent(glossaryModule.Service)
	log.Printf("Glossary enabled: db=%s feeds=%d", dbPath, len(cfg.Glossary.FeedURLs))
	return runtime
}

func buildRemoteGlossaryRuntime(cfg *config.Config, owner runtimeGlossaryRemoteOwner) glossaryRuntime {
	var runtime glossaryRuntime
	if cfg == nil || !cfg.Glossary.Enabled || owner == nil {
		return runtime
	}
	service := glossaryservice.NewGlossaryService(owner)
	mioAdapter := mio_adapter.NewMioGlossaryAdapter(service)
	runtime.CandidateStore = owner
	runtime.IndexedLookup = owner
	runtime.RecentContext = mioAdapter.GetRecentContext
	runtime.RecentTopics = mioAdapter.GetRecentTopics
	runtime.RecentHandler = viewer.HandleGlossaryRecent(service)
	syncGlossary := func() {
		count, err := syncRuntimeGlossaryFeeds(context.Background(), cfg.Glossary.FeedURLs, service)
		if err != nil {
			log.Printf("WARN: glossary sync failed: %v", err)
			return
		}
		log.Printf("Glossary sync complete: %d items", count)
	}
	syncGlossary()
	if cfg.Glossary.RefreshIntervalHr > 0 {
		go func(interval time.Duration) {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for range ticker.C {
				syncGlossary()
			}
		}(time.Duration(cfg.Glossary.RefreshIntervalHr) * time.Hour)
	}
	log.Printf("Glossary enabled: remote owner feeds=%d", len(cfg.Glossary.FeedURLs))
	return runtime
}

func syncRuntimeGlossaryFeeds(ctx context.Context, feedURLs []string, service *glossaryservice.GlossaryService) (int, error) {
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
		if _, err := service.AddGlossaryItem(ctx, item.Term, item.Explanation, item.Source, item.Category); err != nil {
			log.Printf("WARN: glossary save failed term=%q err=%v", item.Term, err)
			continue
		}
		saved++
	}
	return saved, nil
}
