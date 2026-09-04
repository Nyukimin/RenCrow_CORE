package threadmigration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// ExternalSnapshotSchemaVersion identifies the one-shot Step 05 logical
	// snapshot artifact. It is deliberately distinct from the preparation
	// receipts, which describe later transformations.
	ExternalSnapshotSchemaVersion = "rencrow.threadmigration.external_snapshot.v1"

	// ExternalSnapshotMaxFileBytes bounds the serialized artifact before it is
	// parsed or published. The logical surfaces have tighter per-source bounds.
	ExternalSnapshotMaxFileBytes int64 = 512 << 20

	// The source value validators cap each embedded value at depth 128. The
	// artifact wrapper adds several object/array levels around those values,
	// so its scanner allows bounded wrapper overhead while retaining a hard
	// recursion limit.
	externalSnapshotMaxJSONDepth = 256
	externalSnapshotTempPattern  = ".rencrow-threadmigration-external-snapshot-*.tmp"
)

var (
	errExternalSnapshotInvalid       = errors.New("external snapshot is invalid")
	errExternalSnapshotRead          = errors.New("external snapshot read failed")
	errExternalSnapshotPath          = errors.New("external snapshot path is unsafe")
	errExternalSnapshotDestination   = errors.New("external snapshot destination is not fresh")
	errExternalSnapshotWrite         = errors.New("external snapshot write failed")
	errExternalSnapshotSize          = errors.New("external snapshot exceeds size limit")
	errExternalSnapshotDirectory     = errors.New("external snapshot parent is not canonical")
	errExternalSnapshotPublish       = errors.New("external snapshot publication failed")
	errExternalSnapshotDirectorySync = errors.New("external snapshot directory sync failed")
)

// ExternalSnapshot is one hash-bound logical artifact for the legacy Redis
// and Qdrant surfaces used by Step 05. It contains no runtime clients, writer
// quiescence proof, or apply handles; the owner capture operation must bind
// this artifact to its stopped-writer receipt before a later operation uses it.
type ExternalSnapshot struct {
	SchemaVersion  string                `json:"schema_version"`
	Redis          []RedisEntry          `json:"redis"`
	Qdrant         []QdrantPointSnapshot `json:"qdrant"`
	RedisSHA256    string                `json:"redis_sha256"`
	QdrantSHA256   string                `json:"qdrant_sha256"`
	SnapshotSHA256 string                `json:"snapshot_sha256"`
}

