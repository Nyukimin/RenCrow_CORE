package storagehost

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultCallTimeout bounds one storage host operation. It is deliberately
// above the 20s window that would falsely declare the production startup dead.
const DefaultCallTimeout = 30 * time.Second

// errConnectionResetAfterSend marks the test seam where the request reached the
// storage host but the response was lost. Callers must resolve the outcome
// through the journal; they may not assume the operation did not run.
var errConnectionResetAfterSend = errors.New("storagehost: connection reset after send")

// ClientConfig configures a CORE side storage host client.
type ClientConfig struct {
	Endpoint   string
	Token      string
	HTTPClient *http.Client
	Timeout    time.Duration
}

// Client is the CORE side handle to a remote storage host. It never falls back
// to a local store: every failure is returned as a typed error.
type Client struct {
	endpoint   string
	token      string
	scope      string
	httpClient *http.Client
	timeout    time.Duration

	mu         sync.Mutex
	generation int64
	specs      map[string]bool

	// opID pins the op_id for the next mutating call. Empty means the client
	// issues a fresh id per call.
	opID string
	// sendHook is a test seam invoked once a response has been received.
	sendHook func() error

	// lastOpID is the op_id issued by the most recent prepare.
	lastOpID string
	// pendingCalls retains unresolved ordinary Call identities by exact
	// group/op/payload bytes. Entries are shared by identical calls while the
	// outcome is unresolved; prepare never consumes them.
	pendingCalls map[string]*callIdentity
	// resultLookupBroken is a test seam that blocks host.result resolution.
	resultLookupBroken bool
}

type callIdentity struct {
	opID     string
	inFlight int
	done     bool
	failed   bool
}

// Operation is a caller-owned mutating call. Its op_id survives a lost
// response, so retrying the same operation resolves the recorded outcome
// instead of executing a second time.
type Operation struct {
	// OpID is fixed for the operation lifetime.
	OpID string
	// Group and Op name the registered storage host operation.
	Group string
	Op    string

	payload json.RawMessage
	err     error

	mu      sync.Mutex
	pending bool
}

// NewClient builds a storage host client. The endpoint and token are required:
// a remote profile with neither is a configuration error, not a local store.
func NewClient(cfg ClientConfig) (*Client, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if endpoint == "" {
		return nil, errors.New("storagehost: endpoint required")
	}
	if cfg.Token == "" {
		return nil, errors.New("storagehost: token required")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultCallTimeout
	}
	return &Client{
		endpoint:     endpoint + RPCPath,
		token:        cfg.Token,
		scope:        scopeHash(cfg.Token),
		httpClient:   httpClient,
		timeout:      timeout,
		specs:        map[string]bool{},
		pendingCalls: map[string]*callIdentity{},
	}, nil
}

// Generation is the writer epoch the client learned at handshake. Every write
// carries it, and the storage host rejects a stale or future epoch.
func (c *Client) Generation() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

