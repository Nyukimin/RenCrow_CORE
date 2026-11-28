package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	core "github.com/Nyukimin/RenCrow_CORE/modules/core"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

type RefreshWatchStore interface {
	WatchStore
	ClaimResearchWatch(context.Context, string, string, string, time.Time) (l1sqlite.L1ResearchWatch, bool, error)
	CommitResearchWatchCursor(context.Context, string, string, string, string, string, bool, time.Time, time.Duration) error
	BindResearchWatchRefresh(context.Context, l1sqlite.L1ResearchWatchRefresh, time.Time) error
	GetResearchWatchRefresh(context.Context, string, string, string) (l1sqlite.L1ResearchWatchRefresh, bool, error)
	RevalidateResearchWatchRefresh(context.Context, l1sqlite.L1ResearchWatchRefresh, string, time.Time) (l1sqlite.L1ResearchWatchRefresh, bool, error)
	CommitResearchWatchRefresh(context.Context, l1sqlite.L1ResearchWatchRefresh, string, string, string, string, time.Time) error
	ScheduleResearchWatchRefreshRetry(context.Context, l1sqlite.L1ResearchWatchRefresh, string, string, time.Duration, time.Time) (time.Time, error)
}

type RefreshAdapter struct {
	store     RefreshWatchStore
	policy    SourcePolicy
	artifacts engine.Store
	owner     engine.OwnerPort
	now       func() time.Time
}

func NewRefreshAdapter(store RefreshWatchStore, policy SourcePolicy, artifacts engine.Store, owner engine.OwnerPort) *RefreshAdapter {
	return &RefreshAdapter{store: store, policy: policy, artifacts: artifacts, owner: owner, now: func() time.Time { return time.Now().UTC() }}
}

func (a *RefreshAdapter) WithClock(now func() time.Time) *RefreshAdapter {
	if a != nil && now != nil {
		a.now = now
	}
	return a
}

var _ engine.RefreshPort = (*RefreshAdapter)(nil)
var _ engine.RefreshResumePort = (*RefreshAdapter)(nil)

type researchWatchCursor struct {
	SchemaVersion string            `json:"schema_version"`
	ItemHashes    map[string]string `json:"item_hashes"`
}

type researchWatchSourcePolicy struct {
	SourceID      string        `json:"source_id"`
	URL           string        `json:"url"`
	Kind          string        `json:"kind"`
	TrustScore    float64       `json:"trust_score"`
	FetchInterval time.Duration `json:"fetch_interval_ns"`
	LicenseNote   string        `json:"license_note"`
	Namespace     string        `json:"namespace,omitempty"`
	Retention     bool          `json:"retention_allowed"`
	Primary       bool          `json:"primary"`
}

