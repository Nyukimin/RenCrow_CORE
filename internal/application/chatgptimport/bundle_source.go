package chatgptimport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
)

type chunkReference struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type sourceIndexRecord struct {
	Path   string           `json:"path"`
	Bytes  int64            `json:"bytes"`
	SHA256 string           `json:"sha256"`
	Chunks []chunkReference `json:"chunks"`
}

func decodeSourceIndexRecord(data []byte, chunkBytes int64) (sourceIndexRecord, error) {
	if err := validateJSONObject(data, []string{"path", "bytes", "sha256", "chunks"}, []string{"path", "bytes", "sha256", "chunks"}); err != nil {
		return sourceIndexRecord{}, err
	}
	var raw struct {
		Path   string            `json:"path"`
		Bytes  int64             `json:"bytes"`
		SHA256 string            `json:"sha256"`
		Chunks []json.RawMessage `json:"chunks"`
	}
	if err := decodeStrictJSON(data, &raw); err != nil {
		return sourceIndexRecord{}, err
	}
	value := sourceIndexRecord{Path: raw.Path, Bytes: raw.Bytes, SHA256: raw.SHA256}
	for _, encodedChunk := range raw.Chunks {
		if err := validateJSONObject(encodedChunk, []string{"sha256", "bytes"}, []string{"sha256", "bytes"}); err != nil {
			return sourceIndexRecord{}, err
		}
		var chunk chunkReference
		if err := decodeStrictJSON(encodedChunk, &chunk); err != nil {
			return sourceIndexRecord{}, err
		}
		value.Chunks = append(value.Chunks, chunk)
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, data) {
		return sourceIndexRecord{}, errors.New("source index record is not canonical JSON")
	}
	if err := validateSourcePath(value.Path); err != nil {
		return sourceIndexRecord{}, fmt.Errorf("source path is invalid: %w", err)
	}
	if value.Bytes < 0 || !isLowerHex64(value.SHA256) {
		return sourceIndexRecord{}, errors.New("source index metadata is invalid")
	}
	var total int64
	for _, chunk := range value.Chunks {
		if chunk.Bytes <= 0 || chunk.Bytes > chunkBytes || !isLowerHex64(chunk.SHA256) || total > value.Bytes-chunk.Bytes {
			return sourceIndexRecord{}, errors.New("source index chunk metadata is invalid")
		}
		total += chunk.Bytes
	}
	if total != value.Bytes {
		return sourceIndexRecord{}, errors.New("source index chunks do not reconstruct file size")
	}
	return value, nil
}

func validateSourceIndex(manifest bundleManifest, sources []sourceIndexRecord, objects map[string]objectMetadata) error {
	if len(sources) != manifest.SourceFileCount || len(sources) != len(manifest.Files) {
		return errors.New("source index file count does not match manifest")
	}
	expectedObjects := make(map[string]int64)
	chunkCount := 0
	for index, source := range sources {
		file := manifest.Files[index]
		if source.Path != file.Path || source.Bytes != file.Bytes || source.SHA256 != file.SHA256 {
			return errors.New("source index does not match manifest source metadata")
		}
		chunkCount += len(source.Chunks)
		for _, chunk := range source.Chunks {
			if size, exists := expectedObjects[chunk.SHA256]; exists && size != chunk.Bytes {
				return errors.New("one object hash has inconsistent source chunk sizes")
			}
			expectedObjects[chunk.SHA256] = chunk.Bytes
		}
	}
	if chunkCount != manifest.SourceChunkCount || len(expectedObjects) != manifest.SourceObjectCount {
		return errors.New("source chunk or object count does not match manifest")
	}
	if len(objects) != len(expectedObjects) {
		return errors.New("TAR object set does not match source index")
	}
	for hash, expectedSize := range expectedObjects {
		actual, exists := objects[hash]
		if !exists || actual.bytes != expectedSize {
			return errors.New("source object is missing or has the wrong size")
		}
	}
	return nil
}

func verifySourceReconstruction(ctx context.Context, sources []sourceIndexRecord, objects map[string]objectMetadata) error {
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		hash := sha256.New()
		var total int64
		for _, chunk := range source.Chunks {
			metadata, exists := objects[chunk.SHA256]
			if !exists || metadata.bytes != chunk.Bytes {
				return fmt.Errorf("%w: source chunk is not available", ErrInvalidBundle)
			}
			file, info, err := openPrivateRegular(metadata.path)
			if err != nil {
				return err
			}
			if info.Size() != chunk.Bytes {
				_ = file.Close()
				return fmt.Errorf("%w: source chunk size changed", ErrInvalidBundle)
			}
			count, copyErr := io.Copy(hash, &contextReader{ctx: ctx, reader: file})
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if count != chunk.Bytes {
				return fmt.Errorf("%w: source chunk ended early", ErrInvalidBundle)
			}
			total += count
		}
		if total != source.Bytes || hex.EncodeToString(hash.Sum(nil)) != source.SHA256 {
			return fmt.Errorf("%w: source reconstruction hash or size mismatch", ErrInvalidBundle)
		}
	}
	return nil
}

