package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/researchembedding"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/datacapability"
	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	coreresearch "github.com/Nyukimin/RenCrow_CORE/internal/application/research"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/localbackend"
)

func buildResearchEngine(cfg *config.Config, searcher coreresearch.Searcher, fetcher coreresearch.Fetcher, staging coreresearch.L1StagingStore) (*engine.Engine, *engine.FileStore, error) {
	if !cfg.Research.Enabled {
		return nil, nil, nil
	}
	if !filepath.IsAbs(cfg.Research.ArtifactRoot) {
		return nil, nil, fmt.Errorf("research.artifact_root must be an explicit absolute Tools-owned root")
	}
	store, err := engine.NewFileStore(cfg.Research.ArtifactRoot)
	if err != nil {
		return nil, nil, err
	}
	sourcePolicy := configuredResearchSourcePolicy(cfg.Research)
	adapter := coreresearch.NewAdapters(searcher, fetcher, staging).WithSourcePolicy(sourcePolicy)
	routes, caps, err := configureResearchSources(cfg.Research, adapter, fetcher != nil, sourcePolicy)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	caps = append(caps, c.Capability{ProviderID: "google_browser_manual", SourceKinds: []string{"web"}, Search: true, RequiresInput: true, Enabled: true, Ready: true, FreeAllowed: true})
	policyJSON, err := json.Marshal(cfg.Research)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	policyHash := sha256.Sum256(policyJSON)
	ports := engine.Ports{Store: store, Discovery: routes, Acquisition: routes, Owner: adapter, Capabilities: caps}
	if documentStore, ok := staging.(coreresearch.ResearchDocumentStore); ok {
		ports.LocalDocuments = coreresearch.NewLocalDocumentRetriever(documentStore, sourcePolicy)
	}
	if watchStore, ok := staging.(coreresearch.WatchStore); ok {
		ports.Watch = coreresearch.NewWatchAdapter(watchStore, sourcePolicy)
	}
	if refreshStore, ok := staging.(coreresearch.RefreshWatchStore); ok {
		ports.Refresh = coreresearch.NewRefreshAdapter(refreshStore, sourcePolicy, store, adapter)
	}
	if cfg.Research.AnalysisAlias != "" {
		if !cfg.LLMGateway.Enabled {
			_ = store.Close()
			return nil, nil, fmt.Errorf("Research analysis requires the existing LLM Gateway")
		}
		client, err := researchGatewayHTTPClient(cfg, 45*time.Second)
		if err != nil {
			_ = store.Close()
			return nil, nil, err
		}
		analysis, err := coreresearch.NewHTTPAnalysisPort(cfg.LLMGateway.BaseURL, cfg.Research.AnalysisAlias, client)
		if err != nil {
			_ = store.Close()
			return nil, nil, err
		}
		ports.Analysis = analysis
	}
	return &engine.Engine{PolicyRevision: tools.ResearchPolicyRevision + ":" + hex.EncodeToString(policyHash[:]), Ports: ports}, store, nil
}

func buildResearchSemanticEmbeddingPort(cfg *config.Config) (coreresearch.SemanticEmbeddingPort, error) {
	if cfg == nil || !cfg.Research.SemanticIndex.Enabled {
		return nil, nil
	}
	if !cfg.Research.Enabled || !cfg.LLMGateway.Enabled || strings.TrimSpace(cfg.LLMGateway.BaseURL) == "" {
		return nil, fmt.Errorf("Research semantic index requires the configured Research owner and LLM Gateway")
	}
	client, err := researchGatewayHTTPClient(cfg, time.Duration(cfg.LLMGateway.TimeoutSec)*time.Second)
	if err != nil {
		return nil, err
	}
	embeddings, err := researchembedding.NewHTTPPort(cfg.LLMGateway.BaseURL, cfg.Research.SemanticIndex.ServiceRef, client)
	if err != nil {
		return nil, err
	}
	return embeddings, nil
}

