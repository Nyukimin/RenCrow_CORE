package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	appattachment "github.com/Nyukimin/RenCrow_CORE/internal/application/attachment"
	domainattachment "github.com/Nyukimin/RenCrow_CORE/internal/domain/attachment"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

type MessageHandler func(ctx context.Context, req SendRequest) (string, error)
type MessageErrorHandler func(req SendRequest, err error)

// AttachmentSaver persists uploaded Viewer files before they enter orchestration.

type AttachmentSaver interface {
	SaveAll(ctx context.Context, files []appattachment.IncomingFile) ([]domainattachment.Attachment, error)
}

// SendRequest is the adapter-neutral payload passed from Viewer to orchestration.

type SendRequest struct {
	JobID          string
	ViewerClientID string
	Provenance     RequestProvenance
	Message        string
	To             modulechat.ViewerRecipient
	Attachments    []domainattachment.Attachment
}

type viewerSendRequest struct {
	ViewerClientID string `json:"viewer_client_id,omitempty"`
	InputSource    string `json:"input_source,omitempty"`
	UserID         string `json:"user_id,omitempty"`
	DeviceName     string `json:"device_name,omitempty"`
	Message        string `json:"message"`
	To             string `json:"to,omitempty"`
	ModelAlias     string `json:"model_alias,omitempty"`
	BaseURL        string `json:"base_url,omitempty"`
	Model          string `json:"model,omitempty"`
	RoutePrefix    string `json:"route_prefix,omitempty"`
}

type viewerLLMAliasSpec struct {
	ModelAlias  string `json:"model_alias"`
	BaseURL     string `json:"base_url"`
	Model       string `json:"model"`
	RoutePrefix string `json:"route_prefix"`
}

var viewerLLMAliasSpecs = map[string]viewerLLMAliasSpec{
	"worker": {
		ModelAlias:  "Worker",
		BaseURL:     "http://127.0.0.1:8082",
		Model:       "Worker",
		RoutePrefix: "/ops",
	},
	"coder": {
		ModelAlias:  "Coder",
		BaseURL:     "http://127.0.0.1:8082",
		Model:       "Coder",
		RoutePrefix: "/code2",
	},
	"heavy": {
		ModelAlias:  "Heavy",
		BaseURL:     "http://127.0.0.1:8083",
		Model:       "Heavy",
		RoutePrefix: "/analyze",
	},
	"wild": {
		ModelAlias:  "Wild",
		BaseURL:     "http://127.0.0.1:8084",
		Model:       "Wild",
		RoutePrefix: "/wild",
	},
}

func viewerSendAliasSpec(req viewerSendRequest) (viewerLLMAliasSpec, bool) {
	key := strings.ToLower(strings.TrimSpace(req.ModelAlias))
	if key == "" {
		key = strings.ToLower(strings.TrimSpace(req.Model))
	}
	spec, ok := viewerLLMAliasSpecs[key]
	if !ok {
		return viewerLLMAliasSpec{}, false
	}
	if v := strings.TrimSpace(req.BaseURL); v != "" {
		spec.BaseURL = v
	}
	if v := strings.TrimSpace(req.Model); v != "" {
		spec.Model = v
	}
	if v := strings.TrimSpace(req.RoutePrefix); validViewerRoutePrefix(v) {
		spec.RoutePrefix = v
	}
	return spec, ok
}

func validViewerRoutePrefix(prefix string) bool {
	switch strings.TrimSpace(prefix) {
	case "/ops", "/wild", "/heavy", "/code", "/code1", "/code2", "/code3", "/code4", "/plan", "/analyze", "/research", "/chat":
		return true
	default:
		return false
	}
}

func viewerSendHasExplicitRoute(message string) bool {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" || trimmed[0] != '/' {
		return false
	}
	head := strings.Fields(trimmed)
	if len(head) == 0 {
		return false
	}
	switch head[0] {
	case "/ops", "/wild", "/heavy", "/code", "/code1", "/code2", "/code3", "/code4", "/plan", "/analyze", "/research", "/chat":
		return true
	default:
		return false
	}
}

