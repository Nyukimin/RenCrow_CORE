package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

const (
	researchLocalMaterialMetaKind = "structured_local"
	researchLocalMaterialSourceID = "research.local-material:"
	researchLocalMaterialEventID  = "research-local-material:"
	researchLocalMaterialLicense  = "retention explicitly authorized by research request"
)

func (a *Adapters) commitLocalMaterial(ctx context.Context, req engine.CommitRequest, doc c.Document, contentHash string) (c.Receipt, error) {
	if a == nil || a.staging == nil || a.localMaterialOwner == nil || ctx == nil {
		return c.Receipt{}, errors.New("trusted local material staging owner is unavailable")
	}
	if req.Target.Target != KnowledgeStagingTarget || req.Target.DestinationRef != "" {
		return c.Receipt{}, errors.New("local research material target is unavailable")
	}
	identity, err := c.LocalCommitIdentity(doc)
	if err != nil || identity == "" {
		return c.Receipt{}, errors.New("local research material identity is invalid")
	}
	if err := a.localMaterialOwner.VerifyLocalMaterial(ctx, req.Envelope, doc); err != nil {
		return c.Receipt{}, fmt.Errorf("local research material is no longer authorized: %w", err)
	}
	namespace, err := stagingNamespace(req.Envelope)
	if err != nil {
		return c.Receipt{}, err
	}
	sourceID := researchLocalMaterialSourceID + identity
	eventID := researchLocalMaterialEventID + identity
	existing, found, err := a.staging.FindStagingItemByNamespaceEventID(ctx, namespace, eventID)
	if err != nil {
		return c.Receipt{}, err
	}
	if found {
		if !researchLocalMaterialStagingItemMatches(existing, namespace, eventID, sourceID, req.Envelope.DataScope, doc, identity, contentHash) {
			return c.Receipt{}, errors.New("local research material staging identity conflict")
		}
		return a.Verify(ctx, req.Envelope, researchLocalMaterialReceipt(existing, req.Envelope, identity, contentHash))
	}
	originJSON, err := json.Marshal(doc.LocalOrigin)
	if err != nil {
		return c.Receipt{}, errors.New("local research material provenance could not be encoded")
	}
	var storedOrigin any
	if err := json.Unmarshal(originJSON, &storedOrigin); err != nil {
		return c.Receipt{}, errors.New("local research material provenance could not be prepared for staging")
	}
	fetchedAt := a.currentTime()
	item, err := a.staging.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind: l1sqlite.L1StagingKindSearchResult, Namespace: namespace, EventID: eventID,
		SourceID: sourceID, FetchedAt: fetchedAt, RawText: doc.Text, RawHash: contentHash,
		SummaryDraft: modulewebgather.TextPreview(doc.Text, 240), Keywords: []string{"research", "local_material"},
		LicenseNote: researchLocalMaterialLicense, ValidationStatus: l1sqlite.L1StagingStatusPending,
		Meta: map[string]interface{}{
			"research_material_kind":    researchLocalMaterialMetaKind,
			"research_scope":            req.Envelope.DataScope,
			"research_namespace":        namespace,
			"research_digest":           contentHash,
			"research_local_identity":   identity,
			"source_kind":               doc.SourceKind,
			"provider_id":               doc.ProviderID,
			"source_ref":                doc.SourceRef,
			"revision":                  doc.Revision,
			"content_scope":             doc.ContentScope,
			"original_hash":             doc.OriginalHash,
			"extracted_hash":            contentHash,
			"content_kind":              doc.ContentKind,
			"content_complete":          true,
			"retention_allowed":         true,
			"research_identity_version": "structured-local-v1",
			"title":                     doc.Title,
			"time_basis":                doc.TimeBasis,
			"independent_source":        doc.IndependentSource,
			"local_origin":              storedOrigin,
		},
	})
	if err != nil {
		return c.Receipt{}, err
	}
	readback, found, err := a.staging.FindStagingItemByNamespaceEventID(ctx, namespace, eventID)
	if err != nil {
		return c.Receipt{}, err
	}
	if !found || item == nil || readback.ID != item.ID || !researchLocalMaterialStagingItemMatches(readback, namespace, eventID, sourceID, req.Envelope.DataScope, doc, identity, contentHash) {
		return c.Receipt{}, errors.New("local research material exact readback did not match the committed source")
	}
	return a.Verify(ctx, req.Envelope, researchLocalMaterialReceipt(readback, req.Envelope, identity, contentHash))
}

