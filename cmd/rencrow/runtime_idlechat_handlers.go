package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	idlechat "github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
)

type legacyIdleChatStartDisabledResponse struct {
	OK        bool   `json:"ok"`
	ErrorCode string `json:"error_code"`
	Error     string `json:"error"`
}

func rejectLegacyIdleChatStart(w http.ResponseWriter) {
	writeJSONStatus(w, http.StatusConflict, legacyIdleChatStartDisabledResponse{
		OK:        false,
		ErrorCode: "legacy_idlechat_start_disabled",
		Error:     "legacy IdleChat start routes are disabled; use prepared episode playback",
	})
}

func (d *Dependencies) handleIdleChatStart() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rejectLegacyIdleChatStart(w)
	}
}

func (d *Dependencies) handleIdleChatStop() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if d.surfacePresence != nil {
			d.surfacePresence.StopExplicit()
		} else {
			d.idleChatOrch.StopManualMode()
			resetIdleChatTTSQueue()
		}
		writeJSON(w, map[string]any{
			"ok":            true,
			"mode":          d.idleChatOrch.CurrentMode(),
			"manual_mode":   d.idleChatOrch.IsManualMode(),
			"disabled":      d.idleChatOrch.IsDisabled(),
			"chat_active":   d.idleChatOrch.IsChatActive(),
			"current_topic": d.idleChatOrch.CurrentTopic(),
		})
	}
}

type surfacePresenceRequest struct {
	ViewerClientID string `json:"viewer_client_id"`
	Surface        string `json:"surface"`
	Action         string `json:"action"`
}

func (d *Dependencies) handleSurfacePresence() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.surfacePresence == nil {
			http.Error(w, "surface presence is unavailable", http.StatusServiceUnavailable)
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid surface presence request: "+err.Error(), http.StatusBadRequest)
			return
		}
		var envelopeFields map[string]json.RawMessage
		if err := json.Unmarshal(body, &envelopeFields); err != nil {
			http.Error(w, "invalid surface presence request: "+err.Error(), http.StatusBadRequest)
			return
		}
		client, profile, ok := surfacePresenceRequestProfile(r)
		if !ok {
			http.Error(w, "surface presence client/profile is not allowed", http.StatusForbidden)
			return
		}
		principal, ok := surfacePresenceRequestPrincipal(r)
		if !ok {
			http.Error(w, "surface presence actor is invalid", http.StatusForbidden)
			return
		}
		protocolVersion := 1
		if versionJSON, specified := envelopeFields["protocol_version"]; specified {
			if err := json.Unmarshal(versionJSON, &protocolVersion); err != nil || (protocolVersion != 1 && protocolVersion != surfacePresenceProtocolVersionV2) || string(bytes.TrimSpace(versionJSON)) == "null" {
				writeSurfacePresenceError(w, newSurfacePresenceAPIError(http.StatusBadRequest, "protocol_version_unsupported", "unsupported protocol_version"), 2)
				return
			}
		}
		if profile == "portal-other" && protocolVersion != surfacePresenceProtocolVersionV2 {
			http.Error(w, "portal-other requires surface presence V2", http.StatusBadRequest)
			return
		}
		if protocolVersion == surfacePresenceProtocolVersionV2 {
			req, err := decodeSurfacePresenceV2Request(body)
			if err != nil {
				writeSurfacePresenceError(w, newSurfacePresenceAPIError(http.StatusBadRequest, "invalid_request", err.Error()), 2)
				return
			}
			if !validSurfacePresenceViewerClientIDV2(req.ViewerClientID) {
				writeSurfacePresenceError(w, newSurfacePresenceAPIError(http.StatusBadRequest, "invalid_viewer_client_id", "viewer_client_id must be 1 to 128 UTF-8 bytes"), 2)
				return
			}
			if req.Action != "register" && !surfacePresenceProfileAllows(client, profile, req.Surface) {
				writeSurfacePresenceError(w, newSurfacePresenceAPIError(http.StatusForbidden, "profile_surface_mismatch", "interaction profile does not allow this surface"), 2)
				return
			}
			identity := surfacePresenceIdentity{principal: principal, clientType: client, viewerClientID: req.ViewerClientID}
			if req.Action == "register" {
				response, err := d.surfacePresence.RegisterV2(identity)
				if err != nil {
					writeSurfacePresenceError(w, err, 2)
					return
				}
				writeJSON(w, response)
				return
			}
			response, err := d.surfacePresence.UpdateV2(identity, req)
			if err != nil {
				writeSurfacePresenceError(w, err, 2)
				return
			}
			writeJSON(w, response)
			return
		}

		var req surfacePresenceRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "invalid surface presence request: "+err.Error(), http.StatusBadRequest)
			return
		}
		req.ViewerClientID = strings.TrimSpace(req.ViewerClientID)
		req.Surface = strings.ToLower(strings.TrimSpace(req.Surface))
		req.Action = strings.ToLower(strings.TrimSpace(req.Action))
		if !validSurfacePresenceViewerClientID(req.ViewerClientID) {
			http.Error(w, "viewer_client_id is required and must be at most 256 bytes", http.StatusBadRequest)
			return
		}
		if req.Surface != "chat" && req.Surface != "idlechat" {
			http.Error(w, "surface must be chat or idlechat", http.StatusBadRequest)
			return
		}
		if req.Action != "claim" && req.Action != "heartbeat" && req.Action != "release" {
			http.Error(w, "action must be claim, heartbeat, or release", http.StatusBadRequest)
			return
		}
		if !surfacePresenceProfileAllows(client, profile, req.Surface) {
			http.Error(w, "interaction profile does not match surface", http.StatusForbidden)
			return
		}

		identity := surfacePresenceIdentity{principal: principal, clientType: client, viewerClientID: req.ViewerClientID}
		snapshot, err := d.surfacePresence.UpdateLegacy(identity, req.Surface, req.Action)
		if err != nil {
			writeSurfacePresenceError(w, err, 1)
			return
		}
		writeJSON(w, map[string]any{
			"ok":                      true,
			"surface":                 req.Surface,
			"action":                  req.Action,
			"effective_mode":          snapshot.EffectiveMode,
			"idlechat_active":         snapshot.IdleChatActive,
			"chat_presence_count":     snapshot.ChatPresenceCount,
			"idlechat_presence_count": snapshot.IdleChatPresenceCount,
			"lease_expires_at":        snapshot.LeaseExpiresAt,
		})
	}
}

