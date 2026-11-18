package taskcenter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	dailyDefaultLimit      = 50
	dailyMaxLimit          = 100
	dailyMaxCursorBytes    = 2048
	dailyMaxSourceLabel    = 160
	dailyMaxReferenceBytes = 512
)

type dailyCursor struct {
	Version    int    `json:"v"`
	KindID     string `json:"kind_id"`
	AfterID    string `json:"after_id"`
	ScopeToken string `json:"scope"`
}

type dailyAuthorizedScope struct {
	scope tool.ToolExecutionScope
}

func (c *Center) ListDaily(ctx context.Context, query DailyQuery) (DailyListResult, error) {
	scope, err := dailyScopeFromContext(ctx)
	if err != nil {
		return DailyListResult{}, err
	}
	if ctx == nil {
		return DailyListResult{}, ErrForbidden
	}
	if err := ctx.Err(); err != nil {
		return DailyListResult{}, err
	}
	if query.KindID != strings.TrimSpace(query.KindID) || (query.KindID != "" && !dailyCanonicalID(query.KindID)) {
		return DailyListResult{}, fmt.Errorf("%w: invalid daily kind filter", ErrInvalid)
	}
	if query.Limit <= 0 {
		query.Limit = dailyDefaultLimit
	}
	if query.Limit > dailyMaxLimit {
		return DailyListResult{}, fmt.Errorf("%w: daily limit exceeds %d", ErrInvalid, dailyMaxLimit)
	}
	if c == nil || len(c.sources) == 0 || len(c.byID) == 0 {
		return DailyListResult{}, ErrUnavailable
	}
	var afterID string
	if query.Cursor != "" {
		afterID, err = decodeDailyCursor(query.Cursor, query.KindID, scope)
		if err != nil {
			return DailyListResult{}, err
		}
	}

	readAt := c.now().UTC()
	result := DailyListResult{
		Definitions: []DailyDefinition{}, Issues: []DailyIssue{},
		Availability: DailyAvailabilityAvailable, ObservedAt: readAt,
	}
	rows := make([]DailyDefinition, 0, len(c.byID))
	issues := make([]DailyIssue, 0)
	visibleSourceCount := 0
	successfulSourceCount := 0
	failedSourceCount := 0
	partialSourceCount := 0

	for _, source := range c.sources {
		if err := ctx.Err(); err != nil {
			return DailyListResult{}, err
		}
		visibleDefinitions := make([]DailyDefinitionDescriptor, 0, len(source.descriptor.Definitions))
		for _, descriptor := range source.descriptor.Definitions {
			if query.KindID != "" && descriptor.KindID != query.KindID {
				continue
			}
			if dailyDefinitionVisible(scope.scope, descriptor) {
				visibleDefinitions = append(visibleDefinitions, descriptor)
			}
		}
		if len(visibleDefinitions) == 0 {
			continue
		}
		visibleSourceCount++

		snapshot, readErr := source.source.Snapshot(ctx, cloneDailyToolScope(scope.scope))
		if ctxErr := ctx.Err(); ctxErr != nil {
			return DailyListResult{}, ctxErr
		}
		if readErr != nil {
			failedSourceCount++
			issue := DailyIssue{SourceID: source.descriptor.SourceID, Code: DailyIssueSourceUnavailable}
			issues = append(issues, issue)
			for _, descriptor := range visibleDefinitions {
				rows = append(rows, unavailableDailyDefinition(scope.scope, descriptor, source.descriptor.SourceID, DailyIssueSourceUnavailable))
			}
			continue
		}

		observations, valid := indexDailyObservations(snapshot, source.descriptor, visibleDefinitions)
		if !valid {
			failedSourceCount++
			issue := DailyIssue{SourceID: source.descriptor.SourceID, Code: DailyIssueInvalidProjection}
			issues = append(issues, issue)
			for _, descriptor := range visibleDefinitions {
				rows = append(rows, unavailableDailyDefinition(scope.scope, descriptor, source.descriptor.SourceID, DailyIssueInvalidProjection))
			}
			continue
		}

		snapshotIssues, issueValid := normalizeDailyIssues(snapshot.Issues, source.descriptor, visibleDefinitions)
		if !issueValid {
			failedSourceCount++
			issue := DailyIssue{SourceID: source.descriptor.SourceID, Code: DailyIssueInvalidProjection}
			issues = append(issues, issue)
			for _, descriptor := range visibleDefinitions {
				rows = append(rows, unavailableDailyDefinition(scope.scope, descriptor, source.descriptor.SourceID, DailyIssueInvalidProjection))
			}
			continue
		}
		if len(snapshotIssues) > 0 {
			partialSourceCount++
			issues = append(issues, snapshotIssues...)
		}

		sourceHasValidObservation := false
		for _, descriptor := range visibleDefinitions {
			observation, found := observations[descriptor.DefinitionID]
			if !found {
				partialSourceCount++
				failedSourceCount++
				issue := DailyIssue{SourceID: source.descriptor.SourceID, OwnerRef: descriptor.OwnerRef, Code: DailyIssueObservationMissing}
				issues = append(issues, issue)
				row := unavailableDailyDefinition(scope.scope, descriptor, source.descriptor.SourceID, DailyIssueObservationMissing)
				row.Observed.State = DailyObservedUnknown
				row.SourceObservedAt = unknownDailyValue[time.Time](source.descriptor.SourceID, string(DailyIssueObservationMissing))
				row.Issues = append(row.Issues, issue)
				rows = append(rows, row)
				continue
			}
			if validateDailyObservation(observation, descriptor) != nil {
				failedSourceCount++
				partialSourceCount++
				issue := DailyIssue{SourceID: source.descriptor.SourceID, OwnerRef: descriptor.OwnerRef, Code: DailyIssueInvalidProjection}
				issues = append(issues, issue)
				rows = append(rows, unavailableDailyDefinition(scope.scope, descriptor, source.descriptor.SourceID, DailyIssueInvalidProjection))
				continue
			}
			rowIssues, rowIssuesValid := normalizeDailyIssues(observation.Issues, source.descriptor, []DailyDefinitionDescriptor{descriptor})
			if !rowIssuesValid {
				failedSourceCount++
				partialSourceCount++
				issue := DailyIssue{SourceID: source.descriptor.SourceID, OwnerRef: descriptor.OwnerRef, Code: DailyIssueInvalidProjection}
				issues = append(issues, issue)
				rows = append(rows, unavailableDailyDefinition(scope.scope, descriptor, source.descriptor.SourceID, DailyIssueInvalidProjection))
				continue
			}
			if len(rowIssues) > 0 {
				partialSourceCount++
				issues = append(issues, rowIssues...)
			}
			rows = append(rows, projectDailyDefinition(scope.scope, descriptor, source.descriptor.SourceID, snapshot.ObservedAt, observation, append(snapshotIssues, rowIssues...)))
			sourceHasValidObservation = true
		}
		if sourceHasValidObservation {
			successfulSourceCount++
		}
	}

	if visibleSourceCount > 0 && successfulSourceCount == 0 && failedSourceCount > 0 {
		result.Availability = DailyAvailabilityUnavailable
	} else if failedSourceCount > 0 || partialSourceCount > 0 {
		result.Availability = DailyAvailabilityPartial
	}
	result.Partial = result.Availability != DailyAvailabilityAvailable

	sort.Slice(rows, func(i, j int) bool { return rows[i].DefinitionID < rows[j].DefinitionID })
	issues = uniqueSortedDailyIssues(issues)
	result.Issues = issues
	filtered := make([]DailyDefinition, 0, len(rows))
	for _, row := range rows {
		if row.DefinitionID > afterID {
			filtered = append(filtered, row)
		}
	}
	if len(filtered) > query.Limit {
		page := filtered[:query.Limit]
		result.Definitions = page
		result.NextCursor = encodeDailyCursor(query.KindID, page[len(page)-1].DefinitionID, scope)
	} else {
		result.Definitions = filtered
	}
	return result, nil
}

