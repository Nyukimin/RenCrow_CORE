package research

import (
	"context"
	"strings"
	"testing"

	c "github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/contract"
	"github.com/Nyukimin/RenCrow_Tools/tools/research/research_engine/engine"
)

func TestTechnicalSourceOwnerPreservesLimitedCoverageAndRejectsWrongRevision(t *testing.T) {
	for _, fixture := range []struct{ kind, provider, scope, revision string }{
		{"github", "github", "repository_readme", strings.Repeat("a", 40)},
		{"huggingface", "huggingface", "model_card", strings.Repeat("b", 40)},
		{"huggingface", "huggingface", "dataset_card", strings.Repeat("c", 40)},
		{"arxiv", "arxiv", "abstract", "v2"},
	} {
		t.Run(fixture.provider+"_"+fixture.scope, func(t *testing.T) {
			store := &stagingStub{}
			owner := NewAdapters(nil, nil, store).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
			body := "Only the acquired README, card, or abstract is covered."
			hash := digestBytes([]byte(body))
			doc := c.Document{SourceKind: fixture.kind, ProviderID: fixture.provider, SourceRef: "https://example.org/source/" + fixture.revision,
				OriginalURL: "https://example.org/source", FinalURL: "https://example.org/source", Revision: fixture.revision,
				ContentScope: fixture.scope, ContentState: "fetched", ExtractedHash: hash, Text: body, Complete: true, RetentionAllowed: true}
			req := engine.CommitRequest{Envelope: c.Envelope{RunID: "run_technical", DataScope: "public"}, Target: c.SaveTarget{Target: KnowledgeStagingTarget}, Document: &doc, Digest: hash}
			req.IdempotencyKey = IdempotencyKey(req.Envelope.RunID, req.Target.Target, "", hash)
			receipt, err := owner.Commit(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := owner.Verify(context.Background(), req.Envelope, receipt); err != nil {
				t.Fatal(err)
			}
			if store.item.Meta["content_scope"] != fixture.scope || store.item.Meta["revision"] != fixture.revision {
				t.Fatalf("limited coverage/revision was lost: %+v", store.item.Meta)
			}
			changedPolicy := req.Envelope
			changedPolicy.PolicyRevision = "changed_retention_policy"
			if _, err := owner.Verify(context.Background(), changedPolicy, receipt); err == nil {
				t.Fatal("technical source reused after its license/retention policy changed without revalidation")
			}
			for _, mutate := range []func(*c.Document){
				func(d *c.Document) { d.ContentScope = "full_analysis" },
				func(d *c.Document) { d.Revision = "main" },
				func(d *c.Document) { d.ProviderID = "direct" },
			} {
				invalid := doc
				mutate(&invalid)
				other := &stagingStub{}
				adapter := NewAdapters(nil, nil, other).WithSourcePolicy(func(context.Context, c.Candidate) (bool, bool) { return true, true })
				req.Document = &invalid
				if _, err := adapter.Commit(context.Background(), req); err == nil || other.item.EventID != "" {
					t.Fatalf("unregistered coverage/identity reached owner storage: document=%+v err=%v", invalid, err)
				}
			}
			for _, policy := range []SourcePolicy{nil, func(context.Context, c.Candidate) (bool, bool) { return false, false }} {
				other := &stagingStub{}
				adapter := NewAdapters(nil, nil, other).WithSourcePolicy(policy)
				req.Document = &doc
				if _, err := adapter.Commit(context.Background(), req); err == nil || other.item.EventID != "" {
					t.Fatalf("owner wrote body before checking current retention policy: err=%v", err)
				}
			}
		})
	}
}
