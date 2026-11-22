package persistencequeue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

const (
	OwnerSession                    = "session"
	OwnerTask                       = "task"
	OwnerEvent                      = "event"
	OwnerConversation               = "conversation"
	OwnerPersona                    = "persona"
	OwnerSuperAgent                 = "superagent"
	OwnerAction                     = "action"
	OwnerTransport                  = "transport"
	OwnerAIWorkflow                 = "aiworkflow"
	KindTransportReceiptTransaction = "transport.receipt_transaction"
	// MaxJSONLBatchCommands caps one typed JSONL commit group.
	MaxJSONLBatchCommands = 64
	// MaxJSONLBatchBytes caps the canonical payload bytes in one commit group.
	MaxJSONLBatchBytes = int64(1 << 20)
)

var (
	ErrNilPersistenceQueue       = errors.New("persistence queue must not be nil")
	ErrInvalidAdmission          = errors.New("persistence admission is invalid")
	ErrAdmissionReleased         = errors.New("persistence admission has been released")
	ErrInvalidPersistenceCommand = errors.New("persistence command metadata or callback is invalid")
)

// PersistenceCommand is an immutable command accepted for persistence.
// Owner and Kind are stable identifiers. SizeBytes is the canonical byte
// accounting charged against the request's reservation. Apply and JSONLBatch
// must not mutate the command or data reachable from them after Enqueue accepts it.
type PersistenceCommand struct {
	Owner      string
	Kind       string
	SizeBytes  int64
	Apply      func(context.Context) error
	JSONLBatch *JSONLWriteBatch
}

// JSONLBatchWriter commits several immutable JSONL payload maps in one owner
// transaction. Implementations must preserve the order of payload maps.
type JSONLBatchWriter interface {
	WriteJSONLBatch(context.Context, []map[string][]byte) error
}

// JSONLBatchToken identifies one concrete JSONL owner and binds the typed batch
// capability to that owner's commit operation. Its non-zero-sized pointer is
// used only for equality while selecting a contiguous queue prefix.
type JSONLBatchToken struct {
	writer JSONLBatchWriter
	marker byte
}

// NewJSONLBatchToken creates an owner identity for typed JSONL batching.
func NewJSONLBatchToken(writer JSONLBatchWriter) *JSONLBatchToken {
	if writer == nil {
		return nil
	}
	return &JSONLBatchToken{writer: writer, marker: 1}
}

func (token *JSONLBatchToken) write(ctx context.Context, payloads []map[string][]byte) error {
	if token == nil || token.marker == 0 || token.writer == nil {
		return ErrInvalidPersistenceCommand
	}
	return token.writer.WriteJSONLBatch(ctx, payloads)
}

// JSONLWriteBatch carries schema-specific bytes without exposing arbitrary
// Apply closures to the batching path.
type JSONLWriteBatch struct {
	Token    *JSONLBatchToken
	Payloads map[string][]byte
}

// Ticket identifies one accepted persistence command in the shared queue's
// FIFO sequence.
type Ticket uint64

// NewPersistenceQueue creates the shared single-writer queue for persistence
// commands. The callback receives a worker-owned lifecycle context. Until the
// runtime injects a longer-lived context, this is context.Background(), so a
// request's cancellation never cancels work accepted by the queue.
func NewPersistenceQueue(capacity int, byteCapacity int64) (*Queue[PersistenceCommand], error) {
	workerContext := context.Background()
	writer := func(queued Command[PersistenceCommand]) error {
		command := queued.Value
		if err := command.validate(); err != nil {
			return err
		}
		if err := command.Apply(workerContext); err != nil {
			return fmt.Errorf("apply persistence command %s/%s: %w", command.Owner, command.Kind, err)
		}
		return nil
	}
	batchKey := func(command PersistenceCommand) (*JSONLBatchToken, bool) {
		if command.validate() != nil || command.Owner != OwnerTransport ||
			command.Kind != KindTransportReceiptTransaction || command.JSONLBatch == nil {
			return nil, false
		}
		return command.JSONLBatch.Token, true
	}
	batchWriter := func(commands []Command[PersistenceCommand]) error {
		if len(commands) < 2 || len(commands) > MaxJSONLBatchCommands {
			return ErrInvalidPersistenceCommand
		}
		first := commands[0].Value.JSONLBatch
		if first == nil || first.Token == nil {
			return ErrInvalidPersistenceCommand
		}
		payloads := make([]map[string][]byte, len(commands))
		var totalBytes int64
		for index, queued := range commands {
			if err := queued.Value.validate(); err != nil {
				return err
			}
			batch := queued.Value.JSONLBatch
			if batch == nil || batch.Token != first.Token {
				return ErrInvalidPersistenceCommand
			}
			if queued.Value.SizeBytes > MaxJSONLBatchBytes-totalBytes {
				return ErrInvalidPersistenceCommand
			}
			totalBytes += queued.Value.SizeBytes
			payloads[index] = batch.Payloads
		}
		return first.Token.write(workerContext, payloads)
	}
	return newQueue(capacity, byteCapacity, writer, batchKey, batchWriter)
}

