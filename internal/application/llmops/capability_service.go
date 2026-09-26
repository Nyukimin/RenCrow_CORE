// Package llmops aggregates llmfit capability observations across the configured
// LLM nodes for the Debug Viewer Ops screen. It caches per node with TTLs,
// isolates node failures, and never mutates llmfit (read-only observation).
package llmops

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	domainllmops "github.com/Nyukimin/RenCrow_CORE/internal/domain/llmops"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llmfit"
)

// Default TTLs follow the feature spec "更新" section (health 30s, hardware 5m,
// model fit 15m); config may override them.
const (
	defaultSystemTTL = 5 * time.Minute
	defaultModelsTTL = 15 * time.Minute
	defaultHealthTTL = 30 * time.Second
	defaultTopLimit  = 20
)

// NodeStatus is the observation status of a node as shown in the viewer.
type NodeStatus string

// Node statuses. "stale" means the last known value is served because the
// latest fetch or health check failed; "offline" means there is no value.
const (
	StatusOnline   NodeStatus = "online"
	StatusOffline  NodeStatus = "offline"
	StatusStale    NodeStatus = "stale"
	StatusDisabled NodeStatus = "disabled"
)

// Node binds a configured node id to its llmfit provider.
type Node struct {
	ID       string
	Provider llmfit.CapabilityProvider
}

// Options tunes the service. Zero values fall back to the defaults above.
type Options struct {
	// Disabled makes every node report StatusDisabled without touching providers.
	Disabled  bool
	SystemTTL time.Duration
	ModelsTTL time.Duration
	HealthTTL time.Duration
	TopLimit  int
	// Now is the clock used for cache freshness and collected_at stamps.
	Now func() time.Time
}

// ModelsQuery narrows a model fit listing (alias of the provider query so the
// viewer only depends on this package).
type ModelsQuery = llmfit.ModelFitQuery

// ValidateModelsQuery validates and normalizes a query. The error message is
// safe to return to the viewer.
func ValidateModelsQuery(q ModelsQuery) (ModelsQuery, error) {
	if err := q.Validate(); err != nil {
		return q, err
	}
	return q.Normalized(), nil
}

// NodeSnapshot is the hardware view of one node.
type NodeSnapshot struct {
	NodeID      string
	Status      NodeStatus
	Profile     *domainllmops.NodeHardwareProfile
	CollectedAt *time.Time
	Error       string
}

// ModelsSnapshot is the model fit listing of one node.
type ModelsSnapshot struct {
	NodeID      string
	Status      NodeStatus
	CollectedAt *time.Time
	Error       string
	Models      []domainllmops.ModelFitAssessment
}

// MatrixEntry is one node's assessment of a single model. Assessment is nil
// when the node did not report the model.
type MatrixEntry struct {
	NodeID      string
	Status      NodeStatus
	CollectedAt *time.Time
	Error       string
	Assessment  *domainllmops.ModelFitAssessment
}

// RefreshResult reports the outcome of a manual refresh per node.
type RefreshResult struct {
	Refreshed []string
	Failed    map[string]string
}

type node struct {
	id       string
	provider llmfit.CapabilityProvider
	// mu serializes fetches for one node so concurrent viewer requests do not
	// stampede llmfit; different nodes proceed in parallel.
	mu sync.Mutex
}

// CapabilityService is the read model behind the LLM Ops viewer API.
type CapabilityService struct {
	nodes     []*node
	byID      map[string]*node
	disabled  bool
	systemTTL time.Duration
	modelsTTL time.Duration
	healthTTL time.Duration
	topLimit  int
	now       func() time.Time

	systems *ttlCache[domainllmops.NodeHardwareProfile]
	models  *ttlCache[[]domainllmops.ModelFitAssessment]
	search  *ttlCache[[]domainllmops.ModelFitAssessment]
	health  *ttlCache[error]
}

