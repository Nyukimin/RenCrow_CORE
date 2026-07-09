package slack

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	adapterchannels "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels"
	appattachment "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	channelapp "github.com/Nyukimin/RenCrow_CORE/internal/application/channel"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
)

type AttachmentSaver interface {
	SaveAll(ctx context.Context, files []appattachment.IncomingFile) ([]domainattachment.Attachment, error)
}

// Adapter handles Slack Events API webhook and outbound sends.
type Adapter struct {
	botToken        string
	signingSecret   string
	orchestrator    orchestrator.Orchestrator
	httpClient      *http.Client
	apiBaseURL      string
	attachmentSaver AttachmentSaver
}

func NewAdapter(botToken, signingSecret string, orch ...orchestrator.Orchestrator) *Adapter {
	var o orchestrator.Orchestrator
	if len(orch) > 0 {
		o = orch[0]
	}
	return &Adapter{
		botToken:      botToken,
		signingSecret: signingSecret,
		orchestrator:  o,
		httpClient:    &http.Client{Timeout: 10 * time.Second},
		apiBaseURL:    "https://slack.com/api",
	}
}

func (a *Adapter) Name() string { return "slack" }

func (a *Adapter) SetHTTPClient(c *http.Client) {
	if c != nil {
		a.httpClient = c
	}
}

func (a *Adapter) SetAPIBaseURL(url string) {
	if url != "" {
		a.apiBaseURL = url
	}
}

func (a *Adapter) SetAttachmentSaver(saver AttachmentSaver) {
	a.attachmentSaver = saver
}

func (a *Adapter) Send(ctx context.Context, chatID, text string) error {
	if a.botToken == "" {
		return fmt.Errorf("slack bot token is not configured")
	}
	if chatID == "" {
		return fmt.Errorf("chatID is required")
	}
	payload := map[string]any{"channel": chatID, "text": text}
	b, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/chat.postMessage", a.apiBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.botToken)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return fmt.Errorf("slack postMessage failed: status=%d: %s", resp.StatusCode, detail)
		}
		return fmt.Errorf("slack postMessage failed: status=%d", resp.StatusCode)
	}
	return nil
}

func (a *Adapter) Probe(ctx context.Context) error {
	if a.botToken == "" {
		return fmt.Errorf("slack bot token is not configured")
	}
	url := fmt.Sprintf("%s/auth.test", a.apiBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("")))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.botToken)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return fmt.Errorf("slack auth.test failed: status=%d: %s", resp.StatusCode, detail)
		}
		return fmt.Errorf("slack auth.test failed: status=%d", resp.StatusCode)
	}
	return nil
}

