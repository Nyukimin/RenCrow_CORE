package threadmigration

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExternalSnapshotNewCanonicalAndValidation(t *testing.T) {
	redis := []RedisEntry{
		{Key: "thread:7", Value: []byte(`{"thread_id":7,"nested":{"ok":true}}`), ExpireAtUnixMilli: 1800000001200},
		{Key: "sess:legacy-session", Value: []byte(`{"session_id":"legacy-session","payload":{"n":[1,2]}}`), ExpireAtUnixMilli: 1800000002400},
	}
	qdrant := []QdrantPointSnapshot{
		{
			PointID: "00000000-0000-4000-8000-000000000002",
			Vector:  []float32{3, 4},
			Payload: map[string]json.RawMessage{
				"thread_id":  json.RawMessage(`7`),
				"session_id": json.RawMessage(`"legacy-session"`),
				"nested":     json.RawMessage(`{"b":2,"a":1}`),
			},
		},
		{
			PointID: "00000000-0000-4000-8000-000000000001",
			Vector:  []float32{1, 2},
			Payload: map[string]json.RawMessage{
				"thread_id":  json.RawMessage(`7`),
				"session_id": json.RawMessage(`"legacy-session"`),
			},
		},
	}

	redisBefore := cloneRedisEntries(redis)
	qdrantBefore := cloneQdrantPointsForTest(qdrant)
	snapshot, err := NewExternalSnapshot(redis, qdrant)
	if err != nil {
		t.Fatalf("NewExternalSnapshot() error = %v", err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if snapshot.SchemaVersion != ExternalSnapshotSchemaVersion {
		t.Fatalf("SchemaVersion = %q", snapshot.SchemaVersion)
	}
	if len(snapshot.RedisSHA256) != 64 || len(snapshot.QdrantSHA256) != 64 || len(snapshot.SnapshotSHA256) != 64 {
		t.Fatalf("hash lengths = %d/%d/%d", len(snapshot.RedisSHA256), len(snapshot.QdrantSHA256), len(snapshot.SnapshotSHA256))
	}
	if snapshot.RedisSHA256 != strings.ToLower(snapshot.RedisSHA256) || snapshot.QdrantSHA256 != strings.ToLower(snapshot.QdrantSHA256) || snapshot.SnapshotSHA256 != strings.ToLower(snapshot.SnapshotSHA256) {
		t.Fatal("hashes must be lowercase")
	}

	canonical, err := snapshot.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	reversed, err := NewExternalSnapshot(reverseRedisForTest(redis), reverseQdrantForTest(qdrant))
	if err != nil {
		t.Fatalf("NewExternalSnapshot(reversed) error = %v", err)
	}
	reversedCanonical, err := reversed.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON(reversed) error = %v", err)
	}
	if !bytes.Equal(canonical, reversedCanonical) {
		t.Fatalf("canonical bytes depend on input order:\n%s\n%s", canonical, reversedCanonical)
	}

	if !equalRedisEntriesForTest(redis, redisBefore) || !equalQdrantPointsForTest(qdrant, qdrantBefore) {
		t.Fatal("NewExternalSnapshot mutated source slices")
	}
	snapshotRedisBefore := append([]byte(nil), externalSnapshotRedisValueForTest(t, snapshot.Redis, "thread:7")...)
	snapshotQdrantBefore := externalSnapshotQdrantPointForTest(t, snapshot.Qdrant, "00000000-0000-4000-8000-000000000002")
	redis[0].Value[0] = 'X'
	qdrant[0].Vector[0] = 99
	qdrant[0].Payload["nested"][0] = 'X'
	snapshotQdrantAfter := externalSnapshotQdrantPointForTest(t, snapshot.Qdrant, "00000000-0000-4000-8000-000000000002")
	if !bytes.Equal(externalSnapshotRedisValueForTest(t, snapshot.Redis, "thread:7"), snapshotRedisBefore) ||
		snapshotQdrantAfter.Vector[0] != snapshotQdrantBefore.Vector[0] ||
		!bytes.Equal(snapshotQdrantAfter.Payload["nested"], snapshotQdrantBefore.Payload["nested"]) {
		t.Fatal("snapshot does not own cloned source data")
	}

	empty, err := NewExternalSnapshot(nil, nil)
	if err != nil {
		t.Fatalf("NewExternalSnapshot(empty) error = %v", err)
	}
	if len(empty.Redis) != 0 || len(empty.Qdrant) != 0 {
		t.Fatalf("empty surfaces were not normalized: redis=%#v qdrant=%#v", empty.Redis, empty.Qdrant)
	}
	if err := empty.Validate(); err != nil {
		t.Fatalf("Validate(empty) error = %v", err)
	}
	if _, err := NewExternalSnapshot(nil, []QdrantPointSnapshot{{
		PointID: "00000000-0000-4000-8000-000000000001",
		Vector:  []float32{1},
	}}); err == nil {
		t.Fatal("NewExternalSnapshot accepted nil Qdrant payload")
	}
}

func TestExternalSnapshotRejectsBoundsAndStructuralInvalidity(t *testing.T) {
	validQdrant := QdrantPointSnapshot{
		PointID: "00000000-0000-4000-8000-000000000001",
		Vector:  []float32{1, 2},
		Payload: map[string]json.RawMessage{"ok": json.RawMessage(`true`)},
	}
	validRedis := RedisEntry{Key: "sess:legacy-session", Value: []byte(`{"session_id":"legacy-session"}`), ExpireAtUnixMilli: 1800000001000}
	cases := []struct {
		name    string
		redis   []RedisEntry
		qdrant  []QdrantPointSnapshot
		wantErr bool
	}{
		{name: "duplicate redis key", redis: []RedisEntry{validRedis, validRedis}, qdrant: []QdrantPointSnapshot{validQdrant}, wantErr: true},
		{name: "non-positive expiry", redis: []RedisEntry{{Key: validRedis.Key, Value: validRedis.Value, ExpireAtUnixMilli: 0}}, qdrant: []QdrantPointSnapshot{validQdrant}, wantErr: true},
		{name: "invalid legacy key", redis: []RedisEntry{{Key: "unknown:key", Value: validRedis.Value, ExpireAtUnixMilli: 1800000000001}}, qdrant: []QdrantPointSnapshot{validQdrant}, wantErr: true},
		{name: "redis non-object", redis: []RedisEntry{{Key: validRedis.Key, Value: []byte(`[]`), ExpireAtUnixMilli: 1800000000001}}, qdrant: []QdrantPointSnapshot{validQdrant}, wantErr: true},
		{name: "redis duplicate nested member", redis: []RedisEntry{{Key: validRedis.Key, Value: []byte(`{"a":{"x":1,"x":2}}`), ExpireAtUnixMilli: 1800000000001}}, qdrant: []QdrantPointSnapshot{validQdrant}, wantErr: true},
		{name: "duplicate qdrant id", redis: []RedisEntry{validRedis}, qdrant: []QdrantPointSnapshot{validQdrant, validQdrant}, wantErr: true},
		{name: "empty vector", redis: []RedisEntry{validRedis}, qdrant: []QdrantPointSnapshot{{PointID: validQdrant.PointID, Payload: validQdrant.Payload}}, wantErr: true},
		{name: "dimension mismatch", redis: []RedisEntry{validRedis}, qdrant: []QdrantPointSnapshot{validQdrant, {PointID: "00000000-0000-4000-8000-000000000002", Vector: []float32{1}, Payload: validQdrant.Payload}}, wantErr: true},
		{name: "non-finite vector", redis: []RedisEntry{validRedis}, qdrant: []QdrantPointSnapshot{{PointID: validQdrant.PointID, Vector: []float32{1, float32(math.NaN())}, Payload: validQdrant.Payload}}, wantErr: true},
		{name: "nil payload", redis: []RedisEntry{validRedis}, qdrant: []QdrantPointSnapshot{{PointID: validQdrant.PointID, Vector: []float32{1, 2}}}, wantErr: true},
		{name: "malformed payload value", redis: []RedisEntry{validRedis}, qdrant: []QdrantPointSnapshot{{PointID: validQdrant.PointID, Vector: []float32{1, 2}, Payload: map[string]json.RawMessage{"bad": json.RawMessage(`{`)}}}, wantErr: true},
		{name: "duplicate payload member", redis: []RedisEntry{validRedis}, qdrant: []QdrantPointSnapshot{{PointID: validQdrant.PointID, Vector: []float32{1, 2}, Payload: map[string]json.RawMessage{"bad": json.RawMessage(`{"x":1,"x":2}`)}}}, wantErr: true},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			snapshot := ExternalSnapshot{
				SchemaVersion: ExternalSnapshotSchemaVersion,
				Redis:         testCase.redis,
				Qdrant:        testCase.qdrant,
			}
			if err := snapshot.Validate(); (err != nil) != testCase.wantErr {
				t.Fatalf("Validate() error = %v, wantErr=%v", err, testCase.wantErr)
			}
		})
	}
}