func dailyScopeFromContext(ctx context.Context) (dailyAuthorizedScope, error) {
	if ctx == nil {
		return dailyAuthorizedScope{}, ErrForbidden
	}
	scope, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil {
		return dailyAuthorizedScope{}, ErrForbidden
	}
	switch scope.ActorKind {
	case tool.ActorKindUser:
		if scope.ActorID != scope.AuthenticatedUserID {
			return dailyAuthorizedScope{}, ErrForbidden
		}
	case tool.ActorKindAgent:
		if strings.TrimSpace(scope.ActorID) == "" {
			return dailyAuthorizedScope{}, ErrForbidden
		}
	default:
		return dailyAuthorizedScope{}, ErrForbidden
	}
	if !scope.Allows(tool.DataScopePublic) && !scope.Allows(tool.DataScopeUser) && !scope.Allows(tool.DataScopeInternal) {
		return dailyAuthorizedScope{}, ErrUnsupported
	}
	return dailyAuthorizedScope{scope: cloneDailyToolScope(scope)}, nil
}

func dailyDefinitionVisible(scope tool.ToolExecutionScope, descriptor DailyDefinitionDescriptor) bool {
	switch descriptor.DataScope {
	case tool.DataScopePublic:
		return scope.Allows(tool.DataScopePublic)
	case tool.DataScopeUser:
		return scope.Allows(tool.DataScopeUser) && scope.AuthenticatedUserID != "" && scope.AuthenticatedUserID == descriptor.UserID
	case tool.DataScopeInternal:
		return scope.Allows(tool.DataScopeInternal)
	default:
		return false
	}
}

func cloneDailyToolScope(scope tool.ToolExecutionScope) tool.ToolExecutionScope {
	scope.AllowedDataScopes = append([]string(nil), scope.AllowedDataScopes...)
	return scope
}

func indexDailyObservations(snapshot DailySourceSnapshot, descriptor DailySourceDescriptor, visible []DailyDefinitionDescriptor) (map[string]DailyObservation, bool) {
	known := make(map[string]DailyDefinitionDescriptor, len(descriptor.Definitions))
	visibleIDs := make(map[string]struct{}, len(visible))
	for _, definition := range descriptor.Definitions {
		known[definition.DefinitionID] = definition
	}
	for _, definition := range visible {
		visibleIDs[definition.DefinitionID] = struct{}{}
	}
	indexed := make(map[string]DailyObservation, len(snapshot.Observations))
	for _, observation := range snapshot.Observations {
		_, exists := known[observation.DefinitionID]
		if !exists {
			return nil, false
		}
		if _, canRead := visibleIDs[observation.DefinitionID]; !canRead {
			continue
		}
		if _, duplicate := indexed[observation.DefinitionID]; duplicate {
			return nil, false
		}
		indexed[observation.DefinitionID] = observation
	}
	return indexed, true
}

