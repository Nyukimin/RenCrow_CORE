package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	requestFilename  = "transport_request.jsonl"
	responseFilename = "transport_response.jsonl"
)

type JSONLStore struct {
	mu           sync.RWMutex
	requestPath  string
	responsePath string
	batch        *jsonlbatch.Store
	// admissionMu protects the volatile read-your-writes view and queue ticket.
	// The queue worker never holds this mutex while it performs filesystem I/O.
	admissionMu  sync.Mutex
	writeQueue   *persistencequeue.Queue[persistencequeue.PersistenceCommand]
	lastTicket   persistencequeue.Ticket
	admissionErr error
	requests     map[modulecore.RequestID]domaintransport.Request
	responses    map[modulecore.ResponseID]domaintransport.Response
}

func NewJSONLStore(root string) (*JSONLStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("transport store root is required")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	batch, err := jsonlbatch.New(absRoot, []string{requestFilename, responseFilename})
	if err != nil {
		return nil, err
	}
	if err := batch.Recover(context.Background()); err != nil {
		return nil, err
	}
	store := &JSONLStore{
		requestPath:  filepath.Join(absRoot, requestFilename),
		responsePath: filepath.Join(absRoot, responseFilename),
		batch:        batch,
		requests:     make(map[modulecore.RequestID]domaintransport.Request),
		responses:    make(map[modulecore.ResponseID]domaintransport.Response),
	}
	if err := batch.Read(context.Background(), func() error {
		requests, err := store.loadRequests()
		if err != nil {
			return fmt.Errorf("validate transport requests: %w", err)
		}
		responses, err := store.loadResponses()
		if err != nil {
			return fmt.Errorf("validate transport responses: %w", err)
		}
		store.requests = cloneRequests(requests)
		store.responses = cloneResponses(responses)
		return nil
	}); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *JSONLStore) Transaction(ctx context.Context, callback func(domaintransport.Store) error) error {
	if callback == nil {
		return errors.New("transport transaction callback is nil")
	}
	if admission, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		return s.admittedTransaction(ctx, callback, admission)
	}
	for {
		queue, ticket, queued, err := s.queueTransaction(ctx, callback, nil)
		if err != nil {
			return err
		}
		if queued {
			if ticket == 0 {
				return ctx.Err()
			}
			if err := queue.Wait(ctx, uint64(ticket)); err != nil {
				return fmt.Errorf("wait for accepted transport persistence: %w", err)
			}
			return nil
		}
		err = s.withUnqueuedSynchronousAccess(ctx, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			var committed *memoryStore
			var preparationErr error
			err := s.batch.Write(ctx, func() (map[string][]byte, error) {
				requests, err := s.loadRequests()
				if err != nil {
					return nil, err
				}
				responses, err := s.loadResponses()
				if err != nil {
					return nil, err
				}
				tx := &memoryStore{
					requests: requests, responses: responses,
					changedRequests:  map[modulecore.RequestID]domaintransport.Request{},
					changedResponses: map[modulecore.ResponseID]domaintransport.Response{},
				}
				if err := callback(tx); err != nil {
					preparationErr = err
					return nil, err
				}
				payloads, err := tx.payloads()
				if err != nil {
					preparationErr = err
					return nil, err
				}
				committed = tx
				return payloads, nil
			})
			if err == nil && committed != nil {
				s.requests = cloneRequests(committed.requests)
				s.responses = cloneResponses(committed.responses)
			} else if err != nil && preparationErr == nil {
				uncertain := errors.Is(err, jsonlbatch.ErrCommitUncertain) || errors.Is(err, jsonlbatch.ErrRecoveryRequired)
				if uncertain || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
					s.admissionErr = err
				}
			}
			return err
		})
		if errors.Is(err, errQueueConfigured) {
			continue
		}
		return err
	}
}

