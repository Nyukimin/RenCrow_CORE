package conversationruntime

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type externalPeerInput struct {
	binding  domainconversation.ExternalPeerBinding
	message  modulecore.MessageID
	eventID  modulecore.EventID
	traceID  modulecore.TraceID
	turnID   modulecore.TurnID
	eventSeq modulecore.EventSeq
	text     string
}

func validateAcceptedExternalPeerInput(request ReadViewRequest, event modulecore.EventEnvelope) error {
	accepted := request.Accepted
	binding, err := domainconversation.ParseExternalPeerBinding(event.Payload)
	if err != nil {
		return fmt.Errorf("%w: accepted external peer binding is invalid", ErrReadViewInvalid)
	}
	if err := validateExternalPeerBindingSource(binding, event.Payload); err != nil {
		return fmt.Errorf("%w: accepted external peer source is not a validated request or browser followup", ErrReadViewInvalid)
	}
	sourceActorID, err := binding.SourceActorID()
	if err != nil || sourceActorID != accepted.ActorID || sourceActorID != request.Principal.ActorID {
		return fmt.Errorf("%w: accepted external peer source actor does not match its binding", ErrReadViewInvalid)
	}
	responderActorID := binding.EffectiveResponderActorID()
	if event.EventType != "conversation.external_peer_message" || event.ComponentID != "orchestrator" ||
		event.ActorKind != "" || event.ActorID != "" || event.CausationEventID != "" || len(event.DependencyEventIDs) != 0 ||
		event.TaskID != "" || event.RunID != "" || event.ActionID != "" || event.AttemptID != "" ||
		event.WorkstreamID != "" || event.GoalID != "" || event.UtteranceID != "" || event.RequestID != "" ||
		event.ResponseID != "" || event.ArtifactID != "" || event.EvidenceID != "" || event.MemoryID != "" ||
		event.RelationID != "" || event.ScheduleID != "" || event.QueueItemID != "" || event.CheckpointID != "" || event.ReceiptID != "" {
		return fmt.Errorf("%w: accepted external peer event carries an unsupported actor, cause, or auxiliary identity", ErrReadViewInvalid)
	}
	if payloadString(event.Payload, "from") != "external_peer" ||
		payloadString(event.Payload, "source_actor_id") != sourceActorID ||
		payloadString(event.Payload, "to") != responderActorID || payloadString(event.Payload, "visibility") != "public" ||
		payloadString(event.Payload, "reply_to_message_id") != "" {
		return fmt.Errorf("%w: accepted external peer provenance metadata is invalid", ErrReadViewInvalid)
	}
	if value, exists := event.Payload["reply_to_message_id"]; exists {
		replyTo, ok := value.(string)
		if !ok || replyTo != "" {
			return fmt.Errorf("%w: uncorrelated external peer request cannot carry a reply target", ErrReadViewInvalid)
		}
	}
	if err := validateExternalPeerBody(event.Payload); err != nil {
		return fmt.Errorf("%w: accepted external peer body is invalid", ErrReadViewInvalid)
	}
	return nil
}

func validateExternalPeerBody(payload map[string]any) error {
	text, textOK := payload["text"].(string)
	content, contentOK := payload["content"].(string)
	body, bodyOK := payload["body"].(map[string]any)
	bodyText, bodyTextOK := body["text"].(string)
	if !textOK || !contentOK || !bodyOK || !bodyTextOK || strings.TrimSpace(text) == "" ||
		text != content || text != bodyText {
		return errors.New("text, content, and raw body text must be present and equal")
	}
	return nil
}

func validateExternalPeerBindingSource(binding domainconversation.ExternalPeerBinding, payload map[string]any) error {
	switch binding.Classification {
	case domainconversation.ExternalPeerClassificationRequest:
		if err := binding.ValidateNewRequest(); err != nil {
			return err
		}
		causationID, hopCount, found, err := externalBoardSourceCausalChain(payload)
		if err != nil {
			return err
		}
		if found && (causationID != binding.MessageID || hopCount != 0) {
			return errors.New("root BOARD source causal chain is inconsistent")
		}
		return nil
	case domainconversation.ExternalPeerClassificationBrowserReplyFollowup:
		if err := binding.ValidateBrowserReplyFollowup(); err != nil {
			return err
		}
		causationID, hopCount, found, err := externalBoardSourceCausalChain(payload)
		if err != nil {
			return err
		}
		if !found || causationID == "" || hopCount < 1 || hopCount >= domainconversation.BoardCommunicationMaxHopCount {
			return errors.New("browser followup BOARD causal chain is not replyable")
		}
		return nil
	default:
		return errors.New("external peer classification cannot start a conversation turn")
	}
}

