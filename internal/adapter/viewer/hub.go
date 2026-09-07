package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	replayPageLimit = 1000
	replayTimeout   = 30 * time.Second
)

// EventStream is the shared subscription and replay contract used by Viewer
// and Chrome bridge consumers.
type EventStream interface {
	Subscribe() (chan []byte, []orchestrator.OrchestratorEvent)
	Unsubscribe(chan []byte)
	Replay(context.Context, modulecore.EventSeq, []orchestrator.OrchestratorEvent, func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error)
}

// EventHub broadcasts already-persisted orchestrator events to connected SSE
// clients. Canonical publication is owned by the runtime event relay.
type EventHub struct {
	mu                  sync.RWMutex
	clients             map[chan []byte]struct{}
	history             []orchestrator.OrchestratorEvent
	maxHist             int
	replayReader        EventReplayReader
	clientCountListener func(int)
}

// NewEventHub creates a new EventHub with the given history capacity.
func NewEventHub(maxHistory int) *EventHub {
	return &EventHub{
		clients: make(map[chan []byte]struct{}),
		maxHist: maxHistory,
	}
}

// OnEvent projects an event to the Viewer history and SSE clients.
func (h *EventHub) OnEvent(ev orchestrator.OrchestratorEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		log.Printf("[EventHub] OnEvent: marshal error: %v", err)
		return
	}

	h.mu.Lock()
	h.history = append(h.history, ev)
	if h.maxHist <= 0 {
		h.history = nil
	} else if len(h.history) > h.maxHist {
		h.history = h.history[len(h.history)-h.maxHist:]
	}
	clientDropped := false
	for ch := range h.clients {
		select {
		case ch <- data:
		default:
			delete(h.clients, ch)
			close(ch)
			clientDropped = true
		}
	}
	clientCount := len(h.clients)
	h.mu.Unlock()

	if clientDropped {
		log.Printf("[EventHub] disconnect: client too slow (remaining clients=%d)", clientCount)
		h.notifyClientCountChanged(clientCount)
	}
	log.Printf("[EventHub] OnEvent: eventType=%s from=%s to=%s clients=%d", ev.Type, ev.From, ev.To, clientCount)
}

// ClientCount returns the current number of connected SSE clients.
func (h *EventHub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// SetClientCountListener installs a callback invoked when the SSE client count changes.
func (h *EventHub) SetClientCountListener(listener func(int)) {
	h.mu.Lock()
	h.clientCountListener = listener
	h.mu.Unlock()
}

// SetReplayReader configures the canonical durable reader used for positive
// Last-Event-ID reconnects. A nil reader restores in-memory replay fallback.
func (h *EventHub) SetReplayReader(reader EventReplayReader) {
	h.mu.Lock()
	h.replayReader = reader
	h.mu.Unlock()
}

func (h *EventHub) notifyClientCountChanged(count int) {
	h.mu.RLock()
	listener := h.clientCountListener
	h.mu.RUnlock()
	if listener != nil {
		listener(count)
	}
}

// Subscribe registers a new SSE client and returns its event channel and a
// consistent history snapshot captured at registration time.
func (h *EventHub) Subscribe() (chan []byte, []orchestrator.OrchestratorEvent) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	clientCount := len(h.clients)
	history := make([]orchestrator.OrchestratorEvent, len(h.history))
	copy(history, h.history)
	h.mu.Unlock()
	log.Printf("[EventHub] Subscribe: new client connected (total clients=%d)", clientCount)
	h.notifyClientCountChanged(clientCount)
	return ch, history
}

// Unsubscribe removes a client and closes its channel.
func (h *EventHub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	if _, ok := h.clients[ch]; !ok {
		h.mu.Unlock()
		return
	}
	delete(h.clients, ch)
	clientCount := len(h.clients)
	close(ch)
	h.mu.Unlock()
	log.Printf("[EventHub] Unsubscribe: client disconnected (remaining clients=%d)", clientCount)
	h.notifyClientCountChanged(clientCount)
}

// History returns a copy of recent events.
func (h *EventHub) History() []orchestrator.OrchestratorEvent {
	h.mu.RLock()
	defer h.mu.RUnlock()

	result := make([]orchestrator.OrchestratorEvent, len(h.history))
	copy(result, h.history)
	return result
}

// Replay emits a bounded, ordered reconnect history and returns the last
// processed canonical EventSeq. A positive cursor uses the durable reader when
// configured; cursor zero intentionally preserves the existing in-memory
// initial snapshot behavior.
func (h *EventHub) Replay(ctx context.Context, after modulecore.EventSeq, snapshot []orchestrator.OrchestratorEvent, emit func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error) {
	if ctx == nil {
		return after, fmt.Errorf("event replay context is required")
	}
	if after < 0 {
		return after, fmt.Errorf("event replay cursor must not be negative")
	}
	if emit == nil {
		return after, fmt.Errorf("event replay emitter is required")
	}
	boundedContext, cancel := context.WithTimeout(ctx, replayTimeout)
	defer cancel()
	if after == 0 {
		return replaySnapshot(boundedContext, snapshot, after, emit, false)
	}
	h.mu.RLock()
	reader := h.replayReader
	h.mu.RUnlock()
	if reader != nil {
		return replayDurable(boundedContext, reader, after, emit)
	}
	return replaySnapshot(boundedContext, snapshot, after, emit, true)
}

