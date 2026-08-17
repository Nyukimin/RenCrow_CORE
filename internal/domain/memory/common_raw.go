package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Common Raw is the immutable, pre-projection source boundary for the
// conversation_l1 store. It deliberately does not contain any projection or
// Recall operation.
const (
	CommonRawContractVersion      = "common-raw/v1"
	CommonRawPrivateSensitivity   = "private"
	CommonRawStorageInline        = "inline"
	CommonRawStorageObject        = "object"
	CommonRawMaxInlinePayloadSize = 64 * 1024
	CommonRawMaxRecords           = 10_000
	CommonRawMaxAssets            = 10_000
	CommonRawMaxPayloadSize       = 64 * 1024 * 1024
	// A caller must split larger exports into independently committed batches.
	CommonRawMaxBatchPayloadSize = 64 * 1024 * 1024
	CommonRawMaxMetadataSize     = 16 * 1024
)

type CommonRawState string

const (
	CommonRawStateValidating = CommonRawState("validating")
	CommonRawStateCommitting = CommonRawState("committing")
	CommonRawStateCompleted  = CommonRawState("completed")
	CommonRawStateRejected   = CommonRawState("rejected")
	CommonRawStateBlocked    = CommonRawState("blocked")
)

type CommonRawErrorCode string

const (
	CommonRawErrorInvalid       CommonRawErrorCode = "invalid"
	CommonRawErrorForbidden     CommonRawErrorCode = "forbidden"
	CommonRawErrorConflict      CommonRawErrorCode = "conflict"
	CommonRawErrorSourceChanged CommonRawErrorCode = "source_changed"
	CommonRawErrorSchema        CommonRawErrorCode = "unsupported_schema"
	CommonRawErrorRoot          CommonRawErrorCode = "invalid_root"
	CommonRawErrorObject        CommonRawErrorCode = "invalid_object"
	CommonRawErrorUnavailable   CommonRawErrorCode = "unavailable"
)

// CommonRawError is the typed failure contract used by the Raw domain and
// store. Code is safe to return as a machine-readable status; Message is for
// operator diagnostics and never contains payload bytes or absolute paths.
type CommonRawError struct {
	Code    CommonRawErrorCode
	Message string
}

func (e *CommonRawError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return string(e.Code)
	}
	return string(e.Code) + ": " + e.Message
}

func (e *CommonRawError) Is(target error) bool {
	t, ok := target.(*CommonRawError)
	return ok && t != nil && e != nil && e.Code == t.Code
}

func NewCommonRawError(code CommonRawErrorCode, message string) error {
	return &CommonRawError{Code: code, Message: strings.TrimSpace(message)}
}

func CommonRawErrorCodeOf(err error) CommonRawErrorCode {
	var typed *CommonRawError
	if errors.As(err, &typed) && typed != nil {
		return typed.Code
	}
	return ""
}

var (
	ErrCommonRawInvalid       = &CommonRawError{Code: CommonRawErrorInvalid}
	ErrCommonRawForbidden     = &CommonRawError{Code: CommonRawErrorForbidden}
	ErrCommonRawConflict      = &CommonRawError{Code: CommonRawErrorConflict}
	ErrCommonRawSourceChanged = &CommonRawError{Code: CommonRawErrorSourceChanged}
	ErrCommonRawSchema        = &CommonRawError{Code: CommonRawErrorSchema}
	ErrCommonRawRoot          = &CommonRawError{Code: CommonRawErrorRoot}
	ErrCommonRawObject        = &CommonRawError{Code: CommonRawErrorObject}
	ErrCommonRawUnavailable   = &CommonRawError{Code: CommonRawErrorUnavailable}
)

