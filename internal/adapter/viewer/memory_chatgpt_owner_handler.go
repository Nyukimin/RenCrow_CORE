package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	chatgptimport "github.com/Nyukimin/RenCrow_CORE/internal/application/chatgptimport"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/google/uuid"
)

const (
	chatGPTImportOwnerRoute            = "/v1/memory/import/chatgpt"
	chatGPTImportOwnerManifestMaxBytes = int64(64 << 20)
	chatGPTImportOwnerArtifactMaxBytes = int64(64 << 30)
	chatGPTImportOwnerConfirmMaxBytes  = int64(8 << 10)
)

var (
	errChatGPTOwnerInvalid    = errors.New("invalid ChatGPT owner request")
	errChatGPTOwnerTooLarge   = errors.New("ChatGPT owner request exceeds its bound")
	errChatGPTOwnerFilesystem = errors.New("ChatGPT owner staging is unavailable")
)

// ChatGPTImportOwnerLimits are the bounded HTTP input limits. Tests may lower
// production limits without ever raising them; zero selects the production
// default.
type ChatGPTImportOwnerLimits struct {
	ManifestMaxBytes int64
	ArtifactMaxBytes int64
	ConfirmMaxBytes  int64
}

func defaultChatGPTImportOwnerLimits() ChatGPTImportOwnerLimits {
	return ChatGPTImportOwnerLimits{
		ManifestMaxBytes: chatGPTImportOwnerManifestMaxBytes,
		ArtifactMaxBytes: chatGPTImportOwnerArtifactMaxBytes,
		ConfirmMaxBytes:  chatGPTImportOwnerConfirmMaxBytes,
	}
}

func (limits ChatGPTImportOwnerLimits) normalized() (ChatGPTImportOwnerLimits, error) {
	choose := func(value, fallback int64) (int64, error) {
		if value == 0 {
			value = fallback
		}
		if value < 1 || value > fallback {
			return 0, errChatGPTOwnerInvalid
		}
		return value, nil
	}
	manifest, err := choose(limits.ManifestMaxBytes, chatGPTImportOwnerManifestMaxBytes)
	if err != nil {
		return ChatGPTImportOwnerLimits{}, err
	}
	artifact, err := choose(limits.ArtifactMaxBytes, chatGPTImportOwnerArtifactMaxBytes)
	if err != nil {
		return ChatGPTImportOwnerLimits{}, err
	}
	confirm, err := choose(limits.ConfirmMaxBytes, chatGPTImportOwnerConfirmMaxBytes)
	if err != nil {
		return ChatGPTImportOwnerLimits{}, err
	}
	return ChatGPTImportOwnerLimits{ManifestMaxBytes: manifest, ArtifactMaxBytes: artifact, ConfirmMaxBytes: confirm}, nil
}

// ChatGPTImportOwnerService is the application boundary for one authenticated
// Common Raw upload. The HTTP adapter supplies only CORE-owned identity and
// paths created below the configured raw source root.
type ChatGPTImportOwnerService interface {
	Import(context.Context, chatgptimport.ImportRequest) (chatgptimport.ImportResult, error)
}

// ChatGPTImportOwnerStore is the small owner API boundary used by status and
// confirmation. It intentionally does not require the broad MemoryOwnerStore.
type ChatGPTImportOwnerStore interface {
	GetChatGPTImportStatus(context.Context, string, string, string, string) (domainmemory.ChatGPTImportView, error)
	ConfirmChatGPTImportCandidates(context.Context, domainmemory.ChatGPTImportConfirmInput) (domainmemory.ChatGPTImportConfirmResult, error)
}

// NewMemoryChatGPTOwnerHandler constructs the authenticated ChatGPT Common Raw
// import/status/confirm handler.
func NewMemoryChatGPTOwnerHandler(service ChatGPTImportOwnerService, store ChatGPTImportOwnerStore, rawSourceRoot, userID string, token []byte) http.HandlerFunc {
	return NewMemoryChatGPTOwnerHandlerWithLimits(service, store, rawSourceRoot, userID, token, defaultChatGPTImportOwnerLimits())
}

// NewMemoryChatGPTOwnerHandlerWithLimits is the bounded-limit constructor
// used by adapter tests and integrations that deliberately choose lower caps.
func NewMemoryChatGPTOwnerHandlerWithLimits(service ChatGPTImportOwnerService, store ChatGPTImportOwnerStore, rawSourceRoot, userID string, token []byte, limits ChatGPTImportOwnerLimits) http.HandlerFunc {
	normalized, err := limits.normalized()
	return (&memoryChatGPTOwnerHandler{
		service:       service,
		store:         store,
		rawSourceRoot: strings.TrimSpace(rawSourceRoot),
		userID:        strings.TrimSpace(userID),
		token:         append([]byte(nil), token...),
		limits:        normalized,
		limitsErr:     err,
	}).ServeHTTP
}

