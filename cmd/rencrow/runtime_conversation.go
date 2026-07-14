package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	knowledgerelationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgerelation"
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
	Engine  conversation.ConversationEngine
	Manager *conversationpersistence.RealConversationManager
	L1Store *l1sqlite.L1SQLiteStore
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
			cfg.Conversation.DuckDBPath,
			cfg.Conversation.VectorDBURL,
			vectorCollection,
			uint64(vectorDimension),
		)
		if err != nil {
			log.Fatalf("Failed to initialize conversation manager: %v", err)
		}
		log.Printf("  VectorDB collection: %s (dimension=%d)", vectorCollection, vectorDimension)
		if cfg.Conversation.L1SQLitePath != "" {
			if err := os.MkdirAll(filepath.Dir(cfg.Conversation.L1SQLitePath), 0755); err != nil {
				log.Fatalf("Failed to create L1 SQLite directory: %v", err)
			}
			l1Store, err = l1sqlite.NewL1SQLiteStore(cfg.Conversation.L1SQLitePath)
			if err != nil {
				log.Fatalf("Failed to initialize L1 SQLite store: %v", err)
			}
			realMgr.WithL1Store(l1Store)
			log.Printf("  L1 SQLite: %s", cfg.Conversation.L1SQLitePath)
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

		var profileExtractor conversation.ProfileExtractor
		if summaryProvider != nil {
			profileExtractor = conversationpersistence.NewLLMProfileExtractor(summaryProvider)
			log.Printf("  ProfileExtractor: %s", summaryProviderLabel)
		}

		engine := conversationpersistence.NewRealConversationEngine(
			realMgr,
			conversation.NewMioPersona(cfg.Prompts.MioPersona),
		).WithDetector(detector)
		if l1Store != nil {
			engine = engine.WithRecallTraceStore(l1Store)
			if cfg.KnowledgeRelation.Enabled {
				engine = engine.WithKnowledgeRelationRecall(cfg.KnowledgeRelation.MaxHops)
				log.Printf("  Knowledge Relation recall: enabled (max_hops=%d)", cfg.KnowledgeRelation.MaxHops)
			}
		}
		if profileExtractor != nil {
			engine = engine.WithProfileExtractor(profileExtractor)
		}
		convEngine = engine

		log.Printf("ConversationEngine v5.1 enabled (RecallPack + Persona + ProfileExtractor)")
		log.Printf("  Redis: %s", cfg.Conversation.RedisURL)
		log.Printf("  DuckDB: %s", cfg.Conversation.DuckDBPath)
		log.Printf("  VectorDB: %s", cfg.Conversation.VectorDBURL)
	} else {
		convEngine = nil
		log.Printf("Conversation LLM disabled (v3/v4 mode)")
	}
	if realMgr != nil {
		webSearchCache := newConversationWebSearchCacheAdapter(realMgr)
		chatToolRunnerV2.WithWebSearchCache(webSearchCache)
		workerToolRunnerV2.WithWebSearchCache(webSearchCache)
		log.Printf("ToolRunner web_search cache enabled via Conversation L1")
	}
	if l1Store != nil {
		webGatherUseCase := webgatherapp.NewUseCase(
			webgatherinfra.NewHTTPFetcher(),
			webgatherinfra.NewBasicExtractor(),
			webgatherapp.NewL1StagingWriter(l1Store),
		).WithFetchCache(webgatherapp.NewL1FetchCache(l1Store))
		if cfg.WebwrightFetch.Enabled {
			webGatherUseCase.WithFetchProvider("webwright", webgatherinfra.NewWebwrightFetcher(webwrightFetcherConfigFromRuntime(cfg.WebwrightFetch)))
		}
		webGatherProviders := map[string]modulewebgather.SearchProvider{}
		webGatherProviders["rss_atom"] = webgatherinfra.NewFeedDiscoveryProvider()
		webGatherProviders["sitemap"] = webgatherinfra.NewFeedDiscoveryProvider()
		if searxngBaseURL := strings.TrimSpace(cfg.WebGather.SearXNGBaseURL); searxngBaseURL != "" {
			webGatherProviders["searxng"] = webgatherinfra.NewSearXNGProvider(searxngBaseURL)
		}
		if yacyBaseURL := strings.TrimSpace(cfg.WebGather.YaCyBaseURL); yacyBaseURL != "" {
			webGatherProviders["yacy"] = webgatherinfra.NewYaCyProvider(yacyBaseURL)
		}
		webGatherSearchUseCase := webgatherapp.NewSearchUseCase(webgatherapp.NewL1SearchCache(l1Store), webGatherProviders)
		webGatherSearchAndFetchUseCase := webgatherapp.NewSearchAndFetchUseCase(webGatherSearchUseCase, webGatherUseCase)
		workerToolRunnerV2.WithWebGatherFetcher(webGatherUseCase)
		workerToolRunnerV2.WithWebGatherSearcher(webGatherSearchUseCase)
		workerToolRunnerV2.WithWebGatherSearchAndFetcher(webGatherSearchAndFetchUseCase)
		log.Printf("ToolRunner web_gather.fetch/search/search_and_fetch enabled via Conversation L1")
	}
	return conversationRuntime{
		Engine:  convEngine,
		Manager: realMgr,
		L1Store: l1Store,
	}
}
