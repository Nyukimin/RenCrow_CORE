package core

import "time"

type ManifestSnapshot struct {
	UpdatedAt string             `json:"updated_at"`
	Modules   []ModuleDescriptor `json:"modules"`
}

func BuildManifestSnapshot(descriptors []ModuleDescriptor, updatedAt time.Time) ManifestSnapshot {
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	return ManifestSnapshot{
		UpdatedAt: updatedAt.UTC().Format(time.RFC3339),
		Modules:   descriptors,
	}
}