func surfacePresenceRequestProfile(r *http.Request) (client, profile string, ok bool) {
	if r == nil {
		return "", "", false
	}
	clientValues := headerValuesCaseInsensitive(r.Header, "X-RenCrow-Client")
	profileValues := headerValuesCaseInsensitive(r.Header, interactionProfileHeader)
	if len(clientValues) != 1 || len(profileValues) != 1 {
		return "", "", false
	}
	client = strings.TrimSpace(clientValues[0])
	profile = strings.ToLower(strings.TrimSpace(profileValues[0]))
	if client == "" || profile == "" || !surfacePresenceProfileAllowsClient(client, profile) {
		return "", "", false
	}
	return client, profile, true
}

func surfacePresenceRequestPrincipal(r *http.Request) (string, bool) {
	values := headerValuesCaseInsensitive(r.Header, "X-RenCrow-Authenticated-Actor")
	if len(values) > 1 {
		return "", false
	}
	if len(values) == 0 {
		return "local", true
	}
	principal := strings.TrimSpace(values[0])
	if !validTailscaleAuthenticatedActor(principal) || len(principal) > 128 {
		return "", false
	}
	for _, character := range principal {
		if character < 0x20 || character == 0x7f {
			return "", false
		}
	}
	return principal, true
}

func surfacePresenceProfileAllowsClient(client, profile string) bool {
	switch profile {
	case "portal-chat", "portal-idlechat", "portal-games", "portal-other":
		return client == "RenCrow_PORTAL"
	case "core-viewer":
		return client == "RenCrow_CORE"
	case "assistant-core":
		return client == "RenCrow_ASSISTANT"
	case "cmd-chat", "cmd-idlechat", "cmd-diagnostics", "cmd-control":
		return client == "RenCrow_CMD"
	default:
		return false
	}
}

