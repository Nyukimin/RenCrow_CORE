// Package worldread is CORE's authenticated read client for the WORLD R07
// Snapshot and Event routes.
//
// It deliberately owns no production HTTP route. CORE's PORTAL-facing read
// bridge route is still an open CORE/WORLD contract, so this package stops at
// the owner boundary: it reads one exact WORLD scope, validates the response
// contract, advances a cursor without partial pages, and hides Actors outside
// CORE's explicit view scope.
package worldread

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultTimeout bounds one Snapshot or Event page request.
	DefaultTimeout = 5 * time.Second

	// DefaultMaxEvents is WORLD's documented maximum page size.
	DefaultMaxEvents = 500

	// maxJSONSequence is the largest integer a WORLD public JSON sequence may
	// carry without losing precision in a JavaScript client.
	maxJSONSequence = uint64(9007199254740991)

	// maxResponseBytes bounds one Snapshot or Event page response.
	maxResponseBytes = 8 << 20

	// maxFeedPages bounds one catch-up read. A larger backlog requires a fresh
	// Snapshot rather than an unbounded loop.
	maxFeedPages = 1000

	schemaVersion = 1
)

var (
	ErrInvalidConfig  = errors.New("world read: invalid config")
	ErrContract       = errors.New("world read: response contract invalid")
	ErrUnauthorized   = errors.New("world read: credential rejected")
	ErrForbidden      = errors.New("world read: scope forbidden")
	ErrNotFound       = errors.New("world read: resource not found")
	ErrUnavailable    = errors.New("world read: service unavailable")
	ErrResyncRequired = errors.New("world read: resync required")
)

// Config is one exact CORE read scope. ActorIDs is an allowlist, never a
// filter applied after a full response is exposed.
type Config struct {
	BaseURL       string
	Credential    string
	WorldID       string
	TimelineID    string
	TimelineEpoch string
	ActorIDs      []string
	Timeout       time.Duration
	MaxEvents     int
}

// Actor is one CORE-visible WORLD Actor. Actors in the configured allowlist
// that are absent from the WORLD Snapshot are still returned with Present
// false. Actors outside the allowlist are never returned.
type Actor struct {
	ActorID    string
	Present    bool
	RoomID     string
	ActivityID string
}

// Snapshot is the CORE-visible part of a WORLD Snapshot.
type Snapshot struct {
	SchemaVersion int
	WorldID       string
	TimelineID    string
	TimelineEpoch string
	StateSeq      uint64
	StateHash     string
	Actors        []Actor
}

// Event is one CORE-visible WORLD Event. Payload is the exact WORLD bytes for
// an event that is either Actor-global or scoped to an allowed Actor.
type Event struct {
	Seq           uint64
	TransactionID string
	EventID       string
	Kind          string
	Payload       json.RawMessage
	RecordedAt    time.Time
}

// EventBatch is one whole-transaction page. Skipped counts Actor-scoped events
// that were outside the CORE view scope and therefore carried no payload to
// the caller.
type EventBatch struct {
	Events  []Event
	NextSeq uint64
	Done    bool
	Skipped uint64
}

// Feed is a Snapshot followed by contiguous visible Events from the caller's
// cursor. Cursor is the last transaction boundary that was consumed, including
// hidden events, so a caller never re-reads a redacted event.
type Feed struct {
	Snapshot Snapshot
	Events   []Event
	Cursor   uint64
	Skipped  uint64
}

// ReadError carries the transport classification of a failed read. Retryable
// is true only for transport, timeout, throttling, and server failures.
type ReadError struct {
	Op        string
	Status    int
	Retryable bool
	Err       error
}

func (e *ReadError) Error() string {
	if e == nil {
		return "world read failed"
	}
	if e.Err != nil {
		return e.Op + ": " + e.Err.Error()
	}
	return e.Op + ": read failed"
}

