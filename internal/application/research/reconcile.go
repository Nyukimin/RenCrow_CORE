package research

import (
	"context"
	"errors"
	wg "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

var _ engine.ReconcilePort = (*Adapters)(nil)

// Reconcile uses the same owner-issued content/event identity as Commit. Only
// this idempotent owner may declare that an absent commit is safe to retry.
func (a *Adapters) Reconcile(ctx context.Context, req engine.CommitRequest) (engine.ReconcileResult, error) {
	if a == nil || a.staging == nil || req.Target.Target != KnowledgeStagingTarget || req.Target.DestinationRef != "" || req.Document == nil {
		return engine.ReconcileResult{}, errors.New("research staging reconciliation unavailable")
	}
	doc := req.Document
	if !doc.Complete || !doc.RetentionAllowed || doc.ContentState != "fetched" || digestBytes([]byte(doc.Text)) != req.Digest || doc.ExtractedHash != req.Digest {
		return engine.ReconcileResult{}, errors.New("invalid reconciliation binding")
	}
	expectedIdempotencyKey, keyErr := c.CommitIdempotencyKey(firstNonEmpty(req.ResearchRunID, req.Envelope.RunID), req.Target, req.Digest, doc)
	if keyErr != nil || req.IdempotencyKey != expectedIdempotencyKey {
		return engine.ReconcileResult{}, errors.New("invalid reconciliation idempotency key")
	}
	if doc.LocalOrigin != nil {
		return a.reconcileLocalMaterial(ctx, req, *doc)
	}
	if !validResearchDocumentIdentity(*doc) {
		return engine.ReconcileResult{}, errors.New("unsupported research document source identity or content scope")
	}
	ns, err := stagingNamespace(req.Envelope)
	if err != nil {
		return engine.ReconcileResult{}, err
	}
	url, err := canonicalHTTPURL(firstNonEmpty(doc.FinalURL, doc.OriginalURL))
	if err != nil {
		return engine.ReconcileResult{}, err
	}
	originalURL, err := canonicalHTTPURL(firstNonEmpty(doc.OriginalURL, doc.FinalURL))
	if err != nil {
		return engine.ReconcileResult{}, err
	}
	source := wg.SourceIDFromURL(url)
	event := researchEventID(source, url, req.Digest, *doc)
	item, found, err := a.staging.FindStagingItemByNamespaceEventID(ctx, ns, event)
	if err != nil {
		return engine.ReconcileResult{}, err
	}
	if !found {
		return engine.ReconcileResult{RetrySafe: true}, nil
	}
	if item.SourceURL != url || item.SourceID != source || item.RawHash != req.Digest || item.RawText != doc.Text || !researchStagingItemMatches(item, ns, event, req.Envelope.DataScope, originalURL, url, *doc, req.Digest) {
		return engine.ReconcileResult{}, errors.New("reconciliation content conflict")
	}
	receipt := c.Receipt{Owner: "core.l1_staging", OwnerRoute: KnowledgeStagingTarget, AuditRef: event, CanonicalID: item.ID, Digest: req.Digest, State: "committed", Scope: req.Envelope.DataScope, CommittedAt: item.CreatedAt}
	receipt, err = a.Verify(ctx, req.Envelope, receipt)
	return engine.ReconcileResult{Receipt: receipt}, err
}
