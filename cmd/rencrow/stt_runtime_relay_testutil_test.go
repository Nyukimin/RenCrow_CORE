package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sttinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/stt"
	"golang.org/x/net/websocket"
)

// The relay is tested against a RenCrow_STT stand-in that speaks the real
// stream protocol over a real WebSocket on loopback, and against a fake
// upstream and a hand-moved clock where the time itself is what is tested.

const relayTestWait = 3 * time.Second

// fakeSTTServer is a RenCrow_STT audio stream stand-in.
type fakeSTTServer struct {
	URL string
	// Limits is the "limits" object session_ready announces.
	Limits string
	// Protocol is the protocol session_ready announces.
	Protocol string
	// RefuseStatus, when set, makes the upgrade fail with this HTTP status.
	RefuseStatus int

	t     *testing.T
	conns chan *fakeSTTConn

	mu      sync.Mutex
	origins []string
	starts  []map[string]any
}

func startFakeSTTServer(t *testing.T) *fakeSTTServer {
	t.Helper()
	fake := &fakeSTTServer{
		t:        t,
		conns:    make(chan *fakeSTTConn, 16),
		Protocol: "stt.stream.v1",
		Limits:   `{"max_utterance_ms":30000,"max_frame_bytes":65536,"queue_depth":8,"terminal_deadline_ms":75000}`,
	}
	stream := websocket.Server{
		Handshake: func(_ *websocket.Config, req *http.Request) error {
			fake.mu.Lock()
			fake.origins = append(fake.origins, req.Header.Get("Origin"))
			fake.mu.Unlock()
			return nil
		},
		Handler: websocket.Handler(fake.serve),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/audio/stream", func(w http.ResponseWriter, r *http.Request) {
		if fake.RefuseStatus != 0 {
			http.Error(w, "refused", fake.RefuseStatus)
			return
		}
		stream.ServeHTTP(w, r)
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","target_health":{"ready":true},"stream":{"protocol":"stt.stream.v1","enabled":true,"connections":0,"max_connections":32,"pending_jobs":0,"fail_fast":false}}`))
	})
	mux.HandleFunc("/v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("CORE called the HTTP transcription route for a /stt utterance; only STT may call its target")
		http.Error(w, "unexpected", http.StatusTeapot)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	fake.URL = "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/audio/stream"
	return fake
}

func (f *fakeSTTServer) HTTPBase() string {
	return "http" + strings.TrimPrefix(strings.TrimSuffix(f.URL, "/v1/audio/stream"), "ws")
}

func (f *fakeSTTServer) Origins() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.origins...)
}

func (f *fakeSTTServer) SessionStarts() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.starts...)
}

func (f *fakeSTTServer) serve(ws *websocket.Conn) {
	first, err := sttinfra.ReceiveFrame(ws)
	if err != nil {
		return
	}
	var start map[string]any
	_ = json.Unmarshal(first.Data, &start)
	f.mu.Lock()
	f.starts = append(f.starts, start)
	f.mu.Unlock()
	requestID, _ := start["request_id"].(string)
	conn := &fakeSTTConn{ws: ws, RequestID: requestID, seq: 9, recv: make(chan sttinfra.StreamFrame, 256), done: make(chan struct{})}
	ready := fmt.Sprintf(`{"type":"session_ready","seq":1,"protocol":%q,"provider":"rencrow_stt","target":"t","language":"ja","sample_rate":16000,"request_id":%q,"limits":%s}`,
		f.Protocol, requestID, f.Limits)
	if err := sttinfra.SendFrame(ws, sttinfra.StreamFrame{Kind: sttinfra.FrameText, Data: []byte(ready)}); err != nil {
		return
	}
	f.conns <- conn
	defer close(conn.done)
	for {
		frame, err := sttinfra.ReceiveFrame(ws)
		if err != nil {
			return
		}
		select {
		case conn.recv <- frame:
		default:
			f.t.Errorf("the fake STT's receive buffer overflowed")
			return
		}
	}
}

// Accept waits for the next connection CORE opens.
func (f *fakeSTTServer) Accept() *fakeSTTConn {
	f.t.Helper()
	select {
	case conn := <-f.conns:
		return conn
	case <-time.After(relayTestWait):
		f.t.Fatal("CORE did not open a stream to the fake STT")
		return nil
	}
}

