package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	moviecatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
)

const (
	GroupMovieCatalog       = "movie_catalog"
	maxMovieCatalogPayload  = 128 << 10
	maxMovieCatalogResponse = 8 << 20
	maxMovieCatalogPage     = 100
)

// MovieCatalogGroupOwner is the typed owner boundary for the movie catalog.
// It deliberately contains no SQL, path, or arbitrary query operation.
type MovieCatalogGroupOwner interface {
	Lookup(context.Context, moviecatalog.LookupRequest) (moviecatalog.LookupResult, error)
	ViewerStats(context.Context) (map[string]int, error)
	ViewerMovies(context.Context, moviecatalog.QueryParams, int, int) (int, []moviecatalog.MovieItem, error)
	ViewerPeople(context.Context, moviecatalog.QueryParams, int, int) (int, []moviecatalog.PersonItem, error)
	ViewerCards(context.Context, int, int) (int, []moviecatalog.Card, error)
	ViewerMovie(context.Context, string) (map[string]any, error)
	ViewerPerson(context.Context, string) (map[string]any, error)
	FindPreferenceCandidateByID(context.Context, string, string) (moviecatalog.StorageHostPreferenceCandidate, bool, error)
	FindPreferenceCandidateByRequestID(context.Context, string, string) (moviecatalog.StorageHostPreferenceCandidate, bool, error)
	SavePreferenceCandidate(context.Context, moviecatalog.StorageHostOperationIdentity, moviecatalog.StorageHostPreferenceCandidate) (moviecatalog.StorageHostCandidateResult, error)
	FindStorageHostOperationReceipt(context.Context, string) (moviecatalog.StorageHostOperationReceipt, bool, error)
}

type movieCatalogLookupPayload struct {
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Information string `json:"information,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type movieCatalogViewerQueryPayload struct {
	Query  string `json:"query,omitempty"`
	Role   string `json:"role,omitempty"`
	Source string `json:"source,omitempty"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
}

type movieCatalogIDPayload struct {
	ID string `json:"id"`
}
type movieCatalogCandidateQueryPayload struct {
	CandidateID string `json:"candidate_id"`
	UserID      string `json:"user_id"`
}
type movieCatalogRequestCandidatePayload struct {
	RequestID string `json:"request_id"`
	UserID    string `json:"user_id"`
}
type movieCatalogCandidatePayload struct {
	Candidate moviecatalog.StorageHostPreferenceCandidate `json:"candidate"`
}

type movieCatalogLookupDetail struct {
	Movie                *moviecatalog.MovieItem        `json:"movie,omitempty"`
	Person               *moviecatalog.PersonItem       `json:"person,omitempty"`
	Links                *[]moviecatalog.EdgeItem       `json:"links,omitempty"`
	WatchEvents          *[]moviecatalog.WatchEventItem `json:"watch_events,omitempty"`
	InformationAvailable *bool                          `json:"information_available,omitempty"`
}

type movieCatalogLookupResponse struct {
	Kind        string                               `json:"kind"`
	Name        string                               `json:"name"`
	LookupKey   string                               `json:"lookup_key"`
	Information string                               `json:"information,omitempty"`
	Movies      []moviecatalog.MovieLookupCandidate  `json:"movies,omitempty"`
	People      []moviecatalog.PersonLookupCandidate `json:"people,omitempty"`
	Detail      *movieCatalogLookupDetail            `json:"detail,omitempty"`
	NotFound    bool                                 `json:"not_found"`
	Ambiguous   bool                                 `json:"ambiguous"`
}

type movieCatalogPage[T any] struct {
	Total int `json:"total"`
	Items []T `json:"items"`
}
type movieCatalogStatsResult struct {
	Stats map[string]int `json:"stats"`
}
type movieCatalogDetailResult struct {
	Detail *movieCatalogLookupDetail `json:"detail"`
}
type movieCatalogCandidateResult struct {
	Found     bool                                         `json:"found"`
	Candidate *moviecatalog.StorageHostPreferenceCandidate `json:"candidate,omitempty"`
}
type movieCatalogCandidateWriteResult struct {
	Candidate moviecatalog.StorageHostPreferenceCandidate `json:"candidate"`
	Replay    bool                                        `json:"replay"`
}

