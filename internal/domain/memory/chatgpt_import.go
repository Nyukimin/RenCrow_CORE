package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// ChatGPT import constants are part of the cross-module bundle contract. The
// ledger does not parse a bundle; it only records the binding that a future
// artifact service has already authenticated and validated.
const (
	ChatGPTImportBundleFormat     = "rencrow.chatgpt_common_raw_bundle.v1"
	ChatGPTImportRecordSchema     = "rencrow.chatgpt_l3.v1"
	ChatGPTImportConverterVersion = "chatgpt-export-memory-go/v2"
	ChatGPTImportPolicyRevision   = "chatgpt-import-ledger/v1"
	ChatGPTL3ArtifactFormat       = ChatGPTImportRecordSchema
)

// ChatGPTL3ImportRecord is the public message contract shared by the import
// application and persistence adapters. It contains the already-authenticated
// export record but no owner, request, or runtime state.
type ChatGPTL3ImportRecord struct {
	Format                string
	ExportID              string
	EvidenceID            string
	ConversationID        string
	ConversationTitle     string
	ConversationCreatedAt time.Time
	ConversationUpdatedAt time.Time
	NodeID                string
	ParentNodeID          string
	ChildNodeIDs          []string
	OnCurrentBranch       bool
	MessageID             string
	MessageCreatedAt      time.Time
	Role                  string
	ContentType           string
	Text                  string
	Content               json.RawMessage
	Metadata              json.RawMessage
}

// ChatGPTL3ImportResult reports the legacy L3 import outcome.
type ChatGPTL3ImportResult struct {
	Validated int `json:"validated"`
	Imported  int `json:"imported"`
	Existing  int `json:"existing"`
	Queued    int `json:"queued_for_projection"`
}

// ChatGPTL3ConfirmResult reports candidate confirmation and projection state.
type ChatGPTL3ConfirmResult struct {
	Matched             int `json:"matched"`
	Confirmed           int `json:"confirmed"`
	ProjectionPending   int `json:"projection_pending"`
	ProjectionRunning   int `json:"projection_running"`
	ProjectionRetryWait int `json:"projection_retry_wait"`
	ProjectionFailed    int `json:"projection_failed"`
	ProjectionCompleted int `json:"projection_completed"`
}

// ChatGPTRawImportBatch is one bounded, internally checkpointed ChatGPT
// export batch. ExportID is normally derived from Records, but may be set by
// a trusted adapter to make the batch binding explicit.
type ChatGPTRawImportBatch struct {
	ExportID         string                  `json:"export_id,omitempty"`
	ManifestSHA256   string                  `json:"manifest_sha256"`
	ArtifactSHA256   string                  `json:"artifact_sha256"`
	SourceCount      int                     `json:"source_count"`
	SchemaVersion    string                  `json:"schema_version"`
	ConverterVersion string                  `json:"converter_version"`
	BatchIndex       int                     `json:"batch_index"`
	BatchCount       int                     `json:"batch_count"`
	StartLine        int                     `json:"start_line"`
	Records          []ChatGPTL3ImportRecord `json:"records"`
}

// ChatGPTRawImportResult reports the Raw intake and legacy projection
// without exposing Raw payload bytes.
type ChatGPTRawImportResult struct {
	Validated              int                    `json:"validated"`
	RawImported            int                    `json:"raw_imported"`
	RawReplayed            int                    `json:"raw_replayed"`
	Projected              int                    `json:"projected"`
	Existing               int                    `json:"existing"`
	Queued                 int                    `json:"queued_for_projection"`
	RawReceipt             CommonRawIntakeReceipt `json:"raw_receipt"`
	ManifestID             string                 `json:"manifest_id"`
	RawRecordIDs           []string               `json:"raw_record_ids"`
	InternalManifestSHA256 string                 `json:"internal_manifest_sha256"`
	ExternalManifestSHA256 string                 `json:"manifest_sha256"`
	ArtifactSHA256         string                 `json:"artifact_sha256"`
	SourceCount            int                    `json:"source_count"`
	SchemaVersion          string                 `json:"schema_version"`
	ConverterVersion       string                 `json:"converter_version"`
	BatchIndex             int                    `json:"batch_index"`
	BatchCount             int                    `json:"batch_count"`
	StartLine              int                    `json:"start_line"`
}

