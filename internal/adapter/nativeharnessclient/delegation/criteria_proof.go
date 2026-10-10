package delegation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"
)

const criteriaEventsPageSize = 100

func (d *delegation) proveAcceptedRun(ctx context.Context, c Client, start protocol.StartResult, result protocol.RunResult, resultBytes []byte) (*domainaction.NativeDelegationProof, error) {
	if !nativeRunResultAccepted(result) {
		return nil, errors.New("RunResult is not completed and verification-passed")
	}
	criteria := d.task.ExpectedCriteriaRevision
	if !domaintask.ValidCriteriaRevision(criteria) || result.Verification.CriteriaRevision == nil ||
		*result.Verification.CriteriaRevision != criteria {
		return nil, errors.New("RunResult criteria revision does not match the frozen Task revision")
	}
	if len(result.Verification.EvidenceIDs) == 0 || !uniqueNonempty(result.Verification.EvidenceIDs) || !uniqueNonempty(result.EvidenceIDs) ||
		len(result.UnresolvedActionIDs) != 0 || !containsAll(result.EvidenceIDs, result.Verification.EvidenceIDs) {
		return nil, errors.New("RunResult verifier evidence or unresolved actions are invalid")
	}

	runInfo, err := c.RunGet(ctx, protocol.RunGetInput{RunID: start.RunID})
	if err != nil {
		return nil, fmt.Errorf("Harness run/get unavailable: %w", err)
	}
	if !runInfo.Terminal || runInfo.RunID != start.RunID || runInfo.TaskID != start.TaskID || runInfo.ThreadID != start.ThreadID ||
		runInfo.Result == nil || runInfo.LastEventSeq < 1 {
		return nil, errors.New("Harness run/get identity or terminal result is incomplete")
	}
	canonicalOwnerResult, err := protocol.Encode(*runInfo.Result)
	if err != nil || !bytes.Equal(canonicalOwnerResult, resultBytes) {
		return nil, errors.New("Harness run/get result differs from the canonical RunResult")
	}
	previousRunID := ""
	if d.resumeSource != nil {
		previousRunID = d.resumeSource.RunID
	}
	events, err := d.readConfirmedEvents(ctx, c, start, runInfo.LastEventSeq, previousRunID)
	if err != nil {
		return nil, err
	}
	chain, err := findVerifierEventChain(events, start, runInfo.LastEventSeq, result)
	if err != nil {
		return nil, err
	}

	runHash := sha256.Sum256(resultBytes)
	proof := &domainaction.NativeDelegationProof{
		Version: domainaction.NativeDelegationProofVersion, ExpectedCriteriaRevision: criteria,
		RunResultSHA256: hex.EncodeToString(runHash[:]), TerminalEventID: chain.terminal.EventID,
		TerminalEventSeq: chain.terminal.EventSeq,
	}
	for _, evidenceID := range result.Verification.EvidenceIDs {
		read, err := c.EvidenceRead(ctx, protocol.EvidenceReadInput{
			EvidenceID: evidenceID, ProjectionVersion: "raw/v1", Range: protocol.ByteRange{Start: 0, End: 0},
		})
		if err != nil {
			return nil, fmt.Errorf("Harness evidence/read unavailable: %w", err)
		}
		if read.EvidenceID != evidenceID || read.ProjectionVersion != "raw/v1" || read.ReturnedRange.Start != 0 || read.ReturnedRange.End != 0 ||
			!read.CaptureComplete || read.TotalBytes < 0 || !validLowerSHA256(read.RawHash) {
			return nil, errors.New("Harness evidence/read metadata is incomplete or mismatched")
		}
		proof.Evidence = append(proof.Evidence, domainaction.NativeDelegationEvidenceProof{
			EvidenceID: evidenceID, VerifierActionID: chain.actionID, VerifierAttemptID: chain.attemptID,
			CompletionEventID: chain.completed.EventID, RawHash: read.RawHash, TotalBytes: read.TotalBytes,
		})
	}
	if err := proof.Validate(); err != nil {
		return nil, err
	}
	return proof, nil
}

