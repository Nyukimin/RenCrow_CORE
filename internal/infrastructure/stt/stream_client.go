package stt

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
	"golang.org/x/net/websocket"
)

// The WebSocket side of the RenCrow_STT audio stream (protocol stt.stream.v1,
// docs/06_Public_API仕様.md "WS /stt"). CORE opens one stream per /stt client
// connection and relays frames both ways. This file owns the wire: the frame
// kind, the timeouts of opening, the write deadline, and how CORE knows the
// stream is alive. What the frames mean belongs to modules/stt.
//
// golang.org/x/net/websocket hides three things the relay needs, so they are
// handled here:
//
//   - websocket.Message.Receive drops the frame kind. The stream tells control
//     (text) from audio (binary) by kind alone, never by content, so every read
//     and write goes through a Codec that keeps it (ReceiveFrame, SendFrame).
//   - A pong is consumed inside the library and never reaches the caller, so
//     liveness is the time of the last byte read from the socket (LastReceived),
//     recorded by a wrapper around the connection.
//   - A refused upgrade is reported as ErrBadStatus without the status. The
//     wrapper keeps the first bytes of the answer so the status can be read
//     back (503 means STT is full; anything else is a failure).

const (
	// FrameText carries a JSON control message or event.
	FrameText FrameKind = websocket.TextFrame
	// FrameBinary carries PCM16 audio.
	FrameBinary FrameKind = websocket.BinaryFrame
	// FramePing is a WebSocket ping. It is only ever sent, never received (the
	// library answers and discards what the peer pings).
	FramePing FrameKind = websocket.PingFrame

	// StreamStageDial, StreamStageHandshake and StreamStageSessionReady say how
	// far opening the stream got.
	StreamStageDial         = "dial"
	StreamStageHandshake    = "handshake"
	StreamStageSessionReady = "session_ready"

	defaultStreamDialTimeout  = 3 * time.Second
	defaultStreamOpenTimeout  = 5 * time.Second
	defaultStreamWriteTimeout = 10 * time.Second
	// STT's events are small; a frame this large is a broken peer.
	defaultStreamMaxFrameBytes = 1 << 20

	// How much of the upgrade answer is kept to recover the status.
	streamHandshakeSniffBytes = 8 * 1024
	// A close frame is a courtesy; it must not wait for a peer that stopped reading.
	streamCloseWriteTimeout = time.Second
)

// FrameKind is the WebSocket opcode of a frame.
type FrameKind byte

// StreamFrame is one WebSocket message with its kind.
type StreamFrame struct {
	Kind FrameKind
	Data []byte
}

var frameCodec = websocket.Codec{
	Marshal: func(v interface{}) ([]byte, byte, error) {
		frame, ok := v.(StreamFrame)
		if !ok {
			return nil, websocket.UnknownFrame, websocket.ErrNotSupported
		}
		switch frame.Kind {
		case FrameText, FrameBinary, FramePing:
			return frame.Data, byte(frame.Kind), nil
		}
		return nil, websocket.UnknownFrame, websocket.ErrNotSupported
	},
	Unmarshal: func(data []byte, payloadType byte, v interface{}) error {
		out, ok := v.(*StreamFrame)
		if !ok {
			return websocket.ErrNotSupported
		}
		*out = StreamFrame{Kind: FrameKind(payloadType), Data: data}
		return nil
	},
}

// ReceiveFrame reads one message of a WebSocket connection with its kind. It is
// used on both ends of the relay: the stream CORE dials and the client
// connection CORE serves. A frame over the connection's MaxPayloadBytes is
// reported as websocket.ErrFrameTooLarge before it is read.
func ReceiveFrame(ws *websocket.Conn) (StreamFrame, error) {
	var frame StreamFrame
	err := frameCodec.Receive(ws, &frame)
	return frame, err
}

// SendFrame writes one message with its kind.
func SendFrame(ws *websocket.Conn, frame StreamFrame) error {
	return frameCodec.Send(ws, frame)
}

// StreamOpenError says how opening the stream failed. Status is the HTTP status
// of a refused upgrade, 0 when there was none.
type StreamOpenError struct {
	Stage  string
	Status int
	Err    error
}

func (e *StreamOpenError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("open STT stream (%s, HTTP %d): %v", e.Stage, e.Status, e.Err)
	}
	return fmt.Sprintf("open STT stream (%s): %v", e.Stage, e.Err)
}

