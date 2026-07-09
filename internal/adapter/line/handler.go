package line

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	adapterchannels "github.com/Nyukimin/RenCrow_CORE/internal/adapter/channels"
	appattachment "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	channelapp "github.com/Nyukimin/RenCrow_CORE/internal/application/channel"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	domainsecurity "github.com/Nyukimin/RenCrow_CORE/internal/domain/security"
)

type AttachmentSaver interface {
	SaveAll(ctx context.Context, files []appattachment.IncomingFile) ([]domainattachment.Attachment, error)
}

// Handler はLINE webhookハンドラー
type Handler struct {
	orchestrator    orchestrator.Orchestrator
	channelSecret   string
	sender          *MessageSender
	mediaDownloader *MediaDownloader
	attachmentSaver AttachmentSaver
	channelPolicy   *domainsecurity.ChannelPolicy
	botUserID       string // Bot's LINE user ID for mention detection
}

// Name returns channel name.
func (h *Handler) Name() string { return "line" }

// Probe checks basic readiness of LINE adapter.
func (h *Handler) Probe(ctx context.Context) error {
	if h.sender == nil {
		return fmt.Errorf("line sender is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return nil
}

// Send pushes a message to a LINE user.
func (h *Handler) Send(ctx context.Context, chatID, text string) error {
	return h.sender.SendPushMessage(ctx, chatID, text)
}

// Verify validates request signature against LINE channel secret.
func (h *Handler) Verify(_ *http.Request, body []byte, signature string) error {
	if !verifySignature(body, signature, h.channelSecret) {
		return fmt.Errorf("invalid signature")
	}
	return nil
}

// NormalizeEvent converts LINE webhook event into a channel-agnostic event.
func (h *Handler) NormalizeEvent(event WebhookEvent, raw []byte) adapterchannels.ChannelEvent {
	timestamp := time.Now().UTC()
	if event.Timestamp > 0 {
		timestamp = time.UnixMilli(event.Timestamp).UTC()
	}
	return adapterchannels.ChannelEvent{
		Channel:   "line",
		ChatID:    lineChatID(event.Source),
		UserID:    event.Source.UserID,
		MessageID: event.Message.ID,
		Text:      event.Message.Text,
		Timestamp: timestamp,
		Raw:       raw,
	}
}

// NewHandler は新しいHandlerを作成
func NewHandler(orch orchestrator.Orchestrator, channelSecret, accessToken string) *Handler {
	return &Handler{
		orchestrator:    orch,
		channelSecret:   channelSecret,
		sender:          NewMessageSender(accessToken),
		mediaDownloader: NewMediaDownloader(accessToken),
		botUserID:       "", // Set via SetBotUserID if needed
	}
}

// SetBotUserID sets the bot's user ID for mention detection in group chats
func (h *Handler) SetBotUserID(botUserID string) {
	h.botUserID = botUserID
}

// SetAttachmentSaver enables LINE media messages to enter the shared attachment pipeline.
func (h *Handler) SetAttachmentSaver(saver AttachmentSaver) {
	h.attachmentSaver = saver
}

// SetChannelPolicy enables channel-level DM/group/sender authorization.
func (h *Handler) SetChannelPolicy(policy domainsecurity.ChannelPolicy) {
	h.channelPolicy = &policy
}

func (h *Handler) ChannelPolicyConfigured() bool {
	return h.channelPolicy != nil
}

// ServeHTTP はHTTPリクエストを処理
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	log.Printf("[HTTP] %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)

	if r.URL.Path == "/webhook" && r.Method == http.MethodPost {
		h.handleWebhook(w, r)
		return
	}

	http.NotFound(w, r)
}

// handleWebhook はLINE webhookを処理
func (h *Handler) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// リクエストボディを読み取り（署名検証のため）
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// 署名検証
	signature := r.Header.Get("X-Line-Signature")
	log.Printf("[Webhook] Body length: %d, Signature present: %v, Secret length: %d",
		len(body), signature != "", len(h.channelSecret))
	if !verifySignature(body, signature, h.channelSecret) {
		log.Printf("[Webhook] Signature verification FAILED")
		http.Error(w, "Invalid signature", http.StatusUnauthorized)
		return
	}
	log.Printf("[Webhook] Signature verified OK, events parsing...")

	// リクエストボディをパース
	var payload WebhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	log.Printf("[Webhook] Events count: %d", len(payload.Events))
	for i, ev := range payload.Events {
		log.Printf("[Webhook] Event[%d]: type=%s, msg_type=%s, source=%s, text=%q",
			i, ev.Type, ev.Message.Type, ev.Source.Type, ev.Message.Text)
	}

	// 即座に200を返し、イベント処理はバックグラウンドで実行
	// （LINE公式推奨: 2秒以内にレスポンスを返す）
	w.WriteHeader(http.StatusOK)

	// イベントをバックグラウンドで処理
	for _, event := range payload.Events {
		// メッセージイベントのみ処理
		if event.Type != "message" {
			continue
		}

		if !h.shouldProcessMessage(event) {
			continue
		}
		if !h.authorizeEvent(event) {
			continue
		}

		// Group/Room chatの場合、Bot mentionチェック
		if h.botUserID != "" && event.Source.Type != "user" {
			var mentionees []Mentionee
			if event.Message.Mention != nil {
				mentionees = event.Message.Mention.Mentionees
			}
			if !isBotMention(event.Source.Type, mentionees, h.botUserID) {
				// Bot mentionがない場合はスキップ
				continue
			}
		}

		go h.processEvent(event)
	}
}

// processEvent はイベントをバックグラウンドで処理（HTTPコンテキストから独立）
func (h *Handler) processEvent(event WebhookEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// セッションID生成（仕様: ChatID = ユーザーID、SessionID = line:<user_id>）
	chatID := lineChatID(event.Source)
	sessionID := h.generateSessionID(chatID)

	attachments, err := h.attachmentsForEvent(ctx, event)
	if err != nil {
		log.Printf("[Webhook] Failed to prepare LINE attachment: %v", err)
		_ = h.sender.SendReplyMessage(ctx, event.ReplyToken, "添付ファイルを取得できませんでした。")
		return
	}
	userMessage := strings.TrimSpace(event.Message.Text)
	if userMessage == "" && len(attachments) > 0 {
		userMessage = "添付ファイルを確認してください。"
	}

	// オーケストレータを呼び出し
	req := orchestrator.ProcessMessageRequest{
		SessionID:   sessionID,
		Channel:     "line",
		ChatID:      chatID,
		UserMessage: userMessage,
		Attachments: attachments,
	}

	resp, err := h.orchestrator.ProcessMessage(ctx, req)
	if err != nil {
		log.Printf("[Webhook] Error processing message: %v", err)
		return
	}

	// Quote token取得
	quoteToken := extractQuoteToken(event)

	// LINE返信API呼び出し（quote token対応）
	var sendErr error
	if quoteToken != "" {
		sendErr = h.sender.SendReplyMessageWithQuote(ctx, event.ReplyToken, resp.Response, quoteToken)
	} else {
		sendErr = h.sender.SendReplyMessage(ctx, event.ReplyToken, resp.Response)
	}

	if sendErr != nil {
		log.Printf("[Webhook] Failed to send reply: %v", sendErr)
	} else {
		log.Printf("[Webhook] Reply sent successfully for session %s", sessionID)
	}
}

// generateSessionID はセッションIDを生成
func (h *Handler) generateSessionID(userID string) string {
	return channelapp.BuildSessionID(time.Now(), "line", userID)
}

func (h *Handler) authorizeEvent(event WebhookEvent) bool {
	if h.channelPolicy == nil {
		return true
	}
	chatID := lineChatID(event.Source)
	decision := h.channelPolicy.Evaluate(domainsecurity.ChannelRequest{
		Channel:    "line",
		SourceType: event.Source.Type,
		SenderID:   event.Source.UserID,
		ChatID:     chatID,
	})
	if decision.Allowed {
		return true
	}
	log.Printf("[Webhook] Channel policy denied source=%s sender=%s chat=%s reason=%s",
		event.Source.Type, event.Source.UserID, chatID, decision.Reason)
	if event.ReplyToken != "" && h.sender != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = h.sender.SendReplyMessage(ctx, event.ReplyToken, "このチャネルからの利用は許可されていません。")
	}
	return false
}

