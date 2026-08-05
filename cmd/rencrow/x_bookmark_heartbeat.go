package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/heartbeat"
	knowledgeapp "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledge"
)

const xBookmarkReportMaxBytes = 1024 * 1024

type xBookmarkCollectionProcess interface {
	Collect(ctx context.Context, outputDir string) error
}

type xBookmarkHeartbeatRunner struct {
	process    xBookmarkCollectionProcess
	store      knowledgeapp.StagingStore
	outputRoot string
}

type xBookmarkCLIProcess struct {
	command    string
	maxScrolls int
}

func newXBookmarkHeartbeatRunner(process xBookmarkCollectionProcess, store knowledgeapp.StagingStore, outputRoot string) heartbeat.XBookmarkCollector {
	return &xBookmarkHeartbeatRunner{process: process, store: store, outputRoot: outputRoot}
}

func newXBookmarkCLIProcess(command string, maxScrolls int) xBookmarkCollectionProcess {
	return &xBookmarkCLIProcess{command: strings.TrimSpace(command), maxScrolls: maxScrolls}
}

func (p *xBookmarkCLIProcess) Collect(ctx context.Context, outputDir string) error {
	cmd := exec.CommandContext(ctx, p.command, xBookmarkCLIArgs(outputDir, p.maxScrolls)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("x bookmark CLI failed: %w", err)
	}
	return nil
}

type xBookmarkCLIReport struct {
	Status                 string `json:"status"`
	CollectedCount         int    `json:"collected_count"`
	ExternalFetchSucceeded int    `json:"external_fetch_succeeded"`
}

func (r *xBookmarkHeartbeatRunner) Collect(ctx context.Context) (heartbeat.XBookmarkCollectionReport, error) {
	if r == nil || r.process == nil {
		return heartbeat.XBookmarkCollectionReport{}, fmt.Errorf("x bookmark collection process is required")
	}
	if r.store == nil {
		return heartbeat.XBookmarkCollectionReport{}, fmt.Errorf("knowledge staging store is required")
	}
	if strings.TrimSpace(r.outputRoot) == "" || !filepath.IsAbs(r.outputRoot) {
		return heartbeat.XBookmarkCollectionReport{}, fmt.Errorf("x bookmark output root must be an absolute path")
	}
	if err := os.MkdirAll(r.outputRoot, 0o700); err != nil {
		return heartbeat.XBookmarkCollectionReport{}, fmt.Errorf("create x bookmark output root: %w", err)
	}
	outputDir, err := os.MkdirTemp(r.outputRoot, "run_")
	if err != nil {
		return heartbeat.XBookmarkCollectionReport{}, fmt.Errorf("create x bookmark run directory: %w", err)
	}
	if err := os.Chmod(outputDir, 0o700); err != nil {
		return heartbeat.XBookmarkCollectionReport{}, fmt.Errorf("protect x bookmark run directory: %w", err)
	}
	if err := r.process.Collect(ctx, outputDir); err != nil {
		return heartbeat.XBookmarkCollectionReport{}, err
	}

	report, err := readXBookmarkCLIReport(filepath.Join(outputDir, "report.json"))
	if err != nil {
		return heartbeat.XBookmarkCollectionReport{}, err
	}
	if report.Status != "completed" {
		return heartbeat.XBookmarkCollectionReport{}, fmt.Errorf("x bookmark report status is not completed")
	}
	corePath := filepath.Join(outputDir, "rencrow_core.jsonl")
	coreFile, err := os.Open(corePath)
	if err != nil {
		return heartbeat.XBookmarkCollectionReport{}, fmt.Errorf("open x bookmark CORE artifact: %w", err)
	}
	defer coreFile.Close()
	result, err := knowledgeapp.ImportKnowledgeCoreJSONL(ctx, r.store, coreFile, knowledgeapp.ImportOptions{})
	if err != nil {
		return heartbeat.XBookmarkCollectionReport{}, fmt.Errorf("import x bookmark CORE artifact: %w", err)
	}
	return heartbeat.XBookmarkCollectionReport{
		Collected:       report.CollectedCount,
		Imported:        result.Imported,
		ExternalFetched: report.ExternalFetchSucceeded,
	}, nil
}

func readXBookmarkCLIReport(path string) (xBookmarkCLIReport, error) {
	f, err := os.Open(path)
	if err != nil {
		return xBookmarkCLIReport{}, fmt.Errorf("open x bookmark report: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return xBookmarkCLIReport{}, fmt.Errorf("stat x bookmark report: %w", err)
	}
	if info.Size() > xBookmarkReportMaxBytes {
		return xBookmarkCLIReport{}, fmt.Errorf("x bookmark report exceeds size limit")
	}
	decoder := json.NewDecoder(io.LimitReader(f, xBookmarkReportMaxBytes))
	var report xBookmarkCLIReport
	if err := decoder.Decode(&report); err != nil {
		return xBookmarkCLIReport{}, fmt.Errorf("decode x bookmark report: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return xBookmarkCLIReport{}, fmt.Errorf("x bookmark report must contain one JSON object")
	}
	if report.CollectedCount < 0 || report.ExternalFetchSucceeded < 0 {
		return xBookmarkCLIReport{}, fmt.Errorf("x bookmark report contains negative counts")
	}
	return report, nil
}

func xBookmarkCLIArgs(outputDir string, maxScrolls int) []string {
	return []string{
		"--headless",
		"--output-dir", outputDir,
		"--max-scrolls", strconv.Itoa(maxScrolls),
	}
}
