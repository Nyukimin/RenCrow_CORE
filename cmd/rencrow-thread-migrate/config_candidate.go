package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	adapterconfig "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
)

const (
	canonicalThreadConfigCandidateSchema        = "rencrow.threadmigration.config_candidate.v1"
	canonicalThreadConfigCandidateReady         = "candidate_not_active"
	canonicalThreadConfigCandidateBlocked       = "blocked"
	canonicalThreadConfigMaxBytes         int64 = 16 << 20
)

type canonicalThreadConfigCandidateReceipt struct {
	SchemaVersion                   string `json:"schema_version"`
	Status                          string `json:"status"`
	SourceConfigSHA256              string `json:"source_config_sha256"`
	OutputConfigSHA256              string `json:"output_config_sha256"`
	SourceRedisDB                   int    `json:"source_redis_db"`
	TargetRedisDB                   int    `json:"target_redis_db"`
	TargetCollectionSHA256          string `json:"target_collection_sha256"`
	OnlyCanonicalRouteFieldsChanged bool   `json:"only_canonical_route_fields_changed"`
	ReceiptSHA256                   string `json:"receipt_sha256"`
	ErrorCode                       string `json:"error_code"`
}

func (receipt canonicalThreadConfigCandidateReceipt) canonicalJSON() ([]byte, error) {
	copy := receipt
	copy.ReceiptSHA256 = ""
	return json.Marshal(copy)
}

func (receipt canonicalThreadConfigCandidateReceipt) computeSHA256() (string, error) {
	data, err := receipt.canonicalJSON()
	if err != nil {
		return "", err
	}
	return canonicalThreadConfigSHA256(data), nil
}

func (receipt canonicalThreadConfigCandidateReceipt) validate() error {
	if receipt.SchemaVersion != canonicalThreadConfigCandidateSchema {
		return errors.New("config candidate receipt schema is invalid")
	}
	switch receipt.Status {
	case canonicalThreadConfigCandidateReady:
		if receipt.ErrorCode != "" || !receipt.OnlyCanonicalRouteFieldsChanged || receipt.SourceRedisDB < 0 || receipt.TargetRedisDB < 0 || receipt.SourceRedisDB == receipt.TargetRedisDB {
			return errors.New("config candidate receipt terminal state is invalid")
		}
		for _, value := range []string{receipt.SourceConfigSHA256, receipt.OutputConfigSHA256, receipt.TargetCollectionSHA256} {
			if !validCanonicalThreadConfigSHA256(value) {
				return errors.New("config candidate receipt hash is invalid")
			}
		}
	case canonicalThreadConfigCandidateBlocked:
		if receipt.ErrorCode == "" || receipt.OnlyCanonicalRouteFieldsChanged || receipt.SourceRedisDB < -1 || receipt.TargetRedisDB < -1 {
			return errors.New("blocked config candidate receipt is invalid")
		}
	default:
		return errors.New("config candidate receipt status is invalid")
	}
	if !validCanonicalThreadConfigSHA256(receipt.ReceiptSHA256) {
		return errors.New("config candidate receipt self hash is invalid")
	}
	digest, err := receipt.computeSHA256()
	if err != nil || digest != receipt.ReceiptSHA256 {
		return errors.New("config candidate receipt self hash does not match")
	}
	return nil
}

