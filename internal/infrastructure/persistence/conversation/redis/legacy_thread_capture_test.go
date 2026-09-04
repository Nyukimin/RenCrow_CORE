package redisstore

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/threadmigration"
)

type fakeLegacyRedisScanPage struct {
	keys   []string
	cursor uint64
	err    error
}

type fakeLegacyRedisReadResult struct {
	value  []byte
	expiry int64
	err    error
}

type fakeLegacyRedisCaptureSource struct {
	scanPages map[string][]fakeLegacyRedisScanPage
	scanIndex map[string]int
	reads     map[string]fakeLegacyRedisReadResult
	scanCalls []fakeLegacyRedisScanCall
	readCalls []string
	onRead    func(string)
}

type fakeLegacyRedisScanCall struct {
	cursor uint64
	match  string
	count  int64
}

func (source *fakeLegacyRedisCaptureSource) Scan(_ context.Context, cursor uint64, match string, count int64) ([]string, uint64, error) {
	source.scanCalls = append(source.scanCalls, fakeLegacyRedisScanCall{cursor: cursor, match: match, count: count})
	index := source.scanIndex[match]
	pages := source.scanPages[match]
	if index >= len(pages) {
		return nil, 0, errors.New("unexpected scan call")
	}
	source.scanIndex[match] = index + 1
	page := pages[index]
	return append([]string(nil), page.keys...), page.cursor, page.err
}

func (source *fakeLegacyRedisCaptureSource) Read(_ context.Context, key string) ([]byte, int64, error) {
	source.readCalls = append(source.readCalls, key)
	if source.onRead != nil {
		source.onRead(key)
	}
	result, ok := source.reads[key]
	if !ok {
		return nil, 0, errors.New("unexpected read call")
	}
	return result.value, result.expiry, result.err
}

func TestCaptureLegacyThreadEntriesCollectsSortedDedupedAbsoluteExpiry(t *testing.T) {
	sessionAValue := []byte(`{"session_id":"a"}`)
	source := &fakeLegacyRedisCaptureSource{
		scanPages: map[string][]fakeLegacyRedisScanPage{
			"sess:*": {
				{keys: []string{"sess:b", "sess:a"}, cursor: 11},
				{keys: []string{"sess:a"}, cursor: 0},
			},
			"thread:*": {
				{keys: []string{"thread:2"}, cursor: 22},
				{keys: []string{"thread:1", "thread:2"}, cursor: 0},
			},
		},
		scanIndex: make(map[string]int),
		reads: map[string]fakeLegacyRedisReadResult{
			"sess:a":   {value: sessionAValue, expiry: 1800000000100},
			"sess:b":   {value: []byte(`{"session_id":"b"}`), expiry: 1800000000200},
			"thread:1": {value: []byte(`{"thread_id":1,"session_id":"a"}`), expiry: 1800000000300},
			"thread:2": {value: []byte(`{"thread_id":2,"session_id":"b"}`), expiry: 1800000000400},
		},
	}

	entries, err := captureLegacyThreadEntries(context.Background(), source)
	if err != nil {
		t.Fatalf("captureLegacyThreadEntries() error = %v", err)
	}
	wantKeys := []string{"sess:a", "sess:b", "thread:1", "thread:2"}
	wantExpiries := []int64{1800000000100, 1800000000200, 1800000000300, 1800000000400}
	if len(entries) != len(wantKeys) {
		t.Fatalf("entry count = %d, want %d", len(entries), len(wantKeys))
	}
	for index, entry := range entries {
		if entry.Key != wantKeys[index] || entry.ExpireAtUnixMilli != wantExpiries[index] {
			t.Fatalf("entry %d = %#v, want key=%q expiry=%d", index, entry, wantKeys[index], wantExpiries[index])
		}
	}
	if !reflect.DeepEqual(source.readCalls, wantKeys) {
		t.Fatalf("read order = %#v, want %#v", source.readCalls, wantKeys)
	}
	for _, call := range source.scanCalls {
		if call.count != 256 {
			t.Fatalf("scan count = %d, want 256", call.count)
		}
	}
	if got := []string{source.scanCalls[0].match, source.scanCalls[2].match}; !reflect.DeepEqual(got, []string{"sess:*", "thread:*"}) {
		t.Fatalf("scan patterns = %#v", got)
	}

	sessionAValue[0] = '['
	if !bytes.Equal(entries[0].Value, []byte(`{"session_id":"a"}`)) {
		t.Fatal("returned value aliases source bytes")
	}
}

