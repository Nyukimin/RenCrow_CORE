package conversationruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrBoardOwnerRequestRuntimeUnavailable = errors.New("board owner request runtime is unavailable")

// BoardOwnerRequestSource is the accepted owner and BOARD target scope. Its
// string fields keep BOARD references out of CORE typed identity fields.
type BoardOwnerRequestSource struct {
	OwnerID         string `json:"owner_id"`
	BoardInstanceID string `json:"board_instance_id"`
	CorePrincipalID string `json:"core_principal_id"`
	ChannelID       string `json:"channel_id"`
	PeerPrincipalID string `json:"peer_principal_id"`
	Title           string `json:"title"`
	IdempotencyKey  string `json:"idempotency_key"`
	TargetActorID   string `json:"target_actor_id,omitempty"`
}

// NewBoardThreadTarget intentionally has no thread or reply identity because
// a BOARD thread does not exist until the later publication receipt.
type NewBoardThreadTarget struct {
	BoardInstanceID string `json:"board_instance_id"`
	CorePrincipalID string `json:"core_principal_id"`
	ChannelID       string `json:"channel_id"`
	PeerPrincipalID string `json:"peer_principal_id"`
	Title           string `json:"title"`
}

// BoardOwnerRequestLookup binds one accepted owner request and generation
// epoch to its exact source and content fingerprint.
type BoardOwnerRequestLookup struct {
	Source            BoardOwnerRequestSource `json:"source"`
	OwnerID           string                  `json:"owner_id"`
	SessionID         modulecore.SessionID    `json:"session_id"`
	TurnID            modulecore.TurnID       `json:"turn_id"`
	GenerationEpoch   int64                   `json:"generation_epoch"`
	OperationID       string                  `json:"operation_id"`
	SourceFingerprint string                  `json:"source_fingerprint"`
}

// BoardOwnerRequestCommitInput gives the journal one canonical target-actor response
// and the private unresolved BOARD target/key to persist atomically.
type BoardOwnerRequestCommitInput struct {
	ExpectedLookup    BoardOwnerRequestLookup
	Event             modulecore.EventEnvelope
	MessageIndex      MessageIndex
	InvocationReceipt agent.InvocationReceipt
	Target            NewBoardThreadTarget
	IdempotencyKey    string
}

// BoardOwnerRequestOutcome is the exact durable response and private target
// required for replay and later publication recovery.
type BoardOwnerRequestOutcome struct {
	Lookup            BoardOwnerRequestLookup
	Event             modulecore.EventEnvelope
	MessageIndex      MessageIndex
	InvocationReceipt agent.InvocationReceipt
	Target            NewBoardThreadTarget
	IdempotencyKey    string
}

// BoardOwnerRequestJournal atomically stores or recovers the generated
// response and its private unresolved new-thread intent.
type BoardOwnerRequestJournal interface {
	LoadBoardOwnerRequestOutcome(context.Context, BoardOwnerRequestLookup) (BoardOwnerRequestOutcome, bool, error)
	CommitBoardOwnerRequest(context.Context, BoardOwnerRequestCommitInput) (BoardOwnerRequestOutcome, error)
}

type BoardOwnerRequestStatus string

const (
	BoardOwnerRequestStatusFailedBeforeCommit BoardOwnerRequestStatus = "failed_before_commit"
	BoardOwnerRequestStatusCommitted          BoardOwnerRequestStatus = "committed"
	BoardOwnerRequestStatusCommitUncertain    BoardOwnerRequestStatus = "commit_uncertain"
)

type BoardOwnerRequestResult struct {
	Status    BoardOwnerRequestStatus
	Outcome   BoardOwnerRequestOutcome
	Replayed  bool
	Recovered bool
	Cause     error
}

type BoardOwnerRequestRuntimeDependencies struct {
	Composer       PromptComposer
	Generator      EmissionGenerator
	Journal        BoardOwnerRequestJournal
	ActorAdmission ActorAdmission
	PolicyVersion  string
	TargetActorID  string
}