func renderCanonicalThreadConfigCandidate(sourcePath, outputPath string, targetRedisDB int, targetCollection string) (canonicalThreadConfigCandidateReceipt, error) {
	receipt := canonicalThreadConfigCandidateReceipt{
		SchemaVersion:          canonicalThreadConfigCandidateSchema,
		Status:                 canonicalThreadConfigCandidateBlocked,
		SourceRedisDB:          -1,
		TargetRedisDB:          targetRedisDB,
		TargetCollectionSHA256: canonicalThreadConfigSHA256([]byte(targetCollection)),
	}
	if targetRedisDB < 0 {
		return blockCanonicalThreadConfigCandidate(receipt, "invalid_target_db")
	}
	if !validCanonicalThreadConfigCollection(targetCollection) {
		return blockCanonicalThreadConfigCandidate(receipt, "invalid_target_collection")
	}

	source, sourceInfo, err := readCanonicalThreadConfigSource(sourcePath)
	if err != nil {
		return blockCanonicalThreadConfigCandidate(receipt, "source_read")
	}
	receipt.SourceConfigSHA256 = canonicalThreadConfigSHA256(source)
	output, targetURL, sourceDB, err := rewriteCanonicalThreadConfig(source, targetRedisDB, targetCollection)
	receipt.SourceRedisDB = sourceDB
	if err != nil {
		return blockCanonicalThreadConfigCandidate(receipt, "source_config")
	}
	if sourceDB == targetRedisDB {
		return blockCanonicalThreadConfigCandidate(receipt, "source_target_db_equal")
	}
	receipt.OutputConfigSHA256 = canonicalThreadConfigSHA256(output)

	if err := verifyCanonicalThreadConfigSourceUnchanged(sourcePath, sourceInfo, source); err != nil {
		return blockCanonicalThreadConfigCandidate(receipt, "source_changed")
	}
	target, err := resolveCanonicalThreadConfigOutput(outputPath)
	if err != nil {
		return blockCanonicalThreadConfigCandidate(receipt, "output_path")
	}
	if err := publishCanonicalThreadConfigCandidate(target, output); err != nil {
		return blockCanonicalThreadConfigCandidate(receipt, "output_write")
	}
	if err := verifyCanonicalThreadConfigCandidate(sourcePath, target, targetURL, targetCollection, output); err != nil {
		return blockCanonicalThreadConfigCandidate(receipt, "output_verify")
	}
	receipt.Status = canonicalThreadConfigCandidateReady
	receipt.ErrorCode = ""
	receipt.OnlyCanonicalRouteFieldsChanged = true
	sealed, err := sealCanonicalThreadConfigCandidate(receipt)
	if err != nil {
		return blockCanonicalThreadConfigCandidate(receipt, "receipt_invalid")
	}
	return sealed, nil
}

func rewriteCanonicalThreadConfig(source []byte, targetRedisDB int, targetCollection string) ([]byte, string, int, error) {
	if len(source) == 0 || int64(len(source)) > canonicalThreadConfigMaxBytes || !utf8.Valid(source) {
		return nil, "", -1, errors.New("invalid config bytes")
	}
	var root yaml.Node
	if err := yaml.Unmarshal(source, &root); err != nil || canonicalThreadConfigHasAlias(&root) {
		return nil, "", -1, errors.New("invalid YAML")
	}
	document := canonicalThreadConfigDocumentMapping(&root)
	conversation, ok := canonicalThreadConfigUniqueMappingValue(document, "conversation")
	if !ok || conversation.Kind != yaml.MappingNode {
		return nil, "", -1, errors.New("conversation mapping is invalid")
	}
	redisNode, ok := canonicalThreadConfigUniqueMappingValue(conversation, "redis_url")
	if !ok {
		return nil, "", -1, errors.New("redis_url is missing or duplicate")
	}
	collectionNode, ok := canonicalThreadConfigUniqueMappingValue(conversation, "vector_collection")
	if !ok {
		return nil, "", -1, errors.New("vector_collection is missing or duplicate")
	}
	for _, node := range []*yaml.Node{redisNode, collectionNode} {
		if node.Kind != yaml.ScalarNode || node.Style&(yaml.LiteralStyle|yaml.FoldedStyle|yaml.TaggedStyle) != 0 || strings.Contains(node.Value, "${") {
			return nil, "", -1, errors.New("route scalar is not a literal single-line value")
		}
	}
	targetURL, sourceDB, err := canonicalThreadConfigTargetRedisURL(redisNode.Value, targetRedisDB)
	if err != nil {
		return nil, "", -1, err
	}
	redisSpan, err := canonicalThreadConfigScalarSpan(source, redisNode)
	if err != nil {
		return nil, "", -1, err
	}
	collectionSpan, err := canonicalThreadConfigScalarSpan(source, collectionNode)
	if err != nil {
		return nil, "", -1, err
	}
	type replacement struct {
		start int
		end   int
		value string
	}
	replacements := []replacement{
		{redisSpan[0], redisSpan[1], strconv.Quote(targetURL)},
		{collectionSpan[0], collectionSpan[1], strconv.Quote(targetCollection)},
	}
	sort.Slice(replacements, func(left, right int) bool { return replacements[left].start > replacements[right].start })
	output := append([]byte(nil), source...)
	for _, item := range replacements {
		output = append(output[:item.start], append([]byte(item.value), output[item.end:]...)...)
	}
	return output, targetURL, sourceDB, nil
}

