package jsonlutil

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestWriteCompactedLinesBuffersManyRecordsWithoutChangingBytes(t *testing.T) {
	const recordCount = 10000
	lines := make([][]byte, recordCount)
	var want bytes.Buffer
	for i := range lines {
		lines[i] = []byte(`{"id":123456789}`)
		want.Write(lines[i])
		want.WriteByte('\n')
	}

	dst := &countingWriter{}
	if err := writeCompactedLines(dst, lines); err != nil {
		t.Fatalf("writeCompactedLines() error = %v", err)
	}
	if !bytes.Equal(dst.Bytes(), want.Bytes()) {
		t.Fatal("written bytes differ from input records with newline separators")
	}
	t.Logf("records=%d underlying_write_calls=%d bytes=%d", recordCount, dst.WriteCalls(), dst.Len())
	if dst.WriteCalls() >= recordCount/10 {
		t.Fatalf("underlying writes = %d, want fewer than %d for %d records", dst.WriteCalls(), recordCount/10, recordCount)
	}
}

func TestWriteCompactedLinesHandlesLargeRecordAndEmptyInput(t *testing.T) {
	t.Run("large record", func(t *testing.T) {
		line := bytes.Repeat([]byte{'x'}, 64*1024)
		dst := &countingWriter{}
		if err := writeCompactedLines(dst, [][]byte{line}); err != nil {
			t.Fatalf("writeCompactedLines() error = %v", err)
		}
		want := append(bytes.Clone(line), '\n')
		if !bytes.Equal(dst.Bytes(), want) {
			t.Fatal("large record bytes changed")
		}
	})

	t.Run("empty", func(t *testing.T) {
		dst := &countingWriter{}
		if err := writeCompactedLines(dst, nil); err != nil {
			t.Fatalf("writeCompactedLines() error = %v", err)
		}
		if dst.Len() != 0 || dst.WriteCalls() != 0 {
			t.Fatalf("empty input wrote %d bytes in %d calls", dst.Len(), dst.WriteCalls())
		}
	})
}

func TestWriteCompactedLinesReturnsFlushAndShortWriteErrors(t *testing.T) {
	sentinel := errors.New("injected write failure")
	t.Run("write error", func(t *testing.T) {
		dst := &failingWriter{err: sentinel}
		if err := writeCompactedLines(dst, [][]byte{[]byte("record")}); !errors.Is(err, sentinel) {
			t.Fatalf("writeCompactedLines() error = %v, want %v", err, sentinel)
		}
	})

	t.Run("short write", func(t *testing.T) {
		dst := &shortWriter{}
		if err := writeCompactedLines(dst, [][]byte{[]byte("record")}); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("writeCompactedLines() error = %v, want %v", err, io.ErrShortWrite)
		}
	})
}

type countingWriter struct {
	bytes.Buffer
	writes int
}

func (w *countingWriter) Write(data []byte) (int, error) {
	w.writes++
	return w.Buffer.Write(data)
}

func (w *countingWriter) WriteCalls() int {
	return w.writes
}

type failingWriter struct {
	err error
}

func (w *failingWriter) Write([]byte) (int, error) {
	return 0, w.err
}

type shortWriter struct{}

func (*shortWriter) Write(data []byte) (int, error) {
	return len(data) - 1, nil
}