// RegisterMovieCatalogGroup registers only closed movie catalog projections
// and authenticated-user candidate operations. Candidate writes require a
// same-database operation receipt and are recoverable by the storage journal.
func RegisterMovieCatalogGroup(handler *Handler, owner MovieCatalogGroupOwner) error {
	if handler == nil || owner == nil {
		return errors.New("storagehost: movie catalog group needs a handler and an owner")
	}
	if err := handler.Register(GroupMovieCatalog, "lookup", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var payload movieCatalogLookupPayload
		if decodeMovieCatalogPayload(raw, &payload) != nil || validateMovieCatalogLookupPayload(payload) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "movie catalog lookup rejected")
		}
		information := payload.Information
		if strings.EqualFold(strings.TrimSpace(information), "all") {
			information = ""
		}
		result, err := owner.Lookup(ctx, moviecatalog.LookupRequest{Kind: payload.Kind, Name: payload.Name, Information: information, Limit: payload.Limit})
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog lookup failed")
		}
		response, err := movieCatalogLookupResponseFromResult(result)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog owner returned a malformed lookup result")
		}
		if response.Kind != strings.ToLower(strings.TrimSpace(payload.Kind)) || response.Name != strings.TrimSpace(payload.Name) || response.Information != information {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog owner returned a mismatched lookup result")
		}
		return boundedMovieCatalogResult(response)
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupMovieCatalog, "viewer_stats", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		if decodeMovieCatalogEmptyPayload(raw) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "movie catalog stats query rejected")
		}
		stats, err := owner.ViewerStats(ctx)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog stats query failed")
		}
		if len(stats) > 64 {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog stats result is malformed")
		}
		return boundedMovieCatalogResult(movieCatalogStatsResult{Stats: stats})
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupMovieCatalog, "viewer_movies", false, movieCatalogListHandler(func(ctx context.Context, p movieCatalogViewerQueryPayload) (any, error) {
		total, items, err := owner.ViewerMovies(ctx, moviecatalog.QueryParams{Query: p.Query, Role: p.Role, Source: p.Source}, p.Limit, p.Offset)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog movie list failed")
		}
		if total < 0 || len(items) > p.Limit {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog movie list is malformed")
		}
		if items == nil {
			items = []moviecatalog.MovieItem{}
		}
		return boundedMovieCatalogResult(movieCatalogPage[moviecatalog.MovieItem]{Total: total, Items: items})
	})); err != nil {
		return err
	}
	if err := handler.Register(GroupMovieCatalog, "viewer_people", false, movieCatalogListHandler(func(ctx context.Context, p movieCatalogViewerQueryPayload) (any, error) {
		total, items, err := owner.ViewerPeople(ctx, moviecatalog.QueryParams{Query: p.Query, Role: p.Role, Source: p.Source}, p.Limit, p.Offset)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog people list failed")
		}
		if total < 0 || len(items) > p.Limit {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog people list is malformed")
		}
		if items == nil {
			items = []moviecatalog.PersonItem{}
		}
		return boundedMovieCatalogResult(movieCatalogPage[moviecatalog.PersonItem]{Total: total, Items: items})
	})); err != nil {
		return err
	}
	if err := handler.Register(GroupMovieCatalog, "viewer_cards", false, movieCatalogListHandler(func(ctx context.Context, p movieCatalogViewerQueryPayload) (any, error) {
		total, items, err := owner.ViewerCards(ctx, p.Limit, p.Offset)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog card list failed")
		}
		if total < 0 || len(items) > p.Limit {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog card list is malformed")
		}
		if items == nil {
			items = []moviecatalog.Card{}
		}
		return boundedMovieCatalogResult(movieCatalogPage[moviecatalog.Card]{Total: total, Items: items})
	})); err != nil {
		return err
	}
	if err := handler.Register(GroupMovieCatalog, "viewer_movie", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p movieCatalogIDPayload
		if decodeMovieCatalogPayload(raw, &p) != nil || !validMovieCatalogText(p.ID, 256, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "movie catalog detail query rejected")
		}
		detail, err := owner.ViewerMovie(ctx, p.ID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog movie detail failed")
		}
		wire, err := movieCatalogDetailFromMap(detail, "movie")
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog movie detail is malformed")
		}
		return boundedMovieCatalogResult(movieCatalogDetailResult{Detail: wire})
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupMovieCatalog, "viewer_person", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p movieCatalogIDPayload
		if decodeMovieCatalogPayload(raw, &p) != nil || !validMovieCatalogText(p.ID, 256, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "movie catalog detail query rejected")
		}
		detail, err := owner.ViewerPerson(ctx, p.ID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog person detail failed")
		}
		wire, err := movieCatalogDetailFromMap(detail, "person")
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog person detail is malformed")
		}
		return boundedMovieCatalogResult(movieCatalogDetailResult{Detail: wire})
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupMovieCatalog, "candidate_by_id", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p movieCatalogCandidateQueryPayload
		if decodeMovieCatalogPayload(raw, &p) != nil || !validMovieCatalogText(p.CandidateID, 256, true) || !validMovieCatalogText(p.UserID, 256, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "movie catalog candidate query rejected")
		}
		candidate, found, err := owner.FindPreferenceCandidateByID(ctx, p.UserID, p.CandidateID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog candidate lookup failed")
		}
		return movieCatalogCandidateQueryResult(candidate, found, p.CandidateID, p.UserID, "id")
	}); err != nil {
		return err
	}
	if err := handler.Register(GroupMovieCatalog, "candidate_by_request", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p movieCatalogRequestCandidatePayload
		if decodeMovieCatalogPayload(raw, &p) != nil || !validMovieCatalogText(p.RequestID, 256, true) || !validMovieCatalogText(p.UserID, 256, true) {
			return nil, NewError(ErrorCodeSchemaRejected, "movie catalog candidate query rejected")
		}
		candidate, found, err := owner.FindPreferenceCandidateByRequestID(ctx, p.UserID, p.RequestID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog candidate lookup failed")
		}
		return movieCatalogCandidateQueryResult(candidate, found, p.RequestID, p.UserID, "request_id")
	}); err != nil {
		return err
	}
	return handler.RegisterRecoverable(GroupMovieCatalog, "candidate_save", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var p movieCatalogCandidatePayload
		if decodeMovieCatalogPayload(mutation.Payload, &p) != nil || !validMovieCatalogCandidate(p.Candidate) {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "movie catalog candidate rejected"))
		}
		identity := moviecatalog.StorageHostOperationIdentity{OpID: mutation.OpID, Operation: "candidate_save", PayloadHash: payloadHash(GroupMovieCatalog, "candidate_save", mutation.Payload), WriterGeneration: mutation.JournalGeneration}
		result, err := owner.SavePreferenceCandidate(ctx, identity, p.Candidate)
		if err != nil {
			if errors.Is(err, moviecatalog.ErrStorageHostOperationConflict) {
				return nil, ownerRolledBack(NewError(ErrorCodeDuplicateConflict, "movie catalog op_id conflicts with an owner receipt"))
			}
			if errors.Is(err, moviecatalog.ErrStorageHostReceiptUnknown) {
				return nil, NewError(ErrorCodeOutcomeUnknown, "movie catalog owner receipt is unknown")
			}
			return nil, NewError(ErrorCodeOutcomeUnknown, "movie catalog candidate outcome is unknown")
		}
		if !validMovieCatalogCandidateResult(result, p.Candidate) {
			return nil, NewError(ErrorCodeOutcomeUnknown, "movie catalog candidate receipt result is malformed")
		}
		return movieCatalogCandidateWriteResult{Candidate: result.Candidate, Replay: result.Replay}, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		var p movieCatalogCandidatePayload
		if decodeMovieCatalogPayload(mutation.Payload, &p) != nil || !validMovieCatalogCandidate(p.Candidate) {
			return UnknownOutcome(), nil
		}
		want := moviecatalog.StorageHostOperationIdentity{OpID: mutation.OpID, Operation: "candidate_save", PayloadHash: payloadHash(GroupMovieCatalog, "candidate_save", mutation.Payload), WriterGeneration: mutation.JournalGeneration}
		receipt, found, err := owner.FindStorageHostOperationReceipt(ctx, mutation.OpID)
		if err != nil || !found {
			return UnknownOutcome(), nil
		}
		if receipt.Identity != want || !moviecatalog.ValidateStorageHostOperationReceipt(receipt) {
			return UnknownOutcome(), nil
		}
		result, err := decodeMovieCatalogCandidateResult(receipt.ResultJSON)
		if err != nil || !validMovieCatalogCandidateResult(result, p.Candidate) {
			return UnknownOutcome(), nil
		}
		return Committed(movieCatalogCandidateWriteResult{Candidate: result.Candidate, Replay: result.Replay}), nil
	})
}

