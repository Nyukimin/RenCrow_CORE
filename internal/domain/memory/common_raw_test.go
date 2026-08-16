package memory

import (
	"strings"
	"testing"
	"time"
)

func TestCommonRawInputHashIsIndependentOfInputOrder(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	records := []CommonRawRecord{
		{SourceRecordID: "record-b", Sensitivity: CommonRawPrivateSensitivity, Role: "assistant", ContentType: "text/plain", OccurredAt: now, Content: []byte("b"), ContentSHA256: SHA256Hex([]byte("b")), Provenance: "export", Rights: "owner", License: "private"},
		{SourceRecordID: "record-a", Sensitivity: CommonRawPrivateSensitivity, Role: "user", ContentType: "text/plain", OccurredAt: now, Content: []byte("a"), ContentSHA256: SHA256Hex([]byte("a")), Provenance: "export", Rights: "owner", License: "private", AssetRefs: []string{"asset-a"}},
	}
	assets := []CommonRawAsset{{SourceAssetID: "asset-a", MediaType: "image/png", Content: []byte("asset"), ContentSHA256: SHA256Hex([]byte("asset")), Provenance: "export", Rights: "owner", License: "private"}}
	manifest := CommonRawManifest{ContractVersion: CommonRawContractVersion, SourceType: "test", SourceIdentity: "export-1", SourceCount: 2, AssetCount: 1, SchemaVersion: "schema-1", ConverterVersion: "converter-1", Scope: "user:ren", Sensitivity: CommonRawPrivateSensitivity, Rights: "owner", License: "private", Provenance: "export"}
	first, err := CommonRawInputHash(manifest, records, assets)
	if err != nil {
		t.Fatalf("CommonRawInputHash first: %v", err)
	}
	second, err := CommonRawInputHash(manifest, []CommonRawRecord{records[1], records[0]}, []CommonRawAsset{assets[0]})
	if err != nil {
		t.Fatalf("CommonRawInputHash second: %v", err)
	}
	if first != second || len(first) != 64 || first != strings.ToLower(first) {
		t.Fatalf("order-dependent or noncanonical hash: first=%q second=%q", first, second)
	}
}

func TestCommonRawValidationRejectsNonCanonicalClaimsAndPublicSensitivity(t *testing.T) {
	manifest := CommonRawManifest{ContractVersion: CommonRawContractVersion, SourceType: "test", SourceIdentity: "export-1", ManifestSHA256: strings.Repeat("A", 64), SchemaVersion: "schema-1", ConverterVersion: "converter-1", Scope: "user:ren", Sensitivity: CommonRawPrivateSensitivity, Rights: "owner", License: "private", Provenance: "export"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("uppercase manifest hash must be rejected")
	}
	manifest.ManifestSHA256 = strings.Repeat("a", 64)
	manifest.Sensitivity = "normal"
	if err := manifest.Validate(); err == nil {
		t.Fatal("non-private sensitivity must be rejected")
	}
}

func TestDeterministicCommonRawIDsBindOwnerScopeAndHashes(t *testing.T) {
	manifestID := DeterministicCommonRawManifestID("ren", "user:ren", "chatgpt", "export-1", strings.Repeat("a", 64))
	otherOwnerID := DeterministicCommonRawManifestID("other", "user:other", "chatgpt", "export-1", strings.Repeat("a", 64))
	if manifestID == otherOwnerID || !strings.HasPrefix(manifestID, "raw-manifest:") {
		t.Fatalf("manifest IDs are not owner-scoped: %q %q", manifestID, otherOwnerID)
	}
	recordID := DeterministicCommonRawRecordID("ren", "user:ren", "chatgpt", "export-1", "message-1", strings.Repeat("b", 64))
	if !strings.HasPrefix(recordID, "raw-record:") {
		t.Fatalf("unexpected record ID: %q", recordID)
	}
}