func projectDailyDefinition(scope tool.ToolExecutionScope, descriptor DailyDefinitionDescriptor, sourceID string, snapshotAt time.Time, observation DailyObservation, issues []DailyIssue) DailyDefinition {
	row := DailyDefinition{
		DailyDefinitionDescriptor: cloneDailyDefinitionDescriptor(descriptor),
		Configured:                cloneDailyAxis(observation.Configured), Effective: cloneDailyAxis(observation.Effective), Observed: cloneDailyAxis(observation.Observed),
		SourceObservedAt: unknownDailyValue[time.Time](sourceID, "snapshot_time_unknown"),
		Timing:           cloneDailyTimingProjection(observation.Timing), NextRunAt: cloneDailyValue(observation.NextRunAt),
		TargetCount: cloneDailyValue(observation.TargetCount), NativeRefs: cloneDailyResourceRefs(observation.NativeRefs),
		NativeRefCount: cloneDailyValue(observation.NativeRefCount), NativeRefsTruncated: observation.NativeRefsTruncated,
		LatestEvidence: cloneDailyEvidence(observation.LatestEvidence), Occurrences: cloneDailyOccurrences(observation.Occurrences),
		OccurrenceCount: cloneDailyValue(observation.OccurrenceCount), OccurrencesTruncated: observation.OccurrencesTruncated,
		Issues: append([]DailyIssue{}, issues...),
	}
	if !snapshotAt.IsZero() {
		observedAt := snapshotAt.UTC()
		row.SourceObservedAt = DailyValue[time.Time]{State: DailyValueKnown, SourceRef: sourceID, Value: &observedAt}
	}
	row.SourceRefs, _ = filterDailyResourceRefs(scope, mergeDailyResourceRefs(descriptor.SourceRefs, observation.SourceRefs, sourceID, descriptor))
	var filtered bool
	row.LatestEvidence.Refs, filtered = filterDailyResourceRefs(scope, inheritDailyRefScope(row.LatestEvidence.Refs, descriptor))
	if filtered {
		row.LatestEvidence = scopeFilteredDailyEvidence(sourceID)
	}
	row.NativeRefs, filtered = filterDailyResourceRefs(scope, inheritDailyRefScope(row.NativeRefs, descriptor))
	if filtered {
		row.NativeRefCount = scopeFilteredDailyValue[int](sourceID)
		row.NativeRefsTruncated = false
	}
	visibleOccurrences := make([]DailyOccurrence, 0, len(row.Occurrences))
	filteredOccurrences := false
	for _, occurrence := range row.Occurrences {
		occurrence.NativeRefs, filtered = filterDailyResourceRefs(scope, inheritDailyRefScope(occurrence.NativeRefs, descriptor))
		if filtered {
			filteredOccurrences = true
			continue
		}
		if occurrence.MeetingProgress != nil && occurrence.MeetingProgress.Minutes.Known() {
			minutes := inheritDailyRefScope([]DailyResourceRef{*occurrence.MeetingProgress.Minutes.Value}, descriptor)
			visibleMinutes, minutesFiltered := filterDailyResourceRefs(scope, minutes)
			if minutesFiltered || len(visibleMinutes) != 1 {
				occurrence.MeetingProgress.Minutes = scopeFilteredDailyValue[DailyResourceRef](sourceID)
			} else {
				value := visibleMinutes[0]
				occurrence.MeetingProgress.Minutes.Value = &value
			}
		}
		visibleOccurrences = append(visibleOccurrences, occurrence)
	}
	row.Occurrences = visibleOccurrences
	if filteredOccurrences {
		row.OccurrenceCount = scopeFilteredDailyValue[int](sourceID)
		row.OccurrencesTruncated = false
	}
	return row
}

func unavailableDailyDefinition(scope tool.ToolExecutionScope, descriptor DailyDefinitionDescriptor, sourceID string, issueCode DailyIssueCode) DailyDefinition {
	code := string(issueCode)
	unknownAxis := func(axis string) DailyAxis {
		state := DailyConfiguredUnknown
		if axis == "effective" {
			state = DailyEffectiveUnknown
		} else if axis == "observed" {
			state = DailyObservedUnknown
			if issueCode == DailyIssueSourceUnavailable || issueCode == DailyIssueInvalidProjection {
				state = DailyObservedUnavailable
			}
		}
		return DailyAxis{State: state, SourceRef: sourceID, ReasonCode: code}
	}
	row := DailyDefinition{
		DailyDefinitionDescriptor: cloneDailyDefinitionDescriptor(descriptor),
		Configured:                unknownAxis("configured"), Effective: unknownAxis("effective"), Observed: unknownAxis("observed"),
		SourceObservedAt: DailyValue[time.Time]{State: DailyValueUnknown, SourceRef: sourceID, ReasonCode: code},
		Timing: DailyTimingProjection{
			Timezone:                 unknownDailyValue[string](sourceID, code),
			ScheduledStartAt:         unknownDailyValue[time.Time](sourceID, code),
			NotBefore:                unknownDailyValue[time.Time](sourceID, code),
			AllowedWindows:           unknownDailyValue[[]modulecore.TaskTimeWindow](sourceID, code),
			DeadlineAt:               unknownDailyValue[time.Time](sourceID, code),
			Recurrence:               unknownDailyValue[modulecore.TaskRecurrence](sourceID, code),
			EstimatedDurationSeconds: unknownDailyValue[int64](sourceID, code),
		},
		NextRunAt:   unknownDailyValue[time.Time](sourceID, code),
		TargetCount: unknownDailyValue[int](sourceID, code),
		NativeRefs:  []DailyResourceRef{}, NativeRefCount: unknownDailyValue[int](sourceID, code),
		LatestEvidence: DailyEvidence{
			State:         DailyValueUnknown,
			LastAttemptAt: unknownDailyValue[time.Time](sourceID, code),
			LastSuccessAt: unknownDailyValue[time.Time](sourceID, code),
			LastResult:    unknownDailyValue[DailyOutcome](sourceID, code), Refs: []DailyResourceRef{},
		},
		Occurrences: []DailyOccurrence{}, OccurrenceCount: unknownDailyValue[int](sourceID, code),
		Issues: []DailyIssue{{SourceID: sourceID, OwnerRef: descriptor.OwnerRef, Code: issueCode}},
	}
	row.SourceRefs, _ = filterDailyResourceRefs(scope, mergeDailyResourceRefs(descriptor.SourceRefs, nil, sourceID, descriptor))
	return row
}