// BoardOwnerRequestRuntime uses Mio's existing CHAT composition and emission
// ports, with a typed journal and no legacy publisher or dispatch queue.
type BoardOwnerRequestRuntime struct {
	composer       PromptComposer
	generator      EmissionGenerator
	journal        BoardOwnerRequestJournal
	actorAdmission ActorAdmission
	policyVersion  string
	targetActorID  string
}

func NewBoardOwnerRequestRuntime(dependencies BoardOwnerRequestRuntimeDependencies) (*BoardOwnerRequestRuntime, error) {
	if dependencies.Composer == nil || dependencies.Generator == nil || dependencies.Journal == nil {
		return nil, errors.New("board owner request dependencies are required")
	}
	policyVersion := strings.TrimSpace(dependencies.PolicyVersion)
	if policyVersion == "" {
		return nil, errors.New("board owner request policy_version is required")
	}
	targetActorID := strings.TrimSpace(dependencies.TargetActorID)
	if targetActorID != dependencies.TargetActorID || targetActorID != "" && ValidateBoardOwnerRequestTargetActorID(targetActorID) != nil {
		return nil, errors.New("board owner request target_actor_id is invalid")
	}
	return &BoardOwnerRequestRuntime{
		composer: dependencies.Composer, generator: dependencies.Generator,
		journal: dependencies.Journal, actorAdmission: dependencies.ActorAdmission,
		policyVersion: policyVersion, targetActorID: targetActorID,
	}, nil
}

// SetActorAdmission attaches the same actor reservation used by ordinary
// conversation generation. Production callers invoke it before processing.
func (r *BoardOwnerRequestRuntime) SetActorAdmission(admission ActorAdmission) {
	if r != nil {
		r.actorAdmission = admission
	}
}

// ValidateBoardOwnerRequestTargetActorID accepts only the eight CORE actors
// that the BOARD owner request route can explicitly target.
func ValidateBoardOwnerRequestTargetActorID(actorID string) error {
	switch actorID {
	case "mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin":
		return nil
	default:
		return errors.New("unsupported BOARD owner request target actor")
	}
}