// NewCapabilityService builds the service. Nodes with an empty id or nil
// provider are ignored; duplicate ids keep the first occurrence.
func NewCapabilityService(nodes []Node, opts Options) *CapabilityService {
	s := &CapabilityService{
		byID:      map[string]*node{},
		disabled:  opts.Disabled,
		systemTTL: opts.SystemTTL,
		modelsTTL: opts.ModelsTTL,
		healthTTL: opts.HealthTTL,
		topLimit:  opts.TopLimit,
		now:       opts.Now,
		systems:   newTTLCache[domainllmops.NodeHardwareProfile](),
		models:    newTTLCache[[]domainllmops.ModelFitAssessment](),
		search:    newTTLCache[[]domainllmops.ModelFitAssessment](),
		health:    newTTLCache[error](),
	}
	if s.systemTTL <= 0 {
		s.systemTTL = defaultSystemTTL
	}
	if s.modelsTTL <= 0 {
		s.modelsTTL = defaultModelsTTL
	}
	if s.healthTTL <= 0 {
		s.healthTTL = defaultHealthTTL
	}
	if s.topLimit <= 0 {
		s.topLimit = defaultTopLimit
	}
	if s.now == nil {
		s.now = time.Now
	}
	for _, n := range nodes {
		id := strings.TrimSpace(n.ID)
		if id == "" || n.Provider == nil {
			continue
		}
		if _, exists := s.byID[id]; exists {
			continue
		}
		entry := &node{id: id, provider: n.Provider}
		s.nodes = append(s.nodes, entry)
		s.byID[id] = entry
	}
	return s
}

// NodeIDs lists configured node ids in configuration order.
func (s *CapabilityService) NodeIDs() []string {
	ids := make([]string, 0, len(s.nodes))
	for _, n := range s.nodes {
		ids = append(ids, n.id)
	}
	return ids
}

// Nodes returns the hardware snapshot of every node. Nodes are fetched in
// parallel and one node's failure never affects another.
func (s *CapabilityService) Nodes(ctx context.Context) []NodeSnapshot {
	out := make([]NodeSnapshot, len(s.nodes))
	var wg sync.WaitGroup
	for i, n := range s.nodes {
		wg.Add(1)
		go func(i int, n *node) {
			defer wg.Done()
			out[i] = s.systemSnapshot(ctx, n, false)
		}(i, n)
	}
	wg.Wait()
	return out
}

// Node returns the hardware snapshot of one node; ok is false for unknown ids.
func (s *CapabilityService) Node(ctx context.Context, nodeID string) (NodeSnapshot, bool) {
	n, ok := s.byID[strings.TrimSpace(nodeID)]
	if !ok {
		return NodeSnapshot{}, false
	}
	return s.systemSnapshot(ctx, n, false), true
}

// NodeModels returns the model fit listing of one node for the query; ok is
// false for unknown ids. The query must already be validated.
func (s *CapabilityService) NodeModels(ctx context.Context, nodeID string, query ModelsQuery) (ModelsSnapshot, bool) {
	n, ok := s.byID[strings.TrimSpace(nodeID)]
	if !ok {
		return ModelsSnapshot{}, false
	}
	return s.modelsSnapshot(ctx, n, query, false), true
}

// ModelMatrix returns every node's assessment of modelID (exact name match,
// case-insensitive). Too-tight fits are included so the matrix can show why a
// model does not run on a node.
func (s *CapabilityService) ModelMatrix(ctx context.Context, modelID string) []MatrixEntry {
	modelID = strings.TrimSpace(modelID)
	out := make([]MatrixEntry, len(s.nodes))
	var wg sync.WaitGroup
	for i, n := range s.nodes {
		wg.Add(1)
		go func(i int, n *node) {
			defer wg.Done()
			out[i] = s.matrixEntry(ctx, n, modelID)
		}(i, n)
	}
	wg.Wait()
	return out
}