type memoryChatGPTOwnerHandler struct {
	service       ChatGPTImportOwnerService
	store         ChatGPTImportOwnerStore
	rawSourceRoot string
	userID        string
	token         []byte
	limits        ChatGPTImportOwnerLimits
	limitsErr     error
}

func (h *memoryChatGPTOwnerHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	operation, exportID, ok := chatGPTOwnerRoute(r)
	if !ok {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if !memoryOwnerLoopback(r) {
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
		return
	}
	if !memoryOwnerBearerAuthorized(r, h.token) {
		writeMemoryOwnerError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !memoryOwnerClientProfileAllowed(r) {
		writeMemoryOwnerError(w, http.StatusForbidden, "forbidden")
		return
	}
	ctx, err := memoryOwnerOwnerContext(r.Context(), h.userID)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "blocked")
		return
	}

	switch operation {
	case chatGPTOwnerOperationUpload:
		if h.service == nil || h.limitsErr != nil {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		h.upload(ctx, w, r)
	case chatGPTOwnerOperationStatus:
		if h.store == nil {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		h.status(ctx, w, r, exportID)
	case chatGPTOwnerOperationConfirm:
		if h.store == nil {
			writeMemoryOwnerError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		h.confirm(ctx, w, r)
	default:
		writeMemoryOwnerError(w, http.StatusNotFound, "not_found")
	}
}

type chatGPTOwnerOperation string

const (
	chatGPTOwnerOperationUpload  chatGPTOwnerOperation = "upload"
	chatGPTOwnerOperationStatus  chatGPTOwnerOperation = "status"
	chatGPTOwnerOperationConfirm chatGPTOwnerOperation = "confirm"
)

func chatGPTOwnerRoute(r *http.Request) (chatGPTOwnerOperation, string, bool) {
	if r == nil || r.URL == nil {
		return "", "", false
	}
	escaped := r.URL.EscapedPath()
	if r.URL.RawPath != "" && r.URL.RawPath != escaped {
		return "", "", false
	}
	switch {
	case escaped == chatGPTImportOwnerRoute && r.Method == http.MethodPost:
		return chatGPTOwnerOperationUpload, "", true
	case escaped == chatGPTImportOwnerRoute+"/confirm" && r.Method == http.MethodPost:
		return chatGPTOwnerOperationConfirm, "", true
	}
	prefix := chatGPTImportOwnerRoute + "/"
	if r.Method != http.MethodGet || !strings.HasPrefix(escaped, prefix) {
		return "", "", false
	}
	rawID := strings.TrimPrefix(escaped, prefix)
	if rawID == "" || strings.Contains(rawID, "/") {
		return "", "", false
	}
	exportID, err := url.PathUnescape(rawID)
	if err != nil || strings.TrimSpace(exportID) == "" || len(exportID) > domainmemory.ChatGPTImportMaxIdentifierByte || !utf8.ValidString(exportID) || strings.ContainsAny(exportID, "/\\\r\n\x00") {
		return "", "", false
	}
	return chatGPTOwnerOperationStatus, exportID, true
}

func chatGPTOwnerValidateNoQuery(r *http.Request) error {
	if r == nil || r.URL == nil {
		return errChatGPTOwnerInvalid
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		return errChatGPTOwnerInvalid
	}
	return nil
}

func chatGPTOwnerValidateNoRequestBody(r *http.Request) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	return errChatGPTOwnerInvalid
}

func chatGPTOwnerScope(ctx context.Context, configuredUser string) (requestID, ownerID, actorID string, ok bool) {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindUser || scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP || scope.ActorID != strings.TrimSpace(configuredUser) || scope.AuthenticatedUserID != strings.TrimSpace(configuredUser) || !scope.Allows(domaintool.DataScopeUser) {
		return "", "", "", false
	}
	return scope.RequestID, scope.AuthenticatedUserID, scope.ActorID, true
}

func (h *memoryChatGPTOwnerHandler) status(ctx context.Context, w http.ResponseWriter, r *http.Request, exportID string) {
	if err := chatGPTOwnerValidateNoQuery(r); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := chatGPTOwnerValidateNoRequestBody(r); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ownerID, actorID, ok := chatGPTOwnerScope(ctx, h.userID)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "blocked")
		return
	}
	view, err := h.store.GetChatGPTImportStatus(ctx, requestID, ownerID, actorID, exportID)
	if err != nil {
		writeChatGPTOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, chatGPTImportOwnerViewResponse{ChatGPTImportView: view})
}