// RunAcceptedBoardOwnerRequest starts after the EventStore has durably accepted
// the owner input. Initial admission timestamps and Session resolution belong
// to that owner boundary, before this generation method runs.
func (r *BoardOwnerRequestRuntime) RunAcceptedBoardOwnerRequest(ctx context.Context, input AcceptedTurnInput) BoardOwnerRequestResult {
	failed := func(err error) BoardOwnerRequestResult {
		return BoardOwnerRequestResult{Status: BoardOwnerRequestStatusFailedBeforeCommit, Cause: err}
	}
	uncertain := func(err error) BoardOwnerRequestResult {
		return BoardOwnerRequestResult{Status: BoardOwnerRequestStatusCommitUncertain, Cause: err}
	}
	if ctx == nil {
		return failed(errors.New("board owner request context is required"))
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	if r == nil || r.composer == nil || r.generator == nil || r.journal == nil || strings.TrimSpace(r.policyVersion) == "" {
		return failed(ErrBoardOwnerRequestRuntimeUnavailable)
	}

	source, instruction, err := parseAcceptedBoardOwnerRequest(input.ReadViewRequest)
	if err != nil {
		return failed(err)
	}
	if !boardOwnerRequestTargetMatchesRuntime(r.targetActorID, source.TargetActorID) {
		return failed(errors.New("accepted BOARD owner request target actor does not match the bound runtime actor"))
	}
	accepted := input.ReadViewRequest.Accepted
	lookup := BoardOwnerRequestLookup{
		Source: source, OwnerID: input.ReadViewRequest.Session.OwnerID,
		SessionID: accepted.SessionID, TurnID: accepted.TurnID, GenerationEpoch: accepted.GenerationEpoch,
		OperationID: turnOperationID(accepted.TurnID), SourceFingerprint: computeBoardOwnerRequestSourceFingerprint(source, instruction),
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	prior, found, loadErr := r.journal.LoadBoardOwnerRequestOutcome(ctx, lookup)
	if loadErr != nil {
		if err := ctx.Err(); err != nil {
			return failed(err)
		}
		return uncertain(loadErr)
	}
	if found {
		if err := validateBoardOwnerRequestOutcome(prior, lookup, accepted, instruction); err != nil {
			return uncertain(err)
		}
		return BoardOwnerRequestResult{Status: BoardOwnerRequestStatusCommitted, Outcome: prior, Replayed: true}
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}

	scope := boardOwnerRequestScope(input.ReadViewRequest, r.policyVersion, source.TargetActorID)
	canonical := boardOwnerRequestPromptSnapshot(input.ReadViewRequest, instruction)
	composed, err := r.composer.ComposeConversationPrompt(ctx, scope, canonical.Clone())
	if err != nil {
		if ctx.Err() != nil {
			return failed(ctx.Err())
		}
		return failed(fmt.Errorf("compose board owner request with Mio: %w", err))
	}
	if err := validateBoardOwnerRequestPrompt(composed, canonical, instruction); err != nil {
		return failed(err)
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	actorID := source.TargetActorID
	if actorID == "" {
		actorID = conversationActorMio
	}
	var releaseAdmission func()
	if r.actorAdmission != nil && isOperativeHeartbeatActor(actorID) {
		releaseAdmission, err = r.actorAdmission.Acquire(ctx, actorID, domaintask.PriorityHigh)
		if err != nil {
			return failed(fmt.Errorf("acquire %s BOARD owner actor: %w", actorID, err))
		}
		if releaseAdmission == nil {
			return failed(fmt.Errorf("acquire %s BOARD owner actor: admission returned no release", actorID))
		}
		defer releaseAdmission()
	}
	allowlist := conversation.EmissionAllowlist{
		AllowedSourceMessageIDs: append([]modulecore.MessageID(nil), composed.AllowedSourceMessageIDs...),
	}
	emission, receipt, generationErr := r.generator.GenerateEmission(ctx, scope, composed, allowlist)
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	if generationErr != nil {
		return failed(fmt.Errorf("generate board owner request with Mio: %w", generationErr))
	}
	if !validBoardOwnerRequestInvocationReceipt(receipt) {
		return failed(errors.New("Mio board owner request invocation receipt is invalid"))
	}
	validated, err := validateTypedEmission(scope, allowlist, emission)
	if err != nil {
		return failed(fmt.Errorf("validate Mio board owner request emission: %w", err))
	}
	if strings.TrimSpace(validated.UserMessage.Text) == strings.TrimSpace(instruction) {
		return failed(errors.New("Mio board owner request must compose new message content"))
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}

	commit := boardOwnerRequestCommit(input.ReadViewRequest, lookup, validated, receipt)
	committed, commitErr := r.journal.CommitBoardOwnerRequest(ctx, commit)
	if commitErr != nil {
		lookupCtx := context.WithoutCancel(ctx)
		saved, recovered, recoveryErr := r.journal.LoadBoardOwnerRequestOutcome(lookupCtx, lookup)
		if recoveryErr != nil {
			return uncertain(errors.Join(commitErr, recoveryErr))
		}
		if !recovered {
			return uncertain(commitErr)
		}
		if err := validateBoardOwnerRequestOutcome(saved, lookup, accepted, instruction); err != nil || !sameBoardOwnerRequestOutcome(saved, commit) {
			if err == nil {
				err = errors.New("stored board owner request outcome does not match the commit")
			}
			return uncertain(errors.Join(commitErr, err))
		}
		return BoardOwnerRequestResult{Status: BoardOwnerRequestStatusCommitted, Outcome: saved, Recovered: true}
	}
	if err := validateBoardOwnerRequestOutcome(committed, lookup, accepted, instruction); err != nil || !sameBoardOwnerRequestOutcome(committed, commit) {
		if err == nil {
			err = errors.New("committed board owner request outcome does not match the commit")
		}
		return uncertain(err)
	}
	return BoardOwnerRequestResult{Status: BoardOwnerRequestStatusCommitted, Outcome: committed}
}

func parseAcceptedBoardOwnerRequest(request ReadViewRequest) (BoardOwnerRequestSource, string, error) {
	invalid := func(reason string) (BoardOwnerRequestSource, string, error) {
		return BoardOwnerRequestSource{}, "", fmt.Errorf("%w: accepted BOARD owner request %s", ErrReadViewInvalid, reason)
	}
	accepted, session := request.Accepted, request.Session
	if request.Principal.OwnerID == "" || request.Principal.ActorID == "" || session.OwnerID == "" ||
		request.Principal.OwnerID != session.OwnerID || request.Principal.ActorID != accepted.ActorID || accepted.ActorID == "" {
		return invalid("owner provenance is invalid")
	}
	if session.SessionID.Validate() != nil || accepted.TurnID.Validate() != nil || accepted.InputMessageID.Validate() != nil {
		return invalid("identity is invalid")
	}
	if session.RuntimeVersion != ConversationRuntimeV2 || session.CutoverSeq < 0 || session.AdmissionSeq <= 0 ||
		session.ProjectionWatermark < 0 || session.GenerationEpoch <= 0 || accepted.GenerationEpoch != session.GenerationEpoch ||
		accepted.SessionID != session.SessionID || accepted.AdmissionSeq <= session.CutoverSeq || accepted.AdmissionSeq > session.AdmissionSeq ||
		accepted.State != "accepted" || accepted.TriggerKind != "board_owner_request" {
		return invalid("accepted session boundary is inconsistent")
	}
	if len(accepted.ClientSubmissionID) == 0 || len(accepted.ClientSubmissionID) > maxClientSubmissionIDBytes ||
		strings.TrimSpace(accepted.ClientSubmissionID) != accepted.ClientSubmissionID {
		return invalid("client submission identity is invalid")
	}
	event := accepted.InputEvent
	if err := modulecore.ValidateEventEnvelope(event); err != nil || event.EventSeq != accepted.AdmissionSeq ||
		event.SessionID != accepted.SessionID || event.TurnID != accepted.TurnID || event.MessageID != accepted.InputMessageID {
		return invalid("canonical input identity is inconsistent")
	}
	if event.EventType != "conversation.board_owner_request" || event.ComponentID != "orchestrator" ||
		event.ActorKind != "" || event.ActorID != "" || event.CausationEventID != "" || len(event.DependencyEventIDs) != 0 ||
		event.ThreadID != "" || event.RequestID != "" || event.ResponseID != "" || event.UtteranceID != "" ||
		event.TaskID != "" || event.RunID != "" || event.ActionID != "" || event.AttemptID != "" ||
		event.WorkstreamID != "" || event.GoalID != "" || event.ArtifactID != "" || event.EvidenceID != "" ||
		event.MemoryID != "" || event.RelationID != "" || event.ScheduleID != "" || event.QueueItemID != "" ||
		event.CheckpointID != "" || event.ReceiptID != "" {
		return invalid("event type or execution provenance is invalid")
	}
	const payloadFieldCount = 7
	if !exactStringKeys(event.Payload, payloadFieldCount, "from", "source_actor_id", "to", "text", "content", "body", "board_owner_request_binding") {
		return invalid("payload shape is invalid")
	}
	if payloadString(event.Payload, "from") != "board_owner" || payloadString(event.Payload, "source_actor_id") != accepted.ActorID ||
		payloadString(event.Payload, "to") != "mio" {
		return invalid("owner actor provenance is invalid")
	}
	text, textOK := event.Payload["text"].(string)
	content, contentOK := event.Payload["content"].(string)
	body, bodyOK := event.Payload["body"].(map[string]any)
	if !textOK || !contentOK || !bodyOK || !exactStringKeys(body, 1, "text") {
		return invalid("request body shape is invalid")
	}
	bodyText, bodyTextOK := body["text"].(string)
	if !bodyTextOK || text == "" || strings.TrimSpace(text) == "" || text != content || text != bodyText ||
		len([]byte(text)) > conversation.MaxUserMessageBytes || !utf8.ValidString(text) {
		return invalid("request body text is invalid")
	}
	binding, ok := event.Payload["board_owner_request_binding"].(map[string]any)
	legacyBinding := []string{"board_instance_id", "core_principal_id", "channel_id", "peer_principal_id", "title", "idempotency_key"}
	explicitBinding := []string{"board_instance_id", "core_principal_id", "channel_id", "peer_principal_id", "title", "idempotency_key", "target_actor_id"}
	if !ok || !exactStringKeys(binding, len(legacyBinding), legacyBinding...) && !exactStringKeys(binding, len(explicitBinding), explicitBinding...) {
		return invalid("BOARD target binding shape is invalid")
	}
	readRef := func(name string, limit int) (string, bool) {
		value, ok := binding[name].(string)
		return value, ok && validBoardOwnerRequestValue(value, limit)
	}
	boardInstanceID, boardInstanceOK := readRef("board_instance_id", 512)
	corePrincipalID, corePrincipalOK := readRef("core_principal_id", 512)
	channelID, channelOK := readRef("channel_id", 512)
	peerPrincipalID, peerPrincipalOK := readRef("peer_principal_id", 512)
	title, titleOK := readRef("title", 2048)
	idempotencyKey, keyOK := readRef("idempotency_key", 512)
	if !boardInstanceOK || !corePrincipalOK || !channelOK || !peerPrincipalOK || !titleOK || !keyOK {
		return invalid("BOARD target binding value is invalid")
	}
	targetActorID := ""
	if _, explicit := binding["target_actor_id"]; explicit {
		targetActorID, ok = binding["target_actor_id"].(string)
		if !ok || ValidateBoardOwnerRequestTargetActorID(targetActorID) != nil {
			return invalid("target actor binding is invalid")
		}
	}
	return BoardOwnerRequestSource{
		OwnerID: session.OwnerID, BoardInstanceID: boardInstanceID, CorePrincipalID: corePrincipalID,
		ChannelID: channelID, PeerPrincipalID: peerPrincipalID, Title: title, IdempotencyKey: idempotencyKey,
		TargetActorID: targetActorID,
	}, text, nil
}

func exactStringKeys(values map[string]any, count int, expected ...string) bool {
	if len(values) != count {
		return false
	}
	for _, key := range expected {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func validBoardOwnerRequestValue(value string, maxBytes int) bool {
	return value != "" && len(value) <= maxBytes && strings.TrimSpace(value) == value && strings.IndexFunc(value, unicode.IsControl) < 0
}

func computeBoardOwnerRequestSourceFingerprint(source BoardOwnerRequestSource, body string) string {
	material, err := json.Marshal(struct {
		Source BoardOwnerRequestSource `json:"source"`
		Body   string                  `json:"body"`
	}{Source: source, Body: body})
	if err != nil {
		return ""
	}
	fingerprint := sha256.Sum256(material)
	return hex.EncodeToString(fingerprint[:])
}

func boardOwnerRequestScope(request ReadViewRequest, policyVersion, targetActorID string) conversation.ConversationScope {
	accepted := request.Accepted
	actorID := accepted.ActorID
	if targetActorID != "" {
		actorID = targetActorID
	}
	return conversation.ConversationScope{
		TurnID: accepted.TurnID, SessionID: accepted.SessionID, TraceID: accepted.InputEvent.TraceID,
		ActorID: actorID, InputID: accepted.InputMessageID,
		GenerationID: fmt.Sprintf("%s:%d", accepted.TurnID, accepted.GenerationEpoch),
		Purpose:      conversation.PurposeBoardOwnerRequest, PolicyVersion: policyVersion,
	}
}

func boardOwnerRequestPromptSnapshot(request ReadViewRequest, instruction string) agent.PromptSnapshot {
	accepted := request.Accepted
	return agent.PromptSnapshot{
		Messages:                []llm.Message{{Role: "user", Content: instruction, Type: llm.PromptContextUser}},
		AllowedSourceMessageIDs: []modulecore.MessageID{accepted.InputMessageID},
		SourceRevision:          "board-owner-request:" + string(accepted.InputEvent.EventID),
		CutoverRevision:         fmt.Sprintf("cutover:%d", request.Session.CutoverSeq),
	}
}

func validateBoardOwnerRequestPrompt(composed, canonical agent.PromptSnapshot, instruction string) error {
	invalid := errors.New("Mio board owner request prompt contains an unsupported source or context")
	if len(canonical.AllowedSourceMessageIDs) != 1 || len(composed.AllowedSourceMessageIDs) != 1 ||
		composed.AllowedSourceMessageIDs[0] != canonical.AllowedSourceMessageIDs[0] ||
		composed.SourceRevision != canonical.SourceRevision || composed.CutoverRevision != canonical.CutoverRevision {
		return invalid
	}
	userCount, characterCount := 0, 0
	for index, message := range composed.Messages {
		if message.Role == "user" {
			userCount++
			if index != len(composed.Messages)-1 || message.Content != instruction || message.Type != llm.PromptContextUser {
				return invalid
			}
			continue
		}
		if message.Role != "system" || message.Type != llm.PromptContextCharacter || strings.TrimSpace(message.Content) == "" {
			return invalid
		}
		characterCount++
	}
	if userCount != 1 || characterCount == 0 {
		return invalid
	}
	return nil
}

func boardOwnerRequestCommit(request ReadViewRequest, lookup BoardOwnerRequestLookup, emission conversation.AgentEmission, receipt agent.InvocationReceipt) BoardOwnerRequestCommitInput {
	accepted := request.Accepted
	actorID := boardOwnerRequestCanonicalActorID(lookup.Source)
	messageID := modulecore.NewMessageID()
	text := emission.UserMessage.Text
	payload := map[string]any{
		"kind": "reply", "visibility": "public", "from": actorID, "source_actor_id": actorID, "to": "external_peer",
		"text": text, "content": text,
	}
	if lookup.Source.TargetActorID != "" {
		payload["mentions"] = []any{lookup.Source.PeerPrincipalID}
	}
	event := modulecore.NewEventEnvelope(accepted.InputEvent.TraceID, accepted.InputEvent.EventID, nil, "conversation_runtime", "agent.response", time.Now().UTC(), payload)
	event.SessionID, event.TurnID, event.MessageID = accepted.SessionID, accepted.TurnID, messageID
	event.ActorKind, event.ActorID = "agent", actorID
	index := MessageIndex{
		MessageID: messageID, EventID: event.EventID, SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		FromActor: actorID, ToKind: "external_peer", ToActor: lookup.Source.PeerPrincipalID,
		Kind: "board_owner_request", Visibility: "public",
	}
	return BoardOwnerRequestCommitInput{
		ExpectedLookup: lookup, Event: event, MessageIndex: index, InvocationReceipt: receipt,
		Target: NewBoardThreadTarget{
			BoardInstanceID: lookup.Source.BoardInstanceID, CorePrincipalID: lookup.Source.CorePrincipalID,
			ChannelID: lookup.Source.ChannelID, PeerPrincipalID: lookup.Source.PeerPrincipalID, Title: lookup.Source.Title,
		},
		IdempotencyKey: lookup.Source.IdempotencyKey,
	}
}

func boardOwnerRequestCanonicalActorID(source BoardOwnerRequestSource) string {
	if source.TargetActorID == "" {
		return "mio"
	}
	return source.TargetActorID
}

func boardOwnerRequestTargetMatchesRuntime(runtimeActorID, requestedActorID string) bool {
	if runtimeActorID == "mio" {
		return requestedActorID == "" || requestedActorID == "mio"
	}
	return runtimeActorID == requestedActorID
}

func boardOwnerRequestMentionsMatch(value any, peerPrincipalID string) bool {
	switch mentions := value.(type) {
	case []string:
		return len(mentions) == 1 && mentions[0] == peerPrincipalID
	case []any:
		if len(mentions) != 1 {
			return false
		}
		mention, ok := mentions[0].(string)
		return ok && mention == peerPrincipalID
	default:
		return false
	}
}

func validBoardOwnerRequestInvocationReceipt(receipt agent.InvocationReceipt) bool {
	return receipt.Status == "completed" && receipt.RequestID != "" && receipt.ResponseID != ""
}

func validateBoardOwnerRequestOutcome(outcome BoardOwnerRequestOutcome, expected BoardOwnerRequestLookup, accepted AcceptedTurn, instruction string) error {
	if outcome.Lookup != expected || outcome.Target != (NewBoardThreadTarget{
		BoardInstanceID: expected.Source.BoardInstanceID, CorePrincipalID: expected.Source.CorePrincipalID,
		ChannelID: expected.Source.ChannelID, PeerPrincipalID: expected.Source.PeerPrincipalID, Title: expected.Source.Title,
	}) || outcome.IdempotencyKey != expected.Source.IdempotencyKey {
		return errors.New("stored board owner request target or lookup does not match the accepted scope")
	}
	event, index := outcome.Event, outcome.MessageIndex
	actorID := boardOwnerRequestCanonicalActorID(expected.Source)
	validPayloadShape := exactStringKeys(event.Payload, 7, "kind", "visibility", "from", "source_actor_id", "to", "text", "content")
	if expected.Source.TargetActorID != "" {
		validPayloadShape = exactStringKeys(event.Payload, 8, "kind", "visibility", "from", "source_actor_id", "to", "text", "content", "mentions") &&
			boardOwnerRequestMentionsMatch(event.Payload["mentions"], expected.Source.PeerPrincipalID)
	}
	if err := modulecore.ValidateEventEnvelope(event); err != nil ||
		event.EventType != "agent.response" || event.ComponentID != "conversation_runtime" ||
		event.ActorKind != "agent" || event.ActorID != actorID || event.TraceID != accepted.InputEvent.TraceID ||
		event.CausationEventID != accepted.InputEvent.EventID || len(event.DependencyEventIDs) != 0 ||
		event.SessionID != accepted.SessionID || event.TurnID != accepted.TurnID || event.ThreadID != "" ||
		event.RequestID != "" || event.ResponseID != "" || event.UtteranceID != "" || event.TaskID != "" ||
		event.RunID != "" || event.ActionID != "" || event.AttemptID != "" || event.WorkstreamID != "" ||
		event.GoalID != "" || event.ArtifactID != "" || event.EvidenceID != "" || event.MemoryID != "" ||
		event.RelationID != "" || event.ScheduleID != "" || event.QueueItemID != "" || event.CheckpointID != "" ||
		event.ReceiptID != "" ||
		event.MessageID == accepted.InputMessageID || event.MessageID == "" || !validPayloadShape {
		return errors.New("stored board owner request response envelope is invalid")
	}
	text, textOK := event.Payload["text"].(string)
	content, contentOK := event.Payload["content"].(string)
	if !textOK || !contentOK || strings.TrimSpace(text) == "" || text != content ||
		strings.TrimSpace(text) == strings.TrimSpace(instruction) || event.Payload["kind"] != "reply" ||
		event.Payload["visibility"] != "public" ||
		event.Payload["source_actor_id"] != actorID || event.Payload["from"] != actorID || event.Payload["to"] != "external_peer" {
		return errors.New("stored board owner request response content is invalid")
	}
	if index.MessageID != event.MessageID || index.EventID != event.EventID || index.SessionID != accepted.SessionID ||
		index.TurnID != accepted.TurnID || index.FromActor != actorID || index.ToKind != "external_peer" ||
		index.ToActor != expected.Source.PeerPrincipalID || index.Kind != "board_owner_request" ||
		index.Visibility != "public" || index.ReplyToMessageID != "" {
		return errors.New("stored board owner request response index is invalid")
	}
	if !validBoardOwnerRequestInvocationReceipt(outcome.InvocationReceipt) {
		return errors.New("stored board owner request invocation receipt is invalid")
	}
	return nil
}

func sameBoardOwnerRequestOutcome(outcome BoardOwnerRequestOutcome, expected BoardOwnerRequestCommitInput) bool {
	if outcome.Lookup != expected.ExpectedLookup || outcome.MessageIndex != expected.MessageIndex ||
		outcome.InvocationReceipt != expected.InvocationReceipt || outcome.Target != expected.Target ||
		outcome.IdempotencyKey != expected.IdempotencyKey {
		return false
	}
	event := expected.Event
	event.EventSeq = outcome.Event.EventSeq
	return reflect.DeepEqual(outcome.Event, event)
}
