// Package storagehost exposes CORE-owned persistence operations over a typed
// authenticated RPC so that a CORE process can run on a different host than the
// durable storage host. It is not a general SQL or file API: only operations
// registered by the persistence owner are reachable.
package storagehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sync"
)

const (
	// ContractVersion is the wire contract identifier. Mismatches are rejected
	// before any store is touched.
	ContractVersion = "rencrow-storage-rpc/v1"
	// RPCPath is the only served path. Anything else, including queries, is 404.
	RPCPath = "/v1/rpc"
	// GroupHost holds the protocol-level read operations.
	GroupHost = "host"

	// maxRequestBytes remains the protocol limit for every ordinary operation
	// and for client responses. Only the closed ChatGPT Raw batch import accepts
	// a larger request: its canonical owner payload may itself be 64 MiB, and
	// the typed JSON envelope adds bounded metadata. The 96 MiB cap is applied
	// only after authentication; every other operation retains the 32 MiB cap.
	maxRequestBytes          = 32 << 20
	maxCommonRawRequestBytes = 96 << 20
	resultPageBytes          = 8 << 20
	requestAdmissionPrefix   = 512
)

// Typed error codes. Clients must surface these verbatim; there is no silent
// fallback to a local store.
const (
	ErrorCodeUnauthorized         = "unauthorized"
	ErrorCodeContractMismatch     = "contract_version_mismatch"
	ErrorCodeOperationUnsupported = "operation_unsupported"
	ErrorCodeSchemaRejected       = "schema_rejected"
	ErrorCodeDuplicateConflict    = "duplicate_op_conflict"
	ErrorCodeGenerationStale      = "writer_generation_stale"
	ErrorCodeStoreUnavailable     = "store_unavailable"
	ErrorCodeForbidden            = "forbidden"
	ErrorCodeUnreachable          = "storage_host_unreachable"
	ErrorCodeOutcomeUnknown       = "outcome_unknown"
)

// Error is the wire-level typed error.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *Error) Error() string {
	if e.Message == "" {
		return "storagehost: " + e.Code
	}
	return "storagehost: " + e.Code + ": " + e.Message
}

// NewError builds a typed error that registered operations may return so the
// code survives the wire boundary.
func NewError(code, message string) *Error {
	return &Error{Code: code, Message: message}
}

// Request is the single request envelope. Unknown fields are rejected.
type Request struct {
	Contract   string          `json:"contract"`
	OpID       string          `json:"op_id"`
	Group      string          `json:"group"`
	Op         string          `json:"op"`
	Generation int64           `json:"generation,omitempty"`
	Payload    json.RawMessage `json:"payload,omitempty"`
}

// Response is the single response envelope.
type Response struct {
	Result        json.RawMessage `json:"result,omitempty"`
	ResultPageSet *ResultPageSet  `json:"result_page_set,omitempty"`
	Error         *Error          `json:"error,omitempty"`
}

// OperationSpec describes a registered operation.
type OperationSpec struct {
	Group    string `json:"group"`
	Op       string `json:"op"`
	Mutating bool   `json:"mutating"`
}

// ContractInfo is returned by the host contract operation.
type ContractInfo struct {
	ContractVersion string          `json:"contract_version"`
	Generation      int64           `json:"generation"`
	Operations      []OperationSpec `json:"operations,omitempty"`
}

