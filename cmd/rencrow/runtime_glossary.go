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
)

type glossaryRuntime struct {
	RecentContext func(context.Context, int) (string, error)
	RecentTopics  func(context.Context, int) ([]string, error)
	RecentHandler http.HandlerFunc
}

func buildGlossaryRuntime(cfg *config.Config) glossaryRuntime {
	var runtime glossaryRuntime
	if !cfg.Glossary.Enabled {
		return runtime
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
