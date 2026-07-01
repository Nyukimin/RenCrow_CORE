package jsonlutil

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	initialTailWindowBytes = int64(1 << 20)
	maxTailWindowBytes     = int64(16 << 20)
)

type BoundOptions struct {
	MaxRecords int
	MaxBytes   int64
}

func Append(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	line, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	return err
}

func AppendBounded(path string, value any, opts BoundOptions) error {
	if err := Append(path, value); err != nil {
		return err
	}
	if opts.MaxRecords <= 0 && opts.MaxBytes <= 0 {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if opts.MaxBytes > 0 && info.Size() <= opts.MaxBytes {
		return nil
	}
	return CompactLatestRecords(path, opts.MaxRecords)
}

func ListLatest[T any](path string, limit int) ([]T, error) {
	if limit <= 0 {
		limit = 50
	}
	lines, err := TailLines(path, limit)
	if err != nil {
		return nil, err
	}
	items := make([]T, 0, len(lines))
	for i := 0; i < len(lines); i++ {
		var item T
		if err := json.Unmarshal(lines[i], &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func Read(path string, fn func([]byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if err := fn(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func CompactLatestRecords(path string, maxRecords int) error {
	if maxRecords <= 0 {
		return nil
	}
	lines, err := ReadAllLines(path)
	if err != nil {
		return err
	}
	if len(lines) <= maxRecords {
		return nil
	}
	dropped := lines[:len(lines)-maxRecords]
	kept := lines[len(lines)-maxRecords:]
	if err := archiveDroppedRecords(path, dropped); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	tmp, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	for _, line := range kept {
		if _, err := tmp.Write(append(line, '\n')); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func ReadAllLines(path string) ([][]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return [][]byte{}, nil
		}
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lines := make([][]byte, 0)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func archiveDroppedRecords(path string, lines [][]byte) error {
	if len(lines) == 0 {
		return nil
	}
	archivePath := compactArchivePath(path, time.Now().UTC())
	if err := os.MkdirAll(filepath.Dir(archivePath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(archivePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	for _, line := range lines {
		if _, err := zw.Write(append(line, '\n')); err != nil {
			_ = zw.Close()
			_ = f.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func compactArchivePath(path string, now time.Time) string {
	dir := filepath.Dir(path)
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	stamp := now.UTC().Format("20060102T150405.000000000Z")
	return filepath.Join(dir, "archive", fmt.Sprintf("%s.compacted.%s.jsonl.gz", base, stamp))
}

func TailLines(path string, limit int) ([][]byte, error) {
	if limit <= 0 {
		return [][]byte{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return [][]byte{}, nil
		}
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() == 0 {
		return [][]byte{}, nil
	}
	window := initialTailWindowBytes
	if window > info.Size() {
		window = info.Size()
	}
	for {
		lines, complete, err := readTailWindow(f, info.Size(), window, limit)
		if err != nil {
			return nil, err
		}
		if len(lines) >= limit || complete || window >= info.Size() || window >= maxTailWindowBytes {
			return lines, nil
		}
		window *= 2
		if window > info.Size() {
			window = info.Size()
		}
	}
}

func readTailWindow(f *os.File, fileSize, window int64, limit int) ([][]byte, bool, error) {
	offset := fileSize - window
	buf := make([]byte, window)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false, err
	}
	buf = buf[:n]
	if len(buf) == 0 {
		return [][]byte{}, offset == 0, nil
	}
	if offset > 0 {
		if idx := firstNewline(buf); idx >= 0 {
			buf = buf[idx+1:]
		} else {
			return [][]byte{}, false, nil
		}
	}
	lines := make([][]byte, 0, limit)
	end := len(buf)
	for end > 0 && (buf[end-1] == '\n' || buf[end-1] == '\r') {
		end--
	}
	for end > 0 && len(lines) < limit {
		start := lastNewlineBefore(buf, end)
		lineStart := 0
		if start >= 0 {
			lineStart = start + 1
		}
		line := append([]byte(nil), buf[lineStart:end]...)
		if len(line) > 0 {
			lines = append(lines, line)
		}
		if start < 0 {
			break
		}
		end = start
		for end > 0 && (buf[end-1] == '\n' || buf[end-1] == '\r') {
			end--
		}
	}
	return lines, offset == 0, nil
}

func firstNewline(buf []byte) int {
	for i, b := range buf {
		if b == '\n' {
			return i
		}
	}
	return -1
}

func lastNewlineBefore(buf []byte, end int) int {
	if end > len(buf) {
		panic(fmt.Sprintf("end %d exceeds buffer length %d", end, len(buf)))
	}
	for i := end - 1; i >= 0; i-- {
		if buf[i] == '\n' {
			return i
		}
	}
	return -1
}