func TestExternalSnapshotStrictReadWriteAndTamper(t *testing.T) {
	snapshot := externalSnapshotFixture(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "external-snapshot.json")
	if err := WriteExternalSnapshotFresh(path, snapshot); err != nil {
		t.Fatalf("WriteExternalSnapshotFresh() error = %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat() error = %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("file mode = %o, want 600", got)
		}
	}
	read, err := ReadExternalSnapshotStrict(path)
	if err != nil {
		t.Fatalf("ReadExternalSnapshotStrict() error = %v", err)
	}
	if err := read.Validate(); err != nil {
		t.Fatalf("read Validate() error = %v", err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if err := WriteExternalSnapshotFresh(path, snapshot); err == nil {
		t.Fatal("second write unexpectedly overwrote existing file")
	}
	unchanged, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after rejected write) error = %v", err)
	}
	if !bytes.Equal(original, unchanged) {
		t.Fatal("rejected fresh write changed existing file")
	}

	tampered := append([]byte(nil), original...)
	needle := []byte(`"snapshot_sha256":"`)
	position := bytes.Index(tampered, needle)
	if position < 0 {
		t.Fatal("fixture is missing snapshot hash")
	}
	tampered[position+len(needle)] = '0'
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatalf("WriteFile(tamper) error = %v", err)
	}
	if _, err := ReadExternalSnapshotStrict(path); err == nil {
		t.Fatal("tampered snapshot was accepted")
	}
}