func canonicalThreadConfigTargetRedisURL(source string, targetDB int) (string, int, error) {
	sourceOptions, err := redis.ParseURL(source)
	if err != nil || sourceOptions == nil || sourceOptions.DB < 0 || targetDB < 0 || sourceOptions.DB == targetDB {
		return "", -1, errors.New("invalid Redis route")
	}
	parsed, err := url.Parse(source)
	if err != nil || parsed.Opaque != "" || (parsed.Scheme != "redis" && parsed.Scheme != "rediss") || parsed.Host == "" {
		return "", -1, errors.New("invalid Redis URL")
	}
	parsed.Path = "/" + strconv.Itoa(targetDB)
	parsed.RawPath = ""
	target := parsed.String()
	targetOptions, err := redis.ParseURL(target)
	if err != nil || targetOptions == nil || targetOptions.DB != targetDB {
		return "", -1, errors.New("invalid target Redis URL")
	}
	expected := *sourceOptions
	expected.DB = targetDB
	if !reflect.DeepEqual(&expected, targetOptions) {
		return "", -1, errors.New("Redis URL changed fields other than DB")
	}
	return target, sourceOptions.DB, nil
}

func canonicalThreadConfigDocumentMapping(root *yaml.Node) *yaml.Node {
	if root != nil && root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
		root = root.Content[0]
	}
	if root == nil || root.Kind != yaml.MappingNode {
		return nil
	}
	return root
}

func canonicalThreadConfigUniqueMappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool) {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil, false
	}
	var found *yaml.Node
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value != key {
			continue
		}
		if found != nil {
			return nil, false
		}
		found = mapping.Content[index+1]
	}
	return found, found != nil
}

func canonicalThreadConfigHasAlias(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" || node.Alias != nil {
		return true
	}
	for _, child := range node.Content {
		if canonicalThreadConfigHasAlias(child) {
			return true
		}
	}
	return false
}

func canonicalThreadConfigScalarSpan(data []byte, node *yaml.Node) ([2]int, error) {
	var zero [2]int
	if node == nil || node.Line <= 0 || node.Column <= 0 {
		return zero, errors.New("scalar position is invalid")
	}
	lineStart := 0
	for line := 1; line < node.Line; line++ {
		next := bytes.IndexByte(data[lineStart:], '\n')
		if next < 0 {
			return zero, errors.New("scalar line is missing")
		}
		lineStart += next + 1
	}
	lineEnd := len(data)
	if next := bytes.IndexByte(data[lineStart:], '\n'); next >= 0 {
		lineEnd = lineStart + next
	}
	contentEnd := lineEnd
	if contentEnd > lineStart && data[contentEnd-1] == '\r' {
		contentEnd--
	}
	columnOffset, err := canonicalThreadConfigRuneColumnOffset(data[lineStart:contentEnd], node.Column)
	if err != nil {
		return zero, err
	}
	start := lineStart + columnOffset
	if start >= contentEnd {
		return zero, errors.New("scalar start is outside line")
	}
	end, err := canonicalThreadConfigScalarEnd(data, start, contentEnd)
	if err != nil {
		return zero, err
	}
	return [2]int{start, end}, nil
}

