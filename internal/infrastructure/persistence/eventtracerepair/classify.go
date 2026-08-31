package eventtracerepair

import (
	"encoding/json"
	"sort"
	"strings"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	repairEvidenceMessageReceivedRoot         = "message_received_root"
	repairEvidenceRunQueueClaimedRoot         = "run_queue_claimed_root"
	repairEvidenceBackgroundFailure           = "background_failure_root"
	repairEvidenceTTSSession                  = "tts_session_existing_trace"
	repairEvidenceIdleChatSessionTopicRoot    = "idlechat_session_topic_root"
	repairEvidenceIdleChatStoryTurnRoot       = "idlechat_story_turn_sequence_root"
	repairEvidenceIdleChatForecastFailureRoot = "idlechat_forecast_failure_root"

	unresolvedReasonMissingOwnerRoot            = "missing_owner_root"
	unresolvedReasonAmbiguousRoot               = "ambiguous_root"
	unresolvedReasonAmbiguousIdleChatSession    = "ambiguous_idlechat_session"
	unresolvedReasonInvalidIdleChatTurnSequence = "invalid_idlechat_turn_sequence"
)

type repairGroup struct {
	indexes []int
	traces  map[modulecore.TraceID]struct{}
}

type repairSegment struct {
	indexes  []int
	target   modulecore.TraceID
	evidence string
}

type repairSegmentRef struct {
	jobID   string
	segment int
}

type repairResult struct {
	events                     []modulecore.EventEnvelope
	repairJobCount             int
	repairSegmentCount         int
	repairEventCount           int
	verifiedJobCount           int
	repairableJobCount         int
	unresolvedJobCount         int
	repairIdleChatRunCount     int
	verifiedIdleChatRunCount   int
	repairableIdleChatRunCount int
	unresolvedIdleChatRunCount int
	repairEvidenceCounts       map[string]int
	unresolvedReasonCounts     map[string]int
}

type ownerRootKind string

const (
	ownerRootMessageReceived ownerRootKind = "message_received"
	ownerRootQueueClaimed    ownerRootKind = "run_queue_claimed"
)

type ownerRoot struct {
	position   int
	eventIndex int
	kind       ownerRootKind
}

func statusForUnresolved(unresolvedJobCount, unresolvedIdleChatRunCount int) string {
	if unresolvedJobCount > 0 || unresolvedIdleChatRunCount > 0 {
		return StatusReadyWithUnresolved
	}
	return StatusReady
}