func buildExternalPeerReadView(ctx context.Context, request ReadViewRequest, tail CanonicalTail, tailAfter modulecore.EventSeq) (ReadView, error) {
	accepted := request.Accepted
	currentBinding, err := domainconversation.ParseExternalPeerBinding(accepted.InputEvent.Payload)
	if err != nil {
		return ReadView{}, fmt.Errorf("%w: accepted external peer binding is invalid", ErrReadViewInvalid)
	}
	if err := validateExternalPeerBindingSource(currentBinding, accepted.InputEvent.Payload); err != nil {
		return ReadView{}, fmt.Errorf("%w: accepted external peer source is invalid", ErrReadViewInvalid)
	}

	requestsByBinding := make(map[domainconversation.ExternalPeerBinding]externalPeerInput)
	requestsByMessage := make(map[modulecore.MessageID]externalPeerInput)
	var currentInputFound bool
	for _, message := range tail.Messages {
		if err := validateExternalTailStructure(message, request, tailAfter, tail.ThroughSeq); err != nil {
			return ReadView{}, err
		}
		event, index := message.Event, message.Index
		if event.EventSeq == accepted.AdmissionSeq && index.MessageID == accepted.InputMessageID {
			if currentInputFound || event.EventID != accepted.InputEvent.EventID || !reflect.DeepEqual(event, accepted.InputEvent) {
				return ReadView{}, fmt.Errorf("%w: accepted external input differs from its canonical tail row", ErrReadViewInvalid)
			}
			if err := validateExternalPeerRequestMessage(message, currentBinding); err != nil {
				return ReadView{}, err
			}
			currentInputFound = true
		}
		if event.EventSeq >= accepted.AdmissionSeq || event.EventType != "conversation.external_peer_message" {
			continue
		}
		binding, err := domainconversation.ParseExternalPeerBinding(event.Payload)
		if err != nil {
			return ReadView{}, fmt.Errorf("%w: canonical external peer request binding is invalid", ErrReadViewInvalid)
		}
		if !binding.SameContext(currentBinding) {
			continue
		}
		if err := validateExternalPeerRequestMessage(message, binding); err != nil {
			return ReadView{}, err
		}
		text, err := inputText(event)
		if err != nil {
			return ReadView{}, fmt.Errorf("%w: canonical external peer request text is invalid", ErrReadViewInvalid)
		}
		input := externalPeerInput{
			binding: binding, message: index.MessageID, eventID: event.EventID, traceID: event.TraceID,
			turnID: event.TurnID, eventSeq: event.EventSeq, text: text,
		}
		if _, found := requestsByMessage[index.MessageID]; found {
			return ReadView{}, fmt.Errorf("%w: canonical external peer message appears more than once", ErrReadViewInvalid)
		}
		if _, found := requestsByBinding[binding]; found {
			return ReadView{}, fmt.Errorf("%w: canonical external source binding appears more than once", ErrReadViewInvalid)
		}
		requestsByBinding[binding] = input
		requestsByMessage[index.MessageID] = input
	}
	if !currentInputFound {
		return ReadView{}, fmt.Errorf("%w: accepted external input is missing from the canonical tail", ErrReadViewInvalid)
	}

	messages := make([]PromptMessage, 0, len(requestsByBinding)*2+1)
	for _, message := range tail.Messages {
		if err := ctx.Err(); err != nil {
			return ReadView{}, err
		}
		event, index := message.Event, message.Index
		if event.EventSeq >= accepted.AdmissionSeq || event.EventType != "agent.response" || event.ComponentID != "conversation_runtime" {
			continue
		}
		if payloadString(event.Payload, "to") != "external_peer" {
			continue
		}
		binding, err := domainconversation.ParseExternalPeerBinding(event.Payload)
		if err != nil {
			return ReadView{}, fmt.Errorf("%w: canonical external response binding is invalid", ErrReadViewInvalid)
		}
		if !binding.SameContext(currentBinding) {
			continue
		}
		source, found := requestsByBinding[binding]
		if !found || index.ReplyToMessageID != source.message || payloadString(event.Payload, "reply_to_message_id") != string(source.message) {
			continue
		}
		if err := validateExternalPeerResponseMessage(message, binding, source); err != nil {
			return ReadView{}, err
		}
		text, err := inputText(event)
		if err != nil {
			return ReadView{}, fmt.Errorf("%w: canonical external response text is invalid", ErrReadViewInvalid)
		}
		messages = append(messages, PromptMessage{
			Role: "assistant", Text: text, MessageID: string(index.MessageID), TurnID: index.TurnID, EventSeq: event.EventSeq,
		})
	}
	for _, message := range tail.Messages {
		event, index := message.Event, message.Index
		if event.EventSeq >= accepted.AdmissionSeq || event.EventType != "conversation.external_peer_message" {
			continue
		}
		binding, err := domainconversation.ParseExternalPeerBinding(event.Payload)
		if err != nil || !binding.SameContext(currentBinding) {
			continue
		}
		if _, found := requestsByMessage[index.MessageID]; found {
			input := requestsByMessage[index.MessageID]
			messages = append(messages, PromptMessage{
				Role: "user", Text: input.text, MessageID: string(index.MessageID), TurnID: index.TurnID, EventSeq: event.EventSeq,
			})
		}
	}
	sort.SliceStable(messages, func(i, j int) bool {
		if messages[i].EventSeq != messages[j].EventSeq {
			return messages[i].EventSeq < messages[j].EventSeq
		}
		if messages[i].Role != messages[j].Role {
			return messages[i].Role == "user"
		}
		return messages[i].MessageID < messages[j].MessageID
	})
	text, err := inputText(accepted.InputEvent)
	if err != nil {
		return ReadView{}, fmt.Errorf("%w: accepted external peer body is invalid", ErrReadViewInvalid)
	}
	messages = append(messages, PromptMessage{
		Role: "user", Text: text, MessageID: string(accepted.InputMessageID),
		TurnID: accepted.TurnID, EventSeq: accepted.AdmissionSeq,
	})
	return ReadView{
		SessionID: accepted.SessionID, RuntimeVersion: request.Session.RuntimeVersion,
		CutoverSeq: request.Session.CutoverSeq, ProjectionWatermark: request.Session.ProjectionWatermark,
		ThroughSeq: tail.ThroughSeq, Messages: messages,
	}, nil
}

