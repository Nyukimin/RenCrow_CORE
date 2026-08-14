package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

var runtimeToolRegistryNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

type runtimeToolRegistryWritePayload struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	SchemaJSON  string   `json:"schema_json"`
	Platforms   []string `json:"platforms"`
}

type runtimeToolRegistryWriter struct {
	mu           sync.Mutex
	workspaceDir string
	registry     capdomain.ToolRegistryReceiptOwner
}

// registerRuntimeDataWriteToolRegistry installs the internal Owner route for
// registering an already-existing workspace script. The caller supplies the
// trusted runtime registry; model payloads never select a database or path.
func registerRuntimeDataWriteToolRegistry(r *runtimeDataWriteRegistry, workspaceDir string, runtimeToolRegistry capdomain.ToolRegistry) error {
	if r == nil || runtimeToolRegistry == nil {
		return fmt.Errorf("tool registry data write unavailable")
	}
	owner, ok := runtimeToolRegistry.(capdomain.ToolRegistryReceiptOwner)
	if !ok || owner == nil {
		return fmt.Errorf("tool registry receipt owner unavailable")
	}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return fmt.Errorf("tool registry workspace directory is required")
	}
	writer := &runtimeToolRegistryWriter{workspaceDir: workspaceDir, registry: owner}
	return r.RegisterWithContract("tool_registry", "register_existing_script", dataRecallAccessInternal, runtimeDataWriteContract{
		RequiredPayloadFields: []string{"description", "name", "platforms", "schema_json"},
	}, writer.write)
}

func (w *runtimeToolRegistryWriter) write(ctx context.Context, request tools.DataWriteRequest) (runtimeDataWriteOwnerResult, error) {
	if w == nil || w.registry == nil {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("tool registry receipt owner unavailable")
	}
	scope, err := runtimeDataWriteOwnerScope(ctx)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	payload, err := decodeRuntimeToolRegistryWritePayload(request.Payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	scriptPath, err := resolveRuntimeToolRegistryScriptPath(w.workspaceDir, payload.Name)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	canonicalPayload, payloadHash, err := canonicalRuntimeToolRegistryPayload(payload)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}

	// Serialize the adapter's validation and persistence call. The store also
	// serializes its transaction, so this is only a small in-process guard for
	// alternate receipt-owner implementations used by tests or deployments.
	w.mu.Lock()
	defer w.mu.Unlock()
	result, err := w.registry.RegisterWithReceipt(ctx, capdomain.ToolEntry{
		Name:        payload.Name,
		Description: payload.Description,
		SchemaJSON:  canonicalPayload.SchemaJSON,
		Platforms:   append([]string(nil), canonicalPayload.Platforms...),
		Source:      capdomain.ToolSource(scriptPath),
		CreatedBy:   strings.TrimSpace(scope.ActorID),
	}, scope.RequestID, scope.ActorID, payloadHash)
	if err != nil {
		return runtimeDataWriteOwnerResult{}, err
	}
	if strings.TrimSpace(result.Receipt.RequestID) != strings.TrimSpace(scope.RequestID) || strings.TrimSpace(result.Receipt.ToolName) != payload.Name {
		return runtimeDataWriteOwnerResult{}, fmt.Errorf("tool registry receipt identity mismatch")
	}
	return runtimeDataWriteOwnerResult{
		SchemaVersion:    "tool-registry/v1",
		MigrationState:   "embedded_current",
		ValidationState:  "owner_validated",
		AuditRef:         result.Receipt.RequestID,
		IdempotencyKey:   scope.RequestID,
		IdempotentReplay: result.RequestReplay,
		PolicyRevision:   runtimeDataWritePolicyRevision,
	}, nil
}

