//go:build !((linux && amd64) || (darwin && arm64))

package toolregistry

import (
	"context"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
)

// DuckDBToolRegistryStore is unavailable on platforms where the DuckDB driver does not build.
type DuckDBToolRegistryStore struct{}

func NewDuckDBToolRegistryStore(_ string) (*DuckDBToolRegistryStore, error) {
	return nil, fmt.Errorf("duckdb tool registry is not supported on this platform")
}

func (s *DuckDBToolRegistryStore) Close() error {
	return nil
}

func (s *DuckDBToolRegistryStore) Register(context.Context, capability.ToolEntry) error {
	return fmt.Errorf("duckdb tool registry is not supported on this platform")
}

func (s *DuckDBToolRegistryStore) ListForPlatform(context.Context, string) ([]capability.ToolEntry, error) {
	return nil, fmt.Errorf("duckdb tool registry is not supported on this platform")
}

func (s *DuckDBToolRegistryStore) Get(context.Context, string) (capability.ToolEntry, error) {
	return capability.ToolEntry{}, fmt.Errorf("duckdb tool registry is not supported on this platform")
}

var _ capability.ToolRegistry = (*DuckDBToolRegistryStore)(nil)
