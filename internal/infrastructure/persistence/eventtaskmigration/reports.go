package eventtaskmigration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const maxExecutionReportLineBytes = 16 << 20

type migratedExecutionReport struct {
	legacyJobID   string
	report        domainexecution.ExecutionReport
	canonicalJSON []byte
}

type executionReportCounts struct {
	byEvent int
	derived int
}

func loadAndMigrateExecutionReports(ctx context.Context, path string, eventTasks map[string]map[modulecore.TaskID]struct{}) ([]migratedExecutionReport, executionReportCounts, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, executionReportCounts{}, "", coded("report_source_invalid", "open execution report snapshot: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxExecutionReportLineBytes)
	reports := make([]migratedExecutionReport, 0)
	seenJobs := make(map[string]struct{})
	counts := executionReportCounts{}
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		if err := ctx.Err(); err != nil {
			return nil, executionReportCounts{}, "", err
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			return nil, executionReportCounts{}, "", coded("report_row_invalid", "execution report row %d is blank", lineNumber)
		}
		object, err := decodeJSONObject(line)
		if err != nil {
			return nil, executionReportCounts{}, "", coded("report_row_invalid", "execution report row %d: %v", lineNumber, err)
		}
		if _, exists := object["task_id"]; exists {
			return nil, executionReportCounts{}, "", coded("report_row_invalid", "execution report row %d already contains task_id", lineNumber)
		}
		legacyValue, exists := object["job_id"]
		legacyJob, ok := legacyValue.(string)
		if !exists || !ok || legacyJob == "" {
			return nil, executionReportCounts{}, "", coded("report_row_invalid", "execution report row %d requires a non-empty string job_id", lineNumber)
		}
		if _, duplicate := seenJobs[legacyJob]; duplicate {
			return nil, executionReportCounts{}, "", coded("report_job_duplicate", "execution report snapshot contains duplicate job_id rows")
		}
		seenJobs[legacyJob] = struct{}{}
		var taskID modulecore.TaskID
		candidates := eventTasks[legacyJob]
		if len(candidates) > 1 {
			return nil, executionReportCounts{}, "", coded("report_job_ambiguous", "execution report job_id matches multiple per-trace Event TaskID values")
		}
		if len(candidates) == 1 {
			for candidate := range candidates {
				taskID = candidate
			}
			counts.byEvent++
		} else {
			raw, err := modulecore.NewMigrationID(modulecore.CanonicalTaskID, "execution_report", "job_id", legacyJob)
			if err != nil {
				return nil, executionReportCounts{}, "", coded("report_identity_invalid", "execution report row %d: %v", lineNumber, err)
			}
			taskID = modulecore.TaskID(raw)
			counts.derived++
		}
		delete(object, "job_id")
		object["task_id"] = string(taskID)
		canonical, err := json.Marshal(object)
		if err != nil {
			return nil, executionReportCounts{}, "", coded("report_row_invalid", "execution report row %d: %v", lineNumber, err)
		}
		report, err := decodeAndValidateCurrentExecutionReport(canonical)
		if err != nil {
			return nil, executionReportCounts{}, "", coded("report_contract_invalid", "execution report row %d cannot round-trip current contract: %v", lineNumber, err)
		}
		if report.TaskID != taskID {
			return nil, executionReportCounts{}, "", coded("report_identity_invalid", "execution report row %d changed task_id", lineNumber)
		}
		reports = append(reports, migratedExecutionReport{legacyJobID: legacyJob, report: report, canonicalJSON: canonical})
	}
	if err := scanner.Err(); err != nil {
		return nil, executionReportCounts{}, "", coded("report_source_invalid", "scan execution report snapshot: %v", err)
	}
	digest := sha256.Sum256(encodeExecutionReports(reports))
	return reports, counts, hex.EncodeToString(digest[:]), nil
}

func executionReportTaskMappings(reports []migratedExecutionReport) (map[string]modulecore.TaskID, error) {
	mappings := make(map[string]modulecore.TaskID, len(reports))
	for _, report := range reports {
		if report.legacyJobID == "" {
			return nil, coded("report_identity_invalid", "migrated execution report is missing legacy job_id correlation")
		}
		if existing, ok := mappings[report.legacyJobID]; ok && existing != report.report.TaskID {
			return nil, coded("report_job_ambiguous", "legacy execution report job_id maps to multiple TaskID values")
		}
		mappings[report.legacyJobID] = report.report.TaskID
	}
	return mappings, nil
}