type chatGPTRawPayload struct {
	Format                string          `json:"format"`
	ExportID              string          `json:"export_id"`
	EvidenceID            string          `json:"evidence_id"`
	ConversationID        string          `json:"conversation_id"`
	ConversationTitle     string          `json:"conversation_title"`
	ConversationCreatedAt time.Time       `json:"conversation_created_at"`
	ConversationUpdatedAt time.Time       `json:"conversation_updated_at"`
	NodeID                string          `json:"node_id"`
	ParentNodeID          string          `json:"parent_node_id"`
	ChildNodeIDs          []string        `json:"child_node_ids"`
	OnCurrentBranch       bool            `json:"on_current_branch"`
	MessageID             string          `json:"message_id"`
	MessageCreatedAt      time.Time       `json:"message_created_at"`
	Role                  string          `json:"role"`
	ContentType           string          `json:"content_type"`
	Text                  string          `json:"text"`
	Content               json.RawMessage `json:"content"`
	Metadata              json.RawMessage `json:"metadata"`
	ArtifactLine          int             `json:"artifact_line"`
	ManifestSHA256        string          `json:"manifest_sha256"`
	ArtifactSHA256        string          `json:"artifact_sha256"`
	SourceCount           int             `json:"source_count"`
	SchemaVersion         string          `json:"schema_version"`
	ConverterVersion      string          `json:"converter_version"`
	BatchIndex            int             `json:"batch_index"`
	BatchCount            int             `json:"batch_count"`
	StartLine             int             `json:"start_line"`
}

// MarshalChatGPTRawPayload returns the canonical payload stored for one Raw
// record. Its field order and UTC normalization are part of the storage
// contract, so projection code must use this helper instead of re-encoding
// the message independently.
func MarshalChatGPTRawPayload(batch ChatGPTRawImportBatch, recordIndex int) ([]byte, error) {
	if recordIndex < 0 || recordIndex >= len(batch.Records) {
		return nil, fmt.Errorf("ChatGPT Raw record index %d is out of range: %w", recordIndex, ErrChatGPTImportInvalid)
	}
	item := batch.Records[recordIndex]
	return json.Marshal(chatGPTRawPayload{
		Format:                item.Format,
		ExportID:              item.ExportID,
		EvidenceID:            item.EvidenceID,
		ConversationID:        item.ConversationID,
		ConversationTitle:     item.ConversationTitle,
		ConversationCreatedAt: item.ConversationCreatedAt.UTC(),
		ConversationUpdatedAt: item.ConversationUpdatedAt.UTC(),
		NodeID:                item.NodeID,
		ParentNodeID:          item.ParentNodeID,
		ChildNodeIDs:          item.ChildNodeIDs,
		OnCurrentBranch:       item.OnCurrentBranch,
		MessageID:             item.MessageID,
		MessageCreatedAt:      item.MessageCreatedAt.UTC(),
		Role:                  item.Role,
		ContentType:           item.ContentType,
		Text:                  item.Text,
		Content:               item.Content,
		Metadata:              item.Metadata,
		ArtifactLine:          batch.StartLine + recordIndex,
		ManifestSHA256:        batch.ManifestSHA256,
		ArtifactSHA256:        batch.ArtifactSHA256,
		SourceCount:           batch.SourceCount,
		SchemaVersion:         batch.SchemaVersion,
		ConverterVersion:      batch.ConverterVersion,
		BatchIndex:            batch.BatchIndex,
		BatchCount:            batch.BatchCount,
		StartLine:             batch.StartLine,
	})
}

// ValidateChatGPTL3ImportRecord preserves the existing import boundary
// validation without depending on a persistence implementation.
func ValidateChatGPTL3ImportRecord(item ChatGPTL3ImportRecord) error {
	if item.Format != ChatGPTL3ArtifactFormat {
		return fmt.Errorf("unsupported ChatGPT L3 artifact format: %q", item.Format)
	}
	if strings.TrimSpace(item.ExportID) == "" || strings.TrimSpace(item.ConversationID) == "" || strings.TrimSpace(item.MessageID) == "" {
		return errors.New("export_id, conversation_id, and message_id are required")
	}
	wantEvidence := "chatgpt_export:" + item.ConversationID + ":" + item.MessageID
	if item.EvidenceID != wantEvidence {
		return fmt.Errorf("evidence_id mismatch: got %q want %q", item.EvidenceID, wantEvidence)
	}
	switch item.Role {
	case "user", "assistant", "system", "tool":
	default:
		return fmt.Errorf("unsupported ChatGPT message role: %q", item.Role)
	}
	if strings.TrimSpace(item.Text) == "" && len(item.Content) == 0 {
		return errors.New("message text or content is required")
	}
	return nil
}

