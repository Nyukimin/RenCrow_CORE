package task

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrTaskWriterBusy indicates that another process currently owns the task
// store writer lock.
var ErrTaskWriterBusy = errors.New("task store writer is busy")

// acquireTaskWriter obtains the persistent advisory lock for one task-store
// root. The lock file is intentionally retained so its inode never changes
// while another process may still hold an open descriptor.
func acquireTaskWriter(root string) (*os.File, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("task store root is required")
	}
	file, err := os.OpenFile(filepath.Join(root, ".writer.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open task store writer lock: %w", err)
	}
	if err := lockTaskWriter(file); err != nil {
		_ = file.Close()
		if errors.Is(err, ErrTaskWriterBusy) {
			return nil, fmt.Errorf("%w: task store writer lock is already held", ErrTaskWriterBusy)
		}
		return nil, fmt.Errorf("acquire task store writer lock: %w", err)
	}
	return file, nil
}

// releaseTaskWriter releases the advisory lock and closes its descriptor. It
// attempts both operations so a close failure is not hidden by an unlock
// failure, or vice versa.
func releaseTaskWriter(file *os.File) error {
	if file == nil {
		return fmt.Errorf("task store writer lock file is nil")
	}
	unlockErr := unlockTaskWriter(file)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}

// advanceTaskWriterGeneration increments the bounded decimal fencing counter
// stored in the already locked writer file. The caller must hold the OS lock.
func advanceTaskWriterGeneration(file *os.File) (uint64, error) {
	if file == nil {
		return 0, fmt.Errorf("task store writer lock file is nil")
	}
	// Append-only records prevent a torn overwrite from turning e.g. 19 into 1.
	// A partial final record fails closed; no generation is reused after a crash.
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	const tailBytes int64 = 128
	offset := info.Size() - tailBytes
	if offset < 0 {
		offset = 0
	}
	data := make([]byte, info.Size()-offset)
	if len(data) > 0 {
		if _, err := file.ReadAt(data, offset); err != nil {
			return 0, fmt.Errorf("read task writer generation: %w", err)
		}
	}
	var current uint64
	if len(data) > 0 {
		if data[len(data)-1] != '\n' {
			return 0, fmt.Errorf("task writer generation has incomplete final record")
		}
		text := string(data)
		if offset > 0 {
			at := strings.IndexByte(text, '\n')
			if at < 0 {
				return 0, fmt.Errorf("task writer generation record exceeds bound")
			}
			text = text[at+1:]
		}
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		for i, line := range lines {
			if len(line) == 0 || len(line) > 20 {
				return 0, fmt.Errorf("task writer generation is malformed")
			}
			for _, ch := range line {
				if ch < '0' || ch > '9' {
					return 0, fmt.Errorf("task writer generation is malformed")
				}
			}
			value, err := strconv.ParseUint(line, 10, 64)
			if err != nil {
				return 0, fmt.Errorf("task writer generation is invalid: %w", err)
			}
			if i > 0 && (current == ^uint64(0) || value != current+1) {
				return 0, fmt.Errorf("task writer generations are not consecutive")
			}
			current = value
		}
	}
	if current == ^uint64(0) {
		return 0, fmt.Errorf("task store writer generation overflow")
	}
	next := current + 1
	payload := []byte(strconv.FormatUint(next, 10) + "\n")
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return 0, fmt.Errorf("seek task store writer generation: %w", err)
	}
	written, err := file.Write(payload)
	if err != nil {
		return 0, fmt.Errorf("write task store writer generation: %w", err)
	}
	if written != len(payload) {
		return 0, io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return 0, fmt.Errorf("sync task store writer generation: %w", err)
	}
	return next, nil
}