func decodeJSONObject(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := decodeUniqueJSONValue(decoder)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok || object == nil {
		return nil, errors.New("JSON value must be an object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("row has trailing JSON")
	}
	return object, nil
}

func decodeUniqueJSONValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, composite := token.(json.Delim)
	if !composite {
		return token, nil
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON object key is not a string")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, fmt.Errorf("duplicate JSON object key %q", key)
			}
			value, err := decodeUniqueJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return nil, errors.New("JSON object is not closed")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeUniqueJSONValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return nil, errors.New("JSON array is not closed")
		}
		return array, nil
	default:
		return nil, errors.New("unexpected JSON delimiter")
	}
}

func decodeAndValidateCurrentExecutionReport(data []byte) (domainexecution.ExecutionReport, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var report domainexecution.ExecutionReport
	if err := decoder.Decode(&report); err != nil {
		return domainexecution.ExecutionReport{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domainexecution.ExecutionReport{}, errors.New("report has trailing JSON")
	}
	if err := report.Validate(); err != nil {
		return domainexecution.ExecutionReport{}, err
	}
	return report, nil
}

func encodeExecutionReports(reports []migratedExecutionReport) []byte {
	var output bytes.Buffer
	for _, report := range reports {
		output.Write(report.canonicalJSON)
		output.WriteByte('\n')
	}
	return output.Bytes()
}

func writeAndVerifyExecutionReports(path string, reports []migratedExecutionReport, expectedSHA string) (resultErr error) {
	if err := requireAbsentTarget(path); err != nil {
		return coded("report_target_exists", "%v", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return coded("report_target", "create execution report target directory: %v", err)
	}
	temporary, err := os.CreateTemp(dir, ".rencrow-execution-report-migrate-*.tmp")
	if err != nil {
		return coded("report_target", "create execution report temporary file: %v", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return coded("report_target", "secure execution report temporary file: %v", err)
	}
	if _, err := temporary.Write(encodeExecutionReports(reports)); err != nil {
		_ = temporary.Close()
		return coded("report_target", "write execution report temporary file: %v", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return coded("report_target", "sync execution report temporary file: %v", err)
	}
	if err := temporary.Close(); err != nil {
		return coded("report_target", "close execution report temporary file: %v", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return coded("report_target", "publish execution report target: %v", err)
	}
	published := true
	defer func() {
		if resultErr != nil && published {
			if cleanupErr := cleanupAppliedTargets(resolvedPaths{targetExecutionReports: path}, false, true, false); cleanupErr != nil {
				resultErr = fmt.Errorf("%w; clean failed execution report target: %v", resultErr, cleanupErr)
			}
		}
	}()
	if err := os.Chmod(path, 0o600); err != nil {
		return coded("report_target", "secure execution report target: %v", err)
	}
	if err := verifyExecutionReportTarget(path, reports, expectedSHA); err != nil {
		return err
	}
	published = false
	return nil
}

func verifyExecutionReportTarget(path string, expected []migratedExecutionReport, expectedSHA string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return coded("report_target_verify", "execution report target is missing or unsafe")
	}
	digest, err := hashFile(path)
	if err != nil || digest != expectedSHA {
		return coded("report_target_verify", "execution report target checksum mismatch")
	}
	file, err := os.Open(path)
	if err != nil {
		return coded("report_target_verify", "open execution report target: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), maxExecutionReportLineBytes)
	row := 0
	for scanner.Scan() {
		if row >= len(expected) {
			return coded("report_target_verify", "execution report target has extra rows")
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.Equal(line, expected[row].canonicalJSON) {
			return coded("report_target_verify", "execution report target row %d differs from plan", row+1)
		}
		actual, err := decodeAndValidateCurrentExecutionReport(line)
		if err != nil || !reflect.DeepEqual(actual, expected[row].report) {
			return coded("report_target_verify", "execution report target row %d fails current contract", row+1)
		}
		row++
	}
	if err := scanner.Err(); err != nil {
		return coded("report_target_verify", "scan execution report target: %v", err)
	}
	if row != len(expected) {
		return coded("report_target_verify", "execution report target row count differs from plan")
	}
	return nil
}