func (a *RefreshAdapter) Prepare(ctx context.Context, env c.Envelope, watchID string) (c.RefreshPlan, error) {
	if a == nil || a.store == nil || a.policy == nil || a.artifacts == nil || a.owner == nil || ctx == nil {
		return c.RefreshPlan{}, errors.New("unavailable: research refresh owner")
	}
	if err := validateRefreshEnvelope(env); err != nil {
		return c.RefreshPlan{}, err
	}
	originalWatchID := watchID
	watchID = strings.TrimSpace(watchID)
	if watchID != originalWatchID || !strings.HasPrefix(watchID, "rw_") || len(watchID) < 8 || len(watchID) > 256 {
		return c.RefreshPlan{}, errors.New("invalid_request: watch id")
	}
	now := a.currentTime()
	ownerID, err := refreshOwnerID(env)
	if err != nil {
		return c.RefreshPlan{}, err
	}
	watch, found, err := a.store.ClaimResearchWatch(ctx, env.DataScope, ownerID, watchID, now)
	if err != nil {
		return c.RefreshPlan{}, err
	}
	if !found {
		return c.RefreshPlan{}, errors.New("unavailable: watch is not due or already leased")
	}
	planBound := false
	defer func() {
		if !planBound {
			cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			if cleanupErr := a.store.CommitResearchWatchCursor(cleanupCtx, env.DataScope, ownerID, watchID, watch.LeaseToken, "", false, a.currentTime(), l1sqlite.L1ResearchWatchDefaultRetry); cleanupErr != nil {
				log.Print("research watch preflight lease release failed")
			}
		}
	}()
	request, sources, sourcePolicyRevision, err := a.resolveWatch(ctx, env, watch)
	if err != nil {
		return c.RefreshPlan{}, err
	}
	previousCursor, itemHashes, err := decodeResearchWatchCursor(watch.CursorJSON)
	if err != nil {
		return c.RefreshPlan{}, errors.New("unavailable: stored watch cursor is invalid")
	}
	plan := c.RefreshPlan{
		WatchID: watchID, PlanID: researchPlanID(watchID, watch.LeaseToken, env), LeaseToken: watch.LeaseToken,
		Execution: env, Scope: env.DataScope, OwnerID: env.OwnerID, PolicyRevision: env.PolicyRevision,
		SourcePolicyRevision: sourcePolicyRevision, Request: request, Sources: sources,
		PreviousCursor: previousCursor, PreviousItemHashes: itemHashes,
	}
	plan.PlanDigest = engine.RefreshPlanDigest(plan)
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return c.RefreshPlan{}, err
	}
	executionJSON, _ := json.Marshal(env)
	binding := l1sqlite.L1ResearchWatchRefresh{
		Scope: env.DataScope, OwnerID: ownerID, WatchID: watchID, PlanID: plan.PlanID, LeaseToken: plan.LeaseToken,
		PlanJSON: string(planJSON), PlanDigest: plan.PlanDigest, OriginalExecutionJSON: string(executionJSON),
		CurrentInvocationJSON: string(executionJSON), SourcePolicyRevision: sourcePolicyRevision,
		PreviousCursor: previousCursor,
	}
	if err := a.store.BindResearchWatchRefresh(ctx, binding, now); err != nil {
		return c.RefreshPlan{}, err
	}
	planBound = true
	return plan, nil
}

func (a *RefreshAdapter) Revalidate(ctx context.Context, env c.Envelope, prior c.RefreshPlan) (c.RefreshPlan, error) {
	if a == nil || a.store == nil || a.policy == nil || a.artifacts == nil || ctx == nil {
		return c.RefreshPlan{}, errors.New("unavailable: research refresh revalidation owner")
	}
	if err := validateRefreshEnvelope(env); err != nil || !sameRefreshPrincipal(prior.Execution, env) || env.RunID == prior.Execution.RunID || env.TraceID == prior.Execution.TraceID || env.RequestID == prior.Execution.RequestID {
		return c.RefreshPlan{}, errors.New("blocked_by_policy: refresh continuation identity")
	}
	if prior.PlanDigest == "" || engine.RefreshPlanDigest(prior) != prior.PlanDigest {
		return c.RefreshPlan{}, errors.New("unavailable: refresh plan digest mismatch")
	}
	ownerID, err := refreshOwnerID(prior.Execution)
	if err != nil {
		return c.RefreshPlan{}, err
	}
	stored, found, err := a.store.GetResearchWatchRefresh(ctx, prior.Scope, ownerID, prior.WatchID)
	if err != nil || !found || stored.PlanID != prior.PlanID || stored.PlanDigest != prior.PlanDigest || stored.PlanJSON != mustCanonicalJSON(prior) || stored.LeaseToken != prior.LeaseToken || stored.PreviousCursor != prior.PreviousCursor {
		return c.RefreshPlan{}, errors.New("unavailable: owner refresh plan journal mismatch")
	}
	watch, found, err := a.store.GetResearchWatch(ctx, prior.Scope, ownerID, prior.WatchID)
	if err != nil || !found || !watch.Enabled || watch.CursorJSON != prior.PreviousCursor {
		return c.RefreshPlan{}, errors.New("unavailable: watch registration or cursor changed")
	}
	request, sources, sourcePolicyRevision, err := a.resolveWatch(ctx, prior.Execution, watch)
	if err != nil || sourcePolicyRevision != prior.SourcePolicyRevision || !reflect.DeepEqual(request, prior.Request) || !reflect.DeepEqual(sources, prior.Sources) {
		return c.RefreshPlan{}, errors.New("blocked_by_policy: refresh source or request policy changed")
	}
	if err := a.verifyEnginePlanJournal(ctx, prior, env, nil); err != nil {
		return c.RefreshPlan{}, err
	}
	invocationJSON, err := json.Marshal(env)
	if err != nil {
		return c.RefreshPlan{}, err
	}
	updated, allowed, err := a.store.RevalidateResearchWatchRefresh(ctx, stored, string(invocationJSON), a.currentTime())
	if err != nil || !allowed {
		return c.RefreshPlan{}, errors.New("unavailable: watch lease cannot be explicitly revalidated")
	}
	if updated.PlanDigest != prior.PlanDigest || updated.PlanJSON != stored.PlanJSON || updated.LeaseToken != prior.LeaseToken {
		return c.RefreshPlan{}, errors.New("unavailable: owner changed the frozen refresh plan")
	}
	return prior, nil
}