func lineChatID(source EventSource) string {
	switch source.Type {
	case "group":
		if source.GroupID != "" {
			return source.GroupID
		}
	case "room":
		if source.RoomID != "" {
			return source.RoomID
		}
	}
	return source.UserID
}

func (h *Handler) shouldProcessMessage(event WebhookEvent) bool {
	switch event.Message.Type {
	case "text", "image", "file":
		return true
	default:
		return false
	}
}

func (h *Handler) attachmentsForEvent(ctx context.Context, event WebhookEvent) ([]domainattachment.Attachment, error) {
	switch event.Message.Type {
	case "image", "file":
	default:
		return nil, nil
	}
	if h.mediaDownloader == nil {
		return nil, fmt.Errorf("line media downloader is nil")
	}
	if h.attachmentSaver == nil {
		return nil, fmt.Errorf("line attachment saver is nil")
	}
	media, err := h.mediaDownloader.DownloadContent(ctx, event.Message.ID)
	if err != nil {
		return nil, err
	}
	contentType := http.DetectContentType(media)
	filename := lineAttachmentFilename(event, contentType)
	return h.attachmentSaver.SaveAll(ctx, []appattachment.IncomingFile{{
		Filename:    filename,
		ContentType: contentType,
		SizeBytes:   int64(len(media)),
		Reader:      bytes.NewReader(media),
	}})
}