// fakeSTTConn is one stream as the STT side sees it.
type fakeSTTConn struct {
	ws        *websocket.Conn
	RequestID string
	recv      chan sttinfra.StreamFrame
	done      chan struct{}

	mu  sync.Mutex
	seq int
}

// Send writes an event. payload is a JSON object without seq; the stand-in adds
// an increasing one, starting well above CORE's own so a leaked seq is visible.
func (c *fakeSTTConn) Send(payload string) {
	c.SendWithSeq(payload, 0)
}

// SendWithSeq writes an event with the given hop seq (0 means the next one).
func (c *fakeSTTConn) SendWithSeq(payload string, seq int) {
	c.mu.Lock()
	switch {
	case seq == 0:
		c.seq++
		seq = c.seq
	case seq > 0:
		c.seq = seq
	}
	c.mu.Unlock()
	body := payload
	if seq > 0 { // a negative seq sends the event without one
		body = strings.Replace(payload, "{", fmt.Sprintf(`{"seq":%d,`, seq), 1)
	}
	if err := sttinfra.SendFrame(c.ws, sttinfra.StreamFrame{Kind: sttinfra.FrameText, Data: []byte(body)}); err != nil {
		// A test that closed the stream itself does not mind.
		return
	}
}

// SendRaw writes a frame as it is.
func (c *fakeSTTConn) SendRaw(kind sttinfra.FrameKind, data []byte) {
	_ = sttinfra.SendFrame(c.ws, sttinfra.StreamFrame{Kind: kind, Data: data})
}

// Next returns the next frame CORE sent to STT.
func (c *fakeSTTConn) Next(t *testing.T) sttinfra.StreamFrame {
	t.Helper()
	select {
	case frame := <-c.recv:
		return frame
	case <-time.After(relayTestWait):
		t.Fatal("CORE sent nothing more to the fake STT")
		return sttinfra.StreamFrame{}
	}
}

// NextControl returns the next text frame decoded as JSON.
func (c *fakeSTTConn) NextControl(t *testing.T) map[string]any {
	t.Helper()
	frame := c.Next(t)
	if frame.Kind != sttinfra.FrameText {
		t.Fatalf("expected a text frame, got kind %d (%d bytes)", frame.Kind, len(frame.Data))
	}
	var msg map[string]any
	if err := json.Unmarshal(frame.Data, &msg); err != nil {
		t.Fatalf("CORE sent invalid JSON upstream %q: %v", frame.Data, err)
	}
	return msg
}

// Quiet fails when CORE sends anything within d.
func (c *fakeSTTConn) Quiet(t *testing.T, d time.Duration) {
	t.Helper()
	select {
	case frame := <-c.recv:
		t.Fatalf("CORE sent a frame that must not have been forwarded: kind %d %q", frame.Kind, frame.Data)
	case <-time.After(d):
	}
}

// Drop ends the stream the way a crash does, with no close frame.
func (c *fakeSTTConn) Drop() {
	_ = c.ws.Close()
}

// Disconnected reports whether CORE closed its end.
func (c *fakeSTTConn) Disconnected() bool {
	select {
	case <-c.done:
		return true
	case <-time.After(relayTestWait):
		return false
	}
}

// contractSTT answers the controls CORE relays the way the stream contract says
// RenCrow_STT does (3.5 table, 3.7 d., e., i. and j.). It is not the segmenter:
// the test says when an utterance is announced and when silence closes it, and
// the model decides what a start, a stop or a cancel addresses and what STT
// sends back for it. That is the part a relay bug can change the outcome of:
//
//   - an id-less stop or cancel addresses the utterance in progress that STT
//     announced; with none, a stop is answered by a final that has no
//     utterance_id (empty_reason no_active_utterance) and a cancel by nothing,
//   - a stop or cancel that names an utterance addresses it only while it is the
//     one in progress (or its origin: the id the client gave); the id of a
//     finished utterance, or of another one, is ignored,
//   - the continuation after a split by silence is a new utterance named by STT
//     (srv-<n>) that does not inherit the id of the client.
type contractSTT struct {
	conn *fakeSTTConn

	mu     sync.Mutex
	active string            // the announced utterance in progress, "" when none
	origin map[string]string // utterance id -> the id the client gave it
	nextID int
}

func newContractSTT(conn *fakeSTTConn) *contractSTT {
	return &contractSTT{conn: conn, origin: map[string]string{}, nextID: 1}
}

