package boardcommunication

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxSSELineBytes  = 16 << 10
	maxSSEFrameBytes = 64 << 10
	sseReadIdleLimit = 45 * time.Second
)

// EventStream represents one caller-owned BOARD event connection.
type EventStream struct {
	client      *Client
	body        io.ReadCloser
	reader      *bufio.Reader
	cancel      context.CancelFunc
	stopLife    func() bool
	stopContext func() bool
	idleTimer   *time.Timer
	mu          sync.Mutex
	nextMu      sync.Mutex
	closed      bool
	terminalErr error
	lastSeq     int64
}

// OpenEvents opens one SSE connection from a nonnegative sequence hint.
// It does not reconnect, catch up the inbox, or acknowledge any item.
func (c *Client) OpenEvents(ctx context.Context, afterSeq int64) (*EventStream, error) {
	if c == nil || ctx == nil {
		return nil, ErrInvalidRequest
	}
	if afterSeq < 0 {
		return nil, ErrInvalidRequest
	}
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return nil, ErrClosed
	}
	base := c.base
	lifetime := c.lifetime
	tokenSource := c.tokenSource
	httpClient := c.eventClient
	socketPath := c.socketPath
	expectedConnection := c.expectedConnection
	c.mu.RUnlock()
	if lifetime == nil || tokenSource == nil || httpClient == nil {
		return nil, ErrUnavailable
	}
	if ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	if socketPath != "" {
		if err := validateUnixSocketPath(socketPath); err != nil {
			return nil, err
		}
	}

	streamCtx, cancel := context.WithCancel(ctx)
	stopLife := context.AfterFunc(lifetime, cancel)
	cleanup := func() {
		stopLife()
		cancel()
	}
	tokenCtx, tokenCancel := context.WithTimeout(streamCtx, defaultRequestTimeout)
	token, err := tokenSource(tokenCtx)
	tokenCancel()
	if err != nil || !validBearerToken(token) {
		cleanup()
		return nil, ErrAuthentication
	}

	requestURL := base
	requestURL.Path = "/api/events"
	requestURL.RawPath = ""
	requestURL.RawQuery = ""
	request, err := http.NewRequestWithContext(streamCtx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		cleanup()
		return nil, ErrInvalidRequest
	}
	request.GetBody = nil
	request.Header.Set("Authorization", "Bearer "+string(token))
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("Last-Event-ID", strconv.FormatInt(afterSeq, 10))
	setConnectionIdentityHeaders(request.Header, expectedConnection)
	response, err := httpClient.Do(request)
	if err != nil {
		cleanup()
		if c.isClosed() {
			return nil, ErrClosed
		}
		return nil, ErrUnavailable
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusConflict {
			bodyTimer := time.AfterFunc(defaultRequestTimeout, func() { _ = response.Body.Close() })
			body, readErr := readBounded(response.Body, maxResponseBodyBytes)
			bodyTimer.Stop()
			response.Body.Close()
			cleanup()
			if readErr != nil {
				if errors.Is(readErr, errResponseRead) {
					if c.isClosed() {
						return nil, ErrClosed
					}
					return nil, ErrUnavailable
				}
				return nil, ErrProtocol
			}
			return nil, connectionIdentityResponseError(response.StatusCode, body, response.Header)
		}
		response.Body.Close()
		cleanup()
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			return nil, ErrAuthentication
		}
		if response.StatusCode >= 500 && response.StatusCode <= 599 {
			return nil, ErrUnavailable
		}
		return nil, ErrProtocol
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		response.Body.Close()
		cleanup()
		return nil, ErrProtocol
	}

	stream := &EventStream{
		client: c, body: response.Body,
		reader: bufio.NewReaderSize(response.Body, maxSSELineBytes),
		cancel: cancel, stopLife: stopLife, lastSeq: afterSeq,
	}
	stream.idleTimer = time.AfterFunc(sseReadIdleLimit, func() {
		stream.finish(ErrUnavailable)
	})
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		stream.finish(ErrClosed)
		return nil, ErrClosed
	}
	stream.mu.Lock()
	if stream.closed {
		stream.mu.Unlock()
		c.mu.Unlock()
		return nil, stream.currentError()
	}
	c.streams[stream] = struct{}{}
	stream.stopContext = context.AfterFunc(streamCtx, func() {
		terminalErr := ErrUnavailable
		if stream.client.isClosed() {
			terminalErr = ErrClosed
		}
		stream.finish(terminalErr)
	})
	stream.mu.Unlock()
	c.mu.Unlock()
	return stream, nil
}

