package chatgptimport

import (
	"archive/tar"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// VerifyBundle validates and extracts an already-private manifest and
// uncompressed bundle into a verifier-owned directory below stageRoot. It does
// not call a network service, an LLM, or a data store.
func VerifyBundle(ctx context.Context, stageRoot, manifestPath, artifactPath string, options Options) (_ *VerifiedBundle, resultErr error) {
	if ctx == nil {
		return nil, errors.New("verification context is required")
	}
	options, err := options.normalized()
	if err != nil {
		return nil, err
	}
	root, err := resolvePrivateStageRoot(stageRoot)
	if err != nil {
		return nil, err
	}
	if same, err := sameInputFile(manifestPath, artifactPath); err != nil {
		return nil, err
	} else if same {
		return nil, errors.New("manifest and artifact must be distinct files")
	}
	manifest, err := readManifest(ctx, manifestPath, options)
	if err != nil {
		return nil, err
	}
	if manifest.ArtifactBytes > options.MaxArtifactBytes {
		return nil, fmt.Errorf("%w: artifact is larger than %d bytes", ErrBounds, options.MaxArtifactBytes)
	}
	stage, err := os.MkdirTemp(root, ".chatgpt-bundle-verify-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(stage, 0o700); err != nil {
		_ = removeVerifierStage(root, stage)
		return nil, err
	}
	succeeded := false
	defer func() {
		if succeeded {
			return
		}
		if cleanupErr := removeVerifierStage(root, stage); cleanupErr != nil {
			resultErr = errors.Join(resultErr, cleanupErr)
		}
	}()

	extracted, err := verifyArtifact(ctx, artifactPath, stage, manifest, options)
	if err != nil {
		return nil, err
	}
	if err := validateSourceIndex(manifest, extracted.sources, extracted.objects); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBundle, err)
	}
	if err := verifySourceReconstruction(ctx, extracted.sources, extracted.objects); err != nil {
		return nil, err
	}
	derived, err := compareDerivedRecords(ctx, manifest, extracted.sources, extracted.objects, extracted.recordsPath, options.MaxJSONLineBytes)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrBounds) || errors.Is(err, errChatGPTImportSourceChanged) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: source-derived records: %w", ErrInvalidBundle, err)
	}
	if derived.Records != extracted.recordCounts.Records || derived.UserMessages != extracted.recordCounts.UserMessages || derived.AssistantMessages != extracted.recordCounts.AssistantMessages || len(derived.Conversations) != len(extracted.recordCounts.Conversations) {
		return nil, fmt.Errorf("%w: source-derived record counts differ from artifact records", ErrInvalidBundle)
	}
	bundle := &VerifiedBundle{
		binding: manifest.binding(), root: root, stage: stage,
		records: extracted.recordsPath, sourceIndex: extracted.indexPath, objects: extracted.objects,
	}
	succeeded = true
	return bundle, nil
}