func TestCaptureLegacyThreadEntriesAcceptsEmptyProgressPage(t *testing.T) {
	source := &fakeLegacyRedisCaptureSource{
		scanPages: map[string][]fakeLegacyRedisScanPage{
			"sess:*":   {{keys: nil, cursor: 1}, {keys: []string{"sess:one"}, cursor: 0}},
			"thread:*": {{keys: nil, cursor: 0}},
		},
		scanIndex: make(map[string]int),
		reads: map[string]fakeLegacyRedisReadResult{
			"sess:one": {value: []byte(`{"session_id":"one"}`), expiry: 1800000000001},
		},
	}
	entries, err := captureLegacyThreadEntries(context.Background(), source)
	if err != nil {
		t.Fatalf("captureLegacyThreadEntries() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Key != "sess:one" || entries[0].ExpireAtUnixMilli != 1800000000001 {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestCaptureLegacyThreadEntriesRejectsScanFailuresAndCursorNonProgress(t *testing.T) {
	cases := []struct {
		name  string
		pages []fakeLegacyRedisScanPage
	}{
		{name: "scan error", pages: []fakeLegacyRedisScanPage{{err: errors.New("backend secret")}}},
		{name: "repeated cursor", pages: []fakeLegacyRedisScanPage{{cursor: 7}, {cursor: 7}}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := &fakeLegacyRedisCaptureSource{
				scanPages: map[string][]fakeLegacyRedisScanPage{"sess:*": testCase.pages, "thread:*": {{cursor: 0}}},
				scanIndex: make(map[string]int),
				reads:     map[string]fakeLegacyRedisReadResult{"sess:a": {value: []byte(`{"session_id":"a"}`), expiry: 1800000000001}},
			}
			entries, err := captureLegacyThreadEntries(context.Background(), source)
			if err == nil || entries != nil {
				t.Fatalf("entries=%#v err=%v, want bounded failure with no partial snapshot", entries, err)
			}
			if strings.Contains(err.Error(), "backend secret") {
				t.Fatalf("source error leaked: %v", err)
			}
		})
	}
}

func TestCaptureLegacyThreadEntriesRejectsReadAndExpiryFailures(t *testing.T) {
	cases := []struct {
		name    string
		expiry  int64
		readErr error
	}{
		{name: "read error", expiry: 1800000000001, readErr: errors.New("backend secret")},
		{name: "missing sentinel", expiry: -2},
		{name: "persistent sentinel", expiry: -1},
		{name: "zero expiry", expiry: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := &fakeLegacyRedisCaptureSource{
				scanPages: map[string][]fakeLegacyRedisScanPage{
					"sess:*":   {{keys: []string{"sess:a"}, cursor: 0}},
					"thread:*": {{cursor: 0}},
				},
				scanIndex: make(map[string]int),
				reads: map[string]fakeLegacyRedisReadResult{
					"sess:a": {value: []byte(`{"session_id":"a"}`), expiry: testCase.expiry, err: testCase.readErr},
				},
			}
			entries, err := captureLegacyThreadEntries(context.Background(), source)
			if err == nil || entries != nil {
				t.Fatalf("entries=%#v err=%v, want bounded failure with no partial snapshot", entries, err)
			}
			if strings.Contains(err.Error(), "backend secret") {
				t.Fatalf("read error leaked: %v", err)
			}
		})
	}
}

