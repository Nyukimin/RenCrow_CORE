package delegation

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
)

// The log of the delegation follows docs/10_ログ仕様.md: one JSON object per
// line, the required fields, CORE's own correlation IDs, and the IDs that
// RenCrow_Harness issued only as {owner, id} references. The Harness prompt,
// Model input and output, Tool results, keys and OriginProof MACs are never
// logged; only reference IDs, status values, codes and counts are.

// Event names of the delegation (docs/10_ログ仕様.md, RenCrow_Harness委譲の相関).
const (
	eventClientStarted     = "native_harness.client.started"
	eventClientStartFailed = "native_harness.client.start_failed"
	eventClientEnded       = "native_harness.client.ended"
	eventClientStopped     = "native_harness.client.stopped"
	eventClientStderr      = "native_harness.client.stderr"
	eventAdmissionRefused  = "native_harness.admission.refused"
	eventDelegateStarted   = "native_harness.delegate.started"
	eventDelegateAccepted  = "native_harness.delegate.accepted"
	eventDelegateFinished  = "native_harness.delegate.finished"
	eventDelegateUnknown   = "native_harness.delegate.outcome_unknown"
	eventHarnessEvent      = "native_harness.event"

	maxLogFieldBytes = 512
)

// harnessRef is an ID that RenCrow_Harness issued, with its owner.
func harnessRef(id string) agent.ExternalRef {
	return agent.ExternalRef{Owner: agent.HarnessOwner, ID: id}
}

type logger struct {
	sink func(line string)
	now  func() time.Time
}

func newLogger(sink func(string), now func() time.Time) *logger {
	if sink == nil {
		sink = func(line string) { log.Print(line) }
	}
	if now == nil {
		now = time.Now
	}
	return &logger{sink: sink, now: now}
}

// emit writes one event. fields are added to the required fields; a field that
// cannot be encoded is dropped rather than failing the delegation.
func (l *logger) emit(level, event string, fields map[string]any) {
	entry := map[string]any{
		"schema_version": 1,
		"ts":             l.now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"level":          level,
		"event":          event,
		"module":         "RenCrow_CORE",
		"tags":           []string{"native_harness"},
	}
	for key, value := range fields {
		entry[key] = value
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		raw, _ = json.Marshal(map[string]any{
			"schema_version": 1, "ts": l.now().UTC().Format("2006-01-02T15:04:05.000Z"), "level": "error",
			"event": event, "module": "RenCrow_CORE", "tags": []string{"native_harness"}, "log_error": "fields not encodable",
		})
	}
	l.sink(string(raw))
}

// stderrWriter returns the writer that receives the child's stderr. The Harness
// writes only diagnostics without request content, keys or paths there; CORE
// still bounds every line and records it as a warning of the client.
func (l *logger) stderrWriter() io.Writer { return &lineWriter{log: l} }

type lineWriter struct {
	log *logger
	buf bytes.Buffer
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadBytes('\n')
		if err != nil {
			w.buf.Write(line) // keep the partial line for the next write
			break
		}
		w.record(line)
	}
	if w.buf.Len() > 4*maxLogFieldBytes { // a line that never ends is bounded too
		w.record(w.buf.Bytes())
		w.buf.Reset()
	}
	return len(p), nil
}

func (w *lineWriter) record(line []byte) {
	text := string(bytes.TrimRight(line, "\r\n"))
	if text == "" {
		return
	}
	w.log.emit("warn", eventClientStderr, map[string]any{"line": truncateUTF8(maskPaths(text), maxLogFieldBytes)})
}

// pathToken matches a file path in a diagnostic line: a POSIX absolute path or a
// Windows drive path that starts at the line, after white space, a quote, a
// bracket or "=".
var pathToken = regexp.MustCompile(`(^|[\s"'(=])((?:/[^\s"'),;]+)+|[A-Za-z]:\\[^\s"'),;]+)`)

// maskPaths replaces every path of a diagnostic line. The Harness states that its
// stderr carries no path; a runtime fault (a panic, a failed open) can still print
// one, and CORE's log must not hold a host's directory layout.
func maskPaths(line string) string { return pathToken.ReplaceAllString(line, "${1}<path>") }

func truncateUTF8(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
