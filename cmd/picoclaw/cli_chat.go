package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultChatCLIBaseURL = "http://127.0.0.1:18790"
const chatCLIBaseURLEnv = "RENCROW_CHAT_URL"

type chatCLIEvent struct {
	Seq       int64  `json:"seq,omitempty"`
	Type      string `json:"type"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
	Content   string `json:"content"`
	Route     string `json:"route,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Channel   string `json:"channel,omitempty"`
	ChatID    string `json:"chat_id,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

type chatCLIOptions struct {
	BaseURL         string
	Message         string
	Timeout         time.Duration
	AudioPath       string
	AudioDirectPath string
	Attachments     []string
}

type chatCLISendPayload struct {
	Message     string
	Attachments []string
}

type chatCLIStringList []string

func (l *chatCLIStringList) String() string {
	if l == nil {
		return ""
	}
	return strings.Join(*l, ",")
}

func (l *chatCLIStringList) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("path is required")
	}
	*l = append(*l, value)
	return nil
}

func cmdChat() {
	os.Exit(runChatCommand(os.Args[2:], os.Stdin, os.Stdout, os.Stderr, http.DefaultClient))
}

func runChatCommand(args []string, in io.Reader, out, errOut io.Writer, client *http.Client) int {
	opts, err := parseChatCLIOptions(args)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	if client == nil {
		client = http.DefaultClient
	}
	if shouldRunChatCLIOneShot(opts) {
		return runChatOneShot(opts, out, errOut, client)
	}
	return runChatInteractive(opts, in, out, errOut, client)
}

func shouldRunChatCLIOneShot(opts chatCLIOptions) bool {
	return strings.TrimSpace(opts.Message) != "" || strings.TrimSpace(opts.AudioPath) != "" || strings.TrimSpace(opts.AudioDirectPath) != "" || len(opts.Attachments) > 0
}

func parseChatCLIOptions(args []string) (chatCLIOptions, error) {
	opts := chatCLIOptions{
		BaseURL: defaultChatCLIBaseURL,
		Timeout: 5 * time.Minute,
	}
	if envURL := strings.TrimSpace(os.Getenv(chatCLIBaseURLEnv)); envURL != "" {
		opts.BaseURL = envURL
	}
	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&opts.BaseURL, "url", opts.BaseURL, "RenCrow server base URL")
	fs.StringVar(&opts.Message, "message", "", "send one message and wait for the first response event")
	fs.DurationVar(&opts.Timeout, "timeout", opts.Timeout, "one-shot wait timeout")
	fs.StringVar(&opts.AudioPath, "audio", "", "transcribe a WAV audio file via the same STT chat-input path used by Viewer")
	fs.StringVar(&opts.AudioDirectPath, "audio-direct", "", "send a WAV file directly to Chat LLM as input_audio (skip STT)")
	fs.Var((*chatCLIStringList)(&opts.Attachments), "attach", "attach a Viewer-supported file; may be repeated")
	fs.Var((*chatCLIStringList)(&opts.Attachments), "image", "attach an image file; may be repeated")
	fs.Var((*chatCLIStringList)(&opts.Attachments), "video", "attach a video file; may be repeated")
	if err := fs.Parse(args); err != nil {
		return opts, fmt.Errorf("usage: picoclaw chat [--url URL] [--message TEXT] [--audio WAV] [--audio-direct WAV] [--image PATH] [--video PATH] [--attach PATH] [--timeout 30s]")
	}
	if opts.Message == "" && len(fs.Args()) > 0 {
		opts.Message = strings.Join(fs.Args(), " ")
	}
	opts.BaseURL = strings.TrimRight(strings.TrimSpace(opts.BaseURL), "/")
	if opts.BaseURL == "" {
		return opts, fmt.Errorf("chat url is required")
	}
	if _, err := url.ParseRequestURI(opts.BaseURL); err != nil {
		return opts, fmt.Errorf("invalid chat url: %w", err)
	}
	if opts.Timeout <= 0 {
		return opts, fmt.Errorf("timeout must be positive")
	}
	opts.AudioPath = strings.TrimSpace(opts.AudioPath)
	opts.AudioDirectPath = strings.TrimSpace(opts.AudioDirectPath)
	if opts.AudioPath != "" && opts.AudioDirectPath != "" {
		return opts, fmt.Errorf("--audio and --audio-direct are mutually exclusive")
	}
	return opts, nil
}

func runChatOneShot(opts chatCLIOptions, out, errOut io.Writer, client *http.Client) int {
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()
	events := make(chan chatCLIEvent, 32)
	ready := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		done <- streamChatCLIEvents(ctx, client, opts.BaseURL, events, ready)
	}()
	if err := <-ready; err != nil {
		fmt.Fprintf(errOut, "chat events unavailable: %v\n", err)
		return 1
	}
	payload, err := buildChatCLISendPayload(ctx, client, opts)
	if err != nil {
		fmt.Fprintf(errOut, "chat input failed: %v\n", err)
		return 1
	}
	if err := sendChatCLIMessage(ctx, client, opts.BaseURL, payload); err != nil {
		fmt.Fprintf(errOut, "chat send failed: %v\n", err)
		return 1
	}
	sentAt := time.Now()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if !printChatCLIEvent(out, ev, sentAt) {
				continue
			}
			if isChatCLITerminalResponse(ev) {
				return 0
			}
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintf(errOut, "chat events stopped: %v\n", err)
				return 1
			}
		case <-ctx.Done():
			fmt.Fprintf(errOut, "chat response timeout: %v\n", ctx.Err())
			return 1
		}
	}
}

func runChatInteractive(opts chatCLIOptions, in io.Reader, out, errOut io.Writer, client *http.Client) int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := make(chan chatCLIEvent, 64)
	ready := make(chan error, 1)
	go func() {
		if err := streamChatCLIEvents(ctx, client, opts.BaseURL, events, ready); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintf(errOut, "chat events stopped: %v\n", err)
		}
	}()
	if err := <-ready; err != nil {
		fmt.Fprintf(errOut, "chat events unavailable: %v\n", err)
		return 1
	}
	fmt.Fprintln(out, "RenCrow terminal chat. Type /quit to exit.")
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "you> ")
		if !scanner.Scan() {
			break
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "/quit" || line == "/exit" {
			break
		}
		if err := sendChatCLIMessage(ctx, client, opts.BaseURL, chatCLISendPayload{Message: line}); err != nil {
			fmt.Fprintf(errOut, "chat send failed: %v\n", err)
			continue
		}
		sentAt := time.Now()
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					fmt.Fprintln(errOut, "chat events stopped")
					return 1
				}
				if ev.Type == "message.received" {
					continue
				}
				if !printChatCLIEvent(out, ev, sentAt) {
					continue
				}
				if isChatCLITerminalResponse(ev) {
					goto nextInput
				}
			case <-ctx.Done():
				fmt.Fprintf(errOut, "chat events stopped: %v\n", ctx.Err())
				return 1
			}
		}
	nextInput:
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(errOut, "chat input failed: %v\n", err)
		return 1
	}
	return 0
}

func buildChatCLISendPayload(ctx context.Context, client *http.Client, opts chatCLIOptions) (chatCLISendPayload, error) {
	message := strings.TrimSpace(opts.Message)
	attachments := append([]string(nil), opts.Attachments...)
	if opts.AudioDirectPath != "" {
		attachments = append(attachments, opts.AudioDirectPath)
		if message == "" {
			message = "この音声を聞いて、話している内容を日本語で要約し、最後に数字も書き出してください。"
		}
		return chatCLISendPayload{Message: message, Attachments: attachments}, nil
	}
	if opts.AudioPath != "" {
		text, err := transcribeChatCLIAudio(ctx, client, opts.BaseURL, opts.AudioPath)
		if err != nil {
			return chatCLISendPayload{}, fmt.Errorf("audio transcription failed: %w", err)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return chatCLISendPayload{}, fmt.Errorf("audio transcription returned empty text")
		}
		if message == "" {
			message = text
		} else {
			message = message + "\n\n" + text
		}
	}
	if message == "" && len(opts.Attachments) == 0 {
		return chatCLISendPayload{}, fmt.Errorf("message, audio, or attachment is required")
	}
	return chatCLISendPayload{Message: message, Attachments: opts.Attachments}, nil
}

func sendChatCLIMessage(ctx context.Context, client *http.Client, baseURL string, payload chatCLISendPayload) error {
	if len(payload.Attachments) > 0 {
		return sendChatCLIMultipartMessage(ctx, client, baseURL, payload)
	}
	body, err := json.Marshal(map[string]string{"message": payload.Message})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/viewer/send", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(text)))
	}
	return nil
}

func sendChatCLIMultipartMessage(ctx context.Context, client *http.Client, baseURL string, payload chatCLISendPayload) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("message", payload.Message); err != nil {
		return err
	}
	for _, path := range payload.Attachments {
		if err := addChatCLIMultipartFile(writer, "attachments", path); err != nil {
			_ = writer.Close()
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/viewer/send", &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(text)))
	}
	return nil
}

func transcribeChatCLIAudio(ctx context.Context, client *http.Client, baseURL, audioPath string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := addChatCLIMultipartFile(writer, "file", audioPath); err != nil {
		_ = writer.Close()
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/stt/chat-input", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(res.Body, 1024*1024))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decode STT response: %w", err)
	}
	return out.Text, nil
}

func addChatCLIMultipartFile(writer *multipart.Writer, fieldName, path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("%s path is required", fieldName)
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s %q: %w", fieldName, path, err)
	}
	defer file.Close()
	filename := filepath.Base(path)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeMultipartQuotes(fieldName), escapeMultipartQuotes(filename)))
	if contentType := chatCLIContentType(path); contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, file); err != nil {
		return fmt.Errorf("copy %s %q: %w", fieldName, path, err)
	}
	return nil
}

func chatCLIContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext == "" {
		return "application/octet-stream"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	switch ext {
	case ".m4v":
		return "video/x-m4v"
	case ".md":
		return "text/markdown; charset=utf-8"
	case ".yaml", ".yml":
		return "application/yaml"
	default:
		return "application/octet-stream"
	}
}

func escapeMultipartQuotes(s string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(s)
}

func streamChatCLIEvents(ctx context.Context, client *http.Client, baseURL string, events chan<- chatCLIEvent, ready chan<- error) error {
	defer close(events)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/viewer/events", nil)
	if err != nil {
		ready <- err
		return err
	}
	req.Header.Set("Last-Event-ID", "9223372036854775807")
	res, err := client.Do(req)
	if err != nil {
		ready <- err
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		text, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		err := fmt.Errorf("HTTP %d: %s", res.StatusCode, strings.TrimSpace(string(text)))
		ready <- err
		return err
	}
	ready <- nil
	return parseChatCLISSE(ctx, res.Body, events)
}

func parseChatCLISSE(ctx context.Context, src io.Reader, events chan<- chatCLIEvent) error {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var data strings.Builder
	flush := func() error {
		raw := strings.TrimSpace(data.String())
		data.Reset()
		if raw == "" {
			return nil
		}
		var ev chatCLIEvent
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return nil
		}
		select {
		case events <- ev:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") || strings.HasPrefix(line, "id:") || strings.HasPrefix(line, "event:") {
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := flush(); err != nil {
		return err
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return ctx.Err()
}

func printChatCLIEvent(out io.Writer, ev chatCLIEvent, sentAt time.Time) bool {
	switch ev.Type {
	case "message.received":
		fmt.Fprintf(out, "user> %s\n", ev.Content)
	case "routing.decision":
		fmt.Fprintf(out, "route> %s %s\n", strings.TrimSpace(ev.Route), strings.TrimSpace(ev.Content))
	case "agent.response":
		from := strings.TrimSpace(ev.From)
		if from == "" {
			from = "agent"
		}
		fmt.Fprintf(out, "%s> token/sec: %.1f\n%s\n", from, estimateChatCLITokensPerSecond(ev.Content, sentAt), ev.Content)
	case "agent.error", "mailbox.error", "worker.classified_failure":
		fmt.Fprintf(out, "error> %s\n", ev.Content)
	default:
		return false
	}
	return true
}

func estimateChatCLITokensPerSecond(content string, sentAt time.Time) float64 {
	elapsed := time.Since(sentAt).Seconds()
	if elapsed < 0.001 {
		elapsed = 0.001
	}
	tokens := estimateChatCLIOutputTokens(content)
	if tokens < 1 {
		tokens = 1
	}
	return float64(tokens) / elapsed
}

func estimateChatCLIOutputTokens(content string) int {
	content = strings.TrimSpace(content)
	if content == "" {
		return 1
	}
	tokens := 0
	inASCIIWord := false
	for _, r := range content {
		if r <= 127 && (r == '_' || r == '-' || r == '\'' || r == '.' || r == '/' || r == ':' || r == '@' || r == '#' || r == '$' || r == '%' || r == '&' || r == '+' || r == '=' || r == '?' || r == '!' || r == ',' || r == ';' || r == '(' || r == ')' || r == '[' || r == ']' || r == '{' || r == '}' || r == '"' || r == '`') {
			if inASCIIWord {
				inASCIIWord = false
			}
			continue
		}
		if r <= 127 {
			if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
				inASCIIWord = false
				continue
			}
			if !inASCIIWord {
				tokens++
				inASCIIWord = true
			}
			continue
		}
		inASCIIWord = false
		tokens++
	}
	if tokens < 1 {
		return 1
	}
	return tokens
}

func isChatCLITerminalResponse(ev chatCLIEvent) bool {
	return ev.Type == "agent.response" || ev.Type == "agent.error" || ev.Type == "mailbox.error" || ev.Type == "worker.classified_failure"
}