func (d *delegation) readConfirmedEvents(ctx context.Context, c Client, start protocol.StartResult, lastEventSeq int64, previousRunID string) ([]protocol.Event, error) {
	var events []protocol.Event
	seenIDs := make(map[string]struct{})
	after := int64(0)
	targetRunStarted := false
	targetInputAccepted := false
	for {
		page, err := c.EventsRead(ctx, protocol.EventsReadInput{ThreadID: start.ThreadID, AfterSeq: after, Limit: criteriaEventsPageSize})
		if err != nil {
			return nil, fmt.Errorf("Harness events/read unavailable: %w", err)
		}
		previous := after
		for _, event := range page.Events {
			if err := event.Validate(); err != nil {
				return nil, errors.New("Harness events/read returned an invalid event")
			}
			if event.EventSeq != after+1 || event.EventID == "" || event.ThreadID != start.ThreadID || event.EventSeq > lastEventSeq {
				return nil, errors.New("Harness events/read sequence or Thread identity is inconsistent")
			}
			if event.RunID != nil && *event.RunID == start.RunID {
				if !targetRunStarted {
					switch event.Type {
					case protocol.EventInputAccepted:
						if previousRunID != "" || targetInputAccepted || event.TaskID == nil || *event.TaskID != start.TaskID {
							return nil, errors.New("Harness initial input.accepted identity or ordering is inconsistent")
						}
						payload, payloadErr := event.TypedPayload()
						accepted, ok := payload.(*protocol.InputAcceptedPayload)
						if payloadErr != nil || !ok || accepted.Disposition != "initial" || accepted.QueueItemID != nil ||
							accepted.Intake.ThreadID != start.ThreadID || accepted.Intake.MessageID == "" {
							return nil, errors.New("Harness initial input.accepted payload is inconsistent")
						}
						targetInputAccepted = true
					case protocol.EventRunStarted:
						if event.TaskID == nil || *event.TaskID != start.TaskID || previousRunID == "" && !targetInputAccepted {
							return nil, errors.New("Harness target Run is missing its initial input.accepted prefix")
						}
						payload, payloadErr := event.TypedPayload()
						started, ok := payload.(*protocol.RunStartedPayload)
						if payloadErr != nil || !ok || started.RunID != start.RunID || !sameOptionalString(started.PreviousRunID, previousRunID) ||
							start.TraceID != "" && started.TraceID != start.TraceID {
							return nil, errors.New("Harness target run.started provenance is inconsistent")
						}
						targetRunStarted = true
					default:
						return nil, errors.New("Harness target Run has an unexpected event before run.started")
					}
				} else if event.Type == protocol.EventRunStarted || event.TaskID == nil || *event.TaskID != start.TaskID {
					return nil, errors.New("Harness target Run event has a duplicate start or mismatched Task identity")
				}
			} else if targetRunStarted && event.RunID != nil {
				return nil, errors.New("Harness Thread interleaved another Run before the target terminal event")
			}
			if _, duplicate := seenIDs[event.EventID]; duplicate {
				return nil, errors.New("Harness events/read duplicated an event ID")
			}
			seenIDs[event.EventID] = struct{}{}
			events = append(events, event)
			after = event.EventSeq
		}
		if page.NextAfterSeq != after || (page.HasMore && after <= previous) {
			return nil, errors.New("Harness events/read cursor did not advance canonically")
		}
		if !page.HasMore {
			if after != lastEventSeq || !targetRunStarted {
				return nil, errors.New("Harness events/read ended before the terminal sequence")
			}
			return events, nil
		}
	}
}

func sameOptionalString(value *string, want string) bool {
	if value == nil {
		return want == ""
	}
	return *value == want
}

