package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

const (
	runtimeMusicGatewayBinEnv   = "RENCROW_MUSIC_GATEWAY_BIN"
	runtimeMusicGatewayTokenEnv = "RENCROW_TEST_MUSIC_GATEWAY_TOKEN"
	runtimeMusicGatewayBadToken = "RENCROW_TEST_MUSIC_GATEWAY_BAD_TOKEN"
	runtimeMusicGatewayToken    = "integration-only-music-token-0123456789abcdef"
	runtimeMusicGatewayUserID   = "music-integration-test-user"
)

// TestRuntimeMusicGatewayIntegration exercises CORE's production Worker
// wiring against a separately built, isolated RenCrow_Music process. It does
// not invoke an LLM or claim production deployment acceptance.
func TestRuntimeMusicGatewayIntegration(t *testing.T) {
	binary := strings.TrimSpace(os.Getenv(runtimeMusicGatewayBinEnv))
	if binary == "" {
		t.Skip(runtimeMusicGatewayBinEnv + " is not set")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatalf("resolve Music gateway binary: %v", err)
	}
	info, err := os.Stat(binary)
	if err != nil || info.IsDir() {
		t.Fatalf("Music gateway binary %q is unavailable: %v", binary, err)
	}

	t.Setenv(runtimeMusicGatewayTokenEnv, runtimeMusicGatewayToken)
	t.Setenv(runtimeMusicGatewayBadToken, "integration-only-invalid-token-0123456789")

	dataRoot := filepath.Join(t.TempDir(), "music-data")
	if err := os.MkdirAll(dataRoot, 0o700); err != nil {
		t.Fatalf("create isolated Music data root: %v", err)
	}
	dataRoot, err = filepath.Abs(dataRoot)
	if err != nil {
		t.Fatalf("resolve isolated Music data root: %v", err)
	}
	process := startRuntimeMusicGateway(t, binary, dataRoot, runtimeMusicGatewayUserID, runtimeMusicGatewayToken)
	address := waitForRuntimeMusicGateway(t, process)
	baseURL := "http://" + address
	taskOwner, taskExecutionContext := runtimeToolOwnerFixture(t, t.TempDir(), "music-worker-agent")

	// The capability probe must authenticate before project Tools enter the
	// actual Worker metadata and its runtime capability snapshot.
	unauthorizedRuntime := buildRuntimeMusicGatewayToolRuntime(
		t, taskOwner, baseURL, runtimeMusicGatewayUserID, runtimeMusicGatewayBadToken,
	)
	unauthorizedMetadata, err := unauthorizedRuntime.WorkerRuntimeRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("Worker ListTools after rejected capability probe: %v", err)
	}
	unauthorizedSnapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(unauthorizedMetadata, nil, nil, nil))
	assertRuntimeMusicProjectToolAvailability(t, unauthorizedMetadata, unauthorizedSnapshot, false)

	wrongActorRuntime := buildRuntimeMusicGatewayToolRuntime(t, taskOwner, baseURL, "different-configured-actor", runtimeMusicGatewayTokenEnv)
	wrongActorMetadata, err := wrongActorRuntime.WorkerRuntimeRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("Worker ListTools after actor-binding mismatch: %v", err)
	}
	wrongActorSnapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(wrongActorMetadata, nil, nil, nil))
	assertRuntimeMusicProjectToolAvailability(t, wrongActorMetadata, wrongActorSnapshot, false)

	runtime := buildRuntimeMusicGatewayToolRuntime(t, taskOwner, baseURL, runtimeMusicGatewayUserID, runtimeMusicGatewayTokenEnv)
	metadata, err := runtime.WorkerRuntimeRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatalf("production Worker ListTools: %v", err)
	}
	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(metadata, nil, nil, nil))
	assertRuntimeMusicProjectToolAvailability(t, metadata, snapshot, true)

	requestID := "music-project-create-integration-1"
	workerContext := runtimeMusicGatewayDelegatedWorkerContext(t, taskExecutionContext, runtimeMusicGatewayUserID, requestID)
	brief := "雨上がりの街を歩くための静かなピアノ曲。柔らかな弦を重ね、余韻を長く残す。"
	createArgs := map[string]any{"title": "雨上がりの夜", "brief": brief}
	created := executeRuntimeMusicProjectTool(t, runtime.WorkerRuntimeRunnerV2, workerContext, "music.project.create", createArgs)
	if created.IsError() {
		t.Fatalf("Worker music.project.create returned an error: %s", created.String())
	}
	project, ok := created.Result.(toolsinfra.MusicProject)
	if !ok {
		t.Fatalf("created project result has type %T, want tools.MusicProject", created.Result)
	}
	if project.ID == "" || project.Title != "雨上がりの夜" || project.Brief != brief {
		t.Fatalf("created Music project = %+v; expected opaque ID and preserved Japanese title/brief", project)
	}
	if project.IntendedUse != "personal_noncommercial" || project.Mode != "interactive" {
		t.Fatalf("created Music project defaults = %+v; expected personal_noncommercial and interactive", project)
	}
	if project.State != "draft" {
		t.Fatalf("created Music project state = %q, want draft", project.State)
	}

	// The same trusted HTTP request ID is the idempotency key on replay.
	replayed := executeRuntimeMusicProjectTool(t, runtime.WorkerRuntimeRunnerV2, workerContext, "music.project.create", createArgs)
	if replayed.IsError() {
		t.Fatalf("Worker music.project.create replay returned an error: %s", replayed.String())
	}
	replayedProject, ok := replayed.Result.(toolsinfra.MusicProject)
	if !ok {
		t.Fatalf("replayed project result has type %T, want tools.MusicProject", replayed.Result)
	}
	if !reflect.DeepEqual(replayedProject, project) {
		t.Fatalf("same-request replay created a different project: first=%+v replay=%+v", project, replayedProject)
	}

	readResponse := executeRuntimeMusicProjectTool(t, runtime.WorkerRuntimeRunnerV2, workerContext, "music.project.get", map[string]any{"project_id": project.ID})
	if readResponse.IsError() {
		t.Fatalf("Worker music.project.get returned an error: %s", readResponse.String())
	}
	workerReadback, ok := readResponse.Result.(toolsinfra.MusicProject)
	if !ok {
		t.Fatalf("Worker readback result has type %T, want tools.MusicProject", readResponse.Result)
	}
	if !reflect.DeepEqual(workerReadback, project) {
		t.Fatalf("Worker readback differs from created Music project: created=%+v readback=%+v", project, workerReadback)
	}

	// Independently read the saved record over Music's authenticated HTTP API.
	httpReadback := getRuntimeMusicProject(t, baseURL, runtimeMusicGatewayToken, project.ID)
	if !reflect.DeepEqual(httpReadback, project) {
		t.Fatalf("real Music HTTP readback differs from CORE result: created=%+v readback=%+v", project, httpReadback)
	}

	otherUserContext := runtimeMusicGatewayDelegatedWorkerContext(t, taskExecutionContext, "other-music-test-user", "music-other-user-request")
	assertRuntimeMusicProjectToolDenied(t, runtime.WorkerRuntimeRunnerV2, otherUserContext, "music.project.create", createArgs)

	publicAgentContext, err := domaintool.DeriveAgentToolExecutionScope(
		taskExecutionContext, "music-public-agent-request", "music-worker-agent", "worker", "integration test", false,
	)
	if err != nil {
		t.Fatalf("derive public Agent scope: %v", err)
	}
	if _, found := domaintool.AuthenticatedHTTPUserScopeFromContext(publicAgentContext); found {
		t.Fatal("public Agent unexpectedly has authenticated HTTP User proof")
	}
	assertRuntimeMusicProjectToolDenied(t, runtime.WorkerRuntimeRunnerV2, publicAgentContext, "music.project.create", createArgs)
}