func (e *StreamOpenError) Unwrap() error { return e.Err }

// StreamClient opens the stream. The zero value of every timeout and limit means
// its default.
type StreamClient struct {
	// URL is the ws:// or wss:// URL of the stream.
	URL string
	// Origin is sent with the upgrade. It defaults to the fixed value the stream
	// accepts (modulestt.StreamOrigin).
	Origin string
	// DialTimeout bounds the TCP connection (default 3s).
	DialTimeout time.Duration
	// OpenTimeout bounds the WebSocket handshake and the wait for session_ready
	// together (default 5s).
	OpenTimeout time.Duration
	// WriteTimeout bounds one write after the stream is open (default 10s).
	WriteTimeout time.Duration
	// MaxFrameBytes is the largest frame accepted from STT (default 1 MiB).
	MaxFrameBytes int
	// Now is the clock of LastReceived. Deadlines on the socket always use real time.
	Now func() time.Time
}

func (c StreamClient) withDefaults() StreamClient {
	if c.Origin == "" {
		c.Origin = modulestt.StreamOrigin
	}
	if c.DialTimeout <= 0 {
		c.DialTimeout = defaultStreamDialTimeout
	}
	if c.OpenTimeout <= 0 {
		c.OpenTimeout = defaultStreamOpenTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = defaultStreamWriteTimeout
	}
	if c.MaxFrameBytes <= 0 {
		c.MaxFrameBytes = defaultStreamMaxFrameBytes
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

// Open dials the stream, sends sessionStart as the first text frame, and waits
// for the first text frame back, which it returns (session_ready, validated by
// the caller). The wait for the handshake and that frame is bounded together by
// OpenTimeout and by ctx. On any error nothing is left open.
func (c StreamClient) Open(ctx context.Context, sessionStart []byte) (*StreamConn, []byte, error) {
	c = c.withDefaults()
	target, err := url.Parse(c.URL)
	if err != nil || (target.Scheme != "ws" && target.Scheme != "wss") || target.Host == "" {
		return nil, nil, &StreamOpenError{Stage: StreamStageDial, Err: errors.New("the STT stream URL is not an absolute ws:// or wss:// URL")}
	}

	dialer := &net.Dialer{Timeout: c.DialTimeout}
	raw, err := dialer.DialContext(ctx, "tcp", streamAddress(target))
	if err != nil {
		return nil, nil, &StreamOpenError{Stage: StreamStageDial, Err: err}
	}
	// One deadline covers the handshake, session.start and session_ready. It is
	// cleared once the stream is open, and context cancellation brings it forward.
	_ = raw.SetDeadline(time.Now().Add(c.OpenTimeout))
	stopCancel := context.AfterFunc(ctx, func() { _ = raw.SetDeadline(time.Now()) })
	fail := func(stage string, status int, err error) (*StreamConn, []byte, error) {
		stopCancel()
		_ = raw.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = errors.Join(err, ctxErr)
		}
		return nil, nil, &StreamOpenError{Stage: stage, Status: status, Err: err}
	}

	var wire net.Conn = raw
	if target.Scheme == "wss" {
		secured := tls.Client(raw, &tls.Config{ServerName: target.Hostname(), MinVersion: tls.VersionTLS12})
		if err := secured.Handshake(); err != nil {
			return fail(StreamStageHandshake, 0, err)
		}
		wire = secured
	}
	tracked := newTrackedConn(wire, c.Now)
	config, err := websocket.NewConfig(c.URL, c.Origin)
	if err != nil {
		return fail(StreamStageDial, 0, err)
	}
	ws, err := websocket.NewClient(config, tracked)
	if err != nil {
		return fail(StreamStageHandshake, tracked.upgradeStatus(), err)
	}
	tracked.stopSniffing()
	ws.MaxPayloadBytes = c.MaxFrameBytes
	conn := &StreamConn{ws: ws, wire: wire, tracked: tracked, writeTimeout: c.WriteTimeout}

	if err := SendFrame(ws, StreamFrame{Kind: FrameText, Data: sessionStart}); err != nil {
		return fail(StreamStageSessionReady, 0, err)
	}
	first, err := ReceiveFrame(ws)
	if err != nil {
		return fail(StreamStageSessionReady, 0, err)
	}
	if first.Kind != FrameText {
		return fail(StreamStageSessionReady, 0, errors.New("the first frame of the stream is not text"))
	}
	if !stopCancel() {
		// The context ended while the stream was opening and already moved the
		// deadline; the connection cannot be trusted.
		return fail(StreamStageSessionReady, 0, context.Cause(ctx))
	}
	_ = wire.SetDeadline(time.Time{})
	return conn, first.Data, nil
}

func streamAddress(target *url.URL) string {
	if target.Port() != "" {
		return target.Host
	}
	port := "80"
	if target.Scheme == "wss" {
		port = "443"
	}
	return net.JoinHostPort(target.Hostname(), port)
}

// StreamConn is an open stream. Receive may run in one goroutine while Send
// and Ping run in another; Close may be called from any.
type StreamConn struct {
	ws           *websocket.Conn
	wire         net.Conn
	tracked      *trackedConn
	writeTimeout time.Duration

	sendMu    sync.Mutex
	closeOnce sync.Once
}

// Send writes one frame, giving up after the write timeout. A frame kind other
// than text, binary or ping is refused before it reaches the wire.
func (c *StreamConn) Send(frame StreamFrame) error {
	switch frame.Kind {
	case FrameText, FrameBinary, FramePing:
	default:
		return fmt.Errorf("unsupported STT stream frame kind %d", frame.Kind)
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(c.writeTimeout))
	return SendFrame(c.ws, frame)
}

// Ping sends a WebSocket ping. STT answers with a pong, which LastReceived counts.
func (c *StreamConn) Ping() error {
	return c.Send(StreamFrame{Kind: FramePing})
}

// Receive blocks for the next text or binary frame. It has no read deadline: a
// deadline that fires inside a frame can corrupt the stream. Liveness is judged
// from LastReceived instead.
func (c *StreamConn) Receive() (StreamFrame, error) {
	return ReceiveFrame(c.ws)
}

// LastReceived is the time the socket last delivered any byte, a pong included.
func (c *StreamConn) LastReceived() time.Time {
	return c.tracked.lastReceived()
}

// Close ends the stream and unblocks Receive. It is idempotent: only the first
// call does anything and reports its error.
func (c *StreamConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		if c.sendMu.TryLock() {
			// Nobody is writing: say goodbye, but never wait for a peer that stopped reading.
			_ = c.wire.SetWriteDeadline(time.Now().Add(streamCloseWriteTimeout))
			err = c.ws.Close()
			c.sendMu.Unlock()
			return
		}
		// A write is stuck. Abort it instead of queueing behind it.
		_ = c.wire.SetDeadline(time.Now())
		err = c.wire.Close()
	})
	return err
}