type movieCatalogListOperation func(context.Context, movieCatalogViewerQueryPayload) (any, error)

func movieCatalogListHandler(operation movieCatalogListOperation) OperationFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p movieCatalogViewerQueryPayload
		if decodeMovieCatalogPayload(raw, &p) != nil || validateMovieCatalogViewerQuery(p) != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "movie catalog list query rejected")
		}
		return operation(ctx, p)
	}
}

func decodeMovieCatalogPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || len(trimmed) > maxMovieCatalogPayload || !utf8.Valid(trimmed) || trimmed[0] != '{' {
		return errors.New("movie catalog payload rejected")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return errors.New("movie catalog payload has trailing data")
	}
	return nil
}
func decodeMovieCatalogEmptyPayload(raw json.RawMessage) error {
	var p struct{}
	return decodeMovieCatalogPayload(raw, &p)
}
func validMovieCatalogText(value string, maxRunes int, required bool) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, utf8.RuneError) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maxRunes && (!required || strings.TrimSpace(value) != "")
}
func validateMovieCatalogLookupPayload(p movieCatalogLookupPayload) error {
	kind := strings.ToLower(strings.TrimSpace(p.Kind))
	name := strings.TrimSpace(p.Name)
	information := strings.ToLower(strings.TrimSpace(p.Information))
	if (kind != "movie" && kind != "person") || !validMovieCatalogText(name, 256, true) || !validMovieCatalogText(information, 16, false) || p.Limit < 0 || p.Limit > 20 {
		return errors.New("invalid movie catalog lookup")
	}
	if information == "all" {
		return nil
	}
	if information == "" {
		return nil
	}
	if kind == "movie" && (information == "overview" || information == "cast" || information == "staff") {
		return nil
	}
	if kind == "person" && (information == "profile" || information == "filmography") {
		return nil
	}
	return errors.New("invalid movie catalog information selector")
}
func validateMovieCatalogViewerQuery(p movieCatalogViewerQueryPayload) error {
	if !validateMovieCatalogPage(p.Limit, p.Offset) || !validMovieCatalogText(p.Query, 256, false) || !validMovieCatalogText(p.Role, 128, false) || !validMovieCatalogText(p.Source, 128, false) {
		return errors.New("invalid movie catalog viewer query")
	}
	return nil
}
func validateMovieCatalogPage(limit, offset int) bool {
	return limit >= 1 && limit <= maxMovieCatalogPage && offset >= 0 && offset <= 1_000_000
}

