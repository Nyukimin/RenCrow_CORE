package main

import (
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
)

func TestAuthenticatedMioViewerProvenanceAcceptsOnlyTheTwoIngressContracts(t *testing.T) {
	for _, tt := range []struct {
		name       string
		provenance viewer.RequestProvenance
		want       bool
	}{
		{name: "legacy CMD chat", provenance: viewer.RequestProvenance{OperationSource: "RenCrow_CMD", InteractionProfile: "cmd-chat"}, want: true},
		{name: "native COREViewer", provenance: viewer.RequestProvenance{OperationSource: "RenCrow_CORE", InteractionProfile: "core-viewer"}, want: true},
		{name: "CMD control profile", provenance: viewer.RequestProvenance{OperationSource: "RenCrow_CMD", InteractionProfile: "cmd-control"}},
		{name: "native forged profile", provenance: viewer.RequestProvenance{OperationSource: "RenCrow_CORE", InteractionProfile: "core-viewer-forged"}},
		{name: "crossed client profile", provenance: viewer.RequestProvenance{OperationSource: "RenCrow_CMD", InteractionProfile: "core-viewer"}},
		{name: "unrelated client", provenance: viewer.RequestProvenance{OperationSource: "RenCrow_PORTAL", InteractionProfile: "portal-chat"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAuthenticatedMioViewerProvenance(tt.provenance); got != tt.want {
				t.Fatalf("isAuthenticatedMioViewerProvenance(%+v)=%t, want %t", tt.provenance, got, tt.want)
			}
		})
	}
}
