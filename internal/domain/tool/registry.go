package tool

import (
	"fmt"
	"sort"
	"sync"
)

// Registry は ToolManifest を管理する。
type Registry struct {
	mu        sync.RWMutex
	manifests map[string]ToolManifest
}

func NewRegistry() *Registry {
	return &Registry{manifests: make(map[string]ToolManifest)}
}

func (r *Registry) Register(manifest ToolManifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.manifests[manifest.ID]; ok && existing.Version != manifest.Version {
		return fmt.Errorf("manifest version conflict: %s (%s vs %s)", manifest.ID, existing.Version, manifest.Version)
	}
	r.manifests[manifest.ID] = manifest
	return nil
}

func (r *Registry) Get(id string) (ToolManifest, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.manifests[id]
	return m, ok
}

func (r *Registry) List() []ToolManifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	list := make([]ToolManifest, 0, len(r.manifests))
	for _, m := range r.manifests {
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list
}