type verifierEventChain struct {
	actionID  string
	attemptID string
	completed protocol.Event
	terminal  protocol.Event
}

func findVerifierEventChain(events []protocol.Event, start protocol.StartResult, lastEventSeq int64, result protocol.RunResult) (verifierEventChain, error) {
	var chain verifierEventChain
	var prepared, dispatched bool
	var terminalFound bool
	var targetRunStarted bool
	for _, event := range events {
		if event.RunID == nil || *event.RunID != start.RunID {
			continue
		}
		if !targetRunStarted {
			targetRunStarted = event.Type == protocol.EventRunStarted && event.TaskID != nil && *event.TaskID == start.TaskID
			continue
		}
		payload, err := event.TypedPayload()
		if err != nil {
			return verifierEventChain{}, errors.New("Harness event payload is invalid")
		}
		switch value := payload.(type) {
		case *protocol.ActionPreparedPayload:
			if value.Kind != "verification" || value.Name != "process.exec" {
				continue
			}
			if prepared || value.ActionID == "" || value.AttemptID == "" || event.TaskID == nil || *event.TaskID != start.TaskID || event.RunID == nil || *event.RunID != start.RunID {
				return verifierEventChain{}, errors.New("Harness verifier action.prepared identity is duplicate or mismatched")
			}
			prepared = true
			chain.actionID, chain.attemptID = value.ActionID, value.AttemptID
		case *protocol.ActionDispatchStartedPayload:
			if value.ActionID != chain.actionID {
				continue
			}
			if !prepared || dispatched || value.AttemptID != chain.attemptID || event.TaskID == nil || *event.TaskID != start.TaskID || event.RunID == nil || *event.RunID != start.RunID {
				return verifierEventChain{}, errors.New("Harness verifier dispatch identity or ordering is invalid")
			}
			dispatched = true
		case *protocol.ActionCompletedPayload:
			if value.ActionID != chain.actionID {
				continue
			}
			if !dispatched || chain.completed.EventID != "" || value.AttemptID != chain.attemptID || value.EffectState != "completed" ||
				value.ExitCode == nil || *value.ExitCode != 0 || !value.CaptureComplete ||
				!equalStrings(value.ResultEvidenceIDs, result.Verification.EvidenceIDs) || event.TaskID == nil || *event.TaskID != start.TaskID ||
				event.RunID == nil || *event.RunID != start.RunID {
				return verifierEventChain{}, errors.New("Harness verifier completion evidence or identity is invalid")
			}
			chain.completed = event
		case *protocol.RunTerminalPayload:
			if terminalFound || event.TaskID == nil || *event.TaskID != start.TaskID || event.RunID == nil || *event.RunID != start.RunID ||
				value.Status != result.Status || value.Code != result.Code {
				return verifierEventChain{}, errors.New("Harness run.terminal identity or result differs")
			}
			terminalFound = true
			chain.terminal = event
		}
	}
	if !targetRunStarted || !prepared || !dispatched || chain.completed.EventID == "" || !terminalFound || chain.terminal.EventSeq != lastEventSeq ||
		events[len(events)-1].EventID != chain.terminal.EventID || chain.completed.EventSeq >= chain.terminal.EventSeq {
		return verifierEventChain{}, errors.New("Harness verifier event chain is incomplete or out of order")
	}
	return chain, nil
}

func nativeRunResultAccepted(result protocol.RunResult) bool {
	return result.Status == "completed" && result.Verification.Status == "passed" && len(result.UnresolvedActionIDs) == 0
}

func containsAll(all, required []string) bool {
	set := make(map[string]struct{}, len(all))
	for _, value := range all {
		set[value] = struct{}{}
	}
	for _, value := range required {
		if _, ok := set[value]; !ok {
			return false
		}
	}
	return true
}

func uniqueNonempty(values []string) bool {
	if len(values) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
		if _, ok := seen[value]; ok {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}
