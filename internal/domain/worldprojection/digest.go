package worldprojection

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
)

// PayloadDigest is the projection digest WORLD records for a payload. It is
// SHA-256 over deterministic JSON with sorted object keys; it is not the hash
// of the exact wire bytes. CORE must use this same function for its outbox
// record so an accepted head can be compared to the submitted revision.
func PayloadDigest(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return "", fmt.Errorf("%w: payload is not valid JSON", ErrInvalidProjection)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("%w: payload has trailing data", ErrInvalidProjection)
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, value); err != nil {
		return "", err
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:]), nil
}

func writeCanonical(buf *bytes.Buffer, value any) error {
	switch v := value.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if v {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		raw, err := json.Marshal(v)
		if err != nil {
			return err
		}
		buf.Write(raw)
	case json.Number:
		// The accepted projection schemas contain integer revisions only.
		if _, err := strconv.ParseUint(v.String(), 10, 64); err != nil {
			return fmt.Errorf("%w: non-integer JSON number", ErrInvalidProjection)
		}
		buf.WriteString(v.String())
	case []any:
		buf.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			raw, err := json.Marshal(key)
			if err != nil {
				return err
			}
			buf.Write(raw)
			buf.WriteByte(':')
			if err := writeCanonical(buf, v[key]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("%w: unsupported JSON value", ErrInvalidProjection)
	}
	return nil
}