// ResultRecord is the answer to a result query for an op_id.
type ResultRecord struct {
	Found   bool            `json:"found"`
	Status  string          `json:"status,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	PageSet *ResultPageSet  `json:"page_set,omitempty"`
	Page    *ResultPage     `json:"page,omitempty"`
}

// ResultPageSet is a closed reference to the complete durable result of one
// common_raw/intake operation. It is not a general range, path, or blob API.
type ResultPageSet struct {
	ContractVersion   string `json:"contract_version"`
	OpID              string `json:"op_id"`
	Group             string `json:"group"`
	Op                string `json:"op"`
	JournalGeneration int64  `json:"journal_generation"`
	ResultBytes       int64  `json:"result_bytes"`
	ResultSHA256      string `json:"result_sha256"`
	PageSize          int64  `json:"page_size"`
	PageCount         int    `json:"page_count"`
}

// ResultPage is one fixed-size, journal-bound slice of a ResultPageSet.
type ResultPage struct {
	PageSet    ResultPageSet `json:"page_set"`
	PageIndex  int           `json:"page_index"`
	PageSHA256 string        `json:"page_sha256"`
	Data       []byte        `json:"data"`
}

type resultPageRequest struct {
	PageSet   ResultPageSet `json:"page_set"`
	PageIndex int           `json:"page_index"`
}

type resultQueryRequest struct {
	OpID string             `json:"op_id"`
	Page *resultPageRequest `json:"page,omitempty"`
}

type journalEntryView struct {
	OpID        string          `json:"op_id"`
	Group       string          `json:"group"`
	Op          string          `json:"op"`
	PayloadHash string          `json:"payload_hash"`
	Scope       string          `json:"scope"`
	Generation  int64           `json:"generation"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	PageSet     *ResultPageSet  `json:"result_page_set,omitempty"`
}

// OperationFunc runs a registered operation against the owner store.
type OperationFunc func(ctx context.Context, payload json.RawMessage) (any, error)

// MutationMetadata identifies the exact owner mutation accepted by the storage
// host. RequestGeneration is the authenticated request's current writer
// generation; JournalGeneration is the generation recorded when this op_id was
// first begun. They are equal during execution and may differ during recovery
// after a host restart. Payload is an isolated copy of the bounded JSON request
// payload; callbacks must treat it as read-only.
type MutationMetadata struct {
	OpID              string
	Payload           json.RawMessage
	RequestGeneration int64
	JournalGeneration int64
}

// OwnerMutationFunc executes a mutating operation with its stable identity.
type OwnerMutationFunc func(ctx context.Context, mutation MutationMetadata) (any, error)

type reconciliationDisposition uint8

const (
	reconciliationUnknown reconciliationDisposition = iota
	reconciliationCommitted
	reconciliationConfirmedNotCommitted
)

// ReconcileDecision is a closed result from a registered owner reconciliation
// hook. Its zero value is UnknownOutcome. Use the constructors to report a
// committed result or proof that the mutation did not commit.
type ReconcileDecision struct {
	disposition reconciliationDisposition
	result      any
}

// Committed reports that the owner has durable proof that this op_id committed
// and provides the original result to journal and return to the caller.
func Committed(result any) ReconcileDecision {
	return ReconcileDecision{disposition: reconciliationCommitted, result: result}
}

// ConfirmedNotCommitted reports owner-side durable proof that this op_id did
// not commit. It is the only disposition that authorizes re-execution.
func ConfirmedNotCommitted() ReconcileDecision {
	return ReconcileDecision{disposition: reconciliationConfirmedNotCommitted}
}

// UnknownOutcome leaves the begun journal entry intact and blocks re-execution.
func UnknownOutcome() ReconcileDecision { return ReconcileDecision{} }

// OwnerReconcileFunc performs a read-only, proof-based lookup in the owner
// store. It is called only for a matching begun journal entry after request
// authentication and current-generation validation. It must not mutate owner
// state, run arbitrary SQL, inspect filesystem paths, or serialize callbacks.
type OwnerReconcileFunc func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error)

type recoverableOperation struct {
	execute   OwnerMutationFunc
	reconcile OwnerReconcileFunc
}

var opIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// errProcessCrashAfterCommit is the test seam panic that models the storage
// host process dying between the store commit and the outcome journal write.
var errProcessCrashAfterCommit = errors.New("storagehost: process crashed after the store commit")

// HandlerConfig configures a storage host handler.
type HandlerConfig struct {
	// Token is the shared bearer credential. It is compared in constant time
	// and never echoed back in an error or log line.
	Token string
	// JournalDir is the storage host durable directory that holds the
	// idempotency journal and the monotonic writer generation epoch.
	JournalDir string
}

