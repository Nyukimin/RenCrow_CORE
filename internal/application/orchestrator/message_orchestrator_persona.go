package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainpersona "github.com/Nyukimin/RenCrow_CORE/internal/domain/persona"
)

func (o *MessageOrchestrator) recordPersonaRuntimeObservation(ctx context.Context, req ProcessMessageRequest) error {
	if o == nil || o.personaRuntime == nil {
		return nil
	}
	now := time.Now().UTC()
	sessionKey := personaSessionKey(req)
	sessionID := "persona_session:" + sessionKey
	if err := o.personaRuntime.SaveInterfaceSession(ctx, domainpersona.InterfaceSession{
		SessionID:     sessionID,
		CharacterID:   "mio",
		InterfaceType: personaInterfaceType(req.Channel),
		SessionKey:    sessionKey,
		CreatedAt:     now,
		LastUsedAt:    now,
	}); err != nil {
		return fmt.Errorf("record persona interface session: %w", err)
	}
	evidenceRefs := []string{"session:" + strings.TrimSpace(req.SessionID)}
	if strings.TrimSpace(req.Channel) != "" {
		evidenceRefs = append(evidenceRefs, "channel:"+strings.TrimSpace(req.Channel))
	}
	if strings.TrimSpace(req.ChatID) != "" {
		evidenceRefs = append(evidenceRefs, "chat:"+strings.TrimSpace(req.ChatID))
	}
	if err := o.personaRuntime.SaveObservationLog(ctx, domainpersona.ObservationLog{
		EventID:         fmt.Sprintf("evt_persona_chat_observation_%d", now.UnixNano()),
		ObserverID:      "mio",
		TargetID:        "ren",
		ObservationType: "chat_message",
		Summary:         "Chat runtime observed a user message; review is required before memory promotion.",
		EvidenceRefs:    evidenceRefs,
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}); err != nil {
		return fmt.Errorf("record persona observation: %w", err)
	}
	if candidate, ok := buildChatMetaProfileUpdateCandidate(req, now, evidenceRefs); ok {
		if err := o.personaRuntime.SaveMetaProfileUpdate(ctx, candidate); err != nil {
			return fmt.Errorf("record persona meta profile update candidate: %w", err)
		}
	}
	if match, ok := domainpersona.MatchTrigger(req.UserMessage, o.personaTriggers); ok {
		if err := o.personaRuntime.SaveTriggerLog(ctx, domainpersona.TriggerLog{
			EventID:         fmt.Sprintf("evt_persona_trigger_%d", now.UnixNano()),
			CharacterID:     match.CharacterID,
			TriggerID:       match.TriggerID,
			TriggerCategory: match.Category,
			Activated:       true,
			Confidence:      match.Confidence,
			CreatedAt:       now,
		}); err != nil {
			return fmt.Errorf("record persona trigger: %w", err)
		}
	}
	return nil
}

func buildChatMetaProfileUpdateCandidate(req ProcessMessageRequest, now time.Time, evidenceRefs []string) (domainpersona.MetaProfileUpdate, bool) {
	message := strings.TrimSpace(req.UserMessage)
	if !shouldProposePersonaMetaUpdateFromText(message) {
		return domainpersona.MetaProfileUpdate{}, false
	}
	return domainpersona.MetaProfileUpdate{
		UpdateID:        fmt.Sprintf("meta_persona_chat_%d", now.UnixNano()),
		ObserverID:      "mio",
		TargetID:        "ren",
		Section:         "flow_observation",
		ProposedContent: "Runtime candidate from Chat user message. Human review is required before treating this as stable memory.\n\n" + message,
		EvidenceRefs:    append([]string(nil), evidenceRefs...),
		Sensitivity:     "normal",
		ReviewStatus:    "pending",
		CreatedAt:       now,
	}, true
}

