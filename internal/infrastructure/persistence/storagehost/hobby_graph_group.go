package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	musiccatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/musiccatalog"
)

const (
	GroupHobbyGraph       = "hobby_graph"
	hobbyGraphMaxPayload  = 128 << 10
	hobbyGraphMaxResponse = 8 << 20
	hobbyGraphMaxPage     = 20
)

// HobbyGraphGroupOwner exposes only the typed catalog, lyrics, Viewer, and
// authenticated-user preference-candidate operations. It has no SQL or path
// surface.
type HobbyGraphGroupOwner interface {
	LookupCatalog(context.Context, musiccatalog.CatalogRequest) (musiccatalog.CatalogResult, error)
	LookupLyrics(context.Context, musiccatalog.LyricsRequest) (musiccatalog.LyricsResult, error)
	ViewerStats(context.Context) (map[string]int, error)
	ViewerOverview(context.Context, int) (musiccatalog.StorageHostOverview, error)
	FindPreferenceCandidateByID(context.Context, string, string) (musiccatalog.StorageHostPreferenceCandidate, bool, error)
	FindPreferenceCandidateByRequestID(context.Context, string, string) (musiccatalog.StorageHostPreferenceCandidate, bool, error)
	SavePreferenceCandidate(context.Context, musiccatalog.StorageHostOperationIdentity, musiccatalog.StorageHostPreferenceCandidate) (musiccatalog.StorageHostCandidateResult, error)
	FindStorageHostOperationReceipt(context.Context, string) (musiccatalog.StorageHostOperationReceipt, bool, error)
}