type ChatGPTImportState string

const (
	ChatGPTImportStateValidating ChatGPTImportState = "validating"
	ChatGPTImportStateCommitting ChatGPTImportState = "committing"
	ChatGPTImportStateCompleted  ChatGPTImportState = "completed"
	ChatGPTImportStateRejected   ChatGPTImportState = "rejected"
	ChatGPTImportStateBlocked    ChatGPTImportState = "blocked"
)

const (
	ChatGPTImportMaxWarnings       = 8
	ChatGPTImportMaxDiagnosticByte = 512
	ChatGPTImportMaxDiagnosticRune = 256
	ChatGPTImportMaxAuditByte      = 256
	ChatGPTImportMaxIdentifierByte = 512
	ChatGPTImportMaxArtifactBytes  = 64 << 30
)

type ChatGPTImportErrorCode string

const (
	ChatGPTImportErrorInvalid         ChatGPTImportErrorCode = "invalid"
	ChatGPTImportErrorTooLarge        ChatGPTImportErrorCode = "too_large"
	ChatGPTImportErrorArtifactInvalid ChatGPTImportErrorCode = "artifact_invalid"
	ChatGPTImportErrorInternal        ChatGPTImportErrorCode = "internal"
	ChatGPTImportErrorForbidden       ChatGPTImportErrorCode = "forbidden"
	ChatGPTImportErrorNotFound        ChatGPTImportErrorCode = "not_found"
	ChatGPTImportErrorConflict        ChatGPTImportErrorCode = "conflict"
	ChatGPTImportErrorSourceChanged   ChatGPTImportErrorCode = "source_changed"
	ChatGPTImportErrorUnavailable     ChatGPTImportErrorCode = "unavailable"
	ChatGPTImportErrorBlocked         ChatGPTImportErrorCode = "blocked"
)

// ChatGPTImportError is the bounded machine-readable failure contract. Its
// message is an operator-safe explanation; it never contains payload bytes or
// filesystem paths.
type ChatGPTImportError struct {
	Code    ChatGPTImportErrorCode
	Message string
}

func (e *ChatGPTImportError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

func (e *ChatGPTImportError) Is(target error) bool {
	t, ok := target.(*ChatGPTImportError)
	return ok && t != nil && e != nil && e.Code == t.Code
}

func NewChatGPTImportError(code ChatGPTImportErrorCode, message string) error {
	return &ChatGPTImportError{Code: code, Message: strings.TrimSpace(message)}
}

func ChatGPTImportErrorCodeOf(err error) ChatGPTImportErrorCode {
	var typed *ChatGPTImportError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code
	}
	return ""
}

var (
	ErrChatGPTImportInvalid         = &ChatGPTImportError{Code: ChatGPTImportErrorInvalid}
	ErrChatGPTImportTooLarge        = &ChatGPTImportError{Code: ChatGPTImportErrorTooLarge}
	ErrChatGPTImportArtifactInvalid = &ChatGPTImportError{Code: ChatGPTImportErrorArtifactInvalid}
	ErrChatGPTImportInternal        = &ChatGPTImportError{Code: ChatGPTImportErrorInternal}
	ErrChatGPTImportForbidden       = &ChatGPTImportError{Code: ChatGPTImportErrorForbidden}
	ErrChatGPTImportNotFound        = &ChatGPTImportError{Code: ChatGPTImportErrorNotFound}
	ErrChatGPTImportConflict        = &ChatGPTImportError{Code: ChatGPTImportErrorConflict}
	ErrChatGPTImportSourceChanged   = &ChatGPTImportError{Code: ChatGPTImportErrorSourceChanged}
	ErrChatGPTImportUnavailable     = &ChatGPTImportError{Code: ChatGPTImportErrorUnavailable}
	ErrChatGPTImportBlocked         = &ChatGPTImportError{Code: ChatGPTImportErrorBlocked}
)