func canonicalThreadConfigRuneColumnOffset(line []byte, column int) (int, error) {
	want := column - 1
	index := 0
	for count := 0; count < want; count++ {
		if index >= len(line) {
			return 0, errors.New("scalar column is outside line")
		}
		_, size := utf8.DecodeRune(line[index:])
		if size == 0 {
			return 0, errors.New("scalar column is invalid")
		}
		index += size
	}
	return index, nil
}

func canonicalThreadConfigScalarEnd(data []byte, start, lineEnd int) (int, error) {
	switch data[start] {
	case '"':
		escaped := false
		for index := start + 1; index < lineEnd; index++ {
			if escaped {
				escaped = false
				continue
			}
			if data[index] == '\\' {
				escaped = true
				continue
			}
			if data[index] == '"' {
				return canonicalThreadConfigValidateScalarTail(data, index+1, lineEnd)
			}
		}
	case '\'':
		for index := start + 1; index < lineEnd; index++ {
			if data[index] != '\'' {
				continue
			}
			if index+1 < lineEnd && data[index+1] == '\'' {
				index++
				continue
			}
			return canonicalThreadConfigValidateScalarTail(data, index+1, lineEnd)
		}
	default:
		end := lineEnd
		for index := start; index < lineEnd; index++ {
			if data[index] == '#' && index > start && (data[index-1] == ' ' || data[index-1] == '\t') {
				end = index
				break
			}
		}
		for end > start && (data[end-1] == ' ' || data[end-1] == '\t') {
			end--
		}
		if end == start || bytes.IndexFunc(data[start:end], unicode.IsSpace) >= 0 {
			return 0, errors.New("plain route scalar is empty or multiline")
		}
		return end, nil
	}
	return 0, errors.New("quoted route scalar is not closed on one line")
}

func canonicalThreadConfigValidateScalarTail(data []byte, end, lineEnd int) (int, error) {
	for index := end; index < lineEnd; index++ {
		if data[index] == ' ' || data[index] == '\t' {
			continue
		}
		if data[index] == '#' {
			return end, nil
		}
		return 0, errors.New("route scalar has trailing YAML content")
	}
	return end, nil
}

func readCanonicalThreadConfigSource(path string) ([]byte, os.FileInfo, error) {
	absolute, err := canonicalThreadConfigCanonicalFile(path)
	if err != nil {
		return nil, nil, err
	}
	before, err := os.Lstat(absolute)
	if err != nil || before.Size() <= 0 || before.Size() > canonicalThreadConfigMaxBytes {
		return nil, nil, errors.New("invalid config source")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, canonicalThreadConfigMaxBytes+1))
	closeErr := file.Close()
	after, statErr := os.Lstat(absolute)
	if readErr != nil || closeErr != nil || statErr != nil || !os.SameFile(before, after) || before.Size() != after.Size() || int64(len(data)) != before.Size() || !utf8.Valid(data) {
		return nil, nil, errors.New("config source changed during read")
	}
	return data, before, nil
}

func verifyCanonicalThreadConfigSourceUnchanged(path string, before os.FileInfo, expected []byte) error {
	absolute, err := canonicalThreadConfigCanonicalFile(path)
	if err != nil {
		return err
	}
	after, err := os.Lstat(absolute)
	if err != nil || !os.SameFile(before, after) || after.Size() != int64(len(expected)) {
		return errors.New("config source identity changed")
	}
	data, err := os.ReadFile(absolute)
	if err != nil || !bytes.Equal(data, expected) {
		return errors.New("config source bytes changed")
	}
	return nil
}

