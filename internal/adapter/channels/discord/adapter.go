package discord

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	adapterchannels "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/channels"
	appattachment "github.com/Nyukimin/picoclaw_multiLLM/internal/application/attachment"
	channelapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/channel"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	domainattachment "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/attachment"
)

type AttachmentSaver interface {
	SaveAll(ctx context.Context, files []appattachment.IncomingFile) ([]domainattachment.Attachment, error)
}

// Adapter handles Discord relay webhook and outbound sends.
type Adapter struct {
	botToken        string
	publicKeyHex    string
	orchestrator    orchestrator.Orchestrator
	httpClient      *http.Client
	apiBaseURL      string
	attachmentSaver AttachmentSaver
}

func NewAdapter(botToken string, orch ...orchestrator.Orchestrator) *Adapter {
	var o orchestrator.Orchestrator
	if len(orch) > 0 {
		o = orch[0]
	}
	return &Adapter{
		botToken:     botToken,
		orchestrator: o,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		apiBaseURL:   "https://discord.com/api/v10",
	}
}

func (a *Adapter) Name() string { return "discord" }

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

func (a *Adapter) SetPublicKeyHex(publicKeyHex string) {
	a.publicKeyHex = publicKeyHex
}

func (a *Adapter) SetAttachmentSaver(saver AttachmentSaver) {
	a.attachmentSaver = saver
}

func (a *Adapter) Send(ctx context.Context, chatID, text string) error {
	if a.botToken == "" {
		return fmt.Errorf("discord bot token is not configured")
	}
	if chatID == "" {
		return fmt.Errorf("chatID is required")
	}
	payload := map[string]any{"content": text}
	b, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/channels/%s/messages", a.apiBaseURL, chatID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bot "+a.botToken)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return fmt.Errorf("discord send message failed: status=%d: %s", resp.StatusCode, detail)
		}
		return fmt.Errorf("discord send message failed: status=%d", resp.StatusCode)
	}
	return nil
}

func (a *Adapter) Probe(ctx context.Context) error {
	if a.botToken == "" {
		return fmt.Errorf("discord bot token is not configured")
	}
	url := fmt.Sprintf("%s/users/@me", a.apiBaseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+a.botToken)
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return fmt.Errorf("discord probe failed: status=%d: %s", resp.StatusCode, detail)
		}
		return fmt.Errorf("discord probe failed: status=%d", resp.StatusCode)
	}
	return nil
}