func movieCatalogLookupResponseFromResult(result moviecatalog.LookupResult) (movieCatalogLookupResponse, error) {
	if (result.Kind != "movie" && result.Kind != "person") || !validMovieCatalogText(result.Name, 256, true) || !validMovieCatalogText(result.LookupKey, 256, true) || len(result.Movies) > 20 || len(result.People) > 20 {
		return movieCatalogLookupResponse{}, errors.New("invalid lookup result")
	}
	response := movieCatalogLookupResponse{Kind: result.Kind, Name: result.Name, LookupKey: result.LookupKey, Information: result.Information, Movies: result.Movies, People: result.People, NotFound: result.NotFound, Ambiguous: result.Ambiguous}
	if result.Detail != nil {
		detail, err := movieCatalogDetailFromMap(result.Detail, result.Kind)
		if err != nil {
			return movieCatalogLookupResponse{}, err
		}
		response.Detail = detail
	}
	return response, nil
}
func movieCatalogDetailFromMap(detail map[string]any, kind string) (*movieCatalogLookupDetail, error) {
	if detail == nil {
		return nil, nil
	}
	wire := &movieCatalogLookupDetail{}
	if raw, ok := detail["movie"]; ok {
		v, ok := raw.(moviecatalog.MovieItem)
		if !ok {
			return nil, errors.New("movie detail has an invalid movie DTO")
		}
		wire.Movie = &v
	}
	if raw, ok := detail["person"]; ok {
		v, ok := raw.(moviecatalog.PersonItem)
		if !ok {
			return nil, errors.New("movie detail has an invalid person DTO")
		}
		wire.Person = &v
	}
	if raw, ok := detail["links"]; ok {
		v, ok := raw.([]moviecatalog.EdgeItem)
		if !ok {
			return nil, errors.New("movie detail has invalid links DTO")
		}
		wire.Links = &v
	}
	if raw, ok := detail["watch_events"]; ok {
		v, ok := raw.([]moviecatalog.WatchEventItem)
		if !ok {
			return nil, errors.New("movie detail has invalid watch events DTO")
		}
		wire.WatchEvents = &v
	}
	if raw, ok := detail["information_available"]; ok {
		v, ok := raw.(bool)
		if !ok {
			return nil, errors.New("movie detail has invalid availability DTO")
		}
		wire.InformationAvailable = &v
	}
	if (kind == "movie" && wire.Person != nil) || (kind == "person" && wire.Movie != nil) {
		return nil, errors.New("movie detail kind mismatch")
	}
	return wire, nil
}
func (d *movieCatalogLookupDetail) toMap() map[string]any {
	if d == nil {
		return nil
	}
	out := map[string]any{}
	if d.Movie != nil {
		out["movie"] = *d.Movie
	}
	if d.Person != nil {
		out["person"] = *d.Person
	}
	if d.Links != nil {
		out["links"] = *d.Links
	}
	if d.WatchEvents != nil {
		out["watch_events"] = *d.WatchEvents
	}
	if d.InformationAvailable != nil {
		out["information_available"] = *d.InformationAvailable
	}
	return out
}
func movieCatalogCandidateQueryResult(candidate moviecatalog.StorageHostPreferenceCandidate, found bool, key, userID, column string) (any, error) {
	if !found {
		return movieCatalogCandidateResult{Found: false}, nil
	}
	if !validMovieCatalogCandidate(candidate) || candidate.UserID != userID || (column == "id" && candidate.ID != key) || (column == "request_id" && candidate.RequestID != key) {
		return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog owner returned a malformed candidate")
	}
	return boundedMovieCatalogResult(movieCatalogCandidateResult{Found: true, Candidate: &candidate})
}
func validMovieCatalogCandidate(candidate moviecatalog.StorageHostPreferenceCandidate) bool {
	return moviecatalog.ValidateStorageHostPreferenceCandidate(candidate) == nil
}
func validMovieCatalogCandidateResult(result moviecatalog.StorageHostCandidateResult, requested moviecatalog.StorageHostPreferenceCandidate) bool {
	return validMovieCatalogCandidate(result.Candidate) && movieCatalogCandidateBindingEqual(result.Candidate, requested)
}
func movieCatalogCandidateBindingEqual(a, b moviecatalog.StorageHostPreferenceCandidate) bool {
	return a.ID == b.ID && a.RequestID == b.RequestID && a.UserID == b.UserID && a.ActorID == b.ActorID && a.PayloadHash == b.PayloadHash && a.TargetKind == b.TargetKind && a.TargetID == b.TargetID && a.Familiarity == b.Familiarity && a.Sentiment == b.Sentiment && a.Note == b.Note && a.State == b.State
}
func boundedMovieCatalogResult(result any) (any, error) {
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > maxMovieCatalogResponse {
		return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog result exceeds the response bound")
	}
	return result, nil
}
func decodeMovieCatalogCandidateResult(raw []byte) (moviecatalog.StorageHostCandidateResult, error) {
	var result moviecatalog.StorageHostCandidateResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return result, errors.New("candidate receipt has trailing data")
	}
	canonical, err := json.Marshal(result)
	if err != nil || !bytes.Equal(canonical, raw) {
		return result, errors.New("candidate receipt is not canonical")
	}
	return result, nil
}