// Handler serves registered operations on the storage host.
type Handler struct {
	token      string
	scope      string
	generation int64

	mu          sync.Mutex
	ops         map[string]OperationFunc
	specs       map[string]OperationSpec
	recoverable map[string]recoverableOperation

	journal *journal

	// crashAfterCommitFor is a test seam that aborts the storage host process
	// right after the owner store commit and before the outcome is journaled.
	crashAfterCommitFor string

	// writeMu serialises mutating operations: the storage host is the single
	// writer for its durable stores.
	writeMu sync.Mutex
}

// NewHandler builds a storage host handler. An empty token or journal directory
// fails closed: there is no unauthenticated or unjournaled storage host.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	if cfg.Token == "" {
		return nil, errors.New("storagehost: token required")
	}
	if cfg.JournalDir == "" {
		return nil, errors.New("storagehost: journal dir required")
	}
	jn, err := openJournal(cfg.JournalDir)
	if err != nil {
		return nil, err
	}
	return &Handler{
		token:       cfg.Token,
		scope:       scopeHash(cfg.Token),
		generation:  jn.Generation(),
		ops:         map[string]OperationFunc{},
		specs:       map[string]OperationSpec{},
		recoverable: map[string]recoverableOperation{},
		journal:     jn,
	}, nil
}