// CommonRawManifest contains source-level claims. Owner and IDs are trusted
// and filled by CORE; scope is generated from the authenticated owner.
type CommonRawManifest struct {
	ContractVersion  string `json:"contract_version"`
	SourceType       string `json:"source_type"`
	SourceIdentity   string `json:"source_identity"`
	ManifestSHA256   string `json:"manifest_sha256"`
	SourceCount      int    `json:"source_count"`
	AssetCount       int    `json:"asset_count"`
	SchemaVersion    string `json:"schema_version"`
	ConverterVersion string `json:"converter_version"`
	Scope            string `json:"scope"`
	Sensitivity      string `json:"sensitivity"`
	Rights           string `json:"rights"`
	License          string `json:"license"`
	Provenance       string `json:"provenance"`
	AllowEmpty       bool   `json:"allow_empty"`
}

// CommonRawRecord is one immutable source record. ContentSHA256 is a caller
// claim and is always recomputed by CORE before it is accepted.
type CommonRawRecord struct {
	SourceRecordID string    `json:"source_record_id"`
	ParentID       string    `json:"parent_id,omitempty"`
	ThreadID       string    `json:"thread_id,omitempty"`
	Sensitivity    string    `json:"sensitivity"`
	Role           string    `json:"role"`
	ContentType    string    `json:"content_type"`
	OccurredAt     time.Time `json:"occurred_at"`
	Content        []byte    `json:"-"`
	ContentSHA256  string    `json:"content_sha256"`
	Provenance     string    `json:"provenance,omitempty"`
	Rights         string    `json:"rights,omitempty"`
	License        string    `json:"license,omitempty"`
	AssetRefs      []string  `json:"asset_refs,omitempty"`
}

// CommonRawAsset is a source asset referenced by one or more records. Assets
// are always stored as content-addressed objects in this first unit.
type CommonRawAsset struct {
	SourceAssetID string `json:"source_asset_id"`
	MediaType     string `json:"media_type"`
	Content       []byte `json:"-"`
	ContentSHA256 string `json:"content_sha256"`
	Provenance    string `json:"provenance,omitempty"`
	Rights        string `json:"rights,omitempty"`
	License       string `json:"license,omitempty"`
}

type CommonRawIntakeRequest struct {
	Manifest CommonRawManifest `json:"manifest"`
	Records  []CommonRawRecord `json:"records"`
	Assets   []CommonRawAsset  `json:"assets"`
}

type CommonRawAssetRef struct {
	SourceAssetID string `json:"source_asset_id"`
	ObjectRef     string `json:"object_ref"`
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	MediaType     string `json:"media_type"`
}

type CommonRawRecordReceipt struct {
	RawRecordID    string              `json:"raw_record_id"`
	SourceRecordID string              `json:"source_record_id"`
	ContentSHA256  string              `json:"content_sha256"`
	ContentSize    int64               `json:"content_size"`
	StorageKind    string              `json:"storage_kind"`
	ObjectRef      string              `json:"object_ref,omitempty"`
	AssetRefs      []CommonRawAssetRef `json:"asset_refs,omitempty"`
}

type CommonRawIntakeReceipt struct {
	RequestID        string                   `json:"request_id"`
	ManifestID       string                   `json:"manifest_id"`
	Status           CommonRawState           `json:"status"`
	ManifestSHA256   string                   `json:"manifest_sha256"`
	SourceCount      int                      `json:"source_count"`
	AssetCount       int                      `json:"asset_count"`
	Checkpoint       string                   `json:"checkpoint"`
	IdempotentReplay bool                     `json:"idempotent_replay"`
	Records          []CommonRawRecordReceipt `json:"records"`
	CreatedAt        time.Time                `json:"created_at"`
}