func decodeRuntimeToolRegistryWritePayload(payload map[string]any) (runtimeToolRegistryWritePayload, error) {
	if err := validateRuntimeDataWritePayloadKeys(payload, map[string]struct{}{
		"name": {}, "description": {}, "schema_json": {}, "platforms": {},
	}); err != nil {
		return runtimeToolRegistryWritePayload{}, err
	}
	var decoded runtimeToolRegistryWritePayload
	if err := decodeRuntimeDataWritePayload(payload, &decoded); err != nil {
		return runtimeToolRegistryWritePayload{}, err
	}
	decoded.Name = strings.TrimSpace(decoded.Name)
	decoded.Description = strings.TrimSpace(decoded.Description)
	decoded.SchemaJSON = strings.TrimSpace(decoded.SchemaJSON)
	if decoded.Name == "" || !runtimeToolRegistryNamePattern.MatchString(decoded.Name) {
		return runtimeToolRegistryWritePayload{}, fmt.Errorf("name must contain only alphanumeric characters and underscores")
	}
	if len(decoded.Name) > 64 {
		return runtimeToolRegistryWritePayload{}, fmt.Errorf("name must not exceed 64 characters")
	}
	if decoded.Description == "" || !utf8.ValidString(decoded.Description) || utf8.RuneCountInString(decoded.Description) > 1000 {
		return runtimeToolRegistryWritePayload{}, fmt.Errorf("description is required")
	}
	if decoded.SchemaJSON == "" || len(decoded.SchemaJSON) > 64<<10 {
		return runtimeToolRegistryWritePayload{}, fmt.Errorf("schema_json is required")
	}
	if len(decoded.Platforms) == 0 || len(decoded.Platforms) > 3 {
		return runtimeToolRegistryWritePayload{}, fmt.Errorf("platforms is required")
	}
	platforms := make([]string, 0, len(decoded.Platforms))
	seen := make(map[string]struct{}, len(decoded.Platforms))
	for _, platform := range decoded.Platforms {
		platform = strings.ToLower(strings.TrimSpace(platform))
		switch platform {
		case "linux", "windows", "darwin":
		default:
			return runtimeToolRegistryWritePayload{}, fmt.Errorf("platforms contains an unsupported value")
		}
		if _, exists := seen[platform]; exists {
			return runtimeToolRegistryWritePayload{}, fmt.Errorf("platforms must not contain duplicates")
		}
		seen[platform] = struct{}{}
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	decoded.Platforms = platforms
	return decoded, nil
}

func canonicalRuntimeToolRegistryPayload(payload runtimeToolRegistryWritePayload) (runtimeToolRegistryWritePayload, string, error) {
	schemaJSON, err := canonicalRuntimeToolRegistryJSON(payload.SchemaJSON)
	if err != nil {
		return runtimeToolRegistryWritePayload{}, "", fmt.Errorf("schema_json is invalid: %w", err)
	}
	canonical := runtimeToolRegistryWritePayload{
		Name:        strings.TrimSpace(payload.Name),
		Description: strings.TrimSpace(payload.Description),
		SchemaJSON:  schemaJSON,
		Platforms:   append([]string(nil), payload.Platforms...),
	}
	sort.Strings(canonical.Platforms)
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return runtimeToolRegistryWritePayload{}, "", fmt.Errorf("canonicalize tool registry payload: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return canonical, hex.EncodeToString(digest[:]), nil
}

func canonicalRuntimeToolRegistryJSON(raw string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	if _, ok := value.(map[string]any); !ok {
		return "", fmt.Errorf("schema_json must be a JSON object")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return "", fmt.Errorf("trailing JSON value")
		}
		return "", err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func resolveRuntimeToolRegistryScriptPath(workspaceDir, name string) (string, error) {
	workspaceDir = strings.TrimSpace(workspaceDir)
	name = strings.TrimSpace(name)
	if workspaceDir == "" {
		return "", fmt.Errorf("workspace directory is required")
	}
	if name == "" || !runtimeToolRegistryNamePattern.MatchString(name) {
		return "", fmt.Errorf("tool name is invalid")
	}
	workspaceAbs, err := filepath.Abs(filepath.Clean(workspaceDir))
	if err != nil {
		return "", fmt.Errorf("resolve workspace directory: %w", err)
	}
	toolsDir := filepath.Join(workspaceAbs, "tools")
	toolsAbs, err := filepath.Abs(filepath.Clean(toolsDir))
	if err != nil {
		return "", fmt.Errorf("resolve workspace tools directory: %w", err)
	}
	scriptAbs, err := filepath.Abs(filepath.Clean(filepath.Join(toolsAbs, name+".sh")))
	if err != nil {
		return "", fmt.Errorf("resolve workspace tool script: %w", err)
	}
	rel, err := filepath.Rel(toolsAbs, scriptAbs)
	if err != nil || rel == "." || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("tool script path escapes workspace tools directory")
	}
	toolsInfo, err := os.Lstat(toolsAbs)
	if err != nil {
		return "", fmt.Errorf("workspace tools directory is unavailable: %w", err)
	}
	if toolsInfo.Mode()&os.ModeSymlink != 0 || !toolsInfo.IsDir() {
		return "", fmt.Errorf("workspace tools directory must be a real directory")
	}
	scriptInfo, err := os.Lstat(scriptAbs)
	if err != nil {
		return "", fmt.Errorf("workspace tool script is unavailable: %w", err)
	}
	if scriptInfo.Mode()&os.ModeSymlink != 0 || !scriptInfo.Mode().IsRegular() {
		return "", fmt.Errorf("workspace tool script must be a regular non-symlink file")
	}
	if scriptInfo.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("workspace tool script must be executable")
	}
	resolved, err := filepath.EvalSymlinks(scriptAbs)
	if err != nil {
		return "", fmt.Errorf("resolve workspace tool script: %w", err)
	}
	if filepath.Clean(resolved) != filepath.Clean(scriptAbs) {
		return "", fmt.Errorf("workspace tool script resolves outside its canonical path")
	}
	return scriptAbs, nil
}
