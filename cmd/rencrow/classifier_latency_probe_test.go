package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	classifierLatencyProbeOptInEnv = "RENCROW_CLASSIFIER_LATENCY_PROBE"
	classifierLatencyProbeDataEnv  = "RENCROW_CLASSIFIER_LATENCY_DATASET"
	classifierLatencyProbeOutEnv   = "RENCROW_CLASSIFIER_LATENCY_OUTPUT"
	classifierLatencyProbeTestRun  = "^TestMioClassifierLatencyProbe$"
	classifierLatencyProbeCases    = 4
	classifierLatencyProbeWarmups  = 20
	classifierLatencyProbeRepeats  = 5
	classifierLatencyProbeRuns     = 2
)

// classifierLatencyProbeObservation deliberately has no message, case ID,
// prompt, or provider-error field. Keep the persisted output metadata-only.
type classifierLatencyProbeObservation struct {
	Phase          string  `json:"phase"`
	Run            int     `json:"run,omitempty"`
	Repetition     int     `json:"repetition,omitempty"`
	CaseIndex      int     `json:"case_index"`
	MessageBytes   int     `json:"message_bytes"`
	MessageRunes   int     `json:"message_runes"`
	MessageSHA256  string  `json:"message_sha256"`
	DurationNanos  int64   `json:"duration_nanos"`
	Route          string  `json:"route,omitempty"`
	Confidence     float64 `json:"confidence,omitempty"`
	EvidenceSource string  `json:"evidence_source,omitempty"`
	ErrorCode      string  `json:"error_code,omitempty"`
}

type classifierLatencyProbeHeader struct {
	Kind                     string `json:"kind"`
	SchemaVersion            int    `json:"schema_version"`
	SourceRevision           string `json:"source_revision"`
	ConfigSHA256             string `json:"config_sha256"`
	DatasetSHA256            string `json:"dataset_sha256"`
	ClassifierPromptSHA256   string `json:"classifier_prompt_sha256"`
	GatewayOrigin            string `json:"gateway_origin"`
	CaseCount                int    `json:"case_count"`
	WarmupCount              int    `json:"warmup_count"`
	MeasuredRunCount         int    `json:"measured_run_count"`
	RepetitionsPerCasePerRun int    `json:"repetitions_per_case_per_run"`
	ProductionEquivalence    string `json:"production_equivalence"`
	Scope                    string `json:"scope"`
}

type classifierLatencyProbeSummary struct {
	Kind           string `json:"kind"`
	WarmupCalls    int    `json:"warmup_calls"`
	MeasuredCalls  int    `json:"measured_calls"`
	ProviderErrors int    `json:"provider_errors"`
}

type classifierLatencyProbeComplete struct {
	Kind           string `json:"kind"`
	ReportComplete bool   `json:"report_complete"`
}

type classifierLatencyProbeRoundTripper struct {
	scheme string
	host   string
	base   http.RoundTripper
}

type classifierLatencyProbeCountingRoundTripper struct {
	allowedOrigin string
	next          http.RoundTripper
	localRequests int
	otherRequests int
}

func encodeClassifierLatencyProbeObservation(message string, observation classifierLatencyProbeObservation) ([]byte, error) {
	messageHash := sha256.Sum256([]byte(message))
	observation.MessageBytes = len(message)
	observation.MessageRunes = utf8.RuneCountInString(message)
	observation.MessageSHA256 = hex.EncodeToString(messageHash[:])
	return json.Marshal(observation)
}

