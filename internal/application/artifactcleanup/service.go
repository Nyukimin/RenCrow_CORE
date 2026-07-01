package artifactcleanup

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Request struct {
	Paths       []string `json:"paths"`
	MaxAgeHours int      `json:"max_age_hours,omitempty"`
	DryRun      bool     `json:"dry_run"`
	RequestedBy string   `json:"requested_by,omitempty"`
}

type Candidate struct {
	Path       string `json:"path"`
	Reason     string `json:"reason"`
	Quarantine string `json:"quarantine,omitempty"`
}

type Report struct {
	Status      string      `json:"status"`
	DryRun      bool        `json:"dry_run"`
	Scanned     int         `json:"scanned"`
	Candidates  []Candidate `json:"candidates"`
	Quarantined int         `json:"quarantined"`
	AuditPath   string      `json:"audit_path"`
	CheckedAt   time.Time   `json:"checked_at"`
}

type Service struct {
	workspaceRoot string
	auditPath     string
	now           func() time.Time
}

func NewService(workspaceRoot, auditPath string) *Service {
	return &Service{workspaceRoot: workspaceRoot, auditPath: auditPath, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) Cleanup(ctx context.Context, req Request) (Report, error) {
	if s == nil {
		return Report{}, fmt.Errorf("artifact cleanup service unavailable")
	}
	root, err := filepath.Abs(filepath.Clean(s.workspaceRoot))
	if err != nil || root == "" {
		return Report{}, fmt.Errorf("workspace root is required")
	}
	now := s.now().UTC()
	report := Report{Status: "ok", DryRun: req.DryRun, AuditPath: s.auditPath, CheckedAt: now}
	maxAge := time.Duration(req.MaxAgeHours) * time.Hour
	if maxAge <= 0 {
		maxAge = 24 * time.Hour
	}
	for _, raw := range req.Paths {
		select {
		case <-ctx.Done():
			return report, ctx.Err()
		default:
		}
		rel, abs, err := resolveWorkspacePath(root, raw)
		if err != nil {
			return Report{}, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			continue
		}
		report.Scanned++
		if info.IsDir() || now.Sub(info.ModTime()) < maxAge || !looksTemporaryArtifact(rel) {
			continue
		}
		candidate := Candidate{Path: rel, Reason: "stale temporary artifact"}
		if !req.DryRun {
			qRel := filepath.ToSlash(filepath.Join("quarantine", "artifact_cleanup", now.Format("20060102T150405Z"), rel))
			qAbs := filepath.Join(root, qRel)
			if err := os.MkdirAll(filepath.Dir(qAbs), 0o755); err != nil {
				return Report{}, err
			}
			if err := os.Rename(abs, qAbs); err != nil {
				return Report{}, err
			}
			candidate.Quarantine = qRel
			report.Quarantined++
		}
		report.Candidates = append(report.Candidates, candidate)
	}
	if err := s.writeAudit(report, req); err != nil {
		return Report{}, err
	}
	return report, nil
}

func resolveWorkspacePath(root, raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || filepath.IsAbs(raw) {
		return "", "", fmt.Errorf("path must be relative")
	}
	clean := filepath.Clean(raw)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", "", fmt.Errorf("path must stay inside workspace")
	}
	abs := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", fmt.Errorf("path must stay inside workspace")
	}
	return filepath.ToSlash(rel), abs, nil
}

func looksTemporaryArtifact(path string) bool {
	lower := strings.ToLower(filepath.ToSlash(path))
	return strings.Contains(lower, "/tmp/") || strings.Contains(lower, "reindex") || strings.Contains(lower, "cache") || strings.HasSuffix(lower, ".tmp") || strings.HasSuffix(lower, ".partial")
}

func (s *Service) writeAudit(report Report, req Request) error {
	if strings.TrimSpace(s.auditPath) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.auditPath), 0o755); err != nil {
		return err
	}
	row := map[string]any{"report": report, "requested_by": strings.TrimSpace(req.RequestedBy)}
	data, err := json.Marshal(row)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.auditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}