func (a *RefreshAdapter) Finish(ctx context.Context, env c.Envelope, plan c.RefreshPlan, execution c.RefreshExecution) (c.RefreshReceipt, error) {
	if a == nil || a.store == nil || a.policy == nil || a.artifacts == nil || a.owner == nil || ctx == nil {
		return c.RefreshReceipt{}, errors.New("unavailable: research refresh finish owner")
	}
	if validateRefreshEnvelope(env) != nil || plan.PlanDigest == "" || engine.RefreshPlanDigest(plan) != plan.PlanDigest || !sameRefreshPrincipal(plan.Execution, env) {
		return c.RefreshReceipt{}, errors.New("blocked_by_policy: refresh finish identity")
	}
	ownerID, err := refreshOwnerID(plan.Execution)
	if err != nil {
		return c.RefreshReceipt{}, err
	}
	binding, found, err := a.store.GetResearchWatchRefresh(ctx, plan.Scope, ownerID, plan.WatchID)
	if err != nil || !found || binding.PlanDigest != plan.PlanDigest || binding.PlanJSON != mustCanonicalJSON(plan) || binding.LeaseToken != plan.LeaseToken {
		return c.RefreshReceipt{}, errors.New("unavailable: owner refresh plan journal mismatch")
	}
	if binding.CurrentInvocationJSON != mustCanonicalJSON(env) {
		return c.RefreshReceipt{}, errors.New("unavailable: owner invocation binding mismatch")
	}
	watch, watchFound, watchErr := a.store.GetResearchWatch(ctx, plan.Scope, ownerID, plan.WatchID)
	if watchErr != nil || !watchFound || !watch.Enabled {
		return c.RefreshReceipt{}, errors.New("unavailable: research watch is no longer enabled")
	}
	request, sources, sourcePolicyRevision, err := a.resolveWatch(ctx, plan.Execution, watch)
	if err != nil || sourcePolicyRevision != plan.SourcePolicyRevision || !reflect.DeepEqual(request, plan.Request) || !reflect.DeepEqual(sources, plan.Sources) {
		return c.RefreshReceipt{}, errors.New("blocked_by_policy: refresh source policy changed")
	}
	if binding.State == "committed" && watch.CursorJSON != binding.CommittedCursor || binding.State != "committed" && watch.CursorJSON != plan.PreviousCursor {
		return c.RefreshReceipt{}, errors.New("conflict: watch cursor no longer matches the journaled effect")
	}
	if err := a.verifyEnginePlanJournal(ctx, plan, env, &execution); err != nil {
		return c.RefreshReceipt{}, err
	}
	executionDigest, err := refreshExecutionDigest(execution)
	if err != nil {
		return c.RefreshReceipt{}, err
	}
	if binding.State == "committed" {
		return a.reconcileCommittedRefresh(plan, env, execution, executionDigest, binding)
	}
	if binding.State == "retry_scheduled" {
		if binding.AttemptDigest != executionDigest {
			return c.RefreshReceipt{}, errors.New("conflict: retry execution differs from durable attempt")
		}
		return c.RefreshReceipt{WatchID: plan.WatchID, PlanID: plan.PlanID, State: "retry_scheduled", Cursor: plan.PreviousCursor, AttemptRef: binding.AttemptRef, RetryAt: binding.RetryAt}, nil
	}
	watch, found, err = a.store.GetResearchWatch(ctx, plan.Scope, ownerID, plan.WatchID)
	if err != nil || !found || !watch.Enabled || watch.LeaseToken != plan.LeaseToken || !watch.LeaseUntil.After(a.currentTime()) || watch.CursorJSON != plan.PreviousCursor {
		return c.RefreshReceipt{}, errors.New("unavailable: refresh lease expired or cursor moved")
	}
	if !execution.Complete {
		attemptRef := digestResearch([]byte(plan.PlanDigest + "\x00" + executionDigest))
		retryAt, retryErr := a.store.ScheduleResearchWatchRefreshRetry(ctx, binding, executionDigest, attemptRef, 0, a.currentTime())
		if retryErr != nil {
			return c.RefreshReceipt{}, retryErr
		}
		return c.RefreshReceipt{WatchID: plan.WatchID, PlanID: plan.PlanID, State: "retry_scheduled", Cursor: plan.PreviousCursor, AttemptRef: attemptRef, RetryAt: retryAt}, nil
	}
	if len(execution.Missing) != 0 || len(execution.Failures) != 0 {
		return c.RefreshReceipt{}, errors.New("invalid complete refresh contains missing items")
	}
	cursorJSON, err := encodeResearchWatchCursor(plan, execution)
	if err != nil {
		return c.RefreshReceipt{}, err
	}
	checkpointDigest := engine.RefreshCheckpointDigest(plan, execution, cursorJSON)
	if checkpointDigest == "" {
		return c.RefreshReceipt{}, errors.New("unavailable: refresh checkpoint digest is incomplete")
	}
	now := a.currentTime()
	receipt := c.RefreshReceipt{
		WatchID: plan.WatchID, PlanID: plan.PlanID, State: "committed", Cursor: cursorJSON,
		CursorCommitted: true, CursorAdvanced: cursorJSON != plan.PreviousCursor,
		Receipt: c.Receipt{Owner: "core.source_registry", OwnerRoute: "research.watch.refresh",
			AuditRef:    "research_watch_refresh:" + plan.WatchID + ":" + plan.PlanID,
			CanonicalID: plan.WatchID, Digest: checkpointDigest, State: "committed", Scope: env.DataScope,
			CommittedAt: now, VerifiedAt: now},
	}
	receiptData, err := json.Marshal(receipt)
	if err != nil {
		return c.RefreshReceipt{}, err
	}
	receiptJSON, err := canonicalResearchJSON(string(receiptData))
	if err != nil {
		return c.RefreshReceipt{}, err
	}
	if err := a.store.CommitResearchWatchRefresh(ctx, binding, cursorJSON, checkpointDigest, executionDigest, receiptJSON, now); err != nil {
		return c.RefreshReceipt{}, err
	}
	committed, found, err := a.store.GetResearchWatchRefresh(ctx, plan.Scope, ownerID, plan.WatchID)
	if err != nil || !found || committed.State != "committed" || committed.CheckpointDigest != checkpointDigest || committed.ExecutionDigest != executionDigest || committed.CommittedCursor != cursorJSON || committed.ReceiptJSON != receiptJSON {
		return c.RefreshReceipt{}, errors.New("unavailable: refresh checkpoint exact readback failed")
	}
	committedWatch, found, err := a.store.GetResearchWatch(ctx, plan.Scope, ownerID, plan.WatchID)
	if err != nil || !found || committedWatch.CursorJSON != cursorJSON || committedWatch.LeaseToken != "" || committedWatch.LastSuccessAt.IsZero() {
		return c.RefreshReceipt{}, errors.New("unavailable: committed watch cursor exact readback failed")
	}
	return receipt, nil
}

