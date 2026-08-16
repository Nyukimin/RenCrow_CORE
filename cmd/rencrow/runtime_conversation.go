package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	knowledgerelationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgerelation"
	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	webgatherapp "github.com/Nyukimin/RenCrow_CORE/internal/application/webgather"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainrelation "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
	conversationpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/archivesqlite"
	categoryrecallinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/categoryrecall"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	webgatherinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/webgather"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type conversationRuntime struct {
	Engine           conversation.ConversationEngine
	Manager          *conversationpersistence.RealConversationManager
	L1Store          *l1sqlite.L1SQLiteStore
	ArchiveStore     *archivesqlite.ArchiveSQLiteStore
	Closer           conversationRuntimeCloser
	ArchiveCloser    conversationRuntimeCloser // nil when Closer owns archive/L1 shutdown
	WebGatherFetcher tools.WebGatherFetcher
	ProfilePromotion *memorypromotionapp.Service
}

type conversationRuntimeCloser interface {
	Close() error
}

func buildConversationRuntime(
	cfg *config.Config,
	primaryProviders primaryLLMProviders,
	chatToolRunnerV2 *tools.ToolRunner,
	workerToolRunnerV2 *tools.ToolRunner,
) conversationRuntime {
	ownerID := conversationRuntimeUserID(cfg)
	var convEngine conversation.ConversationEngine
	var realMgr *conversationpersistence.RealConversationManager
	var l1Store *l1sqlite.L1SQLiteStore
	var archiveStore *archivesqlite.ArchiveSQLiteStore
	var profilePromotion *memorypromotionapp.Service
	mioPersona := conversation.DefaultMioPersona()
	if cfg.Prompts != nil {
		mioPersona = conversation.NewMioPersona(cfg.Prompts.MioPersona)
	}
	if cfg.Storage.Databases.ConversationL1 != "" {
		if err := os.MkdirAll(filepath.Dir(cfg.Storage.Databases.ConversationL1), 0755); err != nil {
			log.Fatalf("Failed to create L1 SQLite directory: %v", err)
		}
		var err error
		l1Store, err = l1sqlite.NewL1SQLiteStore(cfg.Storage.Databases.ConversationL1)
		if err != nil {
			log.Fatalf("Failed to initialize L1 SQLite store: %v", err)
		}
		if exportRoot := strings.TrimSpace(cfg.Storage.Memory.ColdExportDir); exportRoot != "" {
			if err := l1Store.SetParquetExportRoot(exportRoot); err != nil {
				log.Fatalf("Failed to configure Parquet export root: %v", err)
			}
		}
		if rawSourceRoot := strings.TrimSpace(cfg.Storage.Memory.RawSourceDir); rawSourceRoot != "" {
			startupReconcile, err := reconcileChatGPTImportStartup(context.Background(), l1Store, rawSourceRoot)
			if err != nil {
				log.Fatalf("Failed to reconcile ChatGPT import startup state: %v", err)
			}
			log.Printf("  ChatGPT import startup reconcile: removed_stages=%d blocked_imports=%d", startupReconcile.RemovedStages, startupReconcile.BlockedImports)
		}
		log.Printf("  L1 SQLite: %s", cfg.Storage.Databases.ConversationL1)
	}
	// Conversation Archive is a CORE-owned L2 boundary for user-memory
	// archive/receipt routes. It must be available with standard L1-only
	// startup; advanced Redis/vector conversation is not a prerequisite.
	if l1Store != nil {
		archivePath := strings.TrimSpace(cfg.Storage.Databases.ConversationArchive)
		if archivePath != "" {
			if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil {
				log.Fatalf("Failed to create conversation archive directory: %v", err)
			}
			var err error
			archiveStore, err = archivesqlite.NewArchiveSQLiteStore(archivePath)
			if err != nil {
				log.Fatalf("Failed to initialize conversation archive SQLite store: %v", err)
			}
			l1Store.WithArchiveStore(archiveStore)
			log.Printf("  Conversation Archive SQLite: %s", archivePath)
		}
	}
	categoryRecallRegistry := buildCategoryRecallRegistry(context.Background(), cfg, l1Store)
	if cfg.Conversation.Enabled {
		var err error
		vectorCollection := cfg.Conversation.VectorCollection
		if vectorCollection == "" {
			vectorCollection = "rencrow_memory"
		}
		vectorDimension := cfg.Conversation.VectorDimension
		if vectorDimension <= 0 {
			vectorDimension = 768
		}
		realMgr, err = conversationpersistence.NewRealConversationManagerWithVectorOptions(
			cfg.Conversation.RedisURL,
			cfg.Storage.Databases.ConversationArchive,
			cfg.Conversation.VectorDBURL,
			vectorCollection,
			uint64(vectorDimension),
		)
		if err != nil {
			log.Fatalf("Failed to initialize conversation manager: %v", err)
		}
		log.Printf("  VectorDB collection: %s (dimension=%d)", vectorCollection, vectorDimension)
		if l1Store != nil {
			realMgr.WithL1Store(l1Store)
			if archiveStore != nil {
				// RealConversationManager may attach its optional archive
				// connection; the CORE-owned route store is authoritative for
				// L1 archive promotions and request receipts.
				l1Store.WithArchiveStore(archiveStore)
			}
			if cfg.KnowledgeRelation.Enabled {
				scoring := domainrelation.DefaultScoringConfig()
				scoring.MinimumScore = cfg.KnowledgeRelation.MinimumScore
				relationBuilder := knowledgerelationapp.NewRelationBuildService(l1Store, knowledgerelationapp.NewMetadataExtractor(nil), scoring)
				if cfg.KnowledgeRelation.BuildOnImport {
					realMgr.WithKnowledgeRelationImportHook(func(ctx context.Context, item l1sqlite.L1KnowledgeItem) error {
						report, buildErr := relationBuilder.BuildForItem(ctx, item)
						if buildErr == nil {
							log.Printf("Knowledge Relation import build: item=%s entities=%d links=%d relations=%d status=%s", item.ID, report.EntityUpserts, report.ItemEntityUpserts, report.RelationUpserts, report.Status)
						}
						return buildErr
					})
					log.Printf("  Knowledge Relation import hook: enabled")
				}
			}
		}

		embedder, embedderLabel := buildConversationEmbedder(cfg)
		if embedder != nil {
			realMgr.WithEmbedder(embedder)
			log.Printf("  Embedder: %s", embedderLabel)
		}

		summaryProvider, summaryProviderLabel := buildConversationTextProvider(cfg, primaryProviders)
		if summaryProvider != nil {
			summarizer := conversationpersistence.NewLLMSummarizer(summaryProvider)
			realMgr.WithSummarizer(summarizer)
			if l1Store != nil {
				l1Store.WithDailyDigestSummarizer(conversationpersistence.NewLLMDailyDigestSummarizer(summaryProvider))
			}
			log.Printf("  Summarizer: %s", summaryProviderLabel)
		}

		var embedderForDetector conversation.EmbeddingProvider
		embedderForDetector = embedder
		detector := conversationpersistence.NewThreadBoundaryDetector(embedderForDetector)

		engine := conversationpersistence.NewRealConversationEngine(
			realMgr,
			mioPersona,
		).WithDetector(detector)
		if categoryRecallRegistry != nil {
			engine = engine.WithCategoryRecallRegistry(categoryRecallRegistry).WithCategoryRecallScope("public")
			log.Printf("  Category Recall Registry: enabled")
		}
		if l1Store != nil {
			engine = engine.WithUserMemoryStore(l1Store, ownerID)
			if cfg.KnowledgeRelation.Enabled {
				engine = engine.WithKnowledgeRelationRecall(cfg.KnowledgeRelation.MaxHops)
				log.Printf("  Knowledge Relation recall: enabled (max_hops=%d)", cfg.KnowledgeRelation.MaxHops)
			}
		}
		if err := realMgr.DrainConversationTurnOutbox(context.Background(), 100); err != nil {
			code := conversation.ConversationTurnErrorCodeOf(err)
			if code == "" {
				code = conversation.ConversationTurnErrorUnavailable
			}
			log.Printf("Conversation outbox startup drain failed code=%s; durable L1 receipt retained", code)
		}
		if cfg.Conversation.ProfilePromotionEnabledValue() && l1Store != nil && summaryProvider != nil {
			extractor := conversationpersistence.NewLLMProfileExtractor(summaryProvider).WithMinimumUserMessages(1)
			profilePromotion = memorypromotionapp.NewService(l1Store, extractor, memorypromotionapp.Options{
				UserID:        ownerID,
				BatchMessages: cfg.Conversation.ProfilePromotionBatchMessages,
				MaxAttempts:   cfg.Conversation.ProfilePromotionMaxAttempts,
				LeaseDuration: time.Duration(cfg.Conversation.ProfilePromotionTimeoutSeconds+30) * time.Second,
			})
			log.Printf("  Async ProfilePromotion: %s", summaryProviderLabel)
		}
		convEngine = engine

		log.Printf("ConversationEngine v5.1 enabled (RecallPack + Persona + async ProfilePromotion)")
		log.Printf("  Redis: %s", cfg.Conversation.RedisURL)
		log.Printf("  SQLite archive: %s", cfg.Storage.Databases.ConversationArchive)
		log.Printf("  VectorDB: %s", cfg.Conversation.VectorDBURL)
	} else {
		if l1Store != nil {
			l1Manager := conversationpersistence.NewL1ConversationManager(l1Store)
			engine := conversationpersistence.NewRealConversationEngine(
				l1Manager,
				mioPersona,
			).WithUserMemoryStore(l1Store, ownerID)
			if categoryRecallRegistry != nil {
				engine = engine.WithCategoryRecallRegistry(categoryRecallRegistry).WithCategoryRecallScope("public")
				log.Printf("  Category Recall Registry: enabled")
			}
			convEngine = engine
			log.Printf("ConversationEngine L1-only enabled (shared Mio/Shiro/Kuro/Midori context)")
		} else {
			convEngine = nil
			log.Printf("Conversation memory disabled: L1 SQLite is not configured")
		}
		log.Printf("Advanced Conversation LLM disabled (Redis/archive/vector recall unavailable)")
	}
	if realMgr != nil {
		webSearchCache := newConversationWebSearchCacheAdapter(realMgr)
		chatToolRunnerV2.WithWebSearchCache(webSearchCache)
		workerToolRunnerV2.WithWebSearchCache(webSearchCache)
		log.Printf("ToolRunner web_search cache enabled via Conversation L1")
	}
	var dailySourceFetcher tools.WebGatherFetcher
	if workerToolRunnerV2 != nil {
		webGatherUseCase := webgatherapp.NewUseCase(
			webgatherinfra.NewHTTPFetcher(),
			webgatherinfra.NewBasicExtractor(),
			nil,
		)
		if l1Store != nil {
			webGatherUseCase = webgatherapp.NewUseCase(
				webgatherinfra.NewHTTPFetcher(),
				webgatherinfra.NewBasicExtractor(),
				webgatherapp.NewL1StagingWriter(l1Store),
			).WithFetchCache(webgatherapp.NewL1FetchCache(l1Store))
		}
		if cfg.WebwrightFetch.Enabled {
			webGatherUseCase.WithFetchProvider("webwright", webgatherinfra.NewWebwrightFetcher(webwrightFetcherConfigFromRuntime(cfg.WebwrightFetch)))
		}
		dailySourceFetcher = webGatherUseCase
		webGatherProviders := map[string]modulewebgather.SearchProvider{}
		webGatherProviders["bing_rss"] = webgatherinfra.NewBingRSSSearchProvider()
		webGatherProviders["bing_news_rss"] = webgatherinfra.NewBingNewsRSSSearchProvider()
		webGatherProviders["rss_atom"] = webgatherinfra.NewFeedDiscoveryProvider()
		webGatherProviders["sitemap"] = webgatherinfra.NewFeedDiscoveryProvider()
		if searxngBaseURL := strings.TrimSpace(cfg.WebGather.SearXNGBaseURL); searxngBaseURL != "" {
			webGatherProviders["searxng"] = webgatherinfra.NewSearXNGProvider(searxngBaseURL)
		}
		if yacyBaseURL := strings.TrimSpace(cfg.WebGather.YaCyBaseURL); yacyBaseURL != "" {
			webGatherProviders["yacy"] = webgatherinfra.NewYaCyProvider(yacyBaseURL)
		}
		var webGatherSearchCache webgatherapp.SearchCache
		if l1Store != nil {
			webGatherSearchCache = webgatherapp.NewL1SearchCache(l1Store)
		}
		// Search tools do not require Conversation L1 when an explicit remote
		// provider is configured. L1 remains an optional cache/staging layer.
		searchProviderConfigured := len(webGatherProviders) > 0 || webGatherSearchCache != nil
		if searchProviderConfigured {
			webGatherSearchUseCase := webgatherapp.NewSearchUseCase(webGatherSearchCache, webGatherProviders)
			webGatherSearchAndFetchUseCase := webgatherapp.NewSearchAndFetchUseCase(webGatherSearchUseCase, webGatherUseCase)
			workerToolRunnerV2.WithWebGatherFetcher(webGatherUseCase)
			workerToolRunnerV2.WithWebGatherSearcher(webGatherSearchUseCase)
			workerToolRunnerV2.WithWebGatherSearchAndFetcher(webGatherSearchAndFetchUseCase)
			if l1Store == nil {
				log.Printf("ToolRunner web_gather.fetch/search/search_and_fetch enabled without Conversation L1 (external provider)")
			} else {
				log.Printf("ToolRunner web_gather.fetch/search/search_and_fetch enabled via Conversation L1")
			}
		} else {
			log.Printf("Daily source brief direct URL fetch enabled without L1 staging; web_gather search disabled because no provider is configured")
		}
	}
	return conversationRuntime{
		Engine:       convEngine,
		Manager:      realMgr,
		L1Store:      l1Store,
		ArchiveStore: archiveStore,
		Closer: func() conversationRuntimeCloser {
			if realMgr != nil {
				return realMgr
			}
			if l1Store != nil {
				return l1Store
			}
			return nil
		}(),
		ArchiveCloser:    conversationRuntimeArchiveCloser(realMgr, archiveStore),
		WebGatherFetcher: dailySourceFetcher,
		ProfilePromotion: profilePromotion,
	}
}