func unknownDailyValue[T any](sourceRef, reason string) DailyValue[T] {
	return DailyValue[T]{State: DailyValueUnknown, SourceRef: sourceRef, ReasonCode: reason}
}

func scopeFilteredDailyValue[T any](sourceRef string) DailyValue[T] {
	return unknownDailyValue[T](sourceRef, "scope_filtered")
}

func scopeFilteredDailyEvidence(sourceRef string) DailyEvidence {
	return DailyEvidence{
		State:         DailyValueUnknown,
		LastAttemptAt: scopeFilteredDailyValue[time.Time](sourceRef),
		LastSuccessAt: scopeFilteredDailyValue[time.Time](sourceRef),
		LastResult:    scopeFilteredDailyValue[DailyOutcome](sourceRef),
		Refs:          []DailyResourceRef{},
	}
}

func normalizeDailyIssues(issues []DailyIssue, source DailySourceDescriptor, visibleDefinitions []DailyDefinitionDescriptor) ([]DailyIssue, bool) {
	allowedOwners := make(map[string]struct{}, len(visibleDefinitions))
	for _, definition := range visibleDefinitions {
		allowedOwners[definition.OwnerRef] = struct{}{}
	}
	normalized := make([]DailyIssue, 0, len(issues))
	for _, issue := range issues {
		if issue.SourceID != "" && issue.SourceID != source.SourceID {
			return nil, false
		}
		if issue.Code == "" || !dailySafeCode(string(issue.Code)) {
			return nil, false
		}
		if issue.OwnerRef != "" {
			if !dailySafeReference(issue.OwnerRef) {
				return nil, false
			}
			if _, exists := allowedOwners[issue.OwnerRef]; !exists {
				return nil, false
			}
		}
		issue.SourceID = source.SourceID
		normalized = append(normalized, issue)
	}
	return normalized, true
}

func validateDailyObservation(observation DailyObservation, descriptor DailyDefinitionDescriptor) error {
	if observation.DefinitionID != descriptor.DefinitionID {
		return ErrInvalid
	}
	if !validDailyAxis(observation.Configured, "configured") || !validDailyAxis(observation.Effective, "effective") || !validDailyAxis(observation.Observed, "observed") {
		return ErrInvalid
	}
	if err := validateDailyTiming(observation.Timing); err != nil {
		return err
	}
	if err := validateDailyValue(observation.NextRunAt, validateDailyTimestamp); err != nil {
		return err
	}
	if err := validateDailyValue(observation.TargetCount, func(value int) error {
		if value < 0 {
			return ErrInvalid
		}
		return nil
	}); err != nil {
		return err
	}
	if err := validateDailyValue(observation.NativeRefCount, func(value int) error {
		if value < 0 {
			return ErrInvalid
		}
		return nil
	}); err != nil {
		return err
	}
	if err := validateDailyBoundedCount(observation.NativeRefCount, observation.NativeRefsTruncated, len(observation.NativeRefs)); err != nil {
		return ErrInvalid
	}
	if len(observation.NativeRefs) > DailyMaxNativeRefs || len(observation.Occurrences) > DailyMaxOccurrences {
		return ErrInvalid
	}
	if err := validateDailyEvidence(observation.LatestEvidence, descriptor); err != nil {
		return err
	}
	if err := validateDailyValue(observation.OccurrenceCount, func(value int) error {
		if value < 0 {
			return ErrInvalid
		}
		return nil
	}); err != nil {
		return err
	}
	if err := validateDailyBoundedCount(observation.OccurrenceCount, observation.OccurrencesTruncated, len(observation.Occurrences)); err != nil {
		return ErrInvalid
	}
	for _, ref := range observation.SourceRefs {
		if !validDailyResourceRef(ref, descriptor) {
			return ErrInvalid
		}
	}
	for _, ref := range observation.NativeRefs {
		if !validDailyResourceRef(ref, descriptor) {
			return ErrInvalid
		}
	}
	for _, occurrence := range observation.Occurrences {
		if err := validateDailyOccurrence(occurrence, descriptor); err != nil {
			return err
		}
	}
	if len(observation.LatestEvidence.Refs) > DailyMaxNativeRefs {
		return ErrInvalid
	}
	for _, issue := range observation.Issues {
		if issue.Code == "" || !dailySafeCode(string(issue.Code)) ||
			(issue.SourceID != "" && issue.SourceID != descriptor.SourceID) ||
			(issue.OwnerRef != "" && issue.OwnerRef != descriptor.OwnerRef) {
			return ErrInvalid
		}
	}
	return nil
}