func classifyAndRepair(input []modulecore.EventEnvelope) (repairResult, error) {
	result := repairResult{
		events:                 append([]modulecore.EventEnvelope(nil), input...),
		repairEvidenceCounts:   make(map[string]int),
		unresolvedReasonCounts: make(map[string]int),
	}
	if err := modulecore.ValidateEventEnvelopeGraph(input); err != nil {
		return repairResult{}, fail("invalid_graph", "%v", err)
	}
	groups, eventGroup, err := collectRepairGroups(input)
	if err != nil {
		return repairResult{}, err
	}
	idleChatGroups, err := collectIdleChatGroups(input, eventGroup)
	if err != nil {
		return repairResult{}, err
	}
	if err := validateCrossGroupReferences(input, eventGroup); err != nil {
		return repairResult{}, err
	}

	jobIDs := make([]string, 0, len(groups))
	for jobID := range groups {
		jobIDs = append(jobIDs, jobID)
	}
	sort.Strings(jobIDs)
	segmentRefs := make(map[modulecore.EventID]repairSegmentRef)
	for _, jobID := range jobIDs {
		group := groups[jobID]
		if len(group.traces) <= 1 {
			result.verifiedJobCount++
			continue
		}

		segments, reason := classifyGroup(input, group)
		if reason != "" {
			result.unresolvedJobCount++
			result.unresolvedReasonCounts[reason]++
			continue
		}
		changedEventCount := 0
		changedSegmentCount := 0
		for segmentIndex, segment := range segments {
			segmentChanged := 0
			for _, eventIndex := range segment.indexes {
				segmentRefs[input[eventIndex].EventID] = repairSegmentRef{jobID: "job:" + jobID, segment: segmentIndex}
				if input[eventIndex].TraceID == segment.target {
					continue
				}
				result.events[eventIndex].TraceID = segment.target
				segmentChanged++
			}
			if segmentChanged > 0 {
				changedSegmentCount++
				changedEventCount += segmentChanged
				result.repairEvidenceCounts[segment.evidence]++
			}
		}
		if changedEventCount == 0 {
			result.verifiedJobCount++
			continue
		}
		result.repairJobCount++
		result.repairableJobCount++
		result.repairSegmentCount += changedSegmentCount
		result.repairEventCount += changedEventCount
	}

	idleChatSessionIDs := make([]string, 0, len(idleChatGroups))
	for sessionID := range idleChatGroups {
		idleChatSessionIDs = append(idleChatSessionIDs, sessionID)
	}
	sort.Strings(idleChatSessionIDs)
	for _, sessionID := range idleChatSessionIDs {
		group := idleChatGroups[sessionID]
		if len(group.indexes) == 1 {
			result.verifiedIdleChatRunCount++
			continue
		}
		segments, unresolvedRunCount, reason := classifyIdleChatGroup(input, sessionID, group)
		if reason != "" {
			result.unresolvedIdleChatRunCount += unresolvedRunCount
			result.unresolvedReasonCounts[reason] += unresolvedRunCount
			continue
		}
		for segmentIndex, segment := range segments {
			segmentTraces := make(map[modulecore.TraceID]struct{})
			for _, eventIndex := range segment.indexes {
				segmentRefs[input[eventIndex].EventID] = repairSegmentRef{jobID: "idlechat:" + sessionID, segment: segmentIndex}
				segmentTraces[input[eventIndex].TraceID] = struct{}{}
			}
			if len(segmentTraces) <= 1 {
				result.verifiedIdleChatRunCount++
				continue
			}
			changedEventCount := 0
			for _, eventIndex := range segment.indexes {
				if input[eventIndex].TraceID == segment.target {
					continue
				}
				result.events[eventIndex].TraceID = segment.target
				changedEventCount++
			}
			if changedEventCount == 0 {
				result.verifiedIdleChatRunCount++
				continue
			}
			result.repairIdleChatRunCount++
			result.repairableIdleChatRunCount++
			result.repairSegmentCount++
			result.repairEventCount += changedEventCount
			result.repairEvidenceCounts[segment.evidence]++
		}
	}
	if err := validateCrossSegmentReferences(input, segmentRefs); err != nil {
		return repairResult{}, err
	}
	if err := modulecore.ValidateEventEnvelopeGraph(result.events); err != nil {
		return repairResult{}, fail("invalid_repaired_graph", "%v", err)
	}
	return result, nil
}

func collectRepairGroups(input []modulecore.EventEnvelope) (map[string]*repairGroup, map[modulecore.EventID]string, error) {
	groups := make(map[string]*repairGroup)
	eventJob := make(map[modulecore.EventID]string)
	for index, event := range input {
		jobID, err := eventJobID(event)
		if err != nil {
			return nil, nil, err
		}
		if jobID == "" {
			continue
		}
		group := groups[jobID]
		if group == nil {
			group = &repairGroup{traces: make(map[modulecore.TraceID]struct{})}
			groups[jobID] = group
		}
		group.indexes = append(group.indexes, index)
		group.traces[event.TraceID] = struct{}{}
		eventJob[event.EventID] = "job:" + jobID
	}
	return groups, eventJob, nil
}

func collectIdleChatGroups(input []modulecore.EventEnvelope, eventGroup map[modulecore.EventID]string) (map[string]*repairGroup, error) {
	groups := make(map[string]*repairGroup)
	for index, event := range input {
		if event.EventType != "idlechat.topic" && event.EventType != "idlechat.message" && event.EventType != "idlechat.summary" {
			continue
		}
		if event.ComponentID != "orchestrator" {
			return nil, fail("invalid_idlechat_owner", "event %q has idle chat type %q owned by %q", event.EventID, event.EventType, event.ComponentID)
		}
		sessionID, ok := eventPayloadString(event.Payload, "session_id")
		if !ok {
			return nil, fail("missing_idlechat_session", "event %q has no idle chat session_id", event.EventID)
		}
		if owner := eventGroup[event.EventID]; owner != "" {
			return nil, fail("conflicting_repair_identity", "event %q belongs to both %q and idlechat:%s", event.EventID, owner, sessionID)
		}
		group := groups[sessionID]
		if group == nil {
			group = &repairGroup{traces: make(map[modulecore.TraceID]struct{})}
			groups[sessionID] = group
		}
		group.indexes = append(group.indexes, index)
		group.traces[event.TraceID] = struct{}{}
		eventGroup[event.EventID] = "idlechat:" + sessionID
	}
	return groups, nil
}