func (c *Client) isClosed() bool {
	if c == nil {
		return true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.closed
}

func (c *Client) removeEventStream(stream *EventStream) {
	if c == nil || stream == nil {
		return
	}
	c.mu.Lock()
	delete(c.streams, stream)
	c.mu.Unlock()
}

// Next returns the next public BOARD event hint. Comment keepalives are read
// as activity and never returned as events.
func (s *EventStream) Next() (BoardEvent, error) {
	if s == nil {
		return BoardEvent{}, ErrInvalidRequest
	}
	s.nextMu.Lock()
	defer s.nextMu.Unlock()
	if err := s.currentError(); err != nil {
		return BoardEvent{}, err
	}
	var frame sseFrame
	for {
		line, err := s.reader.ReadSlice('\n')
		if len(line) > 0 {
			s.touch()
		}
		if errors.Is(err, bufio.ErrBufferFull) || len(line) > maxSSELineBytes {
			return BoardEvent{}, s.fail(ErrProtocol)
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) == 0 {
				return BoardEvent{}, s.fail(ErrUnavailable)
			}
			if errors.Is(err, io.EOF) {
				return BoardEvent{}, s.fail(ErrProtocol)
			}
			return BoardEvent{}, s.fail(s.currentError())
		}
		frame.size += len(line)
		if frame.size > maxSSEFrameBytes {
			return BoardEvent{}, s.fail(ErrProtocol)
		}
		line = bytesTrimSSELine(line)
		if len(line) == 0 {
			if !frame.hasFields() {
				frame = sseFrame{}
				continue
			}
			event, parseErr := s.decodeFrame(frame)
			if parseErr != nil {
				return BoardEvent{}, s.fail(ErrProtocol)
			}
			return event, nil
		}
		if line[0] == ':' {
			continue
		}
		if !frame.addLine(line) {
			return BoardEvent{}, s.fail(ErrProtocol)
		}
	}
}

// Close releases the event connection and is safe to call repeatedly.
func (s *EventStream) Close() error {
	if s == nil {
		return nil
	}
	s.finish(ErrClosed)
	return nil
}

type sseFrame struct {
	size     int
	id       int64
	event    string
	data     string
	hasID    bool
	hasEvent bool
	hasData  bool
}

func (f *sseFrame) hasFields() bool { return f.hasID || f.hasEvent || f.hasData }

func (f *sseFrame) addLine(line []byte) bool {
	field, value, found := strings.Cut(string(line), ":")
	if !found {
		return false
	}
	if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	switch field {
	case "id":
		if f.hasID || value == "" {
			return false
		}
		sequence, err := strconv.ParseInt(value, 10, 64)
		if err != nil || sequence < 0 || strconv.FormatInt(sequence, 10) != value {
			return false
		}
		f.id, f.hasID = sequence, true
	case "event":
		if f.hasEvent || value == "" {
			return false
		}
		f.event, f.hasEvent = value, true
	case "data":
		if f.hasData || value == "" {
			return false
		}
		f.data, f.hasData = value, true
	default:
		return false
	}
	return true
}

func (s *EventStream) decodeFrame(frame sseFrame) (BoardEvent, error) {
	if !frame.hasID || !frame.hasEvent || !frame.hasData || frame.event != "board" {
		return BoardEvent{}, ErrProtocol
	}
	var event BoardEvent
	data := []byte(frame.data)
	if !boundedJSONDepth(data) || decodeStrictJSON(data, &event) != nil || event.Seq != frame.id || event.Seq <= s.lastSeq ||
		event.Type == "" || event.Time == "" {
		return BoardEvent{}, ErrProtocol
	}
	s.lastSeq = event.Seq
	return event, nil
}

func boundedJSONDepth(data []byte) bool {
	depth := 0
	inString := false
	escaped := false
	for _, value := range data {
		if inString {
			if escaped {
				escaped = false
				continue
			}
			switch value {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		switch value {
		case '"':
			inString = true
		case '{', '[':
			depth++
			if depth > maxCanonicalJSONDepth {
				return false
			}
		case '}', ']':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return !inString && !escaped && depth == 0
}

func bytesTrimSSELine(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\n' {
		line = line[:len(line)-1]
	}
	if len(line) > 0 && line[len(line)-1] == '\r' {
		line = line[:len(line)-1]
	}
	return line
}

func (s *EventStream) touch() {
	s.mu.Lock()
	if !s.closed && s.idleTimer != nil {
		s.idleTimer.Reset(sseReadIdleLimit)
	}
	s.mu.Unlock()
}

func (s *EventStream) currentError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		return nil
	}
	if s.terminalErr == nil {
		return ErrClosed
	}
	return s.terminalErr
}

func (s *EventStream) fail(err error) error {
	if err == nil {
		err = ErrUnavailable
	}
	s.finish(err)
	if terminal := s.currentError(); terminal != nil {
		return terminal
	}
	return err
}

func (s *EventStream) finish(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.terminalErr = err
	body := s.body
	cancel := s.cancel
	stopLife := s.stopLife
	stopContext := s.stopContext
	timer := s.idleTimer
	client := s.client
	s.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	if stopLife != nil {
		stopLife()
	}
	if stopContext != nil {
		stopContext()
	}
	if cancel != nil {
		cancel()
	}
	if body != nil {
		_ = body.Close()
	}
	if client != nil {
		client.removeEventStream(s)
	}
}