func TestCaptureLegacyThreadEntriesRejectsMalformedEntryThroughExternalValidator(t *testing.T) {
	source := &fakeLegacyRedisCaptureSource{
		scanPages: map[string][]fakeLegacyRedisScanPage{
			"sess:*":   {{keys: []string{"sess:a"}, cursor: 0}},
			"thread:*": {{cursor: 0}},
		},
		scanIndex: make(map[string]int),
		reads: map[string]fakeLegacyRedisReadResult{
			"sess:a": {value: []byte(`[]`), expiry: 1800000000001},
		},
	}
	entries, err := captureLegacyThreadEntries(context.Background(), source)
	if err == nil || entries != nil {
		t.Fatalf("entries=%#v err=%v, want validator rejection and no partial snapshot", entries, err)
	}
}

func TestCaptureLegacyThreadEntriesRejectsNilOrCanceledContext(t *testing.T) {
	if entries, err := captureLegacyThreadEntries(nil, &fakeLegacyRedisCaptureSource{}); err == nil || entries != nil {
		t.Fatalf("nil context entries=%#v err=%v, want failure", entries, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	source := &fakeLegacyRedisCaptureSource{
		scanPages: map[string][]fakeLegacyRedisScanPage{"sess:*": {{cursor: 0}}, "thread:*": {{cursor: 0}}},
		scanIndex: make(map[string]int),
	}
	entries, err := captureLegacyThreadEntries(ctx, source)
	if err == nil || entries != nil {
		t.Fatalf("canceled context entries=%#v err=%v, want failure", entries, err)
	}
	if len(source.scanCalls) != 0 {
		t.Fatalf("canceled context made scan calls: %#v", source.scanCalls)
	}

	if entries, err := captureLegacyThreadEntries(context.Background(), nil); err == nil || entries != nil {
		t.Fatalf("nil source entries=%#v err=%v, want failure", entries, err)
	}

	var typedNilSource *fakeLegacyRedisCaptureSource
	if entries, err := captureLegacyThreadEntries(context.Background(), typedNilSource); err == nil || entries != nil {
		t.Fatalf("typed-nil source entries=%#v err=%v, want failure", entries, err)
	}
}

func TestCaptureLegacyThreadEntriesRejectsKeyLimitWithoutReads(t *testing.T) {
	keys := make([]string, threadmigration.RedisPreparationMaxEntries+1)
	for index := range keys {
		keys[index] = "sess:key-" + strings.Repeat("x", 8) + string(rune('a'+index%26)) + strconv.Itoa(index)
	}
	source := &fakeLegacyRedisCaptureSource{
		scanPages: map[string][]fakeLegacyRedisScanPage{
			"sess:*":   {{keys: keys, cursor: 0}},
			"thread:*": {{cursor: 0}},
		},
		scanIndex: make(map[string]int),
	}
	entries, err := captureLegacyThreadEntries(context.Background(), source)
	if err == nil || entries != nil {
		t.Fatalf("entries=%#v err=%v, want key limit rejection", entries, err)
	}
	if len(source.readCalls) != 0 {
		t.Fatalf("key limit caused reads: %#v", source.readCalls)
	}
}

func TestCaptureLegacyThreadEntriesRejectsCancellationAfterRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &fakeLegacyRedisCaptureSource{
		scanPages: map[string][]fakeLegacyRedisScanPage{
			"sess:*":   {{keys: []string{"sess:a"}, cursor: 0}},
			"thread:*": {{cursor: 0}},
		},
		scanIndex: make(map[string]int),
		reads: map[string]fakeLegacyRedisReadResult{
			"sess:a": {value: []byte(`{"session_id":"a"}`), expiry: 1800000000001},
		},
		onRead: func(string) { cancel() },
	}
	entries, err := captureLegacyThreadEntries(ctx, source)
	if err == nil || entries != nil {
		t.Fatalf("entries=%#v err=%v, want cancellation failure", entries, err)
	}
}

var _ legacyRedisCaptureSource = (*fakeLegacyRedisCaptureSource)(nil)