// MovieCatalogClient implements only the typed remote owner surface. Close is
// intentionally a no-op because the RPC client can be shared by owner groups.
type MovieCatalogClient struct{ client *Client }

func NewMovieCatalogClient(client *Client) *MovieCatalogClient {
	return &MovieCatalogClient{client: client}
}
func (c *MovieCatalogClient) Close() error { return nil }
func (c *MovieCatalogClient) Lookup(ctx context.Context, request moviecatalog.LookupRequest) (moviecatalog.LookupResult, error) {
	if c == nil || c.client == nil {
		return moviecatalog.LookupResult{}, NewError(ErrorCodeStoreUnavailable, "movie catalog client is unavailable")
	}
	if validateMovieCatalogLookupPayload(movieCatalogLookupPayload{Kind: request.Kind, Name: request.Name, Information: request.Information, Limit: request.Limit}) != nil {
		return moviecatalog.LookupResult{}, NewError(ErrorCodeSchemaRejected, "movie catalog lookup rejected")
	}
	var wire movieCatalogLookupResponse
	if err := c.client.Call(ctx, GroupMovieCatalog, "lookup", movieCatalogLookupPayload{Kind: request.Kind, Name: request.Name, Information: request.Information, Limit: request.Limit}, &wire); err != nil {
		return moviecatalog.LookupResult{}, err
	}
	if wire.Kind != strings.ToLower(strings.TrimSpace(request.Kind)) || wire.Name != strings.TrimSpace(request.Name) || len(wire.Movies) > 20 || len(wire.People) > 20 {
		return moviecatalog.LookupResult{}, NewError(ErrorCodeOutcomeUnknown, "movie catalog lookup result is malformed")
	}
	result := moviecatalog.LookupResult{Kind: wire.Kind, Name: wire.Name, LookupKey: wire.LookupKey, Information: wire.Information, Movies: wire.Movies, People: wire.People, NotFound: wire.NotFound, Ambiguous: wire.Ambiguous}
	if wire.Detail != nil {
		result.Detail = wire.Detail.toMap()
	}
	return result, nil
}
func (c *MovieCatalogClient) ViewerStats(ctx context.Context) (map[string]int, error) {
	if c == nil || c.client == nil {
		return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog client is unavailable")
	}
	var out movieCatalogStatsResult
	if err := c.client.Call(ctx, GroupMovieCatalog, "viewer_stats", struct{}{}, &out); err != nil {
		return nil, err
	}
	if out.Stats == nil || len(out.Stats) > 64 {
		return nil, NewError(ErrorCodeOutcomeUnknown, "movie catalog stats result is malformed")
	}
	return out.Stats, nil
}
func (c *MovieCatalogClient) ViewerMovies(ctx context.Context, p moviecatalog.QueryParams, limit, offset int) (int, []moviecatalog.MovieItem, error) {
	var out movieCatalogPage[moviecatalog.MovieItem]
	if err := c.callList(ctx, "viewer_movies", p, limit, offset, &out); err != nil {
		return 0, nil, err
	}
	if out.Items == nil || len(out.Items) > limit {
		return 0, nil, NewError(ErrorCodeOutcomeUnknown, "movie catalog movie result is malformed")
	}
	return out.Total, out.Items, nil
}
func (c *MovieCatalogClient) ViewerPeople(ctx context.Context, p moviecatalog.QueryParams, limit, offset int) (int, []moviecatalog.PersonItem, error) {
	var out movieCatalogPage[moviecatalog.PersonItem]
	if err := c.callList(ctx, "viewer_people", p, limit, offset, &out); err != nil {
		return 0, nil, err
	}
	if out.Items == nil || len(out.Items) > limit {
		return 0, nil, NewError(ErrorCodeOutcomeUnknown, "movie catalog people result is malformed")
	}
	return out.Total, out.Items, nil
}
func (c *MovieCatalogClient) ViewerCards(ctx context.Context, limit, offset int) (int, []moviecatalog.Card, error) {
	var out movieCatalogPage[moviecatalog.Card]
	if err := c.callList(ctx, "viewer_cards", moviecatalog.QueryParams{}, limit, offset, &out); err != nil {
		return 0, nil, err
	}
	if out.Items == nil || len(out.Items) > limit {
		return 0, nil, NewError(ErrorCodeOutcomeUnknown, "movie catalog card result is malformed")
	}
	return out.Total, out.Items, nil
}
func (c *MovieCatalogClient) callList(ctx context.Context, op string, p moviecatalog.QueryParams, limit, offset int, out any) error {
	if c == nil || c.client == nil {
		return NewError(ErrorCodeStoreUnavailable, "movie catalog client is unavailable")
	}
	payload := movieCatalogViewerQueryPayload{Query: p.Query, Role: p.Role, Source: p.Source, Limit: limit, Offset: offset}
	if validateMovieCatalogViewerQuery(payload) != nil {
		return NewError(ErrorCodeSchemaRejected, "movie catalog list query rejected")
	}
	return c.client.Call(ctx, GroupMovieCatalog, op, payload, out)
}
func (c *MovieCatalogClient) ViewerMovie(ctx context.Context, id string) (map[string]any, error) {
	return c.viewerDetail(ctx, "viewer_movie", id, "movie")
}
func (c *MovieCatalogClient) ViewerPerson(ctx context.Context, id string) (map[string]any, error) {
	return c.viewerDetail(ctx, "viewer_person", id, "person")
}
func (c *MovieCatalogClient) viewerDetail(ctx context.Context, op, id, kind string) (map[string]any, error) {
	if c == nil || c.client == nil {
		return nil, NewError(ErrorCodeStoreUnavailable, "movie catalog client is unavailable")
	}
	if !validMovieCatalogText(id, 256, true) {
		return nil, NewError(ErrorCodeSchemaRejected, "movie catalog detail query rejected")
	}
	var out movieCatalogDetailResult
	if err := c.client.Call(ctx, GroupMovieCatalog, op, movieCatalogIDPayload{ID: id}, &out); err != nil {
		return nil, err
	}
	if out.Detail == nil || (kind == "movie" && out.Detail.Movie == nil) || (kind == "person" && out.Detail.Person == nil) {
		return nil, NewError(ErrorCodeOutcomeUnknown, "movie catalog detail result is malformed")
	}
	return out.Detail.toMap(), nil
}
func (c *MovieCatalogClient) FindPreferenceCandidateByID(ctx context.Context, userID, candidateID string) (moviecatalog.StorageHostPreferenceCandidate, bool, error) {
	return c.findCandidate(ctx, "candidate_by_id", movieCatalogCandidateQueryPayload{CandidateID: candidateID, UserID: userID}, candidateID, userID, "id")
}
func (c *MovieCatalogClient) FindPreferenceCandidateByRequestID(ctx context.Context, userID, requestID string) (moviecatalog.StorageHostPreferenceCandidate, bool, error) {
	return c.findCandidate(ctx, "candidate_by_request", movieCatalogRequestCandidatePayload{RequestID: requestID, UserID: userID}, requestID, userID, "request_id")
}
func (c *MovieCatalogClient) findCandidate(ctx context.Context, op string, payload any, key, userID, column string) (moviecatalog.StorageHostPreferenceCandidate, bool, error) {
	if c == nil || c.client == nil {
		return moviecatalog.StorageHostPreferenceCandidate{}, false, NewError(ErrorCodeStoreUnavailable, "movie catalog client is unavailable")
	}
	var out movieCatalogCandidateResult
	if err := c.client.Call(ctx, GroupMovieCatalog, op, payload, &out); err != nil {
		return moviecatalog.StorageHostPreferenceCandidate{}, false, err
	}
	if !out.Found {
		return moviecatalog.StorageHostPreferenceCandidate{}, false, nil
	}
	if out.Candidate == nil || !validMovieCatalogCandidate(*out.Candidate) || out.Candidate.UserID != userID || (column == "id" && out.Candidate.ID != key) || (column == "request_id" && out.Candidate.RequestID != key) {
		return moviecatalog.StorageHostPreferenceCandidate{}, false, NewError(ErrorCodeOutcomeUnknown, "movie catalog candidate result is malformed")
	}
	return *out.Candidate, true, nil
}
func (c *MovieCatalogClient) SavePreferenceCandidate(ctx context.Context, candidate moviecatalog.StorageHostPreferenceCandidate) (moviecatalog.StorageHostCandidateResult, error) {
	if c == nil || c.client == nil {
		return moviecatalog.StorageHostCandidateResult{}, NewError(ErrorCodeStoreUnavailable, "movie catalog client is unavailable")
	}
	if !validMovieCatalogCandidate(candidate) {
		return moviecatalog.StorageHostCandidateResult{}, NewError(ErrorCodeSchemaRejected, "movie catalog candidate rejected")
	}
	var out movieCatalogCandidateWriteResult
	if err := c.client.Call(ctx, GroupMovieCatalog, "candidate_save", movieCatalogCandidatePayload{Candidate: candidate}, &out); err != nil {
		return moviecatalog.StorageHostCandidateResult{}, err
	}
	result := moviecatalog.StorageHostCandidateResult{Candidate: out.Candidate, Replay: out.Replay}
	if !validMovieCatalogCandidateResult(result, candidate) {
		return moviecatalog.StorageHostCandidateResult{}, NewError(ErrorCodeOutcomeUnknown, "movie catalog candidate result is malformed")
	}
	return result, nil
}

var _ MovieCatalogGroupOwner = (*moviecatalog.StorageHostSQLiteStore)(nil)
