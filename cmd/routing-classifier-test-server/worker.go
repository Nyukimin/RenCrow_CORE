package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/routing/classifierlab"
)

const (
	workerLineLimit     = classifierlab.MaxHTTPBodyBytes
	childHardTimeoutMS  = 10000
	childStopTimeout    = time.Second
	childStartupTimeout = 30 * time.Second
	childStderrLimit    = 8 << 10
)

type childCommand struct {
	pythonExecutable string
	arguments        []string
	environment      []string
}

type workerInit struct {
	Kind            string                         `json:"kind"`
	Manifest        classifierlab.PreparedManifest `json:"manifest"`
	ModelDirectory  string                         `json:"model_directory"`
	Policy          classifierlab.RoutePolicy      `json:"policy"`
	ReferenceWorker workerReferenceConfig          `json:"reference_worker"`
}

type workerReferenceConfig struct {
	ModelRevision   string `json:"model_revision"`
	ModelDirectory  string `json:"model_directory"`
	Device          string `json:"device"`
	DType           string `json:"dtype"`
	IntraOpThreads  int    `json:"intra_op_threads"`
	InterOpThreads  int    `json:"inter_op_threads"`
	MaxLen          int    `json:"max_len"`
	HeadMaxLen      int    `json:"head_max_len"`
	HardTimeoutMS   int    `json:"hard_timeout_ms"`
	OfflineRequired bool   `json:"offline_required"`
}

type workerMessage struct {
	data []byte
	err  error
}

// boundedTailWriter retains only a bounded stderr tail for process hygiene.
// Child stderr is never forwarded to server logs because it can contain input.
type boundedTailWriter struct {
	mu   sync.Mutex
	max  int
	data []byte
}

func newBoundedTailWriter(maximum int) *boundedTailWriter {
	if maximum < 1 {
		maximum = 1
	}
	return &boundedTailWriter{max: maximum}
}

func (w *boundedTailWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written := len(data)
	if len(data) >= w.max {
		w.data = append(w.data[:0], data[len(data)-w.max:]...)
		return written, nil
	}
	if overflow := len(w.data) + len(data) - w.max; overflow > 0 {
		copy(w.data, w.data[overflow:])
		w.data = w.data[:len(w.data)-overflow]
	}
	w.data = append(w.data, data...)
	return written, nil
}

func (w *boundedTailWriter) snapshot() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.data...)
}

type workerFrame struct {
	Kind                         string                         `json:"kind"`
	Model                        classifierlab.CandidateModel   `json:"model,omitempty"`
	PolicySHA256                 string                         `json:"policy_sha256,omitempty"`
	ModelFilesSHA256             string                         `json:"model_files_sha256,omitempty"`
	SDKLockSHA256                string                         `json:"sdk_lock_sha256,omitempty"`
	EffectiveMaxLen              int                            `json:"effective_max_len,omitempty"`
	EffectiveHeadMaxLen          int                            `json:"effective_head_max_len,omitempty"`
	RequestID                    string                         `json:"request_id,omitempty"`
	Result                       *classifierlab.CandidateResult `json:"result,omitempty"`
	ReasonCode                   string                         `json:"reason_code,omitempty"`
	OfflineConfigurationVerified *bool                          `json:"offline_configuration_verified,omitempty"`
	OfflineAttestation           *workerOfflineAttestation      `json:"offline_attestation,omitempty"`
}

type workerOfflineAttestation struct {
	OfflineEnvironmentCheckedBeforeSDKImport bool              `json:"offline_environment_checked_before_sdk_import"`
	Environment                              map[string]string `json:"environment"`
	ModelSource                              string            `json:"model_source"`
	ModelRevision                            string            `json:"model_revision"`
	ModelFilesSHA256                         string            `json:"model_files_sha256"`
	SDKLockSHA256                            string            `json:"sdk_lock_sha256"`
	PreloadedModels                          []string          `json:"preloaded_models"`
}

type workerCallError struct {
	reasonCode string
	err        error
}

func (e *workerCallError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return e.reasonCode
}

type sdkWorker struct {
	command                      childCommand
	init                         workerInit
	hardTimeout                  time.Duration
	cmd                          *exec.Cmd
	stdin                        io.WriteCloser
	messages                     chan workerMessage
	done                         chan struct{}
	readyDone                    chan struct{}
	stderr                       *boundedTailWriter
	writeMu                      sync.Mutex
	callMu                       sync.Mutex
	stateMu                      sync.RWMutex
	ready                        bool
	offlineConfigurationVerified bool
	readyErr                     string
	closed                       bool
	started                      bool
	childExited                  bool
	startOnce                    sync.Once
	stopOnce                     sync.Once
}