func validateCrossGroupReferences(input []modulecore.EventEnvelope, eventJob map[modulecore.EventID]string) error {
	for _, event := range input {
		ownerJob := eventJob[event.EventID]
		for _, ref := range references(event) {
			refJob := eventJob[ref]
			if ownerJob != refJob && (ownerJob != "" || refJob != "") {
				return fail("cross_group_reference", "event %q and reference %q cross repair groups", event.EventID, ref)
			}
		}
	}
	return nil
}

func validateCrossSegmentReferences(input []modulecore.EventEnvelope, segmentRefs map[modulecore.EventID]repairSegmentRef) error {
	for _, event := range input {
		owner, ownerOK := segmentRefs[event.EventID]
		if !ownerOK {
			continue
		}
		for _, ref := range references(event) {
			referenced, refOK := segmentRefs[ref]
			if refOK && owner != referenced {
				return fail("cross_segment_reference", "event %q and reference %q cross repair segments", event.EventID, ref)
			}
		}
	}
	return nil
}

func classifyGroup(input []modulecore.EventEnvelope, group *repairGroup) ([]repairSegment, string) {
	ordered := orderedGroupIndexes(input, group.indexes)
	if segment, ok := classifyStandaloneTTS(input, ordered); ok {
		return []repairSegment{segment}, ""
	}
	if segment, ok := classifyBackgroundFailure(input, ordered); ok {
		return []repairSegment{segment}, ""
	}
	segments, reason := classifyOwnerRootSegments(input, ordered)
	if reason != "" {
		return nil, reason
	}
	return segments, ""
}

func classifyIdleChatGroup(input []modulecore.EventEnvelope, sessionID string, group *repairGroup) ([]repairSegment, int, string) {
	ordered := orderedGroupIndexes(input, group.indexes)
	topicCount := 0
	summaryCount := 0
	for _, eventIndex := range ordered {
		event := input[eventIndex]
		if payloadSessionID, ok := eventPayloadString(event.Payload, "session_id"); !ok || payloadSessionID != sessionID {
			return nil, 1, unresolvedReasonAmbiguousIdleChatSession
		}
		switch event.EventType {
		case "idlechat.topic":
			topicCount++
		case "idlechat.message":
		case "idlechat.summary":
			summaryCount++
		default:
			return nil, 1, unresolvedReasonAmbiguousIdleChatSession
		}
	}

	if topicCount > 0 {
		return classifyIdleChatTopicSession(input, ordered, topicCount, summaryCount)
	}
	if summaryCount > 0 {
		return nil, 1, unresolvedReasonAmbiguousIdleChatSession
	}
	if segment, ok := classifyIdleChatForecastFailure(input, sessionID, ordered); ok {
		return []repairSegment{segment}, 0, ""
	}
	return classifyIdleChatStoryRuns(input, sessionID, ordered)
}

func classifyIdleChatTopicSession(input []modulecore.EventEnvelope, ordered []int, topicCount, summaryCount int) ([]repairSegment, int, string) {
	if topicCount != 1 || summaryCount > 1 {
		return nil, 1, unresolvedReasonAmbiguousIdleChatSession
	}
	expectedTurn := 1
	topicSeen := false
	announcementSeen := false
	for _, eventIndex := range ordered {
		event := input[eventIndex]
		turn, present, valid := idleChatTurnIndex(event)
		if !valid {
			return nil, 1, unresolvedReasonInvalidIdleChatTurnSequence
		}
		if event.EventType == "idlechat.topic" {
			if present {
				return nil, 1, unresolvedReasonInvalidIdleChatTurnSequence
			}
			topicSeen = true
			continue
		}
		if !present {
			from, fromOK := eventPayloadString(event.Payload, "from")
			to, toOK := eventPayloadString(event.Payload, "to")
			if event.EventType != "idlechat.message" || announcementSeen || topicSeen || !fromOK || !toOK || from != "user" || to != "mio" {
				return nil, 1, unresolvedReasonAmbiguousIdleChatSession
			}
			announcementSeen = true
			continue
		}
		if !topicSeen {
			return nil, 1, unresolvedReasonAmbiguousIdleChatSession
		}
		if turn != expectedTurn {
			return nil, 1, unresolvedReasonInvalidIdleChatTurnSequence
		}
		expectedTurn++
	}
	if expectedTurn == 1 {
		return nil, 1, unresolvedReasonInvalidIdleChatTurnSequence
	}
	return []repairSegment{{
		indexes:  ordered,
		target:   input[ordered[0]].TraceID,
		evidence: repairEvidenceIdleChatSessionTopicRoot,
	}}, 0, ""
}