// ChatGPTImportBinding is the strict, source-owned binding. It intentionally
// contains no owner, path, request, generated_at, or runtime state fields.
type ChatGPTImportBinding struct {
	ExportID          string `json:"export_id"`
	ManifestSHA256    string `json:"manifest_sha256"`
	ArtifactSHA256    string `json:"artifact_sha256"`
	ArtifactBytes     int64  `json:"artifact_bytes"`
	Format            string `json:"format"`
	SchemaVersion     string `json:"schema_version"`
	ConverterVersion  string `json:"converter_version"`
	SourceFileCount   int    `json:"source_file_count"`
	SourceChunkCount  int    `json:"source_chunk_count"`
	SourceObjectCount int    `json:"source_object_count"`
	MessageCount      int    `json:"message_count"`
}

// ChatGPTImportCounts is deliberately closed. New domains must not smuggle
// raw IDs, paths, payloads, or arbitrary counters into a public receipt.
type ChatGPTImportCounts struct {
	SourceCount     int `json:"source_count"`
	FileCount       int `json:"file_count"`
	ChunkCount      int `json:"chunk_count"`
	ObjectCount     int `json:"object_count"`
	MessageCount    int `json:"message_count"`
	BatchCount      int `json:"batch_count"`
	RawCount        int `json:"raw_count"`
	ProjectionCount int `json:"projection_count"`
	JobCount        int `json:"job_count"`
}

// ChatGPTImportEventInput is the trusted internal append request. HTTP/API
// adapters must derive OwnerID and ActorID from ToolExecutionScope rather than
// accepting them as user-controlled import fields.
type ChatGPTImportEventInput struct {
	RequestID      string
	OwnerID        string
	ActorID        string
	Binding        ChatGPTImportBinding
	Apply          bool
	State          ChatGPTImportState
	Counts         ChatGPTImportCounts
	Warnings       []string
	ErrorCode      string
	FailureReason  string
	AuditReference string
}

// ChatGPTImportEventRequest is a descriptive alias used by service callers.
type ChatGPTImportEventRequest = ChatGPTImportEventInput

// ChatGPTImportEvent is the append-only internal ledger event. OwnerID and
// ActorID are intentionally absent from ChatGPTImportView below.
type ChatGPTImportEvent struct {
	EventID        string               `json:"event_id"`
	ImportID       string               `json:"import_id"`
	RequestID      string               `json:"request_id"`
	OwnerID        string               `json:"owner_id"`
	ActorID        string               `json:"actor_id"`
	Binding        ChatGPTImportBinding `json:"binding"`
	BindingSHA256  string               `json:"binding_sha256"`
	Apply          bool                 `json:"apply"`
	State          ChatGPTImportState   `json:"state"`
	Counts         ChatGPTImportCounts  `json:"counts"`
	Warnings       []string             `json:"warnings"`
	ErrorCode      string               `json:"error_code"`
	FailureReason  string               `json:"failure_reason"`
	AuditReference string               `json:"audit_reference"`
	CreatedAt      time.Time            `json:"created_at"`
}

// ChatGPTImportView is the bounded status/read projection. No owner/actor,
// filesystem path, Raw ID, or content field is present by construction.
type ChatGPTImportView struct {
	EventID        string               `json:"event_id"`
	ImportID       string               `json:"import_id"`
	RequestID      string               `json:"request_id"`
	ExportID       string               `json:"export_id"`
	Binding        ChatGPTImportBinding `json:"binding"`
	BindingSHA256  string               `json:"binding_sha256"`
	Apply          bool                 `json:"apply"`
	State          ChatGPTImportState   `json:"state"`
	Counts         ChatGPTImportCounts  `json:"counts"`
	Warnings       []string             `json:"warnings"`
	ErrorCode      string               `json:"error_code"`
	FailureReason  string               `json:"failure_reason"`
	AuditReference string               `json:"audit_reference"`
	CreatedAt      time.Time            `json:"created_at"`
}