func surfacePresenceProfileAllows(client, profile, surface string) bool {
	if !surfacePresenceProfileAllowsClient(client, profile) {
		return false
	}
	switch profile {
	case "portal-chat":
		return surface == "chat"
	case "portal-idlechat":
		return surface == "idlechat"
	case "portal-games":
		return surface == "games"
	case "portal-other":
		return surface == "other"
	case "core-viewer":
		return surface == "other"
	case "cmd-chat", "cmd-idlechat", "cmd-diagnostics", "cmd-control":
		return surface == "cmd"
	default:
		return false
	}
}

func decodeSurfacePresenceV2Request(body []byte) (surfacePresenceV2Request, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return surfacePresenceV2Request{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var req surfacePresenceV2Request
	if err := decoder.Decode(&req); err != nil {
		return surfacePresenceV2Request{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return surfacePresenceV2Request{}, fmt.Errorf("request must contain one JSON object")
	}
	for _, required := range []string{"protocol_version", "viewer_client_id", "action"} {
		if _, exists := fields[required]; !exists {
			return surfacePresenceV2Request{}, fmt.Errorf("%s is required", required)
		}
	}
	if req.ProtocolVersion != surfacePresenceProtocolVersionV2 {
		return surfacePresenceV2Request{}, fmt.Errorf("protocol_version must be 2")
	}
	if req.Action == "register" {
		for _, forbidden := range []string{"runtime_epoch", "registration_receipt", "surface", "update_seq", "claim_seq"} {
			if _, exists := fields[forbidden]; exists {
				return surfacePresenceV2Request{}, fmt.Errorf("register must not include %s", forbidden)
			}
		}
	} else {
		for _, required := range []string{"runtime_epoch", "registration_receipt", "surface", "update_seq"} {
			if _, exists := fields[required]; !exists {
				return surfacePresenceV2Request{}, fmt.Errorf("%s is required", required)
			}
		}
		if req.Action == "claim" {
			if _, exists := fields["claim_seq"]; exists {
				return surfacePresenceV2Request{}, fmt.Errorf("claim must not include claim_seq")
			}
		} else if req.Action == "heartbeat" || req.Action == "release" {
			if _, exists := fields["claim_seq"]; !exists {
				return surfacePresenceV2Request{}, fmt.Errorf("claim_seq is required")
			}
		} else {
			return surfacePresenceV2Request{}, fmt.Errorf("action must be register, claim, heartbeat, or release")
		}
	}
	req.Action = strings.ToLower(strings.TrimSpace(req.Action))
	req.Surface = strings.ToLower(strings.TrimSpace(req.Surface))
	return req, nil
}

func writeSurfacePresenceError(w http.ResponseWriter, err error, protocolVersion int) {
	var apiErr *surfacePresenceAPIError
	if errors.As(err, &apiErr) {
		if protocolVersion == surfacePresenceProtocolVersionV2 {
			writeJSONStatus(w, apiErr.status, map[string]any{
				"ok": false, "protocol_version": surfacePresenceProtocolVersionV2,
				"error_code": apiErr.code, "error": apiErr.message,
			})
			return
		}
		http.Error(w, apiErr.Error(), apiErr.status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func (d *Dependencies) handleIdleChatInterrupt() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		reason := "viewer_interrupt"
		var req struct {
			Reason           string `json:"reason"`
			Source           string `json:"source"`
			ClientGeneration string `json:"client_generation"`
		}
		if strings.Contains(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			_ = json.NewDecoder(r.Body).Decode(&req)
		}
		if strings.TrimSpace(req.Reason) != "" {
			reason = strings.TrimSpace(req.Reason)
		}
		d.idleChatOrch.Interrupt(reason)
		resetIdleChatTTSQueue()
		writeJSON(w, map[string]any{
			"ok":            true,
			"interrupted":   true,
			"mode":          d.idleChatOrch.CurrentMode(),
			"manual_mode":   d.idleChatOrch.IsManualMode(),
			"disabled":      d.idleChatOrch.IsDisabled(),
			"chat_active":   d.idleChatOrch.IsChatActive(),
			"current_topic": d.idleChatOrch.CurrentTopic(),
		})
	}
}

func (d *Dependencies) handleIdleChatStatus() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if !d.idleChatOrch.IsChatActive() {
			clearTTSPublicSequenceStateIfNoRoutes()
		}
		activeSessionID, activeTranscript := d.idleChatOrch.ActiveSessionTranscript(100)
		writeJSON(w, map[string]any{
			"ok":                   true,
			"mode":                 d.idleChatOrch.CurrentMode(),
			"manual_mode":          d.idleChatOrch.IsManualMode(),
			"disabled":             d.idleChatOrch.IsDisabled(),
			"chat_active":          d.idleChatOrch.IsChatActive(),
			"current_topic":        d.idleChatOrch.CurrentTopic(),
			"topic_stock_playback": d.idleChatOrch.TopicStockPlaybackSnapshot(),
			"active_session_id":    activeSessionID,
			"active_transcript":    activeTranscript,
			"watchdog":             d.idleChatOrch.WatchdogSnapshot(time.Now().UTC()),
			"word_topic_stock":     d.idleChatOrch.WordTopicStockSnapshot(),
			"forecast_stock":       d.idleChatOrch.ForecastTopicStockSnapshot(),
			"episode_stock":        d.idleChatOrch.StoryEpisodeStockSnapshot(),
			"llm_busy":             d.snapshotLLMBusy(),
			"tts_pending":          snapshotIdleChatTTSPending(),
			"tts_public":           snapshotTTSPublicSessions(),
		})
	}
}

type idleChatPlaybackRequest struct {
	ProtocolVersion       int    `json:"protocol_version"`
	ViewerClientID        string `json:"viewer_client_id"`
	RuntimeEpoch          string `json:"runtime_epoch"`
	RegistrationReceipt   string `json:"registration_receipt"`
	ClaimSeq              uint64 `json:"claim_seq"`
	ExpectedStateRevision uint64 `json:"expected_state_revision"`
	Action                string `json:"action"`
	ItemID                string `json:"item_id"`
}

func (d *Dependencies) handleIdleChatPlayback() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if d.surfacePresence == nil {
			http.Error(w, "surface presence is unavailable", http.StatusServiceUnavailable)
			return
		}
		client, profile, ok := surfacePresenceRequestProfile(r)
		if !ok || client != "RenCrow_PORTAL" || profile != "portal-idlechat" {
			http.Error(w, "IdleChat playback requires portal-idlechat", http.StatusForbidden)
			return
		}
		principal, ok := surfacePresenceRequestPrincipal(r)
		if !ok {
			http.Error(w, "surface presence actor is invalid", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		var req idleChatPlaybackRequest
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			http.Error(w, "invalid playback request: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			http.Error(w, "invalid playback request", http.StatusBadRequest)
			return
		}
		if req.ProtocolVersion != surfacePresenceProtocolVersionV2 || !validSurfacePresenceViewerClientIDV2(req.ViewerClientID) || req.RuntimeEpoch == "" || req.RegistrationReceipt == "" || req.ClaimSeq == 0 || req.ExpectedStateRevision == 0 {
			writeSurfacePresenceError(w, newSurfacePresenceAPIError(http.StatusBadRequest, "invalid_playback_request", "V2 presence fields are required"), 2)
			return
		}
		action := strings.ToLower(strings.TrimSpace(req.Action))
		if action != "play" && action != "next" && action != "previous" {
			http.Error(w, "action must be play, next, or previous", http.StatusBadRequest)
			return
		}
		identity := surfacePresenceIdentity{principal: principal, clientType: client, viewerClientID: req.ViewerClientID}
		guard := func() error {
			_, err := d.surfacePresence.ValidatePlayback(identity, req.RuntimeEpoch, req.RegistrationReceipt, req.ClaimSeq, req.ExpectedStateRevision)
			return err
		}
		snapshot, err := d.idleChatOrch.StartTopicStockPlaybackWithGuard(action, strings.TrimSpace(req.ItemID), guard)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, idlechat.ErrTopicStockEmpty) || errors.Is(err, idlechat.ErrTopicStockNotFound) || errors.Is(err, idlechat.ErrTopicStockNoPrevious) || errors.Is(err, idlechat.ErrTopicStockPlaybackStale) || errors.Is(err, errPresencePlaybackChanged) || errors.Is(err, errPresenceRegistrationRequired) {
				status = http.StatusConflict
			}
			http.Error(w, err.Error(), status)
			return
		}
		if err := d.surfacePresence.MarkPlaybackStarted(identity, req.RuntimeEpoch, req.RegistrationReceipt, req.ClaimSeq, req.ExpectedStateRevision); err != nil {
			writeSurfacePresenceError(w, err, 2)
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "playback": snapshot,
			"mode": d.idleChatOrch.CurrentMode(), "chat_active": d.idleChatOrch.IsChatActive(),
			"current_topic": d.idleChatOrch.CurrentTopic(),
		})
	}
}

