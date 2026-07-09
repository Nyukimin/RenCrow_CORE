package orchestrator

import (
	"sync"

	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
)

type distributedEventPort struct {
	listener EventListener
	mu       sync.Mutex
	turns    map[string]int
}

func newDistributedEventPort(listener EventListener) *distributedEventPort {
	return &distributedEventPort{listener: listener, turns: map[string]int{}}
}

func (p *distributedEventPort) SetListener(listener EventListener) {
	p.listener = listener
}

func (p *distributedEventPort) Emit(eventType, from, to, content, route, jobID, sessionID, channel, chatID string) {
	if p.listener == nil {
		return
	}
	ev := NewEvent(eventType, from, to, content, route, jobID, sessionID, channel, chatID)
	p.assignConversationIdentity(&ev)
	p.listener.OnEvent(ev)
}

func (p *distributedEventPort) EmitNote(from, to, content, route, jobID, sessionID, channel, chatID string) {
	p.Emit("agent.note", from, to, content, route, jobID, sessionID, channel, chatID)
}

func (p *distributedEventPort) EmitProgress(eventType, from, to, content string, msg domaintransport.Message) {
	route, channel, chatID := routeAndChannelFromMessage(msg)
	p.Emit(eventType, from, to, content, route, msg.JobID, msg.SessionID, channel, chatID)
}

func (p *distributedEventPort) assignConversationIdentity(ev *OrchestratorEvent) {
	if ev == nil || !isConversationMessageEvent(ev.Type) {
		return
	}
	sessionID := conversationIdentitySession(ev.SessionID, ev.ChatID)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns[sessionID]++
	ev.TurnIndex = p.turns[sessionID]
	ev.MessageID = conversationMessageID(sessionID, ev.TurnIndex)
}