func (s *JSONLStore) ReadTransaction(ctx context.Context, callback func(domaintransport.Store) error) error {
	if callback == nil {
		return errors.New("transport read callback is nil")
	}
	if admission, admitted := persistencequeue.AdmissionFromContext(ctx); admitted {
		return s.admittedReadTransaction(ctx, callback, admission)
	}
	for {
		queued, err := s.queuedReadTransaction(ctx, callback)
		if err != nil || queued {
			return err
		}
		err = s.withUnqueuedSynchronousAccess(ctx, func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.batch.Read(ctx, func() error {
				requests, err := s.loadRequests()
				if err != nil {
					return err
				}
				responses, err := s.loadResponses()
				if err != nil {
					return err
				}
				s.requests = cloneRequests(requests)
				s.responses = cloneResponses(responses)
				return callback(&memoryStore{requests: requests, responses: responses, readOnly: true})
			})
		})
		if errors.Is(err, errQueueConfigured) {
			continue
		}
		return err
	}
}

func (s *JSONLStore) SaveRequest(ctx context.Context, value domaintransport.Request) error {
	return s.Transaction(ctx, func(tx domaintransport.Store) error { return tx.SaveRequest(ctx, value) })
}

func (s *JSONLStore) GetRequest(ctx context.Context, id modulecore.RequestID) (domaintransport.Request, error) {
	var result domaintransport.Request
	err := s.ReadTransaction(ctx, func(tx domaintransport.Store) error {
		var err error
		result, err = tx.GetRequest(ctx, id)
		return err
	})
	return result, err
}

func (s *JSONLStore) ListRequests(ctx context.Context, filter domaintransport.RequestFilter) ([]domaintransport.Request, error) {
	var result []domaintransport.Request
	err := s.ReadTransaction(ctx, func(tx domaintransport.Store) error {
		var err error
		result, err = tx.ListRequests(ctx, filter)
		return err
	})
	return result, err
}

func (s *JSONLStore) SaveResponse(ctx context.Context, value domaintransport.Response) error {
	return s.Transaction(ctx, func(tx domaintransport.Store) error { return tx.SaveResponse(ctx, value) })
}

func (s *JSONLStore) GetResponse(ctx context.Context, id modulecore.ResponseID) (domaintransport.Response, error) {
	var result domaintransport.Response
	err := s.ReadTransaction(ctx, func(tx domaintransport.Store) error {
		var err error
		result, err = tx.GetResponse(ctx, id)
		return err
	})
	return result, err
}

func (s *JSONLStore) ListResponses(ctx context.Context, filter domaintransport.ResponseFilter) ([]domaintransport.Response, error) {
	var result []domaintransport.Response
	err := s.ReadTransaction(ctx, func(tx domaintransport.Store) error {
		var err error
		result, err = tx.ListResponses(ctx, filter)
		return err
	})
	return result, err
}

func (s *JSONLStore) loadRequests() (map[modulecore.RequestID]domaintransport.Request, error) {
	raw, err := loadRecords[domaintransport.Request](s.requestPath, func(value domaintransport.Request) (string, error) {
		return string(value.RequestID), value.Validate()
	})
	if err != nil {
		return nil, err
	}
	result := make(map[modulecore.RequestID]domaintransport.Request, len(raw))
	for key, value := range raw {
		result[modulecore.RequestID(key)] = value
	}
	return result, nil
}

func (s *JSONLStore) loadResponses() (map[modulecore.ResponseID]domaintransport.Response, error) {
	raw, err := loadRecords[domaintransport.Response](s.responsePath, func(value domaintransport.Response) (string, error) {
		return string(value.ResponseID), value.Validate()
	})
	if err != nil {
		return nil, err
	}
	result := make(map[modulecore.ResponseID]domaintransport.Response, len(raw))
	for key, value := range raw {
		result[modulecore.ResponseID(key)] = value
	}
	return result, nil
}

type memoryStore struct {
	requests         map[modulecore.RequestID]domaintransport.Request
	responses        map[modulecore.ResponseID]domaintransport.Response
	changedRequests  map[modulecore.RequestID]domaintransport.Request
	changedResponses map[modulecore.ResponseID]domaintransport.Response
	readOnly         bool
}