type virtualSourceReader struct {
	ctx       context.Context
	chunks    []chunkReference
	objects   map[string]objectMetadata
	index     int
	file      *os.File
	remaining int64
}

func (r *virtualSourceReader) Read(data []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if r.index >= len(r.chunks) {
			if err := r.Close(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		if r.file == nil {
			chunk := r.chunks[r.index]
			metadata, exists := r.objects[chunk.SHA256]
			if !exists || metadata.bytes != chunk.Bytes {
				return 0, errors.New("virtual source chunk is unavailable")
			}
			file, info, err := openPrivateRegular(metadata.path)
			if err != nil {
				return 0, err
			}
			if info.Size() != chunk.Bytes {
				_ = file.Close()
				return 0, errors.New("virtual source chunk size changed")
			}
			r.file = file
			r.remaining = chunk.Bytes
		}
		if r.remaining == 0 {
			if err := r.closeCurrent(); err != nil {
				return 0, err
			}
			r.index++
			continue
		}
		if int64(len(data)) > r.remaining {
			data = data[:r.remaining]
		}
		count, err := r.file.Read(data)
		r.remaining -= int64(count)
		if err != nil && !(errors.Is(err, io.EOF) && r.remaining == 0) {
			return count, err
		}
		if r.remaining == 0 {
			if closeErr := r.closeCurrent(); closeErr != nil {
				return count, closeErr
			}
			r.index++
		}
		if count == 0 && err == nil {
			continue
		}
		return count, nil
	}
}

func (r *virtualSourceReader) closeCurrent() error {
	if r.file == nil {
		return nil
	}
	err := r.file.Close()
	r.file = nil
	return err
}

func (r *virtualSourceReader) Close() error { return r.closeCurrent() }

func compareDerivedRecords(ctx context.Context, manifest bundleManifest, sources []sourceIndexRecord, objects map[string]objectMetadata, recordsPath string, maxLineBytes int64) (recordCounts, error) {
	records, _, err := openPrivateRegular(recordsPath)
	if err != nil {
		return recordCounts{}, err
	}
	defer records.Close()
	reader := bufio.NewReaderSize(records, 64*1024)
	counts := recordCounts{Conversations: make(map[string]struct{})}
	evidenceIDs := make(map[string]struct{})
	for _, source := range sources {
		if !conversationFilePattern.MatchString(path.Base(source.Path)) {
			continue
		}
		virtual := &virtualSourceReader{ctx: ctx, chunks: source.Chunks, objects: objects}
		parseErr := parseConversationArray(ctx, virtual, func(conversation exportConversation) error {
			counts.Conversations[conversation.ID] = struct{}{}
			return deriveConversationRecords(manifest.ExportID, conversation, func(record artifactRecord) error {
				if _, exists := evidenceIDs[record.EvidenceID]; exists {
					return errors.New("source-derived records contain a duplicate evidence ID")
				}
				evidenceIDs[record.EvidenceID] = struct{}{}
				expected, err := json.Marshal(record)
				if err != nil {
					return err
				}
				actual, readErr := readCanonicalJSONLine(reader, maxLineBytes)
				if readErr != nil {
					return readErr
				}
				if !bytes.Equal(actual, expected) {
					return errors.New("records JSONL differs from source-derived records")
				}
				counts.Records++
				switch record.Role {
				case "user":
					counts.UserMessages++
				case "assistant":
					counts.AssistantMessages++
				}
				return nil
			})
		})
		closeErr := virtual.Close()
		if parseErr != nil {
			return recordCounts{}, parseErr
		}
		if closeErr != nil {
			return recordCounts{}, closeErr
		}
	}
	if trailing, err := readCanonicalJSONLine(reader, maxLineBytes); !errors.Is(err, io.EOF) {
		if err == nil || len(trailing) != 0 {
			return recordCounts{}, errors.New("records JSONL contains records not derived from source")
		}
		return recordCounts{}, err
	}
	return counts, nil
}

func readCanonicalJSONLine(reader *bufio.Reader, maxBytes int64) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if int64(len(line))+int64(len(fragment)) > maxBytes+1 {
			return nil, fmt.Errorf("%w: JSONL line exceeds configured bound", ErrBounds)
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if len(line) == 0 {
				return nil, io.EOF
			}
			return nil, errors.New("JSONL contains a partial final line")
		}
		if err != nil {
			return nil, err
		}
		if len(line) == 1 {
			return nil, errors.New("JSONL contains an empty line")
		}
		return line[:len(line)-1], nil
	}
}