func (a *Adapters) verifyLocalMaterialReceipt(ctx context.Context, envelope c.Envelope, receipt c.Receipt, item l1sqlite.L1StagingItem) (c.Receipt, error) {
	if a == nil || a.localMaterialOwner == nil || ctx == nil || receipt.LocalIdentity == "" {
		return c.Receipt{}, errors.New("local research material verifier is unavailable")
	}
	doc, err := researchLocalMaterialDocumentFromStagingItem(item)
	if err != nil {
		return c.Receipt{}, err
	}
	identity, err := c.LocalCommitIdentity(doc)
	if err != nil || identity == "" || identity != receipt.LocalIdentity {
		return c.Receipt{}, errors.New("local research receipt identity does not match its source metadata")
	}
	contentHash := digestBytes([]byte(doc.Text))
	sourceID := researchLocalMaterialSourceID + identity
	eventID := researchLocalMaterialEventID + identity
	if receipt.Owner != "core.l1_staging" || receipt.OwnerRoute != KnowledgeStagingTarget || receipt.State != "committed" ||
		receipt.DestinationRef != "" || receipt.Scope != envelope.DataScope || receipt.Digest != contentHash || receipt.AuditRef != item.EventID || receipt.CanonicalID != item.ID ||
		!receipt.CommittedAt.Equal(item.CreatedAt) ||
		!researchLocalMaterialStagingItemMatches(item, item.Namespace, eventID, sourceID, envelope.DataScope, doc, identity, contentHash) {
		return c.Receipt{}, errors.New("local research receipt does not match the exact staged material")
	}
	if err := a.localMaterialOwner.VerifyLocalMaterial(ctx, envelope, doc); err != nil {
		return c.Receipt{}, fmt.Errorf("local research material is no longer authorized: %w", err)
	}
	receipt.VerifiedAt = a.currentTime()
	return receipt, nil
}

func (a *Adapters) reconcileLocalMaterial(ctx context.Context, req engine.CommitRequest, doc c.Document) (engine.ReconcileResult, error) {
	if a == nil || a.staging == nil || a.localMaterialOwner == nil || ctx == nil {
		return engine.ReconcileResult{}, errors.New("trusted local material reconciliation is unavailable")
	}
	identity, err := c.LocalCommitIdentity(doc)
	if err != nil || identity == "" {
		return engine.ReconcileResult{}, errors.New("local research material identity is invalid")
	}
	if err := a.localMaterialOwner.VerifyLocalMaterial(ctx, req.Envelope, doc); err != nil {
		return engine.ReconcileResult{}, fmt.Errorf("local research material is no longer authorized: %w", err)
	}
	namespace, err := stagingNamespace(req.Envelope)
	if err != nil {
		return engine.ReconcileResult{}, err
	}
	eventID := researchLocalMaterialEventID + identity
	sourceID := researchLocalMaterialSourceID + identity
	item, found, err := a.staging.FindStagingItemByNamespaceEventID(ctx, namespace, eventID)
	if err != nil {
		return engine.ReconcileResult{}, err
	}
	if !found {
		return engine.ReconcileResult{RetrySafe: true}, nil
	}
	contentHash := digestBytes([]byte(doc.Text))
	if !researchLocalMaterialStagingItemMatches(item, namespace, eventID, sourceID, req.Envelope.DataScope, doc, identity, contentHash) {
		return engine.ReconcileResult{}, errors.New("local research material reconciliation conflict")
	}
	receipt := researchLocalMaterialReceipt(item, req.Envelope, identity, contentHash)
	receipt, err = a.Verify(ctx, req.Envelope, receipt)
	return engine.ReconcileResult{Receipt: receipt}, err
}

func researchLocalMaterialReceipt(item l1sqlite.L1StagingItem, envelope c.Envelope, identity, contentHash string) c.Receipt {
	return c.Receipt{
		Owner: "core.l1_staging", OwnerRoute: KnowledgeStagingTarget, AuditRef: item.EventID, CanonicalID: item.ID,
		Digest: contentHash, State: "committed", Scope: envelope.DataScope, CommittedAt: item.CreatedAt, LocalIdentity: identity,
	}
}