func newSDKWorker(config loadedConfig, artifacts classifierlab.PreparedArtifacts, script string, hardTimeout time.Duration) *sdkWorker {
	reference := config.Value.ReferenceWorker
	init := workerInit{
		Kind:           "initialize",
		Manifest:       artifacts.Manifest,
		ModelDirectory: artifacts.ModelDirectory,
		Policy:         artifacts.Policy,
		ReferenceWorker: workerReferenceConfig{
			ModelRevision:   artifacts.Manifest.Model.Revision,
			ModelDirectory:  artifacts.ModelDirectory,
			Device:          reference.Device,
			DType:           reference.DType,
			IntraOpThreads:  reference.IntraOpThreads,
			InterOpThreads:  reference.InterOpThreads,
			MaxLen:          reference.MaxLen,
			HeadMaxLen:      reference.HeadMaxLen,
			HardTimeoutMS:   reference.HardTimeoutMS,
			OfflineRequired: reference.OfflineRequired,
		},
	}
	return &sdkWorker{
		command: childCommand{
			pythonExecutable: reference.PythonExecutable,
			arguments:        []string{"-u", "-c", script},
			environment:      offlineEnvironment(os.Environ()),
		},
		init:        init,
		hardTimeout: hardTimeout,
		messages:    make(chan workerMessage, 8),
		done:        make(chan struct{}),
		readyDone:   make(chan struct{}),
		stderr:      newBoundedTailWriter(childStderrLimit),
	}
}

func (w *sdkWorker) Start(ctx context.Context) error {
	var startErr error
	w.startOnce.Do(func() {
		if ctx == nil {
			startErr = errors.New("worker context is nil")
			return
		}
		w.cmd = exec.CommandContext(ctx, w.command.pythonExecutable, w.command.arguments...)
		w.cmd.Env = w.command.environment
		w.cmd.Stderr = w.stderr
		stdout, err := w.cmd.StdoutPipe()
		if err != nil {
			startErr = fmt.Errorf("create SDK worker stdout pipe: %w", err)
			return
		}
		stdin, err := w.cmd.StdinPipe()
		if err != nil {
			startErr = fmt.Errorf("create SDK worker stdin pipe: %w", err)
			return
		}
		if err := w.cmd.Start(); err != nil {
			startErr = fmt.Errorf("start SDK worker: %w", err)
			return
		}
		w.stateMu.Lock()
		w.started = true
		w.stateMu.Unlock()
		w.stdin = stdin
		go w.readLoop(stdout)
		go func() {
			_ = w.cmd.Wait()
			w.stateMu.Lock()
			w.childExited = true
			w.ready = false
			w.offlineConfigurationVerified = false
			if !w.closed && w.readyErr == "" {
				w.readyErr = "MODEL_NOT_READY"
			}
			w.stateMu.Unlock()
			close(w.done)
		}()
		if err := w.writeJSON(w.init); err != nil {
			startErr = fmt.Errorf("write SDK worker initialization: %w", err)
			w.failAndKill("MODEL_NOT_READY")
			return
		}
		go w.awaitReady()
	})
	return startErr
}

func (w *sdkWorker) readLoop(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), workerLineLimit)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		select {
		case w.messages <- workerMessage{data: line}:
		case <-w.done:
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	select {
	case w.messages <- workerMessage{err: err}:
	case <-w.done:
	}
}

func (w *sdkWorker) awaitReady() {
	defer close(w.readyDone)
	timer := time.NewTimer(childStartupTimeout)
	defer timer.Stop()
	select {
	case <-w.done:
		w.setFailure("MODEL_NOT_READY")
		return
	case <-timer.C:
		w.failAndKill("MODEL_NOT_READY")
		return
	case message := <-w.messages:
		if message.err != nil {
			w.failAndKill("MODEL_NOT_READY")
			return
		}
		frame, err := decodeWorkerFrame(message.data)
		if err != nil || frame.Kind != "ready" {
			w.setFailure("SDK_ERROR")
			w.killOwnedChild()
			return
		}
		if err := w.validateReady(frame); err != nil {
			w.setFailure("SDK_ERROR")
			w.killOwnedChild()
			return
		}
		w.stateMu.Lock()
		if !w.closed && !w.childExited {
			w.ready = true
			w.offlineConfigurationVerified = true
			w.readyErr = ""
		} else {
			w.ready = false
			w.offlineConfigurationVerified = false
		}
		w.stateMu.Unlock()
	}
}

