package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/storagehost"
)

type runtimeStorageHostClientResult struct {
	Client *storagehost.Client
	Remote bool
}

type runtimeStorageHostStartupDeps struct {
	readToken   func(string) (string, error)
	newClient   func(storagehost.ClientConfig) (*storagehost.Client, error)
	dialContext func(context.Context, string, string) (net.Conn, error)
	now         func() time.Time
	wait        func(context.Context, time.Duration) error
	backoff     func(int) time.Duration
}

var runtimeStorageHostOperationNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func newRuntimeStorageHostClient(ctx context.Context, cfg *config.Config, required []storagehost.OperationSpec) (runtimeStorageHostClientResult, error) {
	return newRuntimeStorageHostClientWithDeps(ctx, cfg, required, runtimeStorageHostStartupDeps{})
}

func newRuntimeStorageHostClientWithDeps(ctx context.Context, cfg *config.Config, required []storagehost.OperationSpec, deps runtimeStorageHostStartupDeps) (runtimeStorageHostClientResult, error) {
	if cfg == nil {
		return runtimeStorageHostClientResult{}, errors.New("runtime storage host config is required")
	}
	hostConfig := cfg.Storage.Host
	mode := strings.TrimSpace(hostConfig.Mode)
	if mode == "" || mode == config.StorageHostModeLocal {
		return runtimeStorageHostClientResult{}, nil
	}
	if mode != config.StorageHostModeRemote {
		return runtimeStorageHostClientResult{}, errors.New("storage.host.mode is invalid")
	}
	result := runtimeStorageHostClientResult{Remote: true}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	transport := strings.TrimSpace(hostConfig.Transport)
	if transport == "" {
		transport = config.StorageHostTransportTunnel
	}
	if transport != config.StorageHostTransportTunnel {
		return result, errors.New("storage.host.transport is unsupported")
	}
	if strings.TrimSpace(hostConfig.Endpoint) == "" {
		return result, errors.New("storage.host.endpoint is required")
	}
	timeout, err := runtimeStorageHostSecondsDuration(hostConfig.TimeoutSec)
	if err != nil {
		return result, errors.New("storage.host.timeout_sec must be a positive supported duration")
	}
	startupWait, err := runtimeStorageHostSecondsDuration(hostConfig.StartupWaitSec)
	if err != nil {
		return result, errors.New("storage.host.startup_wait_sec must be a positive supported duration")
	}
	normalizedRequired, err := validateRuntimeStorageHostRequiredOps(required)
	if err != nil {
		return result, err
	}

	deps = completeRuntimeStorageHostStartupDeps(deps)
	token, err := deps.readToken(hostConfig.TokenFile)
	if err != nil || token == "" {
		return result, errors.New("storage.host bearer token is unavailable")
	}
	clientConfig := storagehost.ClientConfig{
		Endpoint: hostConfig.Endpoint,
		Token:    token,
		Timeout:  timeout,
		HTTPClient: &http.Client{Transport: &http.Transport{
			DialContext: deps.dialContext,
		}, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
	client, err := deps.newClient(clientConfig)
	if err != nil || client == nil {
		return result, errors.New("storage host client construction failed")
	}

	deadline := deps.now().Add(startupWait)
	for attempt := 0; ; attempt++ {
		remaining := deadline.Sub(deps.now())
		if remaining <= 0 {
			return result, runtimeStorageHostStartupDeadlineError()
		}
		attemptCtx, cancel := context.WithTimeout(ctx, remaining)
		err := client.Handshake(attemptCtx)
		var info storagehost.ContractInfo
		if err == nil {
			info, err = client.Contract(attemptCtx)
		}
		cancel()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		if err == nil {
			if !deps.now().Before(deadline) {
				return result, runtimeStorageHostStartupDeadlineError()
			}
			if err := verifyRuntimeStorageHostRequiredOps(normalizedRequired, info); err != nil {
				return result, sanitizeRuntimeStorageHostStartupError(err)
			}
			return runtimeStorageHostClientResult{Client: client, Remote: true}, nil
		}
		if !isRetryableRuntimeStorageHostReadinessError(err) {
			return result, sanitizeRuntimeStorageHostStartupError(err)
		}
		remaining = deadline.Sub(deps.now())
		if remaining <= 0 {
			return result, runtimeStorageHostStartupDeadlineError()
		}
		delay := deps.backoff(attempt)
		if delay <= 0 {
			return result, errors.New("storage host retry backoff must be positive")
		}
		if delay > remaining {
			delay = remaining
		}
		if err := deps.wait(ctx, delay); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			return result, sanitizeRuntimeStorageHostStartupError(err)
		}
	}
}

func validateRuntimeStorageHostRequiredOps(required []storagehost.OperationSpec) ([]storagehost.OperationSpec, error) {
	if len(required) == 0 {
		return nil, errors.New("remote storage host required operation set must not be empty")
	}
	ordered := append([]storagehost.OperationSpec(nil), required...)
	for _, spec := range ordered {
		if !runtimeStorageHostOperationNamePattern.MatchString(spec.Group) || !runtimeStorageHostOperationNamePattern.MatchString(spec.Op) {
			return nil, errors.New("remote storage host required operation set contains an invalid spec")
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Group != ordered[j].Group {
			return ordered[i].Group < ordered[j].Group
		}
		return ordered[i].Op < ordered[j].Op
	})
	for i := 1; i < len(ordered); i++ {
		if ordered[i-1].Group == ordered[i].Group && ordered[i-1].Op == ordered[i].Op {
			return nil, errors.New("remote storage host required operation set contains a duplicate spec")
		}
	}
	return ordered, nil
}

func verifyRuntimeStorageHostRequiredOps(required []storagehost.OperationSpec, info storagehost.ContractInfo) error {
	if info.ContractVersion != storagehost.ContractVersion {
		return storagehost.NewError(storagehost.ErrorCodeContractMismatch, "storage host contract version does not match")
	}
	if info.Generation <= 0 {
		return storagehost.NewError(storagehost.ErrorCodeSchemaRejected, "storage host contract generation is invalid")
	}
	provided := make(map[string]storagehost.OperationSpec, len(info.Operations))
	for _, spec := range info.Operations {
		if !runtimeStorageHostOperationNamePattern.MatchString(spec.Group) || !runtimeStorageHostOperationNamePattern.MatchString(spec.Op) {
			return storagehost.NewError(storagehost.ErrorCodeSchemaRejected, "storage host operation spec is invalid")
		}
		key := runtimeStorageHostOperationKey(spec.Group, spec.Op)
		if _, exists := provided[key]; exists {
			return storagehost.NewError(storagehost.ErrorCodeSchemaRejected, "storage host operation set contains a duplicate spec")
		}
		provided[key] = spec
	}
	for _, requiredSpec := range required {
		providedSpec, exists := provided[runtimeStorageHostOperationKey(requiredSpec.Group, requiredSpec.Op)]
		if !exists {
			return storagehost.NewError(storagehost.ErrorCodeOperationUnsupported, "storage host does not provide a required operation")
		}
		if providedSpec.Mutating != requiredSpec.Mutating {
			return storagehost.NewError(storagehost.ErrorCodeContractMismatch, "storage host operation mutating contract does not match")
		}
	}
	return nil
}

func completeRuntimeStorageHostStartupDeps(deps runtimeStorageHostStartupDeps) runtimeStorageHostStartupDeps {
	if deps.readToken == nil {
		deps.readToken = config.ReadStorageHostBearerToken
	}
	if deps.dialContext == nil {
		dialer := &net.Dialer{}
		deps.dialContext = dialer.DialContext
	}
	if deps.newClient == nil {
		deps.newClient = storagehost.NewClient
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.wait == nil {
		deps.wait = waitRuntimeStorageHostStartup
	}
	if deps.backoff == nil {
		deps.backoff = runtimeStorageHostStartupBackoff
	}
	return deps
}

func runtimeStorageHostSecondsDuration(seconds int) (time.Duration, error) {
	if seconds <= 0 || int64(seconds) > (1<<63-1)/int64(time.Second) {
		return 0, errors.New("invalid duration")
	}
	return time.Duration(seconds) * time.Second, nil
}

func runtimeStorageHostOperationKey(group, operation string) string {
	return group + "\x00" + operation
}

func isRetryableRuntimeStorageHostReadinessError(err error) bool {
	var storageErr *storagehost.Error
	return errors.As(err, &storageErr) &&
		storageErr.Code == storagehost.ErrorCodeUnreachable &&
		storageErr.Message == "storage host did not answer"
}

func sanitizeRuntimeStorageHostStartupError(err error) error {
	var storageErr *storagehost.Error
	if errors.As(err, &storageErr) {
		return storagehost.NewError(storageErr.Code, "storage host startup readiness check failed")
	}
	return errors.New("storage host startup readiness check failed")
}

func runtimeStorageHostStartupDeadlineError() error {
	return storagehost.NewError(storagehost.ErrorCodeUnreachable, "storage host readiness deadline elapsed")
}

func waitRuntimeStorageHostStartup(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func runtimeStorageHostStartupBackoff(attempt int) time.Duration {
	const base = 100 * time.Millisecond
	const capDelay = 2 * time.Second
	delay := base
	for i := 0; i < attempt && delay < capDelay; i++ {
		delay *= 2
	}
	if delay > capDelay {
		return capDelay
	}
	return delay
}