// Handshake verifies the wire contract and adopts the storage host writer
// generation. A contract mismatch or an unreachable host is terminal.
func (c *Client) Handshake(ctx context.Context) error {
	info, err := c.Contract(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.generation = info.Generation
	c.specs = make(map[string]bool, len(info.Operations))
	for _, spec := range info.Operations {
		c.specs[opKey(spec.Group, spec.Op)] = spec.Mutating
	}
	c.mu.Unlock()
	return nil
}

// Contract reads the storage host contract and served operation list.
func (c *Client) Contract(ctx context.Context) (ContractInfo, error) {
	var out ContractInfo
	err := c.call(ctx, GroupHost, "contract", json.RawMessage("{}"), &out, false)
	return out, err
}

// Call is the sequential convenience API for one mutating intent at a time.
// An identical group/op/payload call is treated as a retry while its earlier
// outcome is unresolved. Call cannot express two distinct identical intents
// concurrently; use one NewOperation handle per intent and retry it with Do.
// Mutating calls retain their op_id across uncertainty and release it only
// after a DONE result is decoded successfully.
func (c *Client) Call(ctx context.Context, group, op string, payload any, out any) error {
	body, err := encodePayload(payload)
	if err != nil {
		return err
	}
	return c.call(ctx, group, op, body, out, true)
}

func (c *Client) call(ctx context.Context, group, op string, payload json.RawMessage, out any, resolve bool) error {
	opID, generation, mutating, key, identity := c.prepare(group, op, payload)
	confirmedDone := false
	defer func() {
		if identity != nil {
			c.finishCall(key, identity, confirmedDone)
		}
	}()
	req := Request{
		Contract:   ContractVersion,
		OpID:       opID,
		Group:      group,
		Op:         op,
		Generation: generation,
		Payload:    payload,
	}
	var resp Response
	if err := c.do(ctx, req, &resp, resolve); err != nil {
		if resolve && mutating && isLostResponse(err) {
			resErr := c.resolve(ctx, opID, group, op, out)
			confirmedDone = resErr == nil
			return resErr
		}
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	if err := c.decodeMutationResponse(ctx, opID, group, op, resp, out); err != nil {
		if resolve && mutating {
			return NewError(ErrorCodeOutcomeUnknown, "op_id "+opID+": completed result could not be confirmed; outcome remains nonrepeatable")
		}
		return err
	}
	confirmedDone = identity != nil
	return nil
}

func (c *Client) decodeMutationResponse(ctx context.Context, opID, group, op string, response Response, out any) error {
	if response.ResultPageSet == nil {
		if len(response.Result) == 0 {
			return errors.New("storagehost: successful mutation response omitted its result")
		}
		return decodeResult(response.Result, out)
	}
	if len(response.Result) != 0 {
		return errors.New("storagehost: response mixes an inline result and a result page set")
	}
	result, err := c.fetchResultPages(ctx, opID, group, op, *response.ResultPageSet)
	if err != nil {
		return err
	}
	return decodeResult(result, out)
}

func (c *Client) decodeResultRecord(ctx context.Context, opID, expectedGroup, expectedOp string, record ResultRecord, out any) error {
	if record.PageSet == nil {
		if record.Page != nil {
			return errors.New("storagehost: result page omitted its page set")
		}
		if len(record.Result) == 0 {
			return errors.New("storagehost: completed result record omitted its result")
		}
		return decodeResult(record.Result, out)
	}
	if len(record.Result) != 0 || record.Page != nil {
		return errors.New("storagehost: result record mixes a page set with another result form")
	}
	result, err := c.fetchResultPages(ctx, opID, expectedGroup, expectedOp, *record.PageSet)
	if err != nil {
		return err
	}
	return decodeResult(result, out)
}

func (c *Client) fetchResultPages(ctx context.Context, opID, group, op string, pageSet ResultPageSet) ([]byte, error) {
	if group != GroupCommonRaw || op != commonRawIntakeOp || pageSet.ContractVersion != ContractVersion || pageSet.OpID != opID || pageSet.Group != group || pageSet.Op != op || pageSet.JournalGeneration <= 0 || pageSet.ResultBytes <= maxRequestBytes-int64(len(`{"result":`)+1) || pageSet.ResultBytes > maxCommonRawIntakeResultBytes || pageSet.PageSize != resultPageBytes || pageSet.PageCount <= 0 || pageSet.PageCount > maxCommonRawIntakeResultPages || len(pageSet.ResultSHA256) != sha256.Size*2 || pageSet.ResultSHA256 != strings.ToLower(pageSet.ResultSHA256) {
		return nil, errors.New("storagehost: result page set binding rejected")
	}
	if _, err := hex.DecodeString(pageSet.ResultSHA256); err != nil {
		return nil, errors.New("storagehost: result page set digest rejected")
	}
	pageCount := (pageSet.ResultBytes-1)/pageSet.PageSize + 1
	if pageCount > int64(^uint(0)>>1) || pageSet.PageCount != int(pageCount) {
		return nil, errors.New("storagehost: result page set length rejected")
	}

	var result bytes.Buffer
	fullHash := sha256.New()
	for pageIndex := 0; pageIndex < pageSet.PageCount; pageIndex++ {
		query := resultQueryRequest{OpID: opID, Page: &resultPageRequest{PageSet: pageSet, PageIndex: pageIndex}}
		var record ResultRecord
		if err := c.call(ctx, GroupHost, "result", mustJSON(query), &record, false); err != nil {
			return nil, err
		}
		if !record.Found || record.Status != journalStatusDone || record.PageSet == nil || *record.PageSet != pageSet || record.Page == nil || record.Result != nil {
			return nil, errors.New("storagehost: result page response binding rejected")
		}
		page := record.Page
		if page.PageSet != pageSet || page.PageIndex != pageIndex || len(page.Data) == 0 || int64(len(page.Data)) > pageSet.PageSize || len(page.PageSHA256) != sha256.Size*2 || page.PageSHA256 != strings.ToLower(page.PageSHA256) {
			return nil, errors.New("storagehost: result page order or size rejected")
		}
		start := int64(pageIndex) * pageSet.PageSize
		wantBytes := pageSet.ResultBytes - start
		if wantBytes > pageSet.PageSize {
			wantBytes = pageSet.PageSize
		}
		if int64(len(page.Data)) != wantBytes {
			return nil, errors.New("storagehost: result page length rejected")
		}
		pageHash := sha256.Sum256(page.Data)
		if hex.EncodeToString(pageHash[:]) != page.PageSHA256 {
			return nil, errors.New("storagehost: result page digest rejected")
		}
		_, _ = result.Write(page.Data)
		_, _ = fullHash.Write(page.Data)
	}
	if int64(result.Len()) != pageSet.ResultBytes || hex.EncodeToString(fullHash.Sum(nil)) != pageSet.ResultSHA256 || !json.Valid(result.Bytes()) {
		return nil, errors.New("storagehost: complete result page set integrity rejected")
	}
	return result.Bytes(), nil
}

func (c *Client) finishCall(key string, identity *callIdentity, confirmedDone bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if confirmedDone {
		identity.done = true
	} else {
		identity.failed = true
	}
	if identity.inFlight > 0 {
		identity.inFlight--
	}
	if identity.inFlight == 0 {
		if identity.done && !identity.failed && c.pendingCalls[key] == identity {
			delete(c.pendingCalls, key)
			return
		}
		// A failure by any member keeps the identity after this wave, even if
		// another member observed DONE. Reset wave-local outcomes so a later
		// clean retry can conclusively release the identity.
		identity.done = false
		identity.failed = false
	}
}

func (c *Client) prepare(group, op string, payload json.RawMessage) (string, int64, bool, string, *callIdentity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	opID := c.opID
	mutating := c.specs[opKey(group, op)]
	key := payloadHash(group, op, payload)
	var identity *callIdentity
	if mutating {
		if c.pendingCalls == nil {
			c.pendingCalls = make(map[string]*callIdentity)
		}
		identity = c.pendingCalls[key]
		if opID == "" {
			if identity == nil {
				opID = newOpID()
				identity = &callIdentity{opID: opID}
				c.pendingCalls[key] = identity
			} else {
				opID = identity.opID
			}
		} else if identity == nil {
			identity = &callIdentity{opID: opID}
			c.pendingCalls[key] = identity
		} else if identity.opID != opID {
			// A test-only pinned ID must not replace a different unresolved
			// ordinary Call identity for the same payload.
			identity = nil
		}
		if identity != nil {
			if identity.inFlight == 0 {
				identity.done = false
				identity.failed = false
			}
			identity.inFlight++
		}
	} else if opID == "" {
		opID = newOpID()
	}
	if opID == "" {
		opID = newOpID()
	}
	c.lastOpID = opID
	return opID, c.generation, mutating, key, identity
}

// NewOperation starts a caller-owned operation lifetime. Create one handle per
// distinct intent, including distinct intents with identical payloads; retry
// that same handle with Do so its op_id is reused.
func (c *Client) NewOperation(group, op string, payload any) *Operation {
	body, err := encodePayload(payload)
	return &Operation{
		OpID:    newOpID(),
		Group:   group,
		Op:      op,
		payload: body,
		err:     err,
	}
}

// Do runs a caller-owned operation. After a lost response the operation is
// resolved through host.result on the next Do with the same op_id; it is
// never blindly re-executed.
func (c *Client) Do(ctx context.Context, op *Operation, out any) error {
	if op == nil {
		return NewError(ErrorCodeSchemaRejected, "operation is nil")
	}
	if op.err != nil {
		return op.err
	}
	op.mu.Lock()
	pending := op.pending
	op.mu.Unlock()
	if pending {
		return c.resolveOperation(ctx, op, out)
	}
	opID, generation := c.prepareOperation(op)
	req := Request{
		Contract:   ContractVersion,
		OpID:       opID,
		Group:      op.Group,
		Op:         op.Op,
		Generation: generation,
		Payload:    op.payload,
	}
	var resp Response
	if err := c.do(ctx, req, &resp, true); err != nil {
		if isLostResponse(err) {
			op.mu.Lock()
			op.pending = true
			op.mu.Unlock()
			return NewError(ErrorCodeOutcomeUnknown, "op_id "+opID+": response lost; retry the same operation to resolve the outcome")
		}
		return err
	}
	if resp.Error != nil {
		if resp.Error.Code == ErrorCodeOutcomeUnknown {
			op.mu.Lock()
			op.pending = true
			op.mu.Unlock()
		}
		return resp.Error
	}
	if err := c.decodeMutationResponse(ctx, opID, op.Group, op.Op, resp, out); err != nil {
		// The owner returned a successful mutating response, so a decode
		// failure cannot make a fresh operation safe. Keep this Operation on
		// its fixed op_id and resolve the same DONE record on later Do calls.
		op.mu.Lock()
		op.pending = true
		op.mu.Unlock()
		return NewError(ErrorCodeOutcomeUnknown, "op_id "+opID+": completed result could not be decoded; outcome remains nonrepeatable")
	}
	return nil
}

func (c *Client) prepareOperation(op *Operation) (string, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastOpID = op.OpID
	return op.OpID, c.generation
}

// resolveOperation asks the storage host what happened to the operation's own
// op_id. A broken or missing result record is outcome_unknown, never proof
// that the operation did not run.
func (c *Client) resolveOperation(ctx context.Context, op *Operation, out any) error {
	if c.resultLookupBroken {
		return NewError(ErrorCodeOutcomeUnknown, "op_id "+op.OpID+": result query unavailable; outcome not confirmed")
	}
	var rec ResultRecord
	err := c.call(ctx, GroupHost, "result", mustJSON(map[string]string{"op_id": op.OpID}), &rec, false)
	if err != nil {
		return NewError(ErrorCodeOutcomeUnknown, "op_id "+op.OpID+": outcome unresolved: "+err.Error())
	}
	switch {
	case rec.Found && rec.Status == journalStatusDone:
		if derr := c.decodeResultRecord(ctx, op.OpID, op.Group, op.Op, rec, out); derr != nil {
			// The commit is recorded but this consumer cannot decode it:
			// explicit uncertainty, never a generic schema rejection that
			// could be treated as a safe fresh retry.
			return NewError(ErrorCodeOutcomeUnknown, "op_id "+op.OpID+": recorded result could not be decoded; outcome not confirmed")
		}
		op.mu.Lock()
		op.pending = false
		op.mu.Unlock()
		return nil
	case rec.Found:
		return NewError(ErrorCodeOutcomeUnknown, "op_id "+op.OpID+" started but its commit is not confirmed")
	default:
		return NewError(ErrorCodeOutcomeUnknown, "op_id "+op.OpID+" outcome is not recorded; retry only after a read-your-writes check")
	}
}

// resolve asks the storage host what happened to op_id. An unknown outcome is
// reported as outcome_unknown; the absence of a journal entry is never read as
// "the operation did not run".
func (c *Client) resolve(ctx context.Context, opID, expectedGroup, expectedOp string, out any) error {
	if c.resultLookupBroken {
		return NewError(ErrorCodeOutcomeUnknown, "op_id "+opID+": result query unavailable; outcome not confirmed")
	}
	var rec ResultRecord
	err := c.call(ctx, GroupHost, "result", mustJSON(map[string]string{"op_id": opID}), &rec, false)
	if err != nil {
		return NewError(ErrorCodeOutcomeUnknown, "op_id "+opID+": outcome unresolved: "+err.Error())
	}
	switch {
	case rec.Found && rec.Status == journalStatusDone:
		if derr := c.decodeResultRecord(ctx, opID, expectedGroup, expectedOp, rec, out); derr != nil {
			return NewError(ErrorCodeOutcomeUnknown, "op_id "+opID+": recorded result could not be decoded; outcome not confirmed")
		}
		return nil
	case rec.Found:
		return NewError(ErrorCodeOutcomeUnknown, "op_id "+opID+" started but its commit is not confirmed")
	default:
		return NewError(ErrorCodeOutcomeUnknown, "op_id "+opID+" outcome is not recorded; retry only after a read-your-writes check")
	}
}

func (c *Client) send(ctx context.Context, req Request, out *Response) error {
	return c.do(ctx, req, out, true)
}

func (c *Client) do(ctx context.Context, req Request, out *Response, hook bool) error {
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	return c.postRawHook(ctx, body, out, hook)
}

// postRaw performs one round trip and applies the test send hook.
func (c *Client) postRaw(ctx context.Context, body []byte, out *Response) error {
	return c.postRawHook(ctx, body, out, true)
}

// postRawHook performs one round trip. Transport failures become
// storage_host_unreachable; there is no local fallback.
func (c *Client) postRawHook(ctx context.Context, body []byte, out *Response, hook bool) error {
	callCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return NewError(ErrorCodeSchemaRejected, "storage host request could not be built")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return NewError(ErrorCodeUnreachable, "storage host did not answer")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRequestBytes+1))
	if err != nil {
		return NewError(ErrorCodeUnreachable, "storage host response was truncated")
	}
	if len(raw) > maxRequestBytes {
		return NewError(ErrorCodeUnreachable, "storage host response exceeds the bounded response limit")
	}
	var decoded Response
	if err := json.Unmarshal(raw, &decoded); err != nil {
		// The response cannot be decoded: the operation may still have run.
		// Callers of mutating operations must resolve the outcome through
		// host.result instead of assuming the store was untouched.
		return NewError(ErrorCodeUnreachable, "storage host response could not be decoded")
	}
	if decoded.Error != nil {
		*out = decoded
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return NewError(ErrorCodeUnreachable, "storage host answered with an unexpected status")
	}
	if hook && c.sendHook != nil {
		// Test seam: the request reached the storage host but the response is
		// lost. The caller must resolve the outcome through the journal.
		if err := c.sendHook(); err != nil {
			return err
		}
	}
	*out = decoded
	return nil
}