func validateExternalTailStructure(message CanonicalMessage, request ReadViewRequest, afterSeq, throughSeq modulecore.EventSeq) error {
	event, index := message.Event, message.Index
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return fmt.Errorf("%w: canonical event is invalid", ErrReadViewInvalid)
	}
	if event.SessionID != request.Session.SessionID || event.EventSeq <= afterSeq || event.EventSeq > throughSeq ||
		index.SessionID != request.Session.SessionID || index.EventID != event.EventID || index.MessageID != event.MessageID ||
		index.TurnID != event.TurnID || !canonicalSourceMatches(event, index.FromActor) || index.Kind == "" || index.FromActor == "" ||
		index.ToKind == "" || index.ToActor == "" || (index.Visibility != "public" && index.Visibility != "private") {
		return fmt.Errorf("%w: canonical message index does not match its event", ErrReadViewInvalid)
	}
	if event.ActorID == "" && index.Visibility == "public" && payloadString(event.Payload, "visibility") != "public" {
		return fmt.Errorf("%w: canonical public message visibility is inconsistent", ErrReadViewInvalid)
	}
	return nil
}

func validateExternalPeerRequestMessage(message CanonicalMessage, binding domainconversation.ExternalPeerBinding) error {
	event, index := message.Event, message.Index
	if err := validateExternalPeerBindingSource(binding, event.Payload); err != nil {
		return fmt.Errorf("%w: canonical external peer input classification is invalid", ErrReadViewInvalid)
	}
	sourceActorID, err := binding.SourceActorID()
	if err != nil || event.EventType != "conversation.external_peer_message" || event.ComponentID != "orchestrator" ||
		event.ActorKind != "" || event.ActorID != "" || event.CausationEventID != "" || len(event.DependencyEventIDs) != 0 ||
		index.FromActor != sourceActorID || index.ToKind != binding.ResponderActorKind() || index.ToActor != binding.EffectiveResponderActorID() ||
		index.Kind != "external_peer_message" || index.Visibility != "public" || index.ReplyToMessageID != "" ||
		payloadString(event.Payload, "from") != "external_peer" || payloadString(event.Payload, "source_actor_id") != sourceActorID ||
		payloadString(event.Payload, "to") != binding.EffectiveResponderActorID() || payloadString(event.Payload, "visibility") != "public" ||
		payloadString(event.Payload, "reply_to_message_id") != "" {
		return fmt.Errorf("%w: canonical external peer request metadata is invalid", ErrReadViewInvalid)
	}
	if value, exists := event.Payload["reply_to_message_id"]; exists {
		replyTo, ok := value.(string)
		if !ok || replyTo != "" {
			return fmt.Errorf("%w: uncorrelated external peer request carries a reply target", ErrReadViewInvalid)
		}
	}
	if err := validateExternalPeerBody(event.Payload); err != nil {
		return fmt.Errorf("%w: canonical external peer request body is invalid", ErrReadViewInvalid)
	}
	stored, err := domainconversation.ParseExternalPeerBinding(event.Payload)
	if err != nil || !stored.Equal(binding) {
		return fmt.Errorf("%w: canonical external peer request binding differs from its source", ErrReadViewInvalid)
	}
	return nil
}