func (s *memoryStore) payloads() (map[string][]byte, error) {
	payloads := map[string][]byte{}
	var err error
	if len(s.changedRequests) > 0 {
		payloads[requestFilename], err = encodeRecords(s.changedRequests)
		if err != nil {
			return nil, err
		}
	}
	if len(s.changedResponses) > 0 {
		payloads[responseFilename], err = encodeRecords(s.changedResponses)
		if err != nil {
			return nil, err
		}
	}
	return payloads, nil
}

func (s *memoryStore) Transaction(ctx context.Context, callback func(domaintransport.Store) error) error {
	return callback(s)
}

func (s *memoryStore) ReadTransaction(ctx context.Context, callback func(domaintransport.Store) error) error {
	return callback(s)
}

func (s *memoryStore) SaveRequest(_ context.Context, value domaintransport.Request) error {
	if s.readOnly {
		return errors.New("transport transaction is read-only")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	s.requests[value.RequestID] = value
	s.changedRequests[value.RequestID] = value
	return nil
}

func (s *memoryStore) GetRequest(_ context.Context, id modulecore.RequestID) (domaintransport.Request, error) {
	value, ok := s.requests[id]
	if !ok {
		return domaintransport.Request{}, domaintransport.ErrNotFound
	}
	return value, nil
}

func (s *memoryStore) ListRequests(_ context.Context, filter domaintransport.RequestFilter) ([]domaintransport.Request, error) {
	result := make([]domaintransport.Request, 0, len(s.requests))
	for _, value := range s.requests {
		if filter.ActionID != "" && value.ActionID != filter.ActionID ||
			filter.AttemptID != "" && value.AttemptID != filter.AttemptID ||
			filter.Status != "" && value.Status != filter.Status {
			continue
		}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].SentAt.Equal(result[j].SentAt) {
			return result[i].RequestID < result[j].RequestID
		}
		return result[i].SentAt.Before(result[j].SentAt)
	})
	return result, nil
}

func (s *memoryStore) SaveResponse(_ context.Context, value domaintransport.Response) error {
	if s.readOnly {
		return errors.New("transport transaction is read-only")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if _, exists := s.responses[value.ResponseID]; exists {
		return fmt.Errorf("response %s already exists", value.ResponseID)
	}
	s.responses[value.ResponseID] = value
	s.changedResponses[value.ResponseID] = value
	return nil
}

func (s *memoryStore) GetResponse(_ context.Context, id modulecore.ResponseID) (domaintransport.Response, error) {
	value, ok := s.responses[id]
	if !ok {
		return domaintransport.Response{}, domaintransport.ErrNotFound
	}
	return value, nil
}

func (s *memoryStore) ListResponses(_ context.Context, filter domaintransport.ResponseFilter) ([]domaintransport.Response, error) {
	result := make([]domaintransport.Response, 0, len(s.responses))
	for _, value := range s.responses {
		if filter.RequestID != "" && value.RequestID != filter.RequestID ||
			filter.ActionID != "" && value.ActionID != filter.ActionID ||
			filter.AttemptID != "" && value.AttemptID != filter.AttemptID {
			continue
		}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ReceivedAt.Equal(result[j].ReceivedAt) {
			return result[i].ResponseID < result[j].ResponseID
		}
		return result[i].ReceivedAt.Before(result[j].ReceivedAt)
	})
	return result, nil
}

func loadRecords[T any](path string, identity func(T) (string, error)) (map[string]T, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := map[string]T{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	line := 0
	for scanner.Scan() {
		line++
		var value T
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		key, err := identity(value)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, line, err)
		}
		if key == "" {
			return nil, fmt.Errorf("%s:%d: empty identity", path, line)
		}
		result[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func encodeRecords[K ~string, T any](records map[K]T) ([]byte, error) {
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, string(key))
	}
	sort.Strings(keys)
	var out bytes.Buffer
	for _, rawKey := range keys {
		value := records[K(rawKey)]
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	return out.Bytes(), nil
}