func buildRuntimeMusicGatewayToolRuntime(t *testing.T, owner *taskmanager.Manager, endpoint, actorID, tokenEnv string) toolRuntime {
	t.Helper()
	cfg := testCapabilityRuntimeConfig(t)
	cfg.Music = config.MusicConfig{
		Enabled:  true,
		Endpoint: endpoint,
		ActorID:  actorID,
		TokenEnv: tokenEnv,
	}
	return buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, nil, nil, nil, testCanonicalMediationStore(t))
}

func assertRuntimeMusicProjectToolAvailability(
	t *testing.T,
	metadata []domaintool.ToolMetadata,
	snapshot capdomain.RuntimeCapabilitySnapshot,
	wantAvailable bool,
) {
	t.Helper()
	for _, toolID := range []string{"music.project.create", "music.project.get"} {
		if got := hasToolMetadata(metadata, toolID); got != wantAvailable {
			t.Fatalf("Music capability probe tool %q available=%v, want %v", toolID, got, wantAvailable)
		}
		entry, ok := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, toolID)
		got := ok && entry.Status == capdomain.CapabilityStatusAvailable
		if got != wantAvailable {
			t.Fatalf("Music capability probe snapshot tool %q available=%v, want %v; snapshot=%+v", toolID, got, wantAvailable, snapshot.Entries)
		}
	}
}

