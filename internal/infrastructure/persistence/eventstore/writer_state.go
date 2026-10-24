package eventstore

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type eventStorePathState struct {
	sequence            eventSeqAllocator
	sequenceWriteMu     sync.Mutex
	sequenceAdmissionMu sync.Mutex
	databasePath        string
	lockFile            *os.File
}

type eventStorePathRegistry struct {
	mu     sync.Mutex
	active map[string]*eventStorePathState
}

var eventStoreWriters = eventStorePathRegistry{active: make(map[string]*eventStorePathState)}
var privateMemoryStoreID atomic.Uint64

type eventSeqAllocator struct {
	last atomic.Int64
}

func (a *eventSeqAllocator) seed(maxSequence int64) error {
	if a == nil {
		return errors.New("event_seq allocator is required")
	}
	if maxSequence < 0 {
		return errors.New("event_seq contains a negative persisted value")
	}
	a.last.Store(maxSequence)
	return nil
}

func (a *eventSeqAllocator) next() (modulecore.EventSeq, error) {
	if a == nil {
		return 0, errors.New("event_seq allocator is required")
	}
	for {
		last := a.last.Load()
		if last >= maxEventSeqValue {
			return 0, errors.New("event_seq exhausted")
		}
		next := last + 1
		if a.last.CompareAndSwap(last, next) {
			return modulecore.EventSeq(next), nil
		}
	}
}

func (a *eventSeqAllocator) reserve(count int) (*eventSequenceRange, int64, error) {
	if a == nil {
		return nil, 0, errors.New("event_seq allocator is required")
	}
	if count < 0 {
		return nil, 0, errors.New("event_seq reservation count must not be negative")
	}
	reserved := int64(count)
	for {
		last := a.last.Load()
		if count == 0 {
			return &eventSequenceRange{next: last + 1, limit: last, previous: last}, last, nil
		}
		if last > maxEventSeqValue-reserved {
			return nil, 0, errors.New("event_seq exhausted")
		}
		limit := last + reserved
		if a.last.CompareAndSwap(last, limit) {
			return &eventSequenceRange{next: last + 1, limit: limit, previous: last}, last, nil
		}
	}
}

// reserveBatch preserves AppendBatch's explicit-sequence behavior while
// reserving its automatically assigned sequences and high watermark before the
// queue ticket becomes visible. The returned floor is the allocator value that
// AppendBatch must use for its legacy ordering validation.
func (a *eventSeqAllocator) reserveBatch(events []modulecore.EventEnvelope) (*eventSequenceRange, int64, error) {
	if a == nil {
		return nil, 0, errors.New("event_seq allocator is required")
	}
	for {
		floor := a.last.Load()
		seen := make(map[modulecore.EventSeq]struct{}, len(events))
		maxReserved := floor
		for _, event := range events {
			if event.EventSeq > 0 {
				seen[event.EventSeq] = struct{}{}
				if int64(event.EventSeq) > maxReserved {
					maxReserved = int64(event.EventSeq)
				}
			}
		}
		planned := make([]modulecore.EventSeq, 0, len(events))
		candidate := floor
		for _, event := range events {
			if event.EventSeq != 0 {
				continue
			}
			for {
				if candidate >= maxEventSeqValue {
					return nil, 0, errors.New("event_seq exhausted")
				}
				candidate++
				if _, reserved := seen[modulecore.EventSeq(candidate)]; !reserved {
					break
				}
			}
			sequence := modulecore.EventSeq(candidate)
			planned = append(planned, sequence)
			seen[sequence] = struct{}{}
		}
		if candidate > maxReserved {
			maxReserved = candidate
		}
		rangeForBatch := &eventSequenceRange{
			next: floor + 1, limit: maxReserved, previous: floor,
			planned: planned, usePlan: true,
		}
		if maxReserved == floor || a.last.CompareAndSwap(floor, maxReserved) {
			return rangeForBatch, floor, nil
		}
	}
}

func (a *eventSeqAllocator) rollback(sequences *eventSequenceRange) {
	if a == nil || sequences == nil {
		return
	}
	// A reservation is rolled back only when queue admission rejects it. The
	// caller holds sequenceAdmissionMu, so no later EventStore queue reservation
	// can have advanced the allocator in between.
	a.last.CompareAndSwap(sequences.limit, sequences.previous)
}

func (a *eventSeqAllocator) advanceTo(sequence int64) {
	if a == nil || sequence <= 0 {
		return
	}
	for {
		last := a.last.Load()
		if last >= sequence || a.last.CompareAndSwap(last, sequence) {
			return
		}
	}
}

