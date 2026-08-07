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
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	webgatherinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/webgather"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type conversationRuntime struct {
	Engine           conversation.ConversationEngine
	Manager          *conversationpersistence.RealConversationManager
	L1Store          *l1sqlite.L1SQLiteStore
	WebGatherFetcher tools.WebGatherFetcher
	ProfilePromotion *memorypromotionapp.Service
}

func buildConversationRuntime(
	cfg *config.Config,
	primaryProviders primaryLLMProviders,
	chatToolRunnerV2 *tools.ToolRunner,
	workerToolRunnerV2 *tools.ToolRunner,
) conversationRuntime {
	var convEngine conversation.ConversationEngine
	var realMgr *conversationpersistence.RealConversationManager
	var l1Store *l1sqlite.L1SQLiteStore
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
		log.Printf("  L1 SQLite: %s", cfg.Storage.Databases.ConversationL1)
	}
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
		if l1Store != nil {
			engine = engine.WithRecallTraceStore(l1Store)
			engine = engine.WithUserMemoryStore(l1Store, "ren")
			if cfg.KnowledgeRelation.Enabled {
				engine = engine.WithKnowledgeRelationRecall(cfg.KnowledgeRelation.MaxHops)
				log.Printf("  Knowledge Relation recall: enabled (max_hops=%d)", cfg.KnowledgeRelation.MaxHops)
			}
		}
		if cfg.Conversation.ProfilePromotionEnabledValue() && l1Store != nil && summaryProvider != nil {
			extractor := conversationpersistence.NewLLMProfileExtractor(summaryProvider).WithMinimumUserMessages(1)
			profilePromotion = memorypromotionapp.NewService(l1Store, extractor, memorypromotionapp.Options{
				UserID:        "ren",
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
			convEngine = conversationpersistence.NewRealConversationEngine(
				l1Manager,
				mioPersona,
			).WithRecallTraceStore(l1Store).WithUserMemoryStore(l1Store, "ren")
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
		Engine:           convEngine,
		Manager:          realMgr,
		L1Store:          l1Store,
		WebGatherFetcher: dailySourceFetcher,
		ProfilePromotion: profilePromotion,
	}
}