// trackedConn records when the socket last delivered bytes and, until the
// handshake is over, keeps the first bytes of the answer.
type trackedConn struct {
	net.Conn
	now      func() time.Time
	last     atomic.Int64
	sniffing atomic.Bool

	mu   sync.Mutex
	head []byte
}

func newTrackedConn(conn net.Conn, now func() time.Time) *trackedConn {
	tracked := &trackedConn{Conn: conn, now: now}
	tracked.last.Store(now().UnixNano())
	tracked.sniffing.Store(true)
	return tracked
}

func (c *trackedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 {
		c.last.Store(c.now().UnixNano())
		if c.sniffing.Load() {
			c.mu.Lock()
			if room := streamHandshakeSniffBytes - len(c.head); room > 0 {
				c.head = append(c.head, p[:min(n, room)]...)
			}
			c.mu.Unlock()
		}
	}
	return n, err
}

func (c *trackedConn) stopSniffing() {
	c.sniffing.Store(false)
	c.mu.Lock()
	c.head = nil
	c.mu.Unlock()
}

func (c *trackedConn) lastReceived() time.Time {
	return time.Unix(0, c.last.Load()).UTC()
}

// upgradeStatus is the HTTP status of the answer to the upgrade request, or 0
// when it was not read.
func (c *trackedConn) upgradeStatus() int {
	c.mu.Lock()
	head := append([]byte(nil), c.head...)
	c.mu.Unlock()
	if len(head) == 0 {
		return 0
	}
	if resp, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(head)), nil); err == nil {
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	// A truncated answer: the status line alone is enough ("HTTP/1.1 503 ...").
	if line, _, ok := bytes.Cut(head, []byte("\r\n")); ok {
		if fields := bytes.Fields(line); len(fields) >= 2 {
			if status, err := strconv.Atoi(string(fields[1])); err == nil {
				return status
			}
		}
	}
	return 0
}