func (w *sdkWorker) validateReady(frame workerFrame) error {
	expectedModel := w.init.Manifest.Model
	expectedSDK := w.init.Manifest.SDK
	if frame.Model.Family != expectedModel.Family || frame.Model.SourceID != expectedModel.SourceID ||
		frame.Model.Revision != expectedModel.Revision || frame.Model.SDKVersion != expectedSDK.Version ||
		frame.Model.Device != expectedModel.Device || frame.Model.DType != expectedModel.DType {
		return errors.New("SDK ready model metadata mismatch")
	}
	if frame.PolicySHA256 != w.init.Manifest.PolicySHA256 ||
		frame.ModelFilesSHA256 != expectedModel.FilesSHA256 || frame.SDKLockSHA256 != expectedSDK.LockSHA256 ||
		frame.EffectiveMaxLen != w.init.ReferenceWorker.MaxLen ||
		frame.EffectiveHeadMaxLen != w.init.ReferenceWorker.HeadMaxLen {
		return errors.New("SDK ready artifact metadata mismatch")
	}
	if frame.OfflineConfigurationVerified == nil || !*frame.OfflineConfigurationVerified || frame.OfflineAttestation == nil {
		return errors.New("SDK offline configuration attestation is missing or false")
	}
	attestation := frame.OfflineAttestation
	if !attestation.OfflineEnvironmentCheckedBeforeSDKImport || !sameStringMap(attestation.Environment, requiredOfflineEnvironment()) {
		return errors.New("SDK offline environment attestation mismatch")
	}
	if attestation.ModelSource != "prepared_local_directory" ||
		attestation.ModelRevision != expectedModel.Revision ||
		attestation.ModelFilesSHA256 != expectedModel.FilesSHA256 ||
		attestation.SDKLockSHA256 != expectedSDK.LockSHA256 ||
		len(attestation.PreloadedModels) != 1 || attestation.PreloadedModels[0] != "multilingual" {
		return errors.New("SDK local model preload attestation mismatch")
	}
	return nil
}

func (w *sdkWorker) Ready() (bool, string) {
	ready, reason, _ := w.Readiness()
	return ready, reason
}

func (w *sdkWorker) Readiness() (bool, string, bool) {
	w.stateMu.RLock()
	defer w.stateMu.RUnlock()
	if w.ready && w.offlineConfigurationVerified && !w.closed && !w.childExited {
		return true, "", true
	}
	if w.readyErr != "" {
		return false, w.readyErr, false
	}
	return false, "MODEL_NOT_READY", false
}

func (w *sdkWorker) OfflineConfigurationVerified() bool {
	_, _, verified := w.Readiness()
	return verified
}

func (w *sdkWorker) Classify(input classifierlab.CandidateInput) (classifierlab.CandidateResult, error) {
	if !w.callMu.TryLock() {
		return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "WORKER_BUSY"}
	}
	defer w.callMu.Unlock()
	if ready, _ := w.Ready(); !ready {
		return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "MODEL_NOT_READY"}
	}
	request := struct {
		Kind  string                       `json:"kind"`
		Input classifierlab.CandidateInput `json:"input"`
	}{Kind: "classify", Input: input}
	if err := w.writeJSON(request); err != nil {
		w.failAndKill("SDK_ERROR")
		return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "SDK_ERROR", err: err}
	}
	timer := time.NewTimer(w.hardTimeout)
	defer timer.Stop()
	select {
	case <-w.done:
		w.setFailure("MODEL_NOT_READY")
		return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "SDK_ERROR"}
	case <-timer.C:
		w.failAndKill("INFERENCE_TIMEOUT")
		return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "INFERENCE_TIMEOUT"}
	case message := <-w.messages:
		if message.err != nil {
			w.failAndKill("SDK_ERROR")
			return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "SDK_ERROR", err: message.err}
		}
		frame, err := decodeWorkerFrame(message.data)
		if err != nil {
			w.failAndKill("INVALID_MODEL_OUTPUT")
			return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "INVALID_MODEL_OUTPUT", err: err}
		}
		if frame.RequestID != input.RequestID {
			w.failAndKill("INVALID_MODEL_OUTPUT")
			return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "INVALID_MODEL_OUTPUT", err: errors.New("worker response request_id mismatch")}
		}
		switch frame.Kind {
		case "response":
			if frame.Result == nil {
				w.failAndKill("INVALID_MODEL_OUTPUT")
				return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "INVALID_MODEL_OUTPUT"}
			}
			return *frame.Result, nil
		case "error":
			if !allowedWorkerError(frame.ReasonCode) {
				w.failAndKill("SDK_ERROR")
				return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "SDK_ERROR"}
			}
			if frame.ReasonCode == "SDK_ERROR" || frame.ReasonCode == "INVALID_MODEL_OUTPUT" {
				w.failAndKill(frame.ReasonCode)
			}
			return classifierlab.CandidateResult{}, &workerCallError{reasonCode: frame.ReasonCode}
		default:
			w.failAndKill("INVALID_MODEL_OUTPUT")
			return classifierlab.CandidateResult{}, &workerCallError{reasonCode: "INVALID_MODEL_OUTPUT"}
		}
	}
}