func (b ChatGPTImportBinding) Validate() error {
	if err := validatePathlessIdentifier("export_id", b.ExportID); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"manifest_sha256":   b.ManifestSHA256,
		"artifact_sha256":   b.ArtifactSHA256,
		"format":            b.Format,
		"schema_version":    b.SchemaVersion,
		"converter_version": b.ConverterVersion,
	} {
		if strings.Contains(name, "sha") {
			if err := validateSHA256(name, value); err != nil {
				return err
			}
			continue
		}
		if err := validateIdentifier(name, value); err != nil {
			return err
		}
	}
	if b.ArtifactBytes <= 0 || b.ArtifactBytes > ChatGPTImportMaxArtifactBytes {
		return fmt.Errorf("artifact_bytes must be between 1 and %d: %w", ChatGPTImportMaxArtifactBytes, ErrChatGPTImportInvalid)
	}
	if b.Format != ChatGPTImportBundleFormat || b.SchemaVersion != ChatGPTImportRecordSchema || b.ConverterVersion != ChatGPTImportConverterVersion {
		return fmt.Errorf("unsupported ChatGPT import binding version: %w", ErrChatGPTImportInvalid)
	}
	for name, value := range map[string]int{
		"source_file_count":   b.SourceFileCount,
		"source_chunk_count":  b.SourceChunkCount,
		"source_object_count": b.SourceObjectCount,
		"message_count":       b.MessageCount,
	} {
		if value < 0 {
			return fmt.Errorf("%s must be non-negative: %w", name, ErrChatGPTImportInvalid)
		}
	}
	return nil
}

func (c ChatGPTImportCounts) Validate() error {
	for name, value := range map[string]int{
		"source_count":     c.SourceCount,
		"file_count":       c.FileCount,
		"chunk_count":      c.ChunkCount,
		"object_count":     c.ObjectCount,
		"message_count":    c.MessageCount,
		"batch_count":      c.BatchCount,
		"raw_count":        c.RawCount,
		"projection_count": c.ProjectionCount,
		"job_count":        c.JobCount,
	} {
		if value < 0 {
			return fmt.Errorf("%s must be non-negative: %w", name, ErrChatGPTImportInvalid)
		}
	}
	return nil
}

func (s ChatGPTImportState) Validate() error {
	switch s {
	case ChatGPTImportStateValidating, ChatGPTImportStateCommitting, ChatGPTImportStateCompleted, ChatGPTImportStateRejected, ChatGPTImportStateBlocked:
		return nil
	default:
		return fmt.Errorf("unsupported ChatGPT import state %q: %w", s, ErrChatGPTImportInvalid)
	}
}

func (in ChatGPTImportEventInput) Validate() error {
	if err := validatePathlessIdentifier("request_id", in.RequestID); err != nil {
		return err
	}
	if err := validatePathlessIdentifier("owner_id", in.OwnerID); err != nil {
		return err
	}
	if err := validatePathlessIdentifier("actor_id", in.ActorID); err != nil {
		return err
	}
	if err := in.Binding.Validate(); err != nil {
		return err
	}
	if err := in.State.Validate(); err != nil {
		return err
	}
	if err := in.Counts.Validate(); err != nil {
		return err
	}
	if len(in.Warnings) > ChatGPTImportMaxWarnings {
		return fmt.Errorf("warnings exceed %d entries: %w", ChatGPTImportMaxWarnings, ErrChatGPTImportInvalid)
	}
	for index, warning := range in.Warnings {
		if err := validateDiagnostic(fmt.Sprintf("warnings[%d]", index), warning, ChatGPTImportMaxDiagnosticByte); err != nil {
			return err
		}
	}
	if err := validateDiagnostic("error_code", in.ErrorCode, ChatGPTImportMaxDiagnosticByte); err != nil {
		return err
	}
	if err := validateDiagnostic("failure_reason", in.FailureReason, ChatGPTImportMaxDiagnosticByte); err != nil {
		return err
	}
	if err := validateDiagnostic("audit_reference", in.AuditReference, ChatGPTImportMaxAuditByte); err != nil {
		return err
	}
	if in.State == ChatGPTImportStateRejected || in.State == ChatGPTImportStateBlocked {
		if in.ErrorCode == "" || in.FailureReason == "" {
			return fmt.Errorf("terminal failure requires error_code and failure_reason: %w", ErrChatGPTImportInvalid)
		}
	}
	return nil
}