func (d *Dependencies) handleIdleChatCollection() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{
			"ok":         true,
			"collection": d.idleChatOrch.DailySeedCollectionSnapshot(time.Now()),
		})
	}
}

func (d *Dependencies) snapshotLLMBusy() llmBusySnapshot {
	if d == nil || d.llmBusyTracker == nil {
		return llmBusySnapshot{}
	}
	return d.llmBusyTracker.Snapshot()
}

func (d *Dependencies) handleIdleChatForecast() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rejectLegacyIdleChatStart(w)
	}
}

func (d *Dependencies) handleIdleChatStory() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rejectLegacyIdleChatStart(w)
	}
}

func (d *Dependencies) handleIdleChatStorySimple() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rejectLegacyIdleChatStart(w)
	}
}

func (d *Dependencies) handleIdleChatEpisodes() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		if episodeID := strings.TrimSpace(r.URL.Query().Get("episode_id")); episodeID != "" {
			episode, ok := d.idleChatOrch.StoryEpisode(episodeID)
			if !ok {
				http.Error(w, "episode not found", http.StatusNotFound)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "episode": episode})
			return
		}
		snapshot := d.idleChatOrch.StoryEpisodeStockSnapshot()
		episodes := make([]map[string]any, 0, len(snapshot.Episodes))
		for _, episode := range snapshot.Episodes {
			episodes = append(episodes, map[string]any{
				"episode_id": episode.EpisodeID, "revision": episode.Revision,
				"episode_kind": episode.EpisodeKind, "task_id": episode.TaskID, "run_id": episode.RunID,
				"story_title":                episode.StoryTitle,
				"replacement_for_episode_id": episode.ReplacementForEpisodeID,
				"source":                     episode.Source, "reader": episode.Reader, "listener": episode.Listener,
				"story_contract": episode.Contract, "production_status": episode.ProductionStatus,
				"validation": episode.Validation, "utterance_count": len(episode.Turns),
				"fixed_prefix_length": episode.FixedPrefixLength, "repair_from_turn": episode.RepairFromTurn,
				"suffix_regenerations": episode.SuffixRegenerations,
				"play_count":           episode.PlayCount, "last_played_at": episode.LastPlayedAt,
				"created_at": episode.CreatedAt, "updated_at": episode.UpdatedAt,
			})
		}
		writeJSON(w, map[string]any{
			"ok": true, "ready": snapshot.Ready, "target": snapshot.Target, "missing": snapshot.Missing,
			"needs_repair": snapshot.NeedsRepair, "failed": snapshot.Failed, "untitled_ready": snapshot.UntitledReady, "filling": snapshot.Filling,
			"generation_attempts": snapshot.GenerationAttempts, "last_failure_phase": snapshot.LastFailurePhase,
			"last_error": snapshot.LastError, "episodes": episodes,
		})
	}
}

