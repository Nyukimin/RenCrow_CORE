package webgather

import (
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	"strings"
)

type L1StagingStore interface {
	SaveStagingItem(ctx context.Context, item l1sqlite.L1StagingItem) (*l1sqlite.L1StagingItem, error)
}

type L1StagingWriter struct {
	store L1StagingStore
}

func NewL1StagingWriter(store L1StagingStore) *L1StagingWriter {
	return &L1StagingWriter{store: store}
}

func (w *L1StagingWriter) Save(ctx context.Context, req modulewebgather.FetchRequest, artifact modulewebgather.FetchArtifact, doc modulewebgather.ExtractedDocument, meta map[string]any) (modulewebgather.StagingRecord, error) {
	if w == nil || w.store == nil {
		return modulewebgather.StagingRecord{}, modulewebgather.NewError(modulewebgather.ErrStagingFailed, "l1 staging store is not configured")
	}
	sourceID := strings.TrimSpace(req.SourceID)
	if sourceID == "" {
		sourceID = modulewebgather.SourceIDFromURL(firstNonEmpty(doc.CanonicalURL, artifact.FinalURL, req.URL))
	}
	rawHash := modulewebgather.SHA256Text(doc.Text)
	eventID := modulewebgather.EventID(sourceID, firstNonEmpty(artifact.FinalURL, req.URL), rawHash)
	summary := strings.TrimSpace(doc.Excerpt)
	if summary == "" {
		summary = modulewebgather.TextPreview(doc.Text, 240)
	}
	keywords := doc.Keywords
	if len(keywords) == 0 {
		keywords = []string{"web_gather"}
	}
	item, err := w.store.SaveStagingItem(ctx, l1sqlite.L1StagingItem{
		Kind:             l1sqlite.L1StagingKindExternalFetch,
		Namespace:        req.Namespace,
		EventID:          eventID,
		SourceID:         sourceID,
		SourceURL:        firstNonEmpty(artifact.FinalURL, req.URL),
		FetchedAt:        artifact.FetchedAt,
		PublishedAt:      doc.PublishedAt,
		RawText:          doc.Text,
		SummaryDraft:     summary,
		Keywords:         keywords,
		LicenseNote:      req.LicenseNote,
		ValidationStatus: l1sqlite.L1StagingStatusPending,
		Meta:             meta,
	})
	if err != nil {
		return modulewebgather.StagingRecord{}, err
	}
	return modulewebgather.StagingRecord{
		ID:               item.ID,
		ValidationStatus: item.ValidationStatus,
		RawHash:          item.RawHash,
	}, nil
}
