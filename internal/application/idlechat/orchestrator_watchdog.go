package idlechat

import (
	"fmt"
	"log"
	"strings"
	"time"
)

type WatchdogSnapshot struct {
	ChatActive      bool      `json:"chat_active"`
	ManualMode      bool      `json:"manual_mode"`
	Disabled        bool      `json:"disabled"`
	Mode            string    `json:"mode"`
	SessionID       string    `json:"session_id"`
	Generation      uint64    `json:"generation"`
	Stage           string    `json:"stage"`
	Detail          string    `json:"detail"`
	From            string    `json:"from,omitempty"`
	To              string    `json:"to,omitempty"`
	MessageID       string    `json:"message_id,omitempty"`
	TurnIndex       int       `json:"turn_index,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
	AgeSeconds      int64     `json:"age_seconds"`
	CurrentTopic    string    `json:"current_topic,omitempty"`
	NextTopicAt     time.Time `json:"next_topic_at,omitempty"`
	LastActivity    time.Time `json:"last_activity,omitempty"`
	ChatBusy        bool      `json:"chat_busy"`
	WorkerBusy      bool      `json:"worker_busy"`
	ExternalLLMBusy bool      `json:"external_llm_busy"`
	RecoveryReady   bool      `json:"recovery_ready"`
}

type WatchdogRecovery struct {
	Reason     string           `json:"reason"`
	Action     string           `json:"action"`
	Recovered  bool             `json:"recovered"`
	Before     WatchdogSnapshot `json:"before"`
	OccurredAt time.Time        `json:"occurred_at"`
}

func (o *IdleChatOrchestrator) markWatchdogStage(stage, detail string, ev TimelineEvent) {
	if o == nil {
		return
	}
	stage = strings.TrimSpace(stage)
	if stage == "" {
		return
	}
	detail = strings.TrimSpace(detail)
	o.mu.Lock()
	o.watchdogStage = stage
	o.watchdogDetail = detail
	o.watchdogFrom = strings.TrimSpace(ev.From)
	o.watchdogTo = strings.TrimSpace(ev.To)
	o.watchdogMessageID = strings.TrimSpace(ev.MessageID)
	o.watchdogTurnIndex = ev.TurnIndex
	o.watchdogUpdatedAt = time.Now().UTC()
	sessionID := o.activeSessionID
	generation := o.activeGeneration
	from := o.watchdogFrom
	to := o.watchdogTo
	messageID := o.watchdogMessageID
	turnIndex := o.watchdogTurnIndex
	o.mu.Unlock()
	log.Printf("[IdleChat] sequence stage=%s detail=%s session=%s from=%s to=%s message_id=%s turn=%d generation=%d",
		stage, detail, sessionID, from, to, messageID, turnIndex, generation)
}

func (o *IdleChatOrchestrator) WatchdogSnapshot(now time.Time) WatchdogSnapshot {
	if o == nil {
		return WatchdogSnapshot{}
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	o.mu.Lock()
	defer o.mu.Unlock()
	ageSeconds := int64(0)
	if !o.watchdogUpdatedAt.IsZero() {
		ageSeconds = int64(now.Sub(o.watchdogUpdatedAt).Seconds())
		if ageSeconds < 0 {
			ageSeconds = 0
		}
	}
	recoveryReady := o.chatActive && !o.watchdogUpdatedAt.IsZero()
	externalLLMBusy := false
	if o.externalLLMBusy != nil {
		externalLLMBusy = o.externalLLMBusy()
	}
	return WatchdogSnapshot{
		ChatActive:      o.chatActive,
		ManualMode:      o.manualMode,
		Disabled:        o.disabled,
		Mode:            o.sessionMode,
		SessionID:       o.activeSessionID,
		Generation:      o.activeGeneration,
		Stage:           o.watchdogStage,
		Detail:          o.watchdogDetail,
		From:            o.watchdogFrom,
		To:              o.watchdogTo,
		MessageID:       o.watchdogMessageID,
		TurnIndex:       o.watchdogTurnIndex,
		UpdatedAt:       o.watchdogUpdatedAt,
		AgeSeconds:      ageSeconds,
		CurrentTopic:    o.currentTopic,
		NextTopicAt:     o.nextTopicAt,
		LastActivity:    o.lastActivity,
		ChatBusy:        o.chatBusy,
		WorkerBusy:      o.workerBusy,
		ExternalLLMBusy: externalLLMBusy,
		RecoveryReady:   recoveryReady,
	}
}

func (o *IdleChatOrchestrator) RecoverIfStalled(now time.Time, threshold time.Duration, reason string) (WatchdogRecovery, bool) {
	if o == nil || threshold <= 0 {
		return WatchdogRecovery{}, false
	}
	snapshot := o.WatchdogSnapshot(now)
	if !snapshot.ChatActive || !snapshot.RecoveryReady {
		return WatchdogRecovery{}, false
	}
	if time.Duration(snapshot.AgeSeconds)*time.Second < threshold {
		return WatchdogRecovery{}, false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "idlechat_watchdog_stalled"
	}
	if snapshot.Stage != "" {
		reason = fmt.Sprintf("%s stage=%s", reason, snapshot.Stage)
	}
	if snapshot.Detail != "" {
		reason = fmt.Sprintf("%s detail=%s", reason, snapshot.Detail)
	}
	log.Printf("[IdleChat] watchdog recovery triggered: reason=%s session=%s stage=%s age=%ds generation=%d",
		reason, snapshot.SessionID, snapshot.Stage, snapshot.AgeSeconds, snapshot.Generation)
	o.Interrupt(reason)
	return WatchdogRecovery{
		Reason:     reason,
		Action:     "interrupt_idlechat_and_clear_active_state",
		Recovered:  true,
		Before:     snapshot,
		OccurredAt: now.UTC(),
	}, true
}

func idleWatchdogEventDetail(ev TimelineEvent) string {
	parts := make([]string, 0, 4)
	if ev.Type != "" {
		parts = append(parts, ev.Type)
	}
	if ev.From != "" || ev.To != "" {
		parts = append(parts, fmt.Sprintf("%s->%s", ev.From, ev.To))
	}
	if ev.MessageID != "" {
		parts = append(parts, ev.MessageID)
	}
	if ev.TurnIndex > 0 {
		parts = append(parts, fmt.Sprintf("turn=%d", ev.TurnIndex))
	}
	return strings.Join(parts, " ")
}