func canonicalThreadConfigCanonicalFile(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("invalid path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("path is not a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil || !canonicalThreadConfigSamePath(absolute, resolved) {
		return "", errors.New("path is not canonical")
	}
	return absolute, nil
}

func resolveCanonicalThreadConfigOutput(path string) (string, error) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", errors.New("invalid output path")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if _, err := os.Lstat(absolute); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("output is not fresh")
	}
	parent := filepath.Dir(absolute)
	info, err := os.Lstat(parent)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("output parent is unsafe")
	}
	resolved, err := filepath.EvalSymlinks(parent)
	if err != nil || !canonicalThreadConfigSamePath(parent, resolved) || (runtime.GOOS != "windows" && (info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o700 != 0o700)) {
		return "", errors.New("output parent is not canonical owner-only")
	}
	return absolute, nil
}

func publishCanonicalThreadConfigCandidate(target string, data []byte) error {
	parent := filepath.Dir(target)
	temporary, err := os.CreateTemp(parent, ".rencrow-thread-config-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	published := false
	defer func() {
		_ = temporary.Close()
		if !published {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	written, err := temporary.Write(data)
	if err != nil || written != len(data) || temporary.Sync() != nil || temporary.Close() != nil {
		return errors.New("candidate write failed")
	}
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("candidate target is not fresh")
	}
	if err := os.Link(temporaryPath, target); err != nil {
		return err
	}
	if err := os.Remove(temporaryPath); err != nil {
		return err
	}
	published = true
	info, err := os.Lstat(target)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("published candidate is unsafe")
	}
	if runtime.GOOS != "windows" {
		directory, err := os.Open(parent)
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil || closeErr != nil {
			return errors.New("candidate directory sync failed")
		}
	}
	return nil
}

func verifyCanonicalThreadConfigCandidate(sourcePath, targetPath, targetURL, targetCollection string, expectedBytes []byte) error {
	actual, err := os.ReadFile(targetPath)
	if err != nil || !bytes.Equal(actual, expectedBytes) {
		return errors.New("candidate bytes do not match")
	}
	sourceConfig, err := adapterconfig.LoadConfig(sourcePath)
	if err != nil {
		return err
	}
	candidateConfig, err := adapterconfig.LoadConfig(targetPath)
	if err != nil {
		return err
	}
	expected := *sourceConfig
	expected.Conversation.RedisURL = targetURL
	expected.Conversation.VectorCollection = targetCollection
	if !reflect.DeepEqual(&expected, candidateConfig) {
		return errors.New("candidate changed config semantics outside canonical route fields")
	}
	return nil
}

func blockCanonicalThreadConfigCandidate(receipt canonicalThreadConfigCandidateReceipt, code string) (canonicalThreadConfigCandidateReceipt, error) {
	receipt.Status = canonicalThreadConfigCandidateBlocked
	receipt.ErrorCode = code
	receipt.OnlyCanonicalRouteFieldsChanged = false
	sealed, err := sealCanonicalThreadConfigCandidate(receipt)
	if err != nil {
		return receipt, errors.New("canonical config candidate blocked: receipt_invalid")
	}
	return sealed, fmt.Errorf("canonical config candidate blocked: %s", code)
}

func sealCanonicalThreadConfigCandidate(receipt canonicalThreadConfigCandidateReceipt) (canonicalThreadConfigCandidateReceipt, error) {
	receipt.ReceiptSHA256 = ""
	hash, err := receipt.computeSHA256()
	if err != nil {
		return receipt, err
	}
	receipt.ReceiptSHA256 = hash
	if err := receipt.validate(); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func validCanonicalThreadConfigCollection(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 255 {
		return false
	}
	for index, character := range value {
		if character > unicode.MaxASCII {
			return false
		}
		if index == 0 {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9')) {
				return false
			}
			continue
		}
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
			return false
		}
	}
	return true
}

func canonicalThreadConfigSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validCanonicalThreadConfigSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func canonicalThreadConfigSamePath(left, right string) bool {
	left, right = filepath.Clean(left), filepath.Clean(right)
	return left == right || (filepath.VolumeName(left) != "" && strings.EqualFold(left, right))
}