func (c *Client) journalEntries(ctx context.Context) ([]JournalEntry, error) {
	var out struct {
		Entries []journalEntryView `json:"entries"`
	}
	if err := c.call(ctx, GroupHost, "journal", json.RawMessage("{}"), &out, false); err != nil {
		return nil, err
	}
	entries := make([]JournalEntry, 0, len(out.Entries))
	for _, view := range out.Entries {
		entry := JournalEntry{
			OpID: view.OpID, Group: view.Group, Op: view.Op, PayloadHash: view.PayloadHash,
			Scope: view.Scope, Generation: view.Generation, Status: view.Status, Result: view.Result,
		}
		if view.PageSet != nil {
			if len(view.Result) != 0 {
				return nil, errors.New("storagehost: journal entry mixes inline and paged results")
			}
			result, err := c.fetchResultPages(ctx, view.OpID, view.Group, view.Op, *view.PageSet)
			if err != nil {
				return nil, err
			}
			entry.Result = result
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// isOutcomeUnknown reports whether err is the typed outcome_unknown error.
func isOutcomeUnknown(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == ErrorCodeOutcomeUnknown
}

func isLostResponse(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == ErrorCodeUnreachable
	}
	return errors.Is(err, errConnectionResetAfterSend)
}

func encodePayload(payload any) (json.RawMessage, error) {
	if payload == nil {
		return json.RawMessage("{}"), nil
	}
	if raw, ok := payload.(json.RawMessage); ok {
		return raw, nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, NewError(ErrorCodeSchemaRejected, "payload could not be encoded")
	}
	return body, nil
}

func decodeResult(raw json.RawMessage, out any) error {
	if out == nil {
		return nil
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return NewError(ErrorCodeSchemaRejected, "storage host result did not match the expected shape")
	}
	return nil
}

func newOpID() string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "op-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return "op-" + hex.EncodeToString(buf[:])
}