func acquireEventStoreWriter(path string) (string, *eventStorePathState, func() error, error) {
	key, target, memoryOnly, err := eventStorePathIdentity(path)
	if err != nil {
		return "", nil, nil, err
	}
	eventStoreWriters.mu.Lock()
	if _, exists := eventStoreWriters.active[key]; exists {
		eventStoreWriters.mu.Unlock()
		return "", nil, nil, errors.New("event store writer is already open for this database path")
	}
	state := &eventStorePathState{databasePath: target}
	eventStoreWriters.active[key] = state
	eventStoreWriters.mu.Unlock()
	if !memoryOnly {
		if err := rejectHardlinkedEventStoreFile(target); err != nil {
			removeEventStoreWriterState(key, state)
			return "", nil, nil, err
		}
		state.lockFile, err = acquireEventStoreFileLock(target + ".writer.lock")
		if err != nil {
			removeEventStoreWriterState(key, state)
			return "", nil, nil, err
		}
	}
	var releaseOnce sync.Once
	var releaseErr error
	release := func() error {
		releaseOnce.Do(func() {
			if state.lockFile != nil {
				releaseErr = releaseEventStoreFileLock(state.lockFile)
				state.lockFile = nil
			}
			removeEventStoreWriterState(key, state)
		})
		return releaseErr
	}
	return key, state, release, nil
}

func eventStorePathLockKey(path string) (string, error) {
	key, _, _, err := eventStorePathIdentity(path)
	return key, err
}

func eventStorePathIdentity(path string) (key, target string, memoryOnly bool, err error) {
	if path == ":memory:" {
		return fmt.Sprintf("sqlite-private-memory:%d", privateMemoryStoreID.Add(1)), "", true, nil
	}
	if !strings.HasPrefix(path, "file:") {
		return filesystemPathIdentity(path)
	}

	parsed, err := url.Parse(path)
	if err != nil {
		return "", "", false, fmt.Errorf("parse event store SQLite URI: %w", err)
	}
	uriPath := parsed.Path
	if parsed.Opaque != "" {
		uriPath, err = url.PathUnescape(parsed.Opaque)
		if err != nil {
			return "", "", false, fmt.Errorf("decode event store SQLite URI path: %w", err)
		}
	}
	if parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost") {
		uriPath = "//" + parsed.Host + uriPath
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", "", false, fmt.Errorf("parse event store SQLite URI query: %w", err)
	}
	mode := strings.ToLower(query.Get("mode"))
	sharedCache := strings.EqualFold(query.Get("cache"), "shared")
	if mode == "memory" || uriPath == ":memory:" {
		if !sharedCache || uriPath == "" {
			return fmt.Sprintf("sqlite-private-memory:%d", privateMemoryStoreID.Add(1)), "", true, nil
		}
		return "sqlite-shared-memory:" + filepath.Clean(uriPath), "", true, nil
	}
	if uriPath == "" {
		return fmt.Sprintf("sqlite-private-memory:%d", privateMemoryStoreID.Add(1)), "", true, nil
	}
	if runtime.GOOS == "windows" && len(uriPath) >= 3 && uriPath[0] == '/' && uriPath[2] == ':' {
		uriPath = uriPath[1:]
	}
	return filesystemPathIdentity(uriPath)
}

func filesystemPathIdentity(path string) (string, string, bool, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", "", false, fmt.Errorf("resolve event store path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	} else {
		if info, statErr := os.Lstat(absolute); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", "", false, fmt.Errorf("resolve event store database symlink: %w", err)
		}
		if resolvedParent, parentErr := filepath.EvalSymlinks(filepath.Dir(absolute)); parentErr == nil {
			absolute = filepath.Join(resolvedParent, filepath.Base(absolute))
		}
	}
	absolute = filepath.Clean(absolute)
	target := absolute
	if runtime.GOOS == "windows" {
		keyPath := strings.ToLower(absolute)
		return "sqlite-file:" + keyPath, target, false, nil
	}
	return "sqlite-file:" + absolute, target, false, nil
}

func ensureEventStorePath(path string) error {
	if isSQLiteURI(path) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return nil
}

func rejectHardlinkedEventStoreFile(path string) error {
	hardlinked, err := eventStoreFileHasMultipleLinks(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("inspect event store database path: %w", err)
	}
	if hardlinked {
		return errors.New("event store database path has hard-link aliases; refusing ambiguous writer lock")
	}
	return nil
}

func removeEventStoreWriterState(key string, state *eventStorePathState) {
	eventStoreWriters.mu.Lock()
	if eventStoreWriters.active[key] == state {
		delete(eventStoreWriters.active, key)
	}
	eventStoreWriters.mu.Unlock()
}