func TestExternalSnapshotStrictJSONRejectsUnknownDuplicateTrailing(t *testing.T) {
	snapshot := externalSnapshotFixture(t)
	canonical, err := snapshot.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON() error = %v", err)
	}
	duplicateRootPrefix := append([]byte(`{"schema_version":"`), []byte(ExternalSnapshotSchemaVersion)...)
	duplicateRootPrefix = append(duplicateRootPrefix, []byte(`","schema_version"`)...)
	cases := []struct {
		name string
		data []byte
	}{
		{name: "unknown root member", data: bytes.Replace(canonical, []byte(`{"schema_version"`), []byte(`{"unknown":1,"schema_version"`), 1)},
		{name: "duplicate root member", data: bytes.Replace(canonical, []byte(`{"schema_version"`), duplicateRootPrefix, 1)},
		{name: "duplicate nested member", data: bytes.Replace(canonical, []byte(`"nested":{"a":1,"b":2}`), []byte(`"nested":{"a":1,"a":2,"b":2}`), 1)},
		{name: "trailing value", data: append(append([]byte(nil), canonical...), []byte(` {}`)...)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "invalid.json")
			if err := os.WriteFile(path, append(testCase.data, '\n'), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := ReadExternalSnapshotStrict(path); err == nil {
				t.Fatal("invalid JSON was accepted")
			}
		})
	}
}

func TestExternalSnapshotStrictFileBoundary(t *testing.T) {
	snapshot := externalSnapshotFixture(t)
	directory := t.TempDir()
	existingDirectory := filepath.Join(directory, "existing")
	if err := os.Mkdir(existingDirectory, 0o755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := WriteExternalSnapshotFresh(existingDirectory, snapshot); err == nil {
		t.Fatal("directory destination was accepted")
	}

	symlinkPath := filepath.Join(directory, "symlink.json")
	targetPath := filepath.Join(directory, "target.json")
	if err := os.WriteFile(targetPath, []byte("target"), 0o600); err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}
	if err := os.Symlink(targetPath, symlinkPath); err == nil {
		if err := WriteExternalSnapshotFresh(symlinkPath, snapshot); err == nil {
			t.Fatal("symlink destination was accepted")
		}
		if _, err := ReadExternalSnapshotStrict(symlinkPath); err == nil {
			t.Fatal("symlink source was accepted")
		}
	}

	overLimit := filepath.Join(directory, "over-limit.json")
	file, err := os.OpenFile(overLimit, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatalf("OpenFile(over-limit) error = %v", err)
	}
	if err := file.Truncate(ExternalSnapshotMaxFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatalf("Truncate(over-limit) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(over-limit) error = %v", err)
	}
	if _, err := ReadExternalSnapshotStrict(overLimit); err == nil {
		t.Fatal("oversized snapshot was accepted")
	}
}