// Announce opens an utterance by itself (STT's own start, or the continuation
// after a split) and sends its speech_start. id "" makes STT name it.
func (m *contractSTT) Announce(id string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.announceLocked(id, "")
}

func (m *contractSTT) announceLocked(id, clientID string) string {
	if id == "" || strings.HasPrefix(id, "srv-") && clientID != "" {
		m.nextID++
		id = fmt.Sprintf("srv-%d", m.nextID)
	}
	m.active = id
	m.origin[id] = clientID
	m.conn.Send(fmt.Sprintf(`{"type":"speech_start","utterance_id":%q,"offset_ms":0}`, id))
	return id
}

// CloseBySilence ends the utterance in progress by silence and sends its final.
func (m *contractSTT) CloseBySilence(text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeLocked("silence", text)
}

func (m *contractSTT) closeLocked(endReason, text string) {
	m.conn.Send(fmt.Sprintf(`{"type":"final","utterance_id":%q,"text":%q,"end_reason":%q,"audio_ms":900,"target_called":true}`, m.active, text, endReason))
	m.active = ""
}

// Active is the utterance in progress, "" when none.
func (m *contractSTT) Active() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active
}

func (m *contractSTT) names(id string) bool {
	return m.active != "" && (id == m.active || (m.origin[m.active] != "" && id == m.origin[m.active]))
}

// Handle applies one control CORE sent upstream.
func (m *contractSTT) Handle(msg map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, _ := msg["utterance_id"].(string)
	switch msg["type"] {
	case "utterance.start":
		if m.active != "" {
			m.closeLocked("restart", "")
		}
		m.announceLocked(id, id)
	case "utterance.stop":
		switch {
		case id == "" && m.active != "":
			m.closeLocked("stop", "stopped")
		case id == "":
			m.conn.Send(`{"type":"final","text":"","empty_reason":"no_active_utterance","end_reason":"stop","audio_ms":0,"target_called":false}`)
		case m.names(id):
			m.closeLocked("stop", "stopped")
		}
	case "utterance.cancel":
		if (id == "" && m.active != "") || m.names(id) {
			m.conn.Send(fmt.Sprintf(`{"type":"cancelled","utterance_id":%q,"target_called":false}`, m.active))
			m.active = ""
		}
	}
}

// relayClient is a /stt client: the Viewer, PORTAL or the probe.
type relayClient struct {
	t  *testing.T
	ws *websocket.Conn
}

func dialRelay(t *testing.T, url string) *relayClient {
	t.Helper()
	ws, err := websocket.Dial(url, "", "http://localhost/")
	if err != nil {
		t.Fatalf("dial /stt: %v", err)
	}
	client := &relayClient{t: t, ws: ws}
	t.Cleanup(func() { _ = ws.Close() })
	return client
}

func (c *relayClient) Text(msg string) {
	c.t.Helper()
	if err := sttinfra.SendFrame(c.ws, sttinfra.StreamFrame{Kind: sttinfra.FrameText, Data: []byte(msg)}); err != nil {
		c.t.Fatalf("send text: %v", err)
	}
}

func (c *relayClient) Control(msg map[string]any) {
	c.t.Helper()
	body, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	c.Text(string(body))
}

func (c *relayClient) Audio(pcm []byte) {
	c.t.Helper()
	if err := sttinfra.SendFrame(c.ws, sttinfra.StreamFrame{Kind: sttinfra.FrameBinary, Data: pcm}); err != nil {
		c.t.Fatalf("send audio: %v", err)
	}
}

// Event returns the next text frame decoded as JSON, or false when none comes
// within d or the connection ended.
func (c *relayClient) Event(d time.Duration) (map[string]any, bool) {
	c.t.Helper()
	_ = c.ws.SetReadDeadline(time.Now().Add(d))
	frame, err := sttinfra.ReceiveFrame(c.ws)
	if err != nil {
		return nil, false
	}
	var event map[string]any
	if err := json.Unmarshal(frame.Data, &event); err != nil {
		c.t.Fatalf("CORE sent a client frame that is not JSON %q: %v", frame.Data, err)
	}
	return event, true
}

