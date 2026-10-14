package stt

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// streamTestClock is a clock the tests move by hand.
type streamTestClock struct{ nanos atomic.Int64 }

func newStreamTestClock() *streamTestClock {
	c := &streamTestClock{}
	c.nanos.Store(time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC).UnixNano())
	return c
}

func (c *streamTestClock) Now() time.Time          { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *streamTestClock) Advance(d time.Duration) { c.nanos.Add(int64(d)) }

// fakeStreamServer is a RenCrow_STT stream stand-in: a real WebSocket server on
// loopback, so the client is tested against the wire rather than a mock.
type fakeStreamServer struct {
	URL string

	mu      sync.Mutex
	origins []string
}

func (s *fakeStreamServer) Origins() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.origins...)
}

func startFakeStreamServer(t *testing.T, handler func(ws *websocket.Conn)) *fakeStreamServer {
	t.Helper()
	fake := &fakeStreamServer{}
	server := websocket.Server{
		Handshake: func(_ *websocket.Config, req *http.Request) error {
			fake.mu.Lock()
			fake.origins = append(fake.origins, req.Header.Get("Origin"))
			fake.mu.Unlock()
			return nil
		},
		Handler: websocket.Handler(handler),
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	fake.URL = "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/v1/audio/stream"
	return fake
}

func testStreamClient(url string) StreamClient {
	return StreamClient{URL: url, DialTimeout: time.Second, OpenTimeout: 2 * time.Second, WriteTimeout: time.Second}
}

const testSessionStart = `{"type":"session.start","protocol":"stt.stream.v1"}`
const testSessionReady = `{"type":"session_ready","seq":1,"protocol":"stt.stream.v1"}`

// echoReady answers the first frame with session_ready and then serves f.
func echoReady(f func(ws *websocket.Conn)) func(ws *websocket.Conn) {
	return func(ws *websocket.Conn) {
		if _, err := ReceiveFrame(ws); err != nil {
			return
		}
		if err := SendFrame(ws, StreamFrame{Kind: FrameText, Data: []byte(testSessionReady)}); err != nil {
			return
		}
		if f != nil {
			f(ws)
		}
	}
}

func TestStreamClientOpenSendsTheFixedOriginAndSessionStartAndReturnsSessionReady(t *testing.T) {
	var first atomic.Value
	fake := startFakeStreamServer(t, func(ws *websocket.Conn) {
		frame, err := ReceiveFrame(ws)
		if err != nil {
			return
		}
		first.Store(frame)
		_ = SendFrame(ws, StreamFrame{Kind: FrameText, Data: []byte(testSessionReady)})
		_, _ = ReceiveFrame(ws) // hold the connection until the client closes it
	})

	conn, ready, err := testStreamClient(fake.URL).Open(context.Background(), []byte(testSessionStart))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}
	defer conn.Close()

	if string(ready) != testSessionReady {
		t.Fatalf("session_ready = %q", ready)
	}
	got, _ := first.Load().(StreamFrame)
	if got.Kind != FrameText || string(got.Data) != testSessionStart {
		t.Fatalf("the first frame must be session.start as text: %#v", got)
	}
	if origins := fake.Origins(); len(origins) != 1 || origins[0] != "http://rencrow-core.invalid" {
		t.Fatalf("the stream accepts only the fixed Origin, got %v", origins)
	}
}