// ServeHTTP accepts both:
// 1) relay payload {"channel_id":"...","author_id":"...","content":"..."}
// 2) Discord interactions payload (PING / APPLICATION_COMMAND)
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
	if a.publicKeyHex != "" && !a.verifySignature(r, body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	var inter Interaction
	if err := json.Unmarshal(body, &inter); err == nil && inter.Type > 0 {
		if inter.Type == 1 {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"type": 1})
			return
		}
		if inter.Type == 2 {
			event, ok := a.NormalizeInteraction(inter, body)
			if !ok {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"ok":true}`))
				return
			}
			resp, err := a.processChannelEvent(r.Context(), event, nil)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": 4,
				"data": map[string]any{"content": resp.Response},
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}

	var p RelayPayload
	if err := json.Unmarshal(body, &p); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	event, ok := a.NormalizeRelayPayload(p, body)
	if !ok {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	attachments, err := a.attachmentsForRelayPayload(r.Context(), p)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	resp, err := a.processChannelEvent(r.Context(), event, attachments)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Send(r.Context(), event.ChatID, resp.Response); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (a *Adapter) verifySignature(r *http.Request, body []byte) bool {
	sigHex := r.Header.Get("X-Signature-Ed25519")
	timestamp := r.Header.Get("X-Signature-Timestamp")
	if sigHex == "" || timestamp == "" {
		return false
	}
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return false
	}
	pub, err := hex.DecodeString(a.publicKeyHex)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return false
	}
	message := append([]byte(timestamp), body...)
	return ed25519.Verify(ed25519.PublicKey(pub), message, sig)
}

func (a *Adapter) processChannelEvent(ctx context.Context, event adapterchannels.ChannelEvent, attachments []domainattachment.Attachment) (orchestrator.ProcessMessageResponse, error) {
	return a.orchestrator.ProcessMessage(ctx, orchestrator.ProcessMessageRequest{
		SessionID:   channelapp.BuildSessionID(time.Now().UTC(), "discord", event.ChatID),
		Channel:     "discord",
		ChatID:      event.UserID,
		UserMessage: event.Text,
		Attachments: attachments,
	})
}

// RelayPayload is a normalized Discord event payload for webhook relay.
type RelayPayload struct {
	ChannelID   string            `json:"channel_id"`
	AuthorID    string            `json:"author_id"`
	Content     string            `json:"content"`
	Attachments []RelayAttachment `json:"attachments,omitempty"`
}

type RelayAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
}

// NormalizeRelayPayload converts relay payload into channel event.
func (a *Adapter) NormalizeRelayPayload(p RelayPayload, raw []byte) (adapterchannels.ChannelEvent, bool) {
	if strings.TrimSpace(p.ChannelID) == "" || (strings.TrimSpace(p.Content) == "" && len(p.Attachments) == 0) {
		return adapterchannels.ChannelEvent{}, false
	}
	userID := p.AuthorID
	if userID == "" {
		userID = "discord-user"
	}
	return adapterchannels.ChannelEvent{
		Channel:   "discord",
		ChatID:    p.ChannelID,
		UserID:    userID,
		Text:      strings.TrimSpace(p.Content),
		Timestamp: time.Now().UTC(),
		Raw:       raw,
	}, true
}

func (a *Adapter) attachmentsForRelayPayload(ctx context.Context, payload RelayPayload) ([]domainattachment.Attachment, error) {
	if len(payload.Attachments) == 0 {
		return nil, nil
	}
	if a.attachmentSaver == nil {
		return nil, fmt.Errorf("discord attachment saver is nil")
	}
	files := make([]appattachment.IncomingFile, 0, len(payload.Attachments))
	for _, attachment := range payload.Attachments {
		if strings.TrimSpace(attachment.URL) == "" {
			return nil, fmt.Errorf("discord attachment url is empty")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, attachment.URL, nil)
		if err != nil {
			return nil, err
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
				return nil, fmt.Errorf("discord attachment download failed: status=%d: %s", resp.StatusCode, detail)
			}
			return nil, fmt.Errorf("discord attachment download failed: status=%d", resp.StatusCode)
		}
		filename := strings.TrimSpace(attachment.Filename)
		if filename == "" {
			filename = strings.TrimSpace(attachment.ID)
		}
		files = append(files, appattachment.IncomingFile{
			Filename:    filename,
			ContentType: firstNonEmptyDiscord(attachment.ContentType, resp.Header.Get("Content-Type")),
			SizeBytes:   attachment.Size,
			Reader:      resp.Body,
		})
	}
	return a.attachmentSaver.SaveAll(ctx, files)
}

func firstNonEmptyDiscord(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// Interaction is Discord interactions payload.
type Interaction struct {
	Type      int                     `json:"type"`
	ChannelID string                  `json:"channel_id"`
	User      *InteractionUser        `json:"user,omitempty"`
	Member    *InteractionMember      `json:"member,omitempty"`
	Data      *InteractionCommandData `json:"data,omitempty"`
}

type InteractionMember struct {
	User *InteractionUser `json:"user,omitempty"`
}

type InteractionUser struct {
	ID string `json:"id"`
}

type InteractionCommandData struct {
	Name    string              `json:"name"`
	Options []InteractionOption `json:"options,omitempty"`
}

type InteractionOption struct {
	Name  string `json:"name"`
	Value any    `json:"value,omitempty"`
}

func (i Interaction) UserID() string {
	if i.Member != nil && i.Member.User != nil && i.Member.User.ID != "" {
		return i.Member.User.ID
	}
	if i.User != nil {
		return i.User.ID
	}
	return ""
}

func (i Interaction) CommandText() string {
	if i.Data == nil || i.Data.Name == "" {
		return ""
	}
	msg := "/" + i.Data.Name
	for _, opt := range i.Data.Options {
		if opt.Value != nil {
			msg += " " + fmt.Sprintf("%v", opt.Value)
		}
	}
	return msg
}

// NormalizeInteraction converts Discord interaction payload into channel event.
func (a *Adapter) NormalizeInteraction(i Interaction, raw []byte) (adapterchannels.ChannelEvent, bool) {
	msg := strings.TrimSpace(i.CommandText())
	if msg == "" {
		return adapterchannels.ChannelEvent{}, false
	}
	userID := i.UserID()
	if userID == "" {
		userID = "discord-user"
	}
	channelID := i.ChannelID
	if channelID == "" {
		channelID = userID
	}
	return adapterchannels.ChannelEvent{
		Channel:   "discord",
		ChatID:    channelID,
		UserID:    userID,
		Text:      msg,
		Timestamp: time.Now().UTC(),
		Raw:       raw,
	}, true
}