// NewExternalSnapshot clones and validates the supplied adapter-neutral
// surfaces, then computes their canonical surface hashes and the artifact
// self-hash. The caller-owned slices and nested values are never changed.
func NewExternalSnapshot(redis []RedisEntry, qdrant []QdrantPointSnapshot) (ExternalSnapshot, error) {
	snapshot := ExternalSnapshot{
		SchemaVersion: ExternalSnapshotSchemaVersion,
		Redis:         cloneRedisEntries(redis),
		Qdrant:        cloneExternalQdrantPoints(qdrant),
	}
	if err := snapshot.validateSurfaces(); err != nil {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	if err := snapshot.computeSurfaceHashes(); err != nil {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	canonical, err := snapshot.CanonicalJSON()
	if err != nil {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	if externalSnapshotEncodedSize(canonical) > ExternalSnapshotMaxFileBytes {
		return ExternalSnapshot{}, errExternalSnapshotSize
	}
	snapshot.SnapshotSHA256 = externalSnapshotSHA256(canonical)
	if err := snapshot.Validate(); err != nil {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	return snapshot, nil
}

// Validate checks the schema, bounded logical surfaces, canonical surface
// hashes, and self-referential artifact hash.
func (snapshot ExternalSnapshot) Validate() error {
	if err := snapshot.validateSurfaces(); err != nil {
		return err
	}
	if !validExternalSnapshotSHA256(snapshot.RedisSHA256) || !validExternalSnapshotSHA256(snapshot.QdrantSHA256) || !validExternalSnapshotSHA256(snapshot.SnapshotSHA256) {
		return errExternalSnapshotInvalid
	}
	redisSHA256, qdrantSHA256, err := snapshot.surfaceHashes()
	if err != nil || redisSHA256 != snapshot.RedisSHA256 || qdrantSHA256 != snapshot.QdrantSHA256 {
		return errExternalSnapshotInvalid
	}
	canonical, err := snapshot.CanonicalJSON()
	if err != nil || externalSnapshotEncodedSize(canonical) > ExternalSnapshotMaxFileBytes || externalSnapshotSHA256(canonical) != snapshot.SnapshotSHA256 {
		return errExternalSnapshotInvalid
	}
	return nil
}

// CanonicalJSON returns deterministic artifact bytes with SnapshotSHA256
// blanked to avoid a self-referential digest. Redis entries are sorted by key
// and Qdrant points by point ID in cloned slices, so the receiver and its
// nested values remain untouched.
func (snapshot ExternalSnapshot) CanonicalJSON() ([]byte, error) {
	if err := snapshot.validateSurfaces(); err != nil {
		return nil, err
	}
	return snapshot.canonicalJSON("")
}

func (snapshot ExternalSnapshot) canonicalJSON(snapshotSHA256 string) ([]byte, error) {
	canonical := externalSnapshotCanonical{
		SchemaVersion:  snapshot.SchemaVersion,
		Redis:          cloneRedisEntries(snapshot.Redis),
		Qdrant:         cloneExternalQdrantPoints(snapshot.Qdrant),
		RedisSHA256:    snapshot.RedisSHA256,
		QdrantSHA256:   snapshot.QdrantSHA256,
		SnapshotSHA256: snapshotSHA256,
	}
	sort.Slice(canonical.Redis, func(left, right int) bool {
		if canonical.Redis[left].Key != canonical.Redis[right].Key {
			return canonical.Redis[left].Key < canonical.Redis[right].Key
		}
		if canonical.Redis[left].ExpireAtUnixMilli != canonical.Redis[right].ExpireAtUnixMilli {
			return canonical.Redis[left].ExpireAtUnixMilli < canonical.Redis[right].ExpireAtUnixMilli
		}
		return bytes.Compare(canonical.Redis[left].Value, canonical.Redis[right].Value) < 0
	})
	sort.Slice(canonical.Qdrant, func(left, right int) bool {
		return canonical.Qdrant[left].PointID < canonical.Qdrant[right].PointID
	})
	return json.Marshal(canonical)
}

// WriteExternalSnapshotFresh atomically publishes one validated artifact to a
// fresh destination. It refuses existing targets and symlinked/non-canonical
// parents, writes a 0600 temporary file, syncs it, then syncs the directory.
func WriteExternalSnapshotFresh(path string, snapshot ExternalSnapshot) error {
	if err := snapshot.Validate(); err != nil {
		return errExternalSnapshotInvalid
	}
	canonical, err := snapshot.canonicalJSON(snapshot.SnapshotSHA256)
	if err != nil {
		return errExternalSnapshotInvalid
	}
	encoded := append(canonical, '\n')
	if int64(len(encoded)) > ExternalSnapshotMaxFileBytes {
		return errExternalSnapshotSize
	}
	absolute, parent, err := externalSnapshotFreshPath(path)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(parent, externalSnapshotTempPattern)
	if err != nil {
		return errExternalSnapshotWrite
	}
	temporaryName := temporary.Name()
	cleanTemporary := true
	defer func() {
		if cleanTemporary {
			_ = temporary.Close()
			_ = os.Remove(temporaryName)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return errExternalSnapshotWrite
	}
	written, err := temporary.Write(encoded)
	if err != nil || written != len(encoded) {
		return errExternalSnapshotWrite
	}
	if err := temporary.Sync(); err != nil {
		return errExternalSnapshotWrite
	}
	if err := temporary.Close(); err != nil {
		return errExternalSnapshotWrite
	}
	if err := ensureExternalSnapshotRegular0600(temporaryName); err != nil {
		return errExternalSnapshotWrite
	}
	// Check immediately before publication as well as during path resolution.
	// The link below is the no-clobber primitive; this check gives callers a
	// stable error before the atomic publication attempt in the usual case.
	if _, err := os.Lstat(absolute); err == nil {
		return errExternalSnapshotDestination
	} else if !errors.Is(err, os.ErrNotExist) {
		return errExternalSnapshotDestination
	}
	// A hard link publishes the already-synced inode without the clobbering
	// semantics of POSIX rename(2). It is atomic for a same-directory target
	// and fails when another writer wins the fresh destination race.
	if err := os.Link(temporaryName, absolute); err != nil {
		return errExternalSnapshotPublish
	}
	// The final name now owns the inode. Do not let deferred cleanup remove a
	// path that may have been replaced if temporary-name cleanup fails.
	cleanTemporary = false
	if err := os.Remove(temporaryName); err != nil {
		return errExternalSnapshotPublish
	}
	if err := ensureExternalSnapshotRegular0600(absolute); err != nil {
		return errExternalSnapshotPublish
	}
	if err := syncExternalSnapshotDirectory(parent); err != nil {
		return errExternalSnapshotDirectorySync
	}
	return nil
}

// ReadExternalSnapshotStrict reads and validates one regular, non-symlink
// artifact. It bounds the file before allocation, detects changes in size
// during the read, and rejects duplicate JSON members at every depth,
// unknown root fields, and trailing JSON values.
func ReadExternalSnapshotStrict(path string) (ExternalSnapshot, error) {
	absolute, _, err := externalSnapshotExistingPath(path)
	if err != nil {
		return ExternalSnapshot{}, errExternalSnapshotRead
	}
	before, err := os.Lstat(absolute)
	if err != nil || !externalSnapshotRegularNonSymlink(before) || before.Size() < 0 || before.Size() > ExternalSnapshotMaxFileBytes {
		return ExternalSnapshot{}, errExternalSnapshotRead
	}
	file, err := os.Open(absolute)
	if err != nil {
		return ExternalSnapshot{}, errExternalSnapshotRead
	}
	data, readErr := io.ReadAll(io.LimitReader(file, ExternalSnapshotMaxFileBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return ExternalSnapshot{}, errExternalSnapshotRead
	}
	after, err := os.Lstat(absolute)
	if err != nil || !externalSnapshotRegularNonSymlink(after) || after.Size() != before.Size() || int64(len(data)) != before.Size() || int64(len(data)) > ExternalSnapshotMaxFileBytes {
		return ExternalSnapshot{}, errExternalSnapshotRead
	}
	snapshot, err := decodeExternalSnapshotStrict(data)
	if err != nil {
		return ExternalSnapshot{}, errExternalSnapshotRead
	}
	if err := snapshot.Validate(); err != nil {
		return ExternalSnapshot{}, errExternalSnapshotRead
	}
	return snapshot, nil
}

type externalSnapshotCanonical struct {
	SchemaVersion  string                `json:"schema_version"`
	Redis          []RedisEntry          `json:"redis"`
	Qdrant         []QdrantPointSnapshot `json:"qdrant"`
	RedisSHA256    string                `json:"redis_sha256"`
	QdrantSHA256   string                `json:"qdrant_sha256"`
	SnapshotSHA256 string                `json:"snapshot_sha256"`
}

func (snapshot ExternalSnapshot) validateSurfaces() error {
	if snapshot.SchemaVersion != ExternalSnapshotSchemaVersion {
		return errExternalSnapshotInvalid
	}
	if len(snapshot.Redis) > RedisPreparationMaxEntries || len(snapshot.Qdrant) > QdrantPreparationMaxPoints {
		return errExternalSnapshotInvalid
	}
	seenRedisKeys := make(map[string]struct{}, len(snapshot.Redis))
	for _, entry := range snapshot.Redis {
		if entry.ExpireAtUnixMilli <= 0 || len(entry.Value) > RedisPreparationMaxValueBytes || !utf8.ValidString(entry.Key) || !utf8.Valid(entry.Value) {
			return errExternalSnapshotInvalid
		}
		if _, exists := seenRedisKeys[entry.Key]; exists {
			return errExternalSnapshotInvalid
		}
		seenRedisKeys[entry.Key] = struct{}{}
		if _, _, _, err := parseRedisLegacyKey(entry.Key); err != nil {
			return errExternalSnapshotInvalid
		}
		if _, err := decodeRedisObject(entry.Value); err != nil {
			return errExternalSnapshotInvalid
		}
	}

	seenPointIDs := make(map[string]struct{}, len(snapshot.Qdrant))
	vectorDimension := 0
	for index, point := range snapshot.Qdrant {
		if err := validateQdrantSourcePointID(point.PointID); err != nil || !utf8.ValidString(point.PointID) {
			return errExternalSnapshotInvalid
		}
		if _, exists := seenPointIDs[point.PointID]; exists {
			return errExternalSnapshotInvalid
		}
		seenPointIDs[point.PointID] = struct{}{}
		if len(point.Vector) == 0 {
			return errExternalSnapshotInvalid
		}
		if index == 0 {
			vectorDimension = len(point.Vector)
		} else if len(point.Vector) != vectorDimension {
			return errExternalSnapshotInvalid
		}
		for _, value := range point.Vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return errExternalSnapshotInvalid
			}
		}
		if point.Payload == nil {
			return errExternalSnapshotInvalid
		}
		for key, raw := range point.Payload {
			if !utf8.ValidString(key) {
				return errExternalSnapshotInvalid
			}
			if _, err := qdrantCanonicalJSONValue(raw); err != nil {
				return errExternalSnapshotInvalid
			}
		}
		payload, err := qdrantPayloadJSONBytes(point.Payload)
		if err != nil || len(payload) > QdrantPreparationMaxPayloadBytes {
			return errExternalSnapshotInvalid
		}
	}
	return nil
}

func (snapshot *ExternalSnapshot) computeSurfaceHashes() error {
	redisSHA256, qdrantSHA256, err := snapshot.surfaceHashes()
	if err != nil {
		return err
	}
	snapshot.RedisSHA256 = redisSHA256
	snapshot.QdrantSHA256 = qdrantSHA256
	return nil
}

func (snapshot ExternalSnapshot) surfaceHashes() (string, string, error) {
	redisSHA256, err := redisEntriesSHA256(snapshot.Redis)
	if err != nil {
		return "", "", err
	}
	qdrantCanonical, err := qdrantCanonicalPointsJSON(snapshot.Qdrant)
	if err != nil {
		return "", "", err
	}
	return redisSHA256, qdrantSHA256(qdrantCanonical), nil
}

func cloneExternalQdrantPoints(points []QdrantPointSnapshot) []QdrantPointSnapshot {
	if points == nil {
		return []QdrantPointSnapshot{}
	}
	clone := make([]QdrantPointSnapshot, len(points))
	for index, point := range points {
		clone[index] = QdrantPointSnapshot{
			PointID: point.PointID,
			Vector:  append([]float32(nil), point.Vector...),
			Payload: cloneExternalQdrantPayload(point.Payload),
		}
	}
	return clone
}

func cloneExternalQdrantPayload(payload map[string]json.RawMessage) map[string]json.RawMessage {
	if payload == nil {
		return nil
	}
	return cloneQdrantPayload(payload)
}

func validExternalSnapshotSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func externalSnapshotSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func externalSnapshotEncodedSize(canonicalWithoutSelfHash []byte) int64 {
	// Publishing replaces the empty self-hash string with 64 lowercase hex
	// characters and appends one newline.
	return int64(len(canonicalWithoutSelfHash)) + sha256.Size*2 + 1
}

func externalSnapshotFreshPath(raw string) (string, string, error) {
	absolute, err := externalSnapshotAbsolutePath(raw)
	if err != nil {
		return "", "", errExternalSnapshotPath
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", "", errExternalSnapshotDestination
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", errExternalSnapshotDestination
	}
	parent := filepath.Dir(absolute)
	if err := validateExternalSnapshotCanonicalDirectory(parent); err != nil {
		return "", "", err
	}
	return absolute, parent, nil
}

func externalSnapshotExistingPath(raw string) (string, string, error) {
	absolute, err := externalSnapshotAbsolutePath(raw)
	if err != nil {
		return "", "", errExternalSnapshotPath
	}
	info, err := os.Lstat(absolute)
	if err != nil || !externalSnapshotRegularNonSymlink(info) {
		return "", "", errExternalSnapshotPath
	}
	realPath, err := filepath.EvalSymlinks(absolute)
	if err != nil || !externalSnapshotSamePath(absolute, realPath) {
		return "", "", errExternalSnapshotPath
	}
	parent := filepath.Dir(absolute)
	if err := validateExternalSnapshotCanonicalDirectory(parent); err != nil {
		return "", "", err
	}
	return absolute, parent, nil
}

func externalSnapshotAbsolutePath(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" || strings.IndexByte(raw, 0) >= 0 {
		return "", errExternalSnapshotPath
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", errExternalSnapshotPath
	}
	return filepath.Clean(absolute), nil
}

func validateExternalSnapshotCanonicalDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errExternalSnapshotDirectory
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || !externalSnapshotSamePath(path, realPath) {
		return errExternalSnapshotDirectory
	}
	return nil
}

func externalSnapshotSamePath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if left == right {
		return true
	}
	return filepath.VolumeName(left) != "" && strings.EqualFold(left, right)
}

func externalSnapshotRegularNonSymlink(info os.FileInfo) bool {
	return info != nil && info.Mode()&os.ModeSymlink == 0 && info.Mode().IsRegular()
}

func ensureExternalSnapshotRegular0600(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !externalSnapshotRegularNonSymlink(info) || info.Mode().Perm() != 0o600 {
		return errExternalSnapshotWrite
	}
	return nil
}

func syncExternalSnapshotDirectory(path string) error {
	// Windows does not expose a portable directory fsync operation. The file
	// itself is synced before publication; Unix-like systems additionally flush
	// the directory entry after publication.
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return errExternalSnapshotDirectorySync
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return errExternalSnapshotDirectorySync
	}
	return nil
}

