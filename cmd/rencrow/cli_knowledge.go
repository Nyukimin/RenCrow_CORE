package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	knowledgeapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledge"
	knowledgerelationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgerelation"
	domainrelation "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgerelation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
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
	knowledgerelationapp.RelationBuildStore
	ValidateStagingItem(ctx context.Context, id string, policy l1sqlite.L1StagingValidationPolicy) (*l1sqlite.L1StagingValidationResult, error)
	PromoteValidatedStagingItemToKnowledge(ctx context.Context, id string, domain string) (*l1sqlite.L1KnowledgeItem, error)
}

type knowledgeImportOptions struct {
	InputPath string
	JSON      bool
	Reviewed  bool
	Promote   bool
}

type knowledgeImportReport struct {
	Imported  int                         `json:"imported"`
	Validated int                         `json:"validated"`
	Promoted  int                         `json:"promoted"`
	Rejected  int                         `json:"rejected"`
	Items     []knowledgeImportReportItem `json:"items"`
}

type knowledgeImportReportItem struct {
	EventID   string                       `json:"event_id"`
	StagingID string                       `json:"staging_id"`
	Domain    string                       `json:"domain"`
	Status    string                       `json:"status"`
	Issues    []knowledgeImportReportIssue `json:"issues,omitempty"`
	Error     string                       `json:"error,omitempty"`
}

type knowledgeImportReportIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func runKnowledgeCommand(args []string, store knowledgeCLIStore, out io.Writer, errOut io.Writer) int {
	subcmd := ""
	if len(args) > 0 {
		subcmd = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch subcmd {
	case "import-core-jsonl":
		options, err := parseKnowledgeImportOptions(args[1:])
		if err != nil {
			if options.JSON {
				writeJSONCLI(out, knowledgeImportReport{Items: []knowledgeImportReportItem{}}, false)
			}
			fmt.Fprintln(errOut, err)
			fmt.Fprintln(errOut, knowledgeImportUsage())
			return 1
		}
		if options.Promote && !options.Reviewed {
			if options.JSON {
				writeJSONCLI(out, knowledgeImportReport{Items: []knowledgeImportReportItem{}}, false)
			}
			fmt.Fprintln(errOut, "--promote requires --reviewed")
			return 1
		}
		f, err := os.Open(options.InputPath)
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
		report, failed := reviewKnowledgeImport(context.Background(), store, result, options)
		if options.JSON {
			writeJSONCLI(out, report, false)
		} else {
			fmt.Fprintf(out, "knowledge core import: imported=%d validated=%d promoted=%d rejected=%d\n",
				report.Imported, report.Validated, report.Promoted, report.Rejected)
		}
		if failed {
			fmt.Fprintf(errOut, "knowledge review failed: rejected=%d\n", report.Rejected)
			return 1
		}
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
	case "relations":
		return runKnowledgeRelationsCommand(args[1:], store, out, errOut)
	default:
		fmt.Fprintf(errOut, "unknown knowledge subcommand: %s\n", subcmd)
		fmt.Fprintf(errOut, "%s | index-wiki [docs/wiki] [--repo-root <path>] | relations build [--domain all] [--limit 100] [--dry-run=true]\n", knowledgeImportUsage())
		return 1
	}
}

func parseKnowledgeImportOptions(args []string) (knowledgeImportOptions, error) {
	var options knowledgeImportOptions
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		switch arg {
		case "":
			continue
		case "--json":
			options.JSON = true
		case "--reviewed":
			options.Reviewed = true
		case "--promote":
			options.Promote = true
		default:
			if strings.HasPrefix(arg, "--") {
				return options, fmt.Errorf("unknown knowledge import option: %s", arg)
			}
			if options.InputPath != "" {
				return options, fmt.Errorf("unexpected knowledge import argument: %s", arg)
			}
			options.InputPath = arg
		}
	}
	if options.InputPath == "" {
		return options, errors.New("knowledge core jsonl path is required")
	}
	return options, nil
}