func (h *memoryChatGPTOwnerHandler) confirm(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if err := chatGPTOwnerValidateNoQuery(r); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if err := chatGPTOwnerValidateJSONContentType(r); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ownerID, actorID, ok := chatGPTOwnerScope(ctx, h.userID)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "blocked")
		return
	}
	request, err := decodeChatGPTOwnerConfirm(w, r, h.limits.ConfirmMaxBytes)
	if errors.Is(err, errChatGPTOwnerTooLarge) {
		writeMemoryOwnerError(w, http.StatusRequestEntityTooLarge, "too_large")
		return
	}
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	input := domainmemory.ChatGPTImportConfirmInput{
		RequestID: requestID, OwnerID: ownerID, ActorID: actorID,
		ExportID: strings.TrimSpace(request.ExportID), Reason: strings.TrimSpace(request.Reason), Apply: request.Apply,
	}
	if err := input.Validate(); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := h.store.ConfirmChatGPTImportCandidates(ctx, input)
	if err != nil {
		writeChatGPTOwnerStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type chatGPTOwnerConfirmRequest struct {
	ExportID string
	Reason   string
	Apply    bool
}

func decodeChatGPTOwnerConfirm(w http.ResponseWriter, r *http.Request, maxBytes int64) (chatGPTOwnerConfirmRequest, error) {
	if r == nil || r.Body == nil {
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes+1))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerTooLarge
		}
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
	}
	if int64(len(payload)) > maxBytes {
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerTooLarge
	}
	if !utf8.Valid(payload) {
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	token, err := decoder.Token()
	if err != nil {
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
	}
	values := make(map[string]json.RawMessage, 3)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
		}
		key, ok := keyToken.(string)
		if !ok {
			return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
		}
		if _, exists := values[key]; exists {
			return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
		}
		if key != "export_id" && key != "reason" && key != "apply" {
			return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
		}
		values[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
	}
	var result chatGPTOwnerConfirmRequest
	raw, ok := values["export_id"]
	if !ok || !decodeNonNullJSONString(raw, &result.ExportID) {
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
	}
	raw, ok = values["reason"]
	if !ok || !decodeNonNullJSONString(raw, &result.Reason) {
		return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
	}
	if raw, ok = values["apply"]; ok {
		if !decodeNonNullJSONBool(raw, &result.Apply) {
			return chatGPTOwnerConfirmRequest{}, errChatGPTOwnerInvalid
		}
	}
	return result, nil
}

func decodeNonNullJSONString(raw json.RawMessage, target *string) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	return json.Unmarshal(raw, target) == nil
}

func decodeNonNullJSONBool(raw json.RawMessage, target *bool) bool {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	return json.Unmarshal(raw, target) == nil
}

func chatGPTOwnerValidateJSONContentType(r *http.Request) error {
	if r == nil {
		return errChatGPTOwnerInvalid
	}
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return errChatGPTOwnerInvalid
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") || len(params) != 0 {
		return errChatGPTOwnerInvalid
	}
	return nil
}