// Admit reserves a request's command count and byte budget before the owner
// performs writes or starts external work. A successful reservation creates
// the explicit capability that owner adapters use for later Enqueue calls.
func Admit(queue *Queue[PersistenceCommand], commandCount int, byteBudget int64) (*Admission, error) {
	if queue == nil {
		return nil, ErrNilPersistenceQueue
	}
	reservation, err := queue.Reserve(commandCount, byteBudget)
	if err != nil {
		return nil, err
	}
	return &Admission{
		queue:       queue,
		reservation: reservation,
		remaining:   commandCount,
	}, nil
}

// Admission is a request-scoped capability backed by one queue reservation.
// Its zero value is invalid; use Admit to create one.
type Admission struct {
	mu          sync.Mutex
	queue       *Queue[PersistenceCommand]
	reservation *Reservation[PersistenceCommand]
	remaining   int
	lastTicket  Ticket
	released    bool
}

// MatchesQueue reports whether this admission was created from the supplied
// shared queue. Queue identity is immutable for the lifetime of an admission.
func (a *Admission) MatchesQueue(queue *Queue[PersistenceCommand]) bool {
	return a != nil && queue != nil && a.queue == queue
}

// Enqueue accepts an immutable command using this request's reserved count and
// byte budget. It returns the queue-local ticket used by Barrier.
func (a *Admission) Enqueue(command PersistenceCommand) (Ticket, error) {
	if a == nil {
		return 0, ErrInvalidAdmission
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.released {
		return 0, ErrAdmissionReleased
	}
	if err := command.validate(); err != nil {
		return 0, err
	}
	sequence, err := a.queue.EnqueueReserved(a.reservation, command, command.SizeBytes)
	if err != nil {
		return 0, err
	}
	a.remaining--
	a.lastTicket = Ticket(sequence)
	return a.lastTicket, nil
}

// Barrier waits for the last command accepted by this admission. Queue FIFO
// ordering makes this a barrier for all earlier commands as well. Release does
// not clear the last accepted ticket, so a barrier remains valid after release.
func (a *Admission) Barrier(ctx context.Context) error {
	if a == nil {
		return ErrInvalidAdmission
	}
	if ctx == nil {
		return ErrNilContext
	}
	a.mu.Lock()
	ticket := a.lastTicket
	a.mu.Unlock()
	if ticket == 0 {
		return nil
	}
	return a.queue.Wait(ctx, uint64(ticket))
}

// Release makes this admission unusable for future commands and returns any
// unused reserved count and byte budget to the shared queue. It is idempotent.
func (a *Admission) Release() error {
	if a == nil {
		return ErrInvalidAdmission
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.released {
		return nil
	}
	a.released = true
	if a.remaining == 0 {
		return nil
	}
	if err := a.queue.CancelReservation(a.reservation); err != nil {
		return err
	}
	a.remaining = 0
	return nil
}

type admissionContextKey struct{}

// WithAdmission attaches an explicit Admission capability to ctx. A boolean
// or other context value does not grant admission. Nil ctx is returned as nil;
// AdmissionFromContext will report no capability for it.
func WithAdmission(ctx context.Context, admission *Admission) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, admissionContextKey{}, admission)
}

// AdmissionFromContext returns the explicit Admission capability in ctx.
func AdmissionFromContext(ctx context.Context) (*Admission, bool) {
	if ctx == nil {
		return nil, false
	}
	admission, ok := ctx.Value(admissionContextKey{}).(*Admission)
	return admission, ok && admission != nil
}

func (command PersistenceCommand) validate() error {
	if command.SizeBytes < 1 {
		return ErrInvalidCommandSize
	}
	if !validPersistenceOwner(command.Owner) || strings.TrimSpace(command.Kind) == "" || command.Apply == nil {
		return ErrInvalidPersistenceCommand
	}
	if command.JSONLBatch != nil {
		if command.Owner != OwnerTransport || command.Kind != KindTransportReceiptTransaction ||
			command.JSONLBatch.Token == nil || command.JSONLBatch.Token.marker == 0 || command.JSONLBatch.Token.writer == nil {
			return ErrInvalidPersistenceCommand
		}
		var payloadBytes int64
		for _, payload := range command.JSONLBatch.Payloads {
			if len(payload) == 0 || int64(len(payload)) > command.SizeBytes-payloadBytes {
				return ErrInvalidPersistenceCommand
			}
			payloadBytes += int64(len(payload))
		}
		if payloadBytes != command.SizeBytes {
			return ErrInvalidPersistenceCommand
		}
	}
	return nil
}

func validPersistenceOwner(owner string) bool {
	switch owner {
	case OwnerSession, OwnerTask, OwnerEvent, OwnerConversation, OwnerPersona, OwnerSuperAgent, OwnerAction, OwnerTransport, OwnerAIWorkflow:
		return true
	default:
		return false
	}
}
