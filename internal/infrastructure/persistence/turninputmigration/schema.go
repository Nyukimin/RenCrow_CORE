package turninputmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsession "github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	rootSessionKeys              = []string{"id", "logical_date", "channel_address", "history", "memory", "created_at", "updated_at"}
	legacyHistoryKeys            = []string{"job_id", "user_message", "channel", "chat_id", "route"}
	canonicalHistoryRequiredKeys = []string{"root_task_id", "turn_id", "trace_id", "user_message_id", "agent_message_id", "message_text", "channel_address", "attachments"}
	canonicalHistoryOptionalKeys = []string{"viewer_recipient", "forced_route", "route"}
)

type sessionSource struct {
	entry             sourceEntry
	id                string
	logicalDate       string
	parent            conversation.ChannelAddress
	legacyParent      bool
	memory            json.RawMessage
	createdAt         time.Time
	updatedAt         time.Time
	legacyRows        []legacyHistoryRow
	canonicalRows     int
	canonicalIdentity []canonicalIdentity
}

type legacyHistoryRow struct {
	jobID       string
	userMessage string
	channel     string
	chatID      string
	route       string
}

type legacyReference struct {
	session *sessionSource
	index   int
	row     legacyHistoryRow
}

type canonicalIdentity struct {
	rootTaskID     modulecore.TaskID
	turnID         modulecore.TurnID
	traceID        modulecore.TraceID
	userMessageID  modulecore.MessageID
	agentMessageID modulecore.MessageID
}

func (identity canonicalIdentity) values() []string {
	return []string{
		string(identity.rootTaskID), string(identity.turnID), string(identity.traceID),
		string(identity.userMessageID), string(identity.agentMessageID),
	}
}

func (identity canonicalIdentity) validate() error {
	if err := identity.rootTaskID.Validate(); err != nil {
		return err
	}
	if err := identity.turnID.Validate(); err != nil {
		return err
	}
	if err := identity.traceID.Validate(); err != nil {
		return err
	}
	if err := identity.userMessageID.Validate(); err != nil {
		return err
	}
	if err := identity.agentMessageID.Validate(); err != nil {
		return err
	}
	if identity.userMessageID == identity.agentMessageID {
		return errors.New("user and agent message IDs must differ")
	}
	seen := make(map[string]struct{}, len(identity.values()))
	for _, value := range identity.values() {
		if _, exists := seen[value]; exists {
			return errors.New("canonical identity values must be distinct")
		}
		seen[value] = struct{}{}
	}
	return nil
}

type rowMapping struct {
	identity canonicalIdentity
	linked   bool
}

