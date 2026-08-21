package backlog

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed catalog/atlas_catalog.json
var atlasCatalogJSON []byte

type AtlasCatalog struct {
	SchemaVersion int              `json:"schema_version"`
	BootstrapAt   string           `json:"bootstrap_at"`
	Source        map[string]any   `json:"source"`
	Features      []map[string]any `json:"features"`
	Modules       []map[string]any `json:"modules"`
}

func LoadAtlasCatalog() (AtlasCatalog, error) {
	var catalog AtlasCatalog
	if err := json.Unmarshal(atlasCatalogJSON, &catalog); err != nil {
		return AtlasCatalog{}, fmt.Errorf("decode embedded Atlas catalog: %w", err)
	}
	if catalog.SchemaVersion != 1 || catalog.BootstrapAt == "" || len(catalog.Features) == 0 {
		return AtlasCatalog{}, fmt.Errorf("embedded Atlas catalog is incomplete")
	}
	return catalog, nil
}