// Expect returns the next event and fails unless it has the given type.
func (c *relayClient) Expect(typ string) map[string]any {
	c.t.Helper()
	event, ok := c.Event(relayTestWait)
	if !ok {
		c.t.Fatalf("no %q event arrived", typ)
	}
	if event["type"] != typ {
		c.t.Fatalf("expected a %q event, got %#v", typ, event)
	}
	return event
}

// ExpectReady consumes session_info and ready and returns ready.
func (c *relayClient) ExpectReady() map[string]any {
	c.t.Helper()
	c.Expect("session_info")
	return c.Expect("ready")
}

// Events reads n events.
func (c *relayClient) Events(n int) []map[string]any {
	c.t.Helper()
	out := make([]map[string]any, 0, n)
	for i := 0; i < n; i++ {
		event, ok := c.Event(relayTestWait)
		if !ok {
			c.t.Fatalf("only %d of %d events arrived: %v", i, n, out)
		}
		out = append(out, event)
	}
	return out
}

// NoEvent fails when an event arrives within d.
func (c *relayClient) NoEvent(d time.Duration) {
	c.t.Helper()
	if event, ok := c.Event(d); ok {
		c.t.Fatalf("an event that must not have been relayed arrived: %#v", event)
	}
}

// WaitClosed reads until the connection ends and returns the events it saw.
func (c *relayClient) WaitClosed() []map[string]any {
	c.t.Helper()
	var seen []map[string]any
	deadline := time.Now().Add(relayTestWait)
	for time.Now().Before(deadline) {
		_ = c.ws.SetReadDeadline(deadline)
		frame, err := sttinfra.ReceiveFrame(c.ws)
		if err != nil {
			if isTimeout(err) {
				break
			}
			return seen
		}
		var event map[string]any
		if json.Unmarshal(frame.Data, &event) == nil {
			seen = append(seen, event)
		}
	}
	c.t.Fatalf("CORE did not close the client connection; events seen: %v", seen)
	return nil
}

func isTimeout(err error) bool {
	var netErr interface{ Timeout() bool }
	return errors.As(err, &netErr) && netErr.Timeout()
}

// recordSink stands in for the recorder.
type recordSink struct {
	mu      sync.Mutex
	records []sttinfra.UtteranceRecord
	full    bool
}

func (s *recordSink) Submit(rec sttinfra.UtteranceRecord) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.full {
		return false
	}
	s.records = append(s.records, rec)
	return true
}

func (s *recordSink) All() []sttinfra.UtteranceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sttinfra.UtteranceRecord(nil), s.records...)
}

// WaitFor waits until at least n records exist.
func (s *recordSink) WaitFor(t *testing.T, n int) []sttinfra.UtteranceRecord {
	t.Helper()
	deadline := time.Now().Add(relayTestWait)
	for time.Now().Before(deadline) {
		if got := s.All(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("only %d of %d records were submitted: %+v", len(s.All()), n, s.All())
	return nil
}

// relayLog collects the relay's log lines.
type relayLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *relayLog) Logf(format string, args ...any) {
	l.mu.Lock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
	l.mu.Unlock()
}

func (l *relayLog) Text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// realStreamConfig is a relay config that dials the fake STT server.
func realStreamConfig(fake *fakeSTTServer, sink *recordSink, logs *relayLog) sttRelayConfig {
	cfg := sttRelayConfig{
		Enabled: true,
		Open: streamClientOpener(sttinfra.StreamClient{
			URL: fake.URL, DialTimeout: time.Second, OpenTimeout: 2 * time.Second, WriteTimeout: time.Second,
		}),
		RequestID: func() string { return "req-test" },
	}
	if sink != nil {
		cfg.Recorder = sink
	}
	if logs != nil {
		cfg.Logf = logs.Logf
	}
	return cfg
}

// serveRelay serves the relay at /stt and returns the client URL.
func serveRelay(t *testing.T, cfg sttRelayConfig) string {
	t.Helper()
	mux := http.NewServeMux()
	registerSTTRoutes(mux, newSTTRelayHandler(cfg))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http") + "/stt"
}

// pcmTone is n milliseconds of 16 kHz PCM16 with a fixed non-zero level.
func pcmTone(ms int) []byte {
	out := make([]byte, ms*32)
	for i := 0; i+1 < len(out); i += 2 {
		out[i], out[i+1] = 0x10, 0x01
	}
	return out
}

// testRelayClock is a clock moved by hand. Its tickers fire only when Tick is called.
type testRelayClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []chan time.Time
}

