package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	boardOwnerRequestsPath                = "/api/board-communication/requests"
	boardOwnerAgentRequestsPath           = "/api/board-communication/agent-requests"
	boardOwnerHTTPMaxBody                 = 64 << 10
	boardOwnerHTTPStageAccepted           = "accepted"
	boardOwnerHTTPStageCompleted          = "completed"
	boardOwnerHTTPStagePublicationPending = "publication_pending"
	boardOwnerHTTPStagePublicationSaved   = "publication_saved"
	boardOwnerHTTPStageReplyConfirmed     = "reply_confirmed"
	boardOwnerHTTPStageRejected           = "rejected"
	boardOwnerHTTPStageUnknown            = "unknown"
)

var (
	errBoardOwnerHTTPConflict = errors.New("BOARD owner request conflicts with a prior request")
	errBoardOwnerHTTPInvalid  = errors.New("BOARD owner request is invalid")
	errBoardOwnerHTTPNotFound = errors.New("BOARD owner request was not found")
)

type boardOwnerHTTPRequest struct {
	ActorID        string `json:"actor_id,omitempty"`
	ChannelID      string `json:"channel_id"`
	PeerID         string `json:"peer_id"`
	Title          string `json:"title"`
	Instruction    string `json:"instruction"`
	IdempotencyKey string `json:"idempotency_key"`
}

type boardOwnerHTTPAcceptedReceipt struct {
	OwnerID        string
	SessionID      modulecore.SessionID
	TurnID         modulecore.TurnID
	TraceID        modulecore.TraceID
	InputMessageID modulecore.MessageID
	AcceptedAt     time.Time
	Durable        bool
}

type boardOwnerHTTPStatusReceipt struct {
	OwnerID        string
	SessionID      modulecore.SessionID
	TurnID         modulecore.TurnID
	TraceID        modulecore.TraceID
	InputMessageID modulecore.MessageID
	ActorID        string
	BoardMessageID string
	ThreadID       string
	ReplyMessageID string
	Stage          string
	AcceptedAt     time.Time
	UpdatedAt      time.Time
	Durable        bool
}

type boardOwnerHTTPCallbacks struct {
	Accept      func(context.Context, string, boardOwnerHTTPRequest) (boardOwnerHTTPAcceptedReceipt, error)
	Status      func(context.Context, string, modulecore.TurnID) (boardOwnerHTTPStatusReceipt, bool, error)
	AgentAccept func(context.Context, string, boardOwnerHTTPRequest) (boardOwnerHTTPAcceptedReceipt, error)
	AgentStatus func(context.Context, string, modulecore.TurnID) (boardOwnerHTTPStatusReceipt, bool, error)
}

type boardOwnerHTTPHandler struct {
	ownerID  string
	token    []byte
	callback boardOwnerHTTPCallbacks
}

type boardOwnerHTTPErrorResponse struct {
	Error string `json:"error"`
}

type boardOwnerHTTPAcceptedResponse struct {
	OwnerID        string               `json:"owner_id"`
	SessionID      modulecore.SessionID `json:"session_id"`
	TurnID         modulecore.TurnID    `json:"turn_id"`
	TraceID        modulecore.TraceID   `json:"trace_id"`
	InputMessageID modulecore.MessageID `json:"input_message_id"`
	Stage          string               `json:"stage"`
	AcceptedAt     time.Time            `json:"accepted_at"`
}

type boardOwnerHTTPStatusResponse struct {
	OwnerID        string               `json:"owner_id"`
	SessionID      modulecore.SessionID `json:"session_id"`
	TurnID         modulecore.TurnID    `json:"turn_id"`
	TraceID        modulecore.TraceID   `json:"trace_id"`
	InputMessageID modulecore.MessageID `json:"input_message_id"`
	ActorID        string               `json:"actor_id,omitempty"`
	BoardMessageID string               `json:"board_message_id,omitempty"`
	ThreadID       string               `json:"thread_id,omitempty"`
	ReplyMessageID string               `json:"reply_message_id,omitempty"`
	Stage          string               `json:"stage"`
	AcceptedAt     time.Time            `json:"accepted_at"`
	UpdatedAt      time.Time            `json:"updated_at"`
}