func (m CommonRawManifest) Validate() error {
	if err := validateCommonRawMetadata("contract version", m.ContractVersion, true); err != nil {
		return err
	}
	if strings.TrimSpace(m.ContractVersion) != CommonRawContractVersion {
		return NewCommonRawError(CommonRawErrorSchema, "contract version is unsupported")
	}
	metadata := []struct {
		name     string
		value    string
		required bool
	}{
		{name: "source type", value: m.SourceType, required: true},
		{name: "source identity", value: m.SourceIdentity, required: true},
		{name: "schema version", value: m.SchemaVersion, required: true},
		{name: "converter version", value: m.ConverterVersion, required: true},
		{name: "sensitivity", value: m.Sensitivity, required: true},
		{name: "rights", value: m.Rights, required: true},
		{name: "license", value: m.License, required: true},
		{name: "provenance", value: m.Provenance, required: true},
		{name: "scope", value: m.Scope},
	}
	for _, field := range metadata {
		if err := validateCommonRawMetadata(field.name, field.value, field.required); err != nil {
			return err
		}
	}
	if !isSHA256(m.ManifestSHA256) {
		return NewCommonRawError(CommonRawErrorInvalid, "manifest sha256 claim is required")
	}
	if m.SourceCount < 0 || m.AssetCount < 0 || m.SourceCount > CommonRawMaxRecords || m.AssetCount > CommonRawMaxAssets {
		return NewCommonRawError(CommonRawErrorInvalid, "manifest count is outside the bounded limit")
	}
	if strings.TrimSpace(m.Scope) == "private" {
		return NewCommonRawError(CommonRawErrorForbidden, "raw intake scope must be owner-derived")
	}
	if strings.TrimSpace(m.Sensitivity) != CommonRawPrivateSensitivity {
		return NewCommonRawError(CommonRawErrorForbidden, "raw intake sensitivity must be private")
	}
	return nil
}

func (r CommonRawRecord) Validate() error {
	metadata := []struct {
		name     string
		value    string
		required bool
	}{
		{name: "record identity", value: r.SourceRecordID, required: true},
		{name: "parent id", value: r.ParentID},
		{name: "thread id", value: r.ThreadID},
		{name: "sensitivity", value: r.Sensitivity, required: true},
		{name: "role", value: r.Role, required: true},
		{name: "content type", value: r.ContentType, required: true},
		{name: "provenance", value: r.Provenance, required: true},
		{name: "rights", value: r.Rights, required: true},
		{name: "license", value: r.License, required: true},
	}
	for _, field := range metadata {
		if err := validateCommonRawMetadata(field.name, field.value, field.required); err != nil {
			return err
		}
	}
	if r.OccurredAt.IsZero() {
		return NewCommonRawError(CommonRawErrorInvalid, "record occurred_at is required")
	}
	if strings.TrimSpace(r.Sensitivity) != CommonRawPrivateSensitivity || strings.TrimSpace(r.Provenance) == "" || strings.TrimSpace(r.Rights) == "" || strings.TrimSpace(r.License) == "" {
		return NewCommonRawError(CommonRawErrorInvalid, "record sensitivity, provenance, rights and license are required")
	}
	if !isSHA256(r.ContentSHA256) {
		return NewCommonRawError(CommonRawErrorInvalid, "record content sha256 claim is required")
	}
	if len(r.Content) > CommonRawMaxPayloadSize {
		return NewCommonRawError(CommonRawErrorInvalid, "record payload exceeds the bounded limit")
	}
	if err := validateCommonRawMetadataList("asset reference", r.AssetRefs); err != nil {
		return err
	}
	return nil
}

func (a CommonRawAsset) Validate() error {
	metadata := []struct {
		name  string
		value string
	}{
		{name: "asset identity", value: a.SourceAssetID},
		{name: "media type", value: a.MediaType},
		{name: "provenance", value: a.Provenance},
		{name: "rights", value: a.Rights},
		{name: "license", value: a.License},
	}
	for _, field := range metadata {
		if err := validateCommonRawMetadata(field.name, field.value, true); err != nil {
			return err
		}
	}
	if !isSHA256(a.ContentSHA256) {
		return NewCommonRawError(CommonRawErrorInvalid, "asset content sha256 claim is required")
	}
	if len(a.Content) == 0 || len(a.Content) > CommonRawMaxPayloadSize {
		return NewCommonRawError(CommonRawErrorInvalid, "asset payload is outside the bounded limit")
	}
	return nil
}