// SHA256 returns the owner-independent canonical binding hash. generated_at,
// request IDs, and runtime state cannot enter this hash because they are not
// members of ChatGPTImportBinding.
func (b ChatGPTImportBinding) SHA256() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(struct {
		ExportID          string `json:"export_id"`
		ManifestSHA256    string `json:"manifest_sha256"`
		ArtifactSHA256    string `json:"artifact_sha256"`
		ArtifactBytes     int64  `json:"artifact_bytes"`
		Format            string `json:"format"`
		SchemaVersion     string `json:"schema_version"`
		ConverterVersion  string `json:"converter_version"`
		SourceFileCount   int    `json:"source_file_count"`
		SourceChunkCount  int    `json:"source_chunk_count"`
		SourceObjectCount int    `json:"source_object_count"`
		MessageCount      int    `json:"message_count"`
	}{
		ExportID: b.ExportID, ManifestSHA256: b.ManifestSHA256, ArtifactSHA256: b.ArtifactSHA256,
		ArtifactBytes: b.ArtifactBytes, Format: b.Format, SchemaVersion: b.SchemaVersion,
		ConverterVersion: b.ConverterVersion, SourceFileCount: b.SourceFileCount,
		SourceChunkCount: b.SourceChunkCount, SourceObjectCount: b.SourceObjectCount,
		MessageCount: b.MessageCount,
	})
	if err != nil {
		return "", fmt.Errorf("marshal ChatGPT import binding: %w", err)
	}
	digest := sha256.Sum256(append([]byte("rencrow.chatgpt-import-binding.v1\x00"), canonical...))
	return hex.EncodeToString(digest[:]), nil
}

// DeterministicChatGPTImportBindingSHA256 returns the owner-bound ledger
// binding hash. The canonical artifact binding remains owner-independent, but
// the durable ledger identity is scoped so two owners cannot share it.
func DeterministicChatGPTImportBindingSHA256(ownerID string, binding ChatGPTImportBinding) (string, error) {
	if err := validatePathlessIdentifier("owner_id", ownerID); err != nil {
		return "", err
	}
	canonicalSHA256, err := binding.SHA256()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-binding-owner.v1\x00" + strings.TrimSpace(ownerID) + "\x00" + canonicalSHA256))
	return hex.EncodeToString(digest[:]), nil
}

func DeterministicChatGPTImportID(ownerID, bindingSHA256 string) string {
	ownerID = strings.TrimSpace(ownerID)
	bindingSHA256 = strings.TrimSpace(bindingSHA256)
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-id.v1\x00" + ownerID + "\x00" + bindingSHA256))
	return "chatgpt-import:" + hex.EncodeToString(digest[:])
}

func DeterministicChatGPTImportEventID(importID, requestID string, state ChatGPTImportState) string {
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-event.v1\x00" + strings.TrimSpace(importID) + "\x00" + strings.TrimSpace(requestID) + "\x00" + string(state)))
	return "chatgpt-import-event:" + hex.EncodeToString(digest[:])
}

func (e ChatGPTImportEvent) View() ChatGPTImportView {
	return ChatGPTImportView{
		EventID: e.EventID, ImportID: e.ImportID, RequestID: e.RequestID,
		ExportID: e.Binding.ExportID, Binding: e.Binding, BindingSHA256: e.BindingSHA256,
		Apply: e.Apply, State: e.State, Counts: e.Counts,
		Warnings: append([]string(nil), e.Warnings...), ErrorCode: e.ErrorCode,
		FailureReason: e.FailureReason, AuditReference: e.AuditReference, CreatedAt: e.CreatedAt,
	}
}

func validateIdentifier(name, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s is required: %w", name, ErrChatGPTImportInvalid)
	}
	if len(value) > ChatGPTImportMaxIdentifierByte || !utf8.ValidString(value) || strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s is invalid or too long: %w", name, ErrChatGPTImportInvalid)
	}
	return nil
}

func validatePathlessIdentifier(name, value string) error {
	if err := validateIdentifier(name, value); err != nil {
		return err
	}
	if strings.ContainsAny(strings.TrimSpace(value), "/\\") {
		return fmt.Errorf("%s must not contain a path separator: %w", name, ErrChatGPTImportInvalid)
	}
	return nil
}