func (e *ReadError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Client is safe for concurrent reads. It never logs or returns the
// credential.
type Client struct {
	base              *url.URL
	basePath          string
	credential        string
	worldIDValue      string
	timelineIDValue   string
	worldTransportID  string
	timelineTransport string
	epoch             string
	actors            map[string]struct{}
	actorOrder        []string
	maxEvents         int
	timeout           time.Duration
	httpClient        *http.Client
}

// New validates an exact read scope. It does not contact WORLD.
func New(cfg Config) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("%w: base URL is required", ErrInvalidConfig)
	}
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: base URL must be an absolute HTTP(S) URL without userinfo, query, or fragment", ErrInvalidConfig)
	}
	if cfg.Credential == "" || strings.TrimSpace(cfg.Credential) != cfg.Credential ||
		strings.ContainsAny(cfg.Credential, "\r\n") {
		return nil, fmt.Errorf("%w: credential is invalid", ErrInvalidConfig)
	}
	for name, value := range map[string]string{
		"world_id":       cfg.WorldID,
		"timeline_id":    cfg.TimelineID,
		"timeline_epoch": cfg.TimelineEpoch,
	} {
		if !validID(value) {
			return nil, fmt.Errorf("%w: %s is invalid", ErrInvalidConfig, name)
		}
	}
	if len(cfg.ActorIDs) == 0 {
		return nil, fmt.Errorf("%w: at least one actor is required", ErrInvalidConfig)
	}
	actors := make(map[string]struct{}, len(cfg.ActorIDs))
	actorOrder := make([]string, 0, len(cfg.ActorIDs))
	for _, actorID := range cfg.ActorIDs {
		if !validID(actorID) {
			return nil, fmt.Errorf("%w: actor ID is invalid", ErrInvalidConfig)
		}
		if _, exists := actors[actorID]; exists {
			return nil, fmt.Errorf("%w: duplicate actor ID %q", ErrInvalidConfig, actorID)
		}
		actors[actorID] = struct{}{}
		actorOrder = append(actorOrder, actorID)
	}
	sort.Strings(actorOrder)

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, fmt.Errorf("%w: timeout must be positive", ErrInvalidConfig)
	}
	maxEvents := cfg.MaxEvents
	if maxEvents == 0 {
		maxEvents = DefaultMaxEvents
	}
	if maxEvents < 1 || maxEvents > DefaultMaxEvents {
		return nil, fmt.Errorf("%w: max events must be between 1 and %d", ErrInvalidConfig, DefaultMaxEvents)
	}
	worldTransportID, err := encodeTransportID(cfg.WorldID)
	if err != nil {
		return nil, fmt.Errorf("%w: world transport ID: %v", ErrInvalidConfig, err)
	}
	timelineTransport, err := encodeTransportID(cfg.TimelineID)
	if err != nil {
		return nil, fmt.Errorf("%w: timeline transport ID: %v", ErrInvalidConfig, err)
	}

	return &Client{
		base:              parsed,
		basePath:          strings.TrimRight(parsed.Path, "/"),
		credential:        cfg.Credential,
		worldIDValue:      cfg.WorldID,
		timelineIDValue:   cfg.TimelineID,
		worldTransportID:  worldTransportID,
		timelineTransport: timelineTransport,
		epoch:             cfg.TimelineEpoch,
		actors:            actors,
		actorOrder:        actorOrder,
		maxEvents:         maxEvents,
		timeout:           timeout,
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Snapshot reads the current WORLD Snapshot for the configured scope.
func (c *Client) Snapshot(ctx context.Context) (Snapshot, error) {
	if c == nil || c.httpClient == nil || c.base == nil {
		return Snapshot{}, &ReadError{Op: "snapshot", Err: ErrInvalidConfig}
	}
	if ctx == nil {
		return Snapshot{}, &ReadError{Op: "snapshot", Err: ErrInvalidConfig}
	}
	var dto snapshotDTO
	if err := c.get(ctx, c.urlFor("snapshot", nil), &dto, "snapshot"); err != nil {
		return Snapshot{}, err
	}
	if err := c.validateSnapshot(dto); err != nil {
		return Snapshot{}, &ReadError{Op: "snapshot", Err: err}
	}

	actors := make([]Actor, 0, len(c.actorOrder))
	for _, actorID := range c.actorOrder {
		state, present := dto.State.Actors[actorID]
		if !present {
			actors = append(actors, Actor{ActorID: actorID})
			continue
		}
		if state.RoomID != "" && !validID(state.RoomID) {
			return Snapshot{}, &ReadError{Op: "snapshot", Err: fmt.Errorf("%w: actor %q room_id is invalid", ErrContract, actorID)}
		}
		if state.ActivityID != "" && !validID(state.ActivityID) {
			return Snapshot{}, &ReadError{Op: "snapshot", Err: fmt.Errorf("%w: actor %q activity_id is invalid", ErrContract, actorID)}
		}
		actors = append(actors, Actor{
			ActorID:    actorID,
			Present:    true,
			RoomID:     state.RoomID,
			ActivityID: state.ActivityID,
		})
	}
	return Snapshot{
		SchemaVersion: dto.SchemaVersion,
		WorldID:       dto.WorldID,
		TimelineID:    dto.TimelineID,
		TimelineEpoch: dto.TimelineEpoch,
		StateSeq:      dto.StateSeq,
		StateHash:     dto.StateHash,
		Actors:        actors,
	}, nil
}

// Events reads one whole-transaction page after a transaction boundary.
func (c *Client) Events(ctx context.Context, after uint64) (EventBatch, error) {
	if c == nil || c.httpClient == nil || c.base == nil {
		return EventBatch{}, &ReadError{Op: "events", Err: ErrInvalidConfig}
	}
	if ctx == nil {
		return EventBatch{}, &ReadError{Op: "events", Err: ErrInvalidConfig}
	}
	if after > maxJSONSequence {
		return EventBatch{}, &ReadError{Op: "events", Err: fmt.Errorf("%w: after_seq is out of range", ErrContract)}
	}
	query := url.Values{}
	query.Set("after_seq", strconv.FormatUint(after, 10))
	query.Set("limit", strconv.Itoa(c.maxEvents))
	var dto eventsDTO
	if err := c.get(ctx, c.urlFor("events", query), &dto, "events"); err != nil {
		return EventBatch{}, err
	}
	if err := validateEvents(after, dto); err != nil {
		return EventBatch{}, &ReadError{Op: "events", Err: err}
	}

	batch := EventBatch{
		Events:  make([]Event, 0, len(dto.Events)),
		NextSeq: dto.NextSeq,
		Done:    dto.Done,
	}
	for _, raw := range dto.Events {
		event, visible, err := c.projectEvent(raw)
		if err != nil {
			return EventBatch{}, &ReadError{Op: "events", Err: err}
		}
		if !visible {
			batch.Skipped++
			continue
		}
		batch.Events = append(batch.Events, event)
	}
	return batch, nil
}

// ReadFeed reads a Snapshot and then every contiguous page from after. A
// cursor that is ahead of the Snapshot, a missing history, or an event gap is
// returned as ErrResyncRequired instead of a partial feed.
func (c *Client) ReadFeed(ctx context.Context, after uint64) (Feed, error) {
	if c == nil || c.httpClient == nil || c.base == nil {
		return Feed{}, &ReadError{Op: "feed", Err: ErrInvalidConfig}
	}
	if ctx == nil {
		return Feed{}, &ReadError{Op: "feed", Err: ErrInvalidConfig}
	}
	if after > maxJSONSequence {
		return Feed{}, &ReadError{Op: "feed", Err: fmt.Errorf("%w: cursor is out of range", ErrResyncRequired)}
	}
	snapshot, err := c.Snapshot(ctx)
	if err != nil {
		return Feed{}, err
	}
	if after > snapshot.StateSeq {
		return Feed{}, &ReadError{Op: "feed", Err: fmt.Errorf("%w: cursor is ahead of the snapshot", ErrResyncRequired)}
	}
	feed := Feed{Snapshot: snapshot, Events: []Event{}, Cursor: after}
	for page := 0; ; page++ {
		if page >= maxFeedPages {
			return Feed{}, &ReadError{Op: "feed", Err: fmt.Errorf("%w: catch-up page bound exceeded", ErrResyncRequired)}
		}
		batch, err := c.Events(ctx, feed.Cursor)
		if err != nil {
			return Feed{}, err
		}
		feed.Events = append(feed.Events, batch.Events...)
		feed.Skipped += batch.Skipped
		if batch.NextSeq < feed.Cursor {
			return Feed{}, &ReadError{Op: "feed", Err: fmt.Errorf("%w: cursor regressed", ErrContract)}
		}
		feed.Cursor = batch.NextSeq
		if batch.Done {
			break
		}
	}
	if feed.Cursor < snapshot.StateSeq {
		return Feed{}, &ReadError{Op: "feed", Err: fmt.Errorf("%w: history is missing", ErrResyncRequired)}
	}
	return feed, nil
}

type snapshotDTO struct {
	SchemaVersion int    `json:"schema_version"`
	WorldID       string `json:"world_id"`
	TimelineID    string `json:"timeline_id"`
	TimelineEpoch string `json:"timeline_epoch"`
	StateSeq      uint64 `json:"state_seq"`
	StateHash     string `json:"state_hash"`
	State         *struct {
		Actors map[string]actorDTO `json:"actors"`
	} `json:"state"`
}

type actorDTO struct {
	RoomID     string `json:"room_id"`
	ActivityID string `json:"activity_id"`
}

type eventDTO struct {
	Seq           uint64          `json:"seq"`
	TransactionID string          `json:"transaction_id"`
	EventID       string          `json:"event_id"`
	Kind          string          `json:"kind"`
	Payload       json.RawMessage `json:"payload"`
	RecordedAt    time.Time       `json:"recorded_at"`
}

type eventsDTO struct {
	SchemaVersion int        `json:"schema_version"`
	Events        []eventDTO `json:"events"`
	NextSeq       uint64     `json:"next_seq"`
	Done          bool       `json:"done"`
}

func (c *Client) validateSnapshot(dto snapshotDTO) error {
	if dto.SchemaVersion != schemaVersion {
		return fmt.Errorf("%w: schema_version", ErrContract)
	}
	if dto.WorldID != c.worldIDValue || dto.TimelineID != c.timelineIDValue || dto.TimelineEpoch != c.epoch {
		return fmt.Errorf("%w: response scope does not match the configured scope", ErrContract)
	}
	if dto.StateSeq > maxJSONSequence {
		return fmt.Errorf("%w: state_seq is out of range", ErrContract)
	}
	if len(dto.StateHash) != 64 || strings.ToLower(dto.StateHash) != dto.StateHash {
		return fmt.Errorf("%w: state_hash is not canonical lowercase SHA-256", ErrContract)
	}
	if _, err := hex.DecodeString(dto.StateHash); err != nil {
		return fmt.Errorf("%w: state_hash is not hexadecimal", ErrContract)
	}
	if dto.State == nil || dto.State.Actors == nil {
		return fmt.Errorf("%w: state.actors is missing", ErrContract)
	}
	return nil
}

func validateEvents(after uint64, dto eventsDTO) error {
	if dto.SchemaVersion != schemaVersion {
		return fmt.Errorf("%w: schema_version", ErrContract)
	}
	if dto.Events == nil {
		return fmt.Errorf("%w: events is missing", ErrContract)
	}
	if dto.NextSeq > maxJSONSequence {
		return fmt.Errorf("%w: next_seq is out of range", ErrContract)
	}
	if len(dto.Events) == 0 {
		if !dto.Done || dto.NextSeq != after {
			return fmt.Errorf("%w: empty page does not match the cursor", ErrContract)
		}
		return nil
	}
	if dto.NextSeq != dto.Events[len(dto.Events)-1].Seq {
		return fmt.Errorf("%w: next_seq does not match the last event", ErrContract)
	}
	if dto.NextSeq < after {
		return fmt.Errorf("%w: next_seq is behind the cursor", ErrContract)
	}
	expected := after + 1
	seen := make(map[string]struct{}, len(dto.Events))
	currentTransaction := ""
	for index, event := range dto.Events {
		if event.Seq > maxJSONSequence {
			return fmt.Errorf("%w: event seq is out of range", ErrContract)
		}
		if index == 0 {
			if event.Seq != expected {
				if event.Seq > expected {
					return fmt.Errorf("%w: event sequence gap", ErrResyncRequired)
				}
				return fmt.Errorf("%w: event sequence is behind the cursor", ErrContract)
			}
		} else if event.Seq != expected {
			return fmt.Errorf("%w: event sequence is not contiguous", ErrContract)
		}
		if !validID(event.TransactionID) || !validID(event.EventID) || !validID(event.Kind) {
			return fmt.Errorf("%w: event identity is invalid", ErrContract)
		}
		if event.RecordedAt.IsZero() {
			return fmt.Errorf("%w: event recorded_at is missing", ErrContract)
		}
		if len(event.Payload) == 0 || !json.Valid(event.Payload) {
			return fmt.Errorf("%w: event payload is not JSON", ErrContract)
		}
		if event.TransactionID != currentTransaction {
			if _, exists := seen[event.TransactionID]; exists {
				return fmt.Errorf("%w: transaction is not contiguous", ErrContract)
			}
			seen[event.TransactionID] = struct{}{}
			currentTransaction = event.TransactionID
		}
		expected++
	}
	if !dto.Done && dto.NextSeq <= after {
		return fmt.Errorf("%w: page made no progress", ErrContract)
	}
	return nil
}

func (c *Client) projectEvent(raw eventDTO) (Event, bool, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw.Payload, &payload); err != nil {
		return Event{}, false, fmt.Errorf("%w: event payload is not an object", ErrContract)
	}
	if actorRaw, ok := payload["actor_id"]; ok {
		var actorID *string
		if err := json.Unmarshal(actorRaw, &actorID); err != nil || actorID == nil {
			return Event{}, false, fmt.Errorf("%w: event actor_id is not a string", ErrContract)
		}
		if _, allowed := c.actors[*actorID]; !allowed {
			return Event{}, false, nil
		}
	}
	return Event{
		Seq:           raw.Seq,
		TransactionID: raw.TransactionID,
		EventID:       raw.EventID,
		Kind:          raw.Kind,
		Payload:       append(json.RawMessage(nil), raw.Payload...),
		RecordedAt:    raw.RecordedAt,
	}, true, nil
}

