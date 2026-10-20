package viewer

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

const (
	speechCancellationCapabilityBytes = 32
	speechCancellationMaxAttempts     = 8
	speechCancellationMaxBodyBytes    = 4096
)

type speechCancellationEntry struct {
	rootTaskID   string
	turnID       string
	cancellation *orchestrator.SpeechTurnCancellation
}

// SpeechCancellationRegistry binds one bearer capability to the speech-only
// cancellation latch of one accepted Viewer turn.
type SpeechCancellationRegistry struct {
	mu               sync.Mutex
	entries          map[string]*speechCancellationEntry
	progressByTurnID map[string]*speechCancellationEntry
	tokenSource      func() (string, error)
}

// NewSpeechCancellationRegistry creates an in-memory registry with
// cryptographically random capabilities.
func NewSpeechCancellationRegistry() *SpeechCancellationRegistry {
	return newSpeechCancellationRegistry(newSpeechCancellationCapability)
}

func newSpeechCancellationRegistry(tokenSource func() (string, error)) *SpeechCancellationRegistry {
	return &SpeechCancellationRegistry{
		entries:          make(map[string]*speechCancellationEntry),
		progressByTurnID: make(map[string]*speechCancellationEntry),
		tokenSource:      tokenSource,
	}
}

// Register stores the exact accepted turn before its asynchronous processor is
// launched. release removes only this registration, even if it is stale.
func (r *SpeechCancellationRegistry) Register(turnID string, cancellation *orchestrator.SpeechTurnCancellation) (token string, release func(), err error) {
	return r.register("", turnID, cancellation)
}

// RegisterTurn stores the accepted root task and turn identities for later
// playback-progress correlation as well as the cancellation capability.
func (r *SpeechCancellationRegistry) RegisterTurn(rootTaskID, turnID string, cancellation *orchestrator.SpeechTurnCancellation) (token string, release func(), err error) {
	if strings.TrimSpace(rootTaskID) == "" {
		return "", nil, errors.New("speech progress root task id is incomplete")
	}
	return r.register(rootTaskID, turnID, cancellation)
}

func (r *SpeechCancellationRegistry) register(rootTaskID, turnID string, cancellation *orchestrator.SpeechTurnCancellation) (token string, release func(), err error) {
	if r == nil {
		return "", nil, errors.New("speech cancellation registry is nil")
	}
	rootTaskID = strings.TrimSpace(rootTaskID)
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || cancellation == nil {
		return "", nil, errors.New("speech cancellation registration is incomplete")
	}
	if r.tokenSource == nil {
		return "", nil, errors.New("speech cancellation capability source is unavailable")
	}
	for attempt := 0; attempt < speechCancellationMaxAttempts; attempt++ {
		candidate, sourceErr := r.tokenSource()
		if sourceErr != nil {
			return "", nil, fmt.Errorf("generate speech cancellation capability: %w", sourceErr)
		}
		if len(candidate) < 22 || len(candidate) > 128 {
			return "", nil, errors.New("generated speech cancellation capability has invalid length")
		}
		entry := &speechCancellationEntry{
			rootTaskID:   rootTaskID,
			turnID:       turnID,
			cancellation: cancellation,
		}
		r.mu.Lock()
		if _, exists := r.entries[candidate]; exists {
			r.mu.Unlock()
			continue
		}
		if rootTaskID != "" {
			if _, exists := r.progressByTurnID[turnID]; exists {
				r.mu.Unlock()
				return "", nil, errors.New("speech progress turn is already registered")
			}
			r.progressByTurnID[turnID] = entry
		}
		r.entries[candidate] = entry
		r.mu.Unlock()

		var once sync.Once
		release = func() {
			once.Do(func() {
				r.mu.Lock()
				if current := r.entries[candidate]; current == entry {
					delete(r.entries, candidate)
				}
				if current := r.progressByTurnID[turnID]; current == entry {
					delete(r.progressByTurnID, turnID)
				}
				r.mu.Unlock()
			})
		}
		return candidate, release, nil
	}
	return "", nil, errors.New("could not allocate unique speech cancellation capability")
}

// HandleCancel creates the guarded Viewer route handler. Valid capabilities
// receive the same response whether active, repeated, or already completed.
func (r *SpeechCancellationRegistry) HandleCancel() http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if req.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if r == nil {
			http.Error(w, "speech cancellation unavailable", http.StatusServiceUnavailable)
			return
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, req.Body, speechCancellationMaxBodyBytes))
		decoder.DisallowUnknownFields()
		var body struct {
			SpeechCancelToken string `json:"speech_cancel_token"`
		}
		if err := decoder.Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		token := strings.TrimSpace(body.SpeechCancelToken)
		if token == "" || len(token) > 128 {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		r.cancelToken(token)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (r *SpeechCancellationRegistry) cancelToken(token string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	entry := r.entries[token]
	r.mu.Unlock()
	if entry == nil || entry.cancellation == nil {
		return false
	}
	entry.cancellation.Cancel()
	return true
}

func newSpeechCancellationCapability() (string, error) {
	value := make([]byte, speechCancellationCapabilityBytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}
