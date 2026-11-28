package attachment

import (
	"context"
	"testing"
)

func TestTrustedRequestAttachmentsKeepOnlyCopiedServerReferences(t *testing.T) {
	input := []Attachment{{ID: "att_opaque", Path: "/workspace/viewer_uploads/att_opaque.flac", Data: []byte("FLAC"), SecurityWarnings: []string{"warning"}}}
	ctx := WithTrustedRequestAttachments(context.Background(), input)
	input[0].Data[0] = 'x'
	input[0].SecurityWarnings[0] = "changed"
	input[0].ID = "att_changed"

	first, found := TrustedRequestAttachmentsFromContext(ctx)
	if !found || len(first) != 1 || first[0].ID != "att_opaque" || first[0].Data != nil || first[0].Path != "/workspace/viewer_uploads/att_opaque.flac" || first[0].SecurityWarnings[0] != "warning" {
		t.Fatalf("stored request attachments changed with source mutation: found=%v attachments=%+v", found, first)
	}
	first[0].SecurityWarnings[0] = "changed again"

	second, found := TrustedRequestAttachmentsFromContext(ctx)
	if !found || second[0].Data != nil || second[0].SecurityWarnings[0] != "warning" {
		t.Fatalf("returned attachment copy mutated context value: found=%v attachments=%+v", found, second)
	}
}

func TestTrustedRequestAttachmentsContextDistinguishesMissingFromEmpty(t *testing.T) {
	if attachments, found := TrustedRequestAttachmentsFromContext(context.Background()); found || attachments != nil {
		t.Fatalf("missing context value = (%v, %v), want (nil, false)", attachments, found)
	}
	if ctx := WithTrustedRequestAttachments(context.Background(), nil); ctx == nil {
		t.Fatal("empty attachment binding returned a nil context")
	}
}

func TestTrustedRequestAttachmentsByIDReturnsOnlyClonedMatchingEntries(t *testing.T) {
	input := []Attachment{
		{ID: "att_other", Path: "/workspace/other"},
		{ID: "att_target", Path: "/workspace/target", Data: []byte("FLAC"), SecurityWarnings: []string{"trusted"}},
	}
	ctx := WithTrustedRequestAttachments(context.Background(), input)
	matches, found := TrustedRequestAttachmentsByIDFromContext(ctx, "att_target")
	if !found || len(matches) != 1 || matches[0].Data != nil || matches[0].Path != "/workspace/target" || matches[0].SecurityWarnings[0] != "trusted" {
		t.Fatalf("lookup result = (found=%v, matches=%+v)", found, matches)
	}
	matches[0].SecurityWarnings[0] = "mutated"
	all, found := TrustedRequestAttachmentsFromContext(ctx)
	if !found || all[1].Data != nil || all[1].SecurityWarnings[0] != "trusted" {
		t.Fatalf("lookup copy changed the bound context: found=%v attachments=%+v", found, all)
	}
	missing, found := TrustedRequestAttachmentsByIDFromContext(ctx, "att_missing")
	if !found || len(missing) != 0 {
		t.Fatalf("missing ID lookup = (found=%v, matches=%v)", found, missing)
	}
}