func (h *memoryChatGPTOwnerHandler) upload(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if err := chatGPTOwnerValidateNoQuery(r); err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	boundary, err := chatGPTOwnerBoundary(r)
	if err != nil {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	requestID, ownerID, actorID, ok := chatGPTOwnerScope(ctx, h.userID)
	if !ok {
		writeMemoryOwnerError(w, http.StatusInternalServerError, "blocked")
		return
	}
	body := r.Body
	if body == nil {
		body = http.NoBody
	}
	finalPattern := []byte("\r\n--" + boundary + "--\r\n")
	tracked := newChatGPTOwnerTailReader(body, len(finalPattern), finalPattern)
	prefix := []byte("--" + boundary + "\r\n")
	initial := make([]byte, len(prefix))
	if _, err := io.ReadFull(tracked, initial); err != nil || !bytes.Equal(initial, prefix) {
		writeMemoryOwnerError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := h.importUpload(ctx, requestID, ownerID, actorID, boundary, initial, tracked)
	if err != nil {
		status, code := chatGPTOwnerErrorStatus(err)
		writeMemoryOwnerError(w, status, code)
		return
	}
	writeJSON(w, http.StatusOK, chatGPTImportOwnerViewResponse{ChatGPTImportView: result.View, IdempotentReplay: result.IdempotentReplay})
}

func chatGPTOwnerBoundary(r *http.Request) (string, error) {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return "", errChatGPTOwnerInvalid
	}
	mediaType, params, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || len(params) != 1 {
		return "", errChatGPTOwnerInvalid
	}
	boundary := params["boundary"]
	if boundary == "" {
		return "", errChatGPTOwnerInvalid
	}
	return boundary, nil
}

func (h *memoryChatGPTOwnerHandler) importUpload(ctx context.Context, requestID, ownerID, actorID, boundary string, initial []byte, tracked *chatGPTOwnerTailReader) (result chatgptimport.ImportResult, err error) {
	stageID := "chatgpt-" + uuid.NewString()
	stage, err := chatgptimport.CreateUploadStage(h.rawSourceRoot, stageID)
	if err != nil {
		return chatgptimport.ImportResult{}, errChatGPTOwnerFilesystem
	}
	defer func() {
		err = finishChatGPTOwnerStage(stage, err)
	}()

	reader := multipart.NewReader(io.MultiReader(bytes.NewReader(initial), tracked), boundary)
	apply, err := h.readMultipartUpload(reader, stage)
	if err != nil {
		return chatgptimport.ImportResult{}, err
	}
	if _, err := io.Copy(io.Discard, tracked); err != nil {
		return chatgptimport.ImportResult{}, errChatGPTOwnerInvalid
	}
	finalPattern := []byte("\r\n--" + boundary + "--\r\n")
	if tracked.finalCount != 1 || tracked.bytesAfterFinal != 0 || !bytes.Equal(tracked.tailBytes(), finalPattern) {
		return chatgptimport.ImportResult{}, errChatGPTOwnerInvalid
	}
	stageRoot, manifestPath, artifactPath, err := stage.Paths()
	if err != nil {
		return chatgptimport.ImportResult{}, errChatGPTOwnerFilesystem
	}
	return h.service.Import(ctx, chatgptimport.ImportRequest{
		RequestID: requestID, OwnerID: ownerID, ActorID: actorID,
		StageRoot: stageRoot, ManifestPath: manifestPath, ArtifactPath: artifactPath,
		Apply: apply,
	})
}

type chatGPTOwnerStageCloser interface {
	Close() error
}

func finishChatGPTOwnerStage(stage chatGPTOwnerStageCloser, resultErr error) error {
	if stage == nil {
		return resultErr
	}
	closeErr := stage.Close()
	if resultErr == nil {
		return nil
	}
	if closeErr == nil {
		return resultErr
	}
	return errors.Join(resultErr, errChatGPTOwnerFilesystem)
}

func (h *memoryChatGPTOwnerHandler) readMultipartUpload(reader *multipart.Reader, stage *chatgptimport.UploadStage) (bool, error) {
	parts := []struct {
		name      string
		mediaType string
	}{
		{name: "apply", mediaType: "text/plain"},
		{name: "manifest", mediaType: "application/json"},
		{name: "artifact", mediaType: "application/x-tar"},
	}
	var applyBody []byte
	for index, expected := range parts {
		part, err := reader.NextRawPart()
		if err != nil {
			return false, errChatGPTOwnerInvalid
		}
		if err := validateChatGPTOwnerPart(part, expected.name, expected.mediaType); err != nil {
			return false, errChatGPTOwnerInvalid
		}
		switch index {
		case 0:
			body, err := io.ReadAll(io.LimitReader(part, 6))
			if err != nil {
				return false, errChatGPTOwnerInvalid
			}
			if len(body) > 5 || (string(body) != "true" && string(body) != "false") {
				return false, errChatGPTOwnerInvalid
			}
			applyBody = body
		case 1:
			if err := writeChatGPTOwnerStageFile(stage.CreateManifest, part, h.limits.ManifestMaxBytes); err != nil {
				return false, err
			}
		case 2:
			if err := writeChatGPTOwnerStageFile(stage.CreateArtifact, part, h.limits.ArtifactMaxBytes); err != nil {
				return false, err
			}
		}
	}
	part, err := reader.NextRawPart()
	if err != io.EOF || part != nil {
		return false, errChatGPTOwnerInvalid
	}
	return string(applyBody) == "true", nil
}

func validateChatGPTOwnerPart(part *multipart.Part, expectedName, expectedMediaType string) error {
	if part == nil || len(part.Header) != 2 {
		return errChatGPTOwnerInvalid
	}
	var disposition, contentType string
	for key, values := range part.Header {
		if len(values) != 1 {
			return errChatGPTOwnerInvalid
		}
		switch {
		case strings.EqualFold(key, "Content-Disposition"):
			if disposition != "" {
				return errChatGPTOwnerInvalid
			}
			disposition = values[0]
		case strings.EqualFold(key, "Content-Type"):
			if contentType != "" {
				return errChatGPTOwnerInvalid
			}
			contentType = values[0]
		default:
			return errChatGPTOwnerInvalid
		}
	}
	mediaType, params, err := mime.ParseMediaType(disposition)
	if err != nil || !strings.EqualFold(mediaType, "form-data") || len(params) != 1 || params["name"] != expectedName {
		return errChatGPTOwnerInvalid
	}
	mediaType, params, err = mime.ParseMediaType(contentType)
	if err != nil || !strings.EqualFold(mediaType, expectedMediaType) || len(params) != 0 {
		return errChatGPTOwnerInvalid
	}
	return nil
}

type chatGPTOwnerStageCreateFile func() (*os.File, error)

func writeChatGPTOwnerStageFile(create chatGPTOwnerStageCreateFile, part *multipart.Part, maxBytes int64) error {
	if create == nil || part == nil || maxBytes < 1 {
		return errChatGPTOwnerFilesystem
	}
	file, err := create()
	if err != nil {
		return errChatGPTOwnerFilesystem
	}
	var result error
	count, copyErr := io.Copy(&chatGPTOwnerFileWriter{file: file}, io.LimitReader(part, maxBytes+1))
	if copyErr != nil {
		if errors.Is(copyErr, errChatGPTOwnerFilesystem) {
			result = errChatGPTOwnerFilesystem
		} else {
			result = errChatGPTOwnerInvalid
		}
	} else if count > maxBytes {
		result = errChatGPTOwnerTooLarge
	}
	if result == nil {
		if err := file.Sync(); err != nil {
			result = errChatGPTOwnerFilesystem
		}
	}
	if closeErr := file.Close(); result == nil && closeErr != nil {
		result = errChatGPTOwnerFilesystem
	}
	return result
}

type chatGPTOwnerFileWriter struct {
	file *os.File
}

func (writer *chatGPTOwnerFileWriter) Write(data []byte) (int, error) {
	count, err := writer.file.Write(data)
	if err != nil {
		return count, errChatGPTOwnerFilesystem
	}
	return count, nil
}

type chatGPTOwnerTailReader struct {
	reader          io.Reader
	max             int
	tail            []byte
	finalPattern    []byte
	scan            []byte
	total           int64
	finalCount      int
	firstFinalEnd   int64
	bytesAfterFinal int64
}

func newChatGPTOwnerTailReader(reader io.Reader, maxTail int, finalPattern []byte) *chatGPTOwnerTailReader {
	if maxTail < 1 {
		maxTail = 1
	}
	return &chatGPTOwnerTailReader{reader: reader, max: maxTail, finalPattern: append([]byte(nil), finalPattern...)}
}

func (reader *chatGPTOwnerTailReader) Read(data []byte) (int, error) {
	count, err := reader.reader.Read(data)
	if count > 0 {
		reader.tail = append(reader.tail, data[:count]...)
		if len(reader.tail) > reader.max {
			reader.tail = append([]byte(nil), reader.tail[len(reader.tail)-reader.max:]...)
		}
		reader.observeFinal(data[:count])
	}
	return count, err
}

func (reader *chatGPTOwnerTailReader) observeFinal(data []byte) {
	if len(reader.finalPattern) == 0 {
		reader.total += int64(len(data))
		return
	}
	previous := len(reader.scan)
	combined := make([]byte, 0, previous+len(data))
	combined = append(combined, reader.scan...)
	combined = append(combined, data...)
	base := reader.total - int64(previous)
	searchFrom := 0
	for {
		index := bytes.Index(combined[searchFrom:], reader.finalPattern)
		if index < 0 {
			break
		}
		index += searchFrom
		start := base + int64(index)
		reader.finalCount++
		if reader.firstFinalEnd == 0 {
			reader.firstFinalEnd = start + int64(len(reader.finalPattern))
		}
		searchFrom = index + 1
		if searchFrom >= len(combined) {
			break
		}
	}
	reader.total += int64(len(data))
	if reader.firstFinalEnd > 0 && reader.total > reader.firstFinalEnd {
		reader.bytesAfterFinal = reader.total - reader.firstFinalEnd
	}
	keep := len(reader.finalPattern) - 1
	if keep < 0 {
		keep = 0
	}
	if len(combined) > keep {
		combined = combined[len(combined)-keep:]
	}
	reader.scan = append(reader.scan[:0], combined...)
}

func (reader *chatGPTOwnerTailReader) tailBytes() []byte {
	return append([]byte(nil), reader.tail...)
}

type chatGPTImportOwnerViewResponse struct {
	domainmemory.ChatGPTImportView
	IdempotentReplay bool `json:"idempotent_replay"`
}

func writeChatGPTOwnerStoreError(w http.ResponseWriter, err error) {
	status, code := chatGPTOwnerErrorStatus(err)
	writeMemoryOwnerError(w, status, code)
}

func chatGPTOwnerErrorStatus(err error) (int, string) {
	if err == nil {
		return http.StatusInternalServerError, "internal"
	}
	// Classify the domain error before the deferred staging cleanup error. A
	// cleanup failure after a rejected import must not hide its terminal HTTP
	// classification.
	switch domainmemory.ChatGPTImportErrorCodeOf(err) {
	case domainmemory.ChatGPTImportErrorTooLarge:
		return http.StatusRequestEntityTooLarge, "too_large"
	case domainmemory.ChatGPTImportErrorArtifactInvalid:
		return http.StatusUnprocessableEntity, "artifact_invalid"
	case domainmemory.ChatGPTImportErrorForbidden:
		return http.StatusForbidden, "forbidden"
	case domainmemory.ChatGPTImportErrorNotFound:
		return http.StatusNotFound, "not_found"
	case domainmemory.ChatGPTImportErrorConflict:
		return http.StatusConflict, "conflict"
	case domainmemory.ChatGPTImportErrorSourceChanged:
		return http.StatusConflict, "source_changed"
	case domainmemory.ChatGPTImportErrorInvalid:
		return http.StatusBadRequest, "invalid_request"
	case domainmemory.ChatGPTImportErrorUnavailable:
		return http.StatusServiceUnavailable, "unavailable"
	case domainmemory.ChatGPTImportErrorInternal:
		return http.StatusInternalServerError, "internal"
	}
	switch {
	case errors.Is(err, errChatGPTOwnerTooLarge), errors.Is(err, domainmemory.ErrChatGPTImportTooLarge):
		return http.StatusRequestEntityTooLarge, "too_large"
	case errors.Is(err, errChatGPTOwnerInvalid), errors.Is(err, domainmemory.ErrChatGPTImportInvalid), errors.Is(err, domainmemory.ErrCommonRawInvalid), errors.Is(err, domainmemory.ErrCommonRawSchema):
		return http.StatusBadRequest, "invalid_request"
	case errors.Is(err, domainmemory.ErrChatGPTImportForbidden), errors.Is(err, domainmemory.ErrCommonRawForbidden):
		return http.StatusForbidden, "forbidden"
	case errors.Is(err, domainmemory.ErrChatGPTImportNotFound):
		return http.StatusNotFound, "not_found"
	case errors.Is(err, domainmemory.ErrChatGPTImportConflict), errors.Is(err, domainmemory.ErrChatGPTImportSourceChanged), errors.Is(err, domainmemory.ErrCommonRawConflict), errors.Is(err, domainmemory.ErrCommonRawSourceChanged):
		if errors.Is(err, domainmemory.ErrChatGPTImportSourceChanged) || errors.Is(err, domainmemory.ErrCommonRawSourceChanged) || domainmemory.ChatGPTImportErrorCodeOf(err) == domainmemory.ChatGPTImportErrorSourceChanged {
			return http.StatusConflict, "source_changed"
		}
		return http.StatusConflict, "conflict"
	case errors.Is(err, domainmemory.ErrChatGPTImportArtifactInvalid), errors.Is(err, domainmemory.ErrCommonRawObject):
		return http.StatusUnprocessableEntity, "artifact_invalid"
	}
	switch {
	case errors.Is(err, domainmemory.ErrChatGPTImportUnavailable), errors.Is(err, domainmemory.ErrCommonRawUnavailable), errors.Is(err, domainmemory.ErrCommonRawRoot), errors.Is(err, errChatGPTOwnerFilesystem):
		return http.StatusServiceUnavailable, "unavailable"
	}
	return http.StatusInternalServerError, "internal"
}
