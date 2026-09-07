package core

import "testing"

func TestArtifactKindValuesAndValidation(t *testing.T) {
	tests := []struct {
		kind ArtifactKind
		want string
	}{
		{ArtifactKindReport, "report"},
		{ArtifactKindDraft, "draft"},
		{ArtifactKindContextPack, "context_pack"},
		{ArtifactKindImage, "image"},
		{ArtifactKindPatch, "patch"},
		{ArtifactKindSpecification, "specification"},
		{ArtifactKindTranscript, "transcript"},
		{ArtifactKindDiff, "diff"},
		{ArtifactKindDocument, "document"},
	}
	for _, test := range tests {
		if string(test.kind) != test.want {
			t.Errorf("artifact kind = %q, want %q", test.kind, test.want)
		}
		if err := test.kind.Validate(); err != nil {
			t.Errorf("artifact kind %q rejected: %v", test.kind, err)
		}
	}
	if err := ArtifactKind("").Validate(); err == nil {
		t.Fatal("empty artifact kind accepted")
	}
	if err := ArtifactKind("invalid").Validate(); err == nil {
		t.Fatal("invalid artifact kind accepted")
	}
}
