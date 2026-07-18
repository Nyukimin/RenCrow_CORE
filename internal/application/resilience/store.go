// Package resilience は、CORE停止事故の台帳、修復状態、証拠保持期間を管理する。
package resilience

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Status string

const (
	StatusUnresolved       Status = "unresolved"
	StatusRestartRecovered Status = "restart_recovered"
	StatusRepairRequested  Status = "repair_requested"
	StatusRepairPending    Status = "repair_completed_pending_verification"
	StatusRepairFailed     Status = "repair_failed"
	StatusResolved         Status = "resolved"
)

const (
	DefaultMaxRepairAttempts = 2
	DefaultVerificationAge   = 24 * time.Hour
	DefaultDetailRetention   = 7 * 24 * time.Hour
	DefaultMetadataRetention = 30 * 24 * time.Hour
)

type Incident struct {
	Signature         string            `json:"signature"`
	Kind              string            `json:"kind"`
	Status            Status            `json:"status"`
	FirstSeen         time.Time         `json:"first_seen"`
	LastSeen          time.Time         `json:"last_seen"`
	OccurrenceCount   int               `json:"occurrence_count"`
	Details           map[string]string `json:"details,omitempty"`
	RepairJobID       string            `json:"repair_job_id,omitempty"`
	RepairAttempts    int               `json:"repair_attempts"`
	LastRepairProbeAt *time.Time        `json:"last_repair_probe_at,omitempty"`
	RepairRequestedAt *time.Time        `json:"repair_requested_at,omitempty"`
	RepairCompletedAt *time.Time        `json:"repair_completed_at,omitempty"`
	ResolvedAt        *time.Time        `json:"resolved_at,omitempty"`
	DetailsPrunedAt   *time.Time        `json:"details_pruned_at,omitempty"`
	LastError         string            `json:"last_error,omitempty"`
}

type Observation struct {
	SignatureSource string
	Kind            string
	At              time.Time
	Details         map[string]string
}

type Store struct{ Root string }

func Signature(source string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(source)))
	return hex.EncodeToString(sum[:12])
}

func (s Store) Capture(obs Observation) (*Incident, error) {
	if obs.At.IsZero() {
		obs.At = time.Now().UTC()
	}
	signature := Signature(obs.SignatureSource)
	if strings.TrimSpace(obs.SignatureSource) == "" {
		return nil, errors.New("signature source is required")
	}
	release, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer release()
	incident, err := s.Load(signature)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if incident == nil {
		incident = &Incident{
			Signature: signature, Kind: strings.TrimSpace(obs.Kind), Status: StatusUnresolved,
			FirstSeen: obs.At, LastSeen: obs.At, OccurrenceCount: 1,
		}
	} else {
		incident.LastSeen = obs.At
		incident.OccurrenceCount++
		if incident.Status == StatusResolved || incident.Status == StatusRepairPending {
			incident.Status = StatusUnresolved
			incident.ResolvedAt = nil
			incident.DetailsPrunedAt = nil
			incident.LastError = "same incident signature recurred"
		}
	}
	incident.Details = cloneMap(obs.Details)
	if incident.Kind == "" {
		incident.Kind = "unknown"
	}
	if err := s.saveUnlocked(incident); err != nil {
		return nil, err
	}
	return incident, nil
}

func (s Store) Load(signature string) (*Incident, error) {
	b, err := os.ReadFile(s.metadataPath(signature))
	if err != nil {
		return nil, err
	}
	var incident Incident
	if err := json.Unmarshal(b, &incident); err != nil {
		return nil, fmt.Errorf("decode incident %s: %w", signature, err)
	}
	return &incident, nil
}

func (s Store) Save(incident *Incident) error {
	release, err := s.lock()
	if err != nil {
		return err
	}
	defer release()
	return s.saveUnlocked(incident)
}

func (s Store) saveUnlocked(incident *Incident) error {
	if incident == nil || incident.Signature == "" {
		return errors.New("incident signature is required")
	}
	dir := s.IncidentDir(incident.Signature)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(incident, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp := s.metadataPath(incident.Signature) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.metadataPath(incident.Signature))
}

