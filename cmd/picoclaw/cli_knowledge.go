package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	knowledgeapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/knowledge"
	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

func cmdKnowledge() {
	configPath := getConfigPath()
	store, err := loadSourceRegistryStore(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize knowledge store: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()
	code := runKnowledgeCommand(os.Args[2:], store, os.Stdout, os.Stderr)
	if code != 0 {
		os.Exit(code)
	}
}

type knowledgeCLIStore interface {
	knowledgeapp.StagingStore
	knowledgeapp.WikiIndexStore
}

func runKnowledgeCommand(args []string, store knowledgeCLIStore, out io.Writer, errOut io.Writer) int {
	subcmd := ""
	if len(args) > 0 {
		subcmd = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch subcmd {
	case "import-core-jsonl":
		jsonOut := hasFlag(args[1:], "--json")
		var inputPath string
		for _, arg := range args[1:] {
			if strings.HasPrefix(arg, "--") {
				continue
			}
			inputPath = arg
			break
		}
		if strings.TrimSpace(inputPath) == "" {
			fmt.Fprintln(errOut, "usage: picoclaw knowledge import-core-jsonl <path> [--json]")
			return 1
		}
		f, err := os.Open(inputPath)
		if err != nil {
			fmt.Fprintf(errOut, "failed to open knowledge jsonl: %v\n", err)
			return 1
		}
		defer f.Close()
		result, err := knowledgeapp.ImportKnowledgeCoreJSONL(context.Background(), store, f, knowledgeapp.ImportOptions{})
		if err != nil {
			fmt.Fprintf(errOut, "failed to import knowledge jsonl: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"imported": result.Imported}, false)
			return 0
		}
		fmt.Fprintf(out, "imported knowledge core records: %d\n", result.Imported)
		return 0
	case "index-wiki":
		jsonOut := hasFlag(args[1:], "--json")
		rootDir, repoRoot := parseWikiIndexArgs(args[1:])
		result, err := knowledgeapp.IndexKnowledgeWiki(context.Background(), store, knowledgeapp.WikiIndexOptions{
			RootDir:  rootDir,
			RepoRoot: repoRoot,
		})
		if err != nil {
			fmt.Fprintf(errOut, "failed to index knowledge wiki: %v\n", err)
			return 1
		}
		if jsonOut {
			writeJSONCLI(out, map[string]any{"indexed": result.Indexed, "skipped": result.Skipped}, false)
			return 0
		}
		fmt.Fprintf(out, "indexed knowledge wiki pages: %d (skipped: %d)\n", result.Indexed, result.Skipped)
		return 0
	default:
		fmt.Fprintf(errOut, "unknown knowledge subcommand: %s\n", subcmd)
		fmt.Fprintln(errOut, "usage: picoclaw knowledge import-core-jsonl <path> | index-wiki [docs/wiki] [--repo-root <path>]")
		return 1
	}
}

func parseWikiIndexArgs(args []string) (string, string) {
	rootDir := filepath.Join("docs", "wiki")
	repoRoot := "."
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch arg {
		case "", "--json":
			continue
		case "--repo-root":
			if i+1 < len(args) {
				repoRoot = args[i+1]
				i++
			}
		default:
			if strings.HasPrefix(arg, "--repo-root=") {
				repoRoot = strings.TrimPrefix(arg, "--repo-root=")
				continue
			}
			if strings.HasPrefix(arg, "--") {
				continue
			}
			rootDir = arg
		}
	}
	if abs, err := filepath.Abs(repoRoot); err == nil {
		repoRoot = abs
	}
	if !filepath.IsAbs(rootDir) {
		rootDir = filepath.Join(repoRoot, rootDir)
	}
	return rootDir, repoRoot
}

var _ knowledgeCLIStore = (*conversationpersistence.L1SQLiteStore)(nil)