func (a *Adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if a.orchestrator == nil {
		http.Error(w, "orchestrator is not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	if a.signingSecret != "" && !a.verifySignature(r, body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	var ev EventEnvelope
	if err := json.Unmarshal(body, &ev); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	if ev.Type == "url_verification" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"challenge": ev.Challenge})
		return
	}
	normalized, ok := a.NormalizeEvent(ev, body)
	if !ok {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	attachments, err := a.attachmentsForEvent(r.Context(), ev.Event)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	resp, err := a.orchestrator.ProcessMessage(r.Context(), orchestrator.ProcessMessageRequest{
		SessionID:   channelapp.BuildSessionID(time.Now().UTC(), "slack", normalized.ChatID),
		Channel:     "slack",
		ChatID:      normalized.UserID,
		UserMessage: normalized.Text,
		Attachments: attachments,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Send(r.Context(), normalized.ChatID, resp.Response); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// NormalizeEvent converts Slack event payload into channel-agnostic event.
// It returns false when the event should be ignored.
func (a *Adapter) NormalizeEvent(ev EventEnvelope, raw []byte) (adapterchannels.ChannelEvent, bool) {
	if ev.Event.Channel == "" {
		return adapterchannels.ChannelEvent{}, false
	}
	if ev.Event.BotID != "" || (ev.Event.Subtype != "" && ev.Event.Subtype != "file_share") {
		return adapterchannels.ChannelEvent{}, false
	}
	if ev.Event.Type != "message" && ev.Event.Type != "app_mention" {
		return adapterchannels.ChannelEvent{}, false
	}
	text := normalizeSlackText(ev.Event.Text)
	if strings.TrimSpace(text) == "" && len(ev.Event.Files) == 0 {
		return adapterchannels.ChannelEvent{}, false
	}
	userID := ev.Event.User
	if userID == "" {
		userID = "slack-user"
	}
	return adapterchannels.ChannelEvent{
		Channel:   "slack",
		ChatID:    ev.Event.Channel,
		UserID:    userID,
		MessageID: ev.Event.ClientMsgID,
		Text:      text,
		Timestamp: time.Now().UTC(),
		Raw:       raw,
	}, true
}

func (a *Adapter) attachmentsForEvent(ctx context.Context, event EventInner) ([]domainattachment.Attachment, error) {
	if len(event.Files) == 0 {
		return nil, nil
	}
	if a.attachmentSaver == nil {
		return nil, fmt.Errorf("slack attachment saver is nil")
	}
	files := make([]appattachment.IncomingFile, 0, len(event.Files))
	for _, file := range event.Files {
		url := firstNonEmptySlack(file.URLPrivateDownload, file.URLPrivate)
		if strings.TrimSpace(url) == "" {
			return nil, fmt.Errorf("slack file download url is empty")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		if a.botToken != "" {
			req.Header.Set("Authorization", "Bearer "+a.botToken)
		}
		resp, err := a.httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			detail := strings.TrimSpace(string(body))
			if detail != "" {
				return nil, fmt.Errorf("slack file download failed: status=%d: %s", resp.StatusCode, detail)
			}
			return nil, fmt.Errorf("slack file download failed: status=%d", resp.StatusCode)
		}
		contentType := firstNonEmptySlack(file.MimeType, resp.Header.Get("Content-Type"))
		files = append(files, appattachment.IncomingFile{
			Filename:    firstNonEmptySlack(file.Name, file.Title, file.ID),
			ContentType: contentType,
			SizeBytes:   file.Size,
			Reader:      resp.Body,
		})
	}
	return a.attachmentSaver.SaveAll(ctx, files)
}

func firstNonEmptySlack(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeSlackText(text string) string {
	parts := strings.Fields(text)
	filtered := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.HasPrefix(p, "<@") && strings.HasSuffix(p, ">") {
			continue
		}
		filtered = append(filtered, p)
	}
	return strings.TrimSpace(strings.Join(filtered, " "))
}

func (a *Adapter) verifySignature(r *http.Request, body []byte) bool {
	ts := r.Header.Get("X-Slack-Request-Timestamp")
	sig := r.Header.Get("X-Slack-Signature")
	if ts == "" || sig == "" {
		return false
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if delta := time.Now().Unix() - sec; delta > 300 || delta < -300 {
		return false
	}
	base := "v0:" + ts + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(a.signingSecret))
	_, _ = mac.Write([]byte(base))
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(expected)), []byte(strings.ToLower(sig)))
}

// Slack Events API payloads.
type EventEnvelope struct {
	Type      string     `json:"type"`
	Challenge string     `json:"challenge,omitempty"`
	Event     EventInner `json:"event"`
}

type EventInner struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype,omitempty"`
	Text        string `json:"text"`
	User        string `json:"user"`
	BotID       string `json:"bot_id,omitempty"`
	Channel     string `json:"channel"`
	ClientMsgID string `json:"client_msg_id,omitempty"`
	Files       []File `json:"files,omitempty"`
}

type File struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Title              string `json:"title"`
	MimeType           string `json:"mimetype"`
	Size               int64  `json:"size"`
	URLPrivate         string `json:"url_private"`
	URLPrivateDownload string `json:"url_private_download"`
}