// newBoardOwnerHTTPHandler creates a fixed-scope owner-only API. The callback
// boundary receives only the configured owner and typed request or TurnID.
func newBoardOwnerHTTPHandler(ownerID string, protectedBearer []byte, callback boardOwnerHTTPCallbacks) (http.Handler, error) {
	if ownerID == "" || strings.TrimSpace(ownerID) != ownerID || !agentOpsUserIDPattern.MatchString(ownerID) {
		return nil, errors.New("BOARD owner HTTP owner_id is invalid")
	}
	if len(protectedBearer) < agentOpsMinTokenBytes || strings.IndexFunc(string(protectedBearer), unicode.IsSpace) >= 0 {
		return nil, errors.New("BOARD owner HTTP bearer is invalid")
	}
	h := &boardOwnerHTTPHandler{ownerID: ownerID, token: append([]byte(nil), protectedBearer...), callback: callback}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+boardOwnerRequestsPath, h.accept)
	mux.HandleFunc("GET "+boardOwnerRequestsPath+"/{turn}", h.status)
	mux.HandleFunc("POST "+boardOwnerAgentRequestsPath, h.accept)
	mux.HandleFunc("GET "+boardOwnerAgentRequestsPath+"/{turn}", h.status)
	return localOnlyHandler(mux.ServeHTTP), nil
}