func newTestRelayClock() *testRelayClock {
	return &testRelayClock{now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
}

func (c *testRelayClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testRelayClock) NewTicker(time.Duration) (<-chan time.Time, func()) {
	ch := make(chan time.Time, 1)
	c.mu.Lock()
	c.tickers = append(c.tickers, ch)
	c.mu.Unlock()
	return ch, func() {}
}

// Advance moves the clock and delivers one tick.
func (c *testRelayClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	tickers := append([]chan time.Time(nil), c.tickers...)
	c.mu.Unlock()
	for _, ch := range tickers {
		select {
		case ch <- now:
		default:
		}
	}
}

// fakeUpstream is an upstream the test drives by hand.
type fakeUpstream struct {
	clock *testRelayClock

	mu       sync.Mutex
	sent     []sttinfra.StreamFrame
	pings    int
	last     time.Time
	closed   bool
	sendErr  error
	incoming chan upstreamItem
}

type upstreamItem struct {
	frame sttinfra.StreamFrame
	err   error
}

func newFakeUpstream(clock *testRelayClock) *fakeUpstream {
	return &fakeUpstream{clock: clock, last: clock.Now(), incoming: make(chan upstreamItem, 64)}
}

func (u *fakeUpstream) Send(frame sttinfra.StreamFrame) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.closed {
		return errors.New("closed")
	}
	if u.sendErr != nil {
		return u.sendErr
	}
	u.sent = append(u.sent, frame)
	return nil
}

func (u *fakeUpstream) Receive() (sttinfra.StreamFrame, error) {
	item, ok := <-u.incoming
	if !ok {
		return sttinfra.StreamFrame{}, errors.New("closed")
	}
	return item.frame, item.err
}

func (u *fakeUpstream) Ping() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.pings++
	return nil
}

func (u *fakeUpstream) LastReceived() time.Time {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.last
}

func (u *fakeUpstream) Close() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.closed {
		u.closed = true
		close(u.incoming)
	}
	return nil
}

// Touch counts as traffic from STT without delivering an event.
func (u *fakeUpstream) Touch() {
	u.mu.Lock()
	u.last = u.clock.Now()
	u.mu.Unlock()
}

// Deliver hands an event to CORE as if STT had sent it, and counts it as traffic.
func (u *fakeUpstream) Deliver(payload string) {
	u.mu.Lock()
	u.last = u.clock.Now()
	u.mu.Unlock()
	u.incoming <- upstreamItem{frame: sttinfra.StreamFrame{Kind: sttinfra.FrameText, Data: []byte(payload)}}
}

func (u *fakeUpstream) Pings() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.pings
}

func (u *fakeUpstream) IsClosed() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.closed
}

func (u *fakeUpstream) Sent() []sttinfra.StreamFrame {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]sttinfra.StreamFrame(nil), u.sent...)
}

// waitUntil polls cond for up to relayTestWait.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(relayTestWait)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// fixedOpener returns an opener that hands out the given upstream with a valid session_ready.
func fixedOpener(up *fakeUpstream, readyJSON string) sttUpstreamOpener {
	return func(ctx context.Context, sessionStart []byte) (sttUpstream, []byte, error) {
		return up, []byte(readyJSON), nil
	}
}

const testReadyJSON = `{"type":"session_ready","seq":1,"protocol":"stt.stream.v1","provider":"rencrow_stt","limits":{"max_utterance_ms":30000,"max_frame_bytes":65536,"queue_depth":8,"terminal_deadline_ms":75000}}`

// tcpTap forwards TCP connections to target and counts the bytes that flow from
// the target back to the client, so a test can see what the relay wrote even
// when the library writes it by itself (a pong).
type tcpTap struct {
	Addr       string
	fromTarget atomic.Int64
}

func startTCPTap(t *testing.T, target string) *tcpTap {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tap := &tcpTap{Addr: listener.Addr().String()}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", target)
			if err != nil {
				client.Close()
				return
			}
			go func() {
				_, _ = io.Copy(upstream, client)
				upstream.Close()
			}()
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := upstream.Read(buf)
					if n > 0 {
						tap.fromTarget.Add(int64(n))
						if _, werr := client.Write(buf[:n]); werr != nil {
							break
						}
					}
					if err != nil {
						break
					}
				}
				client.Close()
			}()
		}
	}()
	return tap
}