func knowledgeImportUsage() string {
	return "usage: rencrow knowledge import-core-jsonl <path> [--reviewed] [--promote] [--json]"
}

func reviewKnowledgeImport(ctx context.Context, store knowledgeCLIStore, result knowledgeapp.ImportResult, options knowledgeImportOptions) (knowledgeImportReport, bool) {
	report := knowledgeImportReport{
		Imported: result.Imported,
		Items:    make([]knowledgeImportReportItem, 0, len(result.Items)),
	}
	failed := false
	for _, imported := range result.Items {
		item := knowledgeImportReportItem{
			EventID:   imported.EventID,
			StagingID: imported.StagingID,
			Domain:    imported.Domain,
			Status:    imported.Status,
		}
		if !options.Reviewed {
			report.Items = append(report.Items, item)
			continue
		}
		validation, err := store.ValidateStagingItem(ctx, imported.StagingID, l1sqlite.L1StagingValidationPolicy{})
		if err != nil {
			item.Status = "error"
			item.Error = "validation_failed"
			failed = true
			report.Items = append(report.Items, item)
			continue
		}
		item.Status = validation.Status
		for _, issue := range validation.Issues {
			item.Issues = append(item.Issues, knowledgeImportReportIssue{Code: issue.Code, Message: issue.Message})
		}
		if !validation.Passed {
			report.Rejected++
			failed = true
			report.Items = append(report.Items, item)
			continue
		}
		report.Validated++
		if options.Promote {
			if _, err := store.PromoteValidatedStagingItemToKnowledge(ctx, imported.StagingID, imported.Domain); err != nil {
				item.Status = "error"
				item.Error = "promotion_failed"
				failed = true
				report.Items = append(report.Items, item)
				continue
			}
			item.Status = "promoted"
			report.Promoted++
		}
		report.Items = append(report.Items, item)
	}
	return report, failed
}

func runKnowledgeRelationsCommand(args []string, store knowledgerelationapp.RelationBuildStore, out io.Writer, errOut io.Writer) int {
	if len(args) == 0 || strings.ToLower(strings.TrimSpace(args[0])) != "build" {
		fmt.Fprintln(errOut, "usage: rencrow knowledge relations build [--domain all] [--limit 100] [--dry-run=true] [--json]")
		return 1
	}
	fs := flag.NewFlagSet("knowledge relations build", flag.ContinueOnError)
	fs.SetOutput(errOut)
	domain := fs.String("domain", "all", "knowledge domain or all")
	limit := fs.Int("limit", 100, "maximum knowledge items")
	dryRun := fs.Bool("dry-run", true, "report intended writes without applying them")
	jsonOut := fs.Bool("json", false, "write JSON report")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if *limit < 1 || *limit > 1000 {
		fmt.Fprintln(errOut, "limit must be between 1 and 1000")
		return 1
	}
	service := knowledgerelationapp.NewRelationBuildService(store, knowledgerelationapp.NewMetadataExtractor(nil), domainrelation.DefaultScoringConfig())
	report, err := service.BuildBatch(context.Background(), knowledgerelationapp.BatchQuery{Domain: *domain, Limit: *limit, DryRun: *dryRun})
	if err != nil {
		fmt.Fprintf(errOut, "failed to build knowledge relations: %v\n", err)
		return 1
	}
	if *jsonOut {
		writeJSONCLI(out, report, false)
		return 0
	}
	fmt.Fprintf(out, "knowledge relation build: status=%s dry_run=%t checked=%d entities=%d item_entities=%d relations=%d skipped=%d\n",
		report.Status, report.DryRun, report.CheckedItems, report.EntityUpserts, report.ItemEntityUpserts, report.RelationUpserts, report.Skipped)
	return 0
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

var _ knowledgeCLIStore = (*l1sqlite.L1SQLiteStore)(nil)
