package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	resilienceStackPlanSchema = "rencrow.launchd-stack/v1"
	resilienceStackPlanPath   = "/Library/Application Support/RenCrow/resilience-stack.v1.json"
	resilienceStackStatePath  = "/Library/Application Support/RenCrow/resilience-stack.state.json"
	resilienceStackHoldPath   = "/Library/Application Support/RenCrow/resilience-stack.maintenance"
	resilienceStackLockPath   = "/Library/Application Support/RenCrow/resilience-stack.lock"
	resilienceStackDomain     = "system"
	resilienceStackGUI501     = "gui/501"
	resilienceStackUser501    = "user/501"
	resilienceStackPollEvery  = 250 * time.Millisecond
)

type resilienceStackPlan struct {
	Schema   string                   `json:"schema"`
	Complete bool                     `json:"complete"`
	Services []resilienceStackService `json:"services"`
}

type resilienceStackService struct {
	Label        string                   `json:"label"`
	Domain       string                   `json:"domain"`
	PlistPath    string                   `json:"plist_path"`
	Dependencies []string                 `json:"dependencies,omitempty"`
	Readiness    resilienceStackReadiness `json:"readiness"`
}

type resilienceStackReadiness struct {
	Kind           string `json:"kind"`
	Address        string `json:"address"`
	Path           string `json:"path,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type resilienceStackSnapshot struct {
	Loaded   bool
	Disabled bool
	Running  bool
	PID      int
}

type resilienceStackResult struct {
	Action   string   `json:"action"`
	Reason   string   `json:"reason,omitempty"`
	Degraded []string `json:"degraded,omitempty"`
	Stopped  []string `json:"stopped,omitempty"`
	Started  []string `json:"started,omitempty"`
	CorePID  int      `json:"core_pid,omitempty"`
}

type resilienceStackOperator interface {
	ValidateTarget(context.Context, resilienceStackService) error
	Inspect(context.Context, resilienceStackService) (resilienceStackSnapshot, error)
	Readiness(context.Context, resilienceStackService) error
	Bootout(context.Context, resilienceStackService) error
	Bootstrap(context.Context, resilienceStackService) error
}

type resilienceStackState struct {
	CorePID           int    `json:"core_pid"`
	PendingPlanSHA256 string `json:"pending_plan_sha256,omitempty"`
}

type resilienceStackStateStore interface {
	Save(resilienceStackState) error
}

// The plan is root-owned deployment input. This allowlist limits launchctl to
// service labels already belonging to the RenCrow host; the manifest supplies
// the installed graph and readiness contract, so this is not a claim that the
// historical host transition list is the current complete graph.
var resilienceStackTargets = map[string]map[string]struct{}{
	"com.rencrow.core":                    {resilienceStackDomain: {}, resilienceStackGUI501: {}, resilienceStackUser501: {}},
	"com.rencrow.redis":                   {resilienceStackDomain: {}},
	"com.rencrow.qdrant":                  {resilienceStackDomain: {}},
	"com.rencrow.llm-gateway":             {resilienceStackDomain: {}},
	"com.rencrow.portal":                  {resilienceStackDomain: {}},
	"com.rencrow.board-gateway.public":    {resilienceStackDomain: {}, resilienceStackGUI501: {}},
	"com.rencrow.board-gateway.lan":       {resilienceStackDomain: {}, resilienceStackGUI501: {}},
	"com.rencrow.board-communication":     {resilienceStackDomain: {}, resilienceStackGUI501: {}},
	"com.rencrow.board-openai-mcp-tunnel": {resilienceStackDomain: {}, resilienceStackGUI501: {}},
	"com.rencrow.board-lumina-mcp":        {resilienceStackDomain: {}, resilienceStackGUI501: {}},
	"com.rencrow.tailnet-browser":         {resilienceStackDomain: {}, resilienceStackGUI501: {}},
	"com.rencrow.fxembed-daily":           {resilienceStackGUI501: {}},
	"com.rencrow.core-dependencies":       {resilienceStackDomain: {}},
	"com.rencrow.irodori-tts":             {resilienceStackDomain: {}},
	"com.rencrow.comfyui.housekeeping":    {resilienceStackDomain: {}},
	"com.rencrow.comfyui":                 {resilienceStackDomain: {}},
	"com.rencrow.ollama":                  {resilienceStackDomain: {}},
	"com.rencrow.backend-event-watch":     {resilienceStackDomain: {}},
	"com.rencrow.vision":                  {resilienceStackDomain: {}},
	"com.rencrow.person-related-catalog":  {resilienceStackDomain: {}},
	"com.rencrow.embed":                   {resilienceStackDomain: {}},
	"com.rencrow.stt":                     {resilienceStackDomain: {}},
	"com.rencrow.trade":                   {resilienceStackDomain: {}},
	"com.rencrow.whisper":                 {resilienceStackDomain: {}},
	"com.rencrow.musetalk":                {resilienceStackDomain: {}},
	"com.rencrow.mio-gemma4":              {resilienceStackDomain: {}},
	"com.rencrow.gitea":                   {resilienceStackDomain: {}},
	"com.rencrow.image":                   {resilienceStackDomain: {}},
	"com.rencrow.lyrics-collector":        {resilienceStackDomain: {}},
	"com.rencrow.games-observer":          {resilienceStackDomain: {}},
	"com.rencrow.llm-runtime-205":         {resilienceStackDomain: {}},
	"com.rencrow.llm-node":                {resilienceStackDomain: {}},
	"com.rencrow.telemetry":               {resilienceStackDomain: {}},
	"com.rencrow.log-rotate-guard":        {resilienceStackDomain: {}},
	"com.rencrow.movie-catalog":           {resilienceStackDomain: {}},
	"com.rencrow.llm-runtime-206":         {resilienceStackDomain: {}},
	"com.rencrow.tts":                     {resilienceStackDomain: {}},
	"com.rencrow.qwen36-wild":             {resilienceStackDomain: {}},
}

func validateResilienceStackPlan(plan resilienceStackPlan) error {
	if plan.Schema != resilienceStackPlanSchema {
		return fmt.Errorf("unsupported launchd stack plan schema %q", plan.Schema)
	}
	if !plan.Complete {
		return errors.New("launchd stack plan must explicitly declare a complete service graph")
	}
	if len(plan.Services) == 0 {
		return errors.New("launchd stack plan has no services")
	}
	seen := make(map[string]struct{}, len(plan.Services))
	for _, service := range plan.Services {
		allowedDomains, allowed := resilienceStackTargets[service.Label]
		if !allowed {
			return fmt.Errorf("launchd stack target %q is not allowlisted", service.Label)
		}
		if _, ok := allowedDomains[service.Domain]; !ok {
			return fmt.Errorf("launchd stack target %q is not allowed in domain %q", service.Label, service.Domain)
		}
		if _, duplicate := seen[service.Label]; duplicate {
			return fmt.Errorf("duplicate launchd stack target %q", service.Label)
		}
		seen[service.Label] = struct{}{}

		expectedPath, err := canonicalResilienceStackPlistPath(service.Domain, service.Label)
		if err != nil {
			return err
		}
		if service.PlistPath != expectedPath {
			return fmt.Errorf("launchd plist for %q must be %q", service.Label, expectedPath)
		}
		if err := validateResilienceStackReadiness(service); err != nil {
			return fmt.Errorf("invalid readiness for %q: %w", service.Label, err)
		}
		if service.Label == "com.rencrow.core" && (service.Readiness.Kind != "http" || service.Readiness.Address != "127.0.0.1:18790" || service.Readiness.Path != "/health/live") {
			return errors.New("CORE readiness must use loopback /health/live on port 18790")
		}
		if service.Label == "com.rencrow.portal" && (service.Readiness.Kind != "http" || service.Readiness.Address != "127.0.0.1:18791" || service.Readiness.Path != "/health/ready") {
			return errors.New("PORTAL readiness must use loopback /health/ready on port 18791")
		}

		dependencies := make(map[string]struct{}, len(service.Dependencies))
		for _, dependency := range service.Dependencies {
			if dependency == service.Label {
				return fmt.Errorf("launchd target %q depends on itself", service.Label)
			}
			if _, duplicate := dependencies[dependency]; duplicate {
				return fmt.Errorf("launchd target %q repeats dependency %q", service.Label, dependency)
			}
			dependencies[dependency] = struct{}{}
			if _, exists := seen[dependency]; !exists {
				return fmt.Errorf("dependency %q must appear before %q in the launchd stack plan", dependency, service.Label)
			}
		}
	}
	if _, ok := seen["com.rencrow.core"]; !ok {
		return errors.New("launchd stack plan must include com.rencrow.core")
	}
	return nil
}

func canonicalResilienceStackPlistPath(domain, label string) (string, error) {
	switch domain {
	case resilienceStackDomain:
		return filepath.Join("/Library/LaunchDaemons", label+".plist"), nil
	case resilienceStackGUI501, resilienceStackUser501:
		account, err := user.LookupId("501")
		if err != nil {
			return "", fmt.Errorf("resolve launchd %s owner: %w", domain, err)
		}
		if !filepath.IsAbs(account.HomeDir) {
			return "", errors.New("launchd gui/501 owner has no absolute home directory")
		}
		return filepath.Join(account.HomeDir, "Library", "LaunchAgents", label+".plist"), nil
	default:
		return "", fmt.Errorf("unsupported launchd domain %q", domain)
	}
}

func validateResilienceStackReadiness(service resilienceStackService) error {
	if service.Readiness.TimeoutSeconds < 1 || service.Readiness.TimeoutSeconds > 300 {
		return errors.New("timeout_seconds must be between 1 and 300")
	}
	host, portText, err := net.SplitHostPort(service.Readiness.Address)
	if err != nil {
		return fmt.Errorf("address must be a loopback host and port: %w", err)
	}
	parsedIP := net.ParseIP(host)
	if parsedIP == nil || !parsedIP.IsLoopback() {
		return errors.New("readiness address must use an IP loopback address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("readiness port is invalid")
	}
	switch service.Readiness.Kind {
	case "tcp":
		if service.Readiness.Path != "" {
			return errors.New("TCP readiness cannot include a path")
		}
	case "http":
		if !strings.HasPrefix(service.Readiness.Path, "/") || strings.ContainsAny(service.Readiness.Path, "?#\r\n") {
			return errors.New("HTTP readiness path must be an absolute local path")
		}
		if service.Readiness.Address != "127.0.0.1:"+portText && service.Readiness.Address != "[::1]:"+portText {
			return errors.New("HTTP readiness must use an explicit loopback address")
		}
	case "":
		return errors.New("readiness kind is required")
	default:
		return fmt.Errorf("unsupported readiness kind %q", service.Readiness.Kind)
	}
	return nil
}

func reconcileResilienceStack(ctx context.Context, plan resilienceStackPlan, uid int, maintenance bool, state resilienceStackState, operator resilienceStackOperator, stateStore resilienceStackStateStore) (resilienceStackResult, error) {
	if err := validateResilienceStackPlan(plan); err != nil {
		return resilienceStackResult{}, err
	}
	if maintenance {
		state.CorePID = 0
		if stateStore != nil {
			if err := stateStore.Save(state); err != nil {
				return resilienceStackResult{}, fmt.Errorf("clear stack PID baseline for maintenance: %w", err)
			}
		}
		return resilienceStackResult{Action: "noop", Reason: "maintenance"}, nil
	}
	for _, service := range plan.Services {
		if err := resilienceStackPermission(uid, service.Domain); err != nil {
			return resilienceStackResult{}, fmt.Errorf("preflight %s: %w", service.Label, err)
		}
	}

	for _, service := range plan.Services {
		if err := operator.ValidateTarget(ctx, service); err != nil {
			return resilienceStackResult{}, fmt.Errorf("preflight plist for %s: %w", service.Label, err)
		}
	}
	snapshots := make(map[string]resilienceStackSnapshot, len(plan.Services))
	maintenanceDetected := false
	for _, service := range plan.Services {
		snapshot, err := operator.Inspect(ctx, service)
		if err != nil {
			return resilienceStackResult{}, fmt.Errorf("inspect %s: %w", service.Label, err)
		}
		snapshots[service.Label] = snapshot
		maintenanceDetected = maintenanceDetected || snapshot.Disabled
	}
	if maintenanceDetected {
		state.CorePID = 0
		if stateStore != nil {
			if err := stateStore.Save(state); err != nil {
				return resilienceStackResult{}, fmt.Errorf("clear stack PID baseline for disabled launchd target: %w", err)
			}
		}
		return resilienceStackResult{Action: "noop", Reason: "launchd target disabled for maintenance"}, nil
	}

	planHash, err := resilienceStackPlanSHA256(plan)
	if err != nil {
		return resilienceStackResult{}, err
	}
	if state.PendingPlanSHA256 != "" {
		if stateStore == nil {
			return resilienceStackResult{}, errors.New("pending stack recovery requires a durable state store")
		}
		if state.PendingPlanSHA256 != planHash {
			return resilienceStackResult{}, errors.New("pending stack recovery belongs to a different launchd plan; preserving the receipt")
		}
		return resumeResilienceStackRecovery(ctx, plan, state, operator, stateStore)
	}

	corePID := 0
	coreHealthy := false
	degraded := make([]string, 0)
	for _, service := range plan.Services {
		snapshot := snapshots[service.Label]
		if service.Label == "com.rencrow.core" {
			corePID = snapshot.PID
		}
		if !snapshot.Loaded || !snapshot.Running || snapshot.PID <= 0 {
			degraded = append(degraded, service.Label)
			continue
		}
		if err := operator.Readiness(ctx, service); err != nil {
			degraded = append(degraded, service.Label)
			if service.Label == "com.rencrow.core" {
				coreHealthy = false
			}
		} else if service.Label == "com.rencrow.core" {
			coreHealthy = true
		}
	}
	if coreHealthy && state.CorePID != 0 && corePID == state.CorePID {
		reason := "healthy"
		if len(degraded) > 0 {
			reason = "CORE healthy; dependencies degraded"
		}
		return resilienceStackResult{Action: "noop", Reason: reason, Degraded: degraded, CorePID: corePID}, nil
	}
	if coreHealthy && state.CorePID == 0 {
		state.CorePID = corePID
		if stateStore == nil {
			return resilienceStackResult{}, errors.New("CORE PID baseline requires a durable state store")
		}
		if err := stateStore.Save(state); err != nil {
			return resilienceStackResult{}, fmt.Errorf("save initial CORE PID baseline: %w", err)
		}
		reason := "baseline established"
		if len(degraded) > 0 {
			reason = "baseline established; dependencies degraded"
		}
		return resilienceStackResult{Action: "noop", Reason: reason, Degraded: degraded, CorePID: corePID}, nil
	}

	if stateStore == nil {
		return resilienceStackResult{}, errors.New("stack recovery requires a durable state store")
	}
	state.PendingPlanSHA256 = planHash
	if err := stateStore.Save(state); err != nil {
		return resilienceStackResult{}, fmt.Errorf("save pending stack recovery receipt before stopping: %w", err)
	}
	result := resilienceStackResult{Action: "partial", Reason: "CORE loss or unexpected restart; pending receipt will resume without repeating the stop phase"}
	for index := len(plan.Services) - 1; index >= 0; index-- {
		service := plan.Services[index]
		if !snapshots[service.Label].Loaded {
			continue
		}
		if err := operator.Bootout(ctx, service); err != nil {
			return result, fmt.Errorf("stack recovery partial while stopping %s; pending receipt is preserved: %w", service.Label, err)
		}
		result.Stopped = append(result.Stopped, service.Label)
	}
	for _, service := range plan.Services {
		if err := operator.Bootstrap(ctx, service); err != nil {
			return result, fmt.Errorf("stack recovery partial while starting %s; next run resumes without repeating stops: %w", service.Label, err)
		}
		result.Started = append(result.Started, service.Label)
		if err := waitResilienceStackReadiness(ctx, service, operator); err != nil {
			return result, fmt.Errorf("stack recovery partial while waiting for %s readiness; next run resumes without repeating stops: %w", service.Label, err)
		}
	}
	coreService := resilienceStackCoreService(plan)
	coreSnapshot, err := operator.Inspect(ctx, coreService)
	if err != nil {
		return result, fmt.Errorf("stack recovery partial after start: inspect CORE: %w", err)
	}
	if !coreSnapshot.Loaded || !coreSnapshot.Running || coreSnapshot.PID <= 0 {
		return result, errors.New("stack recovery partial after start: CORE is not running")
	}
	state.CorePID = coreSnapshot.PID
	state.PendingPlanSHA256 = ""
	if stateStore != nil {
		if err := stateStore.Save(state); err != nil {
			return result, fmt.Errorf("stack ready but pending receipt could not be cleared; next run will verify and resume: %w", err)
		}
	}
	result.Action = "restarted"
	result.Reason = "stack readiness verified"
	result.CorePID = coreSnapshot.PID
	return result, nil
}

func resumeResilienceStackRecovery(ctx context.Context, plan resilienceStackPlan, state resilienceStackState, operator resilienceStackOperator, stateStore resilienceStackStateStore) (resilienceStackResult, error) {
	if stateStore == nil {
		return resilienceStackResult{}, errors.New("pending stack recovery requires a durable state store")
	}
	result := resilienceStackResult{Action: "partial", Reason: "resuming pending stack recovery without repeating the stop phase"}
	for _, service := range plan.Services {
		snapshot, err := operator.Inspect(ctx, service)
		if err != nil {
			return result, fmt.Errorf("pending stack recovery partial: inspect %s: %w", service.Label, err)
		}
		if snapshot.Disabled {
			state.CorePID = 0
			if stateStore != nil {
				if err := stateStore.Save(state); err != nil {
					return result, fmt.Errorf("preserve pending recovery during disabled maintenance: %w", err)
				}
			}
			return resilienceStackResult{Action: "noop", Reason: "launchd target disabled for maintenance; pending receipt preserved"}, nil
		}
		if snapshot.Loaded && snapshot.Running && snapshot.PID > 0 && operator.Readiness(ctx, service) == nil {
			continue
		}
		if snapshot.Loaded {
			if err := operator.Bootout(ctx, service); err != nil {
				return result, fmt.Errorf("pending stack recovery partial while stopping unhealthy %s: %w", service.Label, err)
			}
			result.Stopped = append(result.Stopped, service.Label)
		}
		if err := operator.Bootstrap(ctx, service); err != nil {
			return result, fmt.Errorf("pending stack recovery partial while starting %s: %w", service.Label, err)
		}
		result.Started = append(result.Started, service.Label)
		if err := waitResilienceStackReadiness(ctx, service, operator); err != nil {
			return result, fmt.Errorf("pending stack recovery partial while waiting for %s readiness: %w", service.Label, err)
		}
	}
	coreSnapshot, err := operator.Inspect(ctx, resilienceStackCoreService(plan))
	if err != nil {
		return result, fmt.Errorf("pending stack recovery partial after start: inspect CORE: %w", err)
	}
	if !coreSnapshot.Loaded || !coreSnapshot.Running || coreSnapshot.PID <= 0 {
		return result, errors.New("pending stack recovery partial after start: CORE is not running")
	}
	state.CorePID = coreSnapshot.PID
	state.PendingPlanSHA256 = ""
	if stateStore != nil {
		if err := stateStore.Save(state); err != nil {
			return result, fmt.Errorf("stack ready but pending receipt could not be cleared: %w", err)
		}
	}
	result.Action = "restarted"
	result.Reason = "pending stack recovery completed and readiness verified"
	result.CorePID = coreSnapshot.PID
	return result, nil
}

func resilienceStackCoreService(plan resilienceStackPlan) resilienceStackService {
	for _, service := range plan.Services {
		if service.Label == "com.rencrow.core" {
			return service
		}
	}
	return resilienceStackService{}
}

func resilienceStackPlanSHA256(plan resilienceStackPlan) (string, error) {
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

func resilienceStackPermission(uid int, domain string) error {
	switch domain {
	case resilienceStackDomain:
		if uid != 0 {
			return errors.New("system launchd jobs require the system service owner (effective UID 0)")
		}
	case resilienceStackGUI501, resilienceStackUser501:
		if uid != 0 && uid != 501 {
			return errors.New("gui/501 and user/501 launchd jobs require UID 501 or the system service owner")
		}
	default:
		return fmt.Errorf("unsupported launchd domain %q", domain)
	}
	return nil
}

func waitResilienceStackReadiness(ctx context.Context, service resilienceStackService, operator resilienceStackOperator) error {
	deadline := time.NewTimer(time.Duration(service.Readiness.TimeoutSeconds) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(resilienceStackPollEvery)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := operator.Readiness(ctx, service); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			if lastErr == nil {
				return fmt.Errorf("%s readiness timed out", service.Label)
			}
			return fmt.Errorf("%s readiness timed out: %w", service.Label, lastErr)
		case <-ticker.C:
		}
	}
}

func resilienceStackReconcile() error {
	plan, err := readResilienceStackPlan()
	if err != nil {
		return fmt.Errorf("load stack plan: %w", err)
	}
	if err := validateResilienceStackPlan(plan); err != nil {
		return fmt.Errorf("validate stack plan: %w", err)
	}
	releaseLock, err := acquireResilienceStackLock()
	if err != nil {
		return fmt.Errorf("acquire stack transaction lock: %w", err)
	}
	defer releaseLock()
	plan, err = readResilienceStackPlan()
	if err != nil {
		return fmt.Errorf("reload stack plan under lock: %w", err)
	}
	if err := validateResilienceStackPlan(plan); err != nil {
		return fmt.Errorf("validate stack plan under lock: %w", err)
	}
	maintenance, err := resilienceStackMaintenanceActive()
	if err != nil {
		return err
	}
	uid, err := resilienceStackEffectiveUID()
	if err != nil {
		return err
	}
	state, err := readResilienceStackState()
	if err != nil {
		return err
	}
	operator := newResilienceStackOperator()
	result, reconcileErr := reconcileResilienceStack(context.Background(), plan, uid, maintenance, state, operator, resilienceStackFileStateStore{})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	return reconcileErr
}

func resilienceStackMaintenance(action string) error {
	uid, err := resilienceStackEffectiveUID()
	if err != nil {
		return err
	}
	if uid != 0 {
		return errors.New("stack maintenance marker is owned by the system service owner (effective UID 0)")
	}
	releaseLock, err := acquireResilienceStackLock()
	if err != nil {
		return fmt.Errorf("acquire stack transaction lock: %w", err)
	}
	defer releaseLock()
	switch action {
	case "enter":
		if err := clearResilienceStackPIDBaseline(); err != nil {
			return err
		}
		if _, err := os.Lstat(resilienceStackHoldPath); err == nil {
			if err := validateResilienceStackProtectedFile(resilienceStackHoldPath, 0, false); err != nil {
				return err
			}
			_, err = fmt.Fprintln(os.Stdout, "maintenance=enabled")
			return err
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := validateResilienceStackProtectedFile(resilienceStackHoldPath, 0, true); err != nil {
			return err
		}
		file, err := os.OpenFile(resilienceStackHoldPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("create stack maintenance marker: %w", err)
		}
		_, writeErr := io.WriteString(file, "rencrow-launchd-stack-maintenance/v1\n")
		syncErr := file.Sync()
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		_, err = fmt.Fprintln(os.Stdout, "maintenance=enabled")
		return err
	case "leave":
		if err := validateResilienceStackProtectedFile(resilienceStackHoldPath, 0, true); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				_, writeErr := fmt.Fprintln(os.Stdout, "maintenance=disabled")
				return writeErr
			}
			return err
		}
		if err := os.Remove(resilienceStackHoldPath); err != nil {
			return fmt.Errorf("remove stack maintenance marker: %w", err)
		}
		_, err = fmt.Fprintln(os.Stdout, "maintenance=disabled")
		return err
	default:
		return fmt.Errorf("unknown stack maintenance action %q (expected enter or leave)", action)
	}
}

func readResilienceStackPlan() (resilienceStackPlan, error) {
	if err := validateResilienceStackProtectedFile(resilienceStackPlanPath, 0, false); err != nil {
		return resilienceStackPlan{}, err
	}
	file, err := os.Open(resilienceStackPlanPath)
	if err != nil {
		return resilienceStackPlan{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var plan resilienceStackPlan
	if err := decoder.Decode(&plan); err != nil {
		return resilienceStackPlan{}, fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return resilienceStackPlan{}, errors.New("plan must contain exactly one JSON value")
		}
		return resilienceStackPlan{}, fmt.Errorf("read plan terminator: %w", err)
	}
	return plan, nil
}

func resilienceStackMaintenanceActive() (bool, error) {
	err := validateResilienceStackProtectedFile(resilienceStackHoldPath, 0, true)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func readResilienceStackState() (resilienceStackState, error) {
	if err := validateResilienceStackProtectedFile(resilienceStackStatePath, 0, true); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return resilienceStackState{}, nil
		}
		return resilienceStackState{}, err
	}
	data, err := os.ReadFile(resilienceStackStatePath)
	if err != nil {
		return resilienceStackState{}, err
	}
	var state resilienceStackState
	if err := json.Unmarshal(data, &state); err != nil || state.CorePID < 0 || (state.PendingPlanSHA256 != "" && !resilienceStackStateHashPattern.MatchString(state.PendingPlanSHA256)) {
		return resilienceStackState{}, errors.New("stack monitor state is invalid")
	}
	return state, nil
}

func writeResilienceStackState(state resilienceStackState) error {
	if state.CorePID < 0 || (state.PendingPlanSHA256 != "" && !resilienceStackStateHashPattern.MatchString(state.PendingPlanSHA256)) {
		return errors.New("stack monitor state is invalid")
	}
	if err := validateResilienceStackProtectedFile(resilienceStackStatePath, 0, true); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	temporary, err := os.CreateTemp(filepath.Dir(resilienceStackStatePath), ".resilience-stack-state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := json.NewEncoder(temporary).Encode(state); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, resilienceStackStatePath); err != nil {
		return err
	}
	return nil
}

type resilienceStackFileStateStore struct{}

func (resilienceStackFileStateStore) Save(state resilienceStackState) error {
	return writeResilienceStackState(state)
}

func clearResilienceStackPIDBaseline() error {
	state, err := readResilienceStackState()
	if err != nil {
		return err
	}
	state.CorePID = 0
	return writeResilienceStackState(state)
}

func validateResilienceStackProtectedFile(path string, owner int, allowMissing bool) error {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) || clean != path {
		return fmt.Errorf("protected stack path must be clean and absolute: %q", path)
	}
	current := string(os.PathSeparator)
	parts := strings.Split(strings.TrimPrefix(clean, current), string(os.PathSeparator))
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if allowMissing && index == len(parts)-1 && errors.Is(err, os.ErrNotExist) {
				parentInfo, parentErr := os.Lstat(filepath.Dir(current))
				if parentErr != nil {
					return parentErr
				}
				return validateResilienceStackDirectory(filepath.Dir(current), parentInfo, owner)
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("protected stack path contains a symlink: %s", current)
		}
		isFinal := index == len(parts)-1
		if isFinal {
			if !info.Mode().IsRegular() {
				return fmt.Errorf("protected stack file is not regular: %s", current)
			}
			fileOwner, err := resilienceStackFileOwner(info)
			if err != nil {
				return err
			}
			if fileOwner != owner || info.Mode().Perm()&0o022 != 0 {
				return fmt.Errorf("protected stack file has unsafe owner or permissions: %s", current)
			}
			return nil
		}
		if !info.IsDir() {
			return fmt.Errorf("protected stack path component is not a directory: %s", current)
		}
		if owner == 0 {
			directoryOwner, err := resilienceStackFileOwner(info)
			if err != nil {
				return err
			}
			if directoryOwner != 0 {
				return fmt.Errorf("root-owned stack path has a non-root directory: %s", current)
			}
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("protected stack directory is writable by group or others: %s", current)
		}
	}
	return nil
}

func validateResilienceStackDirectory(path string, info os.FileInfo, owner int) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("protected stack parent is not a safe directory: %s", path)
	}
	directoryOwner, err := resilienceStackFileOwner(info)
	if err != nil {
		return err
	}
	if directoryOwner != owner || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("protected stack parent has unsafe owner or permissions: %s", path)
	}
	return nil
}

func resilienceStackEffectiveUID() (int, error) {
	uid := os.Geteuid()
	if uid < 0 {
		return 0, errors.New("effective launchd service owner UID is invalid")
	}
	return uid, nil
}

var resilienceStackLaunchdStatePattern = regexp.MustCompile(`(?m)^\s*state\s*=\s*([[:alnum:]_-]+)\s*$`)
var resilienceStackLaunchdPIDPattern = regexp.MustCompile(`(?m)^\s*pid\s*=\s*([0-9]+)\s*$`)
var resilienceStackStateHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