func TestClassifierLatencyProbeObservationOmitsMessage(t *testing.T) {
	const privateMessage = "private user text must not be serialized"
	encoded, err := encodeClassifierLatencyProbeObservation(privateMessage, classifierLatencyProbeObservation{
		Phase:         "measure",
		CaseIndex:     1,
		DurationNanos: 123,
		Route:         "CHAT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), privateMessage) || strings.Contains(string(encoded), `"message":`) {
		t.Fatal("classifier latency probe output contains the input message")
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["message_bytes"] != float64(len(privateMessage)) || decoded["message_runes"] != float64(utf8.RuneCountInString(privateMessage)) {
		t.Fatalf("message length metadata missing: %s", encoded)
	}
	wantHash := sha256.Sum256([]byte(privateMessage))
	if decoded["message_sha256"] != hex.EncodeToString(wantHash[:]) {
		t.Fatalf("message hash metadata missing: %s", encoded)
	}
}

func TestClassifierLatencyProbeTransportBlocksRedirects(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Location", "https://example.invalid/private")
				w.WriteHeader(status)
			}))
			defer server.Close()

			origin, err := classifierLatencyProbeLoopbackGateway(server.URL)
			if err != nil {
				t.Fatal("test Gateway must use a literal loopback URL")
			}
			base := &classifierLatencyProbeCountingRoundTripper{
				allowedOrigin: origin,
				next:          http.DefaultTransport,
			}
			transport, err := newClassifierLatencyProbeRoundTripper(origin, base)
			if err != nil {
				t.Fatal("could not construct probe transport")
			}
			client := &http.Client{Transport: transport}
			response, err := client.Get(server.URL + "/redirect")
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil {
				t.Fatal("redirect to a non-Gateway URL was accepted")
			}
			if base.localRequests != 1 || base.otherRequests != 0 {
				t.Fatalf("round trips local=%d other=%d, want 1/0", base.localRequests, base.otherRequests)
			}
		})
	}
}

