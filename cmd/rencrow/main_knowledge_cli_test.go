package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type knowledgeImportCLIReport struct {
	Imported  int                            `json:"imported"`
	Validated int                            `json:"validated"`
	Promoted  int                            `json:"promoted"`
	Rejected  int                            `json:"rejected"`
	Items     []knowledgeImportCLIReportItem `json:"items"`
}

type knowledgeImportCLIReportItem struct {
	EventID   string `json:"event_id"`
	StagingID string `json:"staging_id"`
	Domain    string `json:"domain"`
	Status    string `json:"status"`
	Issues    []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"issues"`
	Error string `json:"error"`
}

func decodeKnowledgeImportCLIReport(t *testing.T, output string) knowledgeImportCLIReport {
	t.Helper()
	var report knowledgeImportCLIReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatalf("decode import report: %v\noutput=%s", err, output)
	}
	return report
}

func TestRunKnowledgeCommandImportCoreJSONLDefaultsToPendingStaging(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	inputPath := filepath.Join(t.TempDir(), "knowledge.jsonl")
	if err := os.WriteFile(inputPath, []byte(`{"id":"bookmark:test","title":"Test Bookmark","summary":"ブックマークのメモ","source_id":"vault:windows"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	var out, errOut bytes.Buffer

	code := runKnowledgeCommand([]string{"import-core-jsonl", inputPath, "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("import should pass, code=%d err=%s", code, errOut.String())
	}
	report := decodeKnowledgeImportCLIReport(t, out.String())
	if report.Imported != 1 || report.Validated != 0 || report.Promoted != 0 || report.Rejected != 0 || len(report.Items) != 1 {
		t.Fatalf("expected pending-only report, got %+v", report)
	}
	if report.Items[0].EventID != "bookmark:test" || report.Items[0].StagingID == "" || report.Items[0].Domain != "general" || report.Items[0].Status != l1sqlite.L1StagingStatusPending {
		t.Fatalf("unexpected pending item report: %+v", report.Items[0])
	}
	items, err := store.RecentStagingItems(context.Background(), l1sqlite.L1StagingStatusPending, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(items) != 1 || items[0].Namespace != "kb:general" || items[0].EventID != "bookmark:test" {
		t.Fatalf("unexpected staged items: %+v", items)
	}
	knowledge, err := store.RecentKnowledgeItems(context.Background(), "general", 10)
	if err != nil {
		t.Fatalf("RecentKnowledgeItems failed: %v", err)
	}
	if len(knowledge) != 0 {
		t.Fatalf("default import must not promote knowledge: %+v", knowledge)
	}
}

func TestRunKnowledgeCommandImportCoreJSONLReviewedPromotesToJSONLDomain(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	inputPath := filepath.Join(t.TempDir(), "knowledge.jsonl")
	if err := os.WriteFile(inputPath, []byte(`{"id":"bookmark:reviewed","domain":"bookmarks","title":"Reviewed Bookmark","raw_text":"安全なブックマーク本文","summary":"安全な要約","source_id":"vault:windows","license_note":"user vault migration"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	var out, errOut bytes.Buffer

	code := runKnowledgeCommand([]string{"import-core-jsonl", inputPath, "--reviewed", "--promote", "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("reviewed promotion should pass, code=%d err=%s", code, errOut.String())
	}
	report := decodeKnowledgeImportCLIReport(t, out.String())
	if report.Imported != 1 || report.Validated != 1 || report.Promoted != 1 || report.Rejected != 0 || len(report.Items) != 1 {
		t.Fatalf("unexpected reviewed promotion report: %+v", report)
	}
	if item := report.Items[0]; item.EventID != "bookmark:reviewed" || item.StagingID == "" || item.Domain != "bookmarks" || item.Status != "promoted" || item.Error != "" || len(item.Issues) != 0 {
		t.Fatalf("unexpected promoted item report: %+v", item)
	}
	knowledge, err := store.RecentKnowledgeItems(context.Background(), "bookmarks", 10)
	if err != nil {
		t.Fatalf("RecentKnowledgeItems failed: %v", err)
	}
	if len(knowledge) != 1 || knowledge[0].StagingID != report.Items[0].StagingID || knowledge[0].Domain != "bookmarks" {
		t.Fatalf("expected knowledge promoted from reported staging item, got %+v", knowledge)
	}
}

func TestRunKnowledgeCommandImportCoreJSONLReviewedValidatesWithoutPromotion(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	inputPath := filepath.Join(t.TempDir(), "knowledge.jsonl")
	if err := os.WriteFile(inputPath, []byte(`{"id":"bookmark:validated","domain":"bookmarks","title":"Validated Bookmark","raw_text":"検証対象の本文","summary":"検証対象の要約","source_id":"vault:windows","license_note":"user vault migration"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	var out, errOut bytes.Buffer

	code := runKnowledgeCommand([]string{"import-core-jsonl", inputPath, "--reviewed", "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("reviewed validation should pass, code=%d err=%s", code, errOut.String())
	}
	report := decodeKnowledgeImportCLIReport(t, out.String())
	if report.Imported != 1 || report.Validated != 1 || report.Promoted != 0 || report.Rejected != 0 || len(report.Items) != 1 {
		t.Fatalf("unexpected reviewed validation report: %+v", report)
	}
	if report.Items[0].Status != l1sqlite.L1StagingStatusValidated {
		t.Fatalf("reviewed item must remain validated without promotion: %+v", report.Items[0])
	}
	knowledge, err := store.RecentKnowledgeItems(context.Background(), "bookmarks", 10)
	if err != nil {
		t.Fatalf("RecentKnowledgeItems failed: %v", err)
	}
	if len(knowledge) != 0 {
		t.Fatalf("--reviewed without --promote must not create knowledge: %+v", knowledge)
	}
}

func TestRunKnowledgeCommandImportCoreJSONLRejectsPromoteWithoutReviewed(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	inputPath := filepath.Join(t.TempDir(), "knowledge.jsonl")
	if err := os.WriteFile(inputPath, []byte(`{"id":"bookmark:blocked","domain":"bookmarks","summary":"未実行","source_id":"vault:windows"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	var out, errOut bytes.Buffer

	code := runKnowledgeCommand([]string{"import-core-jsonl", inputPath, "--promote", "--json"}, store, &out, &errOut)
	if code == 0 || !strings.Contains(errOut.String(), "--promote requires --reviewed") {
		t.Fatalf("promote without reviewed must be rejected, code=%d stderr=%s", code, errOut.String())
	}
	report := decodeKnowledgeImportCLIReport(t, out.String())
	if report.Imported != 0 || report.Validated != 0 || report.Promoted != 0 || report.Rejected != 0 || len(report.Items) != 0 {
		t.Fatalf("promotion gate should not write any items: %+v", report)
	}
	items, err := store.RecentStagingItems(context.Background(), l1sqlite.L1StagingStatusPending, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("promotion gate must run before staging writes: %+v", items)
	}
}

func TestRunKnowledgeCommandImportCoreJSONLReportsRejectedItemWithoutRawText(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	inputPath := filepath.Join(t.TempDir(), "knowledge.jsonl")
	rawText := "secret: do-not-report"
	if err := os.WriteFile(inputPath, []byte(`{"id":"bookmark:rejected","domain":"bookmarks","title":"Rejected Bookmark","raw_text":"`+rawText+`","summary":"安全な要約","source_id":"vault:windows","license_note":"user vault migration"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	var out, errOut bytes.Buffer

	code := runKnowledgeCommand([]string{"import-core-jsonl", inputPath, "--reviewed", "--promote", "--json"}, store, &out, &errOut)
	if code == 0 {
		t.Fatalf("rejected staging item must return nonzero, stdout=%s stderr=%s", out.String(), errOut.String())
	}
	report := decodeKnowledgeImportCLIReport(t, out.String())
	if report.Imported != 1 || report.Validated != 0 || report.Promoted != 0 || report.Rejected != 1 || len(report.Items) != 1 {
		t.Fatalf("unexpected rejection report: %+v", report)
	}
	if item := report.Items[0]; item.Status != l1sqlite.L1StagingStatusRejected || item.Error != "" || len(item.Issues) != 1 || item.Issues[0].Code != "sensitive_raw_text" {
		t.Fatalf("unexpected rejected item report: %+v", item)
	}
	for _, output := range []string{out.String(), errOut.String()} {
		if strings.Contains(output, rawText) || strings.Contains(output, `"raw_text"`) {
			t.Fatalf("report must not expose raw text: %s", output)
		}
	}
	knowledge, err := store.RecentKnowledgeItems(context.Background(), "bookmarks", 10)
	if err != nil {
		t.Fatalf("RecentKnowledgeItems failed: %v", err)
	}
	if len(knowledge) != 0 {
		t.Fatalf("rejected item must not be promoted: %+v", knowledge)
	}
}

func TestRunKnowledgeCommandImportCoreJSONLRerunIsIdempotent(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	inputPath := filepath.Join(t.TempDir(), "knowledge.jsonl")
	if err := os.WriteFile(inputPath, []byte(`{"id":"bookmark:rerun","domain":"bookmarks","title":"Rerun Bookmark","raw_text":"再実行できる本文","summary":"再実行要約","source_id":"vault:windows","license_note":"user vault migration"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	var firstOut, firstErr bytes.Buffer
	if code := runKnowledgeCommand([]string{"import-core-jsonl", inputPath, "--reviewed", "--promote", "--json"}, store, &firstOut, &firstErr); code != 0 {
		t.Fatalf("first import should pass, code=%d err=%s", code, firstErr.String())
	}
	first := decodeKnowledgeImportCLIReport(t, firstOut.String())
	var secondOut, secondErr bytes.Buffer
	if code := runKnowledgeCommand([]string{"import-core-jsonl", inputPath, "--reviewed", "--promote", "--json"}, store, &secondOut, &secondErr); code != 0 {
		t.Fatalf("rerun should pass, code=%d err=%s", code, secondErr.String())
	}
	second := decodeKnowledgeImportCLIReport(t, secondOut.String())
	if first.Imported != 1 || second.Imported != 1 || len(first.Items) != 1 || len(second.Items) != 1 || first.Items[0].StagingID != second.Items[0].StagingID || second.Promoted != 1 {
		t.Fatalf("unexpected rerun reports: first=%+v second=%+v", first, second)
	}
	staging, err := store.RecentStagingItems(context.Background(), l1sqlite.L1StagingStatusValidated, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(staging) != 1 || staging[0].ID != first.Items[0].StagingID {
		t.Fatalf("rerun must retain one staging row: %+v", staging)
	}
	knowledge, err := store.RecentKnowledgeItems(context.Background(), "bookmarks", 10)
	if err != nil {
		t.Fatalf("RecentKnowledgeItems failed: %v", err)
	}
	if len(knowledge) != 1 || knowledge[0].StagingID != first.Items[0].StagingID {
		t.Fatalf("rerun must retain one knowledge row: %+v", knowledge)
	}
}

func TestRunKnowledgeCommandIndexWiki(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	repoRoot := t.TempDir()
	wikiDir := filepath.Join(repoRoot, "docs", "wiki", "concepts")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wikiDir, "source-registry.md"), []byte(`---
type: concept
status: active
owner: core
canonical_source: docs/10_新仕様/09_Memory_SourceRegistry仕様.md
source:
  - docs/10_新仕様/09_Memory_SourceRegistry仕様.md
related:
  - docs/wiki/concepts/memory-lifecycle.md
updated: 2026-06-25
---

# Source Registry

Source Registry は外部 source の登録と検証境界。
`), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	var out, errOut bytes.Buffer

	code := runKnowledgeCommand([]string{"index-wiki", filepath.Join(repoRoot, "docs", "wiki"), "--repo-root", repoRoot, "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("index-wiki should pass, code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"indexed":1`) {
		t.Fatalf("expected json result, got %s", out.String())
	}
	results, err := store.SearchWikiPageIndex(context.Background(), "Source Registry", 10)
	if err != nil {
		t.Fatalf("SearchWikiPageIndex failed: %v", err)
	}
	if len(results) != 1 || results[0].PageID != "concept:source-registry" {
		t.Fatalf("unexpected wiki index results: %+v", results)
	}
}

func TestRunKnowledgeCommandRelationsBuildDefaultsToDryRun(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	var out, errOut bytes.Buffer
	code := runKnowledgeCommand([]string{"relations", "build", "--domain", "all", "--limit", "100", "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("relations build failed: code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"dry_run":true`) || !strings.Contains(out.String(), `"status":"completed"`) {
		t.Fatalf("unexpected report: %s", out.String())
	}
}