func (d *Dependencies) handleIdleChatEpisodesPrepare() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		var request struct {
			Count      json.RawMessage `json:"count"`
			Categories []string        `json:"categories"`
		}
		if r.Body != nil {
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
			var rawRequest json.RawMessage
			decodeErr := decoder.Decode(&rawRequest)
			if decodeErr != nil && !errors.Is(decodeErr, io.EOF) {
				writeIdleChatEpisodePrepareError(w, http.StatusBadRequest, "idlechat_episode_prepare_request_invalid", "request must be one valid JSON object")
				return
			}
			if decodeErr == nil {
				trimmed := strings.TrimSpace(string(rawRequest))
				if len(trimmed) == 0 || trimmed[0] != '{' {
					writeIdleChatEpisodePrepareError(w, http.StatusBadRequest, "idlechat_episode_prepare_request_invalid", "request must be one valid JSON object")
					return
				}
				objectDecoder := json.NewDecoder(strings.NewReader(trimmed))
				objectDecoder.DisallowUnknownFields()
				if err := objectDecoder.Decode(&request); err != nil {
					writeIdleChatEpisodePrepareError(w, http.StatusBadRequest, "idlechat_episode_prepare_request_invalid", "request must be one valid JSON object")
					return
				}
			}
			var trailing any
			if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
				writeIdleChatEpisodePrepareError(w, http.StatusBadRequest, "idlechat_episode_prepare_request_invalid", "request must be one valid JSON object")
				return
			}
		}
		if len(request.Categories) > 0 {
			writeIdleChatEpisodePrepareError(w, http.StatusBadRequest, "idlechat_episode_prepare_categories_unsupported", "category filters are not supported")
			return
		}
		count := 0
		if len(request.Count) > 0 {
			rawCount := strings.TrimSpace(string(request.Count))
			if rawCount == "null" || json.Unmarshal(request.Count, &count) != nil || count < 1 || count > 10 {
				writeIdleChatEpisodePrepareError(w, http.StatusBadRequest, "idlechat_episode_prepare_count_invalid", "count must be an integer between 1 and 10")
				return
			}
		}
		snapshot := d.idleChatOrch.StoryEpisodeStockSnapshot()
		if !snapshot.Enabled {
			writeIdleChatEpisodePrepareError(w, http.StatusServiceUnavailable, "idlechat_episode_stock_unavailable", "story episode stock is unavailable")
			return
		}
		if len(request.Count) == 0 {
			count = snapshot.Missing
			if count == 0 {
				writeJSONStatus(w, http.StatusOK, map[string]any{
					"ok": true, "state": "ready", "target_ready_count": snapshot.Ready, "episode_stock": snapshot,
				})
				return
			}
		}
		target := snapshot.Ready + count
		state, accepted := d.idleChatOrch.RequestNightStoryPreparation(target)
		if !accepted {
			writeIdleChatEpisodePrepareError(w, http.StatusServiceUnavailable, "idlechat_episode_prepare_unavailable", "night preparation scheduler is unavailable")
			return
		}
		writeJSONStatus(w, http.StatusAccepted, map[string]any{
			"ok": true, "state": state, "target_ready_count": target, "episode_stock": snapshot,
		})
	}
}

