package complexity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
)

const (
	ComplexitySaveScanEventStorageHostOperation      = "save_scan_event"
	ComplexitySaveHotspotStorageHostOperation        = "save_hotspot"
	ComplexitySaveHotspotEvidenceStorageHostOperation = "save_hotspot_evidence"
	ComplexitySaveReportArtifactStorageHostOperation = "save_report_artifact"
	complexityStorageHostReceiptTable                 = "complexity_storagehost_operation_receipt"
)

var complexityStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

var ErrComplexityStorageHostConflict = errors.New("complexity storage-host operation conflicts with owner receipt")

// ComplexityStorageHostOperationIdentity binds one typed storage-host request
// to its original payload and writer generation.
type ComplexityStorageHostOperationIdentity struct {
	OpID             string
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
}

func (identity ComplexityStorageHostOperationIdentity) validate() error {
	if !complexityStorageHostOpIDPattern.MatchString(identity.OpID) || identity.WriterGeneration <= 0 || len(identity.PayloadSHA256) != sha256.Size*2 {
		return errors.New("complexity storage-host operation identity is incomplete")
	}
	if _, err := hex.DecodeString(identity.PayloadSHA256); err != nil || identity.PayloadSHA256 != hex.EncodeToString(mustDecodeSHA256(identity.PayloadSHA256)) {
		return errors.New("complexity storage-host payload hash is malformed")
	}
	if _, ok := complexityStorageHostEffect(identity.Operation); !ok {
		return errors.New("complexity storage-host operation is unsupported")
	}
	return nil
}

type complexityStorageHostEffectBinding struct {
	table      string
	idColumn   string
	effectID   string
	indexColumn string
	indexValue string
}

func complexityStorageHostEffect(operation string) (complexityStorageHostEffectBinding, bool) {
	switch operation {
	case ComplexitySaveScanEventStorageHostOperation:
		return complexityStorageHostEffectBinding{table: "complexity_scan_event", idColumn: "scan_id"}, true
	case ComplexitySaveHotspotStorageHostOperation:
		return complexityStorageHostEffectBinding{table: "complexity_hotspot", idColumn: "hotspot_id", indexColumn: "scan_id"}, true
	case ComplexitySaveHotspotEvidenceStorageHostOperation:
		return complexityStorageHostEffectBinding{table: "complexity_hotspot_evidence", idColumn: "evidence_id", indexColumn: "hotspot_id"}, true
	case ComplexitySaveReportArtifactStorageHostOperation:
		return complexityStorageHostEffectBinding{table: "complexity_report_artifact", idColumn: "artifact_id", indexColumn: "scan_id"}, true
	default:
		return complexityStorageHostEffectBinding{}, false
	}
}

func mustDecodeSHA256(value string) []byte {
	decoded, _ := hex.DecodeString(value)
	return decoded
}

func complexityStorageHostIdentityValidFor(identity ComplexityStorageHostOperationIdentity, operation string) error {
	if err := identity.validate(); err != nil {
		return err
	}
	if identity.Operation != operation {
		return errors.New("complexity storage-host operation does not match effect")
	}
	return nil
}

func (s *SQLiteStore) SaveScanEventForStorageHostOperation(ctx context.Context, identity ComplexityStorageHostOperationIdentity, item domaincomplexity.ScanEvent) error {
	if err := domaincomplexity.ValidateScanEvent(item); err != nil {
		return err
	}
	if err := complexityStorageHostIdentityValidFor(identity, ComplexitySaveScanEventStorageHostOperation); err != nil {
		return err
	}
	return s.SaveScanEvent(ctx, item)
}

func (s *SQLiteStore) LookupScanEventStorageHostReceipt(_ context.Context, _ ComplexityStorageHostOperationIdentity, _ domaincomplexity.ScanEvent) (bool, error) {
	return false, nil
}

func (s *SQLiteStore) SaveHotspotForStorageHostOperation(ctx context.Context, identity ComplexityStorageHostOperationIdentity, item domaincomplexity.Hotspot) error {
	if err := domaincomplexity.ValidateHotspot(item); err != nil {
		return err
	}
	if err := complexityStorageHostIdentityValidFor(identity, ComplexitySaveHotspotStorageHostOperation); err != nil {
		return err
	}
	return s.SaveHotspot(ctx, item)
}

func (s *SQLiteStore) LookupHotspotStorageHostReceipt(_ context.Context, _ ComplexityStorageHostOperationIdentity, _ domaincomplexity.Hotspot) (bool, error) {
	return false, nil
}

func (s *SQLiteStore) SaveHotspotEvidenceForStorageHostOperation(ctx context.Context, identity ComplexityStorageHostOperationIdentity, item domaincomplexity.HotspotEvidence) error {
	if err := domaincomplexity.ValidateHotspotEvidence(item); err != nil {
		return err
	}
	if err := complexityStorageHostIdentityValidFor(identity, ComplexitySaveHotspotEvidenceStorageHostOperation); err != nil {
		return err
	}
	return s.SaveHotspotEvidence(ctx, item)
}

func (s *SQLiteStore) LookupHotspotEvidenceStorageHostReceipt(_ context.Context, _ ComplexityStorageHostOperationIdentity, _ domaincomplexity.HotspotEvidence) (bool, error) {
	return false, nil
}

func (s *SQLiteStore) SaveReportArtifactForStorageHostOperation(ctx context.Context, identity ComplexityStorageHostOperationIdentity, item domaincomplexity.ReportArtifact) error {
	if err := domaincomplexity.ValidateReportArtifact(item); err != nil {
		return err
	}
	if err := complexityStorageHostIdentityValidFor(identity, ComplexitySaveReportArtifactStorageHostOperation); err != nil {
		return err
	}
	return s.SaveReportArtifact(ctx, item)
}

func (s *SQLiteStore) LookupReportArtifactStorageHostReceipt(_ context.Context, _ ComplexityStorageHostOperationIdentity, _ domaincomplexity.ReportArtifact) (bool, error) {
	return false, nil
}

func complexityStorageHostEffectID(operation string, item any) (string, string, error) {
	binding, ok := complexityStorageHostEffect(operation)
	if !ok {
		return "", "", errors.New("complexity storage-host operation is unsupported")
	}
	switch value := item.(type) {
	case domaincomplexity.ScanEvent:
		if operation != ComplexitySaveScanEventStorageHostOperation {
			break
		}
		return value.ScanID, value.CreatedAt.Format(timeFormatRFC3339Nano), nil
	case domaincomplexity.Hotspot:
		if operation != ComplexitySaveHotspotStorageHostOperation {
			break
		}
		binding.indexValue = value.ScanID
		return value.HotspotID, value.CreatedAt.Format(timeFormatRFC3339Nano), nil
	case domaincomplexity.HotspotEvidence:
		if operation != ComplexitySaveHotspotEvidenceStorageHostOperation {
			break
		}
		binding.indexValue = value.HotspotID
		return string(value.EvidenceID), value.CreatedAt.Format(timeFormatRFC3339Nano), nil
	case domaincomplexity.ReportArtifact:
		if operation != ComplexitySaveReportArtifactStorageHostOperation {
			break
		}
		binding.indexValue = value.ScanID
		return value.ArtifactID, value.CreatedAt.Format(timeFormatRFC3339Nano), nil
	default:
		return "", "", fmt.Errorf("complexity storage-host effect payload type is unsupported")
	}
	return "", "", fmt.Errorf("complexity storage-host effect payload does not match operation")
}