func (h *boardOwnerHTTPHandler) accept(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.authorized(r) {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.URL == nil || r.URL.RawQuery != "" {
		h.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	agentRequest := r.URL.Path == boardOwnerAgentRequestsPath
	request, err := decodeBoardOwnerHTTPRequest(w, r, agentRequest)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	accept := h.callback.Accept
	if agentRequest {
		accept = h.callback.AgentAccept
	}
	if accept == nil {
		h.writeError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	receipt, err := accept(r.Context(), h.ownerID, request)
	if err != nil {
		h.writeAcceptError(w, err)
		return
	}
	if !validBoardOwnerHTTPAcceptedReceipt(receipt, h.ownerID) {
		h.writeError(w, http.StatusServiceUnavailable, "request_outcome_unknown")
		return
	}
	writeJSONStatus(w, http.StatusAccepted, boardOwnerHTTPAcceptedResponse{
		OwnerID: receipt.OwnerID, SessionID: receipt.SessionID, TurnID: receipt.TurnID,
		TraceID: receipt.TraceID, InputMessageID: receipt.InputMessageID,
		Stage: boardOwnerHTTPStageAccepted, AcceptedAt: receipt.AcceptedAt,
	})
}

func (h *boardOwnerHTTPHandler) status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !h.authorized(r) {
		h.writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.URL == nil || r.URL.RawQuery != "" {
		h.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	turnID := modulecore.TurnID(r.PathValue("turn"))
	if turnID.Validate() != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	agentRequest := strings.HasPrefix(r.URL.Path, boardOwnerAgentRequestsPath+"/")
	status := h.callback.Status
	if agentRequest {
		status = h.callback.AgentStatus
	}
	if status == nil {
		h.writeError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	receipt, found, err := status(r.Context(), h.ownerID, turnID)
	if err != nil {
		if errors.Is(err, errBoardOwnerHTTPNotFound) {
			h.writeError(w, http.StatusNotFound, "not_found")
			return
		}
		h.writeError(w, http.StatusServiceUnavailable, "request_outcome_unknown")
		return
	}
	if !found || receipt.OwnerID != h.ownerID || receipt.TurnID != turnID {
		h.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if (agentRequest && !validBoardOwnerHTTPAgentStatusReceipt(receipt)) || (!agentRequest && !validBoardOwnerHTTPStatusReceipt(receipt)) {
		h.writeError(w, http.StatusServiceUnavailable, "request_outcome_unknown")
		return
	}
	writeJSONStatus(w, http.StatusOK, boardOwnerHTTPStatusResponse{
		OwnerID: receipt.OwnerID, SessionID: receipt.SessionID, TurnID: receipt.TurnID,
		TraceID: receipt.TraceID, InputMessageID: receipt.InputMessageID, Stage: receipt.Stage,
		ActorID: receipt.ActorID, BoardMessageID: receipt.BoardMessageID, ThreadID: receipt.ThreadID,
		ReplyMessageID: receipt.ReplyMessageID,
		AcceptedAt:     receipt.AcceptedAt, UpdatedAt: receipt.UpdatedAt,
	})
}

func (h *boardOwnerHTTPHandler) authorized(r *http.Request) bool {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return false
	}
	scheme, presented, ok := strings.Cut(values[0], " ")
	if !ok || scheme != "Bearer" || presented == "" || strings.IndexFunc(presented, unicode.IsSpace) >= 0 {
		return false
	}
	return constantTimeAgentOpsTokenEqual(h.token, []byte(presented))
}

func (h *boardOwnerHTTPHandler) writeAcceptError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errBoardOwnerHTTPConflict):
		h.writeError(w, http.StatusConflict, "idempotency_conflict")
	case errors.Is(err, errBoardOwnerHTTPInvalid):
		h.writeError(w, http.StatusBadRequest, "invalid_request")
	case errors.Is(err, errBoardOwnerHTTPNotFound):
		h.writeError(w, http.StatusNotFound, "not_found")
	default:
		h.writeError(w, http.StatusServiceUnavailable, "request_outcome_unknown")
	}
}

func (h *boardOwnerHTTPHandler) writeError(w http.ResponseWriter, status int, code string) {
	writeJSONStatus(w, status, boardOwnerHTTPErrorResponse{Error: code})
}

func decodeBoardOwnerHTTPRequest(w http.ResponseWriter, r *http.Request, agentRequest bool) (boardOwnerHTTPRequest, error) {
	if r.Body == nil {
		return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
	}
	r.Body = http.MaxBytesReader(w, r.Body, boardOwnerHTTPMaxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil || len(raw) == 0 || !utf8.Valid(raw) {
		return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
	}
	allowed := map[string]bool{
		"channel_id": true, "peer_id": true, "title": true, "instruction": true, "idempotency_key": true,
	}
	if agentRequest {
		allowed["actor_id"] = true
	}
	values := make(map[string]string, len(allowed))
	for decoder.More() {
		token, tokenErr := decoder.Token()
		key, ok := token.(string)
		if tokenErr != nil || !ok || !allowed[key] {
			return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
		}
		if _, duplicate := values[key]; duplicate {
			return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
		}
		var value *string
		if decodeErr := decoder.Decode(&value); decodeErr != nil || value == nil {
			return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
		}
		values[key] = *value
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(values) != len(allowed) {
		return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
	}
	request := boardOwnerHTTPRequest{
		ActorID: values["actor_id"], ChannelID: values["channel_id"], PeerID: values["peer_id"], Title: values["title"],
		Instruction: values["instruction"], IdempotencyKey: values["idempotency_key"],
	}
	if agentRequest && (!validBoardOwnerHTTPActorID(request.ActorID) || request.PeerID == request.ActorID) {
		return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
	}
	if !validBoardOwnerHTTPValue(request.ChannelID, 512) || !validBoardOwnerHTTPValue(request.PeerID, 512) ||
		!validBoardOwnerHTTPValue(request.Title, 2048) || !validBoardOwnerHTTPValue(request.IdempotencyKey, 512) ||
		!validBoardOwnerHTTPInstruction(request.Instruction) {
		return boardOwnerHTTPRequest{}, errBoardOwnerHTTPInvalid
	}
	return request, nil
}

func validBoardOwnerHTTPActorID(actorID string) bool {
	switch actorID {
	case "mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin":
		return true
	default:
		return false
	}
}

func validBoardOwnerHTTPValue(value string, maxBytes int) bool {
	return value != "" && len([]byte(value)) <= maxBytes && strings.TrimSpace(value) == value &&
		utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validBoardOwnerHTTPInstruction(value string) bool {
	return value != "" && strings.TrimSpace(value) != "" && len([]byte(value)) <= conversation.MaxUserMessageBytes &&
		utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func validBoardOwnerHTTPAcceptedReceipt(receipt boardOwnerHTTPAcceptedReceipt, ownerID string) bool {
	return receipt.Durable && receipt.OwnerID == ownerID && receipt.SessionID.Validate() == nil && receipt.TurnID.Validate() == nil &&
		receipt.TraceID.Validate() == nil && receipt.InputMessageID.Validate() == nil && validBoardOwnerHTTPTime(receipt.AcceptedAt)
}

func validBoardOwnerHTTPStatusReceipt(receipt boardOwnerHTTPStatusReceipt) bool {
	return receipt.ActorID == "" && receipt.BoardMessageID == "" && receipt.ThreadID == "" && receipt.ReplyMessageID == "" &&
		receipt.Durable && receipt.SessionID.Validate() == nil && receipt.TurnID.Validate() == nil &&
		receipt.TraceID.Validate() == nil && receipt.InputMessageID.Validate() == nil && boardOwnerHTTPStageValid(receipt.Stage) &&
		validBoardOwnerHTTPTime(receipt.AcceptedAt) && validBoardOwnerHTTPTime(receipt.UpdatedAt) && !receipt.UpdatedAt.Before(receipt.AcceptedAt)
}

func validBoardOwnerHTTPAgentStatusReceipt(receipt boardOwnerHTTPStatusReceipt) bool {
	if !receipt.Durable || receipt.SessionID.Validate() != nil || receipt.TurnID.Validate() != nil ||
		receipt.TraceID.Validate() != nil || receipt.InputMessageID.Validate() != nil || !boardOwnerHTTPAgentStageValid(receipt.Stage) ||
		!validBoardOwnerHTTPTime(receipt.AcceptedAt) || !validBoardOwnerHTTPTime(receipt.UpdatedAt) || receipt.UpdatedAt.Before(receipt.AcceptedAt) ||
		!validBoardOwnerHTTPActorID(receipt.ActorID) {
		return false
	}
	if receipt.BoardMessageID != "" && !validBoardOwnerHTTPBoardID(receipt.BoardMessageID, "m_") {
		return false
	}
	if receipt.ThreadID != "" && !validBoardOwnerHTTPBoardID(receipt.ThreadID, "t_") {
		return false
	}
	if receipt.ReplyMessageID != "" && !validBoardOwnerHTTPBoardID(receipt.ReplyMessageID, "m_") {
		return false
	}
	switch receipt.Stage {
	case boardOwnerHTTPStagePublicationSaved:
		return receipt.BoardMessageID != "" && receipt.ThreadID != ""
	case boardOwnerHTTPStageReplyConfirmed:
		return receipt.BoardMessageID != "" && receipt.ThreadID != "" && receipt.ReplyMessageID != ""
	default:
		return true
	}
}

func validBoardOwnerHTTPBoardID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+24 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func boardOwnerHTTPAgentStageValid(stage string) bool {
	if boardOwnerHTTPStageValid(stage) {
		return true
	}
	switch stage {
	case boardOwnerHTTPStagePublicationSaved, boardOwnerHTTPStageReplyConfirmed, boardOwnerHTTPStageRejected, boardOwnerHTTPStageUnknown:
		return true
	default:
		return false
	}
}

func boardOwnerHTTPStageValid(stage string) bool {
	switch stage {
	case boardOwnerHTTPStageAccepted, boardOwnerHTTPStageCompleted, boardOwnerHTTPStagePublicationPending:
		return true
	default:
		return false
	}
}

func validBoardOwnerHTTPTime(value time.Time) bool {
	if value.IsZero() {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}