func (s Store) List() ([]*Incident, error) {
	entries, err := os.ReadDir(filepath.Join(s.Root, "incidents"))
	if errors.Is(err, os.ErrNotExist) {
		return []*Incident{}, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Incident
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		incident, loadErr := s.Load(entry.Name())
		if loadErr == nil {
			out = append(out, incident)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out, nil
}

func (s Store) MarkRestartRecovered(incident *Incident, at time.Time) error {
	return s.update(incident, func(current *Incident) error {
		if current.Status == StatusUnresolved {
			current.Status = StatusRestartRecovered
		}
		return nil
	})
}

func (s Store) MarkRepairRequested(incident *Incident, jobID string, at time.Time, maxAttempts int) error {
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxRepairAttempts
	}
	return s.update(incident, func(current *Incident) error {
		if current.RepairAttempts >= maxAttempts {
			return fmt.Errorf("automatic repair attempt limit reached: %d", maxAttempts)
		}
		current.Status = StatusRepairRequested
		current.RepairAttempts++
		current.RepairJobID = jobID
		current.RepairRequestedAt = timePtr(at)
		current.LastError = ""
		return nil
	})
}

func (s Store) MarkRepairProbe(incident *Incident, at time.Time, message string) error {
	return s.update(incident, func(current *Incident) error {
		current.LastRepairProbeAt = timePtr(at)
		current.LastError = strings.TrimSpace(message)
		return nil
	})
}

func (s Store) MarkRepairCompleted(incident *Incident, at time.Time) error {
	return s.update(incident, func(current *Incident) error {
		current.Status = StatusRepairPending
		current.RepairCompletedAt = timePtr(at)
		current.LastError = ""
		return nil
	})
}

func (s Store) MarkRepairFailed(incident *Incident, message string) error {
	return s.update(incident, func(current *Incident) error {
		current.Status = StatusRepairFailed
		current.LastError = strings.TrimSpace(message)
		return nil
	})
}

func (s Store) ResolveStable(incident *Incident, now time.Time, verificationAge time.Duration) (bool, error) {
	if verificationAge <= 0 {
		verificationAge = DefaultVerificationAge
	}
	resolved := false
	err := s.update(incident, func(current *Incident) error {
		if current.Status != StatusRepairPending || current.RepairCompletedAt == nil {
			return nil
		}
		if current.LastSeen.After(*current.RepairCompletedAt) || now.Before(current.RepairCompletedAt.Add(verificationAge)) {
			return nil
		}
		current.Status = StatusResolved
		current.ResolvedAt = timePtr(now)
		resolved = true
		return nil
	})
	return resolved, err
}

func (s Store) SetLastError(incident *Incident, message string) error {
	return s.update(incident, func(current *Incident) error {
		current.LastError = strings.TrimSpace(message)
		return nil
	})
}

type GCResult struct {
	PrunedDetails []string `json:"pruned_details"`
	Deleted       []string `json:"deleted"`
}

func (s Store) GC(now time.Time, detailRetention, metadataRetention time.Duration) (GCResult, error) {
	if detailRetention <= 0 {
		detailRetention = DefaultDetailRetention
	}
	if metadataRetention <= 0 {
		metadataRetention = DefaultMetadataRetention
	}
	release, err := s.lock()
	if err != nil {
		return GCResult{}, err
	}
	defer release()
	incidents, err := s.List()
	if err != nil {
		return GCResult{}, err
	}
	var result GCResult
	for _, incident := range incidents {
		// 未解決事故の証拠は期間に関係なく削除しない。
		if incident.Status != StatusResolved || incident.ResolvedAt == nil {
			continue
		}
		if now.Sub(*incident.ResolvedAt) >= metadataRetention {
			if err := os.RemoveAll(s.IncidentDir(incident.Signature)); err != nil {
				return result, err
			}
			result.Deleted = append(result.Deleted, incident.Signature)
			continue
		}
		if incident.DetailsPrunedAt == nil && now.Sub(*incident.ResolvedAt) >= detailRetention {
			entries, readErr := os.ReadDir(s.IncidentDir(incident.Signature))
			if readErr != nil {
				return result, readErr
			}
			for _, entry := range entries {
				if entry.Name() == "incident.json" {
					continue
				}
				if err := os.RemoveAll(filepath.Join(s.IncidentDir(incident.Signature), entry.Name())); err != nil {
					return result, err
				}
			}
			incident.DetailsPrunedAt = timePtr(now)
			if err := s.saveUnlocked(incident); err != nil {
				return result, err
			}
			result.PrunedDetails = append(result.PrunedDetails, incident.Signature)
		}
	}
	return result, nil
}

func (s Store) IncidentDir(signature string) string {
	return filepath.Join(s.Root, "incidents", filepath.Base(signature))
}

func (s Store) metadataPath(signature string) string {
	return filepath.Join(s.IncidentDir(signature), "incident.json")
}

func (s Store) update(incident *Incident, mutate func(*Incident) error) error {
	if incident == nil || incident.Signature == "" {
		return errors.New("incident signature is required")
	}
	release, err := s.lock()
	if err != nil {
		return err
	}
	defer release()
	current, err := s.Load(incident.Signature)
	if err != nil {
		return err
	}
	if err := mutate(current); err != nil {
		return err
	}
	if err := s.saveUnlocked(current); err != nil {
		return err
	}
	*incident = *current
	return nil
}

func (s Store) lock() (func(), error) {
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Root, ".ledger.lock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := os.Mkdir(path, 0o700)
		if err == nil {
			return func() { _ = os.RemoveAll(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > 2*time.Minute {
			_ = os.RemoveAll(path)
			continue
		}
		if time.Now().After(deadline) {
			return nil, errors.New("resilience ledger is busy")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func timePtr(v time.Time) *time.Time { return &v }

func cloneMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
