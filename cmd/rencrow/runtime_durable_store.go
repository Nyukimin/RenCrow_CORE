package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appstore "github.com/Nyukimin/RenCrow_CORE/internal/application/durablestore"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainstore "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	persistencestore "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/durablestore"
)

func buildDurableStoreRuntime(cfg *config.Config) (orchestrator.DurableStoreWorkflow, interface{ Close() error }, error) {
	if cfg == nil || !cfg.DurableStore.Enabled {
		return nil, nil, nil
	}
	data, err := os.ReadFile(cfg.DurableStore.ManifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read durable store manifest: %w", err)
	}
	manifest, err := decodeDurableStoreManifest(data)
	if err != nil {
		return nil, nil, fmt.Errorf("decode durable store manifest: %w", err)
	}
	if err := domainstore.ValidateRegistry([]domainstore.Manifest{manifest}); err != nil {
		return nil, nil, fmt.Errorf("validate durable store manifest: %w", err)
	}
	store, err := persistencestore.NewSQLiteStore(cfg.Storage.Databases.DurableStoreWorkflow)
	if err != nil {
		return nil, nil, fmt.Errorf("open durable store workflow registry: %w", err)
	}
	return appstore.NewService([]domainstore.Manifest{manifest}, store, nil), store, nil
}

func decodeDurableStoreManifest(data []byte) (domainstore.Manifest, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest domainstore.Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return domainstore.Manifest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return domainstore.Manifest{}, fmt.Errorf("multiple JSON values are not allowed")
		}
		return domainstore.Manifest{}, err
	}
	return manifest, nil
}