func classifyIdleChatForecastFailure(input []modulecore.EventEnvelope, sessionID string, ordered []int) (repairSegment, bool) {
	if !strings.HasPrefix(sessionID, "forecast-") || len(ordered) != 2 {
		return repairSegment{}, false
	}
	root := input[ordered[0]]
	failure := input[ordered[1]]
	if root.EventType != "idlechat.message" || failure.EventType != "idlechat.message" {
		return repairSegment{}, false
	}
	if _, present, valid := idleChatTurnIndex(root); present || !valid {
		return repairSegment{}, false
	}
	failureTurn, present, valid := idleChatTurnIndex(failure)
	if !present || !valid || failureTurn != 1 {
		return repairSegment{}, false
	}
	rootFrom, rootFromOK := eventPayloadString(root.Payload, "from")
	rootTo, rootToOK := eventPayloadString(root.Payload, "to")
	failureFrom, failureFromOK := eventPayloadString(failure.Payload, "from")
	failureTo, failureToOK := eventPayloadString(failure.Payload, "to")
	if !rootFromOK || !rootToOK || !failureFromOK || !failureToOK || rootFrom != "user" || rootTo != "mio" || failureFrom != "shiro" || failureTo != "mio" {
		return repairSegment{}, false
	}
	return repairSegment{
		indexes:  ordered,
		target:   root.TraceID,
		evidence: repairEvidenceIdleChatForecastFailureRoot,
	}, true
}

func classifyIdleChatStoryRuns(input []modulecore.EventEnvelope, sessionID string, ordered []int) ([]repairSegment, int, string) {
	rootCount := 0
	for _, eventIndex := range ordered {
		if turn, present, valid := idleChatTurnIndex(input[eventIndex]); present && valid && turn == 1 {
			rootCount++
		}
	}
	unresolvedCount := rootCount
	if unresolvedCount == 0 {
		unresolvedCount = 1
	}
	if !strings.HasPrefix(sessionID, "story-episode-") {
		return nil, unresolvedCount, unresolvedReasonAmbiguousIdleChatSession
	}

	segments := make([]repairSegment, 0, rootCount)
	current := make([]int, 0)
	expectedTurn := 1
	for _, eventIndex := range ordered {
		event := input[eventIndex]
		if event.EventType != "idlechat.message" {
			return nil, unresolvedCount, unresolvedReasonAmbiguousIdleChatSession
		}
		turn, present, valid := idleChatTurnIndex(event)
		if !present || !valid {
			return nil, unresolvedCount, unresolvedReasonInvalidIdleChatTurnSequence
		}
		if turn == 1 && len(current) > 0 {
			segments = append(segments, repairSegment{
				indexes:  current,
				target:   input[current[0]].TraceID,
				evidence: repairEvidenceIdleChatStoryTurnRoot,
			})
			current = make([]int, 0)
			expectedTurn = 1
		}
		if turn != expectedTurn {
			return nil, unresolvedCount, unresolvedReasonInvalidIdleChatTurnSequence
		}
		current = append(current, eventIndex)
		expectedTurn++
	}
	if len(current) == 0 {
		return nil, unresolvedCount, unresolvedReasonInvalidIdleChatTurnSequence
	}
	segments = append(segments, repairSegment{
		indexes:  current,
		target:   input[current[0]].TraceID,
		evidence: repairEvidenceIdleChatStoryTurnRoot,
	})
	return segments, 0, ""
}