func researchLocalMaterialStagingItemMatches(item l1sqlite.L1StagingItem, namespace, eventID, sourceID, scope string, expected c.Document, identity, contentHash string) bool {
	if item.Kind != l1sqlite.L1StagingKindSearchResult || item.Namespace != namespace || item.EventID != eventID ||
		item.SourceID != sourceID || item.SourceURL != "" || item.RawHash != contentHash || item.RawText != expected.Text ||
		digestBytes([]byte(item.RawText)) != contentHash || item.LicenseNote != researchLocalMaterialLicense ||
		(item.ValidationStatus != l1sqlite.L1StagingStatusPending && item.ValidationStatus != l1sqlite.L1StagingStatusValidated) {
		return false
	}
	meta := item.Meta
	complete, _ := meta["content_complete"].(bool)
	retention, _ := meta["retention_allowed"].(bool)
	if metaString(meta, "research_material_kind") != researchLocalMaterialMetaKind || metaString(meta, "research_identity_version") != "structured-local-v1" || metaString(meta, "research_scope") != scope ||
		metaString(meta, "research_namespace") != namespace || metaString(meta, "research_digest") != contentHash ||
		metaString(meta, "research_local_identity") != identity || metaString(meta, "source_kind") != expected.SourceKind ||
		metaString(meta, "provider_id") != expected.ProviderID || metaString(meta, "source_ref") != expected.SourceRef ||
		metaString(meta, "revision") != expected.Revision || metaString(meta, "content_scope") != expected.ContentScope ||
		metaString(meta, "original_hash") != expected.OriginalHash || metaString(meta, "extracted_hash") != contentHash || !complete || !retention {
		return false
	}
	stored, err := researchLocalMaterialDocumentFromStagingItem(item)
	if err != nil {
		return false
	}
	storedIdentity, err := c.LocalCommitIdentity(stored)
	return err == nil && storedIdentity == identity && stored.Text == expected.Text && stored.OriginalHash == expected.OriginalHash
}

func researchLocalMaterialDocumentFromStagingItem(item l1sqlite.L1StagingItem) (c.Document, error) {
	if item.Kind != l1sqlite.L1StagingKindSearchResult || metaString(item.Meta, "research_material_kind") != researchLocalMaterialMetaKind || item.RawText == "" {
		return c.Document{}, errors.New("staged item is not structured local research material")
	}
	originRaw, err := json.Marshal(item.Meta["local_origin"])
	if err != nil {
		return c.Document{}, errors.New("staged local research provenance is invalid")
	}
	var origin c.LocalMaterialOrigin
	if err := json.Unmarshal(originRaw, &origin); err != nil {
		return c.Document{}, errors.New("staged local research provenance is invalid")
	}
	complete, _ := item.Meta["content_complete"].(bool)
	retention, _ := item.Meta["retention_allowed"].(bool)
	doc := c.Document{
		ID:         item.ID,
		SourceKind: metaString(item.Meta, "source_kind"), ProviderID: metaString(item.Meta, "provider_id"),
		SourceRef: metaString(item.Meta, "source_ref"), Revision: metaString(item.Meta, "revision"),
		Title: metaString(item.Meta, "title"), ContentKind: metaString(item.Meta, "content_kind"),
		ContentScope: metaString(item.Meta, "content_scope"), ContentState: "fetched",
		OriginalHash: metaString(item.Meta, "original_hash"), ExtractedHash: item.RawHash, Text: item.RawText,
		RetentionAllowed: retention, Complete: complete, Primary: false,
		IndependentSource: metaString(item.Meta, "independent_source"), TimeBasis: metaString(item.Meta, "time_basis"),
		RetrievedAt: item.FetchedAt.UTC(), LocalOrigin: &origin,
	}
	if doc.ExtractedHash == "" || doc.ExtractedHash != digestBytes([]byte(doc.Text)) || strings.TrimSpace(doc.ContentScope) == "" ||
		doc.SourceRef == "" || doc.RetrievedAt.IsZero() {
		return c.Document{}, errors.New("staged local research document identity or content is invalid")
	}
	return doc, nil
}
