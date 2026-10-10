package delegation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
)

func waitForLog(t *testing.T, sink *logSink, event string, count int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if entries := sink.events(t, event); len(entries) >= count {
			return entries
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("log event %s did not reach %d entries: %v", event, count, sink.all())
	return nil
}

func harnessEvent(thread, eventType string, seq int64, payload string) protocol.Event {
	return protocol.Event{
		EventID: harnessID("evt"), EventSeq: seq, ThreadID: thread, Type: eventType,
		RecordedAt: "2026-10-08T12:00:00Z", Payload: json.RawMessage(payload),
	}
}

func TestConfirmedHarnessEventsAreLoggedAsReferencesWithCOREIDsAndNoPayload(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onAwait = func(context.Context, string, client.AwaitOptions) (protocol.RunResult, error) {
			// While the delegation is waiting its Thread is correlated: the Harness
			// announces its events here.
			runID, taskID := c.run, c.task
			started := harnessEvent(c.thread, protocol.EventRunStarted, 5, `{"note":"`+secretFinal+`"}`)
			started.RunID, started.TaskID = &runID, &taskID
			c.push(started)
			c.push(harnessEvent(c.thread, protocol.EventModelRequested, 6, `{"prompt":"`+secretText+`"}`)) // not a recorded type
			code := "TOOL_FAILED"
			failed := harnessEvent(c.thread, protocol.EventActionCompleted, 7, `{"result":"`+secretFinal+`"}`)
			failed.Code = &code
			c.push(failed)
			c.push(harnessEvent(c.thread, protocol.EventRunTerminal, 8, `{"final":"`+secretFinal+`"}`))
			c.note <- client.Notification{Kind: client.NotificationProgressDelta, Delta: &protocol.ProgressDelta{RunID: runID, Text: secretText}}
			return c.runResult("completed", "passed"), nil
		}
		return c, nil
	}
	result, err, _ := delegate(t, d, secretText)
	if err != nil || !result.Accepted() {
		t.Fatalf("delegation: %+v %v", result, err)
	}
	events := waitForLog(t, d.log, eventHarnessEvent, 3)
	if len(events) != 3 {
		t.Fatalf("run.started, an Event with a code and run.terminal are recorded; model.requested and progress are not: got %d", len(events))
	}
	c := d.client(0)
	byType := map[string]map[string]any{}
	for _, entry := range events {
		byType[entry["event_type"].(string)] = entry
	}
	for _, name := range []string{protocol.EventRunStarted, protocol.EventActionCompleted, protocol.EventRunTerminal} {
		if byType[name] == nil {
			t.Fatalf("missing %s in %v", name, byType)
		}
	}
	started := byType[protocol.EventRunStarted]
	ref, _ := started["harness_event"].(map[string]any)
	if ref["owner"] != agent.HarnessOwner || ref["event_id"] == "" {
		t.Fatalf("an Event is quoted as {owner, event_id}: %+v", started["harness_event"])
	}
	thread, _ := started["harness_thread"].(map[string]any)
	run, _ := started["harness_run"].(map[string]any)
	task, _ := started["harness_task"].(map[string]any)
	if thread["owner"] != agent.HarnessOwner || thread["id"] != c.thread || run["id"] != c.run || task["id"] != c.task {
		t.Fatalf("Harness IDs are logged with their owner: %+v", started)
	}
	if started["attempt_id"] != string(d.rec.attemptID) || started["action_id"] != string(d.rec.actionID) {
		t.Fatalf("an Event of a correlated Thread is logged next to CORE's Action and Attempt: %+v", started)
	}
	if byType[protocol.EventActionCompleted]["code"] != "TOOL_FAILED" {
		t.Fatalf("an Event that carries a code is recorded with it: %+v", byType[protocol.EventActionCompleted])
	}
	for _, line := range d.log.all() {
		mustNotContain(t, "log", line, secretText, secretFinal)
	}
}

func TestAnEventOfAnUnknownThreadIsStillLoggedWithoutCOREIDs(t *testing.T) {
	d := newDeployment(t)
	ctx, input, _ := newTurn(t, "x")
	if err := d.runtime.AdmitNativeCoding(ctx, input); err != nil {
		t.Fatal(err)
	}
	c := d.client(0)
	c.push(harnessEvent(harnessID("thr"), protocol.EventRunTerminal, 1, `{}`))
	events := waitForLog(t, d.log, eventHarnessEvent, 1)
	if _, ok := events[0]["attempt_id"]; ok {
		t.Fatalf("an unrelated Thread must not be attributed to a delegation: %+v", events[0])
	}
}

func TestChildDiagnosticsAreLoggedWithoutPaths(t *testing.T) {
	sink := &logSink{}
	writer := newLogger(sink.write, nil).stderrWriter()
	lines := []string{
		"panic: open /srv/private/harness/config.json: permission denied\n",
		"fault at C:\\Users\\someone\\harness.json (code 5)\n",
		"path=\"/var/lib/x/y\" retry 5/6 done\n",
		"plain diagnostic without a path\n",
	}
	for _, line := range lines {
		if _, err := writer.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	entries := sink.events(t, eventClientStderr)
	if len(entries) != len(lines) {
		t.Fatalf("one entry per line expected, got %d", len(entries))
	}
	for _, entry := range entries {
		line, _ := entry["line"].(string)
		mustNotContain(t, "diagnostic", line, "/srv/private", "someone", "/var/lib", "config.json", "harness.json")
		if entry["level"] != "warn" {
			t.Fatalf("diagnostics are warnings: %+v", entry)
		}
	}
	first, _ := entries[0]["line"].(string)
	mustContain(t, "diagnostic", first, "panic: open", "<path>", "permission denied")
	third, _ := entries[2]["line"].(string)
	mustContain(t, "diagnostic", third, "5/6")
	fourth, _ := entries[3]["line"].(string)
	if fourth != "plain diagnostic without a path" {
		t.Fatalf("a line without a path is kept as it is: %q", fourth)
	}
}

func TestChildDiagnosticsAreBoundedAndSplitAcrossWrites(t *testing.T) {
	sink := &logSink{}
	writer := newLogger(sink.write, nil).stderrWriter()
	_, _ = writer.Write([]byte("first half of a li"))
	_, _ = writer.Write([]byte("ne\n"))
	_, _ = writer.Write([]byte(strings.Repeat("x", 5000) + "\n"))
	entries := sink.events(t, eventClientStderr)
	if len(entries) != 2 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[0]["line"] != "first half of a line" {
		t.Fatalf("a line split across writes is joined: %v", entries[0]["line"])
	}
	if got := len(entries[1]["line"].(string)); got != maxLogFieldBytes {
		t.Fatalf("a long line is cut to %d bytes, got %d", maxLogFieldBytes, got)
	}
}