// Register adds an operation. Mutating operations are journaled per op_id.
func (h *Handler) Register(group, op string, mutating bool, fn OperationFunc) error {
	if group == "" || op == "" || fn == nil {
		return errors.New("storagehost: invalid operation registration")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := opKey(group, op)
	if _, dup := h.ops[key]; dup {
		return errors.New("storagehost: duplicate operation " + group + "/" + op)
	}
	if _, dup := h.recoverable[key]; dup {
		return errors.New("storagehost: duplicate operation " + group + "/" + op)
	}
	h.ops[key] = fn
	h.specs[key] = OperationSpec{Group: group, Op: op, Mutating: mutating}
	return nil
}

// RegisterRecoverable adds a mutating operation with explicit op_id metadata
// and an owner reconciliation hook. The reconciliation hook is read-only and
// may authorize re-execution only by returning ConfirmedNotCommitted after
// proving from owner state that the original operation did not commit.
func (h *Handler) RegisterRecoverable(group, op string, execute OwnerMutationFunc, reconcile OwnerReconcileFunc) error {
	if group == "" || op == "" || execute == nil || reconcile == nil {
		return errors.New("storagehost: invalid recoverable operation registration")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	key := opKey(group, op)
	if _, dup := h.ops[key]; dup {
		return errors.New("storagehost: duplicate operation " + group + "/" + op)
	}
	if _, dup := h.recoverable[key]; dup {
		return errors.New("storagehost: duplicate operation " + group + "/" + op)
	}
	h.recoverable[key] = recoverableOperation{execute: execute, reconcile: reconcile}
	h.specs[key] = OperationSpec{Group: group, Op: op, Mutating: true}
	return nil
}

// Generation is the storage host writer generation.
func (h *Handler) Generation() int64 { return h.generation }

// Close releases the durable journal handles and the writer lock. It is
// idempotent; the writer lock is held for the whole handler lifetime.
func (h *Handler) Close() error { return h.journal.close() }

func opKey(group, op string) string { return group + "\x00" + op }

// ServeHTTP implements http.Handler.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != RPCPath || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeResponse(w, http.StatusMethodNotAllowed, Response{Error: NewError(ErrorCodeSchemaRejected, "method not allowed")})
		return
	}
	if !h.authorized(r) {
		writeResponse(w, http.StatusUnauthorized, Response{Error: NewError(ErrorCodeUnauthorized, "bearer token rejected")})
		return
	}

	// Only the canonical, authenticated Common Raw ChatGPT batch envelope gets
	// the larger read budget. Inspect a small prefix from the body itself (not
	// an untrusted header), then replay it through the selected total-byte
	// limit so reordered or unqualified large bodies stop at 32 MiB.
	prefix := make([]byte, requestAdmissionPrefix)
	prefixBytes, _ := io.ReadFull(r.Body, prefix)
	largeCommonRawPrefix := isCanonicalLargeCommonRawPrefix(prefix[:prefixBytes])
	requestLimit := int64(maxRequestBytes)
	if largeCommonRawPrefix {
		requestLimit = maxCommonRawRequestBytes
	}
	requestBody := io.MultiReader(bytes.NewReader(prefix[:prefixBytes]), r.Body)
	limited := &io.LimitedReader{R: requestBody, N: requestLimit + 1}
	counted := &countingReader{reader: limited}
	var req Request
	dec := json.NewDecoder(counted)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "request envelope rejected")})
		return
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "trailing request bytes")})
		return
	}
	largeCommonRawImport := largeCommonRawPrefix && req.Group == GroupCommonRaw && req.Op == commonRawChatGPTImportOp
	if counted.count > requestLimit || (!largeCommonRawImport && counted.count > maxRequestBytes) {
		writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "request envelope exceeds its bounded group limit")})
		return
	}
	// group/op may name anything: an unregistered pair is answered as
	// operation_unsupported, not as a schema error.
	if !opIDPattern.MatchString(req.OpID) || len(req.Group) > 64 || len(req.Op) > 64 {
		writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "request fields rejected")})
		return
	}
	if len(req.Payload) == 0 {
		req.Payload = json.RawMessage("{}")
	}
	if !json.Valid(req.Payload) {
		writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "payload not json")})
		return
	}
	if req.Contract != ContractVersion {
		writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeContractMismatch, "storage host contract is "+ContractVersion)})
		return
	}

	if req.Group == GroupHost {
		h.serveHostOp(w, req)
		return
	}
	if req.Generation != h.generation {
		writeResponse(w, http.StatusConflict, Response{Error: NewError(ErrorCodeGenerationStale, "client writer generation does not match the storage host epoch")})
		return
	}

	h.mu.Lock()
	key := opKey(req.Group, req.Op)
	fn, legacyOK := h.ops[key]
	recoverable, recoverableOK := h.recoverable[key]
	spec := h.specs[key]
	h.mu.Unlock()
	if !legacyOK && !recoverableOK {
		writeResponse(w, http.StatusNotFound, Response{Error: NewError(ErrorCodeOperationUnsupported, "operation "+req.Group+"/"+req.Op+" is not served")})
		return
	}
	if !spec.Mutating {
		h.execute(w, r, req, fn)
		return
	}
	h.executeMutating(w, r, req, fn, recoverable)
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += int64(n)
	return n, err
}

func isCanonicalLargeCommonRawPrefix(prefix []byte) bool {
	if !bytes.HasPrefix(prefix, []byte(`{"contract":`)) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(prefix))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	fields := []struct {
		name string
		want string
	}{
		{name: "contract", want: ContractVersion},
		{name: "op_id"},
		{name: "group", want: GroupCommonRaw},
		{name: "op", want: commonRawChatGPTImportOp},
	}
	for _, field := range fields {
		key, err := decoder.Token()
		if err != nil || key != field.name {
			return false
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return false
		}
		if field.name == "op_id" && !opIDPattern.MatchString(value) {
			return false
		}
		if field.want != "" && value != field.want {
			return false
		}
	}
	return true
}

func (h *Handler) authorized(r *http.Request) bool {
	values, ok := r.Header["Authorization"]
	if !ok || len(values) != 1 {
		return false
	}
	const prefix = "Bearer "
	header := values[0]
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	provided := header[len(prefix):]
	if containsComma(provided) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(h.token)) == 1
}

func containsComma(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return true
		}
	}
	return false
}