func buildResearchDocumentIndexBuilder(
	cfg *config.Config,
	documents coreresearch.ResearchDocumentStore,
	embeddings coreresearch.SemanticEmbeddingPort,
	sourcePolicy coreresearch.SourcePolicy,
) (*coreresearch.ResearchDocumentIndexBuilder, error) {
	if cfg == nil || !cfg.Research.SemanticIndex.Enabled {
		return nil, nil
	}
	if !cfg.Research.Enabled || !cfg.LLMGateway.Enabled || strings.TrimSpace(cfg.LLMGateway.BaseURL) == "" {
		return nil, fmt.Errorf("Research semantic index requires the configured Research owner and LLM Gateway")
	}
	if documents == nil {
		return nil, fmt.Errorf("Research semantic index requires the existing L1 document owner")
	}
	vectorReader, ok := documents.(coreresearch.ResearchDocumentVectorReader)
	if !ok {
		return nil, fmt.Errorf("Research semantic vector reader is unavailable")
	}
	vectorWriter, ok := documents.(coreresearch.ResearchDocumentVectorWriter)
	if !ok {
		return nil, fmt.Errorf("Research semantic vector writer is unavailable")
	}
	if embeddings == nil {
		return nil, fmt.Errorf("Research semantic embedding port is unavailable")
	}
	if sourcePolicy == nil {
		return nil, fmt.Errorf("Research semantic source policy is unavailable")
	}
	return coreresearch.NewResearchDocumentIndexBuilder(documents, coreresearch.ResearchSemanticIndexPorts{
		VectorReader: vectorReader,
		VectorWriter: vectorWriter,
		Embeddings:   embeddings,
		SourcePolicy: sourcePolicy,
	}), nil
}

func researchGatewayHTTPClient(cfg *config.Config, timeout time.Duration) (*http.Client, error) {
	if cfg == nil || !cfg.LLMGateway.Enabled || timeout <= 0 {
		return nil, fmt.Errorf("Research Gateway client configuration is unavailable")
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("Research Gateway transport unavailable")
	}
	localTransport := transport.Clone()
	localTransport.Proxy = nil
	token := ""
	if envName := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv); envName != "" {
		token = strings.TrimSpace(os.Getenv(envName))
	}
	return &http.Client{Timeout: timeout, Transport: researchGatewayTransport{base: localTransport, token: token}}, nil
}

type researchGatewayTransport struct {
	base  http.RoundTripper
	token string
}

func (t researchGatewayTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header = r.Header.Clone()
	if t.token != "" {
		copy.Header.Set("Authorization", "Bearer "+t.token)
	}
	return t.base.RoundTrip(copy)
}