func viewerEffectiveMessage(req viewerSendRequest) (string, viewerLLMAliasSpec, bool) {
	message := strings.TrimSpace(req.Message)
	spec, ok := viewerSendAliasSpec(req)
	if !ok || viewerSendHasExplicitRoute(message) {
		return message, viewerLLMAliasSpec{}, false
	}
	return spec.RoutePrefix + " " + message, spec, true
}

// HandleSend creates an HTTP handler that receives messages from the viewer input.
// onError is called with the processing error if the async handler fails (may be nil).

func HandleSend(handler MessageHandler, onError MessageErrorHandler) http.HandlerFunc {
	return HandleSendWithAttachments(handler, onError, nil)
}

// HandleSendWithAttachments receives text and optional file attachments from the Viewer.

func HandleSendWithAttachments(handler MessageHandler, onError MessageErrorHandler, saver AttachmentSaver) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			log.Printf("[Viewer] HandleSend: method not allowed: %s", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		req, attachments, err := parseViewerSendRequest(r, saver)
		if err != nil {
			log.Printf("[Viewer] HandleSend: invalid request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.Message) == "" && len(attachments) == 0 {
			log.Printf("[Viewer] HandleSend: empty message and no attachments")
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		recipient, err := modulechat.NormalizeViewerRecipient(req.To)
		if err != nil {
			log.Printf("[Viewer] HandleSend: invalid recipient: %q", req.To)
			http.Error(w, "invalid recipient", http.StatusBadRequest)
			return
		}
		req.To = string(recipient)
		req.ViewerClientID = strings.TrimSpace(req.ViewerClientID)
		provenance, err := buildViewerRequestProvenance(r, req)
		if err != nil {
			log.Printf("[Viewer] HandleSend: invalid request provenance: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		jobID := task.NewJobID().String()
		sendReq := SendRequest{
			JobID:          jobID,
			ViewerClientID: req.ViewerClientID,
			Provenance:     provenance,
			To:             recipient,
			Attachments:    attachments,
		}

		effectiveMessage, aliasSpec, aliasApplied := viewerEffectiveMessage(req)
		if strings.TrimSpace(effectiveMessage) == "" && len(attachments) > 0 {
			effectiveMessage = defaultAttachmentMessage(attachments)
		}
		sendReq.Message = effectiveMessage
		log.Printf("[Viewer] HandleSend: accepted job_id=%s viewer_client_id=%q recipient=%s attachment_count=%d message_len=%d %s",
			jobID, req.ViewerClientID, recipient, len(attachments), len([]rune(effectiveMessage)), provenance.LogFields())
		if aliasApplied {
			log.Printf("[Viewer] HandleSend: message received: %q alias=%s base_url=%s model=%s route_prefix=%s",
				req.Message, aliasSpec.ModelAlias, aliasSpec.BaseURL, aliasSpec.Model, aliasSpec.RoutePrefix)
		} else {
			log.Printf("[Viewer] HandleSend: message received: %q", req.Message)
		}

		// Process asynchronously — events flow back via SSE.
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			log.Printf("[Viewer] HandleSend: async start job_id=%s viewer_client_id=%q recipient=%s %s", jobID, req.ViewerClientID, recipient, provenance.LogFields())
			response, err := handler(ctx, sendReq)
			if err != nil {
				log.Printf("[Viewer] HandleSend: async error job_id=%s viewer_client_id=%q recipient=%s %s err=%v", jobID, req.ViewerClientID, recipient, provenance.LogFields(), err)
				if onError != nil {
					onError(sendReq, err)
				}
			} else {
				log.Printf("[Viewer] HandleSend: async complete job_id=%s viewer_client_id=%q recipient=%s response_len=%d %s", jobID, req.ViewerClientID, recipient, len(response), provenance.LogFields())
			}
		}()

		w.Header().Set("Content-Type", "application/json")
		if aliasApplied {
			resp := struct {
				OK             bool   `json:"ok"`
				JobID          string `json:"job_id"`
				ViewerClientID string `json:"viewer_client_id,omitempty"`
				Recipient      string `json:"recipient"`
				ModelAlias     string `json:"model_alias"`
				BaseURL        string `json:"base_url"`
				Model          string `json:"model"`
				RoutePrefix    string `json:"route_prefix"`
				Attachments    int    `json:"attachment_count"`
			}{
				OK:             true,
				JobID:          jobID,
				ViewerClientID: req.ViewerClientID,
				Recipient:      string(recipient),
				ModelAlias:     aliasSpec.ModelAlias,
				BaseURL:        aliasSpec.BaseURL,
				Model:          aliasSpec.Model,
				RoutePrefix:    aliasSpec.RoutePrefix,
				Attachments:    len(attachments),
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				log.Printf("[Viewer] HandleSend: response encode error: %v", err)
			}
		} else {
			resp := struct {
				OK             bool   `json:"ok"`
				JobID          string `json:"job_id"`
				ViewerClientID string `json:"viewer_client_id,omitempty"`
				Recipient      string `json:"recipient"`
				Attachments    int    `json:"attachment_count"`
			}{
				OK:             true,
				JobID:          jobID,
				ViewerClientID: req.ViewerClientID,
				Recipient:      string(recipient),
				Attachments:    len(attachments),
			}
			if err := json.NewEncoder(w).Encode(resp); err != nil {
				log.Printf("[Viewer] HandleSend: response encode error: %v", err)
			}
		}
		log.Printf("[Viewer] HandleSend: sent OK response")
	}
}

func defaultAttachmentMessage(attachments []domainattachment.Attachment) string {
	for _, att := range attachments {
		if att.Kind == domainattachment.KindVideo {
			return "添付動画を解析してください。"
		}
	}
	return "添付ファイルを確認してください。"
}

func parseViewerSendRequest(r *http.Request, saver AttachmentSaver) (viewerSendRequest, []domainattachment.Attachment, error) {
	contentType := r.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return parseViewerMultipartSendRequest(r, saver)
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil {
		return viewerSendRequest{}, nil, fmt.Errorf("read body: %w", err)
	}
	var req viewerSendRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return viewerSendRequest{}, nil, fmt.Errorf("json decode: %w", err)
	}
	return req, nil, nil
}

func parseViewerMultipartSendRequest(r *http.Request, saver AttachmentSaver) (viewerSendRequest, []domainattachment.Attachment, error) {
	if saver == nil {
		return viewerSendRequest{}, nil, fmt.Errorf("attachment saver is nil")
	}
	if err := r.ParseMultipartForm(domainattachment.DefaultLimits.MaxTotalBytes + (1 << 20)); err != nil {
		return viewerSendRequest{}, nil, fmt.Errorf("parse multipart: %w", err)
	}
	req := viewerSendRequest{
		ViewerClientID: r.FormValue("viewer_client_id"),
		InputSource:    r.FormValue("input_source"),
		UserID:         r.FormValue("user_id"),
		DeviceName:     r.FormValue("device_name"),
		Message:        r.FormValue("message"),
		To:             r.FormValue("to"),
		ModelAlias:     r.FormValue("model_alias"),
		BaseURL:        r.FormValue("base_url"),
		Model:          r.FormValue("model"),
		RoutePrefix:    r.FormValue("route_prefix"),
	}

	files, err := incomingViewerFiles(r.MultipartForm)
	if err != nil {
		return viewerSendRequest{}, nil, err
	}
	attachments, err := saver.SaveAll(r.Context(), files)
	if err != nil {
		return viewerSendRequest{}, nil, err
	}
	return req, attachments, nil
}

func incomingViewerFiles(form *multipart.Form) ([]appattachment.IncomingFile, error) {
	if form == nil || len(form.File) == 0 {
		return nil, nil
	}
	headers := append([]*multipart.FileHeader{}, form.File["attachments"]...)
	headers = append(headers, form.File["attachments[]"]...)
	files := make([]appattachment.IncomingFile, 0, len(headers))
	for _, fh := range headers {
		f, err := fh.Open()
		if err != nil {
			return nil, fmt.Errorf("open attachment: %w", err)
		}
		files = append(files, appattachment.IncomingFile{
			Filename:    fh.Filename,
			ContentType: fh.Header.Get("Content-Type"),
			SizeBytes:   fh.Size,
			Reader:      f,
		})
	}
	return files, nil
}
