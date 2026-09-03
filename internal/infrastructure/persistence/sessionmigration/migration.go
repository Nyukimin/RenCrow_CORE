package sessionmigration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const ContractVersion = "rencrow-session-identity-migration/v1"

type Options struct {
	Mode          string
	SourceDir     string
	OutputDir     string
	ReceiptPath   string
	DryRunReceipt string
}

type Receipt struct {
	ContractVersion         string `json:"contract_version"`
	Status                  string `json:"status"`
	Mode                    string `json:"mode"`
	SourceSHA256            string `json:"source_sha256"`
	MappingSHA256           string `json:"mapping_sha256"`
	OutputSHA256            string `json:"output_sha256,omitempty"`
	SourceFiles             int    `json:"source_files"`
	LegacySessions          int    `json:"legacy_sessions"`
	CanonicalSessions       int    `json:"canonical_sessions"`
	NonSessionFiles         int    `json:"non_session_files"`
	OutputCanonicalSessions int    `json:"output_canonical_sessions"`
	LegacySessionsRemaining int    `json:"legacy_sessions_remaining"`
}

type plannedFile struct {
	name string
	data []byte
}

func Run(ctx context.Context, options Options) (Receipt, error) {
	receipt := Receipt{ContractVersion: ContractVersion, Mode: options.Mode, Status: "blocked"}
	if options.Mode != "dry-run" && options.Mode != "apply" {
		return receipt, errors.New("mode must be dry-run or apply")
	}
	source, err := cleanExistingDir(options.SourceDir)
	if err != nil {
		return receipt, err
	}
	receiptPath, err := cleanOutputFile(options.ReceiptPath, source)
	if err != nil {
		return receipt, err
	}
	var output string
	var dryRunReceiptPath string
	if options.Mode == "apply" {
		output, err = cleanFreshDir(options.OutputDir)
		if err != nil {
			return receipt, err
		}
		if output == source {
			return receipt, errors.New("source and output directories must differ")
		}
		if pathWithin(receiptPath, output) {
			return receipt, errors.New("receipt must be outside source and output directories")
		}
		dryRunReceiptPath, err = cleanExistingFile(options.DryRunReceipt)
		if err != nil {
			return receipt, fmt.Errorf("load dry-run receipt: %w", err)
		}
		if pathWithin(dryRunReceiptPath, source) || pathWithin(dryRunReceiptPath, output) {
			return receipt, errors.New("dry-run receipt must be outside source and output directories")
		}
	}
	plan, mappings, counts, _, sourceHash, err := prepare(ctx, source)
	if err != nil {
		return receipt, err
	}
	receipt.SourceSHA256 = sourceHash
	receipt.MappingSHA256 = hashStrings(mappings)
	receipt.OutputSHA256 = hashPlan(plan)
	receipt.SourceFiles = len(plan)
	receipt.LegacySessions = counts[0]
	receipt.CanonicalSessions = counts[1]
	receipt.NonSessionFiles = counts[2]
	receipt.OutputCanonicalSessions = counts[0] + counts[1]
	if options.Mode == "apply" {
		prior, err := readReceipt(dryRunReceiptPath)
		if err != nil {
			return receipt, fmt.Errorf("load dry-run receipt: %w", err)
		}
		if prior.ContractVersion != ContractVersion || prior.Status != "ready" || prior.Mode != "dry-run" || prior.SourceSHA256 != receipt.SourceSHA256 || prior.MappingSHA256 != receipt.MappingSHA256 || prior.OutputSHA256 != receipt.OutputSHA256 || prior.SourceFiles != receipt.SourceFiles || prior.LegacySessions != receipt.LegacySessions || prior.CanonicalSessions != receipt.CanonicalSessions || prior.NonSessionFiles != receipt.NonSessionFiles || prior.OutputCanonicalSessions != receipt.OutputCanonicalSessions || prior.LegacySessionsRemaining != 0 {
			return receipt, errors.New("dry-run receipt does not match source plan")
		}
		for _, item := range plan {
			if err := writeAtomic(filepath.Join(output, item.name), item.data); err != nil {
				return receipt, err
			}
		}
		outputHash, err := hashDirectory(output)
		if err != nil {
			return receipt, err
		}
		if outputHash != receipt.OutputSHA256 {
			return receipt, errors.New("materialized output does not match dry-run plan")
		}
		_, _, outputCounts, canonicalIDs, _, err := prepare(ctx, output)
		if err != nil {
			return receipt, fmt.Errorf("verify materialized output: %w", err)
		}
		if outputCounts[0] != 0 || outputCounts[1] != receipt.OutputCanonicalSessions || outputCounts[2] != receipt.NonSessionFiles {
			return receipt, errors.New("materialized output identity counts are invalid")
		}
		if err := verifyCanonicalSessions(ctx, output, canonicalIDs); err != nil {
			return receipt, fmt.Errorf("verify materialized Session reconstruction: %w", err)
		}
	}
	receipt.Status = "ready"
	if err := writeReceipt(receiptPath, receipt); err != nil {
		receipt.Status = "blocked"
		return receipt, err
	}
	return receipt, nil
}