func TestStreamClientKeepsTheFrameKindInBothDirections(t *testing.T) {
	received := make(chan StreamFrame, 4)
	fake := startFakeStreamServer(t, echoReady(func(ws *websocket.Conn) {
		_ = SendFrame(ws, StreamFrame{Kind: FrameText, Data: []byte(`{"type":"speech_start"}`)})
		// A text frame whose body looks like PCM and a binary frame that looks like JSON:
		// the kind decides, never the content.
		_ = SendFrame(ws, StreamFrame{Kind: FrameBinary, Data: []byte(`{"type":"speech_start"}`)})
		for i := 0; i < 2; i++ {
			frame, err := ReceiveFrame(ws)
			if err != nil {
				return
			}
			received <- frame
		}
		_, _ = ReceiveFrame(ws)
	}))

	conn, _, err := testStreamClient(fake.URL).Open(context.Background(), []byte(testSessionStart))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	first, err := conn.Receive()
	if err != nil || first.Kind != FrameText {
		t.Fatalf("first = %#v err=%v", first, err)
	}
	second, err := conn.Receive()
	if err != nil || second.Kind != FrameBinary || string(second.Data) != `{"type":"speech_start"}` {
		t.Fatalf("a binary frame stays binary even when it looks like JSON: %#v err=%v", second, err)
	}

	if err := conn.Send(StreamFrame{Kind: FrameBinary, Data: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Send(StreamFrame{Kind: FrameText, Data: []byte(`{"type":"utterance.stop"}`)}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []StreamFrame{
		{Kind: FrameBinary, Data: []byte{1, 2, 3}},
		{Kind: FrameText, Data: []byte(`{"type":"utterance.stop"}`)},
	} {
		select {
		case got := <-received:
			if got.Kind != want.Kind || string(got.Data) != string(want.Data) {
				t.Fatalf("server got %#v, want %#v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the server did not receive the frame")
		}
	}
}

func TestStreamClientSendRejectsAFrameKindItCannotSend(t *testing.T) {
	fake := startFakeStreamServer(t, echoReady(func(ws *websocket.Conn) { _, _ = ReceiveFrame(ws) }))
	conn, _, err := testStreamClient(fake.URL).Open(context.Background(), []byte(testSessionStart))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Send(StreamFrame{Kind: FrameKind(77), Data: []byte("x")}); err == nil {
		t.Fatal("an unknown frame kind must not reach the wire")
	}
}

func TestStreamClientLastReceivedFollowsAnythingTheServerSendsIncludingPong(t *testing.T) {
	clock := newStreamTestClock()
	// The server only reads, so the only thing it ever sends after session_ready
	// is the pong the library writes for a ping.
	fake := startFakeStreamServer(t, echoReady(func(ws *websocket.Conn) {
		for {
			if _, err := ReceiveFrame(ws); err != nil {
				return
			}
		}
	}))
	client := testStreamClient(fake.URL)
	client.Now = clock.Now
	conn, _, err := client.Open(context.Background(), []byte(testSessionStart))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Nothing reads the connection here, but the pong must still be accounted
	// for: the library discards it, so liveness is measured on the bytes read.
	go func() {
		for {
			if _, err := conn.Receive(); err != nil {
				return
			}
		}
	}()
	opened := conn.LastReceived()
	clock.Advance(10 * time.Second)
	if err := conn.Ping(); err != nil {
		t.Fatalf("Ping() = %v", err)
	}
	want := clock.Now()
	deadline := time.Now().Add(2 * time.Second)
	for conn.LastReceived().Before(want) {
		if time.Now().After(deadline) {
			t.Fatalf("LastReceived stayed at %v (opened %v), the pong was not counted", conn.LastReceived(), opened)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStreamClientReportsTheHTTPStatusOfARefusedUpgrade(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusNotFound, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "refused", status)
			}))
			defer server.Close()
			url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/audio/stream"

			_, _, err := testStreamClient(url).Open(context.Background(), []byte(testSessionStart))

			var openErr *StreamOpenError
			if !errors.As(err, &openErr) {
				t.Fatalf("Open() = %v, want a StreamOpenError", err)
			}
			if openErr.Status != status || openErr.Stage != StreamStageHandshake {
				t.Fatalf("error = %+v, want status %d at the handshake", openErr, status)
			}
		})
	}
}

func TestStreamClientReportsAConnectionThatCannotBeMade(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	listener.Close() // nothing listens there any more

	_, _, err = testStreamClient("ws://"+addr+"/v1/audio/stream").Open(context.Background(), []byte(testSessionStart))
	var openErr *StreamOpenError
	if !errors.As(err, &openErr) || openErr.Stage != StreamStageDial || openErr.Status != 0 {
		t.Fatalf("Open() = %v, want a dial-stage StreamOpenError without a status", err)
	}
}

func TestStreamClientOpenGivesUpWhenTheServerNeverAnswersTheHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			defer conn.Close() // accepted, never answered
		}
	}()
	client := testStreamClient("ws://" + listener.Addr().String() + "/v1/audio/stream")
	client.OpenTimeout = 150 * time.Millisecond

	started := time.Now()
	_, _, err = client.Open(context.Background(), []byte(testSessionStart))
	if err == nil {
		t.Fatal("Open() succeeded against a server that never answers")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Open() took %v, the open timeout is 150ms", elapsed)
	}
}