type hobbyGraphCatalogPayload struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Artist string `json:"artist,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type hobbyGraphLyricsPayload struct {
	Song        string `json:"song"`
	Artist      string `json:"artist,omitempty"`
	Language    string `json:"language,omitempty"`
	Information string `json:"information"`
	Limit       int    `json:"limit,omitempty"`
}

type hobbyGraphCandidateByIDPayload struct {
	CandidateID string `json:"candidate_id"`
	UserID      string `json:"user_id"`
}

type hobbyGraphCandidateByRequestPayload struct {
	RequestID string `json:"request_id"`
	UserID    string `json:"user_id"`
}

type hobbyGraphCandidateSavePayload struct {
	Candidate musiccatalog.StorageHostPreferenceCandidate `json:"candidate"`
}

type hobbyGraphStatsResponse struct {
	Stats map[string]int `json:"stats"`
}

type hobbyGraphCandidateResponse struct {
	Found     bool                                         `json:"found"`
	Candidate *musiccatalog.StorageHostPreferenceCandidate `json:"candidate,omitempty"`
}

type hobbyGraphCandidateWriteResponse struct {
	Candidate musiccatalog.StorageHostPreferenceCandidate `json:"candidate"`
	Replay    bool                                        `json:"replay"`
}

// RegisterHobbyGraphGroup installs a closed typed RPC contract. Lyrics remain
// a distinct rights-conditioned operation; there is intentionally no generic
// recall or query route.
func RegisterHobbyGraphGroup(handler *Handler, owner HobbyGraphGroupOwner) error {
	if handler == nil || owner == nil {
		return NewError(ErrorCodeSchemaRejected, "hobby graph group needs a handler and owner")
	}
	if err := handler.Register(GroupHobbyGraph, "catalog_lookup", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload hobbyGraphCatalogPayload
		if decodeHobbyGraphPayload(raw, &payload) != nil || !validHobbyGraphCatalogPayload(payload) {
			return nil, NewError(ErrorCodeSchemaRejected, "hobby graph catalog lookup rejected")
		}
		result, err := owner.LookupCatalog(ctx, musiccatalog.CatalogRequest{Kind: payload.Kind, Name: payload.Name, Artist: payload.Artist, Limit: payload.Limit})
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph catalog lookup failed")
		}
		if !validHobbyGraphCatalogResult(result, payload) {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph catalog result is malformed")
		}
		return boundedHobbyGraphResult(result)
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupHobbyGraph, "lyrics_lookup", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload hobbyGraphLyricsPayload
		if decodeHobbyGraphPayload(raw, &payload) != nil || !validHobbyGraphLyricsPayload(payload) {
			return nil, NewError(ErrorCodeSchemaRejected, "hobby graph lyrics lookup rejected")
		}
		result, err := owner.LookupLyrics(ctx, musiccatalog.LyricsRequest{Song: payload.Song, Artist: payload.Artist, Language: payload.Language, Information: payload.Information, Limit: payload.Limit})
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph lyrics lookup failed")
		}
		if !validHobbyGraphLyricsResult(result, payload) {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph lyrics result is malformed")
		}
		return boundedHobbyGraphResult(result)
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupHobbyGraph, "viewer_stats", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		if decodeHobbyGraphEmptyPayload(raw) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "hobby graph stats query rejected")
		}
		stats, err := owner.ViewerStats(ctx)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph stats query failed")
		}
		if !validHobbyGraphStats(stats) {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph stats result is malformed")
		}
		return boundedHobbyGraphResult(hobbyGraphStatsResponse{Stats: stats})
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupHobbyGraph, "viewer_overview", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload struct {
			Limit int `json:"limit"`
		}
		if decodeHobbyGraphPayload(raw, &payload) != nil || payload.Limit < 1 || payload.Limit > hobbyGraphMaxPage {
			return nil, NewError(ErrorCodeSchemaRejected, "hobby graph overview query rejected")
		}
		result, err := owner.ViewerOverview(ctx, payload.Limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph overview query failed")
		}
		if !validHobbyGraphStats(result.Stats) || result.Items == nil || result.Relations == nil || result.Interactions == nil || result.TopicCandidates == nil || len(result.Items) > payload.Limit || len(result.Relations) > payload.Limit || len(result.Interactions) > payload.Limit || len(result.TopicCandidates) > payload.Limit {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph overview result is malformed")
		}
		return boundedHobbyGraphResult(result)
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupHobbyGraph, "candidate_by_id", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload hobbyGraphCandidateByIDPayload
		if decodeHobbyGraphPayload(raw, &payload) != nil || !validHobbyGraphText(payload.CandidateID, 320, true) || !validHobbyGraphText(payload.UserID, 256, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "hobby graph candidate query rejected")
		}
		candidate, found, err := owner.FindPreferenceCandidateByID(ctx, payload.UserID, payload.CandidateID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph candidate lookup failed")
		}
		return hobbyGraphCandidateQueryResult(candidate, found, payload.CandidateID, payload.UserID, false)
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupHobbyGraph, "candidate_by_request", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload hobbyGraphCandidateByRequestPayload
		if decodeHobbyGraphPayload(raw, &payload) != nil || !validHobbyGraphText(payload.RequestID, 256, true) || !validHobbyGraphText(payload.UserID, 256, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "hobby graph candidate query rejected")
		}
		candidate, found, err := owner.FindPreferenceCandidateByRequestID(ctx, payload.UserID, payload.RequestID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph candidate lookup failed")
		}
		return hobbyGraphCandidateQueryResult(candidate, found, payload.RequestID, payload.UserID, true)
	}); err != nil {
		return err
	}
	return handler.RegisterRecoverable(GroupHobbyGraph, "candidate_save", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var payload hobbyGraphCandidateSavePayload
		if decodeHobbyGraphPayload(mutation.Payload, &payload) != nil || musiccatalog.ValidateStorageHostPreferenceCandidate(payload.Candidate) != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "hobby graph candidate rejected"))
		}
		identity := musiccatalog.StorageHostOperationIdentity{OpID: mutation.OpID, Operation: "candidate_save", PayloadHash: payloadHash(GroupHobbyGraph, "candidate_save", mutation.Payload), WriterGeneration: mutation.JournalGeneration}
		result, err := owner.SavePreferenceCandidate(ctx, identity, payload.Candidate)
		if err != nil {
			if errors.Is(err, musiccatalog.ErrStorageHostOperationConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "hobby graph op_id conflicts with an owner receipt"))
			}
			return nil, NewError(ErrorCodeOutcomeUnknown, "hobby graph candidate outcome is unknown")
		}
		if !validHobbyGraphCandidateWrite(result, payload.Candidate) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "hobby graph candidate receipt result is malformed")
		}
		return hobbyGraphCandidateWriteResponse{Candidate: result.Candidate, Replay: result.Replay}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var payload hobbyGraphCandidateSavePayload
		if decodeHobbyGraphPayload(mutation.Payload, &payload) != nil || musiccatalog.ValidateStorageHostPreferenceCandidate(payload.Candidate) != nil {
			return UnknownOutcome(), nil
		}
		want := musiccatalog.StorageHostOperationIdentity{OpID: mutation.OpID, Operation: "candidate_save", PayloadHash: payloadHash(GroupHobbyGraph, "candidate_save", mutation.Payload), WriterGeneration: mutation.JournalGeneration}
		receipt, found, err := owner.FindStorageHostOperationReceipt(ctx, mutation.OpID)
		if err != nil || !found || receipt.Identity != want || !musiccatalog.ValidateStorageHostOperationReceipt(receipt) {
			return UnknownOutcome(), nil
		}
		result, err := decodeHobbyGraphCandidateWriteResult(receipt.ResultJSON)
		if err != nil || !validHobbyGraphCandidateWrite(result, payload.Candidate) {
			return UnknownOutcome(), nil
		}
		return Committed(hobbyGraphCandidateWriteResponse{Candidate: result.Candidate, Replay: result.Replay}), nil
	})
}

func hobbyGraphCandidateQueryResult(candidate musiccatalog.StorageHostPreferenceCandidate, found bool, key, userID string, byRequest bool) (any, error) {
	if !found {
		return hobbyGraphCandidateResponse{Found: false}, nil
	}
	if musiccatalog.ValidateStorageHostPreferenceCandidate(candidate) != nil || candidate.UserID != userID || (byRequest && candidate.RequestID != key) || (!byRequest && candidate.CandidateID != key) {
		return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph owner returned a malformed candidate")
	}
	return boundedHobbyGraphResult(hobbyGraphCandidateResponse{Found: true, Candidate: &candidate})
}

func validHobbyGraphCandidateWrite(result musiccatalog.StorageHostCandidateResult, requested musiccatalog.StorageHostPreferenceCandidate) bool {
	return musiccatalog.ValidateStorageHostPreferenceCandidate(result.Candidate) == nil && sameHobbyGraphCandidateBinding(result.Candidate, requested)
}

func sameHobbyGraphCandidateBinding(a, b musiccatalog.StorageHostPreferenceCandidate) bool {
	return a.CandidateID == b.CandidateID && a.RequestID == b.RequestID && a.UserID == b.UserID && a.ActorID == b.ActorID && a.PayloadHash == b.PayloadHash && a.TargetID == b.TargetID && a.SignalType == b.SignalType && a.Note == b.Note && a.State == b.State
}

func validHobbyGraphCatalogPayload(p hobbyGraphCatalogPayload) bool {
	return (p.Kind == "artist" || p.Kind == "song") && validHobbyGraphText(p.Name, 256, true) && validHobbyGraphText(p.Artist, 256, false) && p.Limit >= 0 && p.Limit <= hobbyGraphMaxPage
}

func validHobbyGraphCatalogResult(result musiccatalog.CatalogResult, p hobbyGraphCatalogPayload) bool {
	if result.Kind != p.Kind || result.Name != strings.TrimSpace(p.Name) || result.Artist != strings.TrimSpace(p.Artist) || len(result.Items) > hobbyGraphMaxPage || len(result.Candidates) > hobbyGraphMaxPage {
		return false
	}
	switch result.Status {
	case "ok":
		return len(result.Items) == 1 && len(result.Candidates) == 0
	case "ambiguous":
		return len(result.Items) == 0 && len(result.Candidates) >= 2
	case "not_found":
		return len(result.Items) == 0 && len(result.Candidates) == 0
	default:
		return false
	}
}

func validHobbyGraphLyricsPayload(p hobbyGraphLyricsPayload) bool {
	if !validHobbyGraphText(p.Song, 256, true) || !validHobbyGraphText(p.Artist, 256, false) || !validHobbyGraphText(p.Language, 32, false) || p.Limit < 0 || p.Limit > hobbyGraphMaxPage {
		return false
	}
	switch p.Information {
	case "rights", "full_text", "syntax":
		return true
	default:
		return false
	}
}

func validHobbyGraphLyricsResult(result musiccatalog.LyricsResult, p hobbyGraphLyricsPayload) bool {
	if result.Artist != strings.TrimSpace(p.Artist) || result.Information != p.Information || len(result.Lyrics) > hobbyGraphMaxPage || len(result.Syntax) > hobbyGraphMaxPage || len(result.Candidates) > hobbyGraphMaxPage {
		return false
	}
	switch result.Status {
	case "ok":
		if result.Song.ItemID == "" || len(result.Candidates) != 0 {
			return false
		}
	case "ambiguous":
		if len(result.Song.ItemID) != 0 || len(result.Candidates) < 2 || len(result.Lyrics) != 0 || len(result.Syntax) != 0 {
			return false
		}
	case "not_found":
		if len(result.Song.ItemID) != 0 || len(result.Candidates) != 0 || len(result.Lyrics) != 0 || len(result.Syntax) != 0 {
			return false
		}
	default:
		return false
	}
	if result.Status != "ok" {
		return true
	}
	switch p.Information {
	case "rights":
		if len(result.Syntax) != 0 {
			return false
		}
		for _, entry := range result.Lyrics {
			if entry.LyricsText != "" {
				return false
			}
		}
	case "full_text":
		if len(result.Syntax) != 0 {
			return false
		}
		for _, entry := range result.Lyrics {
			if entry.StorageMode != "full_text" || (entry.RightsStatus != "licensed" && entry.RightsStatus != "public_domain" && entry.RightsStatus != "user_owned") || strings.TrimSpace(entry.LicenseReference) == "" {
				return false
			}
		}
	case "syntax":
		if len(result.Lyrics) != 0 {
			return false
		}
	}
	return true
}

var hobbyGraphStatsKeys = []string{
	"hobby_items", "hobby_relations", "hobby_interactions", "hobby_title_observations",
	"hobby_preference_signals", "hobby_topic_candidates", "hobby_collection_runs",
	"hobby_collection_targets", "hobby_music_lyrics", "hobby_music_syntax_features",
	"hobby_music_collection_receipts",
}

func validHobbyGraphStats(stats map[string]int) bool {
	if len(stats) != len(hobbyGraphStatsKeys) {
		return false
	}
	for _, key := range hobbyGraphStatsKeys {
		if count, ok := stats[key]; !ok || count < 0 {
			return false
		}
	}
	return true
}

func validHobbyGraphText(value string, maxRunes int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, utf8.RuneError) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maxRunes && (!required || strings.TrimSpace(value) != "")
}

func decodeHobbyGraphPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > hobbyGraphMaxPayload || !utf8.Valid(trimmed) || trimmed[0] != '{' {
		return errors.New("hobby graph payload rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("hobby graph payload has trailing data")
	}
	return nil
}

func decodeHobbyGraphEmptyPayload(raw json.RawMessage) error {
	var payload struct{}
	return decodeHobbyGraphPayload(raw, &payload)
}

func boundedHobbyGraphResult(result any) (any, error) {
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > hobbyGraphMaxResponse {
		return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph result exceeds the response bound")
	}
	return result, nil
}

func decodeHobbyGraphCandidateWriteResult(raw []byte) (musiccatalog.StorageHostCandidateResult, error) {
	var wire hobbyGraphCandidateWriteResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return musiccatalog.StorageHostCandidateResult{}, err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) || musiccatalog.ValidateStorageHostPreferenceCandidate(wire.Candidate) != nil {
		return musiccatalog.StorageHostCandidateResult{}, errors.New("hobby graph candidate receipt is malformed")
	}
	return musiccatalog.StorageHostCandidateResult{Candidate: wire.Candidate, Replay: wire.Replay}, nil
}

// HobbyGraphClient implements only the typed remote owner interface. The
// caller owns the shared RPC client, so this owner's Close is intentionally a
// no-op.
type HobbyGraphClient struct{ client *Client }

func NewHobbyGraphClient(client *Client) *HobbyGraphClient { return &HobbyGraphClient{client: client} }
func (c *HobbyGraphClient) Close() error                   { return nil }

func (c *HobbyGraphClient) LookupCatalog(ctx context.Context, request musiccatalog.CatalogRequest) (musiccatalog.CatalogResult, error) {
	payload := hobbyGraphCatalogPayload{Kind: strings.TrimSpace(request.Kind), Name: request.Name, Artist: request.Artist, Limit: request.Limit}
	if c == nil || c.client == nil {
		return musiccatalog.CatalogResult{}, NewError(ErrorCodeStoreUnavailable, "hobby graph client is unavailable")
	}
	if !validHobbyGraphCatalogPayload(payload) {
		return musiccatalog.CatalogResult{}, NewError(ErrorCodeSchemaRejected, "hobby graph catalog lookup rejected")
	}
	var result musiccatalog.CatalogResult
	if err := c.client.Call(ctx, GroupHobbyGraph, "catalog_lookup", payload, &result); err != nil {
		return musiccatalog.CatalogResult{}, err
	}
	if !validHobbyGraphCatalogResult(result, payload) {
		return musiccatalog.CatalogResult{}, NewError(ErrorCodeOutcomeUnknown, "hobby graph catalog result is malformed")
	}
	return result, nil
}

func (c *HobbyGraphClient) LookupLyrics(ctx context.Context, request musiccatalog.LyricsRequest) (musiccatalog.LyricsResult, error) {
	payload := hobbyGraphLyricsPayload{Song: request.Song, Artist: request.Artist, Language: request.Language, Information: request.Information, Limit: request.Limit}
	if c == nil || c.client == nil {
		return musiccatalog.LyricsResult{}, NewError(ErrorCodeStoreUnavailable, "hobby graph client is unavailable")
	}
	if !validHobbyGraphLyricsPayload(payload) {
		return musiccatalog.LyricsResult{}, NewError(ErrorCodeSchemaRejected, "hobby graph lyrics lookup rejected")
	}
	var result musiccatalog.LyricsResult
	if err := c.client.Call(ctx, GroupHobbyGraph, "lyrics_lookup", payload, &result); err != nil {
		return musiccatalog.LyricsResult{}, err
	}
	if !validHobbyGraphLyricsResult(result, payload) {
		return musiccatalog.LyricsResult{}, NewError(ErrorCodeOutcomeUnknown, "hobby graph lyrics result is malformed")
	}
	return result, nil
}

func (c *HobbyGraphClient) ViewerStats(ctx context.Context) (map[string]int, error) {
	if c == nil || c.client == nil {
		return nil, NewError(ErrorCodeStoreUnavailable, "hobby graph client is unavailable")
	}
	var response hobbyGraphStatsResponse
	if err := c.client.Call(ctx, GroupHobbyGraph, "viewer_stats", struct{}{}, &response); err != nil {
		return nil, err
	}
	if !validHobbyGraphStats(response.Stats) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "hobby graph stats result is malformed")
	}
	return response.Stats, nil
}

func (c *HobbyGraphClient) ViewerOverview(ctx context.Context, limit int) (musiccatalog.StorageHostOverview, error) {
	if c == nil || c.client == nil {
		return musiccatalog.StorageHostOverview{}, NewError(ErrorCodeStoreUnavailable, "hobby graph client is unavailable")
	}
	if limit < 1 || limit > hobbyGraphMaxPage {
		return musiccatalog.StorageHostOverview{}, NewError(ErrorCodeSchemaRejected, "hobby graph overview query rejected")
	}
	var result musiccatalog.StorageHostOverview
	if err := c.client.Call(ctx, GroupHobbyGraph, "viewer_overview", struct {
		Limit int `json:"limit"`
	}{Limit: limit}, &result); err != nil {
		return musiccatalog.StorageHostOverview{}, err
	}
	if !validHobbyGraphStats(result.Stats) || result.Items == nil || result.Relations == nil || result.Interactions == nil || result.TopicCandidates == nil || len(result.Items) > limit || len(result.Relations) > limit || len(result.Interactions) > limit || len(result.TopicCandidates) > limit {
		return musiccatalog.StorageHostOverview{}, NewError(ErrorCodeOutcomeUnknown, "hobby graph overview result is malformed")
	}
	return result, nil
}

func (c *HobbyGraphClient) FindPreferenceCandidateByID(ctx context.Context, userID, candidateID string) (musiccatalog.StorageHostPreferenceCandidate, bool, error) {
	return c.findCandidate(ctx, "candidate_by_id", hobbyGraphCandidateByIDPayload{CandidateID: candidateID, UserID: userID}, candidateID, userID, false)
}

func (c *HobbyGraphClient) FindPreferenceCandidateByRequestID(ctx context.Context, userID, requestID string) (musiccatalog.StorageHostPreferenceCandidate, bool, error) {
	return c.findCandidate(ctx, "candidate_by_request", hobbyGraphCandidateByRequestPayload{RequestID: requestID, UserID: userID}, requestID, userID, true)
}

func (c *HobbyGraphClient) findCandidate(ctx context.Context, operation string, payload any, key, userID string, byRequest bool) (musiccatalog.StorageHostPreferenceCandidate, bool, error) {
	if c == nil || c.client == nil {
		return musiccatalog.StorageHostPreferenceCandidate{}, false, NewError(ErrorCodeStoreUnavailable, "hobby graph client is unavailable")
	}
	var response hobbyGraphCandidateResponse
	if err := c.client.Call(ctx, GroupHobbyGraph, operation, payload, &response); err != nil {
		return musiccatalog.StorageHostPreferenceCandidate{}, false, err
	}
	if !response.Found {
		if response.Candidate != nil {
			return musiccatalog.StorageHostPreferenceCandidate{}, false, NewError(ErrorCodeOutcomeUnknown, "hobby graph candidate result is malformed")
		}
		return musiccatalog.StorageHostPreferenceCandidate{}, false, nil
	}
	if response.Candidate == nil || musiccatalog.ValidateStorageHostPreferenceCandidate(*response.Candidate) != nil || response.Candidate.UserID != userID || (byRequest && response.Candidate.RequestID != key) || (!byRequest && response.Candidate.CandidateID != key) {
		return musiccatalog.StorageHostPreferenceCandidate{}, false, NewError(ErrorCodeOutcomeUnknown, "hobby graph candidate result is malformed")
	}
	return *response.Candidate, true, nil
}

func (c *HobbyGraphClient) SavePreferenceCandidate(ctx context.Context, candidate musiccatalog.StorageHostPreferenceCandidate) (musiccatalog.StorageHostCandidateResult, error) {
	if c == nil || c.client == nil {
		return musiccatalog.StorageHostCandidateResult{}, NewError(ErrorCodeStoreUnavailable, "hobby graph client is unavailable")
	}
	if musiccatalog.ValidateStorageHostPreferenceCandidate(candidate) != nil {
		return musiccatalog.StorageHostCandidateResult{}, NewError(ErrorCodeSchemaRejected, "hobby graph candidate rejected")
	}
	var response hobbyGraphCandidateWriteResponse
	if err := c.client.Call(ctx, GroupHobbyGraph, "candidate_save", hobbyGraphCandidateSavePayload{Candidate: candidate}, &response); err != nil {
		return musiccatalog.StorageHostCandidateResult{}, err
	}
	result := musiccatalog.StorageHostCandidateResult{Candidate: response.Candidate, Replay: response.Replay}
	if !validHobbyGraphCandidateWrite(result, candidate) {
		return musiccatalog.StorageHostCandidateResult{}, NewError(ErrorCodeOutcomeUnknown, "hobby graph candidate result is malformed")
	}
	return result, nil
}