func prepare(ctx context.Context, source string) ([]plannedFile, []string, [3]int, []string, string, error) {
	entries, err := os.ReadDir(source)
	if err != nil {
		return nil, nil, [3]int{}, nil, "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	plan := make([]plannedFile, 0, len(entries))
	mappings := []string{}
	canonicalIDs := []string{}
	counts := [3]int{}
	seen := map[string]struct{}{}
	sourceHasher := sha256.New()
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, counts, nil, "", err
		}
		if entry.IsDir() {
			return nil, nil, counts, nil, "", errors.New("nested session source entries are not supported")
		}
		raw, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return nil, nil, counts, nil, "", err
		}
		sourceHasher.Write([]byte(entry.Name()))
		sourceHasher.Write([]byte{0})
		sourceHasher.Write(raw)
		sourceHasher.Write([]byte{0})
		name, output, kind, mapping, err := transform(entry.Name(), raw)
		if err != nil {
			return nil, nil, counts, nil, "", fmt.Errorf("transform session source: %w", err)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, nil, counts, nil, "", errors.New("migration output filename collision")
		}
		seen[name] = struct{}{}
		plan = append(plan, plannedFile{name: name, data: output})
		counts[kind]++
		if mapping != "" {
			mappings = append(mappings, mapping)
		}
		if kind == 1 {
			canonicalIDs = append(canonicalIDs, strings.TrimSuffix(name, ".json"))
		}
	}
	plannedSourceHash := hex.EncodeToString(sourceHasher.Sum(nil))
	stableSourceHash, err := hashDirectory(source)
	if err != nil {
		return nil, nil, counts, nil, "", err
	}
	if plannedSourceHash != stableSourceHash {
		return nil, nil, counts, nil, "", errors.New("session source changed while preparing migration")
	}
	return plan, mappings, counts, canonicalIDs, plannedSourceHash, nil
}

func verifyCanonicalSessions(ctx context.Context, output string, canonicalIDs []string) error {
	repository := sessionpersistence.NewJSONSessionRepository(output)
	for _, id := range canonicalIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		loaded, err := repository.Load(ctx, id)
		if err != nil {
			return err
		}
		if loaded.ID() != id {
			return errors.New("reconstructed Session identity does not match filename")
		}
	}
	return nil
}

func transform(name string, raw []byte) (string, []byte, int, string, error) {
	if filepath.Ext(name) != ".json" {
		return name, raw, 2, "", nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return "", nil, 0, "", err
	}
	required := []string{"id", "history", "memory", "created_at", "updated_at"}
	sessionSignals := append(append([]string(nil), required...), "logical_date", "channel_address", "channel", "chat_id")
	signalCount := 0
	for _, key := range sessionSignals {
		if _, ok := fields[key]; ok {
			signalCount++
		}
	}
	if signalCount == 0 {
		return name, raw, 2, "", nil
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return "", nil, 0, "", errors.New("partial session schema is not migratable")
		}
	}
	var id, createdAt string
	if json.Unmarshal(fields["id"], &id) != nil || json.Unmarshal(fields["created_at"], &createdAt) != nil || id == "" {
		return "", nil, 0, "", errors.New("invalid session identity fields")
	}
	if _, canonicalDate := fields["logical_date"]; canonicalDate {
		if err := modulecore.SessionID(id).Validate(); err != nil {
			return "", nil, 0, "", err
		}
		if _, ok := fields["channel_address"]; !ok {
			return "", nil, 0, "", errors.New("canonical session lacks channel_address")
		}
		if _, ok := fields["channel"]; ok {
			return "", nil, 0, "", errors.New("canonical session contains legacy channel")
		}
		if _, ok := fields["chat_id"]; ok {
			return "", nil, 0, "", errors.New("canonical session contains legacy chat_id")
		}
		var logicalDate string
		var channelAddress domainsession.ChannelAddress
		if json.Unmarshal(fields["logical_date"], &logicalDate) != nil || domainsession.ValidateLogicalDate(logicalDate) != nil || json.Unmarshal(fields["channel_address"], &channelAddress) != nil || channelAddress.Validate() != nil {
			return "", nil, 0, "", errors.New("canonical session lookup attributes are invalid")
		}
		return id + ".json", raw, 1, "", nil
	}
	var channel, address string
	if json.Unmarshal(fields["channel"], &channel) != nil || json.Unmarshal(fields["chat_id"], &address) != nil {
		return "", nil, 0, "", errors.New("legacy session lacks routing attributes")
	}
	channelAddress, err := domainsession.NewChannelAddress(channel, address)
	if err != nil {
		return "", nil, 0, "", err
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return "", nil, 0, "", errors.New("legacy session created_at is invalid")
	}
	newID, err := modulecore.NewMigrationID(modulecore.CanonicalSessionID, "session_files", "id", id)
	if err != nil {
		return "", nil, 0, "", err
	}
	fields["id"], _ = json.Marshal(newID)
	fields["logical_date"], _ = json.Marshal(created.UTC().Format("2006-01-02"))
	fields["channel_address"], _ = json.Marshal(channelAddress)
	delete(fields, "channel")
	delete(fields, "chat_id")
	output, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		return "", nil, 0, "", err
	}
	output = append(output, '\n')
	return newID + ".json", output, 0, id + "\x00" + newID, nil
}