func sameInputFile(left, right string) (bool, error) {
	leftInfo, err := os.Lstat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Lstat(right)
	if err != nil {
		return false, err
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

type extractedBundle struct {
	recordsPath  string
	indexPath    string
	objects      map[string]objectMetadata
	sources      []sourceIndexRecord
	recordCounts recordCounts
}

type countedHashReader struct {
	ctx    context.Context
	reader io.Reader
	count  int64
	hash   hash.Hash
}

func (r *countedHashReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	count, err := r.reader.Read(data)
	if count > 0 {
		r.count += int64(count)
		_, _ = r.hash.Write(data[:count])
	}
	return count, err
}

func verifyArtifact(ctx context.Context, artifactPath, stage string, manifest bundleManifest, options Options) (extractedBundle, error) {
	artifact, initialInfo, err := openPrivateRegular(artifactPath)
	if err != nil {
		return extractedBundle{}, err
	}
	defer artifact.Close()
	if initialInfo.Size() > options.MaxArtifactBytes {
		return extractedBundle{}, fmt.Errorf("%w: artifact is larger than %d bytes", ErrBounds, options.MaxArtifactBytes)
	}
	if initialInfo.Size() != manifest.ArtifactBytes {
		return extractedBundle{}, fmt.Errorf("%w: artifact size does not match manifest", ErrInvalidBundle)
	}
	objectsRoot := filepath.Join(stage, "objects", "sha256")
	if err := os.MkdirAll(objectsRoot, 0o700); err != nil {
		return extractedBundle{}, err
	}
	if err := os.Chmod(filepath.Join(stage, "objects"), 0o700); err != nil {
		return extractedBundle{}, err
	}
	if err := os.Chmod(objectsRoot, 0o700); err != nil {
		return extractedBundle{}, err
	}
	counted := &countedHashReader{ctx: ctx, reader: artifact, hash: sha256.New()}
	reader := tar.NewReader(counted)
	result := extractedBundle{objects: make(map[string]objectMetadata)}
	lastName := ""
	seenRecords, seenIndex := false, false
	expectedArtifactBytes := int64(1024)
	for {
		if err := ctx.Err(); err != nil {
			return extractedBundle{}, err
		}
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			if errors.Is(nextErr, context.Canceled) || errors.Is(nextErr, context.DeadlineExceeded) {
				return extractedBundle{}, nextErr
			}
			return extractedBundle{}, fmt.Errorf("%w: %v", ErrInvalidBundle, nextErr)
		}
		if err := validateTarHeader(header); err != nil {
			return extractedBundle{}, fmt.Errorf("%w: %v", ErrInvalidBundle, err)
		}
		if header.Name <= lastName {
			return extractedBundle{}, fmt.Errorf("%w: TAR members are not strictly sorted", ErrInvalidBundle)
		}
		lastName = header.Name
		memberBytes, overflow := canonicalTarMemberBytes(header.Size)
		if overflow || expectedArtifactBytes > manifest.ArtifactBytes-memberBytes {
			return extractedBundle{}, fmt.Errorf("%w: TAR member sizes exceed artifact size", ErrInvalidBundle)
		}
		expectedArtifactBytes += memberBytes
		switch {
		case strings.HasPrefix(header.Name, "objects/sha256/"):
			if seenRecords || seenIndex {
				return extractedBundle{}, fmt.Errorf("%w: TAR object member is misplaced", ErrInvalidBundle)
			}
			hashValue, err := objectHashFromName(header.Name)
			if err != nil || header.Size <= 0 {
				return extractedBundle{}, fmt.Errorf("%w: TAR object name or size is invalid", ErrInvalidBundle)
			}
			if header.Size > options.ChunkBytes {
				return extractedBundle{}, fmt.Errorf("%w: TAR object exceeds configured chunk bound", ErrBounds)
			}
			if _, exists := result.objects[hashValue]; exists {
				return extractedBundle{}, fmt.Errorf("%w: TAR contains a duplicate object", ErrInvalidBundle)
			}
			directory := filepath.Join(objectsRoot, hashValue[:2])
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return extractedBundle{}, err
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				return extractedBundle{}, err
			}
			target := filepath.Join(directory, hashValue)
			if err := copyVerifiedMember(ctx, reader, target, header.Size, hashValue); err != nil {
				return extractedBundle{}, err
			}
			result.objects[hashValue] = objectMetadata{path: target, bytes: header.Size}
		case header.Name == "records.jsonl":
			if seenRecords || seenIndex {
				return extractedBundle{}, fmt.Errorf("%w: records.jsonl is misplaced or duplicated", ErrInvalidBundle)
			}
			seenRecords = true
			result.recordsPath = filepath.Join(stage, "records.jsonl")
			counts, err := extractAndValidateRecords(ctx, reader, header.Size, result.recordsPath, manifest.ExportID, options.MaxJSONLineBytes)
			if err != nil {
				return extractedBundle{}, err
			}
			result.recordCounts = counts
		case header.Name == "source-files.jsonl":
			if !seenRecords || seenIndex {
				return extractedBundle{}, fmt.Errorf("%w: source-files.jsonl is misplaced or duplicated", ErrInvalidBundle)
			}
			seenIndex = true
			result.indexPath = filepath.Join(stage, "source-files.jsonl")
			sources, err := extractAndValidateSourceIndex(ctx, reader, header.Size, result.indexPath, options)
			if err != nil {
				return extractedBundle{}, err
			}
			result.sources = sources
		default:
			return extractedBundle{}, fmt.Errorf("%w: TAR contains an unexpected member", ErrInvalidBundle)
		}
	}
	if !seenRecords || !seenIndex {
		return extractedBundle{}, fmt.Errorf("%w: TAR is missing records or source index", ErrInvalidBundle)
	}
	if expectedArtifactBytes != manifest.ArtifactBytes || counted.count != expectedArtifactBytes || counted.count != initialInfo.Size() {
		return extractedBundle{}, fmt.Errorf("%w: TAR has trailing, truncated, or noncanonical end bytes", ErrInvalidBundle)
	}
	if hex.EncodeToString(counted.hash.Sum(nil)) != manifest.ArtifactSHA256 {
		return extractedBundle{}, fmt.Errorf("%w: artifact SHA-256 does not match manifest", ErrInvalidBundle)
	}
	canonicalBytes, canonicalHash, err := hashCanonicalArtifact(ctx, result)
	if err != nil {
		return extractedBundle{}, err
	}
	if canonicalBytes != manifest.ArtifactBytes || canonicalHash != manifest.ArtifactSHA256 {
		return extractedBundle{}, fmt.Errorf("%w: artifact TAR bytes are not canonical Tools output", ErrInvalidBundle)
	}
	finalInfo, err := artifact.Stat()
	if err != nil {
		return extractedBundle{}, err
	}
	if finalInfo.Size() != initialInfo.Size() || finalInfo.ModTime() != initialInfo.ModTime() {
		return extractedBundle{}, fmt.Errorf("%w: artifact changed during verification", errChatGPTImportSourceChanged)
	}
	if result.recordCounts.Records != manifest.Messages || result.recordCounts.UserMessages != manifest.UserMessages || result.recordCounts.AssistantMessages != manifest.AssistantMessages || len(result.recordCounts.Conversations) != manifest.Conversations {
		return extractedBundle{}, fmt.Errorf("%w: artifact record counts do not match manifest", ErrInvalidBundle)
	}
	return result, nil
}

