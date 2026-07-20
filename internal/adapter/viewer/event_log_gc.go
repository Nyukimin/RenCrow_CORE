package viewer

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type EventLogGCService struct {
	store      *EventLogStore
	reportPath string
	retention  time.Duration
	interval   time.Duration
	stopCh     chan struct{}
	doneCh     chan struct{}
	startOnce  sync.Once
	stopOnce   sync.Once
}

type EventGCReport struct {
	StartedAt           string `json:"started_at"`
	FinishedAt          string `json:"finished_at"`
	SourcePath          string `json:"source_path"`
	RetentionDays       int    `json:"retention_days"`
	BeforeCount         int    `json:"before_count"`
	AfterCount          int    `json:"after_count"`
	DeletedCount        int    `json:"deleted_count"`
	DecodeErrorCount    int    `json:"decode_error_count,omitempty"`
	TimestampErrorCount int    `json:"timestamp_error_count,omitempty"`
	CompressedCount     int    `json:"compressed_count,omitempty"`
	CompressedPath      string `json:"compressed_path,omitempty"`
	QuarantineCount     int    `json:"quarantine_count,omitempty"`
	QuarantinePath      string `json:"quarantine_path,omitempty"`
	Status              string `json:"status"`
	Error               string `json:"error,omitempty"`
}

func NewEventLogGCService(store *EventLogStore, reportPath string, retentionDays, intervalMinutes int) (*EventLogGCService, error) {
	if store == nil {
		return nil, fmt.Errorf("store is required")
	}
	if retentionDays < 1 {
		return nil, fmt.Errorf("retentionDays must be >= 1")
	}
	if intervalMinutes < 1 {
		return nil, fmt.Errorf("intervalMinutes must be >= 1")
	}
	dir := filepath.Dir(reportPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create gc report dir: %w", err)
	}
	f, err := os.OpenFile(reportPath, os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("touch gc report file: %w", err)
	}
	_ = f.Close()
	return &EventLogGCService{
		store:      store,
		reportPath: reportPath,
		retention:  time.Duration(retentionDays) * 24 * time.Hour,
		interval:   time.Duration(intervalMinutes) * time.Minute,
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}, nil
}

func (s *EventLogGCService) Start() {
	s.startOnce.Do(func() {
		go func() {
			defer close(s.doneCh)
			ticker := time.NewTicker(s.interval)
			defer ticker.Stop()
			_, _ = s.RunOnce(context.Background(), time.Now().UTC())
			for {
				select {
				case <-ticker.C:
					_, _ = s.RunOnce(context.Background(), time.Now().UTC())
				case <-s.stopCh:
					return
				}
			}
		}()
	})
}

func (s *EventLogGCService) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopCh)
	})
	<-s.doneCh
}