func (h *Handler) serveHostOp(w http.ResponseWriter, req Request) {
	switch req.Op {
	case "contract":
		h.mu.Lock()
		specs := make([]OperationSpec, 0, len(h.specs))
		for _, s := range h.specs {
			specs = append(specs, s)
		}
		h.mu.Unlock()
		writeResponse(w, http.StatusOK, Response{Result: mustJSON(ContractInfo{
			ContractVersion: ContractVersion,
			Generation:      h.generation,
			Operations:      specs,
		})})
	case "result":
		var in resultQueryRequest
		decoder := json.NewDecoder(bytes.NewReader(req.Payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&in); err != nil || decoder.Decode(new(any)) != io.EOF || !opIDPattern.MatchString(in.OpID) {
			writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "result query rejected")})
			return
		}
		entry, found := h.journal.lookup(in.OpID)
		if !found {
			writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{})})
			return
		}
		if in.Page != nil {
			page, pageSet, err := resultPageForEntry(entry, *in.Page)
			if err != nil {
				writeResponse(w, http.StatusBadRequest, Response{Error: NewError(ErrorCodeSchemaRejected, "result page query rejected")})
				return
			}
			writeResponse(w, http.StatusOK, Response{Result: mustJSON(ResultRecord{Found: true, Status: entry.Status, PageSet: &pageSet, Page: &page})})
			return
		}
		record := ResultRecord{Found: true, Status: entry.Status}
		if pageSet, paged := resultPageSetForEntry(entry); paged {
			record.PageSet = &pageSet
		} else {
			record.Result = entry.Result
		}
		writeResponse(w, http.StatusOK, Response{Result: mustJSON(record)})
	case "journal":
		entries := h.journal.entries()
		views := make([]journalEntryView, 0, len(entries))
		for _, entry := range entries {
			view := journalEntryView{OpID: entry.OpID, Group: entry.Group, Op: entry.Op, PayloadHash: entry.PayloadHash, Scope: entry.Scope, Generation: entry.Generation, Status: entry.Status}
			if pageSet, paged := resultPageSetForEntry(entry); paged {
				view.PageSet = &pageSet
			} else {
				view.Result = entry.Result
			}
			views = append(views, view)
		}
		writeResponse(w, http.StatusOK, Response{Result: mustJSON(struct {
			Entries []journalEntryView `json:"entries"`
		}{Entries: views})})
	default:
		writeResponse(w, http.StatusNotFound, Response{Error: NewError(ErrorCodeOperationUnsupported, "host operation "+req.Op+" is not served")})
	}
}

func resultPageSetForEntry(entry JournalEntry) (ResultPageSet, bool) {
	if entry.Status != journalStatusDone || entry.Group != GroupCommonRaw || entry.Op != commonRawIntakeOp || len(entry.Result) == 0 {
		return ResultPageSet{}, false
	}
	const resultEnvelopeBytes = len(`{"result":`) + 1
	if len(entry.Result)+resultEnvelopeBytes <= maxRequestBytes {
		return ResultPageSet{}, false
	}
	resultHash := sha256.Sum256(entry.Result)
	pageCount := (len(entry.Result) + resultPageBytes - 1) / resultPageBytes
	return ResultPageSet{
		ContractVersion:   ContractVersion,
		OpID:              entry.OpID,
		Group:             entry.Group,
		Op:                entry.Op,
		JournalGeneration: entry.Generation,
		ResultBytes:       int64(len(entry.Result)),
		ResultSHA256:      hex.EncodeToString(resultHash[:]),
		PageSize:          resultPageBytes,
		PageCount:         pageCount,
	}, true
}

func resultPageForEntry(entry JournalEntry, request resultPageRequest) (ResultPage, ResultPageSet, error) {
	pageSet, ok := resultPageSetForEntry(entry)
	if !ok || request.PageSet != pageSet || request.PageIndex < 0 || request.PageIndex >= pageSet.PageCount {
		return ResultPage{}, ResultPageSet{}, errors.New("storagehost: result page binding rejected")
	}
	start := request.PageIndex * resultPageBytes
	end := start + resultPageBytes
	if end > len(entry.Result) {
		end = len(entry.Result)
	}
	data := append([]byte(nil), entry.Result[start:end]...)
	pageHash := sha256.Sum256(data)
	return ResultPage{PageSet: pageSet, PageIndex: request.PageIndex, PageSHA256: hex.EncodeToString(pageHash[:]), Data: data}, pageSet, nil
}