func shouldProposePersonaMetaUpdateFromText(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	markers := []string{"私は", "私の", "自分は", "自分の", "覚えて", "覚えといて", "記憶して"}
	for _, marker := range markers {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (o *MessageOrchestrator) applyPersonaCanonicalResponse(ctx context.Context, req ProcessMessageRequest, currentResponse string) (string, error) {
	if o == nil || o.personaRuntime == nil || strings.TrimSpace(currentResponse) == "" || len(o.personaCanonicalResponses) == 0 {
		return "", nil
	}
	match, ok := domainpersona.MatchTrigger(req.UserMessage, o.personaTriggers)
	if !ok {
		return "", nil
	}
	def, ok := o.selectPersonaCanonicalResponse(match)
	if !ok || strings.TrimSpace(def.Response) == "" {
		return "", nil
	}
	recent, err := o.personaRuntime.ListCanonicalResponseLogs(ctx, 50)
	if err != nil {
		return "", fmt.Errorf("list persona canonical response logs: %w", err)
	}
	contexts := personaCanonicalContexts(req, match)
	policy := domainpersona.CanonicalResponsePolicy{
		ResponseID:       def.ResponseID,
		CooldownTurns:    def.CooldownTurns,
		MaxPerSession:    def.MaxPerSession,
		RequiredContexts: def.RequiredContexts,
	}
	if !domainpersona.CanUseCanonicalResponse(policy, recent, contexts) {
		return "", nil
	}
	now := time.Now().UTC()
	if err := o.personaRuntime.SaveCanonicalResponseLog(ctx, domainpersona.CanonicalResponseLog{
		EventID:     fmt.Sprintf("evt_persona_canonical_%d", now.UnixNano()),
		CharacterID: def.CharacterID,
		ResponseID:  def.ResponseID,
		MessageID:   strings.TrimSpace(req.SessionID),
		Used:        true,
		Rewritten:   false,
		CreatedAt:   now,
	}); err != nil {
		return "", fmt.Errorf("record persona canonical response: %w", err)
	}
	return def.Response, nil
}

func (o *MessageOrchestrator) selectPersonaCanonicalResponse(match domainpersona.TriggerMatch) (domainpersona.CanonicalResponseDefinition, bool) {
	var selected domainpersona.CanonicalResponseDefinition
	found := false
	for _, def := range o.personaCanonicalResponses {
		if strings.TrimSpace(def.ResponseID) == "" || strings.TrimSpace(def.CharacterID) == "" {
			continue
		}
		if def.CharacterID != match.CharacterID {
			continue
		}
		if strings.TrimSpace(def.Category) != "" && def.Category != match.Category {
			continue
		}
		if !found || def.Priority > selected.Priority {
			selected = def
			found = true
		}
	}
	return selected, found
}

func personaCanonicalContexts(req ProcessMessageRequest, match domainpersona.TriggerMatch) []string {
	contexts := []string{}
	if strings.TrimSpace(match.Category) != "" {
		contexts = append(contexts, strings.TrimSpace(match.Category))
	}
	if strings.TrimSpace(match.TriggerID) != "" {
		contexts = append(contexts, strings.TrimSpace(match.TriggerID))
	}
	if strings.TrimSpace(req.Channel) != "" {
		contexts = append(contexts, strings.TrimSpace(req.Channel))
	}
	return contexts
}

func personaSessionKey(req ProcessMessageRequest) string {
	channel := strings.TrimSpace(req.Channel)
	chatID := strings.TrimSpace(req.ChatID)
	sessionID := strings.TrimSpace(req.SessionID)
	if channel != "" && chatID != "" {
		return channel + ":" + chatID
	}
	if channel != "" && sessionID != "" {
		return channel + ":" + sessionID
	}
	if sessionID != "" {
		return "chat:" + sessionID
	}
	return "chat:unknown"
}

func personaInterfaceType(channel string) string {
	channel = strings.TrimSpace(strings.ToLower(channel))
	switch channel {
	case "line", "slack", "discord", "telegram":
		return channel
	case "":
		return "chat"
	default:
		return channel
	}
}