func (s *EventLogGCService) RunOnce(_ context.Context, now time.Time) (EventGCReport, error) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	startedAt := now.UTC().Format(time.RFC3339)
	report := EventGCReport{
		StartedAt:     startedAt,
		SourcePath:    s.store.path,
		RetentionDays: int(s.retention / (24 * time.Hour)),
		Status:        "ok",
	}

	src, err := os.Open(s.store.path)
	if err != nil {
		report.Status = "error"
		report.Error = err.Error()
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendGCReport(s.reportPath, report)
		return report, fmt.Errorf("open source: %w", err)
	}
	defer src.Close()
	tmpPath := s.store.path + ".tmp"
	tmp, err := os.Create(tmpPath)
	if err != nil {
		report.Status = "error"
		report.Error = err.Error()
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendGCReport(s.reportPath, report)
		return report, fmt.Errorf("create temp: %w", err)
	}

	cutoff := now.Add(-s.retention)
	expiredArchive := newCompressedLineArchive(compressedEventLogArchivePath(s.store.path, "expired", now))
	quarantineArchive := newCompressedLineArchive(compressedEventLogArchivePath(s.store.path, "quarantine", now))
	sc := bufio.NewScanner(src)
	enc := json.NewEncoder(tmp)
	for sc.Scan() {
		report.BeforeCount++
		rawLine := append([]byte(nil), sc.Bytes()...)
		var ev orchestrator.OrchestratorEvent
		if err := json.Unmarshal(rawLine, &ev); err != nil {
			report.DecodeErrorCount++
			if err := quarantineArchive.WriteLine(rawLine); err != nil {
				_ = tmp.Close()
				_ = os.Remove(tmpPath)
				report.Status = "error"
				report.Error = err.Error()
				report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
				_ = appendGCReport(s.reportPath, report)
				return report, fmt.Errorf("compress invalid event log line: %w", err)
			}
			continue
		}
		ts, err := time.Parse(time.RFC3339, ev.Timestamp)
		if err != nil {
			report.TimestampErrorCount++
			if err := quarantineArchive.WriteLine(rawLine); err != nil {
				_ = tmp.Close()
				_ = os.Remove(tmpPath)
				report.Status = "error"
				report.Error = err.Error()
				report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
				_ = appendGCReport(s.reportPath, report)
				return report, fmt.Errorf("compress timestamp-invalid event log line: %w", err)
			}
			continue
		}
		if ts.Before(cutoff) {
			report.DeletedCount++
			if err := expiredArchive.WriteLine(rawLine); err != nil {
				_ = tmp.Close()
				_ = os.Remove(tmpPath)
				report.Status = "error"
				report.Error = err.Error()
				report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
				_ = appendGCReport(s.reportPath, report)
				return report, fmt.Errorf("compress expired event log line: %w", err)
			}
			continue
		}
		if err := enc.Encode(ev); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			report.Status = "error"
			report.Error = err.Error()
			report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
			_ = appendGCReport(s.reportPath, report)
			return report, fmt.Errorf("encode temp: %w", err)
		}
		report.AfterCount++
	}
	if err := sc.Err(); err != nil {
		_ = src.Close()
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		report.Status = "error"
		report.Error = err.Error()
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendGCReport(s.reportPath, report)
		return report, fmt.Errorf("scan source: %w", err)
	}
	if err := src.Close(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		report.Status = "error"
		report.Error = err.Error()
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendGCReport(s.reportPath, report)
		return report, fmt.Errorf("close source: %w", err)
	}
	if err := expiredArchive.Close(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		report.Status = "error"
		report.Error = err.Error()
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendGCReport(s.reportPath, report)
		return report, fmt.Errorf("close expired archive: %w", err)
	}
	if err := quarantineArchive.Close(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		report.Status = "error"
		report.Error = err.Error()
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendGCReport(s.reportPath, report)
		return report, fmt.Errorf("close quarantine archive: %w", err)
	}
	report.CompressedCount = expiredArchive.Count()
	report.CompressedPath = expiredArchive.PathIfWritten()
	report.QuarantineCount = quarantineArchive.Count()
	report.QuarantinePath = quarantineArchive.PathIfWritten()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		report.Status = "error"
		report.Error = err.Error()
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendGCReport(s.reportPath, report)
		return report, fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpPath, s.store.path); err != nil {
		_ = os.Remove(tmpPath)
		report.Status = "error"
		report.Error = err.Error()
		report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		_ = appendGCReport(s.reportPath, report)
		return report, fmt.Errorf("rename temp: %w", err)
	}

	if report.DecodeErrorCount > 0 || report.TimestampErrorCount > 0 {
		report.Status = "partial_error"
	}
	report.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	if err := appendGCReport(s.reportPath, report); err != nil {
		return report, err
	}
	return report, nil
}

type compressedLineArchive struct {
	path  string
	file  *os.File
	gzip  *gzip.Writer
	count int
}

func newCompressedLineArchive(path string) *compressedLineArchive {
	return &compressedLineArchive{path: path}
}

func (a *compressedLineArchive) WriteLine(line []byte) error {
	if a.gzip == nil {
		if err := os.MkdirAll(filepath.Dir(a.path), 0755); err != nil {
			return err
		}
		f, err := os.OpenFile(a.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		a.file = f
		a.gzip = gzip.NewWriter(f)
	}
	if _, err := a.gzip.Write(bytesWithNewline(line)); err != nil {
		return err
	}
	a.count++
	return nil
}

func (a *compressedLineArchive) Close() error {
	if a.gzip == nil {
		return nil
	}
	if err := a.gzip.Close(); err != nil {
		_ = a.file.Close()
		return err
	}
	return a.file.Close()
}

func (a *compressedLineArchive) Count() int {
	return a.count
}

func (a *compressedLineArchive) PathIfWritten() string {
	if a.count == 0 {
		return ""
	}
	return a.path
}

func bytesWithNewline(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		return line
	}
	out := make([]byte, 0, len(line)+1)
	out = append(out, line...)
	out = append(out, '\n')
	return out
}

func compressedEventLogArchivePath(sourcePath string, kind string, now time.Time) string {
	dir := filepath.Dir(sourcePath)
	base := strings.TrimSuffix(filepath.Base(sourcePath), filepath.Ext(sourcePath))
	stamp := now.UTC().Format("20060102T150405Z")
	return filepath.Join(dir, "archive", fmt.Sprintf("%s.%s.%s.jsonl.gz", base, kind, stamp))
}

func appendGCReport(path string, report EventGCReport) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open gc report: %w", err)
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(report); err != nil {
		return fmt.Errorf("encode gc report: %w", err)
	}
	return nil
}
