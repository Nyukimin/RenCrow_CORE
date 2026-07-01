package historyrepair

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type JSONLRepairRequest struct {
	Path        string `json:"path"`
	Reason      string `json:"reason,omitempty"`
	RequestedBy string `json:"requested_by,omitempty"`
}

type JSONLRepairReport struct {
	RepairID       string    `json:"repair_id"`
	Path           string    `json:"path"`
	Status         string    `json:"status"`
	ValidLines     int       `json:"valid_lines"`
	InvalidLines   int       `json:"invalid_lines"`
	RepairedPath   string    `json:"repaired_path,omitempty"`
	QuarantinePath string    `json:"quarantine_path,omitempty"`
	AuditPath      string    `json:"audit_path,omitempty"`
	Reason         string    `json:"reason,omitempty"`
	RequestedBy    string    `json:"requested_by,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type JSONLRepairService struct {
	workspace string
	auditPath string
	now       func() time.Time
}

func NewJSONLRepairService(workspace, auditPath string) *JSONLRepairService {
	if workspace == "" {
		workspace = "."
	}
	if auditPath == "" {
		auditPath = filepath.Join(workspace, "logs", "history_repair.jsonl")
	}
	return &JSONLRepairService{workspace: workspace, auditPath: auditPath, now: func() time.Time { return time.Now().UTC() }}
}

func (s *JSONLRepairService) WithNow(now func() time.Time) *JSONLRepairService {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *JSONLRepairService) RepairJSONL(ctx context.Context, req JSONLRepairRequest) (JSONLRepairReport, error) {
	_ = ctx
	target, err := s.safePath(req.Path)
	if err != nil {
		return JSONLRepairReport{}, err
	}
	now := s.now().UTC()
	report := JSONLRepairReport{
		RepairID:    "history_repair_" + now.Format("20060102150405.000000000"),
		Path:        target,
		Status:      "ok",
		AuditPath:   s.auditPath,
		Reason:      strings.TrimSpace(req.Reason),
		RequestedBy: strings.TrimSpace(req.RequestedBy),
		CreatedAt:   now,
	}
	in, err := os.Open(target)
	if err != nil {
		return JSONLRepairReport{}, err
	}
	defer in.Close()

	repairedPath := target + ".repaired.jsonl"
	quarantinePath := filepath.Join(filepath.Dir(target), "quarantine", filepath.Base(target)+"."+report.RepairID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(quarantinePath), 0o755); err != nil {
		return JSONLRepairReport{}, err
	}
	out, err := os.OpenFile(repairedPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return JSONLRepairReport{}, err
	}
	defer out.Close()
	quarantine, err := os.OpenFile(quarantinePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return JSONLRepairReport{}, err
	}
	defer quarantine.Close()

	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			report.InvalidLines++
			_, _ = fmt.Fprintln(quarantine, line)
			continue
		}
		report.ValidLines++
		_, _ = fmt.Fprintln(out, line)
	}
	if err := scanner.Err(); err != nil {
		return JSONLRepairReport{}, err
	}
	report.RepairedPath = repairedPath
	if report.InvalidLines > 0 {
		report.Status = "repaired"
		report.QuarantinePath = quarantinePath
	} else {
		_ = os.Remove(quarantinePath)
	}
	if err := appendAudit(report.AuditPath, report); err != nil {
		return JSONLRepairReport{}, err
	}
	return report, nil
}

func (s *JSONLRepairService) safePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(raw) {
		raw = filepath.Join(s.workspace, raw)
	}
	target, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	workspace, err := filepath.Abs(s.workspace)
	if err != nil {
		return "", err
	}
	if target != workspace && !strings.HasPrefix(target, workspace+string(os.PathSeparator)) {
		return "", fmt.Errorf("path outside workspace is not allowed")
	}
	return target, nil
}

func appendAudit(path string, report JSONLRepairReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = f.Write(append(encoded, '\n'))
	return err
}