func prepare(ctx context.Context, options resolvedOptions) (preparedPlan, error) {
	entries, err := snapshotSource(options.sourceDir)
	if err != nil {
		return preparedPlan{}, err
	}
	sourceBefore, err := hashSourceEntries(entries)
	if err != nil {
		return preparedPlan{}, err
	}

	sessions := make([]*sessionSource, 0)
	canonicalIDs := make(map[string]string)
	legacyJobs := make(map[string]legacyReference)
	directoryStyle := -1
	for _, entry := range entries {
		if !isSessionFilename(entry.name) {
			continue
		}
		sessionValue, parseErr := parseSessionSource(ctx, entry)
		if parseErr != nil {
			return preparedPlan{}, parseErr
		}
		sessions = append(sessions, sessionValue)
		style := 1
		if sessionValue.legacyParent {
			style = 0
		}
		if directoryStyle != -1 && directoryStyle != style {
			return preparedPlan{}, blocked("schema_invalid", errors.New("Session directory mixes legacy and canonical schemas"))
		}
		directoryStyle = style
		if !sessionValue.legacyParent {
			for _, identity := range sessionValue.canonicalIdentity {
				if err := addGlobalIdentity(canonicalIDs, identity, "canonical history"); err != nil {
					return preparedPlan{}, err
				}
			}
		}
		for index, row := range sessionValue.legacyRows {
			if row.jobID == "" {
				return preparedPlan{}, blocked("duplicate_job_id", errors.New("legacy job ID is required"))
			}
			if _, exists := legacyJobs[row.jobID]; exists {
				return preparedPlan{}, blocked("duplicate_job_id", errors.New("legacy job IDs must be globally unique"))
			}
			legacyJobs[row.jobID] = legacyReference{session: sessionValue, index: index, row: row}
		}
	}

	evidence, err := loadEvidence(ctx, options.eventDBPath, options.conversationDBPath, legacyJobs)
	if err != nil {
		return preparedPlan{}, err
	}

	mappings := make(map[string]rowMapping, len(legacyJobs))
	mappingRecords := make([]string, 0, len(legacyJobs))
	receiptLinkedRows := 0
	deterministicRows := 0
	for jobID, reference := range legacyJobs {
		mapping, mapErr := mapLegacyRow(reference, evidence)
		if mapErr != nil {
			return preparedPlan{}, mapErr
		}
		if err := addGlobalIdentity(canonicalIDs, mapping.identity, "migrated history"); err != nil {
			return preparedPlan{}, err
		}
		mappings[jobID] = mapping
		kind := "deterministic"
		if mapping.linked {
			receiptLinkedRows++
			kind = "receipt-linked"
		} else {
			deterministicRows++
		}
		values := mapping.identity.values()
		mappingRecords = append(mappingRecords, strings.Join(append([]string{jobID, kind}, values...), "\x00"))
	}

	plannedFiles := make([]plannedFile, 0, len(entries))
	sessionIDs := make([]string, 0, len(sessions))
	sessionByName := make(map[string]*sessionSource, len(sessions))
	for _, value := range sessions {
		sessionByName[value.entry.name] = value
	}
	legacyHistoryRows := 0
	canonicalHistoryRows := 0
	outputHistoryRows := 0
	canonicalSessionFiles := 0
	for _, entry := range entries {
		value, isSession := sessionByName[entry.name]
		if !isSession {
			plannedFiles = append(plannedFiles, plannedFile{
				name: entry.name, sourcePath: entry.path, permission: entry.permission,
			})
			continue
		}
		sessionIDs = append(sessionIDs, value.id)
		if value.legacyParent {
			data, buildErr := buildLegacySession(value, mappings)
			if buildErr != nil {
				return preparedPlan{}, buildErr
			}
			legacyHistoryRows += len(value.legacyRows)
			outputHistoryRows += len(value.legacyRows)
			plannedFiles = append(plannedFiles, plannedFile{
				name: entry.name, permission: entry.permission, data: data, materialize: true,
				sessionID: value.id, historyRows: len(value.legacyRows),
			})
			continue
		}
		canonicalSessionFiles++
		canonicalHistoryRows += value.canonicalRows
		outputHistoryRows += value.canonicalRows
		plannedFiles = append(plannedFiles, plannedFile{
			name: entry.name, sourcePath: entry.path, permission: entry.permission,
			sessionID: value.id, historyRows: value.canonicalRows,
		})
	}

	sourceAfter, err := hashDirectory(options.sourceDir)
	if err != nil {
		return preparedPlan{}, err
	}
	if sourceAfter != sourceBefore {
		return preparedPlan{}, blocked("source_drift", errors.New("source changed during preparation"))
	}
	outputHash, err := hashPlannedFiles(plannedFiles)
	if err != nil {
		return preparedPlan{}, err
	}
	receipt := Receipt{
		ContractVersion:            ContractVersion,
		Status:                     "ready",
		Mode:                       ModeDryRun,
		SourceSHA256:               sourceBefore,
		EventEvidenceSHA256:        evidence.eventHash,
		ConversationEvidenceSHA256: evidence.conversationHash,
		MappingSHA256:              writeHashStrings(mappingRecords),
		OutputSHA256:               outputHash,
		SourceFiles:                len(entries),
		CanonicalSessionFiles:      canonicalSessionFiles,
		NonSessionFiles:            len(entries) - len(sessions),
		LegacyHistoryRows:          legacyHistoryRows,
		CanonicalHistoryRows:       canonicalHistoryRows,
		ReceiptLinkedRows:          receiptLinkedRows,
		DeterministicRows:          deterministicRows,
		OutputHistoryRows:          outputHistoryRows,
		LegacyHistoryRowsRemaining: 0,
	}
	return preparedPlan{options: options, files: plannedFiles, receipt: receipt, sessionIDs: sessionIDs}, nil
}