func decodeExternalSnapshotStrict(data []byte) (ExternalSnapshot, error) {
	if !utf8.Valid(data) || scanExternalSnapshotJSON(data) != nil {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil || members == nil {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	expected := map[string]struct{}{
		"schema_version": {}, "redis": {}, "qdrant": {},
		"redis_sha256": {}, "qdrant_sha256": {}, "snapshot_sha256": {},
	}
	if len(members) != len(expected) {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	for key := range expected {
		if _, ok := members[key]; !ok {
			return ExternalSnapshot{}, errExternalSnapshotInvalid
		}
	}
	if bytes.Equal(bytes.TrimSpace(members["redis"]), []byte("null")) || bytes.Equal(bytes.TrimSpace(members["qdrant"]), []byte("null")) {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot ExternalSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return ExternalSnapshot{}, errExternalSnapshotInvalid
	}
	return snapshot, nil
}

func scanExternalSnapshotJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return errExternalSnapshotInvalid
	}
	if err := scanExternalSnapshotJSONValue(decoder, first, 0); err != nil {
		return errExternalSnapshotInvalid
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errExternalSnapshotInvalid
	}
	return nil
}

func scanExternalSnapshotJSONValue(decoder *json.Decoder, token json.Token, depth int) error {
	if depth > externalSnapshotMaxJSONDepth {
		return errExternalSnapshotInvalid
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errExternalSnapshotInvalid
			}
			key, ok := keyToken.(string)
			if !ok {
				return errExternalSnapshotInvalid
			}
			if _, exists := seen[key]; exists {
				return errExternalSnapshotInvalid
			}
			seen[key] = struct{}{}
			valueToken, err := decoder.Token()
			if err != nil {
				return errExternalSnapshotInvalid
			}
			if err := scanExternalSnapshotJSONValue(decoder, valueToken, depth+1); err != nil {
				return errExternalSnapshotInvalid
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim('}') {
			return errExternalSnapshotInvalid
		}
	case '[':
		for decoder.More() {
			valueToken, err := decoder.Token()
			if err != nil {
				return errExternalSnapshotInvalid
			}
			if err := scanExternalSnapshotJSONValue(decoder, valueToken, depth+1); err != nil {
				return errExternalSnapshotInvalid
			}
		}
		closeToken, err := decoder.Token()
		if err != nil || closeToken != json.Delim(']') {
			return errExternalSnapshotInvalid
		}
	default:
		return errExternalSnapshotInvalid
	}
	return nil
}