// Refresh forces a re-fetch of the hardware profile and the default model
// listing for the given nodes (all nodes when none are given). It only
// re-reads llmfit; it never changes llmfit settings, loads models, or runs
// benchmarks. On failure the previous cached value is kept (served as stale).
func (s *CapabilityService) Refresh(ctx context.Context, nodeIDs ...string) RefreshResult {
	result := RefreshResult{Refreshed: []string{}, Failed: map[string]string{}}
	targets := s.nodes
	if len(nodeIDs) > 0 {
		targets = make([]*node, 0, len(nodeIDs))
		for _, raw := range nodeIDs {
			id := strings.TrimSpace(raw)
			n, ok := s.byID[id]
			if !ok {
				result.Failed[id] = "unknown node"
				continue
			}
			targets = append(targets, n)
		}
	}
	if s.disabled {
		for _, n := range targets {
			result.Failed[n.id] = "llm ops is disabled"
		}
		return result
	}

	type outcome struct {
		id  string
		err string
	}
	outcomes := make([]outcome, len(targets))
	var wg sync.WaitGroup
	for i, n := range targets {
		wg.Add(1)
		go func(i int, n *node) {
			defer wg.Done()
			outcomes[i] = outcome{id: n.id, err: s.refreshNode(ctx, n)}
		}(i, n)
	}
	wg.Wait()
	for _, o := range outcomes {
		if o.err != "" {
			result.Failed[o.id] = o.err
			continue
		}
		result.Refreshed = append(result.Refreshed, o.id)
	}
	return result
}

func (s *CapabilityService) refreshNode(ctx context.Context, n *node) string {
	// The hardware profile is refreshed first; when it fails the node is treated
	// as unreachable and the model listing is left untouched (served as stale).
	if snap := s.systemSnapshot(ctx, n, true); snap.Status != StatusOnline {
		return snap.Error
	}
	defaultQuery := s.defaultQuery()
	if snap := s.modelsSnapshot(ctx, n, defaultQuery, true); snap.Status != StatusOnline {
		return snap.Error
	}
	// Other query variants and matrix lookups are dropped so the next request
	// re-reads llmfit instead of serving pre-refresh values.
	keep := modelsKey(n.id, defaultQuery)
	nodePrefix := "models:" + n.id + ":"
	s.models.deleteWhere(func(key string) bool { return strings.HasPrefix(key, nodePrefix) && key != keep })
	searchPrefix := "search:" + n.id + ":"
	s.search.deleteWhere(func(key string) bool { return strings.HasPrefix(key, searchPrefix) })
	return ""
}

func (s *CapabilityService) defaultQuery() ModelsQuery {
	return ModelsQuery{Limit: s.topLimit}
}

func (s *CapabilityService) systemSnapshot(ctx context.Context, n *node, force bool) NodeSnapshot {
	if s.disabled {
		return NodeSnapshot{NodeID: n.id, Status: StatusDisabled}
	}
	profile, status, collectedAt, errText := fetchCached(ctx, s, n, s.systems, "system:"+n.id, s.systemTTL, force,
		func(ctx context.Context, now time.Time) (domainllmops.NodeHardwareProfile, error) {
			fetched, err := n.provider.System(ctx)
			if err != nil {
				return domainllmops.NodeHardwareProfile{}, err
			}
			if fetched == nil {
				return domainllmops.NodeHardwareProfile{}, errors.New("llmfit returned no system profile")
			}
			p := *fetched
			p.NodeID = n.id
			p.CollectedAt = now
			if p.Source == "" {
				p.Source = domainllmops.SourceLLMFit
			}
			if p.GPUs == nil {
				p.GPUs = []domainllmops.GPUProfile{}
			}
			return p, nil
		})
	snap := NodeSnapshot{NodeID: n.id, Status: status, CollectedAt: collectedAt, Error: errText}
	if status == StatusOnline || status == StatusStale {
		copyProfile := profile
		copyProfile.GPUs = append([]domainllmops.GPUProfile{}, profile.GPUs...)
		snap.Profile = &copyProfile
	}
	return snap
}

func (s *CapabilityService) modelsSnapshot(ctx context.Context, n *node, query ModelsQuery, force bool) ModelsSnapshot {
	if s.disabled {
		return ModelsSnapshot{NodeID: n.id, Status: StatusDisabled, Models: []domainllmops.ModelFitAssessment{}}
	}
	query = query.Normalized()
	if query.Limit <= 0 {
		query.Limit = s.topLimit
	}
	models, status, collectedAt, errText := fetchCached(ctx, s, n, s.models, modelsKey(n.id, query), s.modelsTTL, force,
		func(ctx context.Context, now time.Time) ([]domainllmops.ModelFitAssessment, error) {
			fetched, err := n.provider.TopModels(ctx, query)
			if err != nil {
				return nil, err
			}
			return stampModels(n.id, fetched, now), nil
		})
	return ModelsSnapshot{NodeID: n.id, Status: status, CollectedAt: collectedAt, Error: errText, Models: copyModels(models)}
}

