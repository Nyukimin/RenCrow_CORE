// Package pronunciationcheck decides when the pronunciation Tool may use the GPU.
package pronunciationcheck

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type GPUSnapshot struct {
	Index              int    `json:"index"`
	Name               string `json:"name"`
	FreeMB             int    `json:"free_mb"`
	UtilizationPercent int    `json:"utilization_percent"`
}

type GPUProbe interface {
	Snapshot(ctx context.Context, nameMatch string) (GPUSnapshot, error)
}

type ToolReport struct {
	Date      string `json:"date"`
	StartedAt string `json:"started_at"`
	Total     int    `json:"total"`
	Passed    int    `json:"passed"`
	Failed    int    `json:"failed"`
}

type Tool interface {
	Run(ctx context.Context) (ToolReport, error)
}

type Config struct {
	GPUMatch              string
	MinFreeMB             int
	MaxUtilizationPercent int
	IdleSamples           int
	SampleInterval        time.Duration
	RetryAfter            time.Duration
}

type DeferredError struct {
	RetryAfter time.Duration
	Reason     string
}

func (e *DeferredError) Error() string {
	return e.Reason
}

type Service struct {
	probe GPUProbe
	tool  Tool
	cfg   Config
	sleep func(context.Context, time.Duration) error
}

func NewService(probe GPUProbe, tool Tool, cfg Config) *Service {
	if cfg.IdleSamples <= 0 {
		cfg.IdleSamples = 3
	}
	if cfg.SampleInterval <= 0 {
		cfg.SampleInterval = 2 * time.Second
	}
	if cfg.RetryAfter <= 0 {
		cfg.RetryAfter = 5 * time.Minute
	}
	return &Service{probe: probe, tool: tool, cfg: cfg, sleep: sleepContext}
}

func (s *Service) WithSleep(sleep func(context.Context, time.Duration) error) *Service {
	if sleep != nil {
		s.sleep = sleep
	}
	return s
}

func (s *Service) Run(ctx context.Context) (ToolReport, error) {
	if s == nil || s.probe == nil || s.tool == nil {
		return ToolReport{}, fmt.Errorf("pronunciation check dependencies are unavailable")
	}
	for sample := 0; sample < s.cfg.IdleSamples; sample++ {
		snapshot, err := s.probe.Snapshot(ctx, s.cfg.GPUMatch)
		if err != nil {
			return ToolReport{}, fmt.Errorf("query pronunciation GPU: %w", err)
		}
		if snapshot.FreeMB < s.cfg.MinFreeMB || snapshot.UtilizationPercent > s.cfg.MaxUtilizationPercent {
			reason := fmt.Sprintf(
				"GPU %s is busy: free_mb=%d utilization_percent=%d",
				strings.TrimSpace(snapshot.Name), snapshot.FreeMB, snapshot.UtilizationPercent,
			)
			return ToolReport{}, &DeferredError{RetryAfter: s.cfg.RetryAfter, Reason: reason}
		}
		if sample+1 < s.cfg.IdleSamples {
			if err := s.sleep(ctx, s.cfg.SampleInterval); err != nil {
				return ToolReport{}, err
			}
		}
	}
	report, err := s.tool.Run(ctx)
	if err != nil {
		return ToolReport{}, fmt.Errorf("run pronunciation Tool: %w", err)
	}
	return report, nil
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