func runtimeMusicGatewayDelegatedWorkerContext(t *testing.T, executionContext context.Context, userID, requestID string) context.Context {
	t.Helper()
	userScope, err := domaintool.NewToolExecutionScope(
		requestID,
		domaintool.ActorKindUser,
		userID,
		userID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("create authenticated HTTP User scope: %v", err)
	}
	userContext := domaintool.WithToolExecutionScope(executionContext, userScope)
	workerContext, err := domaintool.DeriveAgentToolExecutionScope(
		userContext, requestID, "music-worker-agent", "worker", "music project integration", false,
	)
	if err != nil {
		t.Fatalf("derive Worker Agent scope from authenticated User: %v", err)
	}
	proof, found := domaintool.AuthenticatedHTTPUserScopeFromContext(workerContext)
	if !found || proof.ActorKind != domaintool.ActorKindUser || proof.AuthenticationSource != domaintool.AuthenticationSourceHTTP ||
		proof.RequestID != requestID || proof.AuthenticatedUserID != userID {
		t.Fatalf("derived Worker Agent scope has no matching authenticated HTTP User proof: proof=%+v found=%v", proof, found)
	}
	return workerContext
}

func executeRuntimeMusicProjectTool(
	t *testing.T,
	runner domaintool.RunnerV2,
	ctx context.Context,
	toolID string,
	args map[string]any,
) *domaintool.ToolResponse {
	t.Helper()
	response, err := runner.ExecuteV2(ctx, toolID, args)
	if err != nil {
		t.Fatalf("Worker ExecuteV2(%s): %v", toolID, err)
	}
	if response == nil {
		t.Fatalf("Worker ExecuteV2(%s) returned nil response", toolID)
	}
	return response
}

func assertRuntimeMusicProjectToolDenied(
	t *testing.T,
	runner domaintool.RunnerV2,
	ctx context.Context,
	toolID string,
	args map[string]any,
) {
	t.Helper()
	response, err := runner.ExecuteV2(ctx, toolID, args)
	if err == nil && response != nil && !response.IsError() {
		t.Fatalf("Worker ExecuteV2(%s) accepted an unauthorized execution scope: %+v", toolID, response)
	}
}