func (a *RefreshAdapter) resolveWatch(ctx context.Context, env c.Envelope, watch l1sqlite.L1ResearchWatch) (c.Request, []c.RefreshSource, string, error) {
	var input c.WatchRequest
	if err := strictResearchJSON(watch.PayloadJSON, &input); err != nil {
		return c.Request{}, nil, "", errors.New("unavailable: watch payload is not a supported contract")
	}
	identityDigest, digestErr := researchWatchPayloadDigest(watch.PayloadJSON, watch.SourceRefs, watch.Cadence)
	if input.InitialRun || utf8.RuneCountInString(strings.TrimSpace(input.Objective)) < 1 || utf8.RuneCountInString(strings.TrimSpace(input.Objective)) > 2000 || digestErr != nil || identityDigest != watch.PayloadDigest || watch.WatchID == "" {
		return c.Request{}, nil, "", errors.New("unavailable: watch request is invalid")
	}
	refs := append([]string(nil), input.SourceRefs...)
	sort.Strings(refs)
	if !reflect.DeepEqual(refs, watch.SourceRefs) || len(refs) == 0 || len(refs) > 20 {
		return c.Request{}, nil, "", errors.New("unavailable: watch source references changed")
	}
	entries, err := a.store.ListSourceRegistryEntries(ctx, false)
	if err != nil {
		return c.Request{}, nil, "", err
	}
	byID := make(map[string]l1sqlite.L1SourceRegistryEntry, len(entries))
	for _, entry := range entries {
		byID[entry.SourceID] = entry
	}
	sourceKindSet := map[string]bool{}
	sources := make([]c.RefreshSource, 0, len(refs))
	policyEntries := make([]researchWatchSourcePolicy, 0, len(refs))
	for _, ref := range refs {
		entry, ok := byID[ref]
		if !ok || !entry.Enabled || strings.TrimSpace(entry.LicenseNote) == "" || entry.FetchInterval <= 0 || parsePrivateSourceNamespace(entry.Meta) {
			return c.Request{}, nil, "", errors.New("blocked_by_policy: watch source is disabled, private, or unlicensed")
		}
		canonicalURL, err := canonicalHTTPURL(entry.URL)
		if err != nil || canonicalURL != entry.URL {
			return c.Request{}, nil, "", errors.New("blocked_by_policy: registered source URL is invalid")
		}
		kind, provider, supported := researchRefreshSourceKind(entry.Kind)
		if !supported || input.Cadence == "" {
			return c.Request{}, nil, "", errors.New("unavailable: registered source type is not connected")
		}
		cadence, parseErr := time.ParseDuration(input.Cadence)
		if parseErr != nil || cadence < entry.FetchInterval {
			return c.Request{}, nil, "", errors.New("blocked_by_policy: watch cadence is faster than source policy")
		}
		retention, primary := a.policy(ctx, c.Candidate{URL: canonicalURL, SourceKind: kind, ProviderID: provider})
		if !retention {
			return c.Request{}, nil, "", errors.New("blocked_by_policy: source retention policy changed")
		}
		namespace, _ := entry.Meta["namespace"].(string)
		policyEntries = append(policyEntries, researchWatchSourcePolicy{
			SourceID: entry.SourceID, URL: canonicalURL, Kind: entry.Kind, TrustScore: entry.TrustScore,
			FetchInterval: entry.FetchInterval, LicenseNote: strings.TrimSpace(entry.LicenseNote), Namespace: namespace,
			Retention: retention, Primary: primary,
		})
		sources = append(sources, c.RefreshSource{SourceRef: ref, SeedURL: canonicalURL, SourceKind: kind, ProviderID: provider})
		sourceKindSet[kind] = true
	}
	kinds := make([]string, 0, len(sourceKindSet))
	for kind := range sourceKindSet {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	if len(input.SourceKinds) > 0 {
		registeredKinds := append([]string(nil), input.SourceKinds...)
		sort.Strings(registeredKinds)
		if !reflect.DeepEqual(registeredKinds, kinds) {
			return c.Request{}, nil, "", errors.New("blocked_by_policy: watch source kinds changed")
		}
	}
	outputs := input.Outputs
	for _, target := range outputs.Save {
		if (target.Target != "knowledge_staging" && target.Target != "artifact") || target.DestinationRef != "" {
			return c.Request{}, nil, "", fmt.Errorf("unavailable: watch output target %s is not connected", target.Target)
		}
	}
	request := c.Request{SchemaVersion: c.RequestSchema, Mode: "refresh", WatchID: watch.WatchID,
		Objective: strings.TrimSpace(input.Objective), Query: strings.TrimSpace(input.Objective), SourceKinds: kinds,
		Outputs: outputs, Limits: c.Limits{AnalysisCalls: 0}, CostProfile: "zero_incremental_cost"}
	request, err = engine.NormalizeRefreshPlan(request)
	if err != nil {
		return c.Request{}, nil, "", err
	}
	sort.Slice(policyEntries, func(i, j int) bool { return policyEntries[i].SourceID < policyEntries[j].SourceID })
	policyJSON, err := json.Marshal(policyEntries)
	if err != nil {
		return c.Request{}, nil, "", err
	}
	return request, sources, digestResearch(policyJSON), nil
}

func (a *RefreshAdapter) verifyEnginePlanJournal(ctx context.Context, plan c.RefreshPlan, current c.Envelope, execution *c.RefreshExecution) error {
	record, err := a.artifacts.Load(ctx, plan.Execution.RunID)
	if err != nil || record.Envelope != plan.Execution || record.Request.Mode != "refresh" || record.Request.WatchID != plan.WatchID || record.RefreshPlan == nil || record.RefreshPlan.PlanDigest != plan.PlanDigest || mustCanonicalJSON(*record.RefreshPlan) != mustCanonicalJSON(plan) {
		return errors.New("unavailable: Engine refresh plan journal is not exact")
	}
	if execution == nil {
		if record.Invocation == nil || *record.Invocation != current {
			return errors.New("unavailable: Engine continuation journal is not exact")
		}
		return nil
	}
	if record.Invocation != nil && *record.Invocation != current {
		return errors.New("unavailable: Engine refresh invocation CAS is not exact")
	}
	if record.Result.RunID != plan.Execution.RunID || record.Result.ExecutionRunID != current.RunID || record.Result.Counts.AnalysisCalls != 0 {
		return errors.New("unavailable: Engine refresh identity or analysis CAS is not exact")
	}
	if record.RefreshExecution == nil || !sameResearchJSONValue(*record.RefreshExecution, *execution) {
		return errors.New("unavailable: Engine refresh execution CAS is not exact")
	}
	if record.Collection == nil || !sameResearchJSONValue(*record.Collection, execution.Collection) {
		return errors.New("unavailable: Engine refresh collection CAS is not exact")
	}
	if execution.Collection.Envelope != current {
		return errors.New("unavailable: Engine refresh collection identity mismatch")
	}
	collectionJSON, err := json.Marshal(execution.Collection)
	if err != nil {
		return err
	}
	foundCollectionArtifact := false
	for _, ref := range record.Result.Artifacts {
		if ref.Kind != "collection" {
			continue
		}
		stored, getErr := a.artifacts.Get(ctx, ref)
		if getErr == nil && bytes.Equal(stored, collectionJSON) {
			foundCollectionArtifact = true
			break
		}
	}
	if !foundCollectionArtifact {
		return errors.New("unavailable: immutable refresh collection artifact is not verified")
	}
	for _, doc := range execution.Collection.Documents {
		if doc.ArtifactRef == "" || digestResearch([]byte(doc.Text)) != doc.ExtractedHash {
			return errors.New("unavailable: refresh document artifact does not match the collection")
		}
		body, bodyErr := a.artifacts.Get(ctx, c.ArtifactRef{Kind: "document", Hash: doc.ExtractedHash, Ref: doc.ArtifactRef})
		if bodyErr != nil || !bytes.Equal(body, []byte(doc.Text)) {
			return errors.New("unavailable: refresh document body artifact readback mismatch")
		}
	}
	if execution.Complete && (len(execution.Missing) != 0 || len(execution.Failures) != 0) {
		return errors.New("invalid complete refresh has missing evidence")
	}
	for _, receipt := range execution.Receipts {
		if !receiptPresent(record.Result.Receipts, receipt) {
			return errors.New("unavailable: refresh owner receipt is absent from Engine result CAS")
		}
		verified, verifyErr := a.owner.Verify(ctx, current, receipt)
		if verifyErr != nil || !sameVerifiedResearchReceipt(verified, receipt) {
			return errors.New("unavailable: refresh owner receipt failed exact readback")
		}
	}
	if execution.Complete {
		if err := verifyRequiredRefreshReceipts(plan.Request, *execution); err != nil {
			return err
		}
	}
	return nil
}

func sameResearchJSONValue(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func verifyRequiredRefreshReceipts(request c.Request, execution c.RefreshExecution) error {
	for _, target := range request.Outputs.Save {
		if target.Target == "knowledge_staging" {
			for _, change := range execution.ItemChanges {
				found := false
				for _, receipt := range execution.Receipts {
					if receipt.OwnerRoute == target.Target && receipt.Digest == change.CurrentHash && receipt.State == "committed" {
						found = true
						break
					}
				}
				if !found {
					return errors.New("unavailable: changed refresh item lacks its required staging receipt")
				}
			}
		} else if target.Target != "artifact" {
			return errors.New("unavailable: refresh output target is not connected")
		}
	}
	return nil
}

func (a *RefreshAdapter) reconcileCommittedRefresh(plan c.RefreshPlan, env c.Envelope, execution c.RefreshExecution, executionDigest string, stored l1sqlite.L1ResearchWatchRefresh) (c.RefreshReceipt, error) {
	if stored.ExecutionDigest != executionDigest || stored.CommittedCursor == "" || stored.CheckpointDigest == "" || stored.ReceiptJSON == "" {
		return c.RefreshReceipt{}, errors.New("conflict: committed watch effect differs from retry")
	}
	var receipt c.RefreshReceipt
	if err := strictResearchJSON(stored.ReceiptJSON, &receipt); err != nil {
		return c.RefreshReceipt{}, errors.New("unavailable: stored watch receipt is invalid")
	}
	if receipt.Cursor != stored.CommittedCursor || receipt.Receipt.Digest != stored.CheckpointDigest || receipt.Receipt.Digest != engine.RefreshCheckpointDigest(plan, execution, receipt.Cursor) || receipt.Receipt.Scope != env.DataScope || receipt.PlanID != plan.PlanID || receipt.WatchID != plan.WatchID {
		return c.RefreshReceipt{}, errors.New("conflict: stored watch checkpoint receipt does not match exact effect")
	}
	return receipt, nil
}

func validateRefreshEnvelope(env c.Envelope) error {
	if core.TaskID(env.TaskID).Validate() != nil || core.RunID(env.RunID).Validate() != nil || core.TraceID(env.TraceID).Validate() != nil || core.RequestID(env.RequestID).Validate() != nil || strings.TrimSpace(env.Actor) == "" || env.Actor != strings.TrimSpace(env.Actor) || strings.TrimSpace(env.PolicyRevision) == "" || env.PolicyRevision != strings.TrimSpace(env.PolicyRevision) {
		return errors.New("blocked_by_policy: trusted refresh envelope is invalid")
	}
	if env.DataScope != "public" && env.DataScope != "user" || env.DataScope == "user" && (strings.TrimSpace(env.OwnerID) == "" || env.OwnerID != strings.TrimSpace(env.OwnerID)) || env.DataScope == "public" && env.OwnerID != "" {
		return errors.New("blocked_by_policy: refresh scope is not public or exact user")
	}
	return nil
}

func refreshOwnerID(env c.Envelope) (string, error) {
	if err := validateRefreshEnvelope(env); err != nil {
		return "", err
	}
	if env.DataScope == "user" {
		return env.OwnerID, nil
	}
	return env.Actor, nil
}

func sameRefreshPrincipal(original, current c.Envelope) bool {
	return original.TaskID == current.TaskID && original.Actor == current.Actor && original.OwnerID == current.OwnerID && original.DataScope == current.DataScope && original.PolicyRevision == current.PolicyRevision
}

func decodeResearchWatchCursor(raw string) (string, map[string]string, error) {
	if raw == "" {
		raw = `{}`
	}
	var cursor researchWatchCursor
	if err := strictResearchJSON(raw, &cursor); err != nil {
		return "", nil, err
	}
	if cursor.SchemaVersion != "" && cursor.SchemaVersion != "rencrow.research.watch_cursor.v1" || cursor.ItemHashes == nil && raw != `{}` {
		return "", nil, errors.New("unsupported watch cursor")
	}
	if cursor.ItemHashes == nil {
		cursor.ItemHashes = map[string]string{}
	}
	for item, hash := range cursor.ItemHashes {
		if strings.TrimSpace(item) == "" || item != strings.TrimSpace(item) || !isResearchDigest(hash) {
			return "", nil, errors.New("invalid watch cursor item")
		}
	}
	return raw, cursor.ItemHashes, nil
}

func encodeResearchWatchCursor(plan c.RefreshPlan, execution c.RefreshExecution) (string, error) {
	_, hashes, err := decodeResearchWatchCursor(plan.PreviousCursor)
	if err != nil {
		return "", err
	}
	for _, change := range execution.ItemChanges {
		if change.ItemRef == "" || !isResearchDigest(change.CurrentHash) || change.State != "new" && change.State != "changed" && change.State != "unchanged" {
			return "", errors.New("invalid refresh item change")
		}
		if previous := hashes[change.ItemRef]; previous != change.PreviousHash {
			return "", errors.New("refresh item change does not match previous cursor")
		}
		if change.State == "new" && change.PreviousHash != "" || change.State != "new" && change.PreviousHash == "" || change.State == "unchanged" && change.PreviousHash != change.CurrentHash || change.State == "changed" && change.PreviousHash == change.CurrentHash {
			return "", errors.New("refresh item change state is inconsistent")
		}
		hashes[change.ItemRef] = change.CurrentHash
	}
	data, err := json.Marshal(researchWatchCursor{SchemaVersion: "rencrow.research.watch_cursor.v1", ItemHashes: hashes})
	if err != nil {
		return "", err
	}
	return canonicalResearchJSON(string(data))
}

func strictResearchJSON(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func researchWatchPayloadDigest(payload string, refs []string, cadence time.Duration) (string, error) {
	canonical, err := canonicalResearchJSON(payload)
	if err != nil {
		return "", err
	}
	ordered := append([]string(nil), refs...)
	sort.Strings(ordered)
	identity, err := json.Marshal(struct {
		Payload    json.RawMessage `json:"payload"`
		SourceRefs []string        `json:"source_refs"`
		CadenceNS  int64           `json:"cadence_ns"`
	}{json.RawMessage(canonical), ordered, int64(cadence)})
	if err != nil {
		return "", err
	}
	return digestResearch(identity), nil
}

func canonicalResearchJSON(raw string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return "", errors.New("trailing JSON")
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func researchRefreshSourceKind(kind string) (string, string, bool) {
	switch kind {
	case l1sqlite.L1SourceKindWebGather:
		return "web", "direct", true
	case l1sqlite.L1SourceKindRSS, l1sqlite.L1SourceKindAtom:
		return "rss", "rss_atom", true
	default:
		return "", "", false
	}
}

func parsePrivateSourceNamespace(meta map[string]interface{}) bool {
	if meta == nil {
		return false
	}
	namespace, _ := meta["namespace"].(string)
	namespace = strings.TrimSpace(namespace)
	return namespace == l1sqlite.NamespaceKindUser || strings.HasPrefix(namespace, l1sqlite.NamespaceKindUser+":")
}

func researchPlanID(watchID, leaseToken string, env c.Envelope) string {
	data, _ := json.Marshal(struct {
		WatchID string     `json:"watch_id"`
		Lease   string     `json:"lease"`
		Env     c.Envelope `json:"execution"`
	}{watchID, leaseToken, env})
	return "rwp_" + digestResearch(data)[:32]
}

func refreshExecutionDigest(execution c.RefreshExecution) (string, error) {
	data, err := json.Marshal(execution)
	if err != nil {
		return "", err
	}
	return digestResearch(data), nil
}

func digestResearch(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func isResearchDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func mustCanonicalJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func receiptPresent(values []c.Receipt, wanted c.Receipt) bool {
	for _, receipt := range values {
		if sameVerifiedResearchReceipt(receipt, wanted) {
			return true
		}
	}
	return false
}

func sameVerifiedResearchReceipt(actual, expected c.Receipt) bool {
	return actual.Owner == expected.Owner && actual.OwnerRoute == expected.OwnerRoute && actual.AuditRef == expected.AuditRef && actual.CanonicalID == expected.CanonicalID && actual.Digest == expected.Digest && actual.State == expected.State && actual.Scope == expected.Scope && !actual.CommittedAt.IsZero() && !actual.VerifiedAt.IsZero()
}

func (a *RefreshAdapter) currentTime() time.Time {
	if a.now == nil {
		return time.Now().UTC()
	}
	return a.now().UTC()
}