func externalSnapshotFixture(t *testing.T) ExternalSnapshot {
	t.Helper()
	snapshot, err := NewExternalSnapshot(
		[]RedisEntry{{Key: "sess:legacy-session", Value: []byte(`{"session_id":"legacy-session","payload":{"x":1}}`), ExpireAtUnixMilli: 1800000003600}},
		[]QdrantPointSnapshot{{
			PointID: "00000000-0000-4000-8000-000000000001",
			Vector:  []float32{1, 2},
			Payload: map[string]json.RawMessage{
				"session_id": json.RawMessage(`"legacy-session"`),
				"thread_id":  json.RawMessage(`7`),
				"nested":     json.RawMessage(`{"a":1,"b":2}`),
			},
		}},
	)
	if err != nil {
		t.Fatalf("fixture NewExternalSnapshot() error = %v", err)
	}
	return snapshot
}

func cloneQdrantPointsForTest(points []QdrantPointSnapshot) []QdrantPointSnapshot {
	clone := make([]QdrantPointSnapshot, len(points))
	for index, point := range points {
		clone[index] = QdrantPointSnapshot{PointID: point.PointID, Vector: append([]float32(nil), point.Vector...), Payload: cloneQdrantPayload(point.Payload)}
	}
	return clone
}

func equalRedisEntriesForTest(left, right []RedisEntry) bool {
	if len(left) != len(right) {
		return false
	}
	rightByKey := make(map[string]RedisEntry, len(right))
	for _, entry := range right {
		rightByKey[entry.Key] = entry
	}
	for _, entry := range left {
		other, ok := rightByKey[entry.Key]
		if !ok || entry.ExpireAtUnixMilli != other.ExpireAtUnixMilli || !bytes.Equal(entry.Value, other.Value) {
			return false
		}
	}
	return true
}

func equalQdrantPointsForTest(left, right []QdrantPointSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	rightByID := make(map[string]QdrantPointSnapshot, len(right))
	for _, point := range right {
		rightByID[point.PointID] = point
	}
	for _, point := range left {
		other, ok := rightByID[point.PointID]
		if !ok || len(point.Vector) != len(other.Vector) || len(point.Payload) != len(other.Payload) {
			return false
		}
		for vectorIndex := range point.Vector {
			if point.Vector[vectorIndex] != other.Vector[vectorIndex] {
				return false
			}
		}
		for key, value := range point.Payload {
			if !bytes.Equal(value, other.Payload[key]) {
				return false
			}
		}
	}
	return true
}

func reverseRedisForTest(entries []RedisEntry) []RedisEntry {
	result := cloneRedisEntries(entries)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func reverseQdrantForTest(points []QdrantPointSnapshot) []QdrantPointSnapshot {
	result := cloneQdrantPointsForTest(points)
	for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
		result[left], result[right] = result[right], result[left]
	}
	return result
}

func externalSnapshotRedisValueForTest(t *testing.T, entries []RedisEntry, key string) []byte {
	t.Helper()
	for _, entry := range entries {
		if entry.Key == key {
			return entry.Value
		}
	}
	t.Fatalf("Redis entry %q not found", key)
	return nil
}

func externalSnapshotQdrantPointForTest(t *testing.T, points []QdrantPointSnapshot, pointID string) QdrantPointSnapshot {
	t.Helper()
	for _, point := range points {
		if point.PointID == pointID {
			return QdrantPointSnapshot{
				PointID: point.PointID,
				Vector:  append([]float32(nil), point.Vector...),
				Payload: cloneExternalQdrantPayload(point.Payload),
			}
		}
	}
	t.Fatalf("Qdrant point %q not found", pointID)
	return QdrantPointSnapshot{}
}