func writeMutationResult(w http.ResponseWriter, opID, group, op string, journalGeneration int64, result json.RawMessage) {
	entry := JournalEntry{OpID: opID, Group: group, Op: op, Generation: journalGeneration, Status: journalStatusDone, Result: result}
	response := Response{Result: result}
	if pageSet, paged := resultPageSetForEntry(entry); paged {
		response.Result = nil
		response.ResultPageSet = &pageSet
	}
	writeResponse(w, http.StatusOK, response)
}

func (h *Handler) execute(w http.ResponseWriter, r *http.Request, req Request, fn OperationFunc) {
	result, err := fn(r.Context(), req.Payload)
	if err != nil {
		h.writeOpError(w, err)
		return
	}
	writeResponse(w, http.StatusOK, Response{Result: mustJSON(result)})
}

func (h *Handler) executeMutating(w http.ResponseWriter, r *http.Request, req Request, fn OperationFunc, recoverable recoverableOperation) {
	h.writeMu.Lock()
	defer h.writeMu.Unlock()

	hash := payloadHash(req.Group, req.Op, req.Payload)
	if entry, found := h.journal.lookup(req.OpID); found {
		switch {
		case entry.Scope != h.scope:
			writeResponse(w, http.StatusConflict, Response{Error: NewError(ErrorCodeDuplicateConflict, "op_id "+req.OpID+" was issued under a different credential")})
			return
		case entry.PayloadHash != hash:
			writeResponse(w, http.StatusConflict, Response{Error: NewError(ErrorCodeDuplicateConflict, "op_id "+req.OpID+" was already used with a different payload")})
			return
		case entry.Status == journalStatusBegun:
			if recoverable.reconcile == nil {
				writeUnknownOutcome(w, req.OpID, "started but did not complete")
				return
			}
			decision, err := recoverable.reconcile(r.Context(), mutationMetadata(req, req.Generation, entry.Generation))
			if err != nil {
				writeUnknownOutcome(w, req.OpID, "owner reconciliation failed")
				return
			}
			switch decision.disposition {
			case reconciliationCommitted:
				encoded, err := json.Marshal(decision.result)
				if err != nil {
					writeUnknownOutcome(w, req.OpID, "owner result could not be encoded")
					return
				}
				if err := h.journal.complete(req.OpID, encoded); err != nil {
					writeUnknownOutcome(w, req.OpID, "reconciled result could not be journaled")
					return
				}
				writeMutationResult(w, req.OpID, entry.Group, entry.Op, entry.Generation, encoded)
				return
			case reconciliationConfirmedNotCommitted:
				// The owner supplied explicit durable proof of non-commit. Remove
				// the prior identity and begin the same op_id under the current
				// writer generation before invoking the owner again.
				if err := h.journal.abort(req.OpID); err != nil {
					writeUnknownOutcome(w, req.OpID, "confirmed non-commit could not be journaled")
					return
				}
				if err := h.journal.begin(req.OpID, req.Group, req.Op, hash, h.scope, h.generation); err != nil {
					writeUnknownOutcome(w, req.OpID, "new attempt could not be journaled")
					return
				}
			default:
				writeUnknownOutcome(w, req.OpID, "owner could not confirm the outcome")
				return
			}
		default:
			writeMutationResult(w, entry.OpID, entry.Group, entry.Op, entry.Generation, entry.Result)
			return
		}
	} else if err := h.journal.begin(req.OpID, req.Group, req.Op, hash, h.scope, h.generation); err != nil {
		writeResponse(w, http.StatusServiceUnavailable, Response{Error: NewError(ErrorCodeStoreUnavailable, "idempotency journal unavailable")})
		return
	}
	var result any
	var err error
	if recoverable.execute != nil {
		result, err = recoverable.execute(r.Context(), mutationMetadata(req, h.generation, h.generation))
	} else {
		result, err = fn(r.Context(), req.Payload)
	}
	if err != nil {
		if provesRollback(err) {
			if abortErr := h.journal.abort(req.OpID); abortErr != nil {
				writeResponse(w, http.StatusServiceUnavailable, Response{Error: NewError(ErrorCodeOutcomeUnknown, "operation rolled back but the journal abort failed")})
				return
			}
			h.writeOpError(w, err)
			return
		}
		// The owner failed without proving a rollback: the commit may have
		// landed. The begun entry is kept so a retry of this op_id is
		// outcome_unknown instead of a blind second execution.
		writeResponse(w, http.StatusServiceUnavailable, Response{Error: NewError(ErrorCodeOutcomeUnknown, "op_id "+req.OpID+" failed without a proven rollback: "+rollbackUncertaintyCode(err))})
		return
	}
	encoded := mustJSON(result)
	if h.crashAfterCommitFor != "" && h.crashAfterCommitFor == req.OpID {
		h.crashAfterCommitFor = ""
		// The storage host process died with the commit landed and no recorded
		// outcome: the connection is torn down without a response.
		panic(errProcessCrashAfterCommit)
	}
	if err := h.journal.complete(req.OpID, encoded); err != nil {
		writeResponse(w, http.StatusServiceUnavailable, Response{Error: NewError(ErrorCodeOutcomeUnknown, "operation executed but the result journal write failed")})
		return
	}
	writeMutationResult(w, req.OpID, req.Group, req.Op, h.generation, encoded)
}