type countedHashWriter struct {
	ctx   context.Context
	count int64
	hash  hash.Hash
}

func (w *countedHashWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	written, err := w.hash.Write(data)
	w.count += int64(written)
	return written, err
}

func hashCanonicalArtifact(ctx context.Context, extracted extractedBundle) (int64, string, error) {
	output := &countedHashWriter{ctx: ctx, hash: sha256.New()}
	writer := tar.NewWriter(output)
	hashes := make([]string, 0, len(extracted.objects))
	for hashValue := range extracted.objects {
		hashes = append(hashes, hashValue)
	}
	sort.Strings(hashes)
	for _, hashValue := range hashes {
		metadata := extracted.objects[hashValue]
		if err := writeCanonicalTarMember(ctx, writer, "objects/sha256/"+hashValue[:2]+"/"+hashValue, metadata.path, metadata.bytes); err != nil {
			_ = writer.Close()
			return 0, "", err
		}
	}
	for _, member := range []struct {
		name string
		path string
	}{
		{name: "records.jsonl", path: extracted.recordsPath},
		{name: "source-files.jsonl", path: extracted.indexPath},
	} {
		if err := writeCanonicalTarMember(ctx, writer, member.name, member.path, -1); err != nil {
			_ = writer.Close()
			return 0, "", err
		}
	}
	if err := writer.Close(); err != nil {
		return 0, "", err
	}
	return output.count, hex.EncodeToString(output.hash.Sum(nil)), nil
}

func writeCanonicalTarMember(ctx context.Context, writer *tar.Writer, name, sourcePath string, expectedBytes int64) error {
	file, info, err := openPrivateRegular(sourcePath)
	if err != nil {
		return err
	}
	if expectedBytes < 0 {
		expectedBytes = info.Size()
	}
	if info.Size() != expectedBytes {
		_ = file.Close()
		return fmt.Errorf("%w: extracted TAR member size changed", ErrInvalidBundle)
	}
	header := &tar.Header{
		Name: name, Mode: 0o600, Size: expectedBytes, ModTime: time.Unix(0, 0).UTC(), Typeflag: tar.TypeReg,
		Uid: 0, Gid: 0, Uname: "", Gname: "", Format: tar.FormatUSTAR,
	}
	if err := writer.WriteHeader(header); err != nil {
		_ = file.Close()
		return err
	}
	count, copyErr := io.Copy(writer, &contextReader{ctx: ctx, reader: file})
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if count != expectedBytes {
		return fmt.Errorf("%w: extracted TAR member ended early", ErrInvalidBundle)
	}
	return nil
}

func canonicalTarMemberBytes(size int64) (int64, bool) {
	if size < 0 || size > int64(^uint64(0)>>1)-1023 {
		return 0, true
	}
	padded := (size + 511) / 512 * 512
	return 512 + padded, false
}

func validateTarHeader(header *tar.Header) error {
	if header == nil || header.Format != tar.FormatUSTAR || header.Typeflag != tar.TypeReg || header.Mode != 0o600 || header.Size < 0 {
		return errors.New("TAR header is not deterministic USTAR regular-file form")
	}
	if header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" || header.Linkname != "" || header.Devmajor != 0 || header.Devminor != 0 {
		return errors.New("TAR header identity or link fields are not deterministic")
	}
	if !header.ModTime.Equal(time.Unix(0, 0).UTC()) || !header.AccessTime.IsZero() || !header.ChangeTime.IsZero() || len(header.PAXRecords) != 0 || len(header.Xattrs) != 0 {
		return errors.New("TAR header time or extension fields are not deterministic")
	}
	return nil
}