func replayDurable(ctx context.Context, reader EventReplayReader, after modulecore.EventSeq, emit func(orchestrator.OrchestratorEvent) error) (modulecore.EventSeq, error) {
	cursor := after
	through := modulecore.EventSeq(0)
	for {
		if err := replayContextError(ctx); err != nil {
			return cursor, err
		}
		page, watermark, err := reader.ReplayPage(ctx, cursor, through, replayPageLimit)
		if err != nil {
			return cursor, err
		}
		if through == 0 {
			through = watermark
			if through < after {
				return cursor, fmt.Errorf("event replay watermark %d precedes cursor %d", through, after)
			}
		} else if watermark != through {
			return cursor, fmt.Errorf("event replay watermark changed from %d to %d", through, watermark)
		}
		if len(page) > replayPageLimit {
			return cursor, fmt.Errorf("event replay page exceeds limit %d", replayPageLimit)
		}
		if len(page) == 0 {
			if cursor < through {
				return cursor, fmt.Errorf("event replay ended before fixed watermark %d", through)
			}
			return cursor, nil
		}
		for index, event := range page {
			if event.EventSeq <= cursor {
				return cursor, fmt.Errorf("event replay page %d has non-advancing event_seq %d", index, event.EventSeq)
			}
			if event.EventSeq <= 0 || (through > 0 && event.EventSeq > through) {
				return cursor, fmt.Errorf("event replay page %d has event_seq %d outside fixed window", index, event.EventSeq)
			}
			if err := replayContextError(ctx); err != nil {
				return cursor, err
			}
			if err := emit(event); err != nil {
				return cursor, err
			}
			cursor = event.EventSeq
		}
		if cursor >= through {
			return cursor, nil
		}
	}
}

func replaySnapshot(ctx context.Context, snapshot []orchestrator.OrchestratorEvent, after modulecore.EventSeq, emit func(orchestrator.OrchestratorEvent) error, validateRetention bool) (modulecore.EventSeq, error) {
	if err := replayContextError(ctx); err != nil {
		return after, err
	}
	if !validateRetention {
		cursor := after
		seen := make(map[modulecore.EventSeq]struct{})
		for _, event := range snapshot {
			if err := replayContextError(ctx); err != nil {
				return cursor, err
			}
			if event.EventSeq > 0 {
				if _, exists := seen[event.EventSeq]; exists {
					continue
				}
				seen[event.EventSeq] = struct{}{}
			}
			if err := emit(event); err != nil {
				return cursor, err
			}
			if event.EventSeq > cursor {
				cursor = event.EventSeq
			}
		}
		return cursor, nil
	}

	ordered := make([]orchestrator.OrchestratorEvent, 0, len(snapshot))
	oldest := modulecore.EventSeq(0)
	newest := modulecore.EventSeq(0)
	for _, event := range snapshot {
		if event.EventSeq <= 0 {
			continue
		}
		ordered = append(ordered, event)
		if oldest == 0 || event.EventSeq < oldest {
			oldest = event.EventSeq
		}
		if event.EventSeq > newest {
			newest = event.EventSeq
		}
	}
	if oldest == 0 {
		return after, fmt.Errorf("event replay cursor %d cannot be served without retained history", after)
	}
	if after < oldest-1 {
		return after, fmt.Errorf("event replay cursor %d is older than retained history starting at %d", after, oldest)
	}
	if after > newest {
		return after, fmt.Errorf("event replay cursor %d is beyond retained history ending at %d", after, newest)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].EventSeq < ordered[j].EventSeq
	})
	cursor := after
	seen := make(map[modulecore.EventSeq]struct{}, len(ordered))
	for _, event := range ordered {
		if event.EventSeq <= after {
			continue
		}
		if _, exists := seen[event.EventSeq]; exists {
			continue
		}
		seen[event.EventSeq] = struct{}{}
		if event.EventSeq <= cursor {
			return cursor, fmt.Errorf("retained replay history is not strictly advancing at %d", event.EventSeq)
		}
		if err := replayContextError(ctx); err != nil {
			return cursor, err
		}
		if err := emit(event); err != nil {
			return cursor, err
		}
		cursor = event.EventSeq
	}
	return cursor, nil
}

func replayContextError(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// IsTransientReplayEvent identifies events that are live-only and must not be
// sent from durable or in-memory replay history.
func IsTransientReplayEvent(ev orchestrator.OrchestratorEvent) bool {
	switch ev.Type {
	case "tts.audio_chunk", "tts.session_completed", "idlechat.message", "idlechat.summary":
		return true
	case "investment.refresh", "investment.market", "investment.macro", "investment.features", "investment.events", "investment.snapshot":
		return true
	default:
		return false
	}
}