func (c *Client) urlFor(resource string, query url.Values) *url.URL {
	u := *c.base
	u.Path = c.basePath + "/v1/worlds/" + c.worldTransportID + "/timelines/" + c.timelineTransport + "/" + resource
	u.RawPath = ""
	if query == nil {
		u.RawQuery = ""
	} else {
		u.RawQuery = query.Encode()
	}
	return &u
}

func (c *Client) get(ctx context.Context, u *url.URL, out any, op string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return &ReadError{Op: op, Err: fmt.Errorf("%w: build request: %v", ErrContract, err)}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.credential)

	response, err := c.httpClient.Do(request)
	if err != nil {
		return &ReadError{Op: op, Retryable: true, Err: fmt.Errorf("%w: %v", ErrUnavailable, err)}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return classifyStatus(op, response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return &ReadError{Op: op, Status: response.StatusCode, Retryable: true, Err: fmt.Errorf("%w: read response: %v", ErrUnavailable, err)}
	}
	if len(body) > maxResponseBytes {
		return &ReadError{Op: op, Status: response.StatusCode, Err: fmt.Errorf("%w: response is too large", ErrContract)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &ReadError{Op: op, Status: response.StatusCode, Err: fmt.Errorf("%w: decode response: %v", ErrContract, err)}
	}
	return nil
}

func classifyStatus(op string, status int) *ReadError {
	switch status {
	case http.StatusUnauthorized:
		return &ReadError{Op: op, Status: status, Err: ErrUnauthorized}
	case http.StatusForbidden:
		return &ReadError{Op: op, Status: status, Err: ErrForbidden}
	case http.StatusNotFound:
		return &ReadError{Op: op, Status: status, Err: ErrNotFound}
	}
	if status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= 500 {
		return &ReadError{Op: op, Status: status, Retryable: true, Err: ErrUnavailable}
	}
	return &ReadError{Op: op, Status: status, Err: ErrContract}
}
