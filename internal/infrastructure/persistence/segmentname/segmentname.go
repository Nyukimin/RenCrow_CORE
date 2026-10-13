// Package segmentname is the naming rule of a store's segment directories: one
// directory per UTC day and sequence, YYYYMMDD-NN, whose lexical order is the
// order the segments were made in.
//
// It is the pure part only: names, the UTC day, and listing a directory for
// them. Which files a segment holds, how a segment is frozen and how sizes are
// checked belong to the store that owns the segments (the Task store today), so
// they are not here. A name written by any of these functions is a persisted
// contract: an older binary must find the directories a newer one made.
package segmentname

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"time"
)

var pattern = regexp.MustCompile(`^[0-9]{8}-[0-9]{2}$`)

// IsName reports whether name has the shape of a segment directory name.
func IsName(name string) bool { return pattern.MatchString(name) }

// Day is the UTC day (YYYYMMDD) of a segment name. name must satisfy IsName.
func Day(name string) string { return name[:8] }

// Sequence is the NN of a segment name. name must satisfy IsName.
func Sequence(name string) int {
	n, _ := strconv.Atoi(name[9:])
	return n
}

// Format makes the name of segment sequence of day (YYYYMMDD). A sequence over
// 99 does not satisfy IsName; the caller bounds it.
func Format(day string, sequence int) string {
	return fmt.Sprintf("%s-%02d", day, sequence)
}

// UTCDay is the YYYYMMDD of t in UTC.
func UTCDay(t time.Time) string { return t.UTC().Format("20060102") }

// ListDirs returns the names of the segment directories directly under base, in
// ordinal (= lexical) order. Entries that are not segment directories are
// ignored, and a base that does not exist holds none.
func ListDirs(base string) ([]string, error) {
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && IsName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}
