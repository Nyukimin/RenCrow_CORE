package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
)

func loadRuntimeClassifierTestConfig() (classifierlab.Config, error) {
	return classifierlab.LoadConfigFromEnv()
}

func runtimeClassifierTerminationJSON(stats classifierlab.ShadowStats) (string, error) {
	stats.TurnsTotal = nil
	stats.TurnsTotalStatus = "unmeasured"
	data, err := json.Marshal(stats)
	if err != nil {
		return "", fmt.Errorf("encode classifier shadow termination stats: %w", err)
	}
	return string(data), nil
}

func buildRuntimeClassifier(provider domainllm.LLMProvider, prompt string, cfg classifierlab.Config) (domainagent.Classifier, *classifierlab.ShadowClassifier, context.CancelFunc, error) {
	primary := routing.NewLLMClassifier(provider, prompt)
	return wrapRuntimeClassifierWithFactories(
		primary,
		cfg,
		func(candidateConfig classifierlab.Config) (classifierlab.CandidateClassifier, error) {
			return classifierlab.NewHTTPCandidateClassifierFromEnv(candidateConfig)
		},
		newRuntimeClassifierJSONLRecorder,
	)
}

func wrapRuntimeClassifierWithFactories(
	primary domainagent.Classifier,
	cfg classifierlab.Config,
	candidateFactory classifierlab.CandidateFactory,
	recorderFactory classifierlab.RecorderFactory,
) (domainagent.Classifier, *classifierlab.ShadowClassifier, context.CancelFunc, error) {
	if cfg.Mode == classifierlab.ModeOff || primary == nil {
		wrapped, shadow, err := classifierlab.BuildClassifier(nil, primary, cfg, nil, nil)
		return wrapped, shadow, nil, err
	}
	serviceCtx, cancel := context.WithCancel(context.Background())
	wrapped, shadow, err := classifierlab.BuildClassifier(serviceCtx, primary, cfg, candidateFactory, recorderFactory)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	return wrapped, shadow, cancel, nil
}

type runtimeClassifierJSONLRecorder struct {
	async      *classifierlab.AsyncRecorder
	file       *os.File
	path       string
	closeOnce  sync.Once
	closeDone  chan struct{}
	closeErrMu sync.Mutex
	closeErr   error
}

type runtimeClassifierRecord struct {
	RequestID            string                                `json:"request_id"`
	CorrelationID        string                                `json:"correlation_id"`
	PrimaryDecision      classifierlab.PrimaryDecisionSnapshot `json:"primary_decision"`
	PrimaryError         string                                `json:"primary_error,omitempty"`
	PrimaryDurationNanos int64                                 `json:"primary_duration_nanos"`
	CandidateResult      *classifierlab.CandidateResult        `json:"candidate_result,omitempty"`
	CandidateError       string                                `json:"candidate_error,omitempty"`
	CandidateDurationNS  int64                                 `json:"candidate_duration_ns"`
}

func newRuntimeClassifierJSONLRecorder(cfg classifierlab.RecordingConfig) (classifierlab.ComparisonRecorder, error) {
	if strings.TrimSpace(cfg.Directory) == "" {
		return nil, errors.New("classifier shadow recording directory is empty")
	}
	if err := os.MkdirAll(cfg.Directory, 0o700); err != nil {
		return nil, fmt.Errorf("create classifier shadow recording directory: %w", err)
	}
	info, err := os.Stat(cfg.Directory)
	if err != nil {
		return nil, fmt.Errorf("inspect classifier shadow recording directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("classifier shadow recording path is not a directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("classifier shadow recording directory must be private (0700)")
	}
	file, err := os.CreateTemp(cfg.Directory, "routing-classifier-shadow-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("create classifier shadow JSONL: %w", err)
	}
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(file.Name())
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("restrict classifier shadow JSONL permissions: %w", err)
	}
	// Keep the recorder alive when classifier workers are canceled, so the
	// shadow wrapper can drain it after candidate workers have stopped.
	async, err := classifierlab.NewAsyncRecorder(context.Background(), cfg.OutputQueueCapacity, func(_ context.Context, record classifierlab.ComparisonRecord) error {
		data, err := json.Marshal(runtimeClassifierRecord{
			RequestID:            record.RequestID,
			CorrelationID:        record.CorrelationID,
			PrimaryDecision:      record.PrimaryDecision,
			PrimaryError:         record.PrimaryError,
			PrimaryDurationNanos: record.PrimaryDurationNanos,
			CandidateResult:      record.CandidateResult,
			CandidateError:       record.CandidateError,
			CandidateDurationNS:  record.CandidateDurationNS,
		})
		if err != nil {
			return fmt.Errorf("encode classifier shadow record: %w", err)
		}
		data = append(data, '\n')
		if _, err := file.Write(data); err != nil {
			return fmt.Errorf("write classifier shadow record: %w", err)
		}
		return nil
	})
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("start classifier shadow JSONL recorder: %w", err)
	}
	return &runtimeClassifierJSONLRecorder{
		async: async, file: file, path: file.Name(), closeDone: make(chan struct{}),
	}, nil
}

func (r *runtimeClassifierJSONLRecorder) TryRecord(record classifierlab.ComparisonRecord) bool {
	return r != nil && r.async != nil && r.async.TryRecord(record)
}

func (r *runtimeClassifierJSONLRecorder) Dropped() uint64 {
	if r == nil || r.async == nil {
		return 0
	}
	return r.async.Dropped()
}

func (r *runtimeClassifierJSONLRecorder) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.closeDone
}

func (r *runtimeClassifierJSONLRecorder) Close(ctx context.Context) error {
	if r == nil || r.async == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	r.closeOnce.Do(func() {
		if err := r.async.Close(ctx); err != nil {
			r.setCloseErr(err)
			go func() {
				_ = r.async.Close(context.Background())
				r.setCloseErrIfUnset(r.file.Close())
				close(r.closeDone)
			}()
			return
		}
		r.setCloseErr(r.file.Close())
		close(r.closeDone)
	})
	select {
	case <-r.closeDone:
		return r.getCloseErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *runtimeClassifierJSONLRecorder) setCloseErr(err error) {
	r.closeErrMu.Lock()
	r.closeErr = err
	r.closeErrMu.Unlock()
}

func (r *runtimeClassifierJSONLRecorder) setCloseErrIfUnset(err error) {
	r.closeErrMu.Lock()
	if r.closeErr == nil {
		r.closeErr = err
	}
	r.closeErrMu.Unlock()
}

func (r *runtimeClassifierJSONLRecorder) getCloseErr() error {
	r.closeErrMu.Lock()
	defer r.closeErrMu.Unlock()
	return r.closeErr
}