func getRuntimeMusicProject(t *testing.T, endpoint, token, projectID string) toolsinfra.MusicProject {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/v1/music/projects/"+projectID, nil)
	if err != nil {
		t.Fatalf("create Music readback request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("GET real Music project: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("GET real Music project status = %d, want %d; body=%s", response.StatusCode, http.StatusOK, body)
	}
	var project toolsinfra.MusicProject
	if err := json.NewDecoder(response.Body).Decode(&project); err != nil {
		t.Fatalf("decode real Music project readback: %v", err)
	}
	return project
}

type runtimeMusicGatewayProcess struct {
	cmd     *exec.Cmd
	cancel  context.CancelFunc
	listen  chan string
	done    chan struct{}
	stderr  bytes.Buffer
	exitErr error
}

func startRuntimeMusicGateway(t *testing.T, binary, dataRoot, actorID, token string) *runtimeMusicGatewayProcess {
	t.Helper()
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("create Music process stdout pipe: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	process := &runtimeMusicGatewayProcess{listen: make(chan string, 1), done: make(chan struct{}), cancel: cancel}
	cmd := exec.CommandContext(ctx,
		binary,
		"--profile=s0-local",
		"--listen=127.0.0.1:0",
		"--data-root="+dataRoot,
		"--actor="+actorID,
	)
	cmd.Stdout = stdoutWriter
	cmd.Stderr = &process.stderr
	cmd.Env = runtimeMusicGatewayEnvironment(token)
	if err := cmd.Start(); err != nil {
		cancel()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		t.Fatalf("start isolated Music gateway: %v", err)
	}
	if err := stdoutWriter.Close(); err != nil {
		cancel()
		_ = cmd.Wait()
		_ = stdoutReader.Close()
		t.Fatalf("close parent Music stdout writer: %v", err)
	}
	process.cmd = cmd
	go process.readStdout(stdoutReader)
	go func() {
		process.exitErr = cmd.Wait()
		close(process.done)
	}()
	t.Cleanup(func() {
		if err := process.stop(); err != nil {
			t.Errorf("stop isolated Music gateway: %v", err)
		}
	})
	return process
}

func runtimeMusicGatewayEnvironment(token string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		if strings.EqualFold(strings.SplitN(item, "=", 2)[0], "RENCROW_MUSIC_LOCAL_TOKEN") {
			continue
		}
		env = append(env, item)
	}
	return append(env, "RENCROW_MUSIC_LOCAL_TOKEN="+token)
}

func (p *runtimeMusicGatewayProcess) readStdout(reader *os.File) {
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "LISTEN ") {
			select {
			case p.listen <- strings.TrimSpace(strings.TrimPrefix(line, "LISTEN ")):
			default:
			}
		}
	}
}

func (p *runtimeMusicGatewayProcess) output() string {
	exitErr := p.exitErr
	return fmt.Sprintf("stderr:\n%s\nexit: %v", p.stderr.String(), exitErr)
}

func (p *runtimeMusicGatewayProcess) stop() error {
	p.cancel()
	select {
	case <-p.done:
		return nil
	case <-time.After(5 * time.Second):
		return fmt.Errorf("timed out waiting for Music gateway process exit")
	}
}

func waitForRuntimeMusicGateway(t *testing.T, process *runtimeMusicGatewayProcess) string {
	t.Helper()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case address := <-process.listen:
		host, portText, err := net.SplitHostPort(address)
		port, portErr := strconv.Atoi(portText)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() || portErr != nil || port < 1 || port > 65535 {
			if stopErr := process.stop(); stopErr != nil {
				t.Fatalf("invalid Music listener %q and process cleanup failed: %v", address, stopErr)
			}
			t.Fatalf("Music gateway announced listener %q, want numeric loopback host and allocated port; %s", address, process.output())
		}
		return address
	case <-process.done:
		t.Fatalf("Music gateway exited before announcing LISTEN; %s", process.output())
	case <-timer.C:
		if stopErr := process.stop(); stopErr != nil {
			t.Fatalf("timed out waiting for Music gateway LISTEN; process cleanup failed: %v", stopErr)
		}
		t.Fatalf("timed out waiting for Music gateway LISTEN; %s", process.output())
	}
	return ""
}
