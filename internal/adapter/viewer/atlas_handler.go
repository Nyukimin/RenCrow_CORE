package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	appbacklog "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	featurebacklog "github.com/Nyukimin/RenCrow_CORE/internal/features/backlog"
)

const (
	atlasReadRoot  = "/viewer/atlas"
	atlasOwnerRoot = "/v1/atlas"
)

// AtlasOwnerService is the CORE-owned contract used by the Viewer adapter.
// Keeping the adapter on this contract makes it impossible for the HTTP layer
// to bypass the lifecycle service or to make verification/state decisions.
type AtlasOwnerService interface {
	Projection(context.Context) (appbacklog.Projection, error)
	List(context.Context, int) ([]domainbacklog.Item, error)
	Get(context.Context, string) (domainbacklog.Item, error)
	Evidence(context.Context, string) ([]domainbacklog.EvidenceRef, error)
	ListQueueFreezes(context.Context, int) ([]domainworkstream.QueueFreeze, error)
	Intake(context.Context, appbacklog.IntakeRequest) (appbacklog.IntakeResult, error)
	Candidate(context.Context, string) (domainbacklog.Item, error)
	Adopt(context.Context, string, string) (appbacklog.AdoptionResult, error)
	Defer(context.Context, string, string) (domainbacklog.Item, error)
	Reject(context.Context, string, string) (domainbacklog.Item, error)
	Revise(context.Context, string, appbacklog.ReviseRequest) (domainbacklog.Item, error)
	ResolveQueueFreeze(context.Context, string, appbacklog.ResolveQueueFreezeRequest) (domainworkstream.QueueFreeze, domainworkstream.ImplementationLease, bool, error)
}

// NewAtlasHandler serves both the read-only Debug Viewer projection and the
// authenticated owner mutation surface. GET intentionally remains compatible
// with the existing local Debug Viewer; every POST is bearer/profile gated.
func NewAtlasHandler(service AtlasOwnerService, userID string, token []byte) http.HandlerFunc {
	h := &atlasHandler{service: service, userID: strings.TrimSpace(userID), token: append([]byte(nil), token...)}
	return h.ServeHTTP
}

func HandleAtlas(service AtlasOwnerService) http.HandlerFunc {
	return (&atlasHandler{service: service}).ServeHTTP
}

type atlasHandler struct {
	service AtlasOwnerService
	userID  string
	token   []byte
}