func objectHashFromName(name string) (string, error) {
	tail := strings.TrimPrefix(name, "objects/sha256/")
	parts := strings.Split(tail, "/")
	if len(parts) != 2 || len(parts[0]) != 2 || !isLowerHex64(parts[1]) || parts[0] != parts[1][:2] {
		return "", errors.New("object name is invalid")
	}
	return parts[1], nil
}

func copyVerifiedMember(ctx context.Context, reader io.Reader, target string, expectedBytes int64, expectedHash string) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	hash := sha256.New()
	count, copyErr := io.CopyN(io.MultiWriter(file, hash), &contextReader{ctx: ctx, reader: reader}, expectedBytes)
	if copyErr == nil && (count != expectedBytes || hex.EncodeToString(hash.Sum(nil)) != expectedHash) {
		copyErr = fmt.Errorf("%w: TAR object hash or size mismatch", ErrInvalidBundle)
	}
	if syncErr := file.Sync(); copyErr == nil {
		copyErr = syncErr
	}
	if closeErr := file.Close(); copyErr == nil {
		copyErr = closeErr
	}
	return copyErr
}

func extractAndValidateRecords(ctx context.Context, reader io.Reader, size int64, target, exportID string, maxLineBytes int64) (recordCounts, error) {
	counts := recordCounts{Conversations: make(map[string]struct{})}
	evidenceIDs := make(map[string]struct{})
	err := extractJSONLines(ctx, reader, size, target, maxLineBytes, func(data []byte) error {
		record, err := decodeArtifactRecord(data, exportID)
		if err != nil {
			return err
		}
		if _, exists := evidenceIDs[record.EvidenceID]; exists {
			return errors.New("artifact contains a duplicate evidence ID")
		}
		evidenceIDs[record.EvidenceID] = struct{}{}
		counts.Conversations[record.ConversationID] = struct{}{}
		counts.Records++
		switch record.Role {
		case "user":
			counts.UserMessages++
		case "assistant":
			counts.AssistantMessages++
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrBounds) {
			return recordCounts{}, err
		}
		return recordCounts{}, fmt.Errorf("%w: records.jsonl: %w", ErrInvalidBundle, err)
	}
	if counts.Records == 0 {
		return recordCounts{}, fmt.Errorf("%w: records.jsonl is empty", ErrInvalidBundle)
	}
	return counts, nil
}

func extractAndValidateSourceIndex(ctx context.Context, reader io.Reader, size int64, target string, options Options) ([]sourceIndexRecord, error) {
	var sources []sourceIndexRecord
	var sourceTotalBytes int64
	err := extractJSONLines(ctx, reader, size, target, options.MaxJSONLineBytes, func(data []byte) error {
		if len(sources) >= options.MaxSourceFiles {
			return fmt.Errorf("%w: source index exceeds file count bound", ErrBounds)
		}
		source, err := decodeSourceIndexRecord(data, options.ChunkBytes)
		if err != nil {
			return err
		}
		if source.Bytes > options.MaxSourceFileBytes {
			return fmt.Errorf("%w: source index file exceeds per-file bound", ErrBounds)
		}
		if sourceTotalBytes > options.MaxSourceTotalBytes-source.Bytes {
			return fmt.Errorf("%w: source index exceeds total source bytes bound", ErrBounds)
		}
		sourceTotalBytes += source.Bytes
		if len(sources) > 0 && sources[len(sources)-1].Path >= source.Path {
			return errors.New("source index paths are not strictly sorted")
		}
		sources = append(sources, source)
		return nil
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrBounds) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: source-files.jsonl: %w", ErrInvalidBundle, err)
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("%w: source-files.jsonl is empty", ErrInvalidBundle)
	}
	return sources, nil
}

func extractJSONLines(ctx context.Context, reader io.Reader, size int64, target string, maxLineBytes int64, callback func([]byte) error) error {
	if size <= 0 {
		return errors.New("JSONL member is empty")
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	writer := bufio.NewWriter(file)
	limited := io.LimitReader(&contextReader{ctx: ctx, reader: reader}, size)
	buffered := bufio.NewReaderSize(limited, 64*1024)
	var consumed int64
	for {
		line, readErr := readCanonicalJSONLine(buffered, maxLineBytes)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			_ = file.Close()
			return readErr
		}
		consumed += int64(len(line)) + 1
		if err := callback(line); err != nil {
			_ = file.Close()
			return err
		}
		if _, err := writer.Write(line); err != nil {
			_ = file.Close()
			return err
		}
		if err := writer.WriteByte('\n'); err != nil {
			_ = file.Close()
			return err
		}
	}
	if consumed != size {
		_ = file.Close()
		return errors.New("JSONL member size does not match complete lines")
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
