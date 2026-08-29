package migrationhook

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"regexp"
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
	ExitWriterFailure  = 30
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type Request struct {
	ContractVersion string `json:"contract_version"`
	Operation       string `json:"operation"`
	Owner           string `json:"owner"`
	RequestID       string `json:"request_id"`
	CandidateConfig string `json:"candidate_config,omitempty"`
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
	Artifact        any            `json:"artifact"`
	Counts          map[string]int `json:"counts"`
	Failure         *Failure       `json:"failure"`
}

type ConfigValidator func(candidatePath string) error

func Run(args []string, stdin io.Reader, stdout io.Writer, validateConfig ConfigValidator) int {
	if len(args) != 0 || stdin == nil || stdout == nil || validateConfig == nil {
		return ExitInvalidRequest
	}
	request, ok := decodeRequest(stdin)
	if !ok {
		return ExitInvalidRequest
	}

	var receipt Receipt
	switch request.Operation {
	case "state_describe":
		if request.CandidateConfig != "" {
			return ExitInvalidRequest
		}
		receipt = baseReceipt(request)
		receipt.Status = "completed"
	case "config_validate":
		if !validCandidate(request.CandidateConfig) {
			receipt = rejectedConfigReceipt(request)
			return writeReceipt(stdout, receipt, ExitRejected)
		}
		if err := validateConfig(request.CandidateConfig); err != nil {
			receipt = rejectedConfigReceipt(request)
			return writeReceipt(stdout, receipt, ExitRejected)
		}
		receipt = baseReceipt(request)
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
	if request.Operation != "config_validate" && request.Operation != "state_describe" {
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

func baseReceipt(request Request) Receipt {
	return Receipt{
		ContractVersion: ResponseContractVersion,
		Operation:       request.Operation,
		Owner:           Owner,
		RequestID:       request.RequestID,
		StateClass:      "durable",
		SchemaRevision:  "rencrow-core-migration-state/v1",
		ConsistencyMode: "module_backup_api",
		Operations:      []string{},
		Artifact:        nil,
		Counts:          map[string]int{},
		Failure:         nil,
	}
}

func rejectedConfigReceipt(request Request) Receipt {
	receipt := baseReceipt(request)
	receipt.Status = "rejected"
	receipt.Failure = &Failure{Code: "config_invalid", Boundary: "CORE configuration was rejected"}
	return receipt
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