func isSessionFilename(name string) bool {
	return strings.HasPrefix(name, "ses_") && strings.HasSuffix(name, ".json")
}

func parseSessionSource(ctx context.Context, entry sourceEntry) (*sessionSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, blocked("context_canceled", err)
	}
	raw, err := os.ReadFile(entry.path)
	if err != nil {
		return nil, blocked("source_read_failed", err)
	}
	fields, err := strictObject(raw)
	if err != nil {
		return nil, blocked("schema_invalid", err)
	}
	if !exactKeys(fields, rootSessionKeys, nil) {
		return nil, blocked("schema_invalid", errors.New("Session root schema is not exact"))
	}
	id, err := decodeString(fields["id"])
	if err != nil || modulecore.SessionID(id).Validate() != nil || entry.name != id+".json" {
		return nil, blocked("schema_invalid", errors.New("Session filename and ID are invalid"))
	}
	logicalDate, err := decodeString(fields["logical_date"])
	if err != nil || domainsession.ValidateLogicalDate(logicalDate) != nil {
		return nil, blocked("schema_invalid", errors.New("logical_date is invalid"))
	}
	memory := append(json.RawMessage(nil), fields["memory"]...)
	if !isJSONObject(memory) {
		return nil, blocked("schema_invalid", errors.New("memory must be an object"))
	}
	createdAt, err := decodeTime(fields["created_at"])
	if err != nil {
		return nil, blocked("schema_invalid", errors.New("created_at is invalid"))
	}
	updatedAt, err := decodeTime(fields["updated_at"])
	if err != nil || updatedAt.Before(createdAt) {
		return nil, blocked("schema_invalid", errors.New("updated_at is invalid"))
	}
	parent, legacyParent, err := parseParentAddress(fields["channel_address"])
	if err != nil {
		return nil, blocked("schema_invalid", err)
	}
	history, err := decodeArray(fields["history"])
	if err != nil {
		return nil, blocked("schema_invalid", err)
	}
	value := &sessionSource{
		entry: entry, id: id, logicalDate: logicalDate, parent: parent, legacyParent: legacyParent,
		memory: memory, createdAt: createdAt, updatedAt: updatedAt,
		legacyRows: make([]legacyHistoryRow, 0), canonicalIdentity: make([]canonicalIdentity, 0),
	}
	rowStyle := -1
	for _, rowRaw := range history {
		rowFields, err := strictObject(rowRaw)
		if err != nil {
			return nil, blocked("schema_invalid", errors.New("history row is not an object"))
		}
		style, legacyRow, identity, rowErr := parseHistoryRow(rowFields, parent)
		if rowErr != nil {
			return nil, blocked("schema_invalid", rowErr)
		}
		if rowStyle != -1 && rowStyle != style {
			return nil, blocked("schema_invalid", errors.New("history rows use mixed schemas"))
		}
		rowStyle = style
		if legacyRow != nil {
			if !legacyParent {
				return nil, blocked("schema_invalid", errors.New("legacy history row has canonical parent address"))
			}
			value.legacyRows = append(value.legacyRows, *legacyRow)
		} else {
			if legacyParent {
				return nil, blocked("schema_invalid", errors.New("canonical history row has legacy parent address"))
			}
			value.canonicalIdentity = append(value.canonicalIdentity, identity)
		}
	}
	if !legacyParent {
		repository := sessionpersistence.NewJSONSessionRepository(filepath.Dir(entry.path))
		loaded, loadErr := repository.Load(ctx, id)
		if loadErr != nil {
			return nil, blocked("canonical_load_failed", loadErr)
		}
		if loaded.ID() != id || loaded.HistoryCount() != len(value.canonicalIdentity) {
			return nil, blocked("canonical_load_failed", errors.New("canonical Session load does not match source"))
		}
		value.canonicalRows = loaded.HistoryCount()
		value.canonicalIdentity = make([]canonicalIdentity, 0, loaded.HistoryCount())
		for _, input := range loaded.GetHistory() {
			identity := canonicalIdentity{
				rootTaskID: input.RootTaskID(), turnID: input.TurnID(), traceID: input.TraceID(),
				userMessageID: input.UserMessageID(), agentMessageID: input.AgentMessageID(),
			}
			if err := identity.validate(); err != nil {
				return nil, blocked("canonical_load_failed", err)
			}
			value.canonicalIdentity = append(value.canonicalIdentity, identity)
		}
	}
	return value, nil
}