func idleChatTurnIndex(event modulecore.EventEnvelope) (int, bool, bool) {
	if event.Payload == nil {
		return 0, false, true
	}
	raw, present := event.Payload["turn_index"]
	if !present || raw == nil {
		return 0, false, true
	}
	var turn int
	switch value := raw.(type) {
	case int:
		turn = value
	case int32:
		turn = int(value)
	case int64:
		turn = int(value)
	case float64:
		turn = int(value)
		if float64(turn) != value {
			return 0, true, false
		}
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, true, false
		}
		turn = int(parsed)
	default:
		return 0, true, false
	}
	return turn, true, turn > 0
}

func orderedGroupIndexes(input []modulecore.EventEnvelope, indexes []int) []int {
	ordered := append([]int(nil), indexes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := input[ordered[i]], input[ordered[j]]
		if left.EventID == right.EventID {
			return ordered[i] < ordered[j]
		}
		return left.EventID < right.EventID
	})
	return ordered
}

func classifyOwnerRootSegments(input []modulecore.EventEnvelope, ordered []int) ([]repairSegment, string) {
	firstRoot := -1
	for position, eventIndex := range ordered {
		if _, ok := ownerRootFor(input[eventIndex]); ok {
			firstRoot = position
			break
		}
	}
	if firstRoot < 0 {
		return nil, unresolvedReasonMissingOwnerRoot
	}

	selected := []ownerRoot{{position: firstRoot, eventIndex: ordered[firstRoot], kind: mustOwnerRootKind(input[ordered[firstRoot]])}}
	currentKind := selected[0].kind
	terminalSeen := false
	queueMessageCount := 0
	for position := firstRoot + 1; position < len(ordered); position++ {
		event := input[ordered[position]]
		if isSegmentTerminal(event) {
			terminalSeen = true
		}
		candidateKind, isRoot := ownerRootFor(event)
		if !isRoot {
			continue
		}
		if currentKind == ownerRootQueueClaimed && candidateKind == ownerRootMessageReceived && !terminalSeen {
			if queueMessageCount > 0 {
				return nil, unresolvedReasonAmbiguousRoot
			}
			queueMessageCount++
			continue
		}
		if currentKind == ownerRootQueueClaimed && queueMessageCount != 1 {
			return nil, unresolvedReasonAmbiguousRoot
		}
		if !terminalSeen {
			return nil, unresolvedReasonAmbiguousRoot
		}
		selected = append(selected, ownerRoot{position: position, eventIndex: ordered[position], kind: candidateKind})
		currentKind = candidateKind
		terminalSeen = false
		queueMessageCount = 0
	}
	if currentKind == ownerRootQueueClaimed && queueMessageCount != 1 {
		return nil, unresolvedReasonAmbiguousRoot
	}

	segments := make([]repairSegment, 0, len(selected))
	for index, root := range selected {
		start := 0
		if index > 0 {
			start = root.position
		}
		end := len(ordered)
		if index+1 < len(selected) {
			end = selected[index+1].position
		}
		evidence := repairEvidenceMessageReceivedRoot
		if root.kind == ownerRootQueueClaimed {
			evidence = repairEvidenceRunQueueClaimedRoot
		}
		segmentIndexes := append([]int(nil), ordered[start:end]...)
		segments = append(segments, repairSegment{
			indexes:  segmentIndexes,
			target:   input[root.eventIndex].TraceID,
			evidence: evidence,
		})
	}
	return segments, ""
}

func ownerRootFor(event modulecore.EventEnvelope) (ownerRootKind, bool) {
	switch {
	case event.ComponentID == "orchestrator" && event.EventType == "message.received":
		return ownerRootMessageReceived, true
	case event.ComponentID == "superagent" && event.EventType == "run_queue.claimed":
		return ownerRootQueueClaimed, true
	default:
		return "", false
	}
}

func mustOwnerRootKind(event modulecore.EventEnvelope) ownerRootKind {
	kind, _ := ownerRootFor(event)
	return kind
}

func isSegmentTerminal(event modulecore.EventEnvelope) bool {
	if event.ComponentID == "orchestrator" {
		switch event.EventType {
		case "agent.response", "viewer.error", "verification.report":
			return true
		}
	}
	if event.ComponentID == "superagent" {
		switch event.EventType {
		case "run_queue.completed", "run_queue.failed":
			return true
		}
	}
	return false
}

