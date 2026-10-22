package task

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type DelegationReportState string

const (
	DelegationReportPending   DelegationReportState = "pending"
	DelegationReportDelivered DelegationReportState = "delivered"
)

// DelegationReport is Task-owner state used to resend a completed worker report
// to EventStore after a restart. The envelope sender is reconstructed from
// TrustedSender because ReportEnvelope intentionally omits Sender from JSON.
type DelegationReport struct {
	ReportID         modulecore.ResponseID       `json:"report_id"`
	TaskID           modulecore.TaskID           `json:"task_id"`
	RunID            modulecore.RunID            `json:"run_id"`
	TaskStatus       Status                      `json:"task_status"`
	TrustedSender    string                      `json:"trusted_sender"`
	Envelope         conversation.ReportEnvelope `json:"envelope"`
	OperationID      string                      `json:"operation_id"`
	RequestHash      string                      `json:"request_hash"`
	WriterGeneration uint64                      `json:"writer_generation"`
	State            DelegationReportState       `json:"state"`
	CreatedAt        int64                       `json:"created_at_unix_nano"`
	DeliveredAt      int64                       `json:"delivered_at_unix_nano,omitempty"`
}

// Validate checks report shape and its Task/Run terminal-status contract. It
// does not validate current delegation ownership or revision; EventStore owns
// that check when the pending report is accepted.
func (report DelegationReport) Validate() error {
	for _, id := range []struct {
		name string
		err  error
	}{
		{"report_id", report.ReportID.Validate()},
		{"task_id", report.TaskID.Validate()},
		{"run_id", report.RunID.Validate()},
		{"delegation_id", report.Envelope.DelegationID.Validate()},
		{"work_task_id", report.Envelope.WorkTaskID.Validate()},
		{"work_run_id", report.Envelope.WorkRunID.Validate()},
	} {
		if id.err != nil {
			return fmt.Errorf("delegation report %s is invalid: %w", id.name, id.err)
		}
	}
	if report.ReportID != report.Envelope.ReportID || report.TaskID != report.Envelope.WorkTaskID || report.RunID != report.Envelope.WorkRunID {
		return errors.New("delegation report identity does not match its envelope")
	}
	if report.Envelope.ReportRevision == 0 || report.Envelope.DelegationRevision == 0 {
		return errors.New("delegation report revisions must be positive")
	}
	if report.TrustedSender == "" || len(report.TrustedSender) > 64 || strings.TrimSpace(report.TrustedSender) != report.TrustedSender || !utf8.ValidString(report.TrustedSender) {
		return errors.New("delegation report trusted sender is invalid")
	}
	if report.Envelope.Sender != "" && report.Envelope.Sender != report.TrustedSender {
		return errors.New("delegation report envelope sender differs from trusted sender")
	}
	if len(report.RequestHash) != 64 || strings.ToLower(report.RequestHash) != report.RequestHash {
		return errors.New("delegation report request_hash must be lowercase SHA-256 hex")
	}
	if _, err := hex.DecodeString(report.RequestHash); err != nil {
		return errors.New("delegation report request_hash must be lowercase SHA-256 hex")
	}
	if report.WriterGeneration == 0 || report.CreatedAt <= 0 {
		return errors.New("delegation report writer generation and creation time are required")
	}
	switch report.State {
	case DelegationReportPending:
		if report.DeliveredAt != 0 {
			return errors.New("pending delegation report has delivered_at")
		}
	case DelegationReportDelivered:
		if report.DeliveredAt <= 0 {
			return errors.New("delivered delegation report has no delivered_at")
		}
	default:
		return errors.New("delegation report state is invalid")
	}
	if report.Envelope.Status != conversation.ReportStatusSucceeded && report.Envelope.Status != conversation.ReportStatusPartial && report.Envelope.Status != conversation.ReportStatusFailed && report.Envelope.Status != conversation.ReportStatusCanceled {
		return errors.New("delegation report status is invalid")
	}
	if err := validateReportText("summary", report.Envelope.Summary, conversation.MaxReportSummaryBytes); err != nil {
		return err
	}
	if err := validateReportText("result", report.Envelope.Result, conversation.MaxReportResultBytes); err != nil {
		return err
	}
	if len(report.Envelope.EvidenceRefs)+len(report.Envelope.ArtifactRefs) > conversation.MaxReportReferences {
		return errors.New("delegation report has too many references")
	}
	seenEvidence := make(map[modulecore.EvidenceID]struct{}, len(report.Envelope.EvidenceRefs))
	for _, id := range report.Envelope.EvidenceRefs {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("delegation report evidence reference is invalid: %w", err)
		}
		if _, ok := seenEvidence[id]; ok {
			return errors.New("delegation report evidence references contain a duplicate")
		}
		seenEvidence[id] = struct{}{}
	}
	seenArtifacts := make(map[modulecore.ArtifactID]struct{}, len(report.Envelope.ArtifactRefs))
	for _, id := range report.Envelope.ArtifactRefs {
		if err := id.Validate(); err != nil {
			return fmt.Errorf("delegation report artifact reference is invalid: %w", err)
		}
		if _, ok := seenArtifacts[id]; ok {
			return errors.New("delegation report artifact references contain a duplicate")
		}
		seenArtifacts[id] = struct{}{}
	}
	if len(report.Envelope.Limitations) > conversation.MaxReportLimitations {
		return errors.New("delegation report has too many limitations")
	}
	for _, limitation := range report.Envelope.Limitations {
		if err := validateReportText("limitation", limitation, conversation.MaxReportLimitationBytes); err != nil {
			return err
		}
	}
	if err := validateReportTaskStatus(report.Envelope.Status, report.TaskStatus, len(report.Envelope.Limitations)); err != nil {
		return err
	}
	return nil
}

func validateReportText(name, value string, maxBytes int) error {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > maxBytes {
		return fmt.Errorf("delegation report %s is empty, oversized, or invalid UTF-8", name)
	}
	return nil
}

func validateReportTaskStatus(reportStatus conversation.ReportStatus, taskStatus Status, limitationCount int) error {
	want := Status("")
	switch reportStatus {
	case conversation.ReportStatusSucceeded:
		want = StatusSucceeded
	case conversation.ReportStatusFailed:
		want = StatusFailed
	case conversation.ReportStatusCanceled:
		want = StatusCancelled
	case conversation.ReportStatusPartial:
		if taskStatus != StatusSucceeded && taskStatus != StatusFailed {
			return errors.New("partial delegation report requires succeeded or failed Task status")
		}
		if limitationCount == 0 {
			return errors.New("partial delegation report requires an explicit limitation")
		}
		return nil
	default:
		return errors.New("delegation report status is invalid")
	}
	if taskStatus != want {
		return fmt.Errorf("delegation report status %s contradicts Task status %s", reportStatus, taskStatus)
	}
	return nil
}