func connectResearchRecall(
	e *engine.Engine,
	runtime toolRuntime,
	documents coreresearch.ResearchDocumentStore,
	cfg *config.Config,
	embeddings coreresearch.SemanticEmbeddingPort,
	sourcePolicy coreresearch.SourcePolicy,
	structuredOwners ...*researchStructuredIndexOwner,
) error {
	if e == nil {
		return nil
	}
	if cfg == nil {
		return fmt.Errorf("Research recall configuration is unavailable")
	}
	searcher, _ := runtime.KnowledgeMemoryToolStore.(appkm.IndexedSearcher)
	knowledge := coreresearch.NewRecallAdapter(searcher, func(_ context.Context, env c.Envelope) bool {
		if runtime.DataCapabilityCatalog == nil {
			return false
		}
		value, err := runtime.DataCapabilityCatalog.Execute("describe", "knowledge_memory")
		if err != nil {
			return false
		}
		entry, ok := value.(datacapability.Entry)
		if !ok || entry.Status != datacapability.KnowledgeMemoryStatusAvailable {
			return false
		}
		op := datacapability.KnowledgeMemoryOperationPublicSearch
		if env.DataScope == "user" {
			op = datacapability.KnowledgeMemoryOperationUserPrivateSearch
		}
		for _, allowed := range entry.SafeOperations {
			if allowed == op {
				return true
			}
		}
		return false
	})
	repos := []coreresearch.RegisteredRepository{}
	for _, repo := range cfg.Research.Repositories {
		repos = append(repos, coreresearch.RegisteredRepository{Ref: repo.Ref, Path: repo.Path, AllowedRevisions: append([]string(nil), repo.AllowedRevisions...), Scope: repo.Scope, OwnerID: repo.OwnerID})
	}
	structured := coreresearch.ResearchStructuredRecallPorts{}
	if cfg.Research.StructuredIndex.PageIndex.Enabled || cfg.Research.StructuredIndex.CodeGraph.Enabled {
		if len(structuredOwners) == 0 || structuredOwners[0] == nil {
			return fmt.Errorf("Research structured recall requires its authenticated owner and Task receipt")
		}
		structured = coreresearch.ResearchStructuredRecallPorts{
			Owner: structuredOwners[0], PageIndexEnabled: cfg.Research.StructuredIndex.PageIndex.Enabled,
			CodeGraphEnabled: cfg.Research.StructuredIndex.CodeGraph.Enabled,
		}
	}
	if !cfg.Research.SemanticIndex.Enabled {
		if structured.Owner != nil {
			e.Ports.Recall = coreresearch.NewLocalRecallWithStructured(knowledge, documents, repos, structured)
		} else {
			e.Ports.Recall = coreresearch.NewLocalRecall(knowledge, documents, repos)
		}
		return nil
	}
	vectorReader, ok := documents.(coreresearch.ResearchDocumentVectorReader)
	if !ok || embeddings == nil || sourcePolicy == nil {
		return fmt.Errorf("Research semantic recall requires the configured embedding port, vector reader, and source policy")
	}
	semantic := coreresearch.ResearchSemanticRecallPorts{
		VectorReader: vectorReader,
		Embeddings:   embeddings,
		SourcePolicy: sourcePolicy,
	}
	if structured.Owner != nil {
		e.Ports.Recall = coreresearch.NewLocalRecallWithSemanticAndStructured(knowledge, documents, repos, semantic, structured)
	} else {
		e.Ports.Recall = coreresearch.NewLocalRecallWithSemantic(knowledge, documents, repos, semantic)
	}
	return nil
}

func buildResearchStructuredIndexOwner(cfg *config.Config, e *engine.Engine, tasks *taskmanager.Manager) (*researchStructuredIndexOwner, error) {
	if cfg == nil || !cfg.Research.StructuredIndex.PageIndex.Enabled && !cfg.Research.StructuredIndex.CodeGraph.Enabled {
		return nil, nil
	}
	if e == nil || e.Ports.Store == nil || tasks == nil || !cfg.LocalAgentOps.Enabled || cfg.LocalAgentOps.UserID == "" {
		return nil, fmt.Errorf("Research structured index requires the existing authenticated local owner, artifact Store, and Task owner")
	}
	owner := &researchStructuredIndexOwner{config: cfg, ownerID: cfg.LocalAgentOps.UserID, store: e.Ports.Store, tasks: tasks}
	page := cfg.Research.StructuredIndex.PageIndex
	if page.Enabled {
		port, err := localbackend.NewPageIndexCLI(page.PythonExecutable, page.RuntimeRoot, page.RuntimeManifestSHA256, page.TempRoot, time.Duration(page.TimeoutSeconds)*time.Second)
		if err != nil {
			return nil, fmt.Errorf("initialize trusted local PageIndex backend: %w", err)
		}
		owner.pageIndex = port
	}
	graph := cfg.Research.StructuredIndex.CodeGraph
	if graph.Enabled {
		port, err := localbackend.NewCodebaseMemoryCLI(graph.Executable, graph.BinarySHA256, graph.RuntimeVersion, graph.SourceCommit,
			graph.CacheRoot, graph.RuntimeRoot, graph.TempRoot, time.Duration(graph.TimeoutSeconds)*time.Second)
		if err != nil {
			return nil, fmt.Errorf("initialize trusted local CodeGraph backend: %w", err)
		}
		owner.codeGraph = port
	}
	return owner, nil
}