func parseParentAddress(raw json.RawMessage) (conversation.ChannelAddress, bool, error) {
	fields, err := strictObject(raw)
	if err != nil {
		return conversation.ChannelAddress{}, false, errors.New("channel_address must be an object")
	}
	if exactKeys(fields, []string{"channel", "address"}, nil) {
		channel, channelErr := decodeString(fields["channel"])
		addressValue, addressErr := decodeString(fields["address"])
		if channelErr != nil || addressErr != nil {
			return conversation.ChannelAddress{}, false, errors.New("legacy channel_address values must be strings")
		}
		address, err := conversation.NewChannelAddress(channel, addressValue)
		if err != nil || channel != address.ChannelType() || addressValue != address.ExternalConversationID() {
			return conversation.ChannelAddress{}, false, errors.New("legacy channel_address is not normalized")
		}
		return address, true, nil
	}
	if exactKeys(fields, []string{"channel_type", "external_conversation_id"}, nil) {
		channel, channelErr := decodeString(fields["channel_type"])
		external, externalErr := decodeString(fields["external_conversation_id"])
		if channelErr != nil || externalErr != nil {
			return conversation.ChannelAddress{}, false, errors.New("canonical channel_address values must be strings")
		}
		address, err := conversation.NewChannelAddress(channel, external)
		if err != nil || channel != address.ChannelType() || external != address.ExternalConversationID() {
			return conversation.ChannelAddress{}, false, errors.New("canonical channel_address is not normalized")
		}
		return address, false, nil
	}
	return conversation.ChannelAddress{}, false, errors.New("channel_address schema is mixed or unknown")
}