func validateSHA256(name, value string) error {
	if len(value) != 64 || strings.ToLower(value) != value {
		return fmt.Errorf("%s must be lowercase sha256: %w", name, ErrChatGPTImportInvalid)
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return fmt.Errorf("%s must be hexadecimal sha256: %w", name, ErrChatGPTImportInvalid)
		}
	}
	return nil
}

func validateDiagnostic(name, value string, maxBytes int) error {
	if !utf8.ValidString(value) || len(value) > maxBytes || utf8.RuneCountInString(value) > ChatGPTImportMaxDiagnosticRune || strings.ContainsAny(value, "\r\n\x00/\\") {
		return fmt.Errorf("%s is not a bounded sanitized diagnostic: %w", name, ErrChatGPTImportInvalid)
	}
	return nil
}

// ChatGPTImportConfirmInput is the owner-scoped request for confirming only
// candidates proven by the authenticated ChatGPT Raw import. OwnerID and
// ActorID are trusted CORE fields; adapters must not derive them from an
// untrusted body.
type ChatGPTImportConfirmInput struct {
	RequestID string `json:"request_id"`
	OwnerID   string `json:"owner_id"`
	ActorID   string `json:"actor_id"`
	ExportID  string `json:"export_id"`
	Reason    string `json:"reason"`
	Apply     bool   `json:"apply"`
}

// ChatGPTImportConfirmResult is the safe public receipt. Storage identities,
// evidence/candidate IDs, statements, paths, and content are intentionally
// absent so another owner cannot infer private rows from this result.
type ChatGPTImportConfirmResult struct {
	RequestID           string `json:"request_id"`
	ExportID            string `json:"export_id"`
	Apply               bool   `json:"apply"`
	Matched             int    `json:"matched"`
	Confirmed           int    `json:"confirmed"`
	ProjectionPending   int    `json:"projection_pending"`
	ProjectionRunning   int    `json:"projection_running"`
	ProjectionRetryWait int    `json:"projection_retry_wait"`
	ProjectionFailed    int    `json:"projection_failed"`
	ProjectionCompleted int    `json:"projection_completed"`
	IdempotentReplay    bool   `json:"idempotent_replay"`
	AuditReference      string `json:"audit_reference"`
}

const ChatGPTImportConfirmMaxReasonByte = ChatGPTImportMaxAuditByte