func writeIdleChatEpisodePrepareError(w http.ResponseWriter, status int, code, message string) {
	writeJSONStatus(w, status, map[string]any{"ok": false, "error_code": code, "error": message})
}

func (d *Dependencies) handleIdleChatEpisodesValidate() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		var request struct {
			EpisodeID string `json:"episode_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024)).Decode(&request); err != nil || strings.TrimSpace(request.EpisodeID) == "" {
			http.Error(w, "episode_id is required", http.StatusBadRequest)
			return
		}
		episode, ok := d.idleChatOrch.StoryEpisode(request.EpisodeID)
		if !ok {
			http.Error(w, "episode not found", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{
			"ok": true, "episode_id": episode.EpisodeID, "valid": episode.Validation.Valid,
			"first_invalid_turn": episode.Validation.FirstInvalidTurn, "errors": episode.Validation.Errors,
			"repair_required": !episode.Validation.Valid, "replacement_requested": !episode.Validation.Valid,
		})
	}
}

func (d *Dependencies) handleIdleChatLogs() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if d.idleChatOrch == nil {
			http.Error(w, "idlechat not enabled", http.StatusNotFound)
			return
		}
		limit := 20
		if s := r.URL.Query().Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		activeSessionID, activeTranscript := d.idleChatOrch.ActiveSessionTranscript(100)
		writeJSON(w, map[string]any{
			"ok":                true,
			"mode":              d.idleChatOrch.CurrentMode(),
			"manual_mode":       d.idleChatOrch.IsManualMode(),
			"chat_active":       d.idleChatOrch.IsChatActive(),
			"current_topic":     d.idleChatOrch.CurrentTopic(),
			"active_session_id": activeSessionID,
			"active_transcript": activeTranscript,
			"history":           d.idleChatOrch.GetHistory(limit),
		})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