func parseHistoryRow(fields map[string]json.RawMessage, parent conversation.ChannelAddress) (int, *legacyHistoryRow, canonicalIdentity, error) {
	if exactKeys(fields, legacyHistoryKeys, nil) {
		jobID, jobErr := decodeString(fields["job_id"])
		userMessage, userErr := decodeString(fields["user_message"])
		channel, channelErr := decodeString(fields["channel"])
		chatID, chatErr := decodeString(fields["chat_id"])
		route, routeErr := decodeString(fields["route"])
		if jobErr != nil || userErr != nil || channelErr != nil || chatErr != nil || routeErr != nil || jobID == "" || channel == "" || chatID == "" {
			return 0, nil, canonicalIdentity{}, errors.New("legacy history row has invalid values")
		}
		if channel != parent.ChannelType() || chatID != parent.ExternalConversationID() {
			return 0, nil, canonicalIdentity{}, errors.New("legacy history address does not match parent")
		}
		return 0, &legacyHistoryRow{jobID: jobID, userMessage: userMessage, channel: channel, chatID: chatID, route: route}, canonicalIdentity{}, nil
	}
	if !keysWithRequired(fields, canonicalHistoryRequiredKeys, canonicalHistoryOptionalKeys) {
		return 0, nil, canonicalIdentity{}, errors.New("history row schema is mixed or unknown")
	}
	identity, err := parseCanonicalIdentity(fields)
	if err != nil {
		return 0, nil, canonicalIdentity{}, err
	}
	address, legacyAddress, err := parseParentAddress(fields["channel_address"])
	if err != nil {
		return 0, nil, canonicalIdentity{}, err
	}
	if legacyAddress {
		return 0, nil, canonicalIdentity{}, errors.New("canonical history address uses legacy schema")
	}
	if address != parent {
		return 0, nil, canonicalIdentity{}, errors.New("canonical history address does not match parent")
	}
	if _, err := decodeString(fields["message_text"]); err != nil {
		return 0, nil, canonicalIdentity{}, errors.New("canonical message_text must be a string")
	}
	if !isJSONArrayOrNull(fields["attachments"]) {
		return 0, nil, canonicalIdentity{}, errors.New("canonical attachments must be an array")
	}
	for _, key := range canonicalHistoryOptionalKeys {
		if raw, ok := fields[key]; ok {
			if _, err := decodeString(raw); err != nil {
				return 0, nil, canonicalIdentity{}, errors.New("canonical optional history fields must be strings")
			}
		}
	}
	return 1, nil, identity, nil
}

func parseCanonicalIdentity(fields map[string]json.RawMessage) (canonicalIdentity, error) {
	root, err := decodeString(fields["root_task_id"])
	if err != nil {
		return canonicalIdentity{}, errors.New("root_task_id must be a string")
	}
	turn, err := decodeString(fields["turn_id"])
	if err != nil {
		return canonicalIdentity{}, errors.New("turn_id must be a string")
	}
	trace, err := decodeString(fields["trace_id"])
	if err != nil {
		return canonicalIdentity{}, errors.New("trace_id must be a string")
	}
	userMessage, err := decodeString(fields["user_message_id"])
	if err != nil {
		return canonicalIdentity{}, errors.New("user_message_id must be a string")
	}
	agentMessage, err := decodeString(fields["agent_message_id"])
	if err != nil {
		return canonicalIdentity{}, errors.New("agent_message_id must be a string")
	}
	identity := canonicalIdentity{
		rootTaskID: modulecore.TaskID(root), turnID: modulecore.TurnID(turn), traceID: modulecore.TraceID(trace),
		userMessageID: modulecore.MessageID(userMessage), agentMessageID: modulecore.MessageID(agentMessage),
	}
	if err := identity.validate(); err != nil {
		return canonicalIdentity{}, errors.New("canonical history IDs are invalid")
	}
	return identity, nil
}

func addGlobalIdentity(seen map[string]string, identity canonicalIdentity, source string) error {
	if err := identity.validate(); err != nil {
		return blocked("canonical_identity_collision", errors.New("canonical history identity is invalid"))
	}
	for _, value := range identity.values() {
		if previous, exists := seen[value]; exists {
			_ = previous
			return blocked("canonical_identity_collision", errors.New("canonical history identity collides"))
		}
		seen[value] = source
	}
	return nil
}

func buildLegacySession(value *sessionSource, mappings map[string]rowMapping) ([]byte, error) {
	history := make([]outputTurnDTO, 0, len(value.legacyRows))
	for _, row := range value.legacyRows {
		mapping, ok := mappings[row.jobID]
		if !ok {
			return nil, blocked("mapping_failed", errors.New("legacy history mapping is missing"))
		}
		history = append(history, outputTurnDTO{
			RootTaskID: string(mapping.identity.rootTaskID), TurnID: string(mapping.identity.turnID),
			TraceID: string(mapping.identity.traceID), UserMessageID: string(mapping.identity.userMessageID),
			AgentMessageID: string(mapping.identity.agentMessageID), MessageText: row.userMessage,
			ChannelAddress: outputAddressDTO{ChannelType: value.parent.ChannelType(), ExternalConversationID: value.parent.ExternalConversationID()},
			Attachments:    []attachment.Attachment{}, Route: row.route,
		})
	}
	output := outputSessionDTO{
		ID: value.id, LogicalDate: value.logicalDate,
		ChannelAddress: outputAddressDTO{ChannelType: value.parent.ChannelType(), ExternalConversationID: value.parent.ExternalConversationID()},
		History:        history, Memory: value.memory, CreatedAt: value.createdAt, UpdatedAt: value.updatedAt,
	}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return nil, blocked("output_plan_failed", err)
	}
	data = append(data, '\n')
	return data, nil
}

