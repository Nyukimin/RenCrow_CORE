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

type runtimeBorrowedDurableWorkflowStore struct{ appstore.Store }

// Close deliberately does not own the shared storage-host client lifecycle.
func (*runtimeBorrowedDurableWorkflowStore) Close() error { return nil }

func buildDurableStoreRuntime(cfg *config.Config, selectedStores ...appstore.Store) (orchestrator.DurableStoreWorkflow, interface{ Close() error }, error) {
	if len(selectedStores) > 1 {
		return nil, nil, fmt.Errorf("durable store runtime accepts only one selected owner")
	}
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
	var store appstore.Store
	var closer interface{ Close() error }
	if len(selectedStores) == 1 && selectedStores[0] != nil {
		store = selectedStores[0]
		closer = &runtimeBorrowedDurableWorkflowStore{Store: store}
	} else {
		localStore, err := persistencestore.NewSQLiteStore(cfg.Storage.Databases.DurableStoreWorkflow)
		if err != nil {
			return nil, nil, fmt.Errorf("open durable store workflow registry: %w", err)
		}
		store = localStore
		closer = localStore
	}
	return appstore.NewService([]domainstore.Manifest{manifest}, store, nil), closer, nil
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