func lineAttachmentFilename(event WebhookEvent, contentType string) string {
	if name := strings.TrimSpace(event.Message.FileName); name != "" {
		return name
	}
	id := strings.TrimSpace(event.Message.ID)
	if id == "" {
		id = "line"
	}
	switch strings.TrimSpace(contentType) {
	case "image/png":
		return id + ".png"
	case "image/gif":
		return id + ".gif"
	case "image/webp":
		return id + ".webp"
	case "application/pdf":
		return id + ".pdf"
	default:
		if strings.HasPrefix(contentType, "text/") {
			return id + ".txt"
		}
		return id + ".jpg"
	}
}

// WebhookPayload はLINE webhookペイロード
type WebhookPayload struct {
	Events []WebhookEvent `json:"events"`
}

// WebhookEvent はLINE webhookイベント
type WebhookEvent struct {
	Type       string       `json:"type"`
	Message    EventMessage `json:"message"`
	Source     EventSource  `json:"source"`
	ReplyToken string       `json:"replyToken"`
	Timestamp  int64        `json:"timestamp"`
}

// EventMessage はイベントメッセージ
type EventMessage struct {
	Type       string   `json:"type"`
	Text       string   `json:"text"`
	ID         string   `json:"id"`
	FileName   string   `json:"fileName,omitempty"`
	QuoteToken string   `json:"quoteToken"`
	Mention    *Mention `json:"mention,omitempty"`
}

// Mention はメンション情報
type Mention struct {
	Mentionees []Mentionee `json:"mentionees"`
}

// Mentionee はメンション対象ユーザー
type Mentionee struct {
	Index  int    `json:"index"`
	Length int    `json:"length"`
	UserID string `json:"userId"`
}

// EventSource はイベントソース
type EventSource struct {
	Type    string `json:"type"`
	UserID  string `json:"userId"`
	GroupID string `json:"groupId,omitempty"`
	RoomID  string `json:"roomId,omitempty"`
}
