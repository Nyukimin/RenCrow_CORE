package core

import (
	"testing"
	"time"
)

func TestBuildManifestSnapshot(t *testing.T) {
	descriptors := []ModuleDescriptor{{Name: "core", Owner: StateOwnerCore}}
	snapshot := BuildManifestSnapshot(descriptors, time.Date(2026, 5, 30, 1, 2, 3, 0, time.UTC))

	if snapshot.UpdatedAt != "2026-05-30T01:02:03Z" || len(snapshot.Modules) != 1 || snapshot.Modules[0].Name != "core" {
		t.Fatalf("unexpected manifest snapshot: %+v", snapshot)
	}
}