func classifyBackgroundFailure(input []modulecore.EventEnvelope, ordered []int) (repairSegment, bool) {
	if len(ordered) != 2 {
		return repairSegment{}, false
	}
	failedIndex := -1
	notificationIndex := -1
	for _, eventIndex := range ordered {
		event := input[eventIndex]
		if event.ComponentID != "orchestrator" {
			return repairSegment{}, false
		}
		switch event.EventType {
		case "background_job.failed":
			if failedIndex >= 0 {
				return repairSegment{}, false
			}
			failedIndex = eventIndex
		case "job.notification":
			if notificationIndex >= 0 {
				return repairSegment{}, false
			}
			notificationIndex = eventIndex
		default:
			return repairSegment{}, false
		}
	}
	if failedIndex < 0 || notificationIndex < 0 {
		return repairSegment{}, false
	}
	return repairSegment{
		indexes:  ordered,
		target:   input[failedIndex].TraceID,
		evidence: repairEvidenceBackgroundFailure,
	}, true
}

func classifyStandaloneTTS(input []modulecore.EventEnvelope, ordered []int) (repairSegment, bool) {
	if len(ordered) == 0 {
		return repairSegment{}, false
	}
	sessions := make(map[string]struct{})
	responses := make(map[string]struct{})
	chunkCount := 0
	completionCount := 0
	for _, eventIndex := range ordered {
		event := input[eventIndex]
		if event.ComponentID != "orchestrator" {
			return repairSegment{}, false
		}
		switch event.EventType {
		case "metrics.latency":
			if !isTTSChunkMetric(event) {
				return repairSegment{}, false
			}
		case "tts.audio_chunk":
			chunkCount++
		case "tts.session_completed":
			completionCount++
		default:
			return repairSegment{}, false
		}
		eventSessions := eventIdentityValues(event, "session_id")
		eventResponses := eventIdentityValues(event, "response_id")
		if len(eventSessions) != 1 || len(eventResponses) != 1 {
			return repairSegment{}, false
		}
		for _, value := range eventSessions {
			sessions[value] = struct{}{}
		}
		for _, value := range eventResponses {
			responses[value] = struct{}{}
		}
	}
	if len(sessions) != 1 || len(responses) != 1 || chunkCount < 1 || completionCount != 1 {
		return repairSegment{}, false
	}
	return repairSegment{
		indexes:  ordered,
		target:   input[ordered[0]].TraceID,
		evidence: repairEvidenceTTSSession,
	}, true
}

func isTTSChunkMetric(event modulecore.EventEnvelope) bool {
	kind, _ := event.Payload["kind"].(string)
	point, _ := event.Payload["point"].(string)
	if kind == "tts" && point == "audio_chunk_ready" {
		return true
	}
	content, ok := eventPayloadString(event.Payload, "content")
	if !ok {
		return false
	}
	var metric map[string]any
	if err := json.Unmarshal([]byte(content), &metric); err != nil {
		return false
	}
	kind, _ = metric["kind"].(string)
	point, _ = metric["point"].(string)
	return kind == "tts" && point == "audio_chunk_ready"
}

func eventIdentityValues(event modulecore.EventEnvelope, key string) []string {
	values := make([]string, 0, 2)
	if raw, ok := event.Payload[key].(string); ok && strings.TrimSpace(raw) != "" {
		values = append(values, strings.TrimSpace(raw))
	}
	if key == "session_id" && event.SessionID != "" {
		values = append(values, string(event.SessionID))
	}
	if key == "response_id" && event.ResponseID != "" {
		values = append(values, string(event.ResponseID))
	}
	if content, ok := eventPayloadString(event.Payload, "content"); ok {
		var nested map[string]any
		if json.Unmarshal([]byte(content), &nested) == nil {
			if raw, ok := nested[key].(string); ok && strings.TrimSpace(raw) != "" {
				values = append(values, strings.TrimSpace(raw))
			}
		}
	}
	return uniqueStrings(values)
}

func eventPayloadString(payload map[string]any, key string) (string, bool) {
	if payload == nil {
		return "", false
	}
	raw, ok := payload[key].(string)
	return strings.TrimSpace(raw), ok && strings.TrimSpace(raw) != ""
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}