func TestCommonRawInputHashExcludesTrustedOwnerScope(t *testing.T) {
	record := CommonRawRecord{SourceRecordID: "record-1", Sensitivity: CommonRawPrivateSensitivity, Role: "user", ContentType: "text/plain", OccurredAt: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC), Content: []byte("same"), ContentSHA256: SHA256Hex([]byte("same")), Provenance: "export", Rights: "owner", License: "private"}
	first := CommonRawManifest{ContractVersion: CommonRawContractVersion, SourceType: "test", SourceIdentity: "export-1", SourceCount: 1, SchemaVersion: "schema-1", ConverterVersion: "converter-1", Scope: "", Sensitivity: CommonRawPrivateSensitivity, Rights: "owner", License: "private", Provenance: "export"}
	second := first
	second.Scope = "user:other"
	hashFirst, err := CommonRawInputHash(first, []CommonRawRecord{record}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hashSecond, err := CommonRawInputHash(second, []CommonRawRecord{record}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hashFirst != hashSecond {
		t.Fatalf("trusted owner scope leaked into source manifest hash: %q != %q", hashFirst, hashSecond)
	}
}

func TestCommonRawValidationBoundsEveryMetadataField(t *testing.T) {
	tooLarge := strings.Repeat("m", CommonRawMaxMetadataSize+1)
	manifestFields := []struct {
		name  string
		apply func(*CommonRawManifest)
	}{
		{"source type", func(v *CommonRawManifest) { v.SourceType = tooLarge }},
		{"source identity", func(v *CommonRawManifest) { v.SourceIdentity = tooLarge }},
		{"schema version", func(v *CommonRawManifest) { v.SchemaVersion = tooLarge }},
		{"converter version", func(v *CommonRawManifest) { v.ConverterVersion = tooLarge }},
		{"rights", func(v *CommonRawManifest) { v.Rights = tooLarge }},
		{"license", func(v *CommonRawManifest) { v.License = tooLarge }},
		{"provenance", func(v *CommonRawManifest) { v.Provenance = tooLarge }},
	}
	for _, field := range manifestFields {
		t.Run("manifest-"+field.name, func(t *testing.T) {
			manifest := CommonRawManifest{ContractVersion: CommonRawContractVersion, SourceType: "test", SourceIdentity: "id", ManifestSHA256: strings.Repeat("a", 64), SourceCount: 0, AssetCount: 0, SchemaVersion: "schema", ConverterVersion: "converter", Sensitivity: CommonRawPrivateSensitivity, Rights: "owner", License: "private", Provenance: "export", AllowEmpty: true}
			field.apply(&manifest)
			if CommonRawErrorCodeOf(manifest.Validate()) != CommonRawErrorInvalid {
				t.Fatalf("metadata field %s was not bounded", field.name)
			}
		})
	}

	recordFields := []struct {
		name  string
		apply func(*CommonRawRecord)
	}{
		{"source record id", func(v *CommonRawRecord) { v.SourceRecordID = tooLarge }},
		{"parent id", func(v *CommonRawRecord) { v.ParentID = tooLarge }},
		{"thread id", func(v *CommonRawRecord) { v.ThreadID = tooLarge }},
		{"role", func(v *CommonRawRecord) { v.Role = tooLarge }},
		{"content type", func(v *CommonRawRecord) { v.ContentType = tooLarge }},
		{"provenance", func(v *CommonRawRecord) { v.Provenance = tooLarge }},
		{"rights", func(v *CommonRawRecord) { v.Rights = tooLarge }},
		{"license", func(v *CommonRawRecord) { v.License = tooLarge }},
	}
	for _, field := range recordFields {
		t.Run("record-"+field.name, func(t *testing.T) {
			content := []byte("record")
			record := CommonRawRecord{SourceRecordID: "record", Sensitivity: CommonRawPrivateSensitivity, Role: "user", ContentType: "text/plain", OccurredAt: time.Now().UTC(), Content: content, ContentSHA256: SHA256Hex(content), Provenance: "export", Rights: "owner", License: "private"}
			field.apply(&record)
			if CommonRawErrorCodeOf(record.Validate()) != CommonRawErrorInvalid {
				t.Fatalf("metadata field %s was not bounded", field.name)
			}
		})
	}

	assetFields := []struct {
		name  string
		apply func(*CommonRawAsset)
	}{
		{"source asset id", func(v *CommonRawAsset) { v.SourceAssetID = tooLarge }},
		{"media type", func(v *CommonRawAsset) { v.MediaType = tooLarge }},
		{"provenance", func(v *CommonRawAsset) { v.Provenance = tooLarge }},
		{"rights", func(v *CommonRawAsset) { v.Rights = tooLarge }},
		{"license", func(v *CommonRawAsset) { v.License = tooLarge }},
	}
	for _, field := range assetFields {
		t.Run("asset-"+field.name, func(t *testing.T) {
			content := []byte("asset")
			asset := CommonRawAsset{SourceAssetID: "asset", MediaType: "text/plain", Content: content, ContentSHA256: SHA256Hex(content), Provenance: "export", Rights: "owner", License: "private"}
			field.apply(&asset)
			if CommonRawErrorCodeOf(asset.Validate()) != CommonRawErrorInvalid {
				t.Fatalf("metadata field %s was not bounded", field.name)
			}
		})
	}
}