func TestStreamClientOpenGivesUpWhenSessionReadyNeverComes(t *testing.T) {
	fake := startFakeStreamServer(t, func(ws *websocket.Conn) {
		_, _ = ReceiveFrame(ws) // reads session.start, says nothing
		_, _ = ReceiveFrame(ws)
	})
	client := testStreamClient(fake.URL)
	client.OpenTimeout = 150 * time.Millisecond

	started := time.Now()
	_, _, err := client.Open(context.Background(), []byte(testSessionStart))
	var openErr *StreamOpenError
	if !errors.As(err, &openErr) || openErr.Stage != StreamStageSessionReady {
		t.Fatalf("Open() = %v, want a session_ready-stage StreamOpenError", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Open() took %v, the open timeout is 150ms", elapsed)
	}
}

func TestStreamClientOpenRejectsAFirstFrameThatIsNotText(t *testing.T) {
	fake := startFakeStreamServer(t, func(ws *websocket.Conn) {
		_, _ = ReceiveFrame(ws)
		_ = SendFrame(ws, StreamFrame{Kind: FrameBinary, Data: []byte(testSessionReady)})
		_, _ = ReceiveFrame(ws)
	})
	_, _, err := testStreamClient(fake.URL).Open(context.Background(), []byte(testSessionStart))
	var openErr *StreamOpenError
	if !errors.As(err, &openErr) || openErr.Stage != StreamStageSessionReady {
		t.Fatalf("Open() = %v, want a session_ready-stage StreamOpenError", err)
	}
}

func TestStreamClientOpenStopsWhenTheContextIsCancelled(t *testing.T) {
	fake := startFakeStreamServer(t, func(ws *websocket.Conn) {
		_, _ = ReceiveFrame(ws)
		_, _ = ReceiveFrame(ws)
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	client := testStreamClient(fake.URL)
	client.OpenTimeout = 10 * time.Second

	started := time.Now()
	_, _, err := client.Open(ctx, []byte(testSessionStart))
	if err == nil {
		t.Fatal("Open() succeeded although the context was cancelled and session_ready never came")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("Open() ignored the cancelled context for %v", elapsed)
	}
}

func TestStreamClientRefusesAnUpstreamFrameOverTheLimit(t *testing.T) {
	fake := startFakeStreamServer(t, echoReady(func(ws *websocket.Conn) {
		_ = SendFrame(ws, StreamFrame{Kind: FrameText, Data: []byte(strings.Repeat("x", 4096))})
		_, _ = ReceiveFrame(ws)
	}))
	client := testStreamClient(fake.URL)
	client.MaxFrameBytes = 1024
	conn, _, err := client.Open(context.Background(), []byte(testSessionStart))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Receive(); !errors.Is(err, websocket.ErrFrameTooLarge) {
		t.Fatalf("Receive() = %v, want ErrFrameTooLarge", err)
	}
}

func TestStreamClientSendTimesOutWhenThePeerStopsReading(t *testing.T) {
	block := make(chan struct{})
	fake := startFakeStreamServer(t, echoReady(func(ws *websocket.Conn) { <-block }))
	defer close(block)
	client := testStreamClient(fake.URL)
	client.WriteTimeout = 200 * time.Millisecond
	conn, _, err := client.Open(context.Background(), []byte(testSessionStart))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	chunk := make([]byte, 64*1024)
	started := time.Now()
	for i := 0; i < 4096; i++ { // far more than the socket buffers hold
		if err := conn.Send(StreamFrame{Kind: FrameBinary, Data: chunk}); err != nil {
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("Send blocked for %v although the write timeout is 200ms", elapsed)
			}
			return
		}
	}
	t.Fatal("Send never failed against a peer that does not read")
}

func TestStreamClientCloseIsIdempotentAndUnblocksReceive(t *testing.T) {
	fake := startFakeStreamServer(t, echoReady(func(ws *websocket.Conn) { _, _ = ReceiveFrame(ws) }))
	conn, _, err := testStreamClient(fake.URL).Open(context.Background(), []byte(testSessionStart))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := conn.Receive()
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := conn.Close(); err != nil {
		t.Logf("first Close() = %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("a second Close() = %v, want nil", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Receive returned a frame from a closed connection")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock Receive")
	}
}