func (s *CapabilityService) matrixEntry(ctx context.Context, n *node, modelID string) MatrixEntry {
	if s.disabled {
		return MatrixEntry{NodeID: n.id, Status: StatusDisabled}
	}
	query := ModelsQuery{Keyword: modelID, IncludeTooTight: true, Limit: s.topLimit}
	models, status, collectedAt, errText := fetchCached(ctx, s, n, s.search, "search:"+n.id+":"+strings.ToLower(modelID), s.modelsTTL, false,
		func(ctx context.Context, now time.Time) ([]domainllmops.ModelFitAssessment, error) {
			fetched, err := n.provider.SearchModels(ctx, query)
			if err != nil {
				return nil, err
			}
			return stampModels(n.id, fetched, now), nil
		})
	entry := MatrixEntry{NodeID: n.id, Status: status, CollectedAt: collectedAt, Error: errText}
	for i := range models {
		if strings.EqualFold(models[i].ModelID, modelID) {
			assessment := models[i]
			entry.Assessment = &assessment
			break
		}
	}
	return entry
}

// checkHealth returns the cached health verdict for the node, re-checking the
// provider once the health TTL has elapsed. Must be called with n.mu held.
func (s *CapabilityService) checkHealth(ctx context.Context, n *node, now time.Time) error {
	key := "health:" + n.id
	if cached, storedAt, ok := s.health.get(key); ok && now.Sub(storedAt) < s.healthTTL {
		return cached
	}
	err := n.provider.Health(ctx)
	s.health.set(key, err, now)
	return err
}

// fetchCached serves value from cache while fresh (re-validating health once
// the health TTL elapses), otherwise fetches. A failed fetch keeps and returns
// the previous value as stale; with no previous value the node is offline.
func fetchCached[T any](
	ctx context.Context,
	s *CapabilityService,
	n *node,
	cache *ttlCache[T],
	key string,
	ttl time.Duration,
	force bool,
	fetch func(ctx context.Context, now time.Time) (T, error),
) (T, NodeStatus, *time.Time, string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := s.now()
	cached, storedAt, ok := cache.get(key)
	if ok && !force && now.Sub(storedAt) < ttl {
		at := storedAt
		if err := s.checkHealth(ctx, n, now); err != nil {
			return cached, StatusStale, &at, err.Error()
		}
		return cached, StatusOnline, &at, ""
	}
	fresh, err := fetch(ctx, now)
	if err != nil {
		// A failed fetch is the latest health evidence: other cached views of
		// this node are reported as stale until the health TTL elapses.
		s.health.set("health:"+n.id, err, now)
		if ok {
			at := storedAt
			return cached, StatusStale, &at, err.Error()
		}
		var zero T
		return zero, StatusOffline, nil, err.Error()
	}
	cache.set(key, fresh, now)
	s.health.set("health:"+n.id, nil, now)
	at := now
	return fresh, StatusOnline, &at, ""
}

func modelsKey(nodeID string, q ModelsQuery) string {
	// Spec chapter 8 key prefix plus the remaining query dimensions so listings
	// with different limits / fit floors / sort never alias each other.
	return fmt.Sprintf("models:%s:%s:%s:%s:%s:%s:%s",
		nodeID, q.UseCase, q.Runtime, strconv.Itoa(q.MaxContext), q.MinFit, strconv.Itoa(q.Limit), q.Sort)
}

func stampModels(nodeID string, models []domainllmops.ModelFitAssessment, now time.Time) []domainllmops.ModelFitAssessment {
	out := make([]domainllmops.ModelFitAssessment, 0, len(models))
	for _, m := range models {
		m.NodeID = nodeID
		m.CollectedAt = now
		if m.Capabilities == nil {
			m.Capabilities = []string{}
		}
		if m.Notes == nil {
			m.Notes = []string{}
		}
		out = append(out, m)
	}
	return out
}

func copyModels(models []domainllmops.ModelFitAssessment) []domainllmops.ModelFitAssessment {
	return append([]domainllmops.ModelFitAssessment{}, models...)
}