func conversationRuntimeUserID(cfg *config.Config) string {
	if cfg != nil {
		if userID := strings.TrimSpace(cfg.LocalAgentOps.UserID); userID != "" {
			return userID
		}
	}
	// The standard profile historically uses Ren's local owner when no
	// LocalAgentOps user is configured; keep that compatibility fallback while
	// allowing configured authenticated owner APIs and conversation recall to
	// share one identity.
	return "ren"
}

func conversationRuntimeArchiveCloser(realMgr *conversationpersistence.RealConversationManager, archiveStore *archivesqlite.ArchiveSQLiteStore) conversationRuntimeCloser {
	// RealConversationManager.Close closes its internal archive and attached L1
	// stores. Keep the independent route archive closer only for L1-only mode.
	if realMgr != nil {
		return nil
	}
	return archiveStore
}

// buildCategoryRecallRegistry wires only the category sources that CORE is
// allowed to read. L1 knowledge is the generic validated source; movie and
// hobby databases are optional public read-only catalogs. Other module-owned
// databases are intentionally not opened here. A missing optional database is
// retained as an unavailable source so a related turn records a partial trace
// instead of making runtime startup fatal.
func buildCategoryRecallRegistry(ctx context.Context, cfg *config.Config, l1Store *l1sqlite.L1SQLiteStore) *conversation.DeterministicCategoryRecallRegistry {
	if ctx == nil {
		ctx = context.Background()
	}
	registry := conversation.NewCategoryRecallRegistry()
	hints := map[string][]string{}
	sourceCount := 0
	if l1Store != nil {
		registry.Register(categoryrecallinfra.NewL1KnowledgeSource(l1Store))
		sourceCount++
	}
	if cfg != nil {
		if path := strings.TrimSpace(cfg.Storage.Databases.MovieCatalog); path != "" {
			source := categoryrecallinfra.NewMovieCatalogSource(path)
			registry.Register(source)
			sourceCount++
		}
		if path := strings.TrimSpace(cfg.Storage.Databases.HobbyGraph); path != "" {
			source := categoryrecallinfra.NewHobbyGraphSource(path)
			registry.Register(source)
			sourceCount++
			mergeCategoryRecallHints(hints, sourceStartupEntityHints(ctx, source, source.ID()))
		}
	}
	if sourceCount == 0 {
		return nil
	}
	registry.SetEntityHints(hints)
	return registry
}

type categoryRecallEntityHintSource interface {
	StartupEntityHints(context.Context) (map[string][]string, error)
}

func sourceStartupEntityHints(ctx context.Context, source categoryRecallEntityHintSource, sourceID string) map[string][]string {
	if source == nil {
		return nil
	}
	hints, err := source.StartupEntityHints(ctx)
	if err != nil {
		log.Printf("[CategoryRecall] WARN: startup entity hints unavailable source=%s: %v", sourceID, err)
		return nil
	}
	return hints
}

func mergeCategoryRecallHints(dst map[string][]string, src map[string][]string) {
	for category, values := range src {
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" || containsCategoryRecallHint(dst[category], value) {
				continue
			}
			dst[category] = append(dst[category], value)
		}
	}
}

func containsCategoryRecallHint(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