func (h *atlasHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if !memoryOwnerLoopback(r) {
			writeAtlasError(w, http.StatusNotFound, "not_found")
			return
		}
		if !memoryOwnerBearerAuthorized(r, h.token) {
			writeAtlasError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !memoryOwnerClientProfileAllowed(r) {
			writeAtlasError(w, http.StatusForbidden, "forbidden")
			return
		}
		ctx, err := memoryOwnerOwnerContext(r.Context(), h.userID)
		if err != nil {
			writeAtlasError(w, http.StatusInternalServerError, "scope_unavailable")
			return
		}
		r = r.WithContext(ctx)
	}
	if h.service == nil {
		writeAtlasError(w, http.StatusServiceUnavailable, "atlas_unavailable")
		return
	}
	if r.Method == http.MethodGet {
		h.read(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeAtlasError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	h.write(w, r)
}

func (h *atlasHandler) read(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == atlasReadRoot {
		projection, err := h.service.Projection(r.Context())
		if err != nil {
			writeAtlasError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, projection)
		return
	}
	if path == atlasReadRoot+"/items" {
		items, err := h.service.List(r.Context(), atlasLimit(r))
		if err != nil {
			writeAtlasError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
		return
	}
	if strings.HasPrefix(path, atlasReadRoot+"/items/") {
		id, ok := atlasSinglePathID(path, atlasReadRoot+"/items/")
		if !ok {
			writeAtlasError(w, http.StatusNotFound, "not_found")
			return
		}
		item, err := h.service.Get(r.Context(), id)
		if err != nil {
			writeAtlasError(w, http.StatusNotFound, "not_found")
			return
		}
		response := map[string]any{"item": item}
		if len(item.SpecificationRefs) > 0 {
			pkg, loadErr := featurebacklog.LoadBackfillPackage()
			if loadErr != nil {
				writeAtlasError(w, http.StatusInternalServerError, "specification_unavailable")
				return
			}
			resolved := make([]domainbacklog.SpecificationArtifact, 0, len(item.SpecificationRefs))
			for _, specID := range item.SpecificationRefs {
				if artifact, ok := pkg.Specification(specID); ok {
					resolved = append(resolved, artifact)
				}
			}
			response["resolved_specifications"] = resolved
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	if strings.HasPrefix(path, atlasReadRoot+"/specifications/") {
		specID, ok := atlasSinglePathID(path, atlasReadRoot+"/specifications/")
		if !ok {
			writeAtlasError(w, http.StatusNotFound, "not_found")
			return
		}
		pkg, err := featurebacklog.LoadBackfillPackage()
		if err != nil {
			writeAtlasError(w, http.StatusInternalServerError, "specification_unavailable")
			return
		}
		artifact, ok := pkg.Specification(specID)
		if !ok {
			writeAtlasError(w, http.StatusNotFound, "not_found")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"specification": artifact})
		return
	}
	if strings.HasPrefix(path, atlasReadRoot+"/evidence/") {
		unitID, ok := atlasSinglePathID(path, atlasReadRoot+"/evidence/")
		if !ok {
			writeAtlasError(w, http.StatusNotFound, "not_found")
			return
		}
		evidence, err := h.service.Evidence(r.Context(), unitID)
		if err != nil {
			writeAtlasError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"evidence": evidence})
		return
	}
	if path == atlasReadRoot+"/queue-freezes" {
		freezes, err := h.service.ListQueueFreezes(r.Context(), atlasLimit(r))
		if err != nil {
			writeAtlasError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"queue_freezes": freezes})
		return
	}
	projection, err := h.service.Projection(r.Context())
	if err != nil {
		writeAtlasError(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch path {
	case atlasReadRoot + "/radar":
		writeJSON(w, http.StatusOK, map[string]any{"items": projection.Radar})
	case atlasReadRoot + "/backlog":
		writeJSON(w, http.StatusOK, map[string]any{"items": projection.Backlog})
	case atlasReadRoot + "/queue":
		writeJSON(w, http.StatusOK, map[string]any{"items": projection.Queue})
	case atlasReadRoot + "/active":
		writeJSON(w, http.StatusOK, map[string]any{"active": projection.Active})
	default:
		writeAtlasError(w, http.StatusNotFound, "not_found")
	}
}

func atlasLimit(r *http.Request) int {
	// Projection size is bounded at the service boundary; malformed limits are
	// deliberately treated as the default for compatibility with old Viewer GET.
	return 500
}

func (h *atlasHandler) write(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == atlasOwnerRoot+"/intake" {
		var request appbacklog.IntakeRequest
		if err := decodeAtlasJSON(w, r, &request, 128<<10); err != nil {
			writeAtlasError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		result, err := h.service.Intake(r.Context(), request)
		if err != nil {
			writeAtlasDomainError(w, err)
			return
		}
		status := http.StatusCreated
		if result.Duplicate {
			status = http.StatusOK
		}
		writeJSON(w, status, result)
		return
	}
	freezePrefix := atlasOwnerRoot + "/queue-freezes/"
	if strings.HasPrefix(path, freezePrefix) {
		tail := strings.TrimPrefix(path, freezePrefix)
		parts := strings.Split(tail, "/")
		if len(parts) != 2 || strings.ToLower(strings.TrimSpace(parts[1])) != "resolve" {
			writeAtlasError(w, http.StatusNotFound, "not_found")
			return
		}
		freezeID, ok := atlasSinglePathID(freezePrefix+parts[0], freezePrefix)
		if !ok {
			writeAtlasError(w, http.StatusNotFound, "not_found")
			return
		}
		var request appbacklog.ResolveQueueFreezeRequest
		if err := decodeAtlasJSON(w, r, &request, 64<<10); err != nil {
			writeAtlasError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		freeze, lease, acquired, err := h.service.ResolveQueueFreeze(r.Context(), freezeID, request)
		if err != nil {
			writeAtlasQueueFreezeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"freeze": freeze, "lease": lease, "acquired": acquired})
		return
	}
	prefix := atlasOwnerRoot + "/items/"
	if !strings.HasPrefix(path, prefix) {
		writeAtlasError(w, http.StatusNotFound, "not_found")
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		writeAtlasError(w, http.StatusNotFound, "not_found")
		return
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil || strings.TrimSpace(id) == "" {
		writeAtlasError(w, http.StatusNotFound, "not_found")
		return
	}
	operation := strings.ToLower(strings.TrimSpace(parts[1]))
	switch operation {
	case "candidate":
		item, err := h.service.Candidate(r.Context(), id)
		h.writeMutation(w, item, err)
	case "adopt", "defer", "reject":
		var request struct {
			Reason string `json:"reason"`
		}
		if err := decodeAtlasJSON(w, r, &request, 16<<10); err != nil {
			writeAtlasError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		if strings.TrimSpace(request.Reason) == "" {
			writeAtlasError(w, http.StatusBadRequest, "reason_required")
			return
		}
		var item domainbacklog.Item
		switch operation {
		case "adopt":
			result, err := h.service.Adopt(r.Context(), id, request.Reason)
			if err != nil {
				writeAtlasDomainError(w, err)
				return
			}
			writeJSON(w, http.StatusOK, result)
			return
		case "defer":
			item, err = h.service.Defer(r.Context(), id, request.Reason)
		case "reject":
			item, err = h.service.Reject(r.Context(), id, request.Reason)
		}
		h.writeMutation(w, item, err)
	case "revise":
		var request struct {
			RequestID           string                      `json:"request_id,omitempty"`
			ExpectedRevision    int                         `json:"expected_revision,omitempty"`
			DeliveryState       string                      `json:"delivery_state"`
			TargetDeliveryState string                      `json:"target_delivery_state"`
			EvidenceRefs        []domainbacklog.EvidenceRef `json:"evidence_refs"`
			Reason              string                      `json:"reason,omitempty"`
		}
		if err := decodeAtlasJSON(w, r, &request, 64<<10); err != nil {
			writeAtlasError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		target := request.DeliveryState
		if strings.TrimSpace(target) == "" {
			target = request.TargetDeliveryState
		}
		if strings.TrimSpace(target) == "" || len(request.EvidenceRefs) == 0 {
			writeAtlasError(w, http.StatusBadRequest, "delivery_state_and_evidence_required")
			return
		}
		item, err := h.service.Revise(r.Context(), id, appbacklog.ReviseRequest{
			RequestID: request.RequestID, ExpectedRevision: request.ExpectedRevision,
			TargetDeliveryState: target, EvidenceRefs: request.EvidenceRefs, Reason: request.Reason,
		})
		h.writeMutation(w, item, err)
	default:
		writeAtlasError(w, http.StatusNotFound, "not_found")
	}
}

func (h *atlasHandler) writeMutation(w http.ResponseWriter, item domainbacklog.Item, err error) {
	if err != nil {
		writeAtlasDomainError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"item": item})
}

func decodeAtlasJSON(w http.ResponseWriter, r *http.Request, target any, maxBytes int64) error {
	if r.Body == nil {
		return errors.New("request body is required")
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func atlasSinglePathID(path, prefix string) (string, bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", false
	}
	raw := strings.TrimPrefix(path, prefix)
	if raw == "" || strings.Contains(raw, "/") {
		return "", false
	}
	id, err := url.PathUnescape(raw)
	id = strings.TrimSpace(id)
	if err != nil || id == "" || strings.ContainsAny(id, `/\\`) || id == "." || id == ".." || strings.Contains(id, "../") {
		return "", false
	}
	return id, true
}

func writeAtlasDomainError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, domainbacklog.ErrInvalidTransition) || errors.Is(err, domainbacklog.ErrEvidenceRequired) {
		status = http.StatusConflict
	}
	writeAtlasError(w, status, err.Error())
}

func writeAtlasQueueFreezeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, domainworkstream.ErrQueueFreezeNotFound):
		status = http.StatusNotFound
	case errors.Is(err, domainworkstream.ErrQueueFreezeRevisionConflict),
		errors.Is(err, domainworkstream.ErrQueueFreezeResolutionConflict),
		errors.Is(err, appbacklog.ErrLifecycleConflict),
		errors.Is(err, domainbacklog.ErrInvalidTransition),
		errors.Is(err, domainbacklog.ErrEvidenceRequired):
		status = http.StatusConflict
	case errors.Is(err, domainworkstream.ErrQueueFrozen) || strings.Contains(strings.ToLower(err.Error()), "blocked"):
		// A blocked/frozen queue is an explicit resource state, not a malformed
		// request. HTTP 423 keeps that distinction visible to owner clients.
		status = http.StatusLocked
	}
	writeAtlasError(w, status, err.Error())
}

func writeAtlasError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": strings.TrimSpace(message)})
}
