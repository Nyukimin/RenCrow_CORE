package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	appattachment "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	channelapp "github.com/Nyukimin/RenCrow_CORE/internal/application/channel"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
)

type AttachmentSaver interface {
	SaveAll(ctx context.Context, files []appattachment.IncomingFile) ([]domainattachment.Attachment, error)
}

// Adapter handles Telegram webhook and outbound sends.
type Adapter struct {
	botToken        string
	webhookSecret   string
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
		apiBaseURL:   "https://api.telegram.org",
	}
}

func (a *Adapter) Name() string { return "telegram" }

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

func (a *Adapter) SetWebhookSecret(secret string) {
	a.webhookSecret = secret
}

func (a *Adapter) SetAttachmentSaver(saver AttachmentSaver) {
	a.attachmentSaver = saver
}

func (a *Adapter) Send(ctx context.Context, chatID, text string) error {
	if a.botToken == "" {
		return fmt.Errorf("telegram bot token is not configured")
	}
	if chatID == "" {
		return fmt.Errorf("chatID is required")
	}
	payload := map[string]any{
		"chat_id": chatID,
		"text":    text,
	}
	b, _ := json.Marshal(payload)
	url := fmt.Sprintf("%s/bot%s/sendMessage", a.apiBaseURL, a.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return fmt.Errorf("telegram sendMessage failed: status=%d: %s", resp.StatusCode, detail)
		}
		return fmt.Errorf("telegram sendMessage failed: status=%d", resp.StatusCode)
	}
	return nil
}