// TestMioClassifierLatencyProbe is opt-in and must be run by itself. This
// prevents another test in this package from initializing CORE's process-wide
// raw-log sinks before the probe redirects them to the null device.
func TestMioClassifierLatencyProbe(t *testing.T) {
	if os.Getenv(classifierLatencyProbeOptInEnv) != "1" {
		t.Skip("set RENCROW_CLASSIFIER_LATENCY_PROBE=1 with explicit CORE config, dataset, and output paths")
	}
	if value := flag.Lookup("test.run"); value == nil || value.Value.String() != classifierLatencyProbeTestRun {
		t.Fatal("classifier latency probe requires -run '^TestMioClassifierLatencyProbe$' so no other test initializes provider logging")
	}

	configPath, err := classifierLatencyProbeRequiredFile("RENCROW_CONFIG")
	if err != nil {
		t.Fatal("classifier latency probe requires an absolute, readable RENCROW_CONFIG file")
	}
	datasetPath, err := classifierLatencyProbeRequiredFile(classifierLatencyProbeDataEnv)
	if err != nil {
		t.Fatal("classifier latency probe requires an absolute, readable dataset file")
	}
	outputPath, err := classifierLatencyProbeOutputPath(os.Getenv(classifierLatencyProbeOutEnv))
	if err != nil {
		t.Fatal("classifier latency probe requires an absolute output path in an existing private directory")
	}

	// Provider middleware uses process-wide once-only sinks. These overrides are
	// installed before loading config or constructing the production provider.
	t.Setenv("RENCROW_CHAT_RAW_LOG", os.DevNull)
	t.Setenv("RENCROW_WORKER_RAW_LOG", os.DevNull)
	t.Setenv("RENCROW_IDLECHAT_RAW_LOG", os.DevNull)
	t.Setenv("RENCROW_PROMPT_RECEIPT_LOG", os.DevNull)
	t.Setenv("NO_PROXY", "*")
	t.Setenv("no_proxy", "*")
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("http_proxy", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("https_proxy", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("all_proxy", "")
	previousLogOutput := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(previousLogOutput)

	configHash, err := classifierLatencyProbeFileSHA256(configPath)
	if err != nil {
		t.Fatal("classifier latency probe could not hash the CORE config")
	}
	cfg, err := config.LoadConfig(configPath)
	if err != nil || cfg == nil || cfg.Prompts == nil {
		t.Fatal("classifier latency probe could not load the CORE config and classifier prompt")
	}
	if currentHash, hashErr := classifierLatencyProbeFileSHA256(configPath); hashErr != nil || currentHash != configHash {
		t.Fatal("CORE config changed while the classifier latency probe was loading it")
	}
	gatewayOrigin, err := classifierLatencyProbeLoopbackGateway(cfg.LLMGateway.BaseURL)
	if err != nil {
		t.Fatal("classifier latency probe only permits a literal loopback LLM Gateway URL")
	}
	previousDefaultTransport := http.DefaultTransport
	probeTransport, err := newClassifierLatencyProbeRoundTripper(gatewayOrigin, previousDefaultTransport)
	if err != nil {
		t.Fatal("classifier latency probe could not constrain Gateway HTTP transport")
	}
	http.DefaultTransport = probeTransport
	defer func() { http.DefaultTransport = previousDefaultTransport }()

	datasetHash, err := classifierLatencyProbeFileSHA256(datasetPath)
	if err != nil {
		t.Fatal("classifier latency probe could not hash the private dataset")
	}
	cases, err := classifierlab.LoadReplayDataset(datasetPath)
	if err != nil || len(cases) != classifierLatencyProbeCases {
		t.Fatalf("classifier latency probe requires exactly %d valid replay cases", classifierLatencyProbeCases)
	}
	if currentHash, hashErr := classifierLatencyProbeFileSHA256(datasetPath); hashErr != nil || currentHash != datasetHash {
		t.Fatal("dataset changed while the classifier latency probe was loading it")
	}
	promptHash := sha256.Sum256([]byte(cfg.Prompts.Classifier))

	workspace, err := os.MkdirTemp(filepath.Dir(outputPath), ".classifier-latency-owner-")
	if err != nil {
		t.Fatal("classifier latency probe could not create its isolated owner workspace")
	}
	if err := os.Chmod(workspace, 0o700); err != nil {
		_ = os.RemoveAll(workspace)
		t.Fatal("classifier latency probe could not make its owner workspace private")
	}
	defer os.RemoveAll(workspace)

	var deps Dependencies
	deps.taskStoreIndex = cfg.TaskStore.Index.Enabled
	deps.taskStorePersist = cfg.TaskStore.Index.Persist
	deps.taskStoreRotation = cfg.TaskStore.Rotation.Enabled
	deps.taskStoreSizeValve = cfg.TaskStore.Rotation.SizeValveBytes
	limits := taskParallelLimitsFromConfig(cfg)
	deps.taskParallelLimits = &limits
	if err := initializeRuntimeTaskOwner(&deps, workspace); err != nil {
		t.Fatal("classifier latency probe could not initialize its isolated Task owner")
	}
	actions, err := newRuntimeActionManager(workspace, actionStoreOptions(cfg.ActionStore))
	if err != nil {
		t.Fatal("classifier latency probe could not initialize its isolated Action owner")
	}
	transport, err := newRuntimeTransportManager(workspace, actions)
	if err != nil {
		t.Fatal("classifier latency probe could not initialize its isolated Transport owner")
	}
	llmRuntime := buildLLMRuntimeProviders(cfg, classifierLatencyProbeContextBudgetRecorder{}, newLLMBusyTracker(), actions, deps.taskManager, transport)
	classifier, shadow, cancel, err := buildRuntimeClassifier(llmRuntime.Chat, cfg.Prompts.Classifier, classifierlab.DefaultConfig())
	if err != nil || classifier == nil || shadow != nil {
		if cancel != nil {
			cancel()
		}
		t.Fatal("classifier latency probe could not initialize the production primary classifier")
	}
	if cancel != nil {
		defer cancel()
	}

	output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal("classifier latency probe could not create its private report without overwriting an existing file")
	}
	reportComplete := false
	defer func() {
		if !reportComplete {
			_ = output.Close()
			_ = os.Remove(outputPath)
		}
	}()
	if err := output.Chmod(0o600); err != nil {
		t.Fatal("classifier latency probe could not restrict report permissions")
	}
	writer := bufio.NewWriter(output)
	encoder := json.NewEncoder(writer)
	header := classifierLatencyProbeHeader{
		Kind:                     "classifier_latency_probe",
		SchemaVersion:            1,
		SourceRevision:           classifierLatencyProbeSourceRevision(),
		ConfigSHA256:             configHash,
		DatasetSHA256:            datasetHash,
		ClassifierPromptSHA256:   hex.EncodeToString(promptHash[:]),
		GatewayOrigin:            gatewayOrigin,
		CaseCount:                len(cases),
		WarmupCount:              classifierLatencyProbeWarmups,
		MeasuredRunCount:         classifierLatencyProbeRuns,
		RepetitionsPerCasePerRun: classifierLatencyProbeRepeats,
		ProductionEquivalence:    "not_proven",
		Scope:                    "production Chat provider middleware and primary classifier only; isolated Task/Action/Transport stores; no ingress authentication, policy admission, downstream route execution, or persistent context-budget recorder",
	}
	if err := encoder.Encode(header); err != nil {
		t.Fatal("classifier latency probe could not write report header")
	}
	providerErrors := 0
	providerTimeout := time.Duration(cfg.LLMGateway.TimeoutSec) * time.Second
	if providerTimeout <= 0 {
		providerTimeout = 10 * time.Minute
	}
	writeObservation := func(phase string, run, repetition, caseIndex int) bool {
		item := cases[caseIndex]
		turn := classifierLatencyProbeTurn(t, item)
		ctx, cancel := context.WithTimeout(context.Background(), providerTimeout)
		started := time.Now()
		decision, classifyErr := classifier.Classify(ctx, turn)
		elapsed := time.Since(started)
		cancel()
		observation := classifierLatencyProbeObservation{
			Phase:         phase,
			Run:           run,
			Repetition:    repetition,
			CaseIndex:     caseIndex,
			DurationNanos: elapsed.Nanoseconds(),
		}
		if classifyErr != nil {
			observation.ErrorCode = "classifier_error"
			providerErrors++
		} else {
			observation.Route = decision.Route.String()
			observation.Confidence = decision.Confidence
			if len(decision.Evidence) > 0 {
				observation.EvidenceSource = decision.Evidence[0].Source
			}
		}
		encoded, encodeErr := encodeClassifierLatencyProbeObservation(item.Message, observation)
		if encodeErr != nil {
			t.Fatal("classifier latency probe could not encode metadata-only observation")
		}
		if _, writeErr := writer.Write(encoded); writeErr != nil {
			t.Fatal("classifier latency probe could not write an observation")
		}
		if err := writer.WriteByte('\n'); err != nil {
			t.Fatal("classifier latency probe could not delimit an observation")
		}
		return classifyErr == nil
	}

	// Twenty warmups are balanced across all cases (five per case).
	probeFailed := false
warmupLoop:
	for index := 0; index < classifierLatencyProbeWarmups; index++ {
		if !writeObservation("warmup", 0, index/classifierLatencyProbeCases, index%classifierLatencyProbeCases) {
			probeFailed = true
			break warmupLoop
		}
	}
	// Two sequential runs each contain five observations per case. Rotating the
	// case order by run/repetition limits fixed-order timing bias.
	if !probeFailed {
	measurementLoop:
		for run := 1; run <= classifierLatencyProbeRuns; run++ {
			for repetition := 1; repetition <= classifierLatencyProbeRepeats; repetition++ {
				for offset := 0; offset < len(cases); offset++ {
					caseIndex := (offset + repetition - 1 + run - 1) % len(cases)
					if !writeObservation("measure", run, repetition, caseIndex) {
						probeFailed = true
						break measurementLoop
					}
				}
			}
		}
	}
	if probeFailed {
		t.Fatal("classifier latency probe stopped after the first classifier error; incomplete report removed")
	}
	if err := encoder.Encode(classifierLatencyProbeSummary{
		Kind:           "summary",
		WarmupCalls:    classifierLatencyProbeWarmups,
		MeasuredCalls:  classifierLatencyProbeCases * classifierLatencyProbeRepeats * classifierLatencyProbeRuns,
		ProviderErrors: providerErrors,
	}); err != nil {
		t.Fatal("classifier latency probe could not write report summary")
	}
	if err := writer.Flush(); err != nil {
		t.Fatal("classifier latency probe could not flush its private report")
	}
	if err := output.Sync(); err != nil {
		t.Fatal("classifier latency probe could not sync its private report")
	}
	if err := encoder.Encode(classifierLatencyProbeComplete{Kind: "complete", ReportComplete: true}); err != nil {
		t.Fatal("classifier latency probe could not write report completion marker")
	}
	if err := writer.Flush(); err != nil {
		t.Fatal("classifier latency probe could not flush its completion marker")
	}
	if err := output.Sync(); err != nil {
		t.Fatal("classifier latency probe could not sync its completion marker")
	}
	if err := output.Close(); err != nil {
		t.Fatal("classifier latency probe could not close its private report")
	}
	reportComplete = true
	t.Logf("classifier latency report complete: cases=%d warmups=%d measurements=%d provider_errors=%d", len(cases), classifierLatencyProbeWarmups, classifierLatencyProbeCases*classifierLatencyProbeRepeats*classifierLatencyProbeRuns, providerErrors)
}

// classifierLatencyProbeContextBudgetRecorder keeps the production budget
// policy active without opening or mutating the configured AI Workflow store.
type classifierLatencyProbeContextBudgetRecorder struct{}

func (classifierLatencyProbeContextBudgetRecorder) SaveContextUsage(context.Context, domainai.ContextUsage) error {
	return nil
}

func (classifierLatencyProbeContextBudgetRecorder) Append(context.Context, modulecore.EventEnvelope) error {
	return nil
}

func classifierLatencyProbeTurn(t *testing.T, item classifierlab.ReplayCase) conversation.TurnInput {
	t.Helper()
	fixture, err := classifierlab.ParseReplayTurnFixture(item.TurnFixture)
	if err != nil {
		t.Fatal("classifier latency probe found invalid turn fixture metadata")
	}
	address, err := conversation.NewChannelAddress("probe", "classifier-latency-probe")
	if err != nil {
		t.Fatal("classifier latency probe could not construct its isolated channel address")
	}
	turn, err := conversation.NewTurnInput(modulecore.NewTaskID(), item.Message, address)
	if err != nil {
		t.Fatal("classifier latency probe could not reconstruct a canonical turn input")
	}
	if fixture.SessionID != "" {
		turn = turn.WithSessionID(fixture.SessionID)
	}
	if fixture.ViewerRecipient != "" {
		turn = turn.WithViewerRecipient(fixture.ViewerRecipient)
	}
	return turn
}

func classifierLatencyProbeRequiredFile(envName string) (string, error) {
	path := strings.TrimSpace(os.Getenv(envName))
	if path == "" || !filepath.IsAbs(path) {
		return "", os.ErrInvalid
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", os.ErrNotExist
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", os.ErrPermission
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	return path, file.Close()
}

func classifierLatencyProbeOutputPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" || !filepath.IsAbs(path) {
		return "", os.ErrInvalid
	}
	parent := filepath.Dir(path)
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", os.ErrNotExist
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", os.ErrPermission
	}
	if _, err := os.Lstat(path); err == nil {
		return "", os.ErrExist
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return path, nil
}

func classifierLatencyProbeFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func classifierLatencyProbeLoopbackGateway(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", os.ErrInvalid
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", os.ErrInvalid
	}
	address := net.ParseIP(parsed.Hostname())
	if address == nil || !address.IsLoopback() || parsed.Port() == "" {
		return "", os.ErrPermission
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func newClassifierLatencyProbeRoundTripper(gatewayOrigin string, base http.RoundTripper) (*classifierLatencyProbeRoundTripper, error) {
	parsed, err := url.Parse(gatewayOrigin)
	if err != nil || parsed == nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, os.ErrInvalid
	}
	address := net.ParseIP(parsed.Hostname())
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || address == nil || !address.IsLoopback() || parsed.Port() == "" {
		return nil, os.ErrPermission
	}
	if base == nil {
		base = http.DefaultTransport
	}
	return &classifierLatencyProbeRoundTripper{scheme: parsed.Scheme, host: parsed.Host, base: base}, nil
}

func (transport *classifierLatencyProbeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.base == nil || request == nil {
		return nil, errors.New("classifier latency probe transport is unavailable")
	}
	if request.URL == nil || request.URL.User != nil || request.URL.Scheme != transport.scheme || !strings.EqualFold(request.URL.Host, transport.host) {
		return nil, errors.New("classifier latency probe blocked non-Gateway HTTP request")
	}
	return transport.base.RoundTrip(request)
}

func (transport *classifierLatencyProbeCountingRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	origin := ""
	if request != nil && request.URL != nil {
		origin = request.URL.Scheme + "://" + request.URL.Host
	}
	if strings.EqualFold(origin, transport.allowedOrigin) {
		transport.localRequests++
		return transport.next.RoundTrip(request)
	}
	transport.otherRequests++
	return nil, errors.New("test blocked non-Gateway RoundTrip")
}

func classifierLatencyProbeSourceRevision() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" && setting.Value != "" {
			return setting.Value
		}
	}
	return "unknown"
}
