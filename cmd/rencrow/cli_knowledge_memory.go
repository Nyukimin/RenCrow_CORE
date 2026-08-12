package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	knowledgememorypersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/knowledgememory"
)

type knowledgeMemoryPromoter func(context.Context, string, string) (knowledgememorypersistence.ImportReport, error)

func cmdKnowledgeMemory() {
	cfg, err := config.LoadConfig(getConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to load config: %v\n", err)
		os.Exit(1)
	}
	code := runKnowledgeMemoryCommand(context.Background(), os.Args[2:], cfg, knowledgememorypersistence.ImportJSONLToSQLite, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

func runKnowledgeMemoryCommand(ctx context.Context, args []string, cfg *config.Config, promote knowledgeMemoryPromoter, out, errOut io.Writer) int {
	if len(args) != 1 || strings.TrimSpace(args[0]) != "promote" {
		fmt.Fprintln(errOut, "usage: rencrow knowledge-memory promote")
		return 1
	}
	if cfg == nil || !cfg.KnowledgeMemory.IsEnabled() {
		fmt.Fprintln(errOut, "knowledge memory is disabled")
		return 1
	}
	sourceRoot := strings.TrimSpace(cfg.KnowledgeMemory.LogPath)
	targetPath := strings.TrimSpace(cfg.Storage.Databases.KnowledgeMemory)
	if sourceRoot == "" || targetPath == "" {
		fmt.Fprintln(errOut, "knowledge memory source or target is not configured")
		return 1
	}
	report, err := promote(ctx, sourceRoot, targetPath)
	if err != nil {
		fmt.Fprintf(errOut, "knowledge memory promotion failed: %v\n", err)
		return 1
	}
	if report.Coverage.State != knowledgememorypersistence.KnowledgeMemoryCoverageReady || report.SourceCount != report.ImportedCount {
		fmt.Fprintln(errOut, "knowledge memory promotion did not reach ready integrity state")
		return 1
	}
	if err := json.NewEncoder(out).Encode(report); err != nil {
		fmt.Fprintf(errOut, "encode knowledge memory promotion report: %v\n", err)
		return 1
	}
	return 0
}