func (a *Adapter) Probe(ctx context.Context) error {
	if a.botToken == "" {
		return fmt.Errorf("telegram bot token is not configured")
	}
	url := fmt.Sprintf("%s/bot%s/getMe", a.apiBaseURL, a.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return fmt.Errorf("telegram getMe failed: status=%d: %s", resp.StatusCode, detail)
		}
		return fmt.Errorf("telegram getMe failed: status=%d", resp.StatusCode)
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
	if a.webhookSecret != "" {
		if r.Header.Get("X-Telegram-Bot-Api-Secret-Token") != a.webhookSecret {
			http.Error(w, "invalid secret token", http.StatusUnauthorized)
			return
		}
	}
	var update Update
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if update.Message == nil || (strings.TrimSpace(update.Message.Text) == "" && strings.TrimSpace(update.Message.Caption) == "" && !update.Message.hasFile()) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}
	attachments, err := a.attachmentsForMessage(r.Context(), *update.Message)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	text := strings.TrimSpace(update.Message.Text)
	if text == "" {
		text = strings.TrimSpace(update.Message.Caption)
	}

	req := orchestrator.ProcessMessageRequest{
		SessionID:   channelapp.BuildSessionID(time.Now().UTC(), "telegram", strconv.FormatInt(update.Message.Chat.ID, 10)),
		Channel:     "telegram",
		ChatID:      strconv.FormatInt(update.Message.Chat.ID, 10),
		UserMessage: text,
		Attachments: attachments,
	}
	resp, err := a.orchestrator.ProcessMessage(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := a.Send(r.Context(), strconv.FormatInt(update.Message.Chat.ID, 10), resp.Response); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// Telegram webhook payload
type Update struct {
	UpdateID int64          `json:"update_id"`
	Message  *UpdateMessage `json:"message,omitempty"`
}

type UpdateMessage struct {
	MessageID int64       `json:"message_id"`
	Text      string      `json:"text"`
	Caption   string      `json:"caption,omitempty"`
	Chat      UpdateChat  `json:"chat"`
	From      UpdateUser  `json:"from"`
	Document  *Document   `json:"document,omitempty"`
	Photo     []PhotoSize `json:"photo,omitempty"`
}

type UpdateChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type UpdateUser struct {
	ID int64 `json:"id"`
}

type Document struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileName     string `json:"file_name,omitempty"`
	MimeType     string `json:"mime_type,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id,omitempty"`
	FileSize     int64  `json:"file_size,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
}

func (m UpdateMessage) hasFile() bool {
	return m.Document != nil || len(m.Photo) > 0
}

func (a *Adapter) attachmentsForMessage(ctx context.Context, message UpdateMessage) ([]domainattachment.Attachment, error) {
	if !message.hasFile() {
		return nil, nil
	}
	if a.attachmentSaver == nil {
		return nil, fmt.Errorf("telegram attachment saver is nil")
	}
	files := make([]appattachment.IncomingFile, 0, 1+len(message.Photo))
	if message.Document != nil {
		file, err := a.downloadTelegramFile(ctx, message.Document.FileID, firstNonEmptyTelegram(message.Document.FileName, message.Document.FileUniqueID, message.Document.FileID), message.Document.MimeType, message.Document.FileSize)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	if len(message.Photo) > 0 {
		photo := largestPhoto(message.Photo)
		filename := firstNonEmptyTelegram(photo.FileUniqueID, photo.FileID) + ".jpg"
		file, err := a.downloadTelegramFile(ctx, photo.FileID, filename, "image/jpeg", photo.FileSize)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return a.attachmentSaver.SaveAll(ctx, files)
}

func (a *Adapter) downloadTelegramFile(ctx context.Context, fileID, filename, contentType string, size int64) (appattachment.IncomingFile, error) {
	fileID = strings.TrimSpace(fileID)
	if fileID == "" {
		return appattachment.IncomingFile{}, fmt.Errorf("telegram file_id is empty")
	}
	filePath, fileSize, err := a.getTelegramFilePath(ctx, fileID)
	if err != nil {
		return appattachment.IncomingFile{}, err
	}
	if size <= 0 {
		size = fileSize
	}
	downloadURL := fmt.Sprintf("%s/file/bot%s/%s", strings.TrimRight(a.apiBaseURL, "/"), a.botToken, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return appattachment.IncomingFile{}, err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return appattachment.IncomingFile{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		detail := strings.TrimSpace(string(body))
		if detail != "" {
			return appattachment.IncomingFile{}, fmt.Errorf("telegram file download failed: status=%d: %s", resp.StatusCode, detail)
		}
		return appattachment.IncomingFile{}, fmt.Errorf("telegram file download failed: status=%d", resp.StatusCode)
	}
	return appattachment.IncomingFile{
		Filename:    filename,
		ContentType: firstNonEmptyTelegram(contentType, resp.Header.Get("Content-Type")),
		SizeBytes:   size,
		Reader:      resp.Body,
	}, nil
}

func (a *Adapter) getTelegramFilePath(ctx context.Context, fileID string) (string, int64, error) {
	endpoint := fmt.Sprintf("%s/bot%s/getFile?file_id=%s", strings.TrimRight(a.apiBaseURL, "/"), a.botToken, url.QueryEscape(fileID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if detail := strings.TrimSpace(string(body)); detail != "" {
			return "", 0, fmt.Errorf("telegram getFile failed: status=%d: %s", resp.StatusCode, detail)
		}
		return "", 0, fmt.Errorf("telegram getFile failed: status=%d", resp.StatusCode)
	}
	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			FilePath string `json:"file_path"`
			FileSize int64  `json:"file_size"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", 0, err
	}
	if !payload.OK || strings.TrimSpace(payload.Result.FilePath) == "" {
		return "", 0, fmt.Errorf("telegram getFile did not return file_path")
	}
	return payload.Result.FilePath, payload.Result.FileSize, nil
}

func largestPhoto(photos []PhotoSize) PhotoSize {
	best := photos[0]
	for _, photo := range photos[1:] {
		if photo.FileSize > best.FileSize || (photo.FileSize == best.FileSize && photo.Width*photo.Height > best.Width*best.Height) {
			best = photo
		}
	}
	return best
}

func firstNonEmptyTelegram(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
