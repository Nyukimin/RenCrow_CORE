package audiorouter

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

type Event struct {
	SessionID   string `json:"session_id"`
	ChunkIndex  int    `json:"chunk_index"`
	CharacterID string `json:"character_id"`
	Text        string `json:"text,omitempty"`
	AudioPath   string `json:"audio_path,omitempty"`
	AudioURL    string `json:"audio_url,omitempty"`
}

type Device struct {
	ID   string
	Name string
}

type Player interface {
	ListDevices(ctx context.Context) ([]Device, error)
	PlayWAV(ctx context.Context, deviceID string, wavData []byte, buffer time.Duration) error
}

type Downloader interface {
	Download(ctx context.Context, rawURL string) ([]byte, error)
}

type RouterConfig struct {
	DeviceMap       map[string]string
	Buffer          time.Duration
	QueueDepth      int
	DownloadTimeout time.Duration
}

type CharacterStatus struct {
	DeviceID     string    `json:"device_id"`
	QueueDepth   int       `json:"queue_depth"`
	LastEventKey string    `json:"last_event_key,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	LastPlayedAt time.Time `json:"last_played_at,omitempty"`
}

type Status struct {
	Connected   bool                       `json:"connected"`
	LastEventID int64                      `json:"last_event_id"`
	Characters  map[string]CharacterStatus `json:"characters"`
}

type Router struct {
	cfg        RouterConfig
	player     Player
	downloader Downloader

	mu         sync.RWMutex
	connected  bool
	lastEvent  int64
	characters map[string]*characterWorker
	seen       map[string]time.Time
}

type characterWorker struct {
	id       string
	deviceID string
	queue    chan Event

	mu           sync.RWMutex
	lastEventKey string
	lastError    string
	lastPlayedAt time.Time
}

func NewRouter(cfg RouterConfig, player Player, downloader Downloader) *Router {
	if cfg.QueueDepth <= 0 {
		cfg.QueueDepth = 8
	}
	if cfg.Buffer <= 0 {
		cfg.Buffer = 120 * time.Millisecond
	}
	return &Router{
		cfg:        cfg,
		player:     player,
		downloader: downloader,
		characters: make(map[string]*characterWorker),
		seen:       make(map[string]time.Time),
	}
}

func (r *Router) Start(ctx context.Context) {
	for characterID, deviceID := range r.cfg.DeviceMap {
		w := &characterWorker{
			id:       characterID,
			deviceID: deviceID,
			queue:    make(chan Event, r.cfg.QueueDepth),
		}
		r.characters[characterID] = w
		go r.runCharacterWorker(ctx, w)
	}
}

func (r *Router) SetConnected(connected bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.connected = connected
}

func (r *Router) UpdateLastEventID(id int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id > r.lastEvent {
		r.lastEvent = id
	}
}

func (r *Router) Enqueue(ev Event) error {
	if ev.SessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if ev.CharacterID == "" {
		return fmt.Errorf("character_id is required")
	}
	w, ok := r.characters[ev.CharacterID]
	if !ok {
		return fmt.Errorf("unknown character_id: %s", ev.CharacterID)
	}
	key := fmt.Sprintf("%s:%d", ev.SessionID, ev.ChunkIndex)
	if r.markSeen(key) {
		return nil
	}
	select {
	case w.queue <- ev:
		return nil
	default:
		return fmt.Errorf("character queue full: %s", ev.CharacterID)
	}
}

func (r *Router) Status() Status {
	r.mu.RLock()
	status := Status{
		Connected:   r.connected,
		LastEventID: r.lastEvent,
		Characters:  make(map[string]CharacterStatus, len(r.characters)),
	}
	r.mu.RUnlock()

	for id, w := range r.characters {
		w.mu.RLock()
		status.Characters[id] = CharacterStatus{
			DeviceID:     w.deviceID,
			QueueDepth:   len(w.queue),
			LastEventKey: w.lastEventKey,
			LastError:    w.lastError,
			LastPlayedAt: w.lastPlayedAt,
		}
		w.mu.RUnlock()
	}
	return status
}

func (r *Router) ListDevices(ctx context.Context) ([]Device, error) {
	return r.player.ListDevices(ctx)
}

func (r *Router) runCharacterWorker(ctx context.Context, w *characterWorker) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-w.queue:
			if err := r.playEvent(ctx, w, ev); err != nil {
				w.mu.Lock()
				w.lastEventKey = fmt.Sprintf("%s:%d", ev.SessionID, ev.ChunkIndex)
				w.lastError = err.Error()
				w.mu.Unlock()
				log.Printf("audio_router_play_error character=%s session=%s chunk=%d err=%v", ev.CharacterID, ev.SessionID, ev.ChunkIndex, err)
			}
		}
	}
}

func (r *Router) playEvent(ctx context.Context, w *characterWorker, ev Event) error {
	if ev.AudioURL == "" {
		return fmt.Errorf("audio_url is required")
	}
	downloadCtx, cancel := context.WithTimeout(ctx, r.cfg.DownloadTimeout)
	defer cancel()
	data, err := r.downloader.Download(downloadCtx, ev.AudioURL)
	if err != nil {
		return fmt.Errorf("download audio: %w", err)
	}
	if err := r.player.PlayWAV(ctx, w.deviceID, data, r.cfg.Buffer); err != nil {
		return fmt.Errorf("play wav: %w", err)
	}
	w.mu.Lock()
	w.lastEventKey = fmt.Sprintf("%s:%d", ev.SessionID, ev.ChunkIndex)
	w.lastError = ""
	w.lastPlayedAt = time.Now().UTC()
	w.mu.Unlock()
	return nil
}

func (r *Router) markSeen(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	for k, ts := range r.seen {
		if now.Sub(ts) > 10*time.Minute {
			delete(r.seen, k)
		}
	}
	if _, exists := r.seen[key]; exists {
		return true
	}
	r.seen[key] = now
	return false
}