type outputSessionDTO struct {
	ID             string           `json:"id"`
	LogicalDate    string           `json:"logical_date"`
	ChannelAddress outputAddressDTO `json:"channel_address"`
	History        []outputTurnDTO  `json:"history"`
	Memory         json.RawMessage  `json:"memory"`
	CreatedAt      time.Time        `json:"created_at"`
	UpdatedAt      time.Time        `json:"updated_at"`
}

type outputTurnDTO struct {
	RootTaskID     string                  `json:"root_task_id"`
	TurnID         string                  `json:"turn_id"`
	TraceID        string                  `json:"trace_id"`
	UserMessageID  string                  `json:"user_message_id"`
	AgentMessageID string                  `json:"agent_message_id"`
	MessageText    string                  `json:"message_text"`
	ChannelAddress outputAddressDTO        `json:"channel_address"`
	Attachments    []attachment.Attachment `json:"attachments"`
	Route          string                  `json:"route"`
}

type outputAddressDTO struct {
	ChannelType            string `json:"channel_type"`
	ExternalConversationID string `json:"external_conversation_id"`
}

func strictObject(raw []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := start.(json.Delim)
	if !ok || delim != '{' {
		return nil, errors.New("JSON value must be an object")
	}
	result := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		key, ok := token.(string)
		if !ok {
			return nil, errors.New("JSON object key must be a string")
		}
		if _, exists := result[key]; exists {
			return nil, errors.New("duplicate JSON object key")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	end, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	endDelim, ok := end.(json.Delim)
	if !ok || endDelim != '}' {
		return nil, errors.New("JSON object is not closed")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return result, nil
}

func decodeArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errors.New("array is required")
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, errors.New("JSON value must be an array")
	}
	return values, nil
}

func decodeString(raw json.RawMessage) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '"' {
		return "", errors.New("JSON value must be a string")
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return "", err
	}
	return value, nil
}

func decodeTime(raw json.RawMessage) (time.Time, error) {
	value, err := decodeString(raw)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() {
		return time.Time{}, errors.New("timestamp is invalid")
	}
	return parsed, nil
}

func exactKeys(fields map[string]json.RawMessage, required, optional []string) bool {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = struct{}{}
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	for _, key := range optional {
		allowed[key] = struct{}{}
	}
	if len(fields) != len(required)+countPresent(fields, optional) {
		return false
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

func keysWithRequired(fields map[string]json.RawMessage, required, optional []string) bool {
	allowed := make(map[string]struct{}, len(required)+len(optional))
	for _, key := range required {
		allowed[key] = struct{}{}
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	for _, key := range optional {
		allowed[key] = struct{}{}
	}
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			return false
		}
	}
	return true
}

func countPresent(fields map[string]json.RawMessage, keys []string) int {
	count := 0
	for _, key := range keys {
		if _, ok := fields[key]; ok {
			count++
		}
	}
	return count
}

func isJSONObject(raw []byte) bool {
	return len(bytes.TrimSpace(raw)) > 0 && bytes.TrimSpace(raw)[0] == '{'
}

func isJSONArrayOrNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return false
	}
	var values []json.RawMessage
	return json.Unmarshal(trimmed, &values) == nil
}