func validateExternalPeerResponseMessage(message CanonicalMessage, binding domainconversation.ExternalPeerBinding, source externalPeerInput) error {
	event, index := message.Event, message.Index
	kind, indexKind := payloadString(event.Payload, "kind"), "assistant_message"
	responderActorID := binding.EffectiveResponderActorID()
	expectedActorKind, expectedActorID := binding.ResponderActorKind(), responderActorID
	if kind == "turn_error" {
		indexKind, expectedActorKind, expectedActorID = "turn_error", "", ""
	} else if kind != "reply" {
		return fmt.Errorf("%w: canonical external response kind is invalid", ErrReadViewInvalid)
	}
	if event.EventType != "agent.response" || event.ComponentID != "conversation_runtime" || event.ActorKind != expectedActorKind || event.ActorID != expectedActorID ||
		event.CausationEventID != source.eventID || event.TraceID != source.traceID || event.TurnID != source.turnID || event.EventSeq <= source.eventSeq ||
		index.FromActor != responderActorID || index.ToKind != "external_peer" || index.ToActor != binding.SenderPrincipalID ||
		index.Kind != indexKind || index.Visibility != "public" || index.ReplyToMessageID != source.message ||
		payloadString(event.Payload, "from") != responderActorID || payloadString(event.Payload, "source_actor_id") != responderActorID ||
		payloadString(event.Payload, "to") != "external_peer" || payloadString(event.Payload, "visibility") != "public" ||
		payloadString(event.Payload, "reply_to_message_id") != string(source.message) {
		return fmt.Errorf("%w: canonical external response scope or correlation is invalid", ErrReadViewInvalid)
	}
	stored, err := domainconversation.ParseExternalPeerBinding(event.Payload)
	if err != nil || !stored.Equal(binding) {
		return fmt.Errorf("%w: canonical external response source binding differs from its request", ErrReadViewInvalid)
	}
	if err := inputTextIsExact(event.Payload); err != nil {
		return fmt.Errorf("%w: canonical external response body is invalid", ErrReadViewInvalid)
	}
	return nil
}

func inputTextIsExact(payload map[string]any) error {
	text, textOK := payload["text"].(string)
	content, contentOK := payload["content"].(string)
	if !textOK || !contentOK || strings.TrimSpace(text) == "" || text != content {
		return errors.New("response text and content must be present, equal, and nonempty")
	}
	return nil
}
