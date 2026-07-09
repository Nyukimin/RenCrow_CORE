package complexity

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
)

type JSONLStore struct {
	scanPath     string
	hotspotPath  string
	evidencePath string
	reportPath   string
}

func NewJSONLStore(root string) *JSONLStore {
	if root == "" {
		root = "workspace/logs/complexity_hotspot"
	}
	return &JSONLStore{
		scanPath:     filepath.Join(root, "complexity_scan_event.jsonl"),
		hotspotPath:  filepath.Join(root, "complexity_hotspot.jsonl"),
		evidencePath: filepath.Join(root, "complexity_hotspot_evidence.jsonl"),
		reportPath:   filepath.Join(root, "complexity_report_artifact.jsonl"),
	}
}

func (s *JSONLStore) SaveScanEvent(_ context.Context, item domaincomplexity.ScanEvent) error {
	if err := domaincomplexity.ValidateScanEvent(item); err != nil {
		return err
	}
	return appendJSONL(s.scanPath, item)
}

func (s *JSONLStore) ListScanEvents(_ context.Context, limit int) ([]domaincomplexity.ScanEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaincomplexity.ScanEvent
	if err := readJSONL(s.scanPath, func(line []byte) error {
		var item domaincomplexity.ScanEvent
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseUniqueLimit(items, limit, func(item domaincomplexity.ScanEvent) string { return item.ScanID }), nil
}

func (s *JSONLStore) SaveHotspot(_ context.Context, item domaincomplexity.Hotspot) error {
	if err := domaincomplexity.ValidateHotspot(item); err != nil {
		return err
	}
	return appendJSONL(s.hotspotPath, item)
}

func (s *JSONLStore) ListHotspots(_ context.Context, limit int) ([]domaincomplexity.Hotspot, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaincomplexity.Hotspot
	if err := readJSONL(s.hotspotPath, func(line []byte) error {
		var item domaincomplexity.Hotspot
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseUniqueLimit(items, limit, func(item domaincomplexity.Hotspot) string { return item.HotspotID }), nil
}

func (s *JSONLStore) SaveHotspotEvidence(_ context.Context, item domaincomplexity.HotspotEvidence) error {
	if err := domaincomplexity.ValidateHotspotEvidence(item); err != nil {
		return err
	}
	return appendJSONL(s.evidencePath, item)
}

func (s *JSONLStore) ListHotspotEvidence(_ context.Context, limit int) ([]domaincomplexity.HotspotEvidence, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaincomplexity.HotspotEvidence
	if err := readJSONL(s.evidencePath, func(line []byte) error {
		var item domaincomplexity.HotspotEvidence
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseUniqueLimit(items, limit, func(item domaincomplexity.HotspotEvidence) string { return item.EvidenceID }), nil
}

func (s *JSONLStore) SaveReportArtifact(_ context.Context, item domaincomplexity.ReportArtifact) error {
	if err := domaincomplexity.ValidateReportArtifact(item); err != nil {
		return err
	}
	return appendJSONL(s.reportPath, item)
}

func (s *JSONLStore) ListReportArtifacts(_ context.Context, limit int) ([]domaincomplexity.ReportArtifact, error) {
	if limit <= 0 {
		limit = 50
	}
	var items []domaincomplexity.ReportArtifact
	if err := readJSONL(s.reportPath, func(line []byte) error {
		var item domaincomplexity.ReportArtifact
		if err := json.Unmarshal(line, &item); err != nil {
			return err
		}
		items = append(items, item)
		return nil
	}); err != nil {
		return nil, err
	}
	return reverseUniqueLimit(items, limit, func(item domaincomplexity.ReportArtifact) string { return item.ArtifactID }), nil
}

func appendJSONL(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func readJSONL(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if err := fn(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func reverseLimit[T any](items []T, limit int) []T {
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	out := make([]T, 0, limit)
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, items[i])
	}
	return out
}

func reverseUniqueLimit[T any](items []T, limit int, key func(T) string) []T {
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	seen := map[string]struct{}{}
	out := make([]T, 0, limit)
	for i := len(items) - 1; i >= 0 && len(out) < limit; i-- {
		id := key(items[i])
		if id != "" {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
		}
		out = append(out, items[i])
	}
	return out
}