func cleanExistingDir(path string) (string, error) {
	clean, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil || strings.TrimSpace(path) == "" {
		return "", errors.New("source directory is required")
	}
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return "", errors.New("source directory is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", errors.New("source directory is unavailable")
	}
	return resolved, nil
}

func cleanFreshDir(path string) (string, error) {
	clean, err := cleanExistingDir(path)
	if err != nil {
		return "", errors.New("output directory must exist and be empty")
	}
	entries, err := os.ReadDir(clean)
	if err != nil || len(entries) != 0 {
		return "", errors.New("output directory must exist and be empty")
	}
	return clean, nil
}

func cleanOutputFile(path string, forbiddenDirs ...string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("receipt path is required")
	}
	clean, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return "", errors.New("receipt path is invalid")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(clean))
	if err != nil {
		return "", errors.New("receipt parent directory is unavailable")
	}
	clean = filepath.Join(parent, filepath.Base(clean))
	if _, err := os.Lstat(clean); err == nil {
		return "", errors.New("receipt target must not exist")
	} else if !os.IsNotExist(err) {
		return "", errors.New("receipt target is unavailable")
	}
	for _, forbidden := range forbiddenDirs {
		if pathWithin(clean, forbidden) {
			return "", errors.New("receipt must be outside source and output directories")
		}
	}
	return clean, nil
}

func cleanExistingFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("dry-run receipt path is required")
	}
	clean, err := filepath.Abs(filepath.Clean(strings.TrimSpace(path)))
	if err != nil {
		return "", errors.New("dry-run receipt path is invalid")
	}
	resolved, err := filepath.EvalSymlinks(clean)
	if err != nil {
		return "", errors.New("dry-run receipt is unavailable")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("dry-run receipt is unavailable")
	}
	return resolved, nil
}

func pathWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func hashDirectory(path string) (string, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	h := sha256.New()
	for _, entry := range entries {
		if entry.IsDir() {
			return "", errors.New("nested entries are not supported")
		}
		raw, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			return "", err
		}
		h.Write([]byte(entry.Name()))
		h.Write([]byte{0})
		h.Write(raw)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashStrings(values []string) string {
	sort.Strings(values)
	h := sha256.New()
	for _, value := range values {
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashPlan(plan []plannedFile) string {
	ordered := append([]plannedFile(nil), plan...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].name < ordered[j].name })
	h := sha256.New()
	for _, item := range ordered {
		h.Write([]byte(item.name))
		h.Write([]byte{0})
		h.Write(item.data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func readReceipt(path string) (Receipt, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, err
	}
	if len(raw) > 64*1024 {
		return Receipt{}, errors.New("dry-run receipt exceeds size limit")
	}
	var receipt Receipt
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return Receipt{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Receipt{}, errors.New("dry-run receipt contains trailing data")
	}
	return receipt, nil
}

func writeReceipt(path string, receipt Receipt) error {
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return writeAtomic(path, append(raw, '\n'))
}

func writeAtomic(path string, raw []byte) error {
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".session-migration-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0600); err != nil {
		return err
	}
	if _, err := temporary.Write(raw); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}