func writeUnknownOutcome(w http.ResponseWriter, opID, reason string) {
	writeResponse(w, http.StatusServiceUnavailable, Response{Error: NewError(ErrorCodeOutcomeUnknown, "op_id "+opID+": "+reason)})
}

func mutationMetadata(req Request, requestGeneration, journalGeneration int64) MutationMetadata {
	return MutationMetadata{
		OpID:              req.OpID,
		Payload:           append(json.RawMessage(nil), req.Payload...),
		RequestGeneration: requestGeneration,
		JournalGeneration: journalGeneration,
	}
}

// provesRollback reports whether an owner error demonstrates that no store
// work survived. Only an explicit rollback-confirmed error (RolledBack()==true)
// is proof: a typed code alone is not, because the owner may commit first and
// surface the failure afterwards (store_unavailable included). An owner that
// commits and then fails must return an error without the proof so the begun
// entry survives and the op_id is never re-executed blindly.
func provesRollback(err error) bool {
	var rolled interface{ RolledBack() bool }
	if errors.As(err, &rolled) {
		return rolled.RolledBack()
	}
	return false
}

func rollbackUncertaintyCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "owner_error"
}

func (h *Handler) writeOpError(w http.ResponseWriter, err error) {
	var e *Error
	if errors.As(err, &e) {
		writeResponse(w, statusForError(e), Response{Error: e})
		return
	}
	writeResponse(w, http.StatusInternalServerError, Response{Error: NewError(ErrorCodeStoreUnavailable, "store operation failed")})
}

func statusForError(e *Error) int {
	switch e.Code {
	case ErrorCodeStoreUnavailable, ErrorCodeUnreachable, ErrorCodeOutcomeUnknown:
		return http.StatusServiceUnavailable
	case ErrorCodeDuplicateConflict, ErrorCodeGenerationStale:
		return http.StatusConflict
	case ErrorCodeUnauthorized:
		return http.StatusUnauthorized
	case ErrorCodeForbidden:
		return http.StatusForbidden
	default:
		return http.StatusBadRequest
	}
}

func writeResponse(w http.ResponseWriter, status int, resp Response) {
	body, err := json.Marshal(resp)
	if err != nil {
		body = []byte(`{"error":{"code":"schema_rejected","message":"response encode failed"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func mustJSON(v any) json.RawMessage {
	body, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`null`)
	}
	return body
}
