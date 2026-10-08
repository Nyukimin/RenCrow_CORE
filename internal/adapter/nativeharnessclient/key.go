package nativeharnessclient

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

const (
	keyBytes       = 32
	keyHexLength   = keyBytes * 2
	keyFileMaxSize = keyHexLength + 1 // 64 hex characters and one optional LF
	redactedKey    = "[REDACTED]"
)

var (
	// ErrKeyFormat reports a key file that is not 64 lowercase hex characters
	// followed by at most one LF. The error never contains the file content.
	ErrKeyFormat = errors.New("origin key file must be 64 lowercase hex characters and at most one trailing LF")
	// ErrKeyPath reports a key file path that is not absolute.
	ErrKeyPath = errors.New("origin key file path must be absolute")
	// ErrKeyFixture reports a key file that holds the published synthetic
	// test-vector key. That key is public, so it must never sign on a real
	// machine. The error never contains the key.
	ErrKeyFixture = errors.New("origin key file holds a published fixture key; create a private key")
)

// fixtureKeyDigest is the SHA-256 of the 32 key bytes of the published
// synthetic test-vector key (testdata/synthetic_origin_key.hex). Only the
// digest is kept here, never the key. A test fails if the two drift apart.
const fixtureKeyDigest = "630dcd2966c4336691125448bbb25b4ff412a49c732db2c8abc1b8581bd710dd"

// Key is the 32 byte HMAC-SHA256 key of the OriginProof issuer. Every
// formatting, text and log path prints a fixed marker instead of the bytes.
type Key struct{ bytes [keyBytes]byte }

// String returns a fixed marker, never the key.
func (Key) String() string { return redactedKey }

// GoString returns a fixed marker, never the key.
func (Key) GoString() string { return redactedKey }

// Format prints the fixed marker for every verb.
func (Key) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, redactedKey) }

// MarshalText keeps encoders (JSON, YAML, ...) from serializing the key.
func (Key) MarshalText() ([]byte, error) { return []byte(redactedKey), nil }

// LogValue keeps structured logging from recording the key.
func (Key) LogValue() slog.Value { return slog.StringValue(redactedKey) }

func (key Key) isZero() bool { return key.bytes == [keyBytes]byte{} }

// ParseKey parses the contents of a key file: exactly 64 lowercase hex
// characters and an optional single trailing LF. BOM, CRLF, other whitespace,
// uppercase, raw binary and base64 are rejected. It returns the zero Key on
// failure and never includes the input in the error.
func ParseKey(raw []byte) (Key, error) {
	text := raw
	if len(text) == keyFileMaxSize && text[keyHexLength] == '\n' {
		text = text[:keyHexLength]
	}
	if len(text) != keyHexLength || !isLowerHex(text) {
		return Key{}, ErrKeyFormat
	}
	var key Key
	for i := range key.bytes {
		key.bytes[i] = hexNibble(text[2*i])<<4 | hexNibble(text[2*i+1])
	}
	return key, nil
}

// LoadKeyFile reads the key file at an absolute path with ReadKey. It does not
// check the file's permission or ACL; the CORE configuration check opens the
// key file through a permission-checked handle and calls ReadKey on it.
func LoadKeyFile(path string) (Key, error) {
	if !filepath.IsAbs(path) {
		return Key{}, ErrKeyPath
	}
	file, err := os.Open(path)
	if err != nil {
		return Key{}, fmt.Errorf("open origin key file: %w", err)
	}
	defer file.Close()
	return ReadKey(file)
}

// ReadKey reads the contents of an open key file. At most one byte more than
// the longest valid file is read, so an oversized file is rejected without
// being read in full. A file that holds the published fixture key is rejected
// with ErrKeyFixture; ParseKey, which only parses bytes, still accepts it so
// the test vectors can be reproduced. ReadKey leaves permission and ACL checks
// to whoever opened the reader, so a caller that checked the file reads the
// same handle it checked.
func ReadKey(reader io.Reader) (Key, error) {
	data, err := io.ReadAll(io.LimitReader(reader, keyFileMaxSize+1))
	if err != nil {
		return Key{}, fmt.Errorf("read origin key file: %w", err)
	}
	key, err := ParseKey(data)
	if err != nil {
		return Key{}, err
	}
	if key.isFixture() {
		return Key{}, ErrKeyFixture
	}
	return key, nil
}

// isFixture reports whether the key is the published synthetic test-vector
// key, compared by the digest of the decoded bytes so a trailing LF in the
// file cannot hide it.
func (key Key) isFixture() bool {
	sum := sha256.Sum256(key.bytes[:])
	return hex.EncodeToString(sum[:]) == fixtureKeyDigest
}

// isLowerHex reports whether every element is in [0-9a-f]. It accepts a
// string or a byte slice so key bytes need not be copied into a string.
func isLowerHex[T ~string | ~[]byte](value T) bool {
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// hexNibble decodes one character that isLowerHex already accepted.
func hexNibble(c byte) byte {
	if c <= '9' {
		return c - '0'
	}
	return c - 'a' + 10
}
