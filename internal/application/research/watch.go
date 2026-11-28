package research

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type WatchStore interface {
	RegisterResearchWatch(context.Context, string, string, string, []string, time.Duration, time.Time) (*l1sqlite.L1ResearchWatch, error)
	GetResearchWatch(context.Context, string, string, string) (l1sqlite.L1ResearchWatch, bool, error)
	ListSourceRegistryEntries(context.Context, bool) ([]l1sqlite.L1SourceRegistryEntry, error)
}

type WatchAdapter struct {
	store  WatchStore
	policy SourcePolicy
}

func NewWatchAdapter(store WatchStore, policy SourcePolicy) *WatchAdapter {
	return &WatchAdapter{store: store, policy: policy}
}

var _ engine.WatchPort = (*WatchAdapter)(nil)

func (a *WatchAdapter) Register(ctx context.Context, env c.Envelope, request c.WatchRequest) (c.WatchReceipt, error) {
	if a == nil || a.store == nil || a.policy == nil || ctx == nil {
		return c.WatchReceipt{}, errors.New("unavailable: watch/source owner")
	}
	request.Objective = strings.TrimSpace(request.Objective)
	if request.InitialRun || utf8.RuneCountInString(request.Objective) < 1 || utf8.RuneCountInString(request.Objective) > 2000 || len(request.SourceRefs) < 1 || len(request.SourceRefs) > 20 {
		return c.WatchReceipt{}, errors.New("invalid_request: watch objective/sources/initial run")
	}
	cadence, err := time.ParseDuration(request.Cadence)
	if err != nil || cadence < time.Minute || cadence > 365*24*time.Hour {
		return c.WatchReceipt{}, errors.New("invalid_request: watch cadence must be a bounded interval")
	}
	for _, kind := range request.SourceKinds {
		if kind != "web" && kind != "rss" {
			return c.WatchReceipt{}, errors.New("unavailable: watch source kind")
		}
	}
	normalized, err := engine.Normalize(c.Request{SchemaVersion: c.RequestSchema, Mode: "collect", Objective: request.Objective, Outputs: request.Outputs})
	if err != nil {
		return c.WatchReceipt{}, err
	}
	request.Outputs = normalized.Outputs
	for _, save := range request.Outputs.Save {
		if save.Target != "knowledge_staging" && save.Target != "artifact" || save.DestinationRef != "" {
			return c.WatchReceipt{}, errors.New("unavailable: watch output owner")
		}
	}
	entries, err := a.store.ListSourceRegistryEntries(ctx, true)
	if err != nil {
		return c.WatchReceipt{}, err
	}
	byID := map[string]l1sqlite.L1SourceRegistryEntry{}
	for _, entry := range entries {
		byID[entry.SourceID] = entry
	}
	request.SourceRefs = append([]string(nil), request.SourceRefs...)
	sort.Strings(request.SourceRefs)
	registeredKinds := make([]string, 0, len(request.SourceRefs))
	for i, id := range request.SourceRefs {
		entry, ok := byID[id]
		if !ok || !entry.Enabled || entry.LicenseNote == "" || cadence < entry.FetchInterval || i > 0 && request.SourceRefs[i-1] == id {
			return c.WatchReceipt{}, errors.New("blocked_by_policy: source registration/cadence")
		}
		kind, _, supported := researchRefreshSourceKind(entry.Kind)
		if !supported {
			return c.WatchReceipt{}, errors.New("unavailable: watch source kind is not connected")
		}
		registeredKinds = append(registeredKinds, kind)
		retained, _ := a.policy(ctx, c.Candidate{URL: entry.URL, SourceKind: "web", ProviderID: "source_registry"})
		if !retained {
			return c.WatchReceipt{}, errors.New("blocked_by_policy: source retention")
		}
	}
	request.SourceKinds, err = normalizeWatchSourceKinds(request.SourceKinds, registeredKinds)
	if err != nil {
		return c.WatchReceipt{}, err
	}
	owner := env.OwnerID
	if owner == "" && env.DataScope == "public" {
		owner = env.Actor
	}
	if owner == "" {
		return c.WatchReceipt{}, errors.New("blocked_by_policy: watch owner")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return c.WatchReceipt{}, err
	}
	stored, err := a.store.RegisterResearchWatch(ctx, env.DataScope, owner, string(data), request.SourceRefs, cadence, time.Now().UTC().Add(cadence))
	if err != nil {
		return c.WatchReceipt{}, err
	}
	var expectedObject any
	if err := json.Unmarshal(data, &expectedObject); err != nil {
		return c.WatchReceipt{}, err
	}
	expectedCanonical, err := json.Marshal(expectedObject)
	if err != nil {
		return c.WatchReceipt{}, err
	}
	if stored == nil || stored.PayloadJSON != string(expectedCanonical) {
		return c.WatchReceipt{}, errors.New("unavailable: watch owner receipt")
	}
	readback, found, err := a.store.GetResearchWatch(ctx, env.DataScope, owner, stored.WatchID)
	if err != nil || !found || !readback.Enabled || readback.PayloadDigest != stored.PayloadDigest || readback.PayloadJSON != stored.PayloadJSON || readback.Scope != env.DataScope || readback.OwnerID != owner {
		return c.WatchReceipt{}, errors.New("unavailable: watch exact readback")
	}
	return c.WatchReceipt{WatchID: readback.WatchID, RegistryRef: "source_registry:" + readback.WatchID, State: "active", NextDueAt: readback.NextDueAt, Receipt: c.Receipt{Owner: "core.source_registry", OwnerRoute: "research.watch", AuditRef: "research_watch:" + readback.WatchID, CanonicalID: readback.WatchID, Digest: readback.PayloadDigest, State: "committed", Scope: env.DataScope, CommittedAt: readback.CreatedAt, VerifiedAt: time.Now().UTC()}}, nil
}

func normalizeWatchSourceKinds(requested, registered []string) ([]string, error) {
	normalize := func(kinds []string) []string {
		ordered := append([]string(nil), kinds...)
		sort.Strings(ordered)
		unique := ordered[:0]
		for _, kind := range ordered {
			if len(unique) == 0 || unique[len(unique)-1] != kind {
				unique = append(unique, kind)
			}
		}
		return unique
	}
	registered = normalize(registered)
	if len(requested) == 0 {
		return registered, nil
	}
	requested = normalize(requested)
	if len(requested) != len(registered) {
		return nil, errors.New("blocked_by_policy: watch source kinds do not match registered sources")
	}
	for i := range requested {
		if requested[i] != registered[i] {
			return nil, errors.New("blocked_by_policy: watch source kinds do not match registered sources")
		}
	}
	return requested, nil
}
