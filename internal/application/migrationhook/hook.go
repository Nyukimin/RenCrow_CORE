package migrationhook

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const (
	RequestContractVersion  = "rencrow-migration-owner-hook-request/v1"
	ResponseContractVersion = "rencrow-migration-owner-hook/v1"
	Owner                   = "RenCrow_CORE"
	MaxJSONBytes            = 64 * 1024

	ExitCompleted      = 0
	ExitInvalidRequest = 2
	ExitRejected       = 10
	ExitBlocked        = 20
	ExitWriterFailure  = 30
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Request struct {
	ContractVersion string `json:"contract_version"`
	Operation       string `json:"operation"`
	Owner           string `json:"owner"`
	RequestID       string `json:"request_id"`
	CandidateConfig string `json:"candidate_config,omitempty"`
	OutputDir       string `json:"output_dir,omitempty"`
	PackageDir      string `json:"package_dir,omitempty"`
	TargetRoot      string `json:"target_root,omitempty"`
	Mode            string `json:"mode,omitempty"`
	PolicyScopeRef  string `json:"policy_scope_ref,omitempty"`
}

type Failure struct {
	Code     string `json:"code"`
	Boundary string `json:"boundary"`
}

type Receipt struct {
	ContractVersion string         `json:"contract_version"`
	Operation       string         `json:"operation"`
	Owner           string         `json:"owner"`
	RequestID       string         `json:"request_id"`
	Status          string         `json:"status"`
	StateClass      string         `json:"state_class"`
	SchemaRevision  string         `json:"schema_revision"`
	ConsistencyMode string         `json:"consistency_mode"`
	Operations      []string       `json:"operations"`
	Artifact        *Artifact      `json:"artifact"`
	Counts          map[string]int `json:"counts"`
	Failure         *Failure       `json:"failure"`
}

type ConfigValidator func(candidatePath string) error

type Artifact struct {
	LogicalID string `json:"logical_id"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

type StateOperations struct {
	Export          func(outputDir string) (Artifact, error)
	ValidateRestore func(packageDir, candidateConfig, targetRoot string) error
	ImportDryRun    func(packageDir, candidateConfig, targetRoot string) error
}

func Run(args []string, stdin io.Reader, stdout io.Writer, validateConfig ConfigValidator, stateOptions ...StateOperations) int {
	if len(args) != 0 || stdin == nil || stdout == nil || validateConfig == nil {
		return ExitInvalidRequest
	}
	request, ok := decodeRequest(stdin)
	if !ok {
		return ExitInvalidRequest
	}

	var state StateOperations
	if len(stateOptions) > 1 {
		return ExitInvalidRequest
	}
	if len(stateOptions) == 1 {
		state = stateOptions[0]
	}
	var receipt Receipt
	switch request.Operation {
	case "state_describe":
		if hasStateFields(request) {
			return ExitInvalidRequest
		}
		receipt = baseReceipt(request, state)
		receipt.Status = "completed"
	case "config_validate":
		if request.OutputDir != "" || request.PackageDir != "" || request.TargetRoot != "" || request.Mode != "" || request.PolicyScopeRef != "" || !validCandidate(request.CandidateConfig) {
			receipt = rejectedConfigReceipt(request, state)
			return writeReceipt(stdout, receipt, ExitRejected)
		}
		if err := validateConfig(request.CandidateConfig); err != nil {
			receipt = rejectedConfigReceipt(request, state)
			return writeReceipt(stdout, receipt, ExitRejected)
		}
		receipt = baseReceipt(request, state)
		receipt.Status = "completed"
	case "state_export":
		if request.CandidateConfig != "" || request.PackageDir != "" || request.TargetRoot != "" || request.Mode != "" || request.PolicyScopeRef != "" || !validPrivateDirectory(request.OutputDir) {
			return ExitInvalidRequest
		}
		if state.Export == nil {
			return writeReceipt(stdout, blockedReceipt(request, state), ExitBlocked)
		}
		artifact, err := state.Export(request.OutputDir)
		if err != nil || !validArtifact(artifact) {
			return writeReceipt(stdout, failedReceipt(request, state, "state_export_failed", "CORE state export failed"), ExitWriterFailure)
		}
		receipt = baseReceipt(request, state)
		receipt.Status = "completed"
		receipt.ConsistencyMode = "quiesced"
		receipt.Artifact = &artifact
	case "state_validate_restore", "state_import":
		if request.OutputDir != "" || request.PolicyScopeRef != "" || !validRestoreInputs(request) {
			return ExitInvalidRequest
		}
		operation := state.ValidateRestore
		if request.Operation == "state_import" {
			if request.Mode != "dry-run" {
				return writeReceipt(stdout, blockedReceipt(request, state), ExitBlocked)
			}
			operation = state.ImportDryRun
		} else if request.Mode != "" {
			return ExitInvalidRequest
		}
		if operation == nil {
			return writeReceipt(stdout, blockedReceipt(request, state), ExitBlocked)
		}
		if err := operation(request.PackageDir, request.CandidateConfig, request.TargetRoot); err != nil {
			return writeReceipt(stdout, failedReceipt(request, state, "restore_validation_failed", "CORE isolated restore validation failed"), ExitWriterFailure)
		}
		receipt = baseReceipt(request, state)
		receipt.Status = "completed"
	default:
		return ExitInvalidRequest
	}
	return writeReceipt(stdout, receipt, ExitCompleted)
}

func decodeRequest(reader io.Reader) (Request, bool) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxJSONBytes+1))
	if err != nil || len(data) == 0 || len(data) > MaxJSONBytes {
		return Request{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var request Request
	if err := decoder.Decode(&request); err != nil {
		return Request{}, false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Request{}, false
	}
	if request.ContractVersion != RequestContractVersion || request.Owner != Owner || !requestIDPattern.MatchString(request.RequestID) {
		return Request{}, false
	}
	if request.Operation != "config_validate" && request.Operation != "state_describe" && request.Operation != "state_export" && request.Operation != "state_validate_restore" && request.Operation != "state_import" {
		return Request{}, false
	}
	return request, true
}

func validCandidate(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsRune(path, '\x00') {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func baseReceipt(request Request, state StateOperations) Receipt {
	operations := make([]string, 0, 3)
	if request.Operation == "state_describe" {
		if state.Export != nil {
			operations = append(operations, "state_export")
		}
		if state.ImportDryRun != nil {
			operations = append(operations, "state_import:dry-run")
		}
		if state.ValidateRestore != nil {
			operations = append(operations, "state_validate_restore")
		}
	}
	return Receipt{
		ContractVersion: ResponseContractVersion,
		Operation:       request.Operation,
		Owner:           Owner,
		RequestID:       request.RequestID,
		StateClass:      "durable",
		SchemaRevision:  "rencrow-core-migration-state/v1",
		ConsistencyMode: "quiesced",
		Operations:      operations,
		Artifact:        nil,
		Counts:          map[string]int{},
		Failure:         nil,
	}
}

func rejectedConfigReceipt(request Request, state StateOperations) Receipt {
	receipt := baseReceipt(request, state)
	receipt.Status = "rejected"
	receipt.Failure = &Failure{Code: "config_invalid", Boundary: "CORE configuration was rejected"}
	return receipt
}

func blockedReceipt(request Request, state StateOperations) Receipt {
	receipt := baseReceipt(request, state)
	receipt.Status = "blocked"
	receipt.Failure = &Failure{Code: "owner_operation_unavailable", Boundary: "CORE owner operation is unavailable"}
	return receipt
}

func failedReceipt(request Request, state StateOperations, code, boundary string) Receipt {
	receipt := baseReceipt(request, state)
	receipt.Status = "failed"
	receipt.Failure = &Failure{Code: code, Boundary: boundary}
	return receipt
}

func hasStateFields(request Request) bool {
	return request.CandidateConfig != "" || request.OutputDir != "" || request.PackageDir != "" || request.TargetRoot != "" || request.Mode != "" || request.PolicyScopeRef != ""
}

func validPrivateDirectory(path string) bool {
	if strings.TrimSpace(path) == "" || strings.IndexByte(path, 0) >= 0 {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 && (runtime.GOOS == "windows" || info.Mode().Perm()&0o077 == 0)
}

func validRestoreInputs(request Request) bool {
	if !validPrivateDirectory(request.PackageDir) || !validPrivateDirectory(request.TargetRoot) || !validCandidate(request.CandidateConfig) {
		return false
	}
	target, err := filepath.Abs(request.TargetRoot)
	if err != nil {
		return false
	}
	candidate, err := filepath.Abs(request.CandidateConfig)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(target, candidate)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validArtifact(artifact Artifact) bool {
	if artifact.LogicalID != "core-state-cohort" || artifact.SizeBytes <= 0 || len(artifact.SHA256) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(artifact.SHA256)
	return err == nil && artifact.SHA256 == strings.ToLower(artifact.SHA256)
}

func writeReceipt(writer io.Writer, receipt Receipt, successCode int) int {
	data, err := json.Marshal(receipt)
	if err != nil || len(data)+1 > MaxJSONBytes {
		return ExitWriterFailure
	}
	data = append(data, '\n')
	n, err := writer.Write(data)
	if err != nil || n != len(data) {
		return ExitWriterFailure
	}
	return successCode
}
