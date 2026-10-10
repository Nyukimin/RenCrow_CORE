package task

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	taskKeyPrefix = "tsk_"
	runKeyPrefix  = "run_"

	canonicalUUIDLength = 36
)

// errNonCanonicalID reports an identifier that the canonical ID validators
// accept (uuid.Parse also takes upper case, braces, urn: and unhyphenated
// spellings) but that would not map to a single index key.
var errNonCanonicalID = errors.New("identifier is not in canonical form")

// key16 is the 16 raw bytes of a canonical TaskID/RunID UUID. Comparing keys
// byte-wise orders them exactly like the canonical lower-case ID strings
// ('0'-'9' sort before 'a'-'f' and the hyphens sit at fixed positions), so the
// index sorts on keys where the previous implementation sorted on strings.
type key16 [16]byte

// parseCanonicalKey converts prefix + lower-case hyphenated UUID (v5 or v7, the
// versions modulecore accepts) into a key. Any other spelling is rejected so two
// strings can never collapse into one key, and one ID is never reachable under
// a spelling the log does not contain.
func parseCanonicalKey(raw, prefix string) (key16, error) {
	var key key16
	if !strings.HasPrefix(raw, prefix) || len(raw) != len(prefix)+canonicalUUIDLength {
		return key, fmt.Errorf("%w: %q", errNonCanonicalID, raw)
	}
	text := raw[len(prefix):]
	out := 0
	for i := 0; i < canonicalUUIDLength; {
		switch i {
		case 8, 13, 18, 23:
			if text[i] != '-' {
				return key, fmt.Errorf("%w: %q", errNonCanonicalID, raw)
			}
			i++
			continue
		}
		hi, okHi := lowerHexValue(text[i])
		lo, okLo := lowerHexValue(text[i+1])
		if !okHi || !okLo {
			return key, fmt.Errorf("%w: %q", errNonCanonicalID, raw)
		}
		key[out] = hi<<4 | lo
		out++
		i += 2
	}
	if version := key[6] >> 4; version != 5 && version != 7 {
		return key, fmt.Errorf("%w: %q uses UUIDv%d", errNonCanonicalID, raw, version)
	}
	return key, nil
}

func lowerHexValue(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	}
	return 0, false
}

// id returns the canonical string for the key under prefix.
func (k key16) id(prefix string) string {
	const digits = "0123456789abcdef"
	buf := make([]byte, 0, len(prefix)+canonicalUUIDLength)
	buf = append(buf, prefix...)
	for i, b := range k {
		switch i {
		case 4, 6, 8, 10:
			buf = append(buf, '-')
		}
		buf = append(buf, digits[b>>4], digits[b&0x0f])
	}
	return string(buf)
}

// stamp is an instant as the index keeps it: Unix seconds plus nanoseconds. It
// orders and compares exactly like time.Time.Before / Equal for every instant,
// including the zero time (an unset notification timestamp is legal) and years
// outside what UnixNano can represent, so the index never has to reject or
// reorder a record that the previous implementation accepted.
type stamp struct {
	sec  int64
	nsec int32
}

func toStamp(value time.Time) stamp {
	return stamp{sec: value.Unix(), nsec: int32(value.Nanosecond())}
}

// compare returns -1, 0 or +1 as s is before, equal to or after other.
func (s stamp) compare(other stamp) int {
	switch {
	case s.sec < other.sec:
		return -1
	case s.sec > other.sec:
		return 1
	case s.nsec < other.nsec:
		return -1
	case s.nsec > other.nsec:
		return 1
	}
	return 0
}

// internTable maps repeated strings (assignee, module, title, enum values) to
// small ids so the pointer-free index records stay compact. Id 0 is the empty
// string. A table is mutated only while the index is mutated.
type internTable struct {
	ids  map[string]uint32
	vals []string
}

func (t *internTable) intern(value string) (uint32, error) {
	if value == "" {
		return 0, nil
	}
	if id, ok := t.ids[value]; ok {
		return id, nil
	}
	if t.ids == nil {
		t.ids = make(map[string]uint32)
		t.vals = []string{""}
	}
	if len(t.vals) >= 1<<32-1 {
		return 0, errors.New("intern table is full")
	}
	id := uint32(len(t.vals))
	t.ids[value] = id
	t.vals = append(t.vals, value)
	return id, nil
}

func (t *internTable) value(id uint32) string {
	if int(id) >= len(t.vals) {
		return ""
	}
	return t.vals[id]
}

func (t *internTable) lookup(value string) (uint32, bool) {
	if value == "" {
		return 0, true
	}
	id, ok := t.ids[value]
	return id, ok
}

// equalFold returns the ids of every interned value that strings.EqualFold
// matches, which is what the Assignee filter has always meant.
func (t *internTable) equalFold(value string) []uint32 {
	var matches []uint32
	if value == "" {
		return append(matches, 0)
	}
	for id := 1; id < len(t.vals); id++ {
		if strings.EqualFold(t.vals[id], value) {
			matches = append(matches, uint32(id))
		}
	}
	return matches
}