func (w *sdkWorker) writeJSON(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data)+1 > workerLineLimit {
		return errors.New("SDK worker request exceeds 64 KiB")
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if w.stdin == nil {
		return errors.New("SDK worker stdin is unavailable")
	}
	frame := append(data, '\n')
	for len(frame) > 0 {
		written, err := w.stdin.Write(frame)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		frame = frame[written:]
	}
	return nil
}

func (w *sdkWorker) setFailure(reason string) {
	w.stateMu.Lock()
	w.ready = false
	w.offlineConfigurationVerified = false
	if !w.closed {
		w.readyErr = reason
	}
	w.stateMu.Unlock()
}

func (w *sdkWorker) failAndKill(reason string) {
	w.setFailure(reason)
	w.killOwnedChild()
}

func (w *sdkWorker) killOwnedChild() {
	w.stateMu.RLock()
	cmd := w.cmd
	w.stateMu.RUnlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (w *sdkWorker) Close() {
	w.stopOnce.Do(func() {
		w.stateMu.Lock()
		w.closed = true
		w.ready = false
		w.offlineConfigurationVerified = false
		started := w.started
		w.stateMu.Unlock()
		if !started {
			return
		}
		w.writeMu.Lock()
		if w.stdin != nil {
			_ = w.stdin.Close()
		}
		w.writeMu.Unlock()
		select {
		case <-w.done:
		case <-time.After(childStopTimeout):
			w.killOwnedChild()
			<-w.done
		}
	})
}

func decodeWorkerFrame(data []byte) (workerFrame, error) {
	if len(data) == 0 || len(data) > workerLineLimit {
		return workerFrame{}, errors.New("worker JSONL frame size is invalid")
	}
	if err := rejectDuplicateFields(data); err != nil {
		return workerFrame{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var frame workerFrame
	if err := decoder.Decode(&frame); err != nil {
		return workerFrame{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return workerFrame{}, errors.New("worker frame contains multiple JSON values")
		}
		return workerFrame{}, err
	}
	return frame, nil
}

func rejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return errors.New("worker frame contains trailing JSON data")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("worker JSON key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate worker JSON field %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("unexpected worker JSON delimiter")
	}
}

func allowedWorkerError(reason string) bool {
	switch reason {
	case "INVALID_REQUEST", "POLICY_MISMATCH", "INPUT_TOO_LONG", "POLICY_TOO_LONG", "SDK_ERROR", "INVALID_MODEL_OUTPUT":
		return true
	default:
		return false
	}
}

func offlineEnvironment(environment []string) []string {
	values := make(map[string]string, len(environment)+8)
	allowed := map[string]struct{}{
		"PATH": {}, "HOME": {}, "USERPROFILE": {}, "SYSTEMROOT": {}, "WINDIR": {},
		"TEMP": {}, "TMP": {}, "TMPDIR": {}, "LD_LIBRARY_PATH": {}, "DYLD_LIBRARY_PATH": {},
		"CUDA_VISIBLE_DEVICES": {}, "CUDA_HOME": {}, "ROCM_PATH": {}, "HF_HOME": {},
	}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			if _, keep := allowed[key]; !keep {
				continue
			}
			values[key] = value
		}
	}
	for key, value := range requiredOfflineEnvironment() {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	return result
}

func requiredOfflineEnvironment() map[string]string {
	return map[string]string{
		"HF_HUB_OFFLINE":           "1",
		"TRANSFORMERS_OFFLINE":     "1",
		"HF_DATASETS_OFFLINE":      "1",
		"HF_HUB_DISABLE_TELEMETRY": "1",
		"PYTHONNOUSERSITE":         "1",
		"PYTHONUTF8":               "1",
		"PYTHONIOENCODING":         "utf-8",
		"PYTHONDONTWRITEBYTECODE":  "1",
		"TOKENIZERS_PARALLELISM":   "false",
	}
}

func sameStringMap(actual, expected map[string]string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func httpStatusForWorkerError(err error) int {
	var workerErr *workerCallError
	if !errors.As(err, &workerErr) {
		return http.StatusInternalServerError
	}
	switch workerErr.reasonCode {
	case "INPUT_TOO_LONG", "POLICY_TOO_LONG":
		return http.StatusUnprocessableEntity
	case "POLICY_MISMATCH":
		return http.StatusConflict
	case "INVALID_REQUEST":
		return http.StatusBadRequest
	case "INFERENCE_TIMEOUT":
		return http.StatusGatewayTimeout
	case "MODEL_NOT_READY":
		return http.StatusServiceUnavailable
	case "WORKER_BUSY":
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}
