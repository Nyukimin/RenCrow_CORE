package task

import (
	"bytes"
	"sort"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestParseCanonicalKeyAcceptsOnlyTheCanonicalSpelling(t *testing.T) {
	task := string(modulecore.NewTaskID())
	key, err := parseCanonicalKey(task, taskKeyPrefix)
	if err != nil {
		t.Fatalf("canonical TaskID rejected: %v", err)
	}
	if got := key.id(taskKeyPrefix); got != task {
		t.Fatalf("key.id() = %q, want %q", got, task)
	}
	run := string(modulecore.NewRunID())
	if _, err := parseCanonicalKey(run, runKeyPrefix); err != nil {
		t.Fatalf("canonical RunID rejected: %v", err)
	}

	suffix := strings.TrimPrefix(task, taskKeyPrefix)
	for name, raw := range map[string]string{
		"empty":          "",
		"wrong prefix":   runKeyPrefix + suffix,
		"upper case":     taskKeyPrefix + strings.ToUpper(suffix),
		"braced":         taskKeyPrefix + "{" + suffix + "}",
		"urn":            taskKeyPrefix + "urn:uuid:" + suffix,
		"no hyphens":     taskKeyPrefix + strings.ReplaceAll(suffix, "-", ""),
		"trailing space": task + " ",
		"short":          task[:len(task)-1],
		"long":           task + "0",
		"uuid v4":        taskKeyPrefix + "6ba7b810-9dad-41d1-80b4-00c04fd430c8",
	} {
		if _, err := parseCanonicalKey(raw, taskKeyPrefix); err == nil {
			t.Errorf("%s: %q was accepted as a canonical key", name, raw)
		}
	}
}

func TestKeyOrderMatchesTheOrderOfTheCanonicalIDStrings(t *testing.T) {
	// Index sorting compares key16 bytes where the previous implementation
	// compared the ID strings; they must agree for every canonical ID.
	ids := make([]string, 2000)
	for i := range ids {
		ids[i] = string(modulecore.NewTaskID())
	}
	byString := append([]string(nil), ids...)
	sort.Strings(byString)
	byKey := append([]string(nil), ids...)
	sort.Slice(byKey, func(i, j int) bool {
		a, _ := parseCanonicalKey(byKey[i], taskKeyPrefix)
		b, _ := parseCanonicalKey(byKey[j], taskKeyPrefix)
		return bytes.Compare(a[:], b[:]) < 0
	})
	for i := range byString {
		if byString[i] != byKey[i] {
			t.Fatalf("order differs at %d: string %q key %q", i, byString[i], byKey[i])
		}
	}
}

func TestStampOrdersLikeTimeIncludingTheZeroTime(t *testing.T) {
	base := time.Date(2026, 10, 10, 12, 34, 56, 789, time.UTC)
	jst := base.In(time.FixedZone("JST", 9*3600))
	if toStamp(base).compare(toStamp(jst)) != 0 {
		t.Fatal("the same instant in another zone must compare equal")
	}
	instants := []time.Time{
		{},
		time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1969, 12, 31, 23, 59, 59, 999999999, time.UTC),
		time.Unix(0, 0),
		base,
		base.Add(time.Nanosecond),
		base.Add(time.Hour),
		time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	for i, left := range instants {
		for j, right := range instants {
			want := 0
			switch {
			case left.Before(right):
				want = -1
			case left.After(right):
				want = 1
			}
			if got := toStamp(left).compare(toStamp(right)); got != want {
				t.Errorf("compare(%d,%d) = %d, want %d", i, j, got, want)
			}
		}
	}
}

func TestInternTableReusesIDsAndFoldsCaseForLookup(t *testing.T) {
	var table internTable
	a, err := table.intern("Shiro")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := table.intern("shiro")
	c, _ := table.intern("Shiro")
	if a != c || a == b {
		t.Fatalf("ids a=%d b=%d c=%d", a, b, c)
	}
	if got := table.value(a); got != "Shiro" {
		t.Fatalf("value(a) = %q", got)
	}
	if id, ok := table.lookup("Shiro"); !ok || id != a {
		t.Fatalf("lookup(Shiro) = %d,%v", id, ok)
	}
	if _, ok := table.lookup("Kuro"); ok {
		t.Fatal("lookup(Kuro) found an id that was never interned")
	}
	folded := table.equalFold("SHIRO")
	if len(folded) != 2 {
		t.Fatalf("equalFold(SHIRO) = %v, want both spellings", folded)
	}
}