// Validate applies the bounded, pathless confirmation input contract. The
// returned error deliberately carries a fixed safe message rather than field
// values supplied by a caller.
func (in ChatGPTImportConfirmInput) Validate() error {
	for _, value := range []string{in.RequestID, in.OwnerID, in.ActorID, in.ExportID} {
		if err := validatePathlessIdentifier("confirmation_identifier", value); err != nil {
			return NewChatGPTImportError(ChatGPTImportErrorInvalid, "invalid ChatGPT import confirmation request")
		}
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || len(reason) > ChatGPTImportConfirmMaxReasonByte || utf8.RuneCountInString(reason) > ChatGPTImportMaxDiagnosticRune || !utf8.ValidString(reason) || strings.ContainsAny(reason, "\r\n\x00/\\") {
		return NewChatGPTImportError(ChatGPTImportErrorInvalid, "invalid ChatGPT import confirmation request")
	}
	return nil
}

// ChatGPTImportPromotionStateCounts is the fixed, export-scoped state
// projection used by progress and finalization. The fields are deliberately
// closed so a caller cannot make CORE render arbitrary database state.
type ChatGPTImportPromotionStateCounts struct {
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	RetryWait int `json:"retry_wait"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// ChatGPTImportProgress is the deterministic owner projection for one export.
// It contains counts and states only; it never carries Raw IDs, payloads,
// statements, filesystem paths, or LLM output.
type ChatGPTImportProgress struct {
	RequestID                       string                            `json:"request_id"`
	ImportID                        string                            `json:"import_id"`
	ExportID                        string                            `json:"export_id"`
	Apply                           bool                              `json:"apply"`
	State                           ChatGPTImportState                `json:"state"`
	ExpectedRawCount                int                               `json:"expected_raw_count"`
	ExpectedProjectionCount         int                               `json:"expected_projection_count"`
	ExpectedJobCount                int                               `json:"expected_job_count"`
	RawCount                        int                               `json:"raw_count"`
	ProjectionCount                 int                               `json:"projection_count"`
	CompletedProjectionReceiptCount int                               `json:"completed_projection_receipt_count"`
	JobCount                        int                               `json:"job_count"`
	PromotionStateCounts            ChatGPTImportPromotionStateCounts `json:"promotion_state_counts"`
	FailedWithEvidenceCount         int                               `json:"failed_with_evidence_count"`
	MissingEvidenceCount            int                               `json:"missing_evidence_count"`
	NonTerminalCount                int                               `json:"non_terminal_count"`
	TerminalSuccess                 bool                              `json:"terminal_success"`
}

// ChatGPTImportRetryInput is the authenticated, export-scoped retry command.
// It deliberately has no reason, path, candidate, or LLM fields.
type ChatGPTImportRetryInput struct {
	RequestID string `json:"request_id"`
	OwnerID   string `json:"owner_id"`
	ActorID   string `json:"actor_id"`
	ExportID  string `json:"export_id"`
}

func (in ChatGPTImportRetryInput) Validate() error {
	for _, value := range []string{in.RequestID, in.OwnerID, in.ActorID, in.ExportID} {
		if err := validatePathlessIdentifier("retry_identifier", value); err != nil {
			return NewChatGPTImportError(ChatGPTImportErrorInvalid, "invalid ChatGPT import retry request")
		}
	}
	return nil
}

// ChatGPTImportRetryResult is a bounded deterministic retry receipt. Failed
// jobs without evidence are reported but never requeued.
type ChatGPTImportRetryResult struct {
	RequestID            string `json:"request_id"`
	ExportID             string `json:"export_id"`
	RequeuedCount        int    `json:"requeued_count"`
	MissingEvidenceCount int    `json:"missing_evidence_count"`
	AuditReference       string `json:"audit_reference,omitempty"`
}

// ChatGPTImportFinalizeInput is the authenticated machine finalization
// command. Finalization verifies persisted counts; Apply controls whether it
// writes the immutable receipt. It never confirms, pins, or otherwise changes
// UserMemory candidates.
type ChatGPTImportFinalizeInput struct {
	RequestID string `json:"request_id"`
	OwnerID   string `json:"owner_id"`
	ActorID   string `json:"actor_id"`
	ExportID  string `json:"export_id"`
	Apply     bool   `json:"apply"`
}

func (in ChatGPTImportFinalizeInput) Validate() error {
	for _, value := range []string{in.RequestID, in.OwnerID, in.ActorID, in.ExportID} {
		if err := validatePathlessIdentifier("finalize_identifier", value); err != nil {
			return NewChatGPTImportError(ChatGPTImportErrorInvalid, "invalid ChatGPT import finalization request")
		}
	}
	return nil
}

// ChatGPTImportFinalizeResult is the machine verification result returned for
// a ready export. ReceiptID is empty for a dry-run and identifies the immutable
// receipt when Apply is true. Its counters can be checked without reopening
// private storage.
type ChatGPTImportFinalizeResult struct {
	RequestID                       string                            `json:"request_id"`
	ExportID                        string                            `json:"export_id"`
	Apply                           bool                              `json:"apply"`
	Status                          string                            `json:"status"`
	ReceiptID                       string                            `json:"receipt_id"`
	ExpectedRawCount                int                               `json:"expected_raw_count"`
	ExpectedProjectionCount         int                               `json:"expected_projection_count"`
	ExpectedJobCount                int                               `json:"expected_job_count"`
	RawCount                        int                               `json:"raw_count"`
	ProjectionCount                 int                               `json:"projection_count"`
	CompletedProjectionReceiptCount int                               `json:"completed_projection_receipt_count"`
	JobCount                        int                               `json:"job_count"`
	PromotionStateCounts            ChatGPTImportPromotionStateCounts `json:"promotion_state_counts"`
	FailedWithEvidenceCount         int                               `json:"failed_with_evidence_count"`
	MissingEvidenceCount            int                               `json:"missing_evidence_count"`
	NonTerminalCount                int                               `json:"non_terminal_count"`
	AuditReference                  string                            `json:"audit_reference"`
	IdempotentReplay                bool                              `json:"idempotent_replay"`
}

const ChatGPTImportFinalizeStatusCompleted = "completed"