func validateDailyBoundedCount(value DailyValue[int], truncated bool, payloadLength int) error {
	if payloadLength < 0 {
		return ErrInvalid
	}
	switch value.State {
	case DailyValueKnown:
		if value.Value == nil || *value.Value < payloadLength {
			return ErrInvalid
		}
		if !truncated && *value.Value != payloadLength {
			return ErrInvalid
		}
	case DailyValueUnknown, DailyValueUnsupported:
		return nil
	case DailyValueNone:
		if truncated || payloadLength != 0 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validateDailyEvidence(evidence DailyEvidence, descriptor DailyDefinitionDescriptor) error {
	if !validDailyValueState(evidence.State) {
		return ErrInvalid
	}
	if err := validateDailyValue(evidence.LastAttemptAt, validateDailyTimestamp); err != nil {
		return err
	}
	if err := validateDailyValue(evidence.LastSuccessAt, validateDailyTimestamp); err != nil {
		return err
	}
	if err := validateDailyValue(evidence.LastResult, func(value DailyOutcome) error {
		switch value {
		case DailyOutcomeSuccess, DailyOutcomeFailure, DailyOutcomePartial, DailyOutcomeRunning, DailyOutcomeMissed, DailyOutcomeUnknown:
			return nil
		default:
			return ErrInvalid
		}
	}); err != nil {
		return err
	}
	for _, ref := range evidence.Refs {
		if !validDailyResourceRef(ref, descriptor) {
			return ErrInvalid
		}
	}
	return nil
}

func validateDailyOccurrence(occurrence DailyOccurrence, descriptor DailyDefinitionDescriptor) error {
	if occurrence.OccurrenceID == "" || !dailySafeReference(occurrence.OccurrenceID) {
		return ErrInvalid
	}
	if len(occurrence.NativeRefs) > DailyMaxNativeRefs {
		return ErrInvalid
	}
	switch occurrence.State {
	case DailyOccurrenceScheduled, DailyOccurrenceRunning, DailyOccurrenceSucceeded, DailyOccurrenceFailed, DailyOccurrencePartial, DailyOccurrenceMissed, DailyOccurrenceUnknown, DailyOccurrenceUnavailable:
	default:
		return ErrInvalid
	}
	if occurrence.ScheduledAt != nil {
		if err := validateDailyValue(*occurrence.ScheduledAt, validateDailyTimestamp); err != nil {
			return err
		}
	}
	for _, value := range []DailyValue[time.Time]{occurrence.NextRunAt, occurrence.LastAttemptAt, occurrence.LastSuccessAt} {
		if err := validateDailyValue(value, validateDailyTimestamp); err != nil {
			return err
		}
	}
	if err := validateDailyValue(occurrence.Result, func(value DailyOutcome) error {
		switch value {
		case DailyOutcomeSuccess, DailyOutcomeFailure, DailyOutcomePartial, DailyOutcomeRunning, DailyOutcomeMissed, DailyOutcomeUnknown:
			return nil
		default:
			return ErrInvalid
		}
	}); err != nil {
		return err
	}
	if occurrence.MeetingProgress != nil {
		if err := validateDailyMeetingProgress(*occurrence.MeetingProgress, descriptor); err != nil {
			return err
		}
	}
	for _, ref := range occurrence.NativeRefs {
		if !validDailyResourceRef(ref, descriptor) {
			return ErrInvalid
		}
	}
	return nil
}

func validateDailyMeetingProgress(progress DailyMeetingProgress, descriptor DailyDefinitionDescriptor) error {
	for _, count := range []DailyProgressCount{progress.Attendance, progress.DeliveredReports, progress.AnsweredConcerns} {
		for _, value := range []DailyValue[int]{count.Current, count.Total} {
			if err := validateDailyValue(value, func(value int) error {
				if value < 0 {
					return ErrInvalid
				}
				return nil
			}); err != nil {
				return err
			}
		}
		if count.Current.Known() && count.Total.Known() && *count.Current.Value > *count.Total.Value {
			return ErrInvalid
		}
	}
	if err := validateDailyValue(progress.UnresolvedConcerns, func(value int) error {
		if value < 0 {
			return ErrInvalid
		}
		return nil
	}); err != nil {
		return err
	}
	if progress.UnresolvedConcerns.Known() && progress.AnsweredConcerns.Total.Known() &&
		*progress.UnresolvedConcerns.Value > *progress.AnsweredConcerns.Total.Value {
		return ErrInvalid
	}
	return validateDailyValue(progress.Minutes, func(value DailyResourceRef) error {
		if !validDailyResourceRef(value, descriptor) {
			return ErrInvalid
		}
		return nil
	})
}

func validateDailyTiming(timing DailyTimingProjection) error {
	if err := validateDailyValue(timing.Timezone, func(value string) error {
		if value == "" || value != strings.TrimSpace(value) {
			return ErrInvalid
		}
		if _, err := time.LoadLocation(value); err != nil {
			return ErrInvalid
		}
		return nil
	}); err != nil {
		return err
	}
	for _, value := range []DailyValue[time.Time]{timing.ScheduledStartAt, timing.NotBefore, timing.DeadlineAt} {
		if err := validateDailyValue(value, validateDailyTimestamp); err != nil {
			return err
		}
	}
	if err := validateDailyValue(timing.AllowedWindows, func(value []modulecore.TaskTimeWindow) error {
		if value == nil {
			return ErrInvalid
		}
		for _, window := range value {
			if window.Start != strings.TrimSpace(window.Start) || window.End != strings.TrimSpace(window.End) {
				return ErrInvalid
			}
			start, err := time.Parse("15:04", window.Start)
			if err != nil {
				return ErrInvalid
			}
			end, err := time.Parse("15:04", window.End)
			if err != nil || start.Equal(end) {
				return ErrInvalid
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := validateDailyValue(timing.Recurrence, func(value modulecore.TaskRecurrence) error { return value.Validate() }); err != nil {
		return err
	}
	return validateDailyValue(timing.EstimatedDurationSeconds, func(value int64) error {
		if value < 0 {
			return ErrInvalid
		}
		return nil
	})
}

func validateDailyTimestamp(value time.Time) error {
	if value.IsZero() {
		return ErrInvalid
	}
	return nil
}

func validateDailyValue[T any](value DailyValue[T], validate func(T) error) error {
	if value.SourceRef == "" || !dailySafeReference(value.SourceRef) || (value.ReasonCode != "" && !dailySafeCode(value.ReasonCode)) {
		return ErrInvalid
	}
	switch value.State {
	case DailyValueKnown:
		if value.Value == nil || validate == nil || validate(*value.Value) != nil {
			return ErrInvalid
		}
	case DailyValueNone, DailyValueUnknown, DailyValueUnsupported:
		if value.Value != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validDailyValueState(state DailyValueState) bool {
	switch state {
	case DailyValueKnown, DailyValueNone, DailyValueUnknown, DailyValueUnsupported:
		return true
	default:
		return false
	}
}

func validDailyAxis(axis DailyAxis, kind string) bool {
	if axis.SourceRef == "" || !dailySafeReference(axis.SourceRef) || (axis.ReasonCode != "" && !dailySafeCode(axis.ReasonCode)) {
		return false
	}
	switch kind {
	case "implementation":
		return axis.State == DailyImplementationImplemented || axis.State == DailyImplementationUnimplemented || axis.State == DailyImplementationUnsupported || axis.State == DailyImplementationUnknown
	case "configured":
		return axis.State == DailyConfiguredConfigured || axis.State == DailyConfiguredUnconfigured || axis.State == DailyConfiguredDisabled || axis.State == DailyConfiguredUnsupported || axis.State == DailyConfiguredUnknown
	case "effective":
		return axis.State == DailyEffectiveEnabled || axis.State == DailyEffectiveDisabled || axis.State == DailyEffectiveBlocked || axis.State == DailyEffectiveUnavailable || axis.State == DailyEffectiveUnsupported || axis.State == DailyEffectiveUnknown
	case "observed":
		return axis.State == DailyObservedAvailable || axis.State == DailyObservedUnavailable || axis.State == DailyObservedStale || axis.State == DailyObservedNotObserved || axis.State == DailyObservedUnsupported || axis.State == DailyObservedUnknown
	default:
		return false
	}
}

func validateDailySourceDescriptor(descriptor DailySourceDescriptor) error {
	if !dailyCanonicalID(descriptor.SourceID) || len(descriptor.Definitions) == 0 {
		return ErrInvalid
	}
	seen := make(map[string]struct{}, len(descriptor.Definitions))
	for _, definition := range descriptor.Definitions {
		if definition.SourceID != descriptor.SourceID || !dailyCanonicalID(definition.DefinitionID) || !dailyCanonicalID(definition.KindID) ||
			!dailySafeLabel(definition.Label) || !dailySafeReference(definition.OwnerRef) || !dailyCanonicalID(definition.ResourceType) {
			return ErrInvalid
		}
		if _, exists := seen[definition.DefinitionID]; exists {
			return ErrInvalid
		}
		seen[definition.DefinitionID] = struct{}{}
		switch definition.DataScope {
		case tool.DataScopePublic, tool.DataScopeInternal:
			if definition.UserID != "" {
				return ErrInvalid
			}
		case tool.DataScopeUser:
			if definition.UserID == "" || definition.UserID != strings.TrimSpace(definition.UserID) {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		if !validDailyAxis(definition.Implementation, "implementation") {
			return ErrInvalid
		}
		for _, ref := range definition.SourceRefs {
			if !validDailyResourceRef(ref, definition) {
				return ErrInvalid
			}
		}
	}
	return nil
}

func validDailyResourceRef(ref DailyResourceRef, definition DailyDefinitionDescriptor) bool {
	if !dailySafeReference(ref.OwnerRef) || !dailyCanonicalID(ref.ResourceType) {
		return false
	}
	if ref.SourceID != "" && (!dailyCanonicalID(ref.SourceID) || (definition.SourceID != "" && ref.SourceID != definition.SourceID)) {
		return false
	}
	if ref.Reference != "" && !dailySafeReference(ref.Reference) {
		return false
	}
	if ref.NativeID != "" && (!dailySafeReference(ref.NativeID) || ref.NativeID != strings.TrimSpace(ref.NativeID)) {
		return false
	}
	if ref.DataScope != "" {
		switch ref.DataScope {
		case tool.DataScopePublic, tool.DataScopeInternal:
			if ref.UserID != "" {
				return false
			}
		case tool.DataScopeUser:
			if ref.UserID == "" || ref.UserID != strings.TrimSpace(ref.UserID) {
				return false
			}
		default:
			return false
		}
	} else if ref.UserID != "" {
		return false
	}
	if ref.DisplayName != "" && (!dailySafeLabel(ref.DisplayName) || filepath.IsAbs(ref.DisplayName) || strings.Contains(ref.DisplayName, "\\") || strings.Contains(ref.DisplayName, "://") || strings.HasPrefix(ref.DisplayName, "~/")) {
		return false
	}
	if ref.Filename != "" {
		if len(ref.Filename) > dailyMaxSourceLabel || filepath.Base(ref.Filename) != ref.Filename || ref.Filename == "." || ref.Filename == ".." || strings.Contains(ref.Filename, "..") || strings.ContainsAny(ref.Filename, "/\\") || !dailySafeLabel(ref.Filename) {
			return false
		}
	}
	if ref.LinkRef != "" && (!dailySafeReference(ref.LinkRef) || strings.HasPrefix(ref.LinkRef, "/") || strings.Contains(ref.LinkRef, "..") || strings.Contains(ref.LinkRef, "://")) {
		return false
	}
	return true
}

func dailyCanonicalID(value string) bool {
	if value == "" || value != strings.ToLower(value) || value != strings.TrimSpace(value) || len(value) > dailyMaxReferenceBytes {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r) {
			continue
		}
		return false
	}
	return true
}

func dailySafeReference(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || dailyAbsoluteReference(value) || len(value) > dailyMaxReferenceBytes || strings.Contains(value, "://") || strings.Contains(value, "..") || strings.ContainsAny(value, "\\?#") {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("._:/-", r) {
			continue
		}
		return false
	}
	return true
}

func dailyAbsoluteReference(value string) bool {
	if filepath.IsAbs(value) || strings.HasPrefix(value, "~/") {
		return true
	}
	if len(value) >= 3 && dailyASCIILetter(value[0]) && value[1] == ':' && (value[2] == '/' || value[2] == '\\') {
		return true
	}
	colon := strings.IndexByte(value, ':')
	if colon > 0 && strings.EqualFold(value[:colon], "file") {
		return true
	}
	if colon <= 0 || colon+1 >= len(value) || value[colon+1] != '/' || !dailyASCIILetter(value[0]) {
		return false
	}
	for index := 1; index < colon; index++ {
		character := value[index]
		if !dailyASCIILetter(character) && !(character >= '0' && character <= '9') && character != '+' && character != '.' && character != '-' {
			return false
		}
	}
	return true
}

func dailyASCIILetter(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z')
}

func dailySafeLabel(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len([]rune(value)) > dailyMaxSourceLabel ||
		dailyAbsoluteReference(value) || strings.Contains(value, "\\") {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func dailySafeCode(value string) bool {
	if value == "" || len(value) > 64 || value != strings.ToLower(value) {
		return false
	}
	for index, r := range value {
		allowed := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("._-", r)
		if !allowed || (index == 0 && !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func cloneDailySourceDescriptor(descriptor DailySourceDescriptor) DailySourceDescriptor {
	clone := DailySourceDescriptor{SourceID: descriptor.SourceID, Definitions: make([]DailyDefinitionDescriptor, len(descriptor.Definitions))}
	for index, definition := range descriptor.Definitions {
		clone.Definitions[index] = cloneDailyDefinitionDescriptor(definition)
	}
	return clone
}

func cloneDailyDefinitionDescriptor(descriptor DailyDefinitionDescriptor) DailyDefinitionDescriptor {
	clone := descriptor
	clone.SourceRefs = cloneDailyResourceRefs(descriptor.SourceRefs)
	return clone
}

func cloneDailyAxis(axis DailyAxis) DailyAxis { return axis }

func cloneDailyValue[T any](value DailyValue[T]) DailyValue[T] {
	clone := value
	if value.Value != nil {
		item := *value.Value
		clone.Value = &item
	}
	return clone
}

func cloneDailyTimingProjection(timing DailyTimingProjection) DailyTimingProjection {
	clone := timing
	clone.Timezone = cloneDailyValue(timing.Timezone)
	clone.ScheduledStartAt = cloneDailyValue(timing.ScheduledStartAt)
	clone.NotBefore = cloneDailyValue(timing.NotBefore)
	clone.DeadlineAt = cloneDailyValue(timing.DeadlineAt)
	clone.AllowedWindows = cloneDailyValue(timing.AllowedWindows)
	if timing.AllowedWindows.Value != nil {
		windows := append([]modulecore.TaskTimeWindow{}, (*timing.AllowedWindows.Value)...)
		clone.AllowedWindows.Value = &windows
	}
	clone.Recurrence = cloneDailyValue(timing.Recurrence)
	if timing.Recurrence.Value != nil {
		recurrence := *timing.Recurrence.Value
		recurrence.Times = append([]string{}, recurrence.Times...)
		recurrence.Weekdays = append([]time.Weekday{}, recurrence.Weekdays...)
		if recurrence.IntervalSeconds != nil {
			interval := *recurrence.IntervalSeconds
			recurrence.IntervalSeconds = &interval
		}
		clone.Recurrence.Value = &recurrence
	}
	clone.EstimatedDurationSeconds = cloneDailyValue(timing.EstimatedDurationSeconds)
	return clone
}

func cloneDailyEvidence(evidence DailyEvidence) DailyEvidence {
	clone := evidence
	clone.LastAttemptAt = cloneDailyValue(evidence.LastAttemptAt)
	clone.LastSuccessAt = cloneDailyValue(evidence.LastSuccessAt)
	clone.LastResult = cloneDailyValue(evidence.LastResult)
	clone.Refs = cloneDailyResourceRefs(evidence.Refs)
	return clone
}

func cloneDailyOccurrences(occurrences []DailyOccurrence) []DailyOccurrence {
	clones := make([]DailyOccurrence, len(occurrences))
	for index, occurrence := range occurrences {
		clones[index] = occurrence
		if occurrence.ScheduledAt != nil {
			scheduledAt := cloneDailyValue(*occurrence.ScheduledAt)
			clones[index].ScheduledAt = &scheduledAt
		}
		clones[index].NextRunAt = cloneDailyValue(occurrence.NextRunAt)
		clones[index].LastAttemptAt = cloneDailyValue(occurrence.LastAttemptAt)
		clones[index].LastSuccessAt = cloneDailyValue(occurrence.LastSuccessAt)
		clones[index].Result = cloneDailyValue(occurrence.Result)
		clones[index].NativeRefs = cloneDailyResourceRefs(occurrence.NativeRefs)
		if occurrence.MeetingProgress != nil {
			progress := cloneDailyMeetingProgress(*occurrence.MeetingProgress)
			clones[index].MeetingProgress = &progress
		}
	}
	return clones
}

func cloneDailyMeetingProgress(progress DailyMeetingProgress) DailyMeetingProgress {
	clone := progress
	clone.Attendance.Current = cloneDailyValue(progress.Attendance.Current)
	clone.Attendance.Total = cloneDailyValue(progress.Attendance.Total)
	clone.DeliveredReports.Current = cloneDailyValue(progress.DeliveredReports.Current)
	clone.DeliveredReports.Total = cloneDailyValue(progress.DeliveredReports.Total)
	clone.AnsweredConcerns.Current = cloneDailyValue(progress.AnsweredConcerns.Current)
	clone.AnsweredConcerns.Total = cloneDailyValue(progress.AnsweredConcerns.Total)
	clone.UnresolvedConcerns = cloneDailyValue(progress.UnresolvedConcerns)
	clone.Minutes = cloneDailyValue(progress.Minutes)
	return clone
}

func cloneDailyResourceRefs(refs []DailyResourceRef) []DailyResourceRef {
	return append([]DailyResourceRef{}, refs...)
}

func mergeDailyResourceRefs(first, second []DailyResourceRef, sourceID string, definition DailyDefinitionDescriptor) []DailyResourceRef {
	merged := make([]DailyResourceRef, 0, len(first)+len(second))
	seen := make(map[string]struct{}, len(first)+len(second))
	for _, ref := range append(cloneDailyResourceRefs(first), second...) {
		if ref.SourceID == "" {
			ref.SourceID = sourceID
		}
		if ref.DataScope == "" {
			ref.DataScope = definition.DataScope
			if definition.DataScope == tool.DataScopeUser {
				ref.UserID = definition.UserID
			}
		}
		key, _ := json.Marshal(ref)
		if _, exists := seen[string(key)]; exists {
			continue
		}
		seen[string(key)] = struct{}{}
		merged = append(merged, ref)
	}
	return merged
}

func inheritDailyRefScope(refs []DailyResourceRef, definition DailyDefinitionDescriptor) []DailyResourceRef {
	result := cloneDailyResourceRefs(refs)
	for index := range result {
		if result[index].DataScope == "" {
			result[index].DataScope = definition.DataScope
			if definition.DataScope == tool.DataScopeUser {
				result[index].UserID = definition.UserID
			}
		}
	}
	return result
}

func filterDailyResourceRefs(scope tool.ToolExecutionScope, refs []DailyResourceRef) ([]DailyResourceRef, bool) {
	filtered := make([]DailyResourceRef, 0, len(refs))
	removed := false
	for _, ref := range refs {
		switch ref.DataScope {
		case tool.DataScopePublic:
			if scope.Allows(tool.DataScopePublic) {
				filtered = append(filtered, ref)
			} else {
				removed = true
			}
		case tool.DataScopeUser:
			if scope.Allows(tool.DataScopeUser) && scope.AuthenticatedUserID != "" && scope.AuthenticatedUserID == ref.UserID {
				filtered = append(filtered, ref)
			} else {
				removed = true
			}
		case tool.DataScopeInternal:
			if scope.Allows(tool.DataScopeInternal) {
				filtered = append(filtered, ref)
			} else {
				removed = true
			}
		default:
			removed = true
		}
	}
	return filtered, removed
}

func uniqueSortedDailyIssues(issues []DailyIssue) []DailyIssue {
	if len(issues) == 0 {
		return []DailyIssue{}
	}
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].SourceID != issues[j].SourceID {
			return issues[i].SourceID < issues[j].SourceID
		}
		if issues[i].OwnerRef != issues[j].OwnerRef {
			return issues[i].OwnerRef < issues[j].OwnerRef
		}
		return issues[i].Code < issues[j].Code
	})
	result := make([]DailyIssue, 0, len(issues))
	for _, issue := range issues {
		if len(result) > 0 && result[len(result)-1] == issue {
			continue
		}
		result = append(result, issue)
	}
	return result
}

func encodeDailyCursor(kindID, afterID string, scope dailyAuthorizedScope) string {
	cursor := dailyCursor{Version: 1, KindID: kindID, AfterID: afterID, ScopeToken: dailyScopeToken(scope.scope)}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeDailyCursor(value, kindID string, scope dailyAuthorizedScope) (string, error) {
	if len(value) > dailyMaxCursorBytes {
		return "", fmt.Errorf("%w: invalid daily cursor", ErrInvalid)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return "", fmt.Errorf("%w: invalid daily cursor", ErrInvalid)
	}
	var cursor dailyCursor
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return "", fmt.Errorf("%w: invalid daily cursor", ErrInvalid)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) || cursor.Version != 1 || cursor.KindID != kindID || !dailyCanonicalID(cursor.AfterID) || cursor.ScopeToken != dailyScopeToken(scope.scope) {
		return "", fmt.Errorf("%w: invalid daily cursor", ErrInvalid)
	}
	return cursor.AfterID, nil
}

func dailyScopeToken(scope tool.ToolExecutionScope) string {
	scopes := append([]string(nil), scope.AllowedDataScopes...)
	sort.Strings(scopes)
	identity := string(scope.ActorKind) + "\x00" + scope.ActorID + "\x00" + scope.AuthenticatedUserID + "\x00" + strings.Join(scopes, ",")
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

var _ DailyRead = (*Center)(nil)