func validateCommonRawMetadata(name, value string, required bool) error {
	if len([]byte(value)) > CommonRawMaxMetadataSize {
		return NewCommonRawError(CommonRawErrorInvalid, name+" exceeds the bounded metadata limit")
	}
	if required && strings.TrimSpace(value) == "" {
		return NewCommonRawError(CommonRawErrorInvalid, name+" is required")
	}
	return nil
}

func validateCommonRawMetadataList(name string, values []string) error {
	total := 0
	for _, value := range values {
		if err := validateCommonRawMetadata(name, value, true); err != nil {
			return err
		}
		valueSize := len([]byte(value))
		if valueSize > CommonRawMaxMetadataSize-total {
			return NewCommonRawError(CommonRawErrorInvalid, name+" metadata exceeds the bounded limit")
		}
		total += valueSize
	}
	return nil
}

func isSHA256(value string) bool {
	if value != strings.ToLower(value) || len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func SHA256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

// canonicalCommonRawManifestBytes omits ManifestSHA256 while computing the
// request-wide claim. Callers must use CommonRawInputHash rather than a
// header-only hash.
func canonicalCommonRawManifestBytes(m CommonRawManifest) ([]byte, error) {
	type canonical struct {
		ContractVersion  string `json:"contract_version"`
		SourceType       string `json:"source_type"`
		SourceIdentity   string `json:"source_identity"`
		SourceCount      int    `json:"source_count"`
		AssetCount       int    `json:"asset_count"`
		SchemaVersion    string `json:"schema_version"`
		ConverterVersion string `json:"converter_version"`
		Sensitivity      string `json:"sensitivity"`
		Rights           string `json:"rights"`
		License          string `json:"license"`
		Provenance       string `json:"provenance"`
		AllowEmpty       bool   `json:"allow_empty"`
	}
	return json.Marshal(canonical{
		ContractVersion: strings.TrimSpace(m.ContractVersion), SourceType: strings.TrimSpace(m.SourceType),
		SourceIdentity: strings.TrimSpace(m.SourceIdentity), SourceCount: m.SourceCount, AssetCount: m.AssetCount,
		SchemaVersion: strings.TrimSpace(m.SchemaVersion), ConverterVersion: strings.TrimSpace(m.ConverterVersion),
		Sensitivity: strings.TrimSpace(m.Sensitivity),
		Rights:      strings.TrimSpace(m.Rights), License: strings.TrimSpace(m.License), Provenance: strings.TrimSpace(m.Provenance),
		AllowEmpty: m.AllowEmpty,
	})
}

// DeterministicCommonRawManifestID and DeterministicCommonRawRecordID are
// CORE-owned identities. Source adapters must not provide either ID.
func DeterministicCommonRawManifestID(ownerID, scope, sourceType, sourceIdentity, manifestSHA256 string) string {
	return "raw-manifest:" + SHA256Hex([]byte(strings.Join([]string{CommonRawContractVersion, ownerID, scope, sourceType, sourceIdentity, manifestSHA256}, "\x00")))
}

func DeterministicCommonRawRecordID(ownerID, scope, sourceType, sourceIdentity, sourceRecordID, contentSHA256 string) string {
	return "raw-record:" + SHA256Hex([]byte(strings.Join([]string{CommonRawContractVersion, ownerID, scope, sourceType, sourceIdentity, sourceRecordID, contentSHA256}, "\x00")))
}

func DeterministicCommonRawStateEventID(rawRecordID, eventType, eventHash string) string {
	return "raw-state:" + SHA256Hex([]byte(strings.Join([]string{CommonRawContractVersion, rawRecordID, eventType, eventHash}, "\x00")))
}

func CommonRawInputHash(m CommonRawManifest, records []CommonRawRecord, assets []CommonRawAsset) (string, error) {
	manifestBytes, err := canonicalCommonRawManifestBytes(m)
	if err != nil {
		return "", err
	}
	type canonicalRecord struct {
		SourceRecordID string   `json:"source_record_id"`
		ParentID       string   `json:"parent_id"`
		ThreadID       string   `json:"thread_id"`
		Sensitivity    string   `json:"sensitivity"`
		Role           string   `json:"role"`
		ContentType    string   `json:"content_type"`
		OccurredAt     string   `json:"occurred_at"`
		ContentSHA256  string   `json:"content_sha256"`
		Provenance     string   `json:"provenance"`
		Rights         string   `json:"rights"`
		License        string   `json:"license"`
		AssetRefs      []string `json:"asset_refs"`
	}
	type canonicalAsset struct {
		SourceAssetID string `json:"source_asset_id"`
		MediaType     string `json:"media_type"`
		ContentSHA256 string `json:"content_sha256"`
		Provenance    string `json:"provenance"`
		Rights        string `json:"rights"`
		License       string `json:"license"`
	}
	canonicalRecords := make([]canonicalRecord, 0, len(records))
	for _, record := range records {
		assetRefs := append([]string(nil), record.AssetRefs...)
		sort.Strings(assetRefs)
		canonicalRecords = append(canonicalRecords, canonicalRecord{
			SourceRecordID: strings.TrimSpace(record.SourceRecordID), ParentID: strings.TrimSpace(record.ParentID), ThreadID: strings.TrimSpace(record.ThreadID),
			Sensitivity: strings.TrimSpace(record.Sensitivity), Role: strings.TrimSpace(record.Role), ContentType: strings.TrimSpace(record.ContentType),
			OccurredAt:    record.OccurredAt.UTC().Format(time.RFC3339Nano),
			ContentSHA256: strings.ToLower(strings.TrimSpace(record.ContentSHA256)), Provenance: strings.TrimSpace(record.Provenance),
			Rights: strings.TrimSpace(record.Rights), License: strings.TrimSpace(record.License), AssetRefs: assetRefs,
		})
	}
	sort.Slice(canonicalRecords, func(i, j int) bool { return canonicalRecords[i].SourceRecordID < canonicalRecords[j].SourceRecordID })
	canonicalAssets := make([]canonicalAsset, 0, len(assets))
	for _, asset := range assets {
		canonicalAssets = append(canonicalAssets, canonicalAsset{
			SourceAssetID: strings.TrimSpace(asset.SourceAssetID), MediaType: strings.TrimSpace(asset.MediaType),
			ContentSHA256: strings.ToLower(strings.TrimSpace(asset.ContentSHA256)), Provenance: strings.TrimSpace(asset.Provenance),
			Rights: strings.TrimSpace(asset.Rights), License: strings.TrimSpace(asset.License),
		})
	}
	sort.Slice(canonicalAssets, func(i, j int) bool { return canonicalAssets[i].SourceAssetID < canonicalAssets[j].SourceAssetID })
	canonical := struct {
		Manifest json.RawMessage   `json:"manifest"`
		Records  []canonicalRecord `json:"records"`
		Assets   []canonicalAsset  `json:"assets"`
	}{Manifest: manifestBytes, Records: canonicalRecords, Assets: canonicalAssets}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal common raw input hash: %w", err)
	}
	return SHA256Hex(encoded), nil
}

// KnowledgeCommonRawBackfillResult reports the owner-scoped backfill of
// existing l1_knowledge_item rows into Common Raw and its fail-closed
// coverage gate. It carries IDs, hashes and counts only, never raw text.
type KnowledgeCommonRawBackfillResult struct {
	Validated    int                    `json:"validated"`
	ItemCount    int                    `json:"item_count"`
	Coverage     int                    `json:"coverage"`
	Ready        bool                   `json:"ready"`
	RawImported  int                    `json:"raw_imported"`
	RawReplayed  int                    `json:"raw_replayed"`
	Linked       int                    `json:"linked"`
	Status       CommonRawState         `json:"status"`
	ManifestID   string                 `json:"manifest_id"`
	RawReceipt   CommonRawIntakeReceipt `json:"raw_receipt"`
	RawRecordIDs []string               `json:"raw_record_ids"`
}
