package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunChatCommandOneShotPrintsResponse(t *testing.T) {
	eventsReady := make(chan struct{})
	sendSeen := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/viewer/events":
			if r.Header.Get("Last-Event-ID") != "9223372036854775807" {
				t.Errorf("unexpected Last-Event-ID: %s", r.Header.Get("Last-Event-ID"))
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("expected flushable response writer")
			}
			flusher.Flush()
			close(eventsReady)
			<-sendSeen
			_, _ = w.Write([]byte(`data: {"type":"message.received","from":"user","to":"mio","content":"hello","session_id":"viewer","channel":"viewer","chat_id":"viewer-user"}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"type":"agent.response","from":"mio","to":"user","content":"こんにちは","route":"CHAT","session_id":"viewer","channel":"viewer","chat_id":"viewer-user"}` + "\n\n"))
			flusher.Flush()
		case "/viewer/send":
			<-eventsReady
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode send body: %v", err)
			}
			if body["message"] != "hello" {
				t.Fatalf("unexpected message: %q", body["message"])
			}
			close(sendSeen)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := runChatCommand([]string{"--url", srv.URL, "--message", "hello", "--timeout", "2s"}, strings.NewReader(""), &out, &errOut, srv.Client())
	if code != 0 {
		t.Fatalf("runChatCommand code=%d stderr=%q stdout=%q", code, errOut.String(), out.String())
	}
	if !strings.Contains(out.String(), "user> hello") {
		t.Fatalf("expected echoed user event, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "mio> token/sec:") || !strings.Contains(out.String(), "こんにちは") {
		t.Fatalf("expected agent response, got: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
}

func TestRunChatCommandSendFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/viewer/events":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("expected flushable response writer")
			}
			flusher.Flush()
			<-r.Context().Done()
		case "/viewer/send":
			http.Error(w, "broken", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := runChatCommand([]string{"--url", srv.URL, "--message", "hello", "--timeout", "2s"}, strings.NewReader(""), &out, &errOut, srv.Client())
	if code != 1 {
		t.Fatalf("runChatCommand code=%d stderr=%q stdout=%q", code, errOut.String(), out.String())
	}
	if !strings.Contains(errOut.String(), "chat send failed") || !strings.Contains(errOut.String(), "HTTP 500") {
		t.Fatalf("expected send failure stderr, got: %q", errOut.String())
	}
}

func TestRunChatCommandInteractiveWaitsForResponseWithoutEcho(t *testing.T) {
	eventsReady := make(chan struct{})
	sendSeen := make(chan string, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/viewer/events":
			w.Header().Set("Content-Type", "text/event-stream")
			flusher, ok := w.(http.Flusher)
			if !ok {
				t.Fatal("expected flushable response writer")
			}
			flusher.Flush()
			close(eventsReady)
			message := <-sendSeen
			_, _ = w.Write([]byte(`data: {"type":"message.received","from":"user","content":"` + message + `"}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"type":"routing.decision","route":"CHAT","content":"confidence 70%"}` + "\n\n"))
			_, _ = w.Write([]byte(`data: {"type":"agent.response","from":"mio","content":"返答"}` + "\n\n"))
			flusher.Flush()
			<-r.Context().Done()
		case "/viewer/send":
			<-eventsReady
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode send body: %v", err)
			}
			sendSeen <- body["message"]
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var out bytes.Buffer
	var errOut bytes.Buffer
	code := runChatCommand([]string{"--url", srv.URL}, strings.NewReader("hello\n/quit\n"), &out, &errOut, srv.Client())
	if code != 0 {
		t.Fatalf("runChatCommand code=%d stderr=%q stdout=%q", code, errOut.String(), out.String())
	}
	if strings.Contains(out.String(), "user> hello") {
		t.Fatalf("interactive output must not echo message.received, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "route> CHAT confidence 70%") {
		t.Fatalf("expected route output, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "mio> token/sec:") || !strings.Contains(out.String(), "返答") {
		t.Fatalf("expected agent response, got: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
}

func TestParseChatCLISSEIgnoresHeartbeatAndParsesData(t *testing.T) {
	src := strings.NewReader(": heartbeat\n\nid: 1\ndata: {\"type\":\"agent.response\",\"from\":\"mio\",\"content\":\"ok\"}\n\n")
	events := make(chan chatCLIEvent, 1)

	if err := parseChatCLISSE(context.Background(), src, events); err != nil {
		t.Fatalf("parseChatCLISSE: %v", err)
	}
	ev := <-events
	if ev.Type != "agent.response" || ev.From != "mio" || ev.Content != "ok" {
		t.Fatalf("unexpected event: %+v", ev)
	}
}

func TestPrintChatCLIEventAlwaysShowsTokenPerSecondAfterAgentLabel(t *testing.T) {
	var out bytes.Buffer
	ok := printChatCLIEvent(&out, chatCLIEvent{
		Type:    "agent.response",
		From:    "mio",
		Content: "はろはろ",
	}, time.Now().Add(-2*time.Second))
	if !ok {
		t.Fatal("expected printable event")
	}
	if !strings.HasPrefix(out.String(), "mio> token/sec:") {
		t.Fatalf("expected token/sec immediately after mio label, got: %q", out.String())
	}
	if !strings.Contains(out.String(), "\nはろはろ\n") {
		t.Fatalf("expected response content on following line, got: %q", out.String())
	}
}

func TestParseChatCLIOptionsTrailingArgsBecomeMessage(t *testing.T) {
	opts, err := parseChatCLIOptions([]string{"--url", "http://example.test", "hello", "world"})
	if err != nil {
		t.Fatalf("parseChatCLIOptions: %v", err)
	}
	if opts.Message != "hello world" {
		t.Fatalf("unexpected message: %q", opts.Message)
	}
	if opts.BaseURL != "http://example.test" {
		t.Fatalf("unexpected base URL: %q", opts.BaseURL)
	}
}

func TestParseChatCLIOptionsUsesEnvironmentURL(t *testing.T) {
	t.Setenv(chatCLIBaseURLEnv, "https://ren.example.test")

	opts, err := parseChatCLIOptions([]string{"hello"})
	if err != nil {
		t.Fatalf("parseChatCLIOptions: %v", err)
	}
	if opts.BaseURL != "https://ren.example.test" {
		t.Fatalf("unexpected base URL: %q", opts.BaseURL)
	}
	if opts.Message != "hello" {
		t.Fatalf("unexpected message: %q", opts.Message)
	}
}

func TestShouldRunChatCLIOneShotForViewerStyleInputs(t *testing.T) {
	if !shouldRunChatCLIOneShot(chatCLIOptions{Attachments: []string{"photo.png"}}) {
		t.Fatal("attachment-only chat should run as one-shot like Viewer send")
	}
	if !shouldRunChatCLIOneShot(chatCLIOptions{AudioPath: "voice.wav"}) {
		t.Fatal("audio-only chat should run as one-shot after STT")
	}
	if !shouldRunChatCLIOneShot(chatCLIOptions{AudioDirectPath: "voice.wav"}) {
		t.Fatal("audio-direct chat should run as one-shot")
	}
	if shouldRunChatCLIOneShot(chatCLIOptions{}) {
		t.Fatal("empty chat should remain interactive")
	}
}

func TestParseChatCLIOptionsAcceptsViewerStyleInputs(t *testing.T) {
	opts, err := parseChatCLIOptions([]string{
		"--url", "http://example.test",
		"--audio", "voice.wav",
		"--image", "photo.png",
		"--video", "clip.mp4",
		"--attach", "note.txt",
		"--message", "見て",
	})
	if err != nil {
		t.Fatalf("parseChatCLIOptions: %v", err)
	}
	if opts.AudioPath != "voice.wav" {
		t.Fatalf("unexpected audio path: %q", opts.AudioPath)
	}
	want := []string{"photo.png", "clip.mp4", "note.txt"}
	if strings.Join(opts.Attachments, ",") != strings.Join(want, ",") {
		t.Fatalf("unexpected attachments: %#v", opts.Attachments)
	}
}

func TestSendChatCLIMessageUsesMultipartForAttachments(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "photo.png")
	if err := os.WriteFile(imagePath, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	var seenMessage string
	var seenFile string
	var seenContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/viewer/send" {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Fatalf("expected multipart content type, got %q", r.Header.Get("Content-Type"))
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Fatalf("parse multipart: %v", err)
		}
		seenMessage = r.FormValue("message")
		file, header, err := r.FormFile("attachments")
		if err != nil {
			t.Fatalf("missing attachment: %v", err)
		}
		defer file.Close()
		_, _ = io.ReadAll(file)
		seenFile = header.Filename
		seenContentType = header.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"attachment_count":1}`))
	}))
	defer srv.Close()

	err := sendChatCLIMessage(context.Background(), srv.Client(), srv.URL, chatCLISendPayload{
		Message:     "見て",
		Attachments: []string{imagePath},
	})
	if err != nil {
		t.Fatalf("sendChatCLIMessage: %v", err)
	}
	if seenMessage != "見て" || seenFile != "photo.png" {
		t.Fatalf("unexpected multipart payload: message=%q file=%q", seenMessage, seenFile)
	}
	if !strings.HasPrefix(seenContentType, "image/") {
		t.Fatalf("expected image content type, got %q", seenContentType)
	}
}

func TestTranscribeChatCLIAudioUsesViewerSTTChatInput(t *testing.T) {
	const wav = "RIFF$\x00\x00\x00WAVEfmt "
	dir := t.TempDir()
	audioPath := filepath.Join(dir, "voice.wav")
	if err := os.WriteFile(audioPath, []byte(wav), 0o644); err != nil {
		t.Fatalf("write audio: %v", err)
	}

	var seenPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		if r.URL.Path != "/stt/chat-input" {
			http.NotFound(w, r)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Fatalf("expected multipart content type, got %q", r.Header.Get("Content-Type"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"text":"音声からの入力です","provider":"local_stt"}`))
	}))
	defer srv.Close()

	text, err := transcribeChatCLIAudio(context.Background(), srv.Client(), srv.URL, audioPath)
	if err != nil {
		t.Fatalf("transcribeChatCLIAudio: %v", err)
	}
	if text != "音声からの入力です" || seenPath != "/stt/chat-input" {
		t.Fatalf("unexpected transcript/path: text=%q path=%q", text, seenPath)
	}
}
